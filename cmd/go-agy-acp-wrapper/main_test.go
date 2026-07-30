package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrintModelsUsesCanonicalCatalog(t *testing.T) {
	binary := writeModelCatalogFixture(t,
		"gemini-3.6-flash-high",
		"gemini-3.6-flash-low",
		"claude-opus-4-6-thinking",
	)
	var output bytes.Buffer

	if err := printModels(context.Background(), binary, &output); err != nil {
		t.Fatalf("printModels failed: %v", err)
	}

	if got, want := output.String(),
		"google/gemini-3.6-flash\nanthropic/claude-opus-4.6\n"; got != want {
		t.Fatalf("catalog output = %q, want %q", got, want)
	}
}

func writeModelCatalogFixture(t *testing.T, models ...string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "agy-models.bat")
		content := "@echo off\r\n"
		for _, model := range models {
			content += "echo " + model + "\r\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "agy-models.sh")
	content := "#!/bin/sh\n"
	for _, model := range models {
		content += "printf '%s\\n' " + model + "\n"
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
