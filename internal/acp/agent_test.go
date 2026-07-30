package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/matdev83/go-agy-acp-wrapper/internal/agy"
	"github.com/matdev83/go-agy-acp-wrapper/internal/config"
	"github.com/matdev83/go-agy-acp-wrapper/internal/session"
)

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		AgyBinary:       "echo",
		HomeDir:         t.TempDir(),
		PromptThreshold: 8000,
		TimeoutSeconds:  30,
		SkipPerms:       true,
	}
}

const testDefaultModel = "google/gemini-3.5-flash"

func newTestAgent(t *testing.T) *AgyAgent {
	t.Helper()
	agent := NewAgyAgent(newTestConfig(t))
	agent.modelCatalog = agy.NewModelCatalogFromIDs(
		"gemini-3.5-flash-high",
		"gemini-3.5-flash-medium",
		"gemini-3.5-flash-low",
		"gemini-3.1-pro-high",
		"claude-sonnet-4-6",
		"claude-opus-4-6-thinking",
	)
	t.Cleanup(agent.Close)
	return agent
}

func TestAgyAgent_Initialize(t *testing.T) {
	agent := newTestAgent(t)

	resp, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	if resp.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("expected protocol version %d, got %d", acp.ProtocolVersionNumber, resp.ProtocolVersion)
	}
}

