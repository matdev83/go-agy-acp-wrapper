package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	modelCatalog *agy.ModelCatalog
	promptWriter *agy.PromptFileWriter
	coordinator  *agy.RepoCoordinator
	mu           sync.Mutex
	workdirs     map[string]int
	cancels      map[string]activePrompt
	finalizers   map[string]*sessionFinalizer
	closed       bool
}

type activePrompt struct {
	token  *struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

type sessionFinalizer struct {
	done chan struct{}
	err  error
}

func NewAgyAgent(cfg *config.Config) *AgyAgent {
	return &AgyAgent{
		cfg:          cfg,
		store:        session.NewStore(),
		runner:       agy.NewNonInteractiveRunner(cfg.AgyBinary, cfg.AgyConfigDir()),
		discoverer:   agy.NewConversationDiscoverer(cfg.AgyConfigDir()),
		modelCatalog: agy.NewModelCatalog(cfg.AgyBinary),
		promptWriter: agy.NewPromptFileWriter(cfg.PromptThreshold),
		coordinator:  agy.NewRepoCoordinator(cfg.AgyConfigDir()),
		workdirs:     make(map[string]int),
		cancels:      make(map[string]activePrompt),
		finalizers:   make(map[string]*sessionFinalizer),
	}
}

func (a *AgyAgent) SetAgentConnection(conn *acp.AgentSideConnection) {
	a.conn = conn
}

func (a *AgyAgent) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	slog.Info("initialize received", "protocolVersion", params.ProtocolVersion)
	if err := a.modelCatalog.EnsureLoaded(ctx); err != nil {
		return acp.InitializeResponse{}, fmt.Errorf("load models: %w", err)
	}
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
	if err := a.modelCatalog.EnsureLoaded(ctx); err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("load models: %w", err)
	}

	canonicalCwd, err := canonicalSessionCwd(params.Cwd)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return acp.NewSessionResponse{}, fmt.Errorf("agent is closed")
	}
	sess, err := a.store.Create(canonicalCwd)
	if err == nil {
		a.workdirs[workdirKey(canonicalCwd)]++
	}
	a.mu.Unlock()
	if err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	modelID, err := a.modelCatalog.ResolveCanonical(a.cfg.DefaultModel)
	if err != nil {
		slog.Warn("invalid default model configured, using catalog default", "model", a.cfg.DefaultModel, "error", err)
		modelID = a.modelCatalog.DefaultModelID()
	}
	sess.SetModel(modelID)

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
	token, _, ok := a.startPrompt(sid, cancel)
	if !ok {
		cancel()
		return acp.PromptResponse{}, fmt.Errorf("session %s already has an active prompt", sid)
	}
	defer a.finishPrompt(sid, token)

	unlock, err := a.coordinator.Lock(promptCtx, sess.Cwd)
	if err != nil {
		if promptCtx.Err() == context.Canceled {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, fmt.Errorf("coordinate repository prompt: %w", err)
	}
	defer func() {
		if err := unlock(); err != nil {
			slog.Warn("unlock repository prompt failed", "sessionId", sid, "error", err)
		}
	}()

	if sess.IsClosed() {
		return acp.PromptResponse{}, fmt.Errorf("session %s is closed", sid)
	}
	if a.cfg.InjectExecutionEnvNote {
		promptText = applyExecutionEnvNote(promptText, sess.EnvNoteInjected())
		sess.MarkEnvNoteInjected()
	}
	sess.AddUserMessage(promptText)

	var streamedMu sync.Mutex
	var streamedBuf strings.Builder
	response, err := a.executeTurn(promptCtx, sess, promptText, func(event agy.StreamEvent) {
		update, ok := streamEventUpdate(event)
		if !ok {
			return
		}
		if err := a.conn.SessionUpdate(promptCtx, acp.SessionNotification{
			SessionId: params.SessionId,
			Update:    update,
		}); err != nil {
			slog.Warn("send streamed session update failed", "sessionId", sid, "error", err)
			return
		}

		if event.Kind == agy.StreamEventText {
			streamedMu.Lock()
			streamedBuf.WriteString(event.Text)
			streamedMu.Unlock()
		}
	})
	if err != nil {
		if promptCtx.Err() == context.Canceled {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, promptRequestError(err)
	}

	sess.AddAssistantMessage(response)

	streamedMu.Lock()
	streamedText := streamedBuf.String()
	streamedMu.Unlock()

	tail := response
	if streamedText != "" && strings.HasPrefix(response, streamedText) {
		tail = response[len(streamedText):]
	}

	if tail != "" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: params.SessionId,
			Update:    acp.UpdateAgentMessageText(tail),
		}); err != nil {
			return acp.PromptResponse{}, fmt.Errorf("send session update: %w", err)
		}
	}

	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *AgyAgent) executeTurn(ctx context.Context, sess *session.Context, promptText string, onEvent func(agy.StreamEvent)) (string, error) {
	mode := sess.GetMode()
	convID := sess.GetConversationID()
	turnCount := sess.GetTurnCount()

	nativeModel, err := a.modelCatalog.ResolveNative(sess.GetModel(), sess.GetReasoningEffort())
	if err != nil {
		return "", err
	}

	opts := agy.ExecuteOpts{
		Cwd:       sess.Cwd,
		Model:     nativeModel,
		Timeout:   time.Duration(a.cfg.TimeoutSeconds) * time.Second,
		SkipPerms: a.cfg.SkipPerms,
	}

	switch {
	case mode == session.ModeFallbackContext:
		return a.executeFallbackTurn(ctx, sess, opts, promptText, onEvent)

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

		resp, err := a.runner.ExecuteStream(ctx, opts, onEvent)
		out, err := turnOutput(resp, err, opts)
		if err != nil {
			if !agy.ShouldFallback(err) {
				return "", err
			}
			slog.Warn("native conversation failed, switching to fallback", "error", err, "sessionId", sess.ID)
			sess.SwitchToFallback()
			return a.executeFallbackTurn(ctx, sess, opts, promptText, onEvent)
		}
		return out, nil

	default:
		previousID, err := a.discoverer.SnapshotConversationID(ctx, sess.Cwd)
		if err != nil {
			slog.Warn("conversation cache snapshot failed, using fallback context", "error", err, "sessionId", sess.ID)
			sess.SwitchToFallback()
			return a.executeFallbackTurn(ctx, sess, opts, promptText, onEvent)
		}
		startedAt := time.Now()
		opts.Prompt = promptText
		if a.promptWriter.NeedsFile(promptText) {
			path, err := a.promptWriter.WritePromptFile(sess.Cwd, sess.ID, turnCount, promptText)
			if err != nil {
				return "", fmt.Errorf("write prompt file: %w", err)
			}
			opts.PromptFilePath = path
			opts.Prompt = ""
		}

		resp, err := a.runner.ExecuteStream(ctx, opts, onEvent)
		out, err := turnOutput(resp, err, opts)
		if err != nil {
			return "", err
		}

		if convID == "" {
			if resp.ConversationID != "" {
				sess.SetConversationID(resp.ConversationID)
				slog.Info("conversation ID received from agy stream", "conversationId", resp.ConversationID, "sessionId", sess.ID)
			} else if !a.discoverAndSetConversationID(ctx, sess, previousID, startedAt) {
				sess.SwitchToFallback()
			}
		}

		return out, nil
	}
}

