package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/mateusz/go-agy-acp-wrapper/internal/agy"
	"github.com/mateusz/go-agy-acp-wrapper/internal/config"
	"github.com/mateusz/go-agy-acp-wrapper/internal/session"
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
}

func NewAgyAgent(cfg *config.Config) *AgyAgent {
	return &AgyAgent{
		cfg:          cfg,
		store:        session.NewStore(),
		runner:       agy.NewNonInteractiveRunner(cfg.AgyBinary, cfg.AgyConfigDir()),
		discoverer:   agy.NewConversationDiscoverer(cfg.AgyConfigDir()),
		promptWriter: agy.NewPromptFileWriter(cfg.TempDir, cfg.PromptThreshold),
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
	sess, err := a.store.Create(params.Cwd)
	if err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	if a.cfg.DefaultModel != "" {
		sess.SetModel(a.cfg.DefaultModel)
	}

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

	sess.AddUserMessage(promptText)

	response, err := a.executeTurn(ctx, sess, promptText)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}

	sess.AddAssistantMessage(response)

	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: params.SessionId,
		Update:    acp.UpdateAgentMessageText(response),
	}); err != nil {
		return acp.PromptResponse{}, fmt.Errorf("send session update: %w", err)
	}

	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *AgyAgent) executeTurn(ctx context.Context, sess *session.Context, promptText string) (string, error) {
	mode := sess.GetMode()
	convID := sess.GetConversationID()
	turnCount := sess.GetTurnCount()

	opts := agy.ExecuteOpts{
		Cwd:       sess.Cwd,
		Model:     sess.GetModel(),
		Timeout:   time.Duration(a.cfg.TimeoutSeconds) * time.Second,
		SkipPerms: true,
	}

	switch {
	case mode == session.ModeFallbackContext:
		return a.executeFallbackTurn(ctx, sess, opts, promptText)

	case convID != "" && turnCount > 1:
		opts.ConversationID = convID
		opts.Prompt = promptText
		if a.promptWriter.NeedsFile(promptText) {
			path, err := a.promptWriter.WritePromptFile(sess.ID, turnCount, promptText)
			if err != nil {
				return "", fmt.Errorf("write prompt file: %w", err)
			}
			opts.PromptFilePath = path
			opts.Prompt = ""
		}

		resp, err := a.runner.Execute(ctx, opts)
		if err != nil {
			slog.Warn("native conversation failed, switching to fallback", "error", err, "sessionId", sess.ID)
			sess.SwitchToFallback()
			return a.executeFallbackTurn(ctx, sess, opts, promptText)
		}
		return resp.Output, nil

	default:
		opts.Prompt = promptText
		if a.promptWriter.NeedsFile(promptText) {
			path, err := a.promptWriter.WritePromptFile(sess.ID, turnCount, promptText)
			if err != nil {
				return "", fmt.Errorf("write prompt file: %w", err)
			}
			opts.PromptFilePath = path
			opts.Prompt = ""
		}

		resp, err := a.runner.Execute(ctx, opts)
		if err != nil {
			return "", err
		}

		if convID == "" {
			a.discoverAndSetConversationID(sess)
		}

		return resp.Output, nil
	}
}

func (a *AgyAgent) executeFallbackTurn(ctx context.Context, sess *session.Context, opts agy.ExecuteOpts, promptText string) (string, error) {
	transcript := sess.GetTranscript()
	turnCount := sess.GetTurnCount()
	if len(transcript) > 0 && transcript[len(transcript)-1].Role == session.RoleUser && transcript[len(transcript)-1].Content == promptText {
		transcript = transcript[:len(transcript)-1]
	}

	contextPath, err := a.promptWriter.WriteContextDump(sess.ID, turnCount, transcript, promptText)
	if err != nil {
		return "", fmt.Errorf("write context dump: %w", err)
	}

	opts.ConversationID = ""
	opts.Prompt = ""
	opts.PromptFilePath = contextPath

	resp, err := a.runner.Execute(ctx, opts)
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
	slog.Info("cancel received", "sessionId", string(params.SessionId))
	return nil
}

func (a *AgyAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	sid := string(params.SessionId)
	a.store.Delete(sid)
	_ = a.promptWriter.CleanupSession(sid)
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

var knownModels = []string{
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"Gemini 3.1 Pro (High)",
	"Gemini 3.1 Flash",
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

	newModel := string(params.ValueId.Value)
	sess.SetModel(newModel)
	slog.Info("model changed", "sessionId", sid, "model", newModel)

	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: a.buildConfigOptions(sess),
	}, nil
}

func (a *AgyAgent) buildConfigOptions(sess *session.Context) []acp.SessionConfigOption {
	currentModel := sess.GetModel()
	if currentModel == "" {
		currentModel = "(default)"
	}

	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(knownModels))
	for _, m := range knownModels {
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(m),
			Name:  m,
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

func (a *AgyAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *AgyAgent) Close() {
	a.store.CloseAll()
	_ = a.promptWriter.CleanupAll()
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
