package agy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeModelIDs(t *testing.T) {
	catalog := NewModelCatalogFromIDs(
		"gemini-3.6-flash-high",
		"gemini-3.6-flash-medium",
		"gemini-3.6-flash-low",
		"gemini-3.1-pro-high",
		"gemini-3.1-pro-low",
		"claude-opus-4-6-thinking",
		"gpt-oss-120b-medium",
	)
	profiles := catalog.Profiles()
	if len(profiles) != 4 {
		t.Fatalf("expected 4 canonical profiles, got %#v", profiles)
	}
	if got := profiles[0].CanonicalID; got != "google/gemini-3.6-flash" {
		t.Fatalf("canonical ID = %q", got)
	}
	if got := catalog.DefaultModelID(); got != "google/gemini-3.6-flash" {
		t.Fatalf("default = %q", got)
	}
	if got, err := catalog.ResolveNative("google/gemini-3.6-flash", "high"); err != nil || got != "gemini-3.6-flash-high" {
		t.Fatalf("ResolveNative = %q, %v", got, err)
	}
	if got, err := catalog.ResolveNative("anthropic/claude-opus-4.6", ""); err != nil || got != "claude-opus-4-6-thinking" {
		t.Fatalf("ResolveNative thinking = %q, %v", got, err)
	}
}

func TestModelCatalogResolveCanonicalLegacy(t *testing.T) {
	catalog := NewModelCatalogFromIDs("gemini-3.5-flash-high", "gemini-3.5-flash-medium")
	for _, input := range []string{
		"google/gemini-3.5-flash",
		"google/gemini-3.5-flash-high",
		"gemini-3.5-flash-high",
	} {
		got, err := catalog.ResolveCanonical(input)
		if err != nil || got != "google/gemini-3.5-flash" {
			t.Fatalf("ResolveCanonical(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := catalog.ResolveCanonical("not-a-model"); err == nil {
		t.Fatal("expected unknown-model error")
	}
}

func TestResolveNativeRejectsUnsupportedEffort(t *testing.T) {
	catalog := NewModelCatalogFromIDs("gemini-3.1-pro-high", "gemini-3.1-pro-low")
	if _, err := catalog.ResolveNative("google/gemini-3.1-pro", "medium"); err == nil {
		t.Fatal("expected unsupported effort error")
	}
}

func TestModelCatalogDiscoverFromScriptAndCache(t *testing.T) {
	script := writeModelsScript(t, "gemini-3.6-flash-high\ngemini-3.6-flash-medium\n")
	catalog := NewModelCatalog(script)
	if err := catalog.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if got := catalog.Models(); len(got) != 1 || got[0].ID != "google/gemini-3.6-flash" {
		t.Fatalf("models = %#v", got)
	}
	if err := overwriteFailingScript(script); err != nil {
		t.Fatal(err)
	}
	if err := catalog.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("cached EnsureLoaded: %v", err)
	}
	if got := catalog.DefaultModelID(); got != "google/gemini-3.6-flash" {
		t.Fatalf("cached default = %q", got)
	}
}

func TestModelCatalogFallback(t *testing.T) {
	catalog := NewModelCatalog("nonexistent-binary-xyz")
	if err := catalog.EnsureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := catalog.DefaultModelID(); got != "google/gemini-3.5-flash" {
		t.Fatalf("fallback default = %q", got)
	}
}

func TestStrictModelCatalogRejectsDiscoveryFailure(t *testing.T) {
	catalog := NewStrictModelCatalog("nonexistent-binary-xyz")
	if err := catalog.EnsureLoaded(context.Background()); err == nil {
		t.Fatal("expected strict discovery to fail")
	}
	if got := catalog.Models(); len(got) != 0 {
		t.Fatalf("strict catalog published fallback models: %#v", got)
	}
}

func writeModelsScript(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "agy-models.bat")
		var b strings.Builder
		b.WriteString("@echo off\r\n")
		for _, line := range lines {
			b.WriteString("echo " + line + "\r\n")
		}
		if err := os.WriteFile(path, []byte(b.String()), 0755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "agy-models.sh")
	content := "#!/bin/sh\n"
	for _, line := range lines {
		content += "echo " + line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func overwriteFailingScript(path string) error {
	if runtime.GOOS == "windows" {
		return os.WriteFile(path, []byte("@echo off\r\nexit /b 1\r\n"), 0755)
	}
	return os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0755)
}
