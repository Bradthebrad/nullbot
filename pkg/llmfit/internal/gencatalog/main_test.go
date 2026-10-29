package main

import (
	"fmt"
	"testing"

	"github.com/Bradthebrad/nullbot/pkg/llmfit"
)

func model(name string, params float64, downloads uint64) llmfit.Model {
	return llmfit.Model{
		Name: name, ParametersRaw: uint64(params * 1e9), MinRAMGB: params * 0.6, Quantization: "Q4_K_M",
		ContextLength: 32768, Purpose: "Instruction following, chat", PipelineTag: "text-generation",
		Architecture: "llama", Downloads: downloads,
	}
}

func TestEligible(t *testing.T) {
	ok := model("Qwen/Qwen3-8B", 8.2, 1)
	if !eligible(ok) {
		t.Fatal("a plain release should be eligible")
	}
	rejects := map[string]func(*llmfit.Model){
		"repack":     func(m *llmfit.Model) { m.Name = "Qwen/Qwen3-8B-FP8" },
		"awq format": func(m *llmfit.Model) { m.Format = "awq" },
		"embedding":  func(m *llmfit.Model) { m.Name = "Qwen/Qwen3-Embedding-8B" },
		"speech":     func(m *llmfit.Model) { m.PipelineTag = "text-to-speech" },
		"tiny":       func(m *llmfit.Model) { m.ParametersRaw = 50_000_000 },
		"short":      func(m *llmfit.Model) { m.ContextLength = 1024 },
		"draft head": func(m *llmfit.Model) { m.Name = "acme/Qwen3-8B-DFlash" },
	}
	for why, change := range rejects {
		m := ok
		change(&m)
		if eligible(m) {
			t.Errorf("%s should not be eligible", why)
		}
	}
}

func TestSelectModels(t *testing.T) {
	all := []llmfit.Model{
		model("SwinliQ-AIs/Qwen3-8B", 8.2, 0), // re-upload, merged into the maker's entry
		model("Qwen/Qwen3-8B", 8.2, 9_000_000),
		model("TinyLlama/TinyLlama-1.1B-Chat-v1.0", 1.1, 5), // mapped to an Ollama tag
		model("nobody/obscure-7b", 7, 50),                   // unpopular
	}
	all[0].GGUFSources = []llmfit.GGUFSource{{Repo: "someone/Qwen3-8B-GGUF"}}
	for i := range 15 { // popular community fine-tunes of one family
		all = append(all, model(fmt.Sprintf("tuner%d/llama-ft-%d-8b", i/5, i), 8, uint64(100_000+i)))
	}
	kept, makers, mapped, popular := selectModels(all)
	// The re-upload counts as mapped too: its name has an Ollama tag.
	if makers != 1 || mapped != 2 {
		t.Fatalf("makers %d, mapped %d", makers, mapped)
	}
	// 10 per family at most, and 3 per publisher: tuner0..tuner2 give 9.
	if popular != 9 {
		t.Fatalf("popular %d, want 9", popular)
	}
	names := map[string]llmfit.Model{}
	for _, m := range kept {
		names[m.Name] = m
	}
	if _, ok := names["SwinliQ-AIs/Qwen3-8B"]; ok {
		t.Error("re-upload kept as its own entry")
	}
	if q := names["Qwen/Qwen3-8B"]; len(q.GGUFSources) != 1 {
		t.Errorf("re-upload's GGUF source not merged: %+v", q)
	}
	if _, ok := names["nobody/obscure-7b"]; ok {
		t.Error("unpopular model kept")
	}
	if len(kept) != 11 {
		t.Errorf("kept %d models", len(kept))
	}
}
