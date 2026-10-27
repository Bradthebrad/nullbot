package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelOption struct {
	Provider         string `json:"provider"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Reasoning        bool   `json:"reasoning"`
	Responses        bool   `json:"responses"`
	APIKeyEnv        string `json:"api_key_env"`
	OpenAICompatible bool   `json:"openai_compatible,omitempty"`
	// ContextWindow is the size NullBot plans for with this model, and
	// ContextSource where it came from (see ContextWindow.Source).
	ContextWindow int    `json:"context_window,omitempty"`
	ContextSource string `json:"context_source,omitempty"`
}

type ModelGroup struct {
	Provider string        `json:"provider"`
	Models   []ModelOption `json:"models"`
	Error    string        `json:"error,omitempty"`
}

func ModelCatalog() []ModelOption {
	return []ModelOption{
		{
			Provider:    "codex",
			ID:          "gpt-5.5",
			Name:        "Codex GPT-5.5",
			Description: "Uses your ChatGPT/Codex subscription sign-in instead of OpenAI API-key billing.",
			Reasoning:   true,
			APIKeyEnv:   "CODEX_AUTH",
		},
		{
			Provider:    "codex",
			ID:          "gpt-5.4-mini",
			Name:        "Codex GPT-5.4 mini",
			Description: "Faster Codex-backed model for lighter work and subagents, using subscription auth.",
			Reasoning:   true,
			APIKeyEnv:   "CODEX_AUTH",
		},
		{
			Provider:    "codex",
			ID:          "gpt-5.3-codex-spark",
			Name:        "Codex Spark",
			Description: "Research-preview Codex model for fast coding iteration when your subscription allows it.",
			Reasoning:   true,
			APIKeyEnv:   "CODEX_AUTH",
		},
		{
			Provider:    "openai",
			ID:          "gpt-5.2",
			Name:        "GPT-5.2",
			Description: "OpenAI frontier model for complex agentic work.",
			Reasoning:   true,
			Responses:   true,
			APIKeyEnv:   "OPENAI_API_KEY",
		},
		{
			Provider:    "openai",
			ID:          "gpt-5-mini",
			Name:        "GPT-5 mini",
			Description: "Faster OpenAI model for well-defined tasks.",
			Reasoning:   true,
			Responses:   true,
			APIKeyEnv:   "OPENAI_API_KEY",
		},
		{
			Provider:    "openai",
			ID:          "gpt-4.1",
			Name:        "GPT-4.1",
			Description: "Strong non-reasoning model with tool support.",
			Responses:   true,
			APIKeyEnv:   "OPENAI_API_KEY",
		},
		{
			Provider:    "openai",
			ID:          "gpt-4.1-mini",
			Name:        "GPT-4.1 mini",
			Description: "Small OpenAI model for low-latency interactions.",
			Responses:   true,
			APIKeyEnv:   "OPENAI_API_KEY",
		},
		{
			Provider:    "anthropic",
			ID:          "claude-sonnet-4-20250514",
			Name:        "Claude Sonnet 4",
			Description: "Anthropic balanced model.",
			Reasoning:   true,
			APIKeyEnv:   "ANTHROPIC_API_KEY",
		},
		{
			Provider:    "anthropic",
			ID:          "claude-3-5-haiku-20241022",
			Name:        "Claude 3.5 Haiku",
			Description: "Anthropic fast model.",
			APIKeyEnv:   "ANTHROPIC_API_KEY",
		},
		{
			Provider:         "openrouter",
			ID:               "openai/gpt-5.2",
			Name:             "OpenRouter GPT-5.2",
			Description:      "OpenAI GPT-5.2 through OpenRouter.",
			Reasoning:        true,
			OpenAICompatible: true,
			APIKeyEnv:        "OPENROUTER_API_KEY",
		},
		{
			Provider:         "openrouter",
			ID:               "anthropic/claude-sonnet-4",
			Name:             "OpenRouter Claude Sonnet",
			Description:      "Claude Sonnet through OpenRouter.",
			Reasoning:        true,
			OpenAICompatible: true,
			APIKeyEnv:        "OPENROUTER_API_KEY",
		},
		{
			Provider:         "openrouter",
			ID:               "google/gemini-2.5-pro",
			Name:             "OpenRouter Gemini Pro",
			Description:      "Gemini Pro through OpenRouter.",
			Reasoning:        true,
			OpenAICompatible: true,
			APIKeyEnv:        "OPENROUTER_API_KEY",
		},
	}
}

func DiscoverModelGroups(ctx context.Context, config Config) []ModelGroup {
	keys, _ := LoadAPIKeys(config)
	groups := []ModelGroup{
		{Provider: "codex", Models: staticProviderModels("codex")},
		{Provider: "openai", Models: staticProviderModels("openai")},
		{Provider: "anthropic", Models: staticProviderModels("anthropic")},
		{Provider: "openrouter", Models: staticProviderModels("openrouter")},
	}
	if CodexAuthPresent(config) {
		models, err := FetchCodexModelInfo(ctx, config)
		if err != nil {
			groups[0].Error = err.Error()
		} else if len(models) > 0 {
			groups[0].Models = codexModelInfoOptions(models)
		}
	}
	if keys.OpenAI != "" {
		models, err := fetchOpenAIModels(ctx, keys.OpenAI)
		if err != nil {
			groups[1].Error = err.Error()
		} else if len(models) > 0 {
			groups[1].Models = models
		}
	}
	if keys.Anthropic != "" {
		models, err := fetchAnthropicModels(ctx, keys.Anthropic)
		if err != nil {
			groups[2].Error = err.Error()
		} else if len(models) > 0 {
			groups[2].Models = models
		}
	}
	if keys.OpenRouter != "" {
		models, err := fetchOpenRouterModels(ctx, keys.OpenRouter)
		if err != nil {
			groups[3].Error = err.Error()
		} else if len(models) > 0 {
			groups[3].Models = models
		}
	}
	for g := range groups {
		reported := map[string]int{}
		for _, model := range groups[g].Models {
			if model.ContextWindow > 0 {
				reported[model.ID] = model.ContextWindow
			}
		}
		recordContextWindows(config, groups[g].Provider, reported, "provider", "Reported by "+providerTitle(groups[g].Provider))
	}
	return withContextWindows(config, groups)
}

// withContextWindows fills in the size NullBot plans for with each model, so
// overrides set after a fetch show without fetching again.
func withContextWindows(config Config, groups []ModelGroup) []ModelGroup {
	for g := range groups {
		for i, model := range groups[g].Models {
			window := ResolveContextWindow(config, ModelConfig{Provider: model.Provider, Model: model.ID})
			groups[g].Models[i].ContextWindow = window.Tokens
			groups[g].Models[i].ContextSource = window.Source
		}
	}
	return groups
}

func providerTitle(provider string) string {
	switch provider {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "openrouter":
		return "OpenRouter"
	case "codex":
		return "Codex"
	}
	return provider
}

func fetchAnthropicModels(ctx context.Context, apiKey string) ([]ModelOption, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	var resp struct {
		Data []struct {
			ID             string `json:"id"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := doJSON(req, &resp); err != nil {
		return nil, err
	}
	models := make([]ModelOption, 0, len(resp.Data))
	for _, model := range resp.Data {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		models = append(models, ModelOption{
			Provider:      "anthropic",
			ID:            model.ID,
			Name:          firstNonEmpty(model.DisplayName, model.ID),
			Description:   "Anthropic model.",
			Reasoning:     !strings.HasPrefix(model.ID, "claude-3"),
			APIKeyEnv:     "ANTHROPIC_API_KEY",
			ContextWindow: model.MaxInputTokens,
		})
	}
	sortModels(models)
	return models, nil
}

