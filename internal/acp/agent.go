package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/matdev83/go-agy-acp-wrapper/internal/agy"
	"github.com/matdev83/go-agy-acp-wrapper/internal/config"
	"github.com/matdev83/go-agy-acp-wrapper/internal/session"
)

var _ acp.Agent = (*AgyAgent)(nil)

type AgyAgent struct {
	conn         *acp.AgentSideConnection
	cfg          *config.Config
	store        *session.Store
	runner       agy.Runner
	discoverer   *agy.ConversationDiscoverer
	promptWriter *agy.PromptFileWriter
	mu           sync.Mutex
	workdirs     map[string]int
	cancels      map[string]activePrompt
}

type activePrompt struct {
	token  *struct{}
	cancel context.CancelFunc
}

func NewAgyAgent(cfg *config.Config) *AgyAgent {
	return &AgyAgent{
		cfg:          cfg,
		store:        session.NewStore(),
		runner:       agy.NewNonInteractiveRunner(cfg.AgyBinary, cfg.AgyConfigDir()),
		discoverer:   agy.NewConversationDiscoverer(cfg.AgyConfigDir()),
		promptWriter: agy.NewPromptFileWriter(cfg.PromptThreshold),
		workdirs:     make(map[string]int),
		cancels:      make(map[string]activePrompt),
	}
}

func (a *AgyAgent) SetAgentConnection(conn *acp.AgentSideConnection) {
	a.conn = conn
}

func (a *AgyAgent) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	slog.Info("initialize received", "protocolVersion", params.ProtocolVersion)
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: false,
			SessionCapabilities: acp.SessionCapabilities{
				Close: &acp.SessionCloseCapabilities{},
			},
		},
	}, nil
}

func (a *AgyAgent) Authenticate(ctx context.Context, params acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *AgyAgent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.registerWorkdir(params.Cwd)

	sess, err := a.store.Create(params.Cwd)
	if err != nil {
		a.unregisterWorkdir(params.Cwd)
		return acp.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	sess.SetModel(normalizeModel(a.cfg.DefaultModel))

	slog.Info("new session created", "sessionId", sess.ID, "cwd", params.Cwd, "model", sess.GetModel())

	resp := acp.NewSessionResponse{
		SessionId:     acp.SessionId(sess.ID),
		ConfigOptions: a.buildConfigOptions(sess),
	}
	return resp, nil
}

func (a *AgyAgent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sid := string(params.SessionId)
	sess, ok := a.store.Get(sid)
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("session %s not found", sid)
	}

	promptText := extractPromptText(params.Prompt)
	if promptText == "" {
		return acp.PromptResponse{}, fmt.Errorf("empty prompt")
	}

	promptCtx, cancel := context.WithCancel(ctx)
	token, ok := a.startPrompt(sid, cancel)
	if !ok {
		cancel()
		return acp.PromptResponse{}, fmt.Errorf("session %s already has an active prompt", sid)
	}
	defer a.finishPrompt(sid, token)

	sess.AddUserMessage(promptText)

	var streamed atomic.Bool
	response, err := a.executeTurn(promptCtx, sess, promptText, func(chunk string) {
		if chunk == "" {
			return
		}
		streamed.Store(true)
		if err := a.conn.SessionUpdate(promptCtx, acp.SessionNotification{
			SessionId: params.SessionId,
			Update:    acp.UpdateAgentMessageText(chunk),
		}); err != nil {
			slog.Warn("send streamed session update failed", "sessionId", sid, "error", err)
		}
	})
	if err != nil {
		if promptCtx.Err() == context.Canceled {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}

	sess.AddAssistantMessage(response)

	if !streamed.Load() && response != "" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: params.SessionId,
			Update:    acp.UpdateAgentMessageText(response),
		}); err != nil {
			return acp.PromptResponse{}, fmt.Errorf("send session update: %w", err)
		}
	}

	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *AgyAgent) executeTurn(ctx context.Context, sess *session.Context, promptText string, onStdout func(string)) (string, error) {
	mode := sess.GetMode()
	convID := sess.GetConversationID()
	turnCount := sess.GetTurnCount()

	opts := agy.ExecuteOpts{
		Cwd:       sess.Cwd,
		Model:     agyModelLabel(sess.GetModel()),
		Timeout:   time.Duration(a.cfg.TimeoutSeconds) * time.Second,
		SkipPerms: a.cfg.SkipPerms,
	}

	switch {
	case mode == session.ModeFallbackContext:
		return a.executeFallbackTurn(ctx, sess, opts, promptText, onStdout)

	case convID != "" && turnCount > 1:
		opts.ConversationID = convID
		opts.Prompt = promptText
		if a.promptWriter.NeedsFile(promptText) {
			path, err := a.promptWriter.WritePromptFile(sess.Cwd, sess.ID, turnCount, promptText)
			if err != nil {
				return "", fmt.Errorf("write prompt file: %w", err)
			}
			opts.PromptFilePath = path
			opts.Prompt = ""
		}

		resp, err := a.runner.ExecuteStream(ctx, opts, onStdout)
		if err != nil {
			slog.Warn("native conversation failed, switching to fallback", "error", err, "sessionId", sess.ID)
			sess.SwitchToFallback()
			return a.executeFallbackTurn(ctx, sess, opts, promptText, onStdout)
		}
		return resp.Output, nil

	default:
		opts.Prompt = promptText
		if a.promptWriter.NeedsFile(promptText) {
			path, err := a.promptWriter.WritePromptFile(sess.Cwd, sess.ID, turnCount, promptText)
			if err != nil {
				return "", fmt.Errorf("write prompt file: %w", err)
			}
			opts.PromptFilePath = path
			opts.Prompt = ""
		}

		resp, err := a.runner.ExecuteStream(ctx, opts, onStdout)
		if err != nil {
			return "", err
		}

		if convID == "" {
			a.discoverAndSetConversationID(sess)
		}

		return resp.Output, nil
	}
}

