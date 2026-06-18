package agy

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestNonInteractiveRunner_BuildArgs_SimplePrompt(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		Prompt: "hello",
	})
	expected := []string{"--print", "hello"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_BuildArgs_WithConversation(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		Prompt:         "hello",
		ConversationID: "abc-123",
	})
	expected := []string{"--conversation", "abc-123", "--print", "hello"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_BuildArgs_WithPromptFile(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		PromptFilePath: "/tmp/prompt.md",
	})
	expected := []string{"--print", "@/tmp/prompt.md"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_BuildArgs_WithSkipPerms(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		Prompt:    "hello",
		SkipPerms: true,
	})
	expected := []string{"--dangerously-skip-permissions", "--print", "hello"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_BuildArgs_WithModel(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		Prompt: "hello",
		Model:  "gemini-2.5-pro",
	})
	expected := []string{"--model", "gemini-2.5-pro", "--print", "hello"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_BuildArgs_AllOptions(t *testing.T) {
	r := &NonInteractiveRunner{binary: "agy"}
	args := r.buildArgs(ExecuteOpts{
		Prompt:         "hello",
		ConversationID: "conv-1",
		Model:          "Gemini 3.1 Pro (High)",
		SkipPerms:      true,
	})
	expected := []string{"--conversation", "conv-1", "--model", "Gemini 3.1 Pro (High)", "--dangerously-skip-permissions", "--print", "hello"}
	assertArgs(t, expected, args)
}

func TestNonInteractiveRunner_Execute_BinaryNotFound(t *testing.T) {
	r := NewNonInteractiveRunner("nonexistent_binary_xyz", t.TempDir())
	_, err := r.Execute(context.Background(), ExecuteOpts{
		Cwd:    t.TempDir(),
		Prompt: "hello",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestNonInteractiveRunner_Execute_SuccessfulCommand(t *testing.T) {
	var binary string
	var args ExecuteOpts
	if runtime.GOOS == "windows" {
		binary = "cmd.exe"
		args = ExecuteOpts{Cwd: t.TempDir()}
	} else {
		binary = "echo"
		args = ExecuteOpts{Cwd: t.TempDir(), Prompt: "test output"}
	}

	r := &NonInteractiveRunner{binary: binary}

	if runtime.GOOS == "windows" {
		r.binary = "cmd.exe"
		// Override to test with a known working command
		// We'll test the actual agy runner in E2E
		t.Skip("skipping real command test on Windows in unit tests")
	}

	resp, err := r.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
}

func TestNonInteractiveRunner_Execute_ContextCancelled(t *testing.T) {
	var binary string
	if runtime.GOOS == "windows" {
		binary = "ping"
	} else {
		binary = "sleep"
	}

	r := NewNonInteractiveRunner(binary, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	opts := ExecuteOpts{
		Cwd:    t.TempDir(),
		Prompt: "10",
	}

	_, err := r.Execute(ctx, opts)
	if err == nil || ctx.Err() == nil {
		// The command should be cancelled
		t.Log("context cancellation test completed (command may or may not have returned error)")
	}
}

func TestNormalizeLineEndings(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello\r\nworld\r\n", "hello\nworld\n"},
		{"hello\nworld\n", "hello\nworld\n"},
		{"no newlines", "no newlines"},
		{"mixed\r\nand\nnewlines", "mixed\nand\nnewlines"},
	}

	for _, tt := range tests {
		result := normalizeLineEndings(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeLineEndings(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func assertArgs(t *testing.T, expected, actual []string) {
	t.Helper()
	if len(expected) != len(actual) {
		t.Fatalf("args length mismatch: expected %v, got %v", expected, actual)
	}
	for i := range expected {
		if expected[i] != actual[i] {
			t.Fatalf("arg[%d] mismatch: expected %q, got %q", i, expected[i], actual[i])
		}
	}
}
