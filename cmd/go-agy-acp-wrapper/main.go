package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/matdev83/go-agy-acp-wrapper/internal/acp"
	"github.com/matdev83/go-agy-acp-wrapper/internal/config"
)

const version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	opts, showVersion, err := config.ParseCLIOptions(os.Args[1:])
	if err != nil {
		slog.Error("failed to parse arguments", "error", err)
		os.Exit(2)
	}
	if showVersion {
		fmt.Fprintln(os.Stdout, version)
		return
	}

	cfg, err := config.LoadWithOptions(opts)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := acp.Serve(ctx, cfg, os.Stdin, os.Stdout); err != nil {
		slog.Error("agent server exited with error", "error", err)
		os.Exit(1)
	}
}