func (a *AgyAgent) executeFallbackTurn(ctx context.Context, sess *session.Context, opts agy.ExecuteOpts, promptText string, onStdout func(string)) (string, error) {
	transcript := sess.GetTranscript()
	turnCount := sess.GetTurnCount()
	if len(transcript) > 0 && transcript[len(transcript)-1].Role == session.RoleUser && transcript[len(transcript)-1].Content == promptText {
		transcript = transcript[:len(transcript)-1]
	}

	contextPath, err := a.promptWriter.WriteContextDump(sess.Cwd, sess.ID, turnCount, transcript, promptText)
	if err != nil {
		return "", fmt.Errorf("write context dump: %w", err)
	}

	opts.ConversationID = ""
	opts.Prompt = ""
	opts.PromptFilePath = contextPath

	resp, err := a.runner.ExecuteStream(ctx, opts, onStdout)
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

func (a *AgyAgent) discoverAndSetConversationID(sess *session.Context) {
	id, err := a.discoverer.DiscoverConversationID(sess.Cwd)
	if err != nil {
		slog.Debug("could not discover conversation ID", "error", err, "sessionId", sess.ID)
		return
	}
	if !a.discoverer.ValidateConversationID(id) {
		slog.Debug("discovered conversation ID invalid", "id", id, "sessionId", sess.ID)
		return
	}
	sess.SetConversationID(id)
	slog.Info("conversation ID discovered", "conversationId", id, "sessionId", sess.ID)
}

func (a *AgyAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	sid := string(params.SessionId)
	if cancel := a.cancelPrompt(sid); cancel != nil {
		cancel()
		slog.Info("cancelled active prompt", "sessionId", sid)
		return nil
	}
	slog.Info("cancel received with no active prompt", "sessionId", sid)
	return nil
}

func (a *AgyAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	sid := string(params.SessionId)
	if cancel := a.cancelPrompt(sid); cancel != nil {
		cancel()
	}
	sess, ok := a.store.Get(sid)
	a.store.Delete(sid)
	if ok {
		_ = a.promptWriter.CleanupSession(sess.Cwd, sid)
		if a.unregisterWorkdir(sess.Cwd) {
			_ = a.promptWriter.CleanupWorkdir(sess.Cwd)
		}
	}
	slog.Info("session closed", "sessionId", sid)
	return acp.CloseSessionResponse{}, nil
}

func (a *AgyAgent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *AgyAgent) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *AgyAgent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

const modelConfigID = "model"
const defaultModel = "google/gemini-3.5-flash-high"

type modelOption struct {
	Slug     string
	Name     string
	AgyLabel string
}

var knownModels = []modelOption{
	{Slug: defaultModel, Name: "Gemini 3.5 Flash (High)", AgyLabel: "Gemini 3.5 Flash (High)"},
	{Slug: "google/gemini-3.5-flash-medium", Name: "Gemini 3.5 Flash (Medium)", AgyLabel: "Gemini 3.5 Flash (Medium)"},
	{Slug: "google/gemini-3.5-flash-low", Name: "Gemini 3.5 Flash (Low)", AgyLabel: "Gemini 3.5 Flash (Low)"},
	{Slug: "google/gemini-3.1-pro", Name: "Gemini 3.1 Pro (High)", AgyLabel: "Gemini 3.1 Pro (High)"},
	{Slug: "anthropic/claude-sonnet-4.6-thinking", Name: "Claude Sonnet 4.6 (Thinking)", AgyLabel: "Claude Sonnet 4.6 (Thinking)"},
	{Slug: "anthropic/claude-opus-4.6-thinking", Name: "Claude Opus 4.6 (Thinking)", AgyLabel: "Claude Opus 4.6 (Thinking)"},
}

var modelAliases = map[string]string{
	"gemini-3.5-flash-high":             "google/gemini-3.5-flash-high",
	"gemini-3.5-flash-medium":           "google/gemini-3.5-flash-medium",
	"gemini-3.5-flash-low":              "google/gemini-3.5-flash-low",
	"gemini-3.1-pro":                    "google/gemini-3.1-pro",
	"gemini-3.1-pro-high":               "google/gemini-3.1-pro",
	"google/gemini-3.1-pro-high":        "google/gemini-3.1-pro",
	"claude-sonnet-4.6-thinking":        "anthropic/claude-sonnet-4.6-thinking",
	"claude-sonnet-4.6":                 "anthropic/claude-sonnet-4.6-thinking",
	"claude-opus-4.6-thinking":          "anthropic/claude-opus-4.6-thinking",
	"claude-opus-4.6":                   "anthropic/claude-opus-4.6-thinking",
	"google/claude-sonnet-4.6-thinking": "anthropic/claude-sonnet-4.6-thinking",
	"google/claude-sonnet-4.6":          "anthropic/claude-sonnet-4.6-thinking",
	"google/claude-opus-4.6-thinking":   "anthropic/claude-opus-4.6-thinking",
	"google/claude-opus-4.6":            "anthropic/claude-opus-4.6-thinking",
	"anthropic/claude-sonnet-4.6":       "anthropic/claude-sonnet-4.6-thinking",
	"anthropic/claude-opus-4.6":         "anthropic/claude-opus-4.6-thinking",
}

func (a *AgyAgent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if params.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unsupported config option type")
	}

	if string(params.ValueId.ConfigId) != modelConfigID {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unknown config option: %s", params.ValueId.ConfigId)
	}

	sid := string(params.ValueId.SessionId)
	sess, ok := a.store.Get(sid)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("session %s not found", sid)
	}

	newModel := normalizeModel(string(params.ValueId.Value))
	sess.SetModel(newModel)
	slog.Info("model changed", "sessionId", sid, "model", newModel)

	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: a.buildConfigOptions(sess),
	}, nil
}

