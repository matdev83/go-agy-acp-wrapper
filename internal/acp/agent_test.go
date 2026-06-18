package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/matdev83/go-agy-acp-wrapper/internal/agy"
	"github.com/matdev83/go-agy-acp-wrapper/internal/config"
)

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		AgyBinary:       "echo",
		HomeDir:         t.TempDir(),
		TempDir:         t.TempDir(),
		PromptThreshold: 8000,
		TimeoutSeconds:  30,
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

	sessResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	_, err = agent.CloseSession(context.Background(), acp.CloseSessionRequest{
		SessionId: sessResp.SessionId,
	})
	if err != nil {
		t.Fatalf("CloseSession failed: %v", err)
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

	_, err = agent.executeFallbackTurn(context.Background(), sess, agy.ExecuteOpts{}, "Follow-up question")
	if err != nil {
		t.Fatalf("executeFallbackTurn failed: %v", err)
	}

	path := agent.promptWriterTestContextPath(sess.ID, sess.GetTurnCount())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading context dump: %v", err)
	}
	if count := strings.Count(string(data), "Follow-up question"); count != 1 {
		t.Fatalf("expected current prompt once, got %d in %q", count, string(data))
	}
}

func (a *AgyAgent) promptWriterTestContextPath(sessionID string, turnCount int) string {
	return filepath.Join(a.cfg.TempDir, sessionID, fmt.Sprintf("context_%d.md", turnCount))
}
