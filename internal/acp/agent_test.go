package acp

import (
	"context"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/mateusz/go-agy-acp-wrapper/internal/config"
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

	sessResp, _ := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})

	_, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{
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

	sessResp, _ := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
	})

	_, err := agent.Prompt(context.Background(), acp.PromptRequest{
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
			name: "multiple text blocks uses first",
			blocks: []acp.ContentBlock{
				acp.TextBlock("first"),
				acp.TextBlock("second"),
			},
			expected: "first",
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