func (a *AgyAgent) executeFallbackTurn(ctx context.Context, sess *session.Context, opts agy.ExecuteOpts, promptText string, onEvent func(agy.StreamEvent)) (string, error) {
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

	resp, err := a.runner.ExecuteStream(ctx, opts, onEvent)
	return turnOutput(resp, err, opts)
}

func turnOutput(resp *agy.Response, err error, opts agy.ExecuteOpts) (string, error) {
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("agy returned an empty response")
	}
	if resp.TimedOut {
		return "", &agy.TimeoutError{Timeout: opts.Timeout, Detail: resp.Output}
	}
	return resp.Output, nil
}

const maxToolTitleCommandChars = 120

func toolCallTitle(name string, rawInput any) string {
	title := strings.TrimSpace(name)
	if title == "" {
		title = "AGY tool"
	}
	command := commandLineFromInput(rawInput)
	if command == "" {
		return title
	}
	if len(command) > maxToolTitleCommandChars {
		command = command[:maxToolTitleCommandChars] + "…"
	}
	return title + ": " + command
}

func commandLineFromInput(rawInput any) string {
	fields, ok := rawInput.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"CommandLine", "commandLine", "command", "cmd"} {
		value, ok := fields[key].(string)
		if !ok {
			continue
		}
		if command := strings.TrimSpace(value); command != "" {
			return command
		}
	}
	return ""
}

