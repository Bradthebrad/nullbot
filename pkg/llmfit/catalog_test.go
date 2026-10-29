package llmfit

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedCatalog(t *testing.T) {
	models := Catalog()
	if len(models) < 1000 {
		t.Fatalf("only %d models", len(models))
	}
	if len(catalogGz) > 400<<10 {
		t.Errorf("embedded catalog is %d KB, over the 400 KB budget", len(catalogGz)>>10)
	}
	slugs := map[string]bool{}
	for _, m := range models {
		if slugs[CanonicalSlug(m.Name)] {
			t.Errorf("duplicate model %s", m.Name)
		}
		slugs[CanonicalSlug(m.Name)] = true
		if issue := m.SanityIssue(); issue != "" {
			t.Errorf("%s: %s", m.Name, issue)
		}
		if m.ParametersRaw < 100_000_000 || m.ContextLength < 2048 || m.format() != "gguf" {
			t.Errorf("%s breaks the selection rules: %+v", m.Name, m)
		}
	}
	want := map[string]string{
		"Qwen/Qwen3-8B":                           "qwen3:8b",
		"meta-llama/Llama-3.1-8B-Instruct":        "llama3.1:8b",
		"mistralai/Mistral-7B-Instruct-v0.3":      "mistral:7b",
		"google/gemma-3-12b-it":                   "gemma3:12b",
		"microsoft/phi-4":                         "phi4",
		"deepseek-ai/DeepSeek-R1-Distill-Qwen-7B": "deepseek-r1:7b",
		"openai/gpt-oss-20b":                      "",
	}
	found := map[string]Model{}
	for _, m := range models {
		found[m.Name] = m
	}
	for name, tag := range want {
		m, ok := found[name]
		if !ok {
			t.Errorf("%s missing from the catalog", name)
			continue
		}
		if OllamaTag(m.Name) != tag {
			t.Errorf("%s: Ollama tag %q, want %q", name, OllamaTag(m.Name), tag)
		}
	}
	// Callers get their own slice.
	models[0].Name = "changed"
	if Catalog()[0].Name == "changed" {
		t.Error("Catalog shares its slice")
	}
}

const catalogJSON = `[
 {"name": "meta-llama/Llama-3.1-8B-Instruct", "provider": "Meta", "parameter_count": "8.0B", "parameters_raw": 8030261248,
  "min_ram_gb": 4.5, "recommended_ram_gb": 9, "min_vram_gb": 4.5, "quantization": "Q4_K_M", "format": "gguf",
  "context_length": 131072, "use_case": "Instruction following, chat", "capabilities": [], "pipeline_tag": "text-generation",
  "architecture": "llama", "hf_downloads": 100, "release_date": "2024-07-18", "num_hidden_layers": 32,
  "num_attention_heads": 32, "num_key_value_heads": 8, "head_dim": null, "license": null, "_discovered": true,
  "gguf_sources": [{"repo": "bartowski/Meta-Llama-3.1-8B-Instruct-GGUF", "provider": "bartowski"}]},
 {"name": "unsloth/llama-3.1-8b-instruct", "provider": "unsloth", "parameter_count": "8.0B", "parameters_raw": 8030261248,
  "min_ram_gb": 4.5, "recommended_ram_gb": 9, "min_vram_gb": null, "quantization": "Q4_K_M",
  "context_length": 131072, "use_case": "Instruction following, chat", "hf_downloads": 5000, "head_dim": 128,
  "gguf_sources": [{"repo": "unsloth/Llama-3.1-8B-Instruct-GGUF", "provider": "unsloth"}]},
 {"name": "openai/gpt-oss-120b", "provider": "OpenAI", "parameter_count": "116.8B", "parameters_raw": 116829156672,
  "min_ram_gb": 60, "recommended_ram_gb": 120, "quantization": "Q4_K_M", "context_length": 131072,
  "use_case": "General purpose text generation", "architecture": "gpt_oss",
  "is_moe": true, "num_experts": 128, "active_experts": 4, "active_parameters": 9310000000},
 {"name": "Qwen/Qwen2.5-VL-7B-Instruct", "provider": "Qwen", "parameter_count": "8.3B", "parameters_raw": 8292166656,
  "min_ram_gb": 4.6, "recommended_ram_gb": 9, "quantization": "Q4_K_M", "context_length": 128000,
  "use_case": "Multimodal, vision and text"}
]`

func TestLoadCatalog(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "hf_models.json")
	if err := os.WriteFile(plain, []byte(catalogJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(catalogJSON))
	zw.Close()
	compressed := filepath.Join(dir, "hf_models.json.gz")
	if err := os.WriteFile(compressed, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{plain, compressed} {
		models, err := LoadCatalog(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 3 {
			t.Fatalf("%s: %d models, want the two Llama uploads merged into one", path, len(models))
		}
		llama := models[0]
		// The first entry's name wins; the details merge.
		if llama.Name != "meta-llama/Llama-3.1-8B-Instruct" || llama.HeadDim != 128 || llama.Downloads != 5000 ||
			len(llama.GGUFSources) != 2 || llama.MinVRAMGB != 4.5 {
			t.Errorf("merged entry: %+v", llama)
		}
		if !llama.HasCapability(CapToolUse) {
			t.Errorf("tool use not inferred: %v", llama.Capabilities)
		}
		// gpt-oss-120b's scraped active count is corrected to the card's 5.1B.
		if models[1].ActiveParameters != 5_100_000_000 {
			t.Errorf("gpt-oss active parameters = %d", models[1].ActiveParameters)
		}
		if !models[2].HasCapability(CapVision) || models[2].UseCase() != UseMultimodal {
			t.Errorf("VL model: %+v", models[2])
		}
	}
	if _, err := LoadCatalog(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file should be an error")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"not": "an array"}`), 0o644)
	if _, err := LoadCatalog(bad); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("bad file error = %v", err)
	}
}

func TestCanonicalSlug(t *testing.T) {
	if CanonicalSlug("meta-llama/Llama-3.1-8B") != CanonicalSlug("someone/llama_3.1-8b") {
		t.Error("slugs should ignore organization, case and separators")
	}
	if CanonicalSlug("Qwen/Qwen3-8B") == CanonicalSlug("Qwen/Qwen3-14B") {
		t.Error("different models share a slug")
	}
}
