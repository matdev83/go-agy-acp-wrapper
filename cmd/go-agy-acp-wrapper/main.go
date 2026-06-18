package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/mateusz/go-agy-acp-wrapper/internal/acp"
	"github.com/mateusz/go-agy-acp-wrapper/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
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
