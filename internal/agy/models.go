package agy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strings"
	"sync"
)

const (
	ReasoningLow    = "low"
	ReasoningMedium = "medium"
	ReasoningHigh   = "high"
)

// ModelProfile is the public canonical identity and its agy-native variants.
type ModelProfile struct {
	CanonicalID    string
	DisplayName    string
	Native         string
	NativeByEffort map[string]string
	DefaultEffort  string
}

// ModelCatalog discovers available models from agy and caches the normalized
// profiles for the lifetime of the catalog instance.
type ModelCatalog struct {
	binary        string
	allowFallback bool

	mu           sync.RWMutex
	once         sync.Once
	loaded       bool
	profiles     []ModelProfile
	byID         map[string]ModelProfile
	aliases      map[string]string
	defaultModel string
}

var fallbackModelIDs = []string{
	"gemini-3.5-flash-high",
	"gemini-3.5-flash-medium",
	"gemini-3.5-flash-low",
	"gemini-3.1-pro-high",
	"claude-sonnet-4-6",
	"claude-opus-4-6-thinking",
}

func NewModelCatalog(binary string) *ModelCatalog {
	return &ModelCatalog{binary: binary, allowFallback: true}
}

// NewStrictModelCatalog constructs a catalog that reports discovery failures
// instead of advertising baked-in fallback models.
func NewStrictModelCatalog(binary string) *ModelCatalog {
	return &ModelCatalog{binary: binary}
}

// NewModelCatalogFromIDs constructs a preloaded catalog without invoking agy.
func NewModelCatalogFromIDs(ids ...string) *ModelCatalog {
	c := &ModelCatalog{binary: "test"}
	c.setProfiles(normalizeModelIDs(ids))
	c.once.Do(func() {})
	return c
}

// EnsureLoaded discovers models once. Cancellation is returned rather than
// cached as a fallback so a later call can build a fresh catalog instance.
func (c *ModelCatalog) EnsureLoaded(ctx context.Context) error {
	var loadErr error
	c.once.Do(func() {
		loadErr = c.load(ctx)
	})
	if loadErr != nil {
		return loadErr
	}
	c.mu.RLock()
	loaded := c.loaded
	c.mu.RUnlock()
	if !loaded {
		return fmt.Errorf("model catalog was not loaded")
	}
	return nil
}

func (c *ModelCatalog) load(ctx context.Context) error {
	ids, err := discoverModelIDs(ctx, c.binary)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if !c.allowFallback {
			return err
		}
		slog.Warn("agy model discovery failed, using fallback list", "error", err)
		ids = fallbackModelIDs
	} else if len(ids) == 0 {
		if !c.allowFallback {
			return fmt.Errorf("agy model discovery returned no models")
		}
		slog.Warn("agy model discovery returned no models, using fallback list")
		ids = fallbackModelIDs
	} else {
		slog.Info("discovered agy models", "count", len(ids))
	}
	profiles := normalizeModelIDs(ids)
	if len(profiles) == 0 {
		if !c.allowFallback {
			return fmt.Errorf("agy model discovery produced no recognized models")
		}
		slog.Warn("agy model discovery produced no recognized models, using fallback list")
		profiles = normalizeModelIDs(fallbackModelIDs)
	}
	c.setProfiles(profiles)
	return nil
}

func (c *ModelCatalog) setProfiles(profiles []ModelProfile) {
	byID := make(map[string]ModelProfile, len(profiles))
	aliases := make(map[string]string)
	for _, profile := range profiles {
		byID[profile.CanonicalID] = profile
		aliases[profile.CanonicalID] = profile.CanonicalID
		aliases[profile.DisplayName] = profile.CanonicalID
		if profile.Native != "" {
			aliases[profile.Native] = profile.CanonicalID
		}
		for _, native := range profile.NativeByEffort {
			aliases[native] = profile.CanonicalID
		}
	}
	addLegacyAliases(aliases, byID)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.profiles = profiles
	c.byID = byID
	c.aliases = aliases
	c.defaultModel = pickDefaultModel(profiles)
	c.loaded = true
}

