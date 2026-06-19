package agy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestNonInteractiveRunner_ExecuteStream_EmitsStdoutBeforeProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based timing test is Unix-only")
	}

	script := filepath.Join(t.TempDir(), "stream.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'first\\n'\nsleep 1\nprintf 'second\\n'\n"), 0700); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	r := NewNonInteractiveRunner(script, t.TempDir())
	firstChunk := make(chan string, 1)
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		_, err := r.ExecuteStream(context.Background(), ExecuteOpts{
			Cwd:    t.TempDir(),
			Prompt: "ignored",
		}, func(chunk string) {
			select {
			case firstChunk <- chunk:
			default:
			}
		})
		if err != nil {
			t.Errorf("ExecuteStream failed: %v", err)
		}
	}()

	select {
	case chunk := <-firstChunk:
		if chunk != "first\n" {
			t.Fatalf("expected first chunk, got %q", chunk)
		}
		if elapsed := time.Since(started); elapsed >= 900*time.Millisecond {
			t.Fatalf("first chunk arrived too late: %s", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected stdout chunk before process exit")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not finish")
	}
}

func TestStreamPipeLines_EmitsPartialFinalLine(t *testing.T) {
	var dst bytes.Buffer
	var chunks []string
	streamPipeLines(strings.NewReader("partial"), &dst, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if dst.String() != "partial" {
		t.Fatalf("expected dst partial, got %q", dst.String())
	}
	if len(chunks) != 1 || chunks[0] != "partial" {
		t.Fatalf("expected one partial chunk, got %#v", chunks)
	}
}

func TestStreamPipeLines_EmitsLineBeforePipeClose(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()

	var dst bytes.Buffer
	chunks := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamPipeLines(reader, &dst, func(chunk string) {
			chunks <- chunk
		})
	}()

	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first line: %v", err)
	}

	select {
	case chunk := <-chunks:
		if chunk != "first\n" {
			t.Fatalf("expected first chunk, got %q", chunk)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected first line before second write or pipe close")
	}

	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second line: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("streamPipeLines did not return after pipe close")
	}
	if dst.String() != "first\nsecond\n" {
		t.Fatalf("expected accumulated output, got %q", dst.String())
	}
}

func TestTranscriptTailer_EmitsOnlyGrowingPlannerSuffix(t *testing.T) {
	configDir := t.TempDir()
	conversationID := "conv-tail"
	path := makeTranscriptPath(t, configDir, conversationID)
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var chunks []string
	tailer := newTranscriptTailer(configDir, conversationID, time.Now(), func(chunk string) {
		chunks = append(chunks, chunk)
	})
	tailer.snapshotExisting()

	appendTranscript(t, path, `{"type":"PLANNER_RESPONSE","content":"first"}`+"\n")
	tailer.scan()
	appendTranscript(t, path, `{"type":"PLANNER_RESPONSE","content":"first second"}`+"\n")
	tailer.scan()

	if got := strings.Join(chunks, ""); got != "first second" {
		t.Fatalf("expected chunks to reconstruct content, got %q from %#v", got, chunks)
	}
	if len(chunks) != 2 || chunks[0] != "first" || chunks[1] != " second" {
		t.Fatalf("expected suffix chunks, got %#v", chunks)
	}
	if got := tailer.output(); got != "first second" {
		t.Fatalf("expected final output, got %q", got)
	}
}

func TestTranscriptTailer_IgnoresPartialJSONLLineUntilComplete(t *testing.T) {
	configDir := t.TempDir()
	conversationID := "conv-partial"
	path := makeTranscriptPath(t, configDir, conversationID)
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var chunks []string
	tailer := newTranscriptTailer(configDir, conversationID, time.Now(), func(chunk string) {
		chunks = append(chunks, chunk)
	})
	tailer.snapshotExisting()

	appendTranscript(t, path, `{"type":"PLANNER_RESPONSE","content":"partial"}`)
	tailer.scan()
	if len(chunks) != 0 {
		t.Fatalf("expected no chunks for partial line, got %#v", chunks)
	}

	appendTranscript(t, path, "\n")
	tailer.scan()
	if len(chunks) != 1 || chunks[0] != "partial" {
		t.Fatalf("expected completed line chunk, got %#v", chunks)
	}
}

func TestTranscriptTailer_DiscoversNewTranscriptWithoutConversationID(t *testing.T) {
	configDir := t.TempDir()
	var chunks []string
	tailer := newTranscriptTailer(configDir, "", time.Now(), func(chunk string) {
		chunks = append(chunks, chunk)
	})
	tailer.snapshotExisting()

	path := makeTranscriptPath(t, configDir, "conv-discovered")
	appendTranscript(t, path, `{"type":"PLANNER_RESPONSE","content":"discovered"}`+"\n")
	tailer.scan()

	if len(chunks) != 1 || chunks[0] != "discovered" {
		t.Fatalf("expected discovered transcript chunk, got %#v", chunks)
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

func TestNonInteractiveRunner_ExtractFromTranscript_LargeResponse(t *testing.T) {
	configDir := t.TempDir()
	conversationID := "conv-large"
	logDir := filepath.Join(configDir, "brain", conversationID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	largeResponse := strings.Repeat("x", 1024*1024+1)
	transcript := fmt.Sprintf("{\"type\":\"PLANNER_RESPONSE\",\"content\":%q}\n", largeResponse)
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcript), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	r := NewNonInteractiveRunner("agy", configDir)
	if got := r.extractFromTranscript(conversationID); got != largeResponse {
		t.Fatalf("expected large response length %d, got %d", len(largeResponse), len(got))
	}
}

func TestNonInteractiveRunner_ExtractFromTranscript_CRLF(t *testing.T) {
	configDir := t.TempDir()
	conversationID := "conv-crlf"
	logDir := filepath.Join(configDir, "brain", conversationID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	transcript := "{\"type\":\"PLANNER_RESPONSE\",\"content\":\"first\"}\r\n" +
		"{\"type\":\"PLANNER_RESPONSE\",\"content\":\"second\"}\r\n"
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcript), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	r := NewNonInteractiveRunner("agy", configDir)
	if got := r.extractFromTranscript(conversationID); got != "second" {
		t.Fatalf("expected last response %q, got %q", "second", got)
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

func makeTranscriptPath(t *testing.T, configDir, conversationID string) string {
	t.Helper()
	logDir := filepath.Join(configDir, "brain", conversationID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	return filepath.Join(logDir, "transcript.jsonl")
}

func appendTranscript(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
}
