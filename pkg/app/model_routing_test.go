package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tcagent "github.com/Bradthebrad/tinychain/agent"
)

func configWithCodexAuth(t *testing.T) Config {
	t.Helper()
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	auth := codexAuthStore{
		Provider:     "codex",
		AuthMode:     "chatgpt",
		BaseURL:      defaultCodexBackendURL,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.AppDir, "api", "codex_auth.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestOpenAIModelUsesAPIPathEvenWhenCodexAuthExists(t *testing.T) {
	config := configWithCodexAuth(t)
	if err := SaveAPIKeys(config, APIKeys{OpenAI: "sk-test"}); err != nil {
		t.Fatal(err)
	}

	model, err := modelFromModelConfig(config, ModelConfig{Provider: "openai", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	openAIModel, ok := model.(tcagent.OpenAIModel)
	if !ok {
		t.Fatalf("model type = %T, want tcagent.OpenAIModel", model)
	}
	if openAIModel.Provider != "openai" || openAIModel.Model != "gpt-4o-mini" {
		t.Fatalf("model route = provider %q model %q", openAIModel.Provider, openAIModel.Model)
	}
}

func TestOpenRouterModelUsesOpenRouterPathEvenWhenCodexAuthExists(t *testing.T) {
	config := configWithCodexAuth(t)
	if err := SaveAPIKeys(config, APIKeys{OpenRouter: "sk-or-test"}); err != nil {
		t.Fatal(err)
	}

	model, err := modelFromModelConfig(config, ModelConfig{Provider: "openrouter", Model: "openai/gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	openRouterModel, ok := model.(tcagent.OpenAIModel)
	if !ok {
		t.Fatalf("model type = %T, want tcagent.OpenAIModel", model)
	}
	if openRouterModel.Provider != "openrouter" || openRouterModel.Model != "openai/gpt-4o-mini" {
		t.Fatalf("model route = provider %q model %q", openRouterModel.Provider, openRouterModel.Model)
	}
}

func TestCodexModelUsesSubscriptionPath(t *testing.T) {
	config := configWithCodexAuth(t)

	model, err := modelFromModelConfig(config, ModelConfig{Provider: "codex", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	codexModel, ok := model.(CodexSubscriptionModel)
	if !ok {
		t.Fatalf("model type = %T, want CodexSubscriptionModel", model)
	}
	if codexModel.Model != "gpt-5.5" {
		t.Fatalf("codex model = %q", codexModel.Model)
	}
}

func TestEffectiveModelConfigDoesNotRewriteOpenAIToCodex(t *testing.T) {
	config := configWithCodexAuth(t)
	config.Model = ModelConfig{Provider: "openai", Model: "gpt-4o-mini"}

	effective := effectiveModelConfig(config, config.Model)
	if effective.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", effective.Provider)
	}
}
