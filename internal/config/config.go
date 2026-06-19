package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

const (
	DefaultPromptThreshold = 8000
	DefaultTimeoutSeconds  = 300
	DefaultSessionIDLen    = 8
)

type Config struct {
	AgyBinary       string
	HomeDir         string
	PromptThreshold int
	TimeoutSeconds  int
	DefaultModel    string
	SkipPerms       bool
}

type CLIOptions struct {
	AgyBinary       string
	Model           string
	PromptThreshold int
	TimeoutSeconds  int
	SkipPerms       *bool
}

func Load() (*Config, error) {
	return LoadWithOptions(CLIOptions{})
}

func LoadWithOptions(opts CLIOptions) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		AgyBinary:       getEnvOrDefault("AGY_BINARY", detectAgyBinary()),
		HomeDir:         home,
		PromptThreshold: getEnvIntOrDefault("AGY_PROMPT_THRESHOLD", DefaultPromptThreshold),
		TimeoutSeconds:  getEnvIntOrDefault("AGY_TIMEOUT_SECONDS", DefaultTimeoutSeconds),
		DefaultModel:    os.Getenv("AGY_MODEL"),
		SkipPerms:       getEnvBoolOrDefault("AGY_SKIP_PERMISSIONS", true),
	}
	if opts.AgyBinary != "" {
		cfg.AgyBinary = opts.AgyBinary
	}
	if opts.Model != "" {
		cfg.DefaultModel = opts.Model
	}
	if opts.PromptThreshold > 0 {
		cfg.PromptThreshold = opts.PromptThreshold
	}
	if opts.TimeoutSeconds > 0 {
		cfg.TimeoutSeconds = opts.TimeoutSeconds
	}
	if opts.SkipPerms != nil {
		cfg.SkipPerms = *opts.SkipPerms
	}

	return cfg, nil
}

func ParseCLIOptions(args []string) (CLIOptions, bool, error) {
	fs := flag.NewFlagSet("go-agy-acp-wrapper", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})

	var opts CLIOptions
	var skipPerms bool
	var noSkipPerms bool
	var version bool
	fs.StringVar(&opts.AgyBinary, "agy-binary", "", "agy executable path")
	fs.StringVar(&opts.Model, "model", "", "default agy model")
	fs.IntVar(&opts.PromptThreshold, "prompt-threshold", 0, "prompt file threshold")
	fs.IntVar(&opts.TimeoutSeconds, "timeout-seconds", 0, "agy execution timeout")
	fs.BoolVar(&skipPerms, "skip-permissions", false, "pass --dangerously-skip-permissions to agy")
	fs.BoolVar(&noSkipPerms, "no-skip-permissions", false, "do not pass --dangerously-skip-permissions to agy")
	fs.BoolVar(&version, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return CLIOptions{}, false, err
	}
	if skipPerms && noSkipPerms {
		return CLIOptions{}, false, fmt.Errorf("--skip-permissions and --no-skip-permissions are mutually exclusive")
	}
	if skipPerms {
		v := true
		opts.SkipPerms = &v
	}
	if noSkipPerms {
		v := false
		opts.SkipPerms = &v
	}
	return opts, version, nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func (c *Config) AgyConfigDir() string {
	return filepath.Join(c.HomeDir, ".gemini", "antigravity-cli")
}

func (c *Config) LastConversationsPath() string {
	return filepath.Join(c.AgyConfigDir(), "cache", "last_conversations.json")
}

func (c *Config) BrainDir() string {
	return filepath.Join(c.AgyConfigDir(), "brain")
}

func detectAgyBinary() string {
	if runtime.GOOS == "windows" {
		return "agy.exe"
	}
	return "agy"
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvIntOrDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("invalid integer environment variable, using default", "key", key, "value", v, "default", defaultVal)
	}
	return defaultVal
}

func getEnvBoolOrDefault(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		slog.Warn("invalid boolean environment variable, using default", "key", key, "value", v, "default", defaultVal)
	}
	return defaultVal
}
