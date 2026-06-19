package acp

import (
	"context"
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

func TestAgyAgent_Initialize(t *testing.T) {
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()

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
	cfg := newTestConfig(t)
	agent := NewAgyAgent(cfg)
	defer agent.Close()

	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	if resp.SessionId == "" {
		t.Fatal("expected non-empty session ID")
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
	if _, err := os.Stat(filepath.Join(cwd, agy.PromptFileDirName)); !os.IsNotExist(err) {
		t.Fatal("expected workdir prompt dir to be removed after last session closes")
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
	return filepath.Join(cwd, agy.PromptFileDirName, sessionID, fmt.Sprintf("context_%d.md", turnCount))
}
