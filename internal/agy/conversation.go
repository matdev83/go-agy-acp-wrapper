package agy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ConversationDiscoverer struct {
	configDir string
}

func NewConversationDiscoverer(configDir string) *ConversationDiscoverer {
	return &ConversationDiscoverer{configDir: configDir}
}

func (d *ConversationDiscoverer) DiscoverConversationID(cwd string) (string, error) {
	path := filepath.Join(d.configDir, "cache", "last_conversations.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read last_conversations.json: %w", err)
	}

	var convMap map[string]string
	if err := json.Unmarshal(data, &convMap); err != nil {
		return "", fmt.Errorf("parse last_conversations.json: %w", err)
	}

	normalizedCwd := filepath.Clean(cwd)
	if id, ok := convMap[normalizedCwd]; ok {
		return id, nil
	}

	for key, id := range convMap {
		if filepath.Clean(key) == normalizedCwd {
			return id, nil
		}
	}

	return "", fmt.Errorf("no conversation found for cwd %q", cwd)
}

func (d *ConversationDiscoverer) ValidateConversationID(id string) bool {
	brainPath := filepath.Join(d.configDir, "brain", id)
	info, err := os.Stat(brainPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}