func (a *AgyAgent) buildConfigOptions(sess *session.Context) []acp.SessionConfigOption {
	currentModel := normalizeModel(sess.GetModel())

	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(knownModels))
	for _, model := range knownModels {
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(model.Slug),
			Name:  model.Name,
		})
	}

	category := acp.SessionConfigOptionCategoryModel
	return []acp.SessionConfigOption{
		{
			Select: &acp.SessionConfigOptionSelect{
				Id:           acp.SessionConfigId(modelConfigID),
				Name:         "Model",
				Type:         "select",
				Category:     &category,
				CurrentValue: acp.SessionConfigValueId(currentModel),
				Options: acp.SessionConfigSelectOptions{
					Ungrouped: &options,
				},
			},
		},
	}
}

func normalizeModel(model string) string {
	if alias, ok := modelAliases[model]; ok {
		return alias
	}
	for _, known := range knownModels {
		if model == known.Slug || model == known.AgyLabel || model == known.Name {
			return known.Slug
		}
	}
	return defaultModel
}

func agyModelLabel(model string) string {
	model = normalizeModel(model)
	for _, known := range knownModels {
		if model == known.Slug {
			return known.AgyLabel
		}
	}
	return agyModelLabel(defaultModel)
}

func (a *AgyAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *AgyAgent) Close() {
	cancels := a.closePrompts()
	for _, cancel := range cancels {
		cancel()
	}
	workdirs := a.closeWorkdirs()
	for _, cwd := range workdirs {
		_ = a.promptWriter.CleanupWorkdir(cwd)
	}
	a.store.CloseAll()
}

