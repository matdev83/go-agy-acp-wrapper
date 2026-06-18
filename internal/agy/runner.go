package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ExecuteOpts struct {
	Cwd            string
	Prompt         string
	ConversationID string
	PromptFilePath string
	Model          string
	Timeout        time.Duration
	SkipPerms      bool
}

type Response struct {
	Output   string
	ExitCode int
	TimedOut bool
}

type Runner interface {
	Execute(ctx context.Context, opts ExecuteOpts) (*Response, error)
}

type NonInteractiveRunner struct {
	binary    string
	configDir string
}

func NewNonInteractiveRunner(binary, configDir string) *NonInteractiveRunner {
	return &NonInteractiveRunner{binary: binary, configDir: configDir}
}

func (r *NonInteractiveRunner) Execute(ctx context.Context, opts ExecuteOpts) (*Response, error) {
	args := r.buildArgs(opts)
	slog.Debug("executing agy", "binary", r.binary, "args", args, "cwd", opts.Cwd)

	var execCtx context.Context
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(execCtx, r.binary, args...)
	cmd.Dir = opts.Cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	response := &Response{
		Output: normalizeLineEndings(stdout.String()),
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			response.TimedOut = true
			response.ExitCode = -1
			return response, nil
		}
		if ctx.Err() == context.Canceled {
			response.ExitCode = -1
			return response, ctx.Err()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			response.ExitCode = exitErr.ExitCode()
			if response.Output == "" {
				response.Output = normalizeLineEndings(stderr.String())
			}
			return response, fmt.Errorf("agy exited with code %d: %s", response.ExitCode, stderr.String())
		}
		return nil, fmt.Errorf("failed to execute agy: %w", err)
	}

	response.ExitCode = 0

	if strings.TrimSpace(response.Output) == "" && opts.ConversationID != "" {
		if extracted := r.extractFromTranscript(opts.ConversationID); extracted != "" {
			slog.Debug("stdout empty, extracted response from transcript", "conversationId", opts.ConversationID)
			response.Output = extracted
		}
	} else if strings.TrimSpace(response.Output) == "" && r.configDir != "" {
		disc := NewConversationDiscoverer(r.configDir)
		if convID, err := disc.DiscoverConversationID(opts.Cwd); err == nil {
			if extracted := r.extractFromTranscript(convID); extracted != "" {
				slog.Debug("stdout empty, extracted response from transcript (discovered)", "conversationId", convID)
				response.Output = extracted
			}
		}
	}

	return response, nil
}

func (r *NonInteractiveRunner) buildArgs(opts ExecuteOpts) []string {
	args := make([]string, 0, 10)

	if opts.ConversationID != "" {
		args = append(args, "--conversation", opts.ConversationID)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.SkipPerms {
		args = append(args, "--dangerously-skip-permissions")
	}

	args = append(args, "--print")
	if opts.PromptFilePath != "" {
		args = append(args, "@"+opts.PromptFilePath)
	} else {
		args = append(args, opts.Prompt)
	}

	return args
}

func (r *NonInteractiveRunner) extractFromTranscript(conversationID string) string {
	if r.configDir == "" {
		return ""
	}
	transcriptPath := filepath.Join(r.configDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastResponse string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			if entry.Type == "PLANNER_RESPONSE" && entry.Content != "" {
				lastResponse = entry.Content
			}
		}
	}
	return lastResponse
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
