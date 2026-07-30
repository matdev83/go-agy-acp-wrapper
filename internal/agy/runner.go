package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	Output         string
	ConversationID string
	ExitCode       int
	TimedOut       bool
}

type StreamEventKind int

const (
	StreamEventText StreamEventKind = iota
	StreamEventToolStart
	StreamEventToolUpdate
)

type StreamEvent struct {
	Kind      StreamEventKind
	Text      string
	ToolID    string
	ToolName  string
	ToolState string
	RawInput  any
	RawOutput any
}

// ProcessError preserves a failed agy invocation as a user-facing provider error.
type ProcessError struct {
	ExitCode int
	Detail   string
}

func (e *ProcessError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if IsProviderLimitError(e) {
		if detail == "" {
			return "agy model provider quota or rate limit was exceeded"
		}
		return fmt.Sprintf("agy model provider quota or rate limit was exceeded: %s", detail)
	}
	if detail == "" {
		return fmt.Sprintf("agy request failed (exit code %d)", e.ExitCode)
	}
	return fmt.Sprintf("agy request failed (exit code %d): %s", e.ExitCode, detail)
}

func IsProviderLimitError(err error) bool {
	var processErr *ProcessError
	if !errors.As(err, &processErr) {
		return false
	}
	message := strings.ToLower(processErr.Detail)
	for _, marker := range []string{
		"quota", "resource_exhausted", "resource exhausted", "rate limit",
		"too many requests", "usage limit", "limit reached", "status 429", "code 429",
		"weighted tokens", "tokens remaining", "tokens left", "out of tokens",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

type Runner interface {
	Execute(ctx context.Context, opts ExecuteOpts) (*Response, error)
	ExecuteStream(ctx context.Context, opts ExecuteOpts, onEvent func(StreamEvent)) (*Response, error)
}

type NonInteractiveRunner struct {
	binary    string
	configDir string
}

const transcriptPollInterval = 50 * time.Millisecond

func NewNonInteractiveRunner(binary, configDir string) *NonInteractiveRunner {
	return &NonInteractiveRunner{binary: binary, configDir: configDir}
}

func (r *NonInteractiveRunner) Execute(ctx context.Context, opts ExecuteOpts) (*Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *NonInteractiveRunner) ExecuteStream(ctx context.Context, opts ExecuteOpts, onEvent func(StreamEvent)) (*Response, error) {
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
	cmd.Env = r.commandEnv()
	processTree, err := configureProcessTree(cmd)
	if err != nil {
		return nil, fmt.Errorf("configure agy process tree: %w", err)
	}
	defer processTree.Close()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to execute agy: %w", err)
	}
	if err := processTree.AfterStart(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("contain agy process tree: %w", err)
	}
	var stream agyJSONStream
	var stderr bytes.Buffer
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		stream.read(stdoutPipe, onEvent)
	}()
	go func() {
		defer readers.Done()
		_, _ = io.Copy(&stderr, stderrPipe)
	}()

	err = cmd.Wait()
	readers.Wait()

	response := &Response{
		Output:         stream.output(),
		ConversationID: stream.conversationID,
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
			detail := normalizeLineEndings(stderr.String())
			if strings.TrimSpace(detail) == "" {
				detail = response.Output
			} else if IsProviderLimitError(&ProcessError{Detail: response.Output}) &&
				!IsProviderLimitError(&ProcessError{Detail: detail}) {
				detail = strings.TrimSpace(detail) + "\n" + strings.TrimSpace(response.Output)
			}
			if response.Output == "" {
				response.Output = detail
			}
			return response, &ProcessError{ExitCode: response.ExitCode, Detail: detail}
		}
		return nil, fmt.Errorf("failed to execute agy: %w", err)
	}

	response.ExitCode = 0

	if response.Output == "" && opts.ConversationID != "" {
		if extracted := r.extractFromTranscript(opts.ConversationID); len(extracted) > len(response.Output) {
			slog.Debug("extracted fuller response from transcript", "conversationId", opts.ConversationID)
			response.Output = extracted
		}
	}

	return response, nil
}

type agyJSONStream struct {
	raw            bytes.Buffer
	streamed       strings.Builder
	result         string
	conversationID string
}

