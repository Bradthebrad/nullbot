package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ContextWindow is how many tokens a model's context holds and where that
// number came from.
type ContextWindow struct {
	Tokens int `json:"tokens"`
	// Source is "override" (set by you), "provider" (reported by the
	// provider's model list), "learned" (named in a context-length error),
	// "built-in" (typical for the model family) or "default" (unknown model).
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

// ModelKey identifies a model in ContextWindows overrides and in the saved
// sizes: "provider/model".
func ModelKey(provider, model string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "openai"
	}
	return provider + "/" + strings.TrimSpace(model)
}

// builtinWindows are context sizes for model families whose size is
// documented and stable. They apply only when nothing better is known; a
// provider's own figure or your override wins. For OpenAI's GPT-5 family the
// figure is the input limit (the window less the room kept for output).
var builtinWindows = []struct {
	prefix string
	tokens int
}{
	{"claude-", 200000},
	{"gpt-5", 272000},
	{"gpt-4.1", 1047576},
	{"gpt-4o", 128000},
	{"o1", 200000},
	{"o3", 200000},
	{"o4", 200000},
	{"gemini-2.5", 1048576},
}

func builtinWindow(model string) int {
	id := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		id = id[slash+1:]
	}
	for _, entry := range builtinWindows {
		if strings.HasPrefix(id, entry.prefix) {
			return entry.tokens
		}
	}
	return 0
}

// ResolveContextWindow returns the context size to plan for with model.
// Order: your override, the size the provider reported, a size learned from a
// context-length error, the family's documented size, then the configured
// fallback.
func ResolveContextWindow(config Config, model ModelConfig) ContextWindow {
	model = effectiveModelConfig(config, model)
	key := ModelKey(model.Provider, model.Model)
	if tokens := config.Compaction.ContextWindows[key]; tokens > 0 {
		return ContextWindow{Tokens: tokens, Source: "override", Detail: "Set in Settings"}
	}
	if saved, ok := savedContextWindows(config)[key]; ok && saved.Tokens > 0 {
		// An old learned size gives way to the family's size; the provider's
		// list or the next error will correct it again.
		if saved.Source != "learned" || time.Since(saved.Time) < learnedWindowTTL {
			return saved.ContextWindow
		}
	}
	if tokens := builtinWindow(model.Model); tokens > 0 {
		return ContextWindow{Tokens: tokens, Source: "built-in", Detail: "Typical for this model family"}
	}
	fallback := config.Compaction.ApproxTokenLimit
	if fallback <= 0 {
		fallback = 64000
	}
	return ContextWindow{Tokens: fallback, Source: "default", Detail: "Size unknown; using the default"}
}

type savedWindow struct {
	ContextWindow
	Time time.Time `json:"time"`
}

// windowStore keeps reported and learned sizes in memory and on disk, so a
// size seen once is known offline and after a restart.
var windowStore = struct {
	sync.Mutex
	loaded map[string]map[string]savedWindow
}{loaded: map[string]map[string]savedWindow{}}

func contextWindowsPath(config Config) string {
	return filepath.Join(config.AppDir, "models", "context_windows.json")
}

func savedContextWindows(config Config) map[string]savedWindow {
	if strings.TrimSpace(config.AppDir) == "" {
		return nil
	}
	windowStore.Lock()
	defer windowStore.Unlock()
	return loadWindowsLocked(contextWindowsPath(config))
}

func loadWindowsLocked(path string) map[string]savedWindow {
	if cached, ok := windowStore.loaded[path]; ok {
		return cached
	}
	out := map[string]savedWindow{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	windowStore.loaded[path] = out
	return out
}

// learnedWindowTTL is how long a size named in an error is trusted over the
// provider's own figure. OpenRouter, for one, names the limit of whichever
// endpoint served the request, which may be smaller than the model's.
const learnedWindowTTL = 7 * 24 * time.Hour

// recordContextWindows saves sizes and reports whether anything changed.
// A recent learned size is kept over a larger reported one: it is what the
// provider actually accepted.
func recordContextWindows(config Config, provider string, sizes map[string]int, source, detail string) bool {
	if len(sizes) == 0 || strings.TrimSpace(config.AppDir) == "" {
		return false
	}
	path := contextWindowsPath(config)
	windowStore.Lock()
	defer windowStore.Unlock()
	current := loadWindowsLocked(path)
	now := time.Now().UTC()
	var next map[string]savedWindow
	for model, tokens := range sizes {
		if tokens < 1024 {
			continue
		}
		key := ModelKey(provider, model)
		old, exists := current[key]
		if exists && old.Tokens == tokens && old.Source == source {
			continue
		}
		if exists && old.Source == "learned" && source == "provider" && old.Tokens < tokens && now.Sub(old.Time) < learnedWindowTTL {
			continue
		}
		if next == nil {
			next = make(map[string]savedWindow, len(current)+len(sizes))
			for k, v := range current {
				next[k] = v
			}
		}
		next[key] = savedWindow{ContextWindow: ContextWindow{Tokens: tokens, Source: source, Detail: detail}, Time: now}
	}
	if next == nil {
		return false
	}
	windowStore.loaded[path] = next
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if data, err := json.MarshalIndent(next, "", "  "); err == nil {
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, data, 0o600); err == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}
	return true
}

// learnContextWindow keeps a size a provider named in a context-length error.
func (a *App) learnContextWindow(model ModelConfig, tokens int) {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	model = effectiveModelConfig(config, model)
	if recordContextWindows(config, model.Provider, map[string]int{model.Model: tokens}, "learned", "Named by the provider in an error") {
		a.logInfo("context window learned", "model", ModelKey(model.Provider, model.Model), "tokens", tokens)
	}
}

// ContextWindowFor reports the context size NullBot plans for with a model.
func (a *App) ContextWindowFor(provider, model string) ContextWindow {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	return ResolveContextWindow(config, ModelConfig{Provider: provider, Model: model})
}

// BaseContextWindowFor reports the size a model would get without your
// override: what its provider reported, what was learned, or its family's.
func (a *App) BaseContextWindowFor(provider, model string) ContextWindow {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	config.Compaction.ContextWindows = nil
	return ResolveContextWindow(config, ModelConfig{Provider: provider, Model: model})
}

// SetContextWindowOverride sets the context size used for a model. Zero
// removes the override, so the reported or typical size applies again.
func (a *App) SetContextWindowOverride(provider, model string, tokens int) error {
	if strings.TrimSpace(model) == "" {
		return errMissingModel
	}
	if tokens != 0 && (tokens < 1024 || tokens > 100_000_000) {
		return errContextSize
	}
	key := ModelKey(provider, model)
	return a.UpdateConfig(func(config *Config) {
		// A new map: the old one is shared with running agents.
		windows := make(map[string]int, len(config.Compaction.ContextWindows)+1)
		for k, v := range config.Compaction.ContextWindows {
			windows[k] = v
		}
		if tokens == 0 {
			delete(windows, key)
		} else {
			windows[key] = tokens
		}
		config.Compaction.ContextWindows = windows
	})
}

var (
	errMissingModel = errors.New("choose a model first")
	errContextSize  = errors.New("context size must be between 1,024 and 100,000,000 tokens")
)
