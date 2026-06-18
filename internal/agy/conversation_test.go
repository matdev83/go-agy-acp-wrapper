package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