func (s *agyJSONStream) read(r io.Reader, onEvent func(StreamEvent)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		s.raw.Write(line)
		s.raw.WriteByte('\n')

		var event struct {
			Event          string `json:"event"`
			ConversationID string `json:"conversation_id"`
			StepUpdate     struct {
				ConversationID string `json:"conversation_id"`
				StepIndex      int    `json:"step_index"`
				State          string `json:"state"`
				StepType       string `json:"step_type"`
				TextDelta      string `json:"text_delta"`
				ToolName       string `json:"tool_name"`
				ToolInfo       struct {
					Name       string `json:"name"`
					Parameters any    `json:"parameters"`
					Output     any    `json:"output"`
				} `json:"tool_info"`
			} `json:"step_update"`
			Result struct {
				ConversationID string `json:"conversation_id"`
				Response       string `json:"response"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		switch event.Event {
		case "init":
			s.conversationID = event.ConversationID
		case "step_update":
			if event.StepUpdate.ConversationID != "" {
				s.conversationID = event.StepUpdate.ConversationID
			}
			if event.StepUpdate.StepType == "agent_response" && event.StepUpdate.TextDelta != "" {
				chunk := normalizeLineEndings(event.StepUpdate.TextDelta)
				s.streamed.WriteString(chunk)
				if onEvent != nil {
					onEvent(StreamEvent{Kind: StreamEventText, Text: chunk})
				}
			}
			if event.StepUpdate.StepType == "tool" && onEvent != nil {
				toolName := event.StepUpdate.ToolName
				if toolName == "" {
					toolName = event.StepUpdate.ToolInfo.Name
				}
				streamEvent := StreamEvent{
					ToolID:    fmt.Sprintf("agy-step-%d", event.StepUpdate.StepIndex),
					ToolName:  toolName,
					ToolState: event.StepUpdate.State,
					RawInput:  event.StepUpdate.ToolInfo.Parameters,
					RawOutput: event.StepUpdate.ToolInfo.Output,
				}
				if event.StepUpdate.State == "ACTIVE" {
					streamEvent.Kind = StreamEventToolStart
				} else {
					streamEvent.Kind = StreamEventToolUpdate
				}
				onEvent(streamEvent)
			}
		case "result":
			if event.Result.ConversationID != "" {
				s.conversationID = event.Result.ConversationID
			}
			s.result = normalizeLineEndings(event.Result.Response)
		}
	}
}

func (s *agyJSONStream) output() string {
	if s.result != "" {
		return s.result
	}
	if s.streamed.Len() > 0 {
		return s.streamed.String()
	}
	return normalizeLineEndings(s.raw.String())
}

func streamPipeLines(r io.Reader, dst *bytes.Buffer, onChunk func(string)) {
	buf := make([]byte, 4096)
	var pendingCR bool
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			var sb strings.Builder
			sb.Grow(len(chunk))
			for _, b := range chunk {
				if pendingCR {
					if b == '\n' {
						sb.WriteByte('\n')
						pendingCR = false
						continue
					}
					sb.WriteByte('\r')
					pendingCR = false
				}
				if b == '\r' {
					pendingCR = true
				} else {
					sb.WriteByte(b)
				}
			}
			text := sb.String()
			if text != "" {
				_, _ = dst.WriteString(text)
				if onChunk != nil {
					onChunk(text)
				}
			}
		}
		if err != nil {
			if pendingCR {
				_, _ = dst.WriteString("\r")
				if onChunk != nil {
					onChunk("\r")
				}
			}
			return
		}
	}
}

type transcriptTailer struct {
	configDir      string
	conversationID string
	startedAt      time.Time
	onChunk        func(string)
	states         map[string]transcriptFileState
	lastContent    string
	mu             sync.Mutex
}

type transcriptFileState struct {
	offset  int64
	modTime time.Time
}

func newTranscriptTailer(configDir, conversationID string, startedAt time.Time, onChunk func(string)) *transcriptTailer {
	return &transcriptTailer{
		configDir:      configDir,
		conversationID: conversationID,
		startedAt:      startedAt,
		onChunk:        onChunk,
		states:         make(map[string]transcriptFileState),
	}
}

func (t *transcriptTailer) snapshotExisting() {
	for _, path := range t.candidateTranscriptPaths() {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		t.states[path] = transcriptFileState{offset: info.Size(), modTime: info.ModTime()}
	}
}

func (t *transcriptTailer) run(stop <-chan struct{}) {
	ticker := time.NewTicker(transcriptPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.scan(false)
		}
	}
}

func (t *transcriptTailer) scan(isFinal bool) {
	for _, path := range t.candidateTranscriptPaths() {
		t.scanFile(path, isFinal)
	}
}

func (t *transcriptTailer) output() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastContent
}

func (t *transcriptTailer) candidateTranscriptPaths() []string {
	if t.conversationID != "" {
		return []string{transcriptPath(t.configDir, t.conversationID)}
	}

	brainDir := filepath.Join(t.configDir, "brain")
	var paths []string
	_ = filepath.WalkDir(brainDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "transcript.jsonl" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths
}

func (t *transcriptTailer) scanFile(path string, isFinal bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}

	state, known := t.states[path]
	if !known {
		if info.ModTime().Before(t.startedAt.Add(-time.Second)) {
			return
		}
		state = transcriptFileState{}
	}
	if known && info.Size() == state.offset && info.ModTime().Equal(state.modTime) && !isFinal {
		return
	}
	if info.Size() < state.offset {
		state.offset = 0
	}

	data, nextOffset, err := readJSONLFrom(path, state.offset, isFinal)
	if err != nil {
		slog.Debug("tail transcript read failed", "path", path, "error", err)
		return
	}
	if len(data) > 0 {
		t.consume(data)
	}
	t.states[path] = transcriptFileState{offset: nextOffset, modTime: info.ModTime()}
}

func (t *transcriptTailer) consume(data []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "PLANNER_RESPONSE" || entry.Content == "" {
			continue
		}
		t.emit(entry.Content)
	}
}

func (t *transcriptTailer) emit(content string) {
	content = normalizeLineEndings(content)
	t.mu.Lock()
	previous := t.lastContent
	if content == previous {
		t.mu.Unlock()
		return
	}
	t.lastContent = content
	t.mu.Unlock()

	chunk := content
	if strings.HasPrefix(content, previous) {
		chunk = content[len(previous):]
	}
	if chunk != "" && t.onChunk != nil {
		t.onChunk(chunk)
	}
}

func readJSONLFrom(path string, offset int64, isFinal bool) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	if len(data) == 0 {
		return nil, offset, nil
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		if isFinal {
			return data, offset + int64(len(data)), nil
		}
		return nil, offset, nil
	}
	if isFinal && lastNewline < len(data)-1 {
		return data, offset + int64(len(data)), nil
	}
	complete := data[:lastNewline+1]
	return complete, offset + int64(len(complete)), nil
}

func transcriptPath(configDir, conversationID string) string {
	return filepath.Join(configDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")
}

func (r *NonInteractiveRunner) commandEnv() []string {
	env := os.Environ()
	if r.configDir == "" {
		return env
	}
	home := filepath.Dir(filepath.Dir(r.configDir))
	if home == "." || home == string(filepath.Separator) {
		return env
	}
	if os.Getenv("HOME") == "" {
		env = append(env, "HOME="+home)
	}
	if os.Getenv("USERPROFILE") == "" {
		env = append(env, "USERPROFILE="+home)
	}
	return env
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

	args = append(args, "--output-format", "stream-json", "--print")
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
	f, err := os.Open(transcriptPath(r.configDir, conversationID))
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastResponse string
	reader := bufio.NewReader(f)
	for {
		line, err := readJSONLLine(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Warn("failed to read transcript", "conversationId", conversationID, "error", err)
			return ""
		}

		var entry struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(line, &entry) == nil {
			if entry.Type == "PLANNER_RESPONSE" && entry.Content != "" {
				lastResponse = entry.Content
			}
		}
	}
	return lastResponse
}

func readJSONLLine(r io.ByteReader) ([]byte, error) {
	var line []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		if b == '\n' {
			return line, nil
		}
		if b == '\r' {
			continue
		}
		line = append(line, b)
	}
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