func codexModelOptions(ids []string) []ModelOption {
	out := make([]ModelOption, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out = append(out, ModelOption{
			Provider:    "codex",
			ID:          id,
			Name:        "Codex " + id,
			Description: "Available through the signed-in ChatGPT/Codex subscription.",
			Reasoning:   true,
			APIKeyEnv:   "CODEX_AUTH",
		})
	}
	sortModels(out)
	return out
}

func codexModelInfoOptions(models []CodexModelInfo) []ModelOption {
	ids := make([]string, 0, len(models))
	windows := map[string]int{}
	for _, model := range models {
		ids = append(ids, model.Slug)
		windows[model.Slug] = model.ContextWindow
	}
	out := codexModelOptions(ids)
	for i := range out {
		out[i].ContextWindow = windows[out[i].ID]
	}
	return out
}

func FlattenModelGroups(groups []ModelGroup) []ModelOption {
	var out []ModelOption
	for _, group := range groups {
		out = append(out, group.Models...)
	}
	return out
}

func CurrentModelIndex(config Config) int {
	return CurrentModelIndexIn(config, ModelCatalog())
}

func CurrentModelIndexIn(config Config, options []ModelOption) int {
	for i, option := range options {
		if option.Provider == config.Model.Provider && option.ID == config.Model.Model {
			return i
		}
	}
	return 0
}

func staticProviderModels(provider string) []ModelOption {
	var out []ModelOption
	for _, option := range ModelCatalog() {
		if option.Provider == provider {
			out = append(out, option)
		}
	}
	return out
}

