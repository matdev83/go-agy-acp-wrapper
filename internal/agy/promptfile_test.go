package agy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-agy-acp-wrapper/internal/session"
)

func TestPromptFileWriter_NeedsFile(t *testing.T) {
	w := NewPromptFileWriter(100)

	short := strings.Repeat("a", 50)
	if w.NeedsFile(short) {
		t.Fatal("expected short prompt to not need file")
	}

	long := strings.Repeat("b", 200)
	if !w.NeedsFile(long) {
		t.Fatal("expected long prompt to need file")
	}
}

func TestPromptFileWriter_WritePromptFile(t *testing.T) {
	cwd := t.TempDir()
	w := NewPromptFileWriter(100)

	path, err := w.WritePromptFile(cwd, "sess_abc", 1, "hello world prompt")
	if err != nil {
		t.Fatalf("WritePromptFile failed: %v", err)
	}

	expectedDir := filepath.Join(cwd, PromptFileDirName, "sess_abc")
	if !strings.HasPrefix(path, expectedDir) {
		t.Fatalf("expected path under %s, got %s", expectedDir, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading prompt file: %v", err)
	}
	if string(data) != "hello world prompt" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestPromptFileWriter_WriteContextDump(t *testing.T) {
	cwd := t.TempDir()
	w := NewPromptFileWriter(100)

	transcript := []session.Message{
		{Role: session.RoleUser, Content: "First question"},
		{Role: session.RoleAssistant, Content: "First answer"},
	}

	path, err := w.WriteContextDump(cwd, "sess_xyz", 2, transcript, "Follow-up question")
	if err != nil {
		t.Fatalf("WriteContextDump failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading context dump: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# Conversation Context") {
		t.Fatal("expected context header")
	}
	if !strings.Contains(content, "First question") {
		t.Fatal("expected first user message in context")
	}
	if !strings.Contains(content, "First answer") {
		t.Fatal("expected first assistant message in context")
	}
	if !strings.Contains(content, "Follow-up question") {
		t.Fatal("expected new prompt in context")
	}
}

func TestPromptFileWriter_CleanupSession(t *testing.T) {
	cwd := t.TempDir()
	w := NewPromptFileWriter(100)

	_, err := w.WritePromptFile(cwd, "sess_cleanup", 1, "test content")
	if err != nil {
		t.Fatalf("WritePromptFile failed: %v", err)
	}

	dir := filepath.Join(cwd, PromptFileDirName, "sess_cleanup")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("session dir should exist: %v", err)
	}

	if err := w.CleanupSession(cwd, "sess_cleanup"); err != nil {
		t.Fatalf("CleanupSession failed: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected session dir to be removed")
	}
}

func TestPromptFileWriter_CleanupWorkdir(t *testing.T) {
	cwd := t.TempDir()
	w := NewPromptFileWriter(100)

	_, err := w.WritePromptFile(cwd, "sess_cleanup", 1, "test content")
	if err != nil {
		t.Fatalf("WritePromptFile failed: %v", err)
	}

	dir := filepath.Join(cwd, PromptFileDirName)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("workdir temp dir should exist: %v", err)
	}

	if err := w.CleanupWorkdir(cwd); err != nil {
		t.Fatalf("CleanupWorkdir failed: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected workdir temp dir to be removed")
	}
}
