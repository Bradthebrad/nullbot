package llmfit

import "testing"

func TestMappingTablesCopiedWhole(t *testing.T) {
	// llmfit's providers.rs has 148 Ollama and 45 GGUF mappings.
	if len(ollamaTags) != 148 || len(ggufRepos) != 45 {
		t.Fatalf("tables have %d Ollama and %d GGUF entries", len(ollamaTags), len(ggufRepos))
	}
}

func TestOllamaTag(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen/Qwen3-8B":                        "qwen3:8b",
		"meta-llama/Llama-3.1-8B-Instruct":     "llama3.1:8b",
		"unsloth/Llama-3.1-8B-Instruct":        "llama3.1:8b", // any organization
		"mistralai/Mixtral-8x7B-Instruct-v0.1": "mixtral:8x7b",
		"microsoft/phi-4":                      "phi4",
		"acme/no-such-model":                   "",
	} {
		if got := OllamaTag(name); got != want {
			t.Errorf("OllamaTag(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestGGUFRepo(t *testing.T) {
	m := Model{Name: "Qwen/Qwen3-8B"}
	if got := GGUFRepo(m); got != "bartowski/Qwen3-8B-GGUF" {
		t.Errorf("table lookup = %q", got)
	}
	m.GGUFSources = []GGUFSource{{Repo: "unsloth/Qwen3-8B-GGUF", Provider: "unsloth"}}
	if got := GGUFRepo(m); got != "unsloth/Qwen3-8B-GGUF" {
		t.Errorf("catalog source should win, got %q", got)
	}
	if got := GGUFRepo(Model{Name: "acme/unknown"}); got != "" {
		t.Errorf("unknown model = %q", got)
	}
}

func TestMLXRepo(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen/Qwen3-8B":                           "mlx-community/Qwen3-8B-4bit",
		"meta-llama/Llama-3.2-1B-Instruct":        "mlx-community/Llama-3.2-1B-Instruct-4bit",
		"mlx-community/Qwen3-8B-8bit":             "mlx-community/Qwen3-8B-8bit", // already MLX
		"lmstudio-community/Qwen3.8-27B-MLX-4bit": "lmstudio-community/Qwen3.8-27B-MLX-4bit",
		"Qwen/Qwen3-14B-AWQ":                      "", // no MLX twin of an AWQ repo
	} {
		if got := MLXRepo(name); got != want {
			t.Errorf("MLXRepo(%q) = %q, want %q", name, got, want)
		}
	}
	if got := stripMLXQuantSuffix("Llama-3.2-1B-Instruct-MLX-4bit-dwq"); got != "Llama-3.2-1B-Instruct" {
		t.Errorf("stripMLXQuantSuffix = %q", got)
	}
}
