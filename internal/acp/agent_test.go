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
		AgyBinary:              "echo",
		HomeDir:                t.TempDir(),
		PromptThreshold:        8000,
		TimeoutSeconds:         30,
		SkipPerms:              true,
		InjectExecutionEnvNote: true,
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
	agent.sleep = func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	}
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

func (stubRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	return stubRunner{}.Execute(ctx, opts)
}

type errorRunner struct {
	err   error
	calls int
}

func (r *errorRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *errorRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	r.calls++
	return nil, r.err
}

type scriptedCall struct {
	resp *agy.Response
	err  error
}

type scriptedRunner struct {
	mu     sync.Mutex
	calls  []agy.ExecuteOpts
	script []scriptedCall
}

func (r *scriptedRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *scriptedRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := len(r.calls)
	r.calls = append(r.calls, opts)
	if idx >= len(r.script) {
		return nil, fmt.Errorf("unexpected extra ExecuteStream call %d", idx)
	}
	return r.script[idx].resp, r.script[idx].err
}

type recordingRunner struct {
	mu    sync.Mutex
	calls []agy.ExecuteOpts
	err   error
}

func (r *recordingRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *recordingRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	r.mu.Lock()
	r.calls = append(r.calls, opts)
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return &agy.Response{Output: "ok", ConversationID: "conv-env-note"}, nil
}

func (r *recordingRunner) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		if call.PromptFilePath != "" {
			out = append(out, "@"+call.PromptFilePath)
			continue
		}
		out = append(out, call.Prompt)
	}
	return out
}

func TestAgyAgent_Prompt_InjectsExecutionEnvNoteOnce(t *testing.T) {
	agent := newTestAgent(t)
	runner := &recordingRunner{err: errors.New("stop before session update")}
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("run the tests")},
	})
	if err == nil {
		t.Fatal("expected runner error")
	}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("follow up")},
	})
	if err == nil {
		t.Fatal("expected runner error")
	}

	prompts := runner.prompts()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 runner calls, got %d", len(prompts))
	}
	wantFirst := executionEnvNote + "\n\nrun the tests"
	if prompts[0] != wantFirst {
		t.Fatalf("first prompt = %q", prompts[0])
	}
	if prompts[1] != "follow up" {
		t.Fatalf("second prompt should not repeat the note, got %q", prompts[1])
	}

	sess, ok := agent.store.Get(string(sessResp.SessionId))
	if !ok {
		t.Fatal("session not found")
	}
	if !sess.EnvNoteInjected() {
		t.Fatal("expected session to remember env-note injection")
	}
	transcript := sess.GetTranscript()
	if len(transcript) < 2 || transcript[0].Content != wantFirst || transcript[1].Content != "follow up" {
		t.Fatalf("unexpected transcript: %+v", transcript)
	}
}

func TestAgyAgent_Prompt_ExecutionEnvNoteOptOut(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.InjectExecutionEnvNote = false
	agent := NewAgyAgent(cfg)
	defer agent.Close()
	runner := &recordingRunner{err: errors.New("stop before session update")}
	agent.runner = runner
	agent.modelCatalog = agy.NewModelCatalogFromIDs("gemini-3.5-flash-high")

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("run the tests")},
	})
	if err == nil {
		t.Fatal("expected runner error")
	}
	prompts := runner.prompts()
	if len(prompts) != 1 || prompts[0] != "run the tests" {
		t.Fatalf("opt-out should leave prompt unchanged, got %#v", prompts)
	}
}

func TestAgyAgent_Prompt_DoesNotDuplicateExistingExecutionEnvNote(t *testing.T) {
	agent := newTestAgent(t)
	runner := &recordingRunner{err: errors.New("stop before session update")}
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	incoming := executionEnvNote + "\n\nrun the tests"
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock(incoming)},
	})
	if err == nil {
		t.Fatal("expected runner error")
	}
	prompts := runner.prompts()
	if len(prompts) != 1 || prompts[0] != incoming {
		t.Fatalf("expected existing note to be preserved, got %#v", prompts)
	}
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
	if runner.calls != config.DefaultQuotaRetryAttempts {
		t.Fatalf("quota failure should retry native conversation, not fallback; got %d calls", runner.calls)
	}
	if sess.GetMode() != session.ModeNativeConversation {
		t.Fatal("quota failure unexpectedly switched session to fallback mode")
	}
}

