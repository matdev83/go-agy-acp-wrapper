package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matdev83/go-agy-acp-wrapper/internal/session"
)

type PromptFileWriter struct {
	baseDir   string
	threshold int
}

func NewPromptFileWriter(baseDir string, threshold int) *PromptFileWriter {
	return &PromptFileWriter{
		baseDir:   baseDir,
		threshold: threshold,
	}
}

func (w *PromptFileWriter) NeedsFile(prompt string) bool {
	return len(prompt) > w.threshold
}

func (w *PromptFileWriter) WritePromptFile(sessionID string, turnCount int, prompt string) (string, error) {
	dir := filepath.Join(w.baseDir, sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create prompt dir: %w", err)
	}

	filename := filepath.Join(dir, fmt.Sprintf("turn_%d.md", turnCount))
	if err := os.WriteFile(filename, []byte(prompt), 0600); err != nil {
		return "", fmt.Errorf("write prompt file: %w", err)
	}
	return filename, nil
}

func (w *PromptFileWriter) WriteContextDump(sessionID string, turnCount int, transcript []session.Message, newPrompt string) (string, error) {
	dir := filepath.Join(w.baseDir, sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create context dir: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Conversation Context\n\n")
	sb.WriteString("You are continuing a multi-turn conversation. Below is the full conversation history.\n")
	sb.WriteString("Please respond ONLY to the latest user message at the end. Do NOT repeat previous responses.\n\n")
	sb.WriteString("---\n\n")

	for _, msg := range transcript {
		switch msg.Role {
		case session.RoleUser:
			sb.WriteString("## User\n\n")
		case session.RoleAssistant:
			sb.WriteString("## Assistant\n\n")
		}
		sb.WriteString(msg.Content)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## User\n\n")
	sb.WriteString(newPrompt)
	sb.WriteString("\n")

	filename := filepath.Join(dir, fmt.Sprintf("context_%d.md", turnCount))
	if err := os.WriteFile(filename, []byte(sb.String()), 0600); err != nil {
		return "", fmt.Errorf("write context dump: %w", err)
	}
	return filename, nil
}

func (w *PromptFileWriter) CleanupSession(sessionID string) error {
	dir := filepath.Join(w.baseDir, sessionID)
	return os.RemoveAll(dir)
}

func (w *PromptFileWriter) CleanupAll() error {
	return os.RemoveAll(w.baseDir)
}