func TestAgyAgent_NewSession(t *testing.T) {
	agent := newTestAgent(t)

	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	if resp.SessionId == "" {
		t.Fatal("expected non-empty session ID")
	}
	sess, ok := agent.store.Get(string(resp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	if got := sess.GetModel(); got != testDefaultModel {
		t.Fatalf("expected default model %q, got %q", testDefaultModel, got)
	}
}

func TestAgyAgent_NewSession_InvalidConfiguredModelFallsBackToDefault(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DefaultModel = "gemini-3.5-flash"
	agent := NewAgyAgent(cfg)
	agent.modelCatalog = agy.NewModelCatalogFromIDs("gemini-3.5-flash-high")
	t.Cleanup(agent.Close)

	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	sess, ok := agent.store.Get(string(resp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	if got := sess.GetModel(); got != testDefaultModel {
		t.Fatalf("expected default model %q, got %q", testDefaultModel, got)
	}
}

func TestAgyAgent_SetSessionConfigOption_InvalidModelReturnsError(t *testing.T) {
	agent := newTestAgent(t)

	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	_, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: resp.SessionId,
			ConfigId:  acp.SessionConfigId(modelConfigID),
			Value:     acp.SessionConfigValueId("gemini-3.5-flash"),
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid model")
	}
	sess, ok := agent.store.Get(string(resp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	if got := sess.GetModel(); got != testDefaultModel {
		t.Fatalf("expected session model unchanged %q, got %q", testDefaultModel, got)
	}
}

func TestAgyAgent_SetReasoningEffortSelectsNativeVariant(t *testing.T) {
	agent := newTestAgent(t)
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: acp.SessionId(resp.SessionId),
			ConfigId:  acp.SessionConfigId(reasoningEffortConfigID),
			Value:     acp.SessionConfigValueId("low"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := agent.store.Get(string(resp.SessionId))
	if got := sess.GetReasoningEffort(); got != "low" {
		t.Fatalf("reasoning effort = %q", got)
	}
	if got, err := agent.modelCatalog.ResolveNative(sess.GetModel(), sess.GetReasoningEffort()); err != nil || got != "gemini-3.5-flash-low" {
		t.Fatalf("native model = %q, %v", got, err)
	}
}

func TestAgyAgent_BuildConfigOptions_ExposesModelIDs(t *testing.T) {
	agent := newTestAgent(t)
	sess, err := agent.store.Create(t.TempDir())
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	sess.SetModel("google/gemini-3.5-flash")

	options := agent.buildConfigOptions(sess)
	if len(options) != 2 || options[0].Select == nil || options[1].Select == nil {
		t.Fatalf("expected model and reasoning select config options, got %#v", options)
	}
	selectOption := options[0].Select
	if selectOption.CurrentValue != "google/gemini-3.5-flash" {
		t.Fatalf("expected current value model id, got %q", selectOption.CurrentValue)
	}
	if selectOption.Options.Ungrouped == nil {
		t.Fatal("expected ungrouped model options")
	}
	for _, option := range *selectOption.Options.Ungrouped {
		if strings.Contains(string(option.Value), " ") {
			t.Fatalf("expected raw model id value, got %q", option.Value)
		}
		if !strings.Contains(string(option.Value), "/") {
			t.Fatalf("expected canonical provider-prefixed model id, got %q", option.Value)
		}
	}
}

func TestAgyAgent_CloseSession(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	cwd := t.TempDir()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: cwd,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	path, err := agent.promptWriter.WritePromptFile(cwd, string(sessResp.SessionId), 1, "stale")
	if err != nil {
		t.Fatalf("WritePromptFile failed: %v", err)
	}

	_, err = agent.CloseSession(context.Background(), acp.CloseSessionRequest{
		SessionId: sessResp.SessionId,
	})
	if err != nil {
		t.Fatalf("CloseSession failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected session prompt file to be removed")
	}
}

func TestAgyAgent_Close_CleansWorkdirFiles(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	cwd := t.TempDir()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	path, err := agent.promptWriter.WritePromptFile(cwd, string(sessResp.SessionId), 1, "stale")
	if err != nil {
		t.Fatalf("WritePromptFile failed: %v", err)
	}

	agent.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected prompt file to be removed on agent close")
	}
}

func TestAgyAgent_Prompt_SessionNotFound(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()

	_, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: "nonexistent",
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestAgyAgent_Prompt_EmptyPrompt(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{},
	})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestAgyAgent_UnsupportedMethods(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()

	_, err := agent.ListSessions(context.Background(), acp.ListSessionsRequest{})
	if err == nil {
		t.Fatal("expected error for ListSessions")
	}

	_, err = agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{})
	if err == nil {
		t.Fatal("expected error for ResumeSession")
	}

	_, err = agent.Logout(context.Background(), acp.LogoutRequest{})
	if err == nil {
		t.Fatal("expected error for Logout")
	}
}

func TestExtractPromptText(t *testing.T) {
	tests := []struct {
		name     string
		blocks   []acp.ContentBlock
		expected string
	}{
		{
			name:     "single text block",
			blocks:   []acp.ContentBlock{acp.TextBlock("hello world")},
			expected: "hello world",
		},
		{
			name:     "empty blocks",
			blocks:   []acp.ContentBlock{},
			expected: "",
		},
		{
			name: "multiple text blocks joined",
			blocks: []acp.ContentBlock{
				acp.TextBlock("first"),
				acp.TextBlock("second"),
			},
			expected: "first\n\nsecond",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractPromptText(tt.blocks)
			if result != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

type stubRunner struct{}

func (stubRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return &agy.Response{Output: "ok"}, nil
}

func (stubRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	return stubRunner{}.Execute(ctx, opts)
}

type errorRunner struct {
	err   error
	calls int
}

func (r *errorRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *errorRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	r.calls++
	return nil, r.err
}

func TestAgyAgent_Prompt_ReturnsDescriptiveQuotaRequestError(t *testing.T) {
	agent := newTestAgent(t)
	runner := &errorRunner{err: &agy.ProcessError{
		ExitCode: 1,
		Detail:   "RESOURCE_EXHAUSTED: Gemini quota exceeded for this account",
	}}
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	sess, ok := agent.store.Get(string(sessResp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	sess.SetConversationID("conv-123")
	sess.AddUserMessage("previous")
	sess.AddAssistantMessage("answer")

	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("next")},
	})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("expected ACP request error, got %T: %v", err, err)
	}
	if requestErr.Code != -32003 || requestErr.Message != "Model provider quota or rate limit exceeded" {
		t.Fatalf("unexpected request error: %#v", requestErr)
	}
	if !strings.Contains(fmt.Sprint(requestErr.Data), "Gemini quota exceeded") {
		t.Fatalf("expected provider detail in request error data: %#v", requestErr.Data)
	}
	if runner.calls != 1 {
		t.Fatalf("quota failure should not be retried in fallback mode; got %d calls", runner.calls)
	}
	if sess.GetMode() != session.ModeNativeConversation {
		t.Fatal("quota failure unexpectedly switched session to fallback mode")
	}
}

type blockingRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (r *blockingRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *blockingRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.cancelled)
	return nil, ctx.Err()
}

type slowCancelRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newSlowCancelRunner() *slowCancelRunner {
	return &slowCancelRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (r *slowCancelRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *slowCancelRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.cancelled)
	<-r.release
	return nil, ctx.Err()
}

func TestAgyAgent_Cancel_CancelsActivePrompt(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := newBlockingRunner()
	agent.runner = runner
	cwd := t.TempDir()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	done := make(chan acp.PromptResponse, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		done <- resp
		errs <- err
	}()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}

	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: sessResp.SessionId}); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	select {
	case <-runner.cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner was not cancelled")
	}

	select {
	case resp := <-done:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("expected cancelled stop reason, got %q", resp.StopReason)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt did not finish after cancellation")
	}
	if err := <-errs; err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
}

func TestAgyAgent_Prompt_RejectsConcurrentPromptWithoutTranscriptMutation(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := newBlockingRunner()
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("first")},
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}

	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("second")},
	})
	if err == nil || !strings.Contains(err.Error(), "already has an active prompt") {
		t.Fatalf("expected active prompt error, got %v", err)
	}

	sess, ok := agent.store.Get(string(sessResp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	transcript := sess.GetTranscript()
	if len(transcript) != 1 || transcript[0].Content != "first" {
		t.Fatalf("expected only first prompt in transcript, got %#v", transcript)
	}

	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: sessResp.SessionId}); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first prompt did not finish")
	}
}