// Model is retained as a compact compatibility view of a profile.
type Model struct {
	ID   string
	Name string
}

func (c *ModelCatalog) Models() []Model {
	profiles := c.Profiles()
	out := make([]Model, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, Model{ID: profile.CanonicalID, Name: profile.DisplayName})
	}
	return out
}

func (c *ModelCatalog) Resolve(input string) (string, error) {
	return c.ResolveCanonical(input)
}

func (c *ModelCatalog) Profiles() []ModelProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ModelProfile, len(c.profiles))
	copy(out, c.profiles)
	return out
}

func (c *ModelCatalog) DefaultModelID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.defaultModel
}

func (c *ModelCatalog) Contains(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.byID[id]
	return ok
}

// ResolveCanonical maps public, native, and legacy identifiers to a canonical ID.
func (c *ModelCatalog) ResolveCanonical(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" || input == "auto" {
		return c.DefaultModelID(), nil
	}
	c.mu.RLock()
	canonical, ok := c.aliases[input]
	c.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown model: %s", input)
	}
	return canonical, nil
}

// ResolveNative selects the exact ID accepted by agy for a canonical model and effort.
func (c *ModelCatalog) ResolveNative(model, effort string) (string, error) {
	canonical, err := c.ResolveCanonical(model)
	if err != nil {
		return "", err
	}
	c.mu.RLock()
	profile := c.byID[canonical]
	c.mu.RUnlock()

	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = profile.DefaultEffort
	}
	if effort != "" {
		if native, ok := profile.NativeByEffort[effort]; ok {
			return native, nil
		}
		return "", fmt.Errorf("model %s does not support reasoning effort %s", canonical, effort)
	}
	if profile.Native != "" {
		return profile.Native, nil
	}
	return "", fmt.Errorf("model %s has no native agy variant", canonical)
}

func (c *ModelCatalog) SupportedEfforts(model string) []string {
	canonical, err := c.ResolveCanonical(model)
	if err != nil {
		return nil
	}
	c.mu.RLock()
	profile := c.byID[canonical]
	c.mu.RUnlock()
	return orderedEfforts(profile.NativeByEffort)
}

func discoverModelIDs(ctx context.Context, binary string) ([]string, error) {
	cmd := exec.CommandContext(ctx, binary, "models")
	hideWindow(cmd)
	cmd.Env = agyEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("agy models: %s", msg)
	}

	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for line := range strings.Lines(stdout.String()) {
		id := strings.TrimSpace(line)
		if id == "" || strings.ContainsAny(id, " \t") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func agyEnvironment() []string {
	env := os.Environ()
	home := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		if current, err := user.Current(); err == nil {
			home = strings.TrimSpace(current.HomeDir)
		}
	}
	if home != "" {
		if os.Getenv("USERPROFILE") == "" {
			env = append(env, "USERPROFILE="+home)
		}
		if os.Getenv("HOME") == "" {
			env = append(env, "HOME="+home)
		}
	}
	return env
}

func normalizeModelIDs(ids []string) []ModelProfile {
	type builder struct {
		profile ModelProfile
	}
	byCanonical := make(map[string]*builder)
	order := make([]string, 0)
	for _, native := range ids {
		canonical, display, effort, thinking, ok := parseNativeModelID(strings.TrimSpace(native))
		if !ok {
			slog.Warn("unrecognized agy model omitted", "model", native)
			continue
		}
		b := byCanonical[canonical]
		if b == nil {
			b = &builder{profile: ModelProfile{CanonicalID: canonical, DisplayName: display, NativeByEffort: make(map[string]string)}}
			byCanonical[canonical] = b
			order = append(order, canonical)
		}
		if effort != "" {
			b.profile.NativeByEffort[effort] = native
		} else {
			b.profile.Native = native
		}
		if thinking && b.profile.Native == "" {
			b.profile.Native = native
		}
	}

	profiles := make([]ModelProfile, 0, len(order))
	for _, canonical := range order {
		p := byCanonical[canonical].profile
		p.DefaultEffort = pickDefaultEffort(p.NativeByEffort)
		profiles = append(profiles, p)
	}
	return profiles
}