func toolCallUpdateStatus(state string) acp.ToolCallStatus {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "DONE", "COMPLETED", "COMPLETE", "SUCCESS":
		return acp.ToolCallStatusCompleted
	case "ERROR", "FAILED", "FAIL", "CANCELLED", "CANCELED", "REJECTED":
		return acp.ToolCallStatusFailed
	default:
		return acp.ToolCallStatusInProgress
	}
}

func streamEventUpdate(event agy.StreamEvent) (acp.SessionUpdate, bool) {
	switch event.Kind {
	case agy.StreamEventText:
		if event.Text == "" {
			return acp.SessionUpdate{}, false
		}
		return acp.UpdateAgentMessageText(event.Text), true
	case agy.StreamEventToolStart:
		title := toolCallTitle(event.ToolName, event.RawInput)
		opts := []acp.ToolCallStartOpt{acp.WithStartStatus(acp.ToolCallStatusInProgress)}
		if event.RawInput != nil {
			opts = append(opts, acp.WithStartRawInput(event.RawInput))
		}
		return acp.StartToolCall(acp.ToolCallId(event.ToolID), title, opts...), true
	case agy.StreamEventToolUpdate:
		opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(toolCallUpdateStatus(event.ToolState))}
		if event.RawOutput != nil {
			opts = append(opts, acp.WithUpdateRawOutput(event.RawOutput))
		}
		return acp.UpdateToolCall(acp.ToolCallId(event.ToolID), opts...), true
	default:
		return acp.SessionUpdate{}, false
	}
}

func (a *AgyAgent) discoverAndSetConversationID(ctx context.Context, sess *session.Context, previousID string, startedAt time.Time) bool {
	id, err := a.discoverer.DiscoverNewConversationID(ctx, sess.Cwd, previousID, startedAt)
	if err != nil {
		slog.Warn("conversation attribution failed, switching to fallback", "error", err, "sessionId", sess.ID)
		return false
	}
	sess.SetConversationID(id)
	slog.Info("conversation ID discovered", "conversationId", id, "sessionId", sess.ID)
	return true
}

func (a *AgyAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	sid := string(params.SessionId)
	if cancel, _ := a.cancelPrompt(sid); cancel != nil {
		cancel()
		slog.Info("cancelled active prompt", "sessionId", sid)
		return nil
	}
	slog.Info("cancel received with no active prompt", "sessionId", sid)
	return nil
}

