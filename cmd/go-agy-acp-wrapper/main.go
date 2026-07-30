package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/matdev83/go-agy-acp-wrapper/internal/acp"
	"github.com/matdev83/go-agy-acp-wrapper/internal/agy"
	"github.com/matdev83/go-agy-acp-wrapper/internal/config"
)

var version = "dev"

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

	if opts.ListModels {
		if err := printModels(ctx, cfg.AgyBinary, os.Stdout); err != nil {
			slog.Error("failed to list models", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := acp.Serve(ctx, cfg, os.Stdin, os.Stdout); err != nil {
		slog.Error("agent server exited with error", "error", err)
		os.Exit(1)
	}
}

func printModels(ctx context.Context, agyBinary string, output io.Writer) error {
	catalog := agy.NewStrictModelCatalog(agyBinary)
	if err := catalog.EnsureLoaded(ctx); err != nil {
		return err
	}
	for _, model := range catalog.Models() {
		if _, err := fmt.Fprintln(output, model.ID); err != nil {
			return err
		}
	}
	return nil
}
