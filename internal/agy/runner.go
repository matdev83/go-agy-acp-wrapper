package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	Output   string
	ExitCode int
	TimedOut bool
}

type Runner interface {
	Execute(ctx context.Context, opts ExecuteOpts) (*Response, error)
	ExecuteStream(ctx context.Context, opts ExecuteOpts, onStdout func(string)) (*Response, error)
}

type NonInteractiveRunner struct {
	binary    string
	configDir string
}

const transcriptPollInterval = 250 * time.Millisecond

func NewNonInteractiveRunner(binary, configDir string) *NonInteractiveRunner {
	return &NonInteractiveRunner{binary: binary, configDir: configDir}
}

func (r *NonInteractiveRunner) Execute(ctx context.Context, opts ExecuteOpts) (*Response, error) {
	return r.ExecuteStream(ctx, opts, nil)
}

func (r *NonInteractiveRunner) ExecuteStream(ctx context.Context, opts ExecuteOpts, onStdout func(string)) (*Response, error) {
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

	startedAt := time.Now()
	var tailer *transcriptTailer
	var tailStop chan struct{}
	var stopTailerOnce sync.Once
	streamChunk := onStdout
	if onStdout != nil && r.configDir != "" && opts.ConversationID != "" {
		tailStop = make(chan struct{})
		streamChunk = func(chunk string) {
			onStdout(chunk)
			stopTailerOnce.Do(func() { close(tailStop) })
		}
		tailer = newTranscriptTailer(r.configDir, opts.ConversationID, startedAt, streamChunk)
		tailer.snapshotExisting()
	}

	cmd := exec.CommandContext(execCtx, r.binary, args...)
	cmd.Dir = opts.Cwd

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
	if tailer != nil {
		tailer.startedAt = time.Now()
	}

	var stdout, stderr bytes.Buffer
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		streamPipeLines(stdoutPipe, &stdout, streamChunk)
	}()
	go func() {
		defer readers.Done()
		_, _ = io.Copy(&stderr, stderrPipe)
	}()

	var tailDone chan struct{}
	if tailer != nil {
		tailDone = make(chan struct{})
		go func() {
			defer close(tailDone)
			tailer.run(tailStop)
		}()
	}

	err = cmd.Wait()
	readers.Wait()
	if tailer != nil {
		stopTailerOnce.Do(func() { close(tailStop) })
		<-tailDone
		tailer.scan()
	}

	response := &Response{
		Output: normalizeLineEndings(stdout.String()),
	}
	if strings.TrimSpace(response.Output) == "" && tailer != nil {
		response.Output = tailer.output()
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

func streamPipeLines(r io.Reader, dst *bytes.Buffer, onLine func(string)) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			normalized := normalizeLineEndings(line)
			_, _ = dst.WriteString(normalized)
			if onLine != nil {
				onLine(normalized)
			}
		}
		if err != nil {
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
			t.scan()
		}
	}
}

func (t *transcriptTailer) scan() {
	for _, path := range t.candidateTranscriptPaths() {
		t.scanFile(path)
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

func (t *transcriptTailer) scanFile(path string) {
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
	if known && info.Size() == state.offset && info.ModTime().Equal(state.modTime) {
		return
	}
	if info.Size() < state.offset {
		state.offset = 0
	}

	data, nextOffset, err := readCompleteJSONLFrom(path, state.offset)
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

func readCompleteJSONLFrom(path string, offset int64) ([]byte, int64, error) {
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
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return nil, offset, nil
	}
	complete := data[:lastNewline+1]
	return complete, offset + int64(len(complete)), nil
}

func transcriptPath(configDir, conversationID string) string {
	return filepath.Join(configDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")
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