func (a *AgyAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	sid := string(params.SessionId)
	finalizer := a.beginSessionFinalizer(sid)
	if finalizer == nil {
		return acp.CloseSessionResponse{}, nil
	}
	select {
	case <-finalizer.done:
		a.mu.Lock()
		err := finalizer.err
		a.mu.Unlock()
		if err != nil {
			return acp.CloseSessionResponse{}, fmt.Errorf("finalize session %s: %w", sid, err)
		}
		slog.Info("session closed", "sessionId", sid)
		return acp.CloseSessionResponse{}, nil
	case <-ctx.Done():
		// Finalization intentionally continues after the request deadline.
		return acp.CloseSessionResponse{}, ctx.Err()
	}
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

const (
	modelConfigID           = "model"
	reasoningEffortConfigID = "reasoning_effort"
)

func (a *AgyAgent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if params.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unsupported config option type")
	}

	configID := string(params.ValueId.ConfigId)
	if configID != modelConfigID && configID != reasoningEffortConfigID {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unknown config option: %s", params.ValueId.ConfigId)
	}

	sid := string(params.ValueId.SessionId)
	sess, ok := a.store.Get(sid)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("session %s not found", sid)
	}

	value := string(params.ValueId.Value)
	switch configID {
	case modelConfigID:
		newModel, err := a.modelCatalog.ResolveCanonical(value)
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		if _, err := a.modelCatalog.ResolveNative(newModel, sess.GetReasoningEffort()); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		sess.SetModel(newModel)
		slog.Info("model changed", "sessionId", sid, "model", newModel)
	case reasoningEffortConfigID:
		if _, err := a.modelCatalog.ResolveNative(sess.GetModel(), value); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		sess.SetReasoningEffort(value)
		slog.Info("reasoning effort changed", "sessionId", sid, "reasoningEffort", value)
	}

	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: a.buildConfigOptions(sess),
	}, nil
}

func (a *AgyAgent) buildConfigOptions(sess *session.Context) []acp.SessionConfigOption {
	currentModel := sess.GetModel()
	if !a.modelCatalog.Contains(currentModel) {
		currentModel = a.modelCatalog.DefaultModelID()
	}

	profiles := a.modelCatalog.Profiles()
	modelOptions := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(profiles))
	for _, profile := range profiles {
		modelOptions = append(modelOptions, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(profile.CanonicalID),
			Name:  profile.DisplayName,
		})
	}

	modelCategory := acp.SessionConfigOptionCategoryModel
	result := []acp.SessionConfigOption{{
		Select: &acp.SessionConfigOptionSelect{
			Id:           acp.SessionConfigId(modelConfigID),
			Name:         "Model",
			Type:         "select",
			Category:     &modelCategory,
			CurrentValue: acp.SessionConfigValueId(currentModel),
			Options:      acp.SessionConfigSelectOptions{Ungrouped: &modelOptions},
		},
	}}

	efforts := a.modelCatalog.SupportedEfforts(currentModel)
	if len(efforts) == 0 {
		return result
	}
	currentEffort := sess.GetReasoningEffort()
	if currentEffort == "" {
		for _, profile := range profiles {
			if profile.CanonicalID == currentModel {
				currentEffort = profile.DefaultEffort
				break
			}
		}
	}
	effortOptions := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(efforts))
	for _, effort := range efforts {
		effortOptions = append(effortOptions, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(effort),
			Name:  strings.ToUpper(effort[:1]) + effort[1:],
		})
	}
	result = append(result, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id:           acp.SessionConfigId(reasoningEffortConfigID),
		Name:         "Reasoning effort",
		Type:         "select",
		CurrentValue: acp.SessionConfigValueId(currentEffort),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &effortOptions},
	}})
	return result
}

func (a *AgyAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *AgyAgent) Close() {
	a.mu.Lock()
	if a.closed {
		finalizers := a.finalizerSnapshotLocked()
		a.mu.Unlock()
		waitFinalizers(finalizers)
		return
	}
	a.closed = true
	sessions := a.store.CloseAll()
	type pendingFinalizer struct {
		sess      *session.Context
		prompt    activePrompt
		finalizer *sessionFinalizer
	}
	pending := make([]pendingFinalizer, 0, len(sessions))
	for _, sess := range sessions {
		if _, exists := a.finalizers[sess.ID]; exists {
			continue
		}
		finalizer := &sessionFinalizer{done: make(chan struct{})}
		a.finalizers[sess.ID] = finalizer
		pending = append(pending, pendingFinalizer{sess: sess, prompt: a.cancels[sess.ID], finalizer: finalizer})
	}
	finalizers := a.finalizerSnapshotLocked()
	a.mu.Unlock()

	for _, item := range pending {
		a.runSessionFinalizer(item.sess, item.prompt, item.finalizer)
	}
	waitFinalizers(finalizers)
	workdirs := a.closeWorkdirs()
	for _, cwd := range workdirs {
		if err := retryCleanup(func() error { return a.promptWriter.CleanupWorkdir(cwd) }); err != nil {
			slog.Error("workdir cleanup failed", "cwd", cwd, "error", err)
		}
	}
}

