package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/coder/acp-go-sdk"
)

type smokeClient struct {
	updates []string
}

func (c *smokeClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{Content: "mock file content"}, nil
}

func (c *smokeClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}

func (c *smokeClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if len(params.Options) > 0 {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: params.Options[0].OptionId},
			},
		}, nil
	}
	return acp.RequestPermissionResponse{}, nil
}

func (c *smokeClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	u := params.Update
	switch {
	case u.AgentMessageChunk != nil:
		if u.AgentMessageChunk.Content.Text != nil {
			text := u.AgentMessageChunk.Content.Text.Text
			c.updates = append(c.updates, text)
			fmt.Printf("  [agent] %s\n", text)
		}
	case u.ToolCall != nil:
		fmt.Printf("  [tool_call] %s\n", u.ToolCall.Title)
	case u.ToolCallUpdate != nil:
		fmt.Printf("  [tool_update] %s\n", u.ToolCallUpdate.ToolCallId)
	}
	return nil
}

func (c *smokeClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{TerminalId: "term-smoke"}, nil
}

func (c *smokeClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}

func (c *smokeClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}

func (c *smokeClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *smokeClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	wrapperBin := os.Getenv("WRAPPER_BIN")
	if wrapperBin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot find self: %v\n", err)
			os.Exit(1)
		}
		wrapperBin = filepath.Join(filepath.Dir(exe), "go-agy-acp-wrapper")
		if _, err := os.Stat(wrapperBin); os.IsNotExist(err) {
			wrapperBin = filepath.Join(filepath.Dir(exe), "go-agy-acp-wrapper.exe")
		}
	}

	fmt.Printf("=== ACP Smoke Test ===\n")
	fmt.Printf("Wrapper binary: %s\n\n", wrapperBin)

	cmd := exec.CommandContext(ctx, wrapperBin)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stdin pipe: %v\n", err)
		os.Exit(1)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stdout pipe: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start wrapper: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	client := &smokeClient{}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	conn.SetLogger(logger)

	fmt.Print("1. Initialize... ")
	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK (protocol v%d)\n", initResp.ProtocolVersion)

	cwd, _ := os.Getwd()
	fmt.Printf("2. NewSession (cwd=%s)... ", cwd)
	sessResp, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK (session=%s)\n", sessResp.SessionId)

	prompts := []string{
		"Say hello in one sentence.",
		"Now say goodbye in one sentence.",
		"What was the first thing I asked you?",
	}

	for i, prompt := range prompts {
		fmt.Printf("3.%d. Prompt: %q\n", i+1, prompt)
		promptCtx, promptCancel := context.WithTimeout(ctx, 120*time.Second)
		resp, err := conn.Prompt(promptCtx, acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
		})
		promptCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Stop reason: %s\n\n", resp.StopReason)
	}

	fmt.Printf("4. CloseSession... ")
	_, err = conn.CloseSession(ctx, acp.CloseSessionRequest{
		SessionId: sessResp.SessionId,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK\n")

	fmt.Printf("\n=== SMOKE TEST PASSED ===\n")
	fmt.Printf("Total updates received: %d\n", len(client.updates))
}