func fetchOpenAIModels(ctx context.Context, apiKey string) ([]ModelOption, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := doJSON(req, &resp); err != nil {
		return nil, err
	}
	var models []ModelOption
	for _, model := range resp.Data {
		if !looksChatModel(model.ID) {
			continue
		}
		models = append(models, ModelOption{
			Provider:    "openai",
			ID:          model.ID,
			Name:        model.ID,
			Description: "OpenAI model owned by " + model.OwnedBy,
			Reasoning:   strings.HasPrefix(model.ID, "gpt-5") || strings.HasPrefix(model.ID, "o"),
			Responses:   true,
			APIKeyEnv:   "OPENAI_API_KEY",
		})
	}
	sortModels(models)
	return models, nil
}

func fetchOpenRouterModels(ctx context.Context, apiKey string) ([]ModelOption, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models?output_modalities=text", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Description   string `json:"description"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				ContextLength int `json:"context_length"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := doJSON(req, &resp); err != nil {
		return nil, err
	}
	models := make([]ModelOption, 0, len(resp.Data))
	for _, model := range resp.Data {
		models = append(models, ModelOption{
			Provider:         "openrouter",
			ID:               model.ID,
			Name:             firstNonEmpty(model.Name, model.ID),
			Description:      model.Description,
			Reasoning:        strings.Contains(strings.ToLower(model.ID), "gpt-5") || strings.Contains(strings.ToLower(model.ID), "reason"),
			OpenAICompatible: true,
			APIKeyEnv:        "OPENROUTER_API_KEY",
			ContextWindow:    firstPositive(model.TopProvider.ContextLength, model.ContextLength),
		})
	}
	sortModels(models)
	return models, nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func doJSON(req *http.Request, out any) error {
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func looksChatModel(id string) bool {
	id = strings.ToLower(id)
	return strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "o") || strings.Contains(id, "chat")
}

func sortModels(models []ModelOption) {
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const modelCatalogTTL = 10 * time.Minute

var modelCatalogCache struct {
	sync.Mutex
	key     string
	fetched time.Time
	groups  []ModelGroup
}

// CachedModelGroups returns the provider model catalog, fetching it at most
// once per TTL for the same set of credentials. Pass refresh to force a fetch.
func modelCatalogKey(config Config, keys APIKeys) string {
	return fmt.Sprintf("%t|%s|%s|%s|%s", CodexAuthPresent(config), MaskSecret(keys.OpenAI), MaskSecret(keys.Anthropic), MaskSecret(keys.OpenRouter), config.AppDir)
}

func CachedModelGroups(ctx context.Context, config Config, refresh bool) []ModelGroup {
	keys, _ := LoadAPIKeys(config)
	key := modelCatalogKey(config, keys)
	modelCatalogCache.Lock()
	if !refresh && modelCatalogCache.key == key && time.Since(modelCatalogCache.fetched) < modelCatalogTTL && modelCatalogCache.groups != nil {
		groups := cloneModelGroups(modelCatalogCache.groups)
		modelCatalogCache.Unlock()
		return withContextWindows(config, groups)
	}
	modelCatalogCache.Unlock()
	groups := DiscoverModelGroups(ctx, config)
	modelCatalogCache.Lock()
	modelCatalogCache.key, modelCatalogCache.fetched, modelCatalogCache.groups = key, time.Now(), cloneModelGroups(groups)
	modelCatalogCache.Unlock()
	return groups
}

// PeekModelGroups returns the cached catalog without any network call. fresh
// is false when the cache is empty, stale or for other credentials; callers
// can then fetch with CachedModelGroups in the background.
func PeekModelGroups(config Config) (groups []ModelGroup, fresh bool) {
	keys, _ := LoadAPIKeys(config)
	key := modelCatalogKey(config, keys)
	modelCatalogCache.Lock()
	defer modelCatalogCache.Unlock()
	if modelCatalogCache.groups != nil && modelCatalogCache.key == key {
		return withContextWindows(config, cloneModelGroups(modelCatalogCache.groups)), time.Since(modelCatalogCache.fetched) < modelCatalogTTL
	}
	return withContextWindows(config, StaticModelGroups()), false
}

// StaticModelGroups is the built-in catalog used before any fetch.
func StaticModelGroups() []ModelGroup {
	return []ModelGroup{
		{Provider: "codex", Models: staticProviderModels("codex")},
		{Provider: "openai", Models: staticProviderModels("openai")},
		{Provider: "anthropic", Models: staticProviderModels("anthropic")},
		{Provider: "openrouter", Models: staticProviderModels("openrouter")},
	}
}

func cloneModelGroups(groups []ModelGroup) []ModelGroup {
	out := make([]ModelGroup, len(groups))
	for i, g := range groups {
		g.Models = append([]ModelOption(nil), g.Models...)
		out[i] = g
	}
	return out
}