func waitFinalizers(finalizers []*sessionFinalizer) {
	for _, finalizer := range finalizers {
		<-finalizer.done
		if finalizer.err != nil {
			slog.Error("session finalization failed", "error", finalizer.err)
		}
	}
}

func (a *AgyAgent) beginSessionFinalizer(sessionID string) *sessionFinalizer {
	a.mu.Lock()
	if finalizer, ok := a.finalizers[sessionID]; ok {
		a.mu.Unlock()
		return finalizer
	}
	sess, ok := a.store.Get(sessionID)
	if !ok {
		a.mu.Unlock()
		return nil
	}
	sess.Close()
	a.store.Delete(sessionID)
	finalizer := &sessionFinalizer{done: make(chan struct{})}
	a.finalizers[sessionID] = finalizer
	prompt := a.cancels[sessionID]
	a.mu.Unlock()
	a.runSessionFinalizer(sess, prompt, finalizer)
	return finalizer
}

func (a *AgyAgent) runSessionFinalizer(sess *session.Context, prompt activePrompt, finalizer *sessionFinalizer) {
	if prompt.cancel != nil {
		prompt.cancel()
	}
	go func() {
		if prompt.done != nil {
			<-prompt.done
		}
		err := retryCleanup(func() error {
			return a.promptWriter.CleanupSession(sess.Cwd, sess.ID)
		})
		a.unregisterWorkdir(sess.Cwd)
		a.mu.Lock()
		finalizer.err = err
		delete(a.finalizers, sess.ID)
		close(finalizer.done)
		a.mu.Unlock()
	}()
}

func retryCleanup(cleanup func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = cleanup(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return err
}

func (a *AgyAgent) finalizerSnapshotLocked() []*sessionFinalizer {
	result := make([]*sessionFinalizer, 0, len(a.finalizers))
	for _, finalizer := range a.finalizers {
		result = append(result, finalizer)
	}
	return result
}

func (a *AgyAgent) startPrompt(sessionID string, cancel context.CancelFunc) (*struct{}, <-chan struct{}, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if current, ok := a.cancels[sessionID]; ok {
		return nil, current.done, false
	}
	token := &struct{}{}
	done := make(chan struct{})
	a.cancels[sessionID] = activePrompt{token: token, cancel: cancel, done: done}
	return token, done, true
}

func (a *AgyAgent) finishPrompt(sessionID string, token *struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if current, ok := a.cancels[sessionID]; ok && current.token == token {
		delete(a.cancels, sessionID)
		close(current.done)
	}
}

func (a *AgyAgent) cancelPrompt(sessionID string) (context.CancelFunc, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	prompt, ok := a.cancels[sessionID]
	if !ok {
		return nil, nil
	}
	return prompt.cancel, prompt.done
}

func canonicalSessionCwd(cwd string) (string, error) {
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func workdirKey(cwd string) string {
	key := filepath.Clean(cwd)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func (a *AgyAgent) unregisterWorkdir(cwd string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := workdirKey(cwd)
	if a.workdirs[key] <= 1 {
		delete(a.workdirs, key)
		return true
	}
	a.workdirs[key]--
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

const (
	providerLimitErrorCode = -32003
	timeoutErrorCode       = -32004
)

func promptRequestError(err error) error {
	message := "Agy request failed"
	code := -32001
	if agy.IsProviderLimitError(err) {
		message = "Model provider quota or rate limit exceeded"
		code = providerLimitErrorCode
	} else if agy.IsTimeoutError(err) {
		message = "Agy request timed out"
		code = timeoutErrorCode
	}
	return &acp.RequestError{
		Code:    code,
		Message: message,
		Data:    map[string]any{"error": err.Error()},
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