func parseNativeModelID(native string) (canonical, display, effort string, thinking, ok bool) {
	if native == "" {
		return "", "", "", false, false
	}
	base := native
	for _, candidate := range []string{ReasoningLow, ReasoningMedium, ReasoningHigh} {
		if strings.HasSuffix(base, "-"+candidate) {
			effort = candidate
			base = strings.TrimSuffix(base, "-"+candidate)
			break
		}
	}
	if strings.HasSuffix(base, "-thinking") {
		thinking = true
		base = strings.TrimSuffix(base, "-thinking")
	}

	switch {
	case strings.HasPrefix(base, "gemini-"):
		name := strings.TrimPrefix(base, "gemini-")
		canonical = "google/gemini-" + name
		display = "Gemini " + displayModelTail(name)
	case strings.HasPrefix(base, "claude-"):
		name := strings.TrimPrefix(base, "claude-")
		name = normalizeClaudeVersion(name)
		canonical = "anthropic/claude-" + name
		display = "Claude " + displayModelTail(name)
	case strings.HasPrefix(base, "gpt-"):
		name := strings.TrimPrefix(base, "gpt-")
		canonical = "openai/gpt-" + name
		display = "GPT " + displayModelTail(name)
	default:
		return "", "", "", false, false
	}
	return canonical, display, effort, thinking, true
}

func normalizeClaudeVersion(name string) string {
	parts := strings.Split(name, "-")
	for i := 0; i+1 < len(parts); i++ {
		if allDigits(parts[i]) && allDigits(parts[i+1]) {
			parts[i] += "." + parts[i+1]
			parts = append(parts[:i+1], parts[i+2:]...)
			break
		}
	}
	return strings.Join(parts, "-")
}

func formatModelDisplayName(id string) string {
	_, display, effort, thinking, ok := parseNativeModelID(id)
	if !ok {
		return id
	}
	if effort != "" {
		display += " (" + strings.ToUpper(effort[:1]) + effort[1:] + ")"
	} else if thinking {
		display += " (Thinking)"
	}
	return display
}

func displayModelTail(name string) string {
	parts := strings.Split(name, "-")
	for i, part := range parts {
		if part == "oss" {
			parts[i] = "OSS"
			continue
		}
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pickDefaultEffort(native map[string]string) string {
	for _, effort := range []string{ReasoningMedium, ReasoningHigh, ReasoningLow} {
		if _, ok := native[effort]; ok {
			return effort
		}
	}
	return ""
}

func pickDefaultModel(profiles []ModelProfile) string {
	for _, preferred := range []string{"google/gemini-3.6-flash", "google/gemini-3.5-flash"} {
		for _, profile := range profiles {
			if profile.CanonicalID == preferred {
				return preferred
			}
		}
	}
	if len(profiles) > 0 {
		return profiles[0].CanonicalID
	}
	return ""
}

func orderedEfforts(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, effort := range []string{ReasoningLow, ReasoningMedium, ReasoningHigh} {
		if _, ok := values[effort]; ok {
			out = append(out, effort)
		}
	}
	unknown := make([]string, 0)
	for effort := range values {
		if effort != ReasoningLow && effort != ReasoningMedium && effort != ReasoningHigh {
			unknown = append(unknown, effort)
		}
	}
	sort.Strings(unknown)
	return append(out, unknown...)
}

func addLegacyAliases(aliases map[string]string, profiles map[string]ModelProfile) {
	legacy := map[string]string{
		"google/gemini-3.5-flash-high":       "google/gemini-3.5-flash",
		"google/gemini-3.5-flash-medium":     "google/gemini-3.5-flash",
		"google/gemini-3.5-flash-low":        "google/gemini-3.5-flash",
		"google/gemini-3.1-pro-high":         "google/gemini-3.1-pro",
		"google/gemini-3.1-pro-low":          "google/gemini-3.1-pro",
		"anthropic/claude-opus-4.6-thinking": "anthropic/claude-opus-4.6",
	}
	for alias, canonical := range legacy {
		if _, ok := profiles[canonical]; ok {
			aliases[alias] = canonical
		}
	}
}
