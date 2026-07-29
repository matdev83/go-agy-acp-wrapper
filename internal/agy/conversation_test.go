package agy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationDiscoverer_DiscoverConversationID(t *testing.T) {
	configDir := t.TempDir()
	cacheDir := filepath.Join(configDir, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(string(filepath.Separator), "workspace", "project")
	convMap := map[string]string{
		cwd: "abc-123-def-456",
	}
	data, _ := json.Marshal(convMap)
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	d := NewConversationDiscoverer(configDir)
	id, err := d.DiscoverConversationID(cwd)
	if err != nil {
		t.Fatalf("DiscoverConversationID failed: %v", err)
	}
	if id != "abc-123-def-456" {
		t.Fatalf("expected abc-123-def-456, got %q", id)
	}
}

func TestConversationDiscoverer_DiscoverConversationID_NotFound(t *testing.T) {
	configDir := t.TempDir()
	cacheDir := filepath.Join(configDir, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	convMap := map[string]string{
		"/other/path": "abc-123",
	}
	data, _ := json.Marshal(convMap)
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	d := NewConversationDiscoverer(configDir)
	_, err := d.DiscoverConversationID("/workspace/project")
	if err == nil {
		t.Fatal("expected error for missing cwd")
	}
}

func TestConversationDiscoverer_DiscoverConversationID_NoFile(t *testing.T) {
	configDir := t.TempDir()
	d := NewConversationDiscoverer(configDir)
	_, err := d.DiscoverConversationID("/workspace/project")
	if err == nil {
		t.Fatal("expected error when file doesn't exist")
	}
}

func TestConversationDiscoverer_RetriesPartialCacheWrite(t *testing.T) {
	configDir := t.TempDir()
	cacheDir := filepath.Join(configDir, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cacheDir, "last_conversations.json")
	if err := os.WriteFile(path, []byte(`{"incomplete"`), 0644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(string(filepath.Separator), "workspace", "project")
	go func() {
		time.Sleep(50 * time.Millisecond)
		data, _ := json.Marshal(map[string]string{cwd: "stable-conversation"})
		_ = os.WriteFile(path, data, 0644)
	}()

	id, err := NewConversationDiscoverer(configDir).DiscoverConversationID(cwd)
	if err != nil {
		t.Fatalf("DiscoverConversationID did not recover from partial write: %v", err)
	}
	if id != "stable-conversation" {
		t.Fatalf("expected stable conversation, got %q", id)
	}
}

func TestConversationDiscoverer_DiscoverNewRejectsUnchanged(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	cacheDir := filepath.Join(configDir, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{cwd: "old-id"})
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConversationDiscoverer(configDir).DiscoverNewConversationID(context.Background(), cwd, "old-id", time.Now()); err == nil {
		t.Fatal("expected unchanged mapping to be rejected")
	}
}

func TestConversationDiscoverer_DiscoverNewAcceptsCurrentBrain(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	cacheDir := filepath.Join(configDir, "cache")
	brainDir := filepath.Join(configDir, "brain", "new-id")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := os.MkdirAll(brainDir, 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{cwd: "new-id"})
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	id, err := NewConversationDiscoverer(configDir).DiscoverNewConversationID(context.Background(), cwd, "old-id", startedAt)
	if err != nil {
		t.Fatalf("expected new mapping: %v", err)
	}
	if id != "new-id" {
		t.Fatalf("expected new-id, got %q", id)
	}
}

func TestConversationDiscoverer_DiscoverNewRejectsUnsafeID(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../cache", "nested/id", `nested\\id`} {
		t.Run(strings.ReplaceAll(id, "/", "_"), func(t *testing.T) {
			configDir := t.TempDir()
			cwd := t.TempDir()
			cacheDir := filepath.Join(configDir, "cache")
			if err := os.MkdirAll(cacheDir, 0755); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]string{cwd: id})
			if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := NewConversationDiscoverer(configDir).DiscoverNewConversationID(context.Background(), cwd, "old", time.Now()); err == nil {
				t.Fatalf("unsafe conversation ID %q was accepted", id)
			}
		})
	}
}

func TestConversationDiscoverer_ValidateConversationID(t *testing.T) {
	configDir := t.TempDir()
	brainDir := filepath.Join(configDir, "brain", "valid-id")
	if err := os.MkdirAll(brainDir, 0755); err != nil {
		t.Fatal(err)
	}

	d := NewConversationDiscoverer(configDir)

	if !d.ValidateConversationID("valid-id") {
		t.Fatal("expected valid-id to validate")
	}
	if d.ValidateConversationID("nonexistent-id") {
		t.Fatal("expected nonexistent-id to not validate")
	}
}
