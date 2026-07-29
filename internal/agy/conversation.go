package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ConversationDiscoverer struct {
	configDir string
}

const (
	conversationReadAttempts = 10
	conversationReadDelay    = 20 * time.Millisecond
)

var errConversationNotFound = errors.New("conversation not found")

func NewConversationDiscoverer(configDir string) *ConversationDiscoverer {
	return &ConversationDiscoverer{configDir: configDir}
}

func (d *ConversationDiscoverer) DiscoverConversationID(cwd string) (string, error) {
	return d.DiscoverConversationIDContext(context.Background(), cwd)
}

func (d *ConversationDiscoverer) DiscoverConversationIDContext(ctx context.Context, cwd string) (string, error) {
	convMap, err := d.readConversationMap(ctx)
	if err != nil {
		return "", err
	}
	return lookupConversationID(convMap, cwd)
}

// SnapshotConversationID returns the pre-invocation mapping. A missing cache or
// cwd entry is a valid empty snapshot; malformed or unreadable state is not.
func (d *ConversationDiscoverer) SnapshotConversationID(ctx context.Context, cwd string) (string, error) {
	convMap, err := d.readConversationMap(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id, err := lookupConversationID(convMap, cwd)
	if errors.Is(err, errConversationNotFound) {
		return "", nil
	}
	return id, err
}

// DiscoverNewConversationID accepts only a changed mapping whose brain directory
// was created or updated during this invocation.
func (d *ConversationDiscoverer) DiscoverNewConversationID(ctx context.Context, cwd, previousID string, startedAt time.Time) (string, error) {
	convMap, err := d.readConversationMap(ctx)
	if err != nil {
		return "", err
	}
	id, err := lookupConversationID(convMap, cwd)
	if err != nil {
		return "", err
	}
	if id == previousID {
		return "", fmt.Errorf("conversation for cwd %q unchanged (%q)", cwd, id)
	}
	if id == "" || id == "." || id == ".." || filepath.IsAbs(id) || filepath.Clean(id) != id || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return "", fmt.Errorf("discovered conversation ID %q is not a safe single path component", id)
	}
	brainRoot := filepath.Join(d.configDir, "brain")
	brainPath := filepath.Join(brainRoot, id)
	relative, err := filepath.Rel(brainRoot, brainPath)
	if err != nil || relative != id {
		return "", fmt.Errorf("discovered conversation ID %q escapes brain directory", id)
	}
	info, err := os.Stat(brainPath)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("discovered conversation %q has no brain directory", id)
	}
	if info.ModTime().Before(startedAt.Add(-time.Second)) {
		return "", fmt.Errorf("discovered conversation %q predates this invocation", id)
	}
	return id, nil
}

func (d *ConversationDiscoverer) readConversationMap(ctx context.Context) (map[string]string, error) {
	path := filepath.Join(d.configDir, "cache", "last_conversations.json")
	var lastErr error
	for attempt := 0; attempt < conversationReadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			var convMap map[string]string
			if err := json.Unmarshal(data, &convMap); err == nil {
				return convMap, nil
			}
			lastErr = err
		} else {
			lastErr = err
		}
		if attempt+1 >= conversationReadAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(conversationReadDelay):
		}
	}
	return nil, fmt.Errorf("read stable last_conversations.json: %w", lastErr)
}

func lookupConversationID(convMap map[string]string, cwd string) (string, error) {
	normalizedCwd := filepath.Clean(cwd)
	if id, ok := convMap[normalizedCwd]; ok {
		return id, nil
	}
	for key, id := range convMap {
		if filepath.Clean(key) == normalizedCwd {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w for cwd %q", errConversationNotFound, cwd)
}

func (d *ConversationDiscoverer) ValidateConversationID(id string) bool {
	brainPath := filepath.Join(d.configDir, "brain", id)
	info, err := os.Stat(brainPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}
