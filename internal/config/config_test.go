package config

import "testing"

func TestLoad_DefaultTimeoutIsFourHours(t *testing.T) {
	t.Setenv("AGY_TIMEOUT_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Fatalf("expected default timeout %d, got %d", DefaultTimeoutSeconds, cfg.TimeoutSeconds)
	}
	if DefaultTimeoutSeconds != 4*60*60 {
		t.Fatalf("expected four-hour default, got %d", DefaultTimeoutSeconds)
	}
}

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
	if opts.QuotaRetryAttempts != 0 || opts.QuotaRetryMaxWaitSeconds != 0 {
		t.Fatalf("expected quota retry flags to stay unset, got %+v", opts)
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

func TestParseCLIOptions_ListModels(t *testing.T) {
	opts, showVersion, err := ParseCLIOptions([]string{
		"--list-models",
		"--agy-binary",
		"custom-agy",
	})
	if err != nil {
		t.Fatalf("ParseCLIOptions failed: %v", err)
	}
	if showVersion {
		t.Fatal("did not expect version mode")
	}
	if !opts.ListModels {
		t.Fatal("expected list-models mode")
	}
	if opts.AgyBinary != "custom-agy" {
		t.Fatalf("unexpected agy binary: %q", opts.AgyBinary)
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

func TestLoad_ExecutionEnvNoteDefaultOn(t *testing.T) {
	t.Setenv("AGY_ACP_SKIP_ENV_NOTE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.InjectExecutionEnvNote {
		t.Fatal("expected execution env note to be injected by default")
	}
}

func TestLoad_ExecutionEnvNoteOptOutEnv(t *testing.T) {
	t.Setenv("AGY_ACP_SKIP_ENV_NOTE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.InjectExecutionEnvNote {
		t.Fatal("expected AGY_ACP_SKIP_ENV_NOTE=true to disable injection")
	}
}

func TestParseCLIOptions_NoExecutionEnvNote(t *testing.T) {
	opts, showVersion, err := ParseCLIOptions([]string{"--no-execution-env-note"})
	if err != nil {
		t.Fatalf("ParseCLIOptions failed: %v", err)
	}
	if showVersion {
		t.Fatal("did not expect version mode")
	}
	if opts.InjectExecutionEnvNote == nil || *opts.InjectExecutionEnvNote {
		t.Fatalf("expected env-note opt-out, got %+v", opts.InjectExecutionEnvNote)
	}
}

func TestParseCLIOptions_ExecutionEnvNoteFlagsMutuallyExclusive(t *testing.T) {
	_, _, err := ParseCLIOptions([]string{"--execution-env-note", "--no-execution-env-note"})
	if err == nil {
		t.Fatal("expected mutually exclusive flag error")
	}
}

func TestLoadWithOptions_ExecutionEnvNoteOverridesEnv(t *testing.T) {
	t.Setenv("AGY_ACP_SKIP_ENV_NOTE", "true")
	v := true

	cfg, err := LoadWithOptions(CLIOptions{InjectExecutionEnvNote: &v})
	if err != nil {
		t.Fatalf("LoadWithOptions failed: %v", err)
	}
	if !cfg.InjectExecutionEnvNote {
		t.Fatal("expected CLI override to re-enable env-note injection")
	}
}

func TestLoad_QuotaRetryDefaults(t *testing.T) {
	t.Setenv("AGY_QUOTA_RETRY_ATTEMPTS", "")
	t.Setenv("AGY_QUOTA_RETRY_MAX_WAIT_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.QuotaRetryAttempts != DefaultQuotaRetryAttempts {
		t.Fatalf("attempts = %d, want %d", cfg.QuotaRetryAttempts, DefaultQuotaRetryAttempts)
	}
	if cfg.QuotaRetryMaxWaitSeconds != 0 {
		t.Fatalf("max wait = %d, want 0 (remaining timeout)", cfg.QuotaRetryMaxWaitSeconds)
	}
}

func TestParseCLIOptions_QuotaRetry(t *testing.T) {
	opts, showVersion, err := ParseCLIOptions([]string{
		"--quota-retry-attempts", "3",
		"--quota-retry-max-wait-seconds", "900",
	})
	if err != nil {
		t.Fatalf("ParseCLIOptions failed: %v", err)
	}
	if showVersion {
		t.Fatal("did not expect version mode")
	}
	if opts.QuotaRetryAttempts != 3 || opts.QuotaRetryMaxWaitSeconds != 900 {
		t.Fatalf("unexpected quota retry options: %+v", opts)
	}
}

