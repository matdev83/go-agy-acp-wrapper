package config

import (
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
	TempDir         string
	PromptThreshold int
	TimeoutSeconds  int
	DefaultModel    string
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		AgyBinary:       getEnvOrDefault("AGY_BINARY", detectAgyBinary()),
		HomeDir:         home,
		TempDir:         filepath.Join(os.TempDir(), "go-agy-acp"),
		PromptThreshold: getEnvIntOrDefault("AGY_PROMPT_THRESHOLD", DefaultPromptThreshold),
		TimeoutSeconds:  getEnvIntOrDefault("AGY_TIMEOUT_SECONDS", DefaultTimeoutSeconds),
		DefaultModel:    os.Getenv("AGY_MODEL"),
	}

	return cfg, nil
}

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
