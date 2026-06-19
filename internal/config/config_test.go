package config

import "testing"

func TestLoad_SkipPermsDefault(t *testing.T) {
	t.Setenv("AGY_SKIP_PERMISSIONS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.SkipPerms {
		t.Fatal("expected SkipPerms to default to true")
	}
}

func TestLoad_SkipPermsOptOut(t *testing.T) {
	t.Setenv("AGY_SKIP_PERMISSIONS", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SkipPerms {
		t.Fatal("expected SkipPerms to be false when AGY_SKIP_PERMISSIONS=false")
	}
}

func TestParseCLIOptions(t *testing.T) {
	opts, showVersion, err := ParseCLIOptions([]string{
		"--agy-binary", "custom-agy",
		"--model", "gemini-test",
		"--prompt-threshold", "123",
		"--timeout-seconds", "45",
		"--no-skip-permissions",
	})
	if err != nil {
		t.Fatalf("ParseCLIOptions failed: %v", err)
	}
	if showVersion {
		t.Fatal("did not expect version mode")
	}
	if opts.AgyBinary != "custom-agy" || opts.Model != "gemini-test" || opts.PromptThreshold != 123 || opts.TimeoutSeconds != 45 {
		t.Fatalf("unexpected parsed options: %+v", opts)
	}
	if opts.SkipPerms == nil || *opts.SkipPerms {
		t.Fatalf("expected skip perms opt-out, got %+v", opts.SkipPerms)
	}
}

func TestParseCLIOptions_Version(t *testing.T) {
	_, showVersion, err := ParseCLIOptions([]string{"--version"})
	if err != nil {
		t.Fatalf("ParseCLIOptions failed: %v", err)
	}
	if !showVersion {
		t.Fatal("expected version mode")
	}
}

func TestLoadWithOptions_OverridesEnv(t *testing.T) {
	t.Setenv("AGY_BINARY", "env-agy")
	t.Setenv("AGY_MODEL", "env-model")
	v := false

	cfg, err := LoadWithOptions(CLIOptions{
		AgyBinary:       "cli-agy",
		Model:           "cli-model",
		PromptThreshold: 321,
		TimeoutSeconds:  54,
		SkipPerms:       &v,
	})
	if err != nil {
		t.Fatalf("LoadWithOptions failed: %v", err)
	}
	if cfg.AgyBinary != "cli-agy" || cfg.DefaultModel != "cli-model" || cfg.PromptThreshold != 321 || cfg.TimeoutSeconds != 54 || cfg.SkipPerms {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