func TestAgyAgent_Cancel_BlocksNewPromptUntilOldPromptFinishes(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := newSlowCancelRunner()
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("first")},
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: sessResp.SessionId}); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	select {
	case <-runner.cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner was not cancelled")
	}

	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("second")},
	})
	if err == nil || !strings.Contains(err.Error(), "already has an active prompt") {
		t.Fatalf("expected active prompt error while cancelled prompt unwinds, got %v", err)
	}

	close(runner.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first prompt did not finish")
	}
}

func TestAgyAgent_CloseSessionWaitsForPromptBeforeCleanup(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := newSlowCancelRunner()
	agent.runner = runner
	cwd := t.TempDir()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	path, err := agent.promptWriter.WritePromptFile(cwd, string(sessResp.SessionId), 1, "owned by active prompt")
	if err != nil {
		t.Fatal(err)
	}

	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		_, _ = agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("first")},
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}

	closeDone := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: sessResp.SessionId})
		closeDone <- err
	}()
	select {
	case <-runner.cancelled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel runner")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prompt file removed while runner was still active: %v", err)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before runner stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(runner.release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("CloseSession failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after runner stopped")
	}
	<-promptDone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("prompt file remained after close completed")
	}
}

func TestAgyAgent_CloseSessionTimeoutStillFinalizes(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := newSlowCancelRunner()
	agent.runner = runner
	cwd := t.TempDir()

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	path, err := agent.promptWriter.WritePromptFile(cwd, string(sessResp.SessionId), 1, "active")
	if err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		_, _ = agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("first")},
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := agent.CloseSession(closeCtx, acp.CloseSessionRequest{SessionId: sessResp.SessionId}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected close deadline, got %v", err)
	}
	if _, ok := agent.store.Get(string(sessResp.SessionId)); ok {
		t.Fatal("closing session remained publicly accessible")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prompt file removed before runner stopped: %v", err)
	}

	close(runner.release)
	select {
	case <-promptDone:
	case <-time.After(time.Second):
		t.Fatal("prompt did not unwind")
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background finalizer did not remove prompt file")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgyAgent_AttributionFailureSwitchesToFallback(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	agent.runner = stubRunner{}
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := agent.store.Get(string(resp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}

	if _, err := agent.executeTurn(context.Background(), sess, "test prompt", nil); err != nil {
		t.Fatalf("executeTurn failed: %v", err)
	}
	if sess.GetMode() != session.ModeFallbackContext {
		t.Fatal("expected attribution failure to select fallback mode")
	}
}

type conversationStreamRunner struct{}

func (conversationStreamRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return conversationStreamRunner{}.ExecuteStream(ctx, opts, nil)
}

func (conversationStreamRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	if onStdout != nil {
		onStdout("streamed")
	}
	return &agy.Response{Output: "streamed", ConversationID: "conv-from-stream"}, nil
}

func TestAgyAgent_FirstTurnUsesConversationIDFromAgyStream(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	agent.runner = conversationStreamRunner{}

	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := agent.store.Get(string(resp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}

	if _, err := agent.executeTurn(context.Background(), sess, "first prompt", nil); err != nil {
		t.Fatalf("executeTurn failed: %v", err)
	}
	if got := sess.GetConversationID(); got != "conv-from-stream" {
		t.Fatalf("expected stream conversation ID, got %q", got)
	}
	if sess.GetMode() != session.ModeNativeConversation {
		t.Fatal("stream conversation ID unexpectedly switched session to fallback")
	}
}

func TestAgyAgent_NewSessionCanonicalizesCwdAliases(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	cwd := t.TempDir()
	child := filepath.Join(cwd, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(child, "..")

	first, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: alias})
	if err != nil {
		t.Fatal(err)
	}
	firstSession, _ := agent.store.Get(string(first.SessionId))
	secondSession, _ := agent.store.Get(string(second.SessionId))
	if firstSession.Cwd != secondSession.Cwd {
		t.Fatalf("cwd aliases were not canonicalized: %q != %q", firstSession.Cwd, secondSession.Cwd)
	}
}

func TestAgyAgent_ExecuteFallbackTurn_DoesNotDuplicateCurrentPrompt(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	agent.runner = stubRunner{}

	sess, err := agent.store.Create(t.TempDir())
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	sess.AddUserMessage("First question")
	sess.AddAssistantMessage("First answer")
	sess.AddUserMessage("Follow-up question")

	_, err = agent.executeFallbackTurn(context.Background(), sess, agy.ExecuteOpts{}, "Follow-up question", nil)
	if err != nil {
		t.Fatalf("executeFallbackTurn failed: %v", err)
	}

	path := agent.promptWriterTestContextPath(sess.Cwd, sess.ID, sess.GetTurnCount())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading context dump: %v", err)
	}
	if count := strings.Count(string(data), "Follow-up question"); count != 1 {
		t.Fatalf("expected current prompt once, got %d in %q", count, string(data))
	}
}