func (a *AgyAgent) startPrompt(sessionID string, cancel context.CancelFunc) (*struct{}, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.cancels[sessionID]; ok {
		return nil, false
	}
	token := &struct{}{}
	a.cancels[sessionID] = activePrompt{token: token, cancel: cancel}
	return token, true
}

func (a *AgyAgent) finishPrompt(sessionID string, token *struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if current, ok := a.cancels[sessionID]; ok && current.token == token {
		delete(a.cancels, sessionID)
	}
}

func (a *AgyAgent) cancelPrompt(sessionID string) context.CancelFunc {
	a.mu.Lock()
	defer a.mu.Unlock()

	prompt, ok := a.cancels[sessionID]
	if !ok {
		return nil
	}
	return prompt.cancel
}

func (a *AgyAgent) closePrompts() []context.CancelFunc {
	a.mu.Lock()
	defer a.mu.Unlock()

	cancels := make([]context.CancelFunc, 0, len(a.cancels))
	for _, prompt := range a.cancels {
		cancels = append(cancels, prompt.cancel)
	}
	a.cancels = make(map[string]activePrompt)
	return cancels
}

func (a *AgyAgent) registerWorkdir(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.workdirs[cwd]++
}

func (a *AgyAgent) unregisterWorkdir(cwd string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.workdirs[cwd] <= 1 {
		delete(a.workdirs, cwd)
		return true
	}
	a.workdirs[cwd]--
	return false
}

func (a *AgyAgent) closeWorkdirs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	workdirs := make([]string, 0, len(a.workdirs))
	for cwd := range a.workdirs {
		workdirs = append(workdirs, cwd)
	}
	a.workdirs = make(map[string]int)
	return workdirs
}

func Serve(ctx context.Context, cfg *config.Config, input io.Reader, output io.Writer) error {
	agent := NewAgyAgent(cfg)
	defer agent.Close()

	conn := acp.NewAgentSideConnection(agent, output, input)
	conn.SetLogger(slog.Default())
	agent.SetAgentConnection(conn)

	select {
	case <-ctx.Done():
		return nil
	case <-conn.Done():
		return nil
	}
}

func extractPromptText(blocks []acp.ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return joinNonEmpty(parts)
}

func joinNonEmpty(parts []string) string {
	var result []string
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return ""
	}
	return strings.Join(result, "\n\n")
}