func TestAgyAgent_Prompt_RetriesQuotaThenContinuesNativeConversation(t *testing.T) {
	agent := newTestAgent(t)
	var waits []time.Duration
	agent.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	runner := &scriptedRunner{script: []scriptedCall{
		{resp: &agy.Response{ConversationID: "conv-123"}, err: &agy.ProcessError{ExitCode: 1, Detail: "status 429: overloaded"}},
		{resp: &agy.Response{Output: "resumed", ConversationID: "conv-123"}},
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

	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("next")},
	})
	if err != nil {
		t.Fatalf("Prompt failed after retry: %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %s", resp.StopReason)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 native calls, got %d", len(runner.calls))
	}
	if runner.calls[0].Prompt == "" || !strings.Contains(runner.calls[0].Prompt, "next") {
		t.Fatalf("first prompt should keep the user text, got %q", runner.calls[0].Prompt)
	}
	if runner.calls[1].Prompt != providerLimitContinuePrompt {
		t.Fatalf("retry prompt = %q, want continue cue", runner.calls[1].Prompt)
	}
	if runner.calls[1].ConversationID != "conv-123" {
		t.Fatalf("retry conversation = %q", runner.calls[1].ConversationID)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("backoff waits = %#v", waits)
	}
}

func TestAgyAgent_Prompt_FirstTurnQuotaRetriesOriginalPromptWithoutConversation(t *testing.T) {
	agent := newTestAgent(t)
	agent.cfg.InjectExecutionEnvNote = false
	runner := &scriptedRunner{script: []scriptedCall{
		{err: &agy.ProcessError{ExitCode: 1, Detail: "code 429"}},
		{resp: &agy.Response{Output: "ok", ConversationID: "conv-new"}},
	}}
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("start work")},
	})
	if err != nil {
		t.Fatalf("Prompt failed after retry: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}
	if runner.calls[0].ConversationID != "" || runner.calls[1].ConversationID != "" {
		t.Fatalf("first-turn retries without a conversation id must not pass --conversation: %#v", runner.calls)
	}
	if runner.calls[0].Prompt != "start work" || runner.calls[1].Prompt != "start work" {
		t.Fatalf("expected original prompt on both attempts, got %#v %#v", runner.calls[0].Prompt, runner.calls[1].Prompt)
	}
}

func TestAgyAgent_Prompt_FirstTurnQuotaContinuesAfterStreamConversationID(t *testing.T) {
	agent := newTestAgent(t)
	agent.cfg.InjectExecutionEnvNote = false
	runner := &scriptedRunner{script: []scriptedCall{
		{resp: &agy.Response{ConversationID: "conv-from-stream"}, err: &agy.ProcessError{ExitCode: 1, Detail: "RESOURCE_EXHAUSTED"}},
		{resp: &agy.Response{Output: "ok", ConversationID: "conv-from-stream"}},
	}}
	agent.runner = runner

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("start work")},
	})
	if err != nil {
		t.Fatalf("Prompt failed after retry: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}
	if runner.calls[0].ConversationID != "" {
		t.Fatalf("first attempt should create a conversation, got %q", runner.calls[0].ConversationID)
	}
	if runner.calls[1].ConversationID != "conv-from-stream" || runner.calls[1].Prompt != providerLimitContinuePrompt {
		t.Fatalf("second attempt = %#v", runner.calls[1])
	}
}

func TestAgyAgent_Prompt_TimeoutDoesNotFallback(t *testing.T) {
	agent := newTestAgent(t)
	runner := &errorRunner{err: &agy.TimeoutError{Timeout: 4 * time.Hour, Detail: "print timeout"}}
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
	if requestErr.Code != timeoutErrorCode || requestErr.Message != "Agy request timed out" {
		t.Fatalf("unexpected request error: %#v", requestErr)
	}
	if runner.calls != 1 {
		t.Fatalf("timeout should not be retried in fallback mode; got %d calls", runner.calls)
	}
	if sess.GetMode() != session.ModeNativeConversation {
		t.Fatal("timeout unexpectedly switched session to fallback mode")
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

func (r *blockingRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
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

func (r *slowCancelRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
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
	wantFirst := executionEnvNote + "\n\nfirst"
	if len(transcript) != 1 {
		t.Fatalf("expected 1 transcript message, got %#v", transcript)
	}
	if transcript[0].Content != wantFirst {
		t.Fatalf("first prompt mismatch\ngot  (%d): %q\nwant (%d): %q", len(transcript[0].Content), transcript[0].Content, len(wantFirst), wantFirst)
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

func (conversationStreamRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	if onEvent != nil {
		onEvent(agy.StreamEvent{Kind: agy.StreamEventText, Text: "streamed"})
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

func TestStreamEventUpdate_MapsToolLifecycle(t *testing.T) {
	start, ok := streamEventUpdate(agy.StreamEvent{
		Kind:     agy.StreamEventToolStart,
		ToolID:   "agy-step-3",
		ToolName: "run_command",
		RawInput: map[string]any{"CommandLine": "git status"},
	})
	if !ok || start.ToolCall == nil {
		t.Fatalf("expected ACP tool call start, got %#v", start)
	}
	if start.ToolCall.ToolCallId != "agy-step-3" || start.ToolCall.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("unexpected tool call start: %#v", start.ToolCall)
	}
	if start.ToolCall.Title != "run_command: git status" {
		t.Fatalf("expected command line in tool title, got %q", start.ToolCall.Title)
	}

	done, ok := streamEventUpdate(agy.StreamEvent{
		Kind:      agy.StreamEventToolUpdate,
		ToolID:    "agy-step-3",
		ToolState: "DONE",
		RawOutput: "clean",
	})
	if !ok || done.ToolCallUpdate == nil {
		t.Fatalf("expected ACP tool call completion, got %#v", done)
	}
	if done.ToolCallUpdate.ToolCallId != "agy-step-3" ||
		done.ToolCallUpdate.Status == nil ||
		*done.ToolCallUpdate.Status != acp.ToolCallStatusCompleted {
		t.Fatalf("unexpected tool call completion: %#v", done.ToolCallUpdate)
	}

	active, ok := streamEventUpdate(agy.StreamEvent{
		Kind:      agy.StreamEventToolUpdate,
		ToolID:    "agy-step-3",
		ToolState: "ACTIVE",
	})
	if !ok || active.ToolCallUpdate == nil || active.ToolCallUpdate.Status == nil ||
		*active.ToolCallUpdate.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("expected in-progress for ACTIVE update, got %#v", active.ToolCallUpdate)
	}

	failed, ok := streamEventUpdate(agy.StreamEvent{
		Kind:      agy.StreamEventToolUpdate,
		ToolID:    "agy-step-3",
		ToolState: "ERROR",
	})
	if !ok || failed.ToolCallUpdate == nil || failed.ToolCallUpdate.Status == nil ||
		*failed.ToolCallUpdate.Status != acp.ToolCallStatusFailed {
		t.Fatalf("expected failed for ERROR update, got %#v", failed.ToolCallUpdate)
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

func (r partialStreamRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	if onEvent != nil && r.streamed != "" {
		onEvent(agy.StreamEvent{Kind: agy.StreamEventText, Text: r.streamed})
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
	onStdout := func(event agy.StreamEvent) {
		mu.Lock()
		emittedChunks = append(emittedChunks, event.Text)
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

type concurrentTestRunner struct {
	started chan struct{}
	release chan struct{}
}

func (b *concurrentTestRunner) Execute(ctx context.Context, opts agy.ExecuteOpts) (*agy.Response, error) {
	return b.ExecuteStream(ctx, opts, nil)
}

func (b *concurrentTestRunner) ExecuteStream(ctx context.Context, opts agy.ExecuteOpts, onEvent func(agy.StreamEvent)) (*agy.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return &agy.Response{Output: "done: " + opts.Prompt}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAgyAgent_ConcurrentPromptsInSameWorkdir(t *testing.T) {
	agent := newTestAgent(t)
	sharedWorkdir := t.TempDir()

	sess1, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: sharedWorkdir})
	if err != nil {
		t.Fatal(err)
	}
	sess2, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: sharedWorkdir})
	if err != nil {
		t.Fatal(err)
	}

	runner := &concurrentTestRunner{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	agent.runner = runner

	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		_, pErr := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sess1.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("prompt 1")},
		})
		errCh <- pErr
	}()

	// Wait until prompt 1 is running inside runner
	<-runner.started

	// Prompt 2 starts in the same workdir while prompt 1 is in-flight
	go func() {
		defer wg.Done()
		_, pErr := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sess2.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("prompt 2")},
		})
		errCh <- pErr
	}()

	// Wait until prompt 2 is also running inside runner
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt 2 was blocked and did not start concurrently in the same workdir")
	}

	// Release both
	close(runner.release)
	wg.Wait()
	close(errCh)

	for pErr := range errCh {
		if pErr != nil {
			t.Fatalf("prompt error: %v", pErr)
		}
	}
}

