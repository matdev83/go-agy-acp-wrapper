package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	expectedDir := filepath.Join(cwd, PromptFileDirName, w.instanceID, "sess_abc")
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

	dir := filepath.Join(cwd, PromptFileDirName, w.instanceID, "sess_cleanup")
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
	if err := w.CleanupSession(cwd, "sess_cleanup"); err != nil {
		t.Fatalf("CleanupSession failed: %v", err)
	}

	dir := filepath.Join(cwd, PromptFileDirName)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("workdir temp dir should exist: %v", err)
	}

	if err := w.CleanupWorkdir(cwd); err != nil {
		t.Fatalf("CleanupWorkdir failed: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected empty workdir temp dir to be removed")
	}
}

func TestPromptFileWriter_CleanupWorkdirPreservesOtherInstance(t *testing.T) {
	cwd := t.TempDir()
	first := NewPromptFileWriter(100)
	second := NewPromptFileWriter(100)

	if _, err := first.WritePromptFile(cwd, "first", 1, "first prompt"); err != nil {
		t.Fatalf("first instance write failed: %v", err)
	}
	secondPath, err := second.WritePromptFile(cwd, "second", 1, "second prompt")
	if err != nil {
		t.Fatalf("second instance write failed: %v", err)
	}

	if err := first.CleanupSession(cwd, "first"); err != nil {
		t.Fatalf("first instance session cleanup failed: %v", err)
	}
	if err := first.CleanupWorkdir(cwd); err != nil {
		t.Fatalf("first instance workdir cleanup failed: %v", err)
	}

	data, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("first instance removed second instance's prompt: %v", err)
	}
	if string(data) != "second prompt" {
		t.Fatalf("unexpected second prompt content: %q", data)
	}
}

func TestPromptFileWriter_ConcurrentInstancesInDifferentRepos(t *testing.T) {
	writers := []*PromptFileWriter{NewPromptFileWriter(100), NewPromptFileWriter(100)}
	cwds := []string{t.TempDir(), t.TempDir()}

	var wg sync.WaitGroup
	errs := make(chan error, len(writers))
	for instance := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for turn := 1; turn <= 50; turn++ {
				content := fmt.Sprintf("instance %d turn %d", instance, turn)
				path, err := writers[instance].WritePromptFile(cwds[instance], "session", turn, content)
				if err != nil {
					errs <- err
					return
				}
				data, err := os.ReadFile(path)
				if err != nil {
					errs <- fmt.Errorf("instance %d turn %d: read prompt: %w", instance, turn, err)
					return
				}
				if string(data) != content {
					errs <- fmt.Errorf("instance %d turn %d: content=%q, want %q", instance, turn, data, content)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
