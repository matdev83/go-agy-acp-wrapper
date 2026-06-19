package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matdev83/go-agy-acp-wrapper/internal/session"
)

type PromptFileWriter struct {
	threshold int
}

const PromptFileDirName = ".go-agy-acp-wrapper"

func NewPromptFileWriter(threshold int) *PromptFileWriter {
	return &PromptFileWriter{
		threshold: threshold,
	}
}

func (w *PromptFileWriter) NeedsFile(prompt string) bool {
	return len(prompt) > w.threshold
}

func (w *PromptFileWriter) WritePromptFile(cwd, sessionID string, turnCount int, prompt string) (string, error) {
	dir, err := w.sessionDir(cwd, sessionID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create prompt dir: %w", err)
	}

	filename := filepath.Join(dir, fmt.Sprintf("turn_%d.md", turnCount))
	if err := os.WriteFile(filename, []byte(prompt), 0600); err != nil {
		return "", fmt.Errorf("write prompt file: %w", err)
	}
	return filename, nil
}

func (w *PromptFileWriter) WriteContextDump(cwd, sessionID string, turnCount int, transcript []session.Message, newPrompt string) (string, error) {
	dir, err := w.sessionDir(cwd, sessionID)
	if err != nil {
		return "", err
	}
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

func (w *PromptFileWriter) CleanupSession(cwd, sessionID string) error {
	dir, err := w.sessionDir(cwd, sessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (w *PromptFileWriter) CleanupWorkdir(cwd string) error {
	dir, err := w.workdirDir(cwd)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (w *PromptFileWriter) sessionDir(cwd, sessionID string) (string, error) {
	dir, err := w.workdirDir(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID), nil
}

func (w *PromptFileWriter) workdirDir(cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("cwd is required")
	}
	return filepath.Join(cwd, PromptFileDirName), nil
}