func (a *AgyAgent) promptWriterTestContextPath(cwd, sessionID string, turnCount int) string {
	instanceDir, err := a.promptWriter.InstanceDir(cwd)
	if err != nil {
		return ""
	}
	return filepath.Join(instanceDir, sessionID, fmt.Sprintf("context_%d.md", turnCount))
}

type partialStreamRunner struct {
	streamed string
	full     string
}

func (r partialStreamRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return &agy.Response{Output: r.full}, nil
}

func (r partialStreamRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onStdout func(string)) (*agy.Response, error) {
	if onStdout != nil && r.streamed != "" {
		onStdout(r.streamed)
	}
	return &agy.Response{Output: r.full}, nil
}

func TestAgyAgent_Prompt_FlushesUnsentTailAtTurnCompletion(t *testing.T) {
	agent := newTestAgent(t)

	sess, err := agent.store.Create(t.TempDir())
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	sess.SetConversationID("conv-123")
	sess.AddUserMessage("First message")
	sess.AddAssistantMessage("First answer")

	runner := partialStreamRunner{
		streamed: "Part 1 streamed ",
		full:     "Part 1 streamed Part 2 tail delivered",
	}
	agent.runner = runner

	var emittedChunks []string
	var mu sync.Mutex
	onStdout := func(chunk string) {
		mu.Lock()
		emittedChunks = append(emittedChunks, chunk)
		mu.Unlock()
	}

	resp, err := agent.executeTurn(context.Background(), sess, "Second question", onStdout)
	if err != nil {
		t.Fatalf("executeTurn failed: %v", err)
	}
	if resp != runner.full {
		t.Fatalf("expected full response %q, got %q", runner.full, resp)
	}
	if len(emittedChunks) != 1 || emittedChunks[0] != runner.streamed {
		t.Fatalf("expected streamed chunk %q, got %#v", runner.streamed, emittedChunks)
	}
}
