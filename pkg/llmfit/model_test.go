package llmfit

import (
	"math"
	"strings"
	"testing"
)

// testModel is a plain 7B GGUF model without architecture metadata.
func testModel(name string, params float64) Model {
	return Model{
		Name:           name,
		Provider:       "test",
		ParameterCount: "",
		ParametersRaw:  uint64(params * 1e9),
		MinRAMGB:       params * 0.6,
		Quantization:   "Q4_K_M",
		ContextLength:  4096,
		Purpose:        "General",
	}
}

// kvTestModel is shaped like Llama-3.1-8B: 32 layers, 8 KV heads of 128.
func kvTestModel(name string) Model {
	m := testModel(name, 8)
	m.ContextLength = 8192
	m.NumAttentionHeads, m.NumKeyValueHeads, m.NumHiddenLayers, m.HeadDim = 32, 8, 32, 128
	return m
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestEstimateMemory(t *testing.T) {
	m := testModel("Test Model", 7)
	q4 := m.EstimateMemoryGB("Q4_K_M", 4096)
	// 7B x 0.58 bytes = 4.06 GB, plus KV cache and overhead.
	if q4 <= 4 || q4 >= 6 {
		t.Fatalf("7B Q4_K_M at 4k = %.2f GB, want 4-6", q4)
	}
	if q8 := m.EstimateMemoryGB("Q8_0", 4096); q8 <= q4 {
		t.Fatalf("Q8_0 (%.2f) should need more than Q4_K_M (%.2f)", q8, q4)
	}
}

func TestBestQuantForBudget(t *testing.T) {
	m := testModel("Test Model", 7)
	if q, _, ok := m.bestQuantForBudget(10, 4096, quantHierarchy); !ok || q != "Q8_0" {
		t.Fatalf("10 GB budget: got %q %v, want Q8_0", q, ok)
	}
	if _, _, ok := m.bestQuantForBudget(5, 4096, quantHierarchy); !ok {
		t.Fatal("5 GB budget should fit some quant")
	}
	if _, _, ok := m.bestQuantForBudget(1, 4096, quantHierarchy); ok {
		t.Fatal("1 GB budget should fit nothing")
	}
}

func TestKVCachePreciseFormula(t *testing.T) {
	// 32 layers x 2 (K+V) x 8 heads x 128 x 8192 tokens x 2 bytes = 1 GiB.
	if kv := kvTestModel("Llama-3.1-8B").kvCacheGB(8192); !near(kv, 1.0, 0.05) {
		t.Fatalf("kv = %.4f GB, want ~1.0", kv)
	}
	// Without metadata the coarse approximation still gives a cache.
	m := kvTestModel("nameless")
	m.NumHiddenLayers, m.HeadDim = 0, 0
	if kv := m.kvCacheGB(8192); kv <= 0 {
		t.Fatalf("fallback kv = %v", kv)
	}
}

func TestKVCacheHybridCountsOnlyFullAttention(t *testing.T) {
	dense := kvTestModel("dense")
	dense.NumHiddenLayers = 40
	hybrid := dense
	hybrid.Name = "Qwen/Qwen3.5-35B-A3B" // 10 full + 30 linear layers
	ratio := hybrid.kvCacheGB(8192) / dense.kvCacheGB(8192)
	if !near(ratio, 0.25, 0.01) {
		t.Fatalf("hybrid/dense KV = %.3f, want 0.25", ratio)
	}
	// A family template scales to the model's own layer count.
	if l := (attentionLayout{16, 48}).normalizedFor(24); l != (attentionLayout{6, 18}) {
		t.Fatalf("16/48 on 24 layers = %+v, want 6/18", l)
	}
}

func TestParamsB(t *testing.T) {
	cases := []struct {
		name string
		raw  uint64
		want float64
	}{
		// Repacks report packed tensor counts; the name wins.
		{"gittensor-model-hub/Qwen3.8-27B-NVFP4-RTX5090", 15_200_000_000, 27},
		{"OsaurusAI/Qwen3.8-27B-MXFP8", 7_900_000_000, 27},
		{"Qwen/Qwen3.6-35B-A3B-AWQ", 9_000_000_000, 35},
		// Without a repack marker the catalog figure stands.
		{"Qwen/Qwen3.6-35B-A3B", 9_000_000_000, 9},
		{"meta-llama/Llama-3.1-8B-Instruct", 8_030_000_000, 8.03},
		{"moonshotai/Kimi-K2.7-Code", 9_000_000_000, 9},
		{"mistralai/Mixtral-8x7B-Instruct-v0.1", 46_700_000_000, 46.7},
		{"stabilityai/stablelm-2-1_6b", 1_600_000_000, 1.6},
		{"internlm/internlm2_5-7b-chat", 7_700_000_000, 7.7},
		// Draft heads keep the parent's size token but aren't that size.
		{"acme/Nemotron-3.5-Lightning-30B-A3B-NVFP4-DFlash", 663_000_000, 0.663},
		// "17B-16E" is 17B active across 16 experts, not a 17B total.
		{"acme/Llama-4-Scout-17B-16E-Instruct-NVFP4", 108_000_000_000, 108},
	}
	for _, c := range cases {
		m := kvTestModel(c.name)
		m.ParametersRaw = c.raw
		if got := m.ParamsB(); !near(got, c.want, 0.01) {
			t.Errorf("%s: ParamsB = %v, want %v", c.name, got, c.want)
		}
	}
	// Strings work too, and an unknown size defaults to 7B like llmfit.
	if got := (Model{ParameterCount: "137M"}).ParamsB(); !near(got, 0.137, 1e-9) {
		t.Errorf("137M = %v", got)
	}
	if got := (Model{Name: "acme/mystery"}).ParamsB(); got != 7 {
		t.Errorf("unknown size = %v, want 7", got)
	}
}

func TestSanityIssue(t *testing.T) {
	model := func(name string, params float64, minRAM float64) Model {
		m := testModel(name, params)
		m.MinRAMGB = minRAM
		return m
	}
	flagged := []Model{
		model("yuhuili/EAGLE-LLaMA3-Instruct-70B", 3, 2),
		model("RedHatAI/Llama-3.3-70B-Instruct-speculator.eagle3", 3, 2),
		model("LiquidAI/LFM2.5-1.2B-Instruct-DSpark", 3, 2),
		model("acme/Llama-Clone-70B-Instruct", 3.2, 2), // size/name divergence
		model("acme/impossible-117b", 117, 1.2),        // 0.08 bits per parameter
		model("acme/impossible-117b-floor", 117, 1.0),  // the 1 GB floor excuses only the upper bound
	}
	for _, m := range flagged {
		if m.SanityIssue() == "" {
			t.Errorf("%s should be flagged", m.Name)
		}
	}
	clean := []Model{
		model("SC117/Ornith-1.0-35B-Heretic-MTP", 35, 20), // MTP is not a draft marker
		model("Qwen/Qwen3-30B-A3B", 30.5, 18),             // A3B is the active count
		model("Qwen/Qwen3-8B", 8.2, 5),
		model("HuggingFaceTB/SmolLM2-135M-Instruct", 0.1345, 1.0), // at the scraper's RAM floor
		model("HuggingFaceTB/SmolLM2-1.7B-Instruct", 1.711, 1.2),
		model("acme/plausible-7b-f16", 7, 14),
		{Name: "acme/mystery-model", ParameterCount: "Unknown", MinRAMGB: 4},
	}
	for _, m := range clean {
		if issue := m.SanityIssue(); issue != "" {
			t.Errorf("%s flagged: %s", m.Name, issue)
		}
	}
	for name, want := range map[string]float64{
		"SmolLM2-1.7B": 1.7, "Qwen3.5-0.8B": 0.8, "acme-7B.gguf": 7, "SmolLM2-1.7B.gguf": 1.7, "Qwen3.5-0.8B.Q4_K_M.gguf": 0.8,
	} {
		if got, ok := nameDerivedParamsB(name); !ok || got != want {
			t.Errorf("nameDerivedParamsB(%q) = %v %v, want %v", name, got, ok, want)
		}
	}
}

func TestMoEMemory(t *testing.T) {
	m := testModel("Mixtral", 46.7)
	if _, ok := m.moeActiveVRAMGB("Q4_K_M"); ok {
		t.Fatal("dense model has no active-expert VRAM")
	}
	m.IsMoE, m.NumExperts, m.ActiveExperts, m.ActiveParameters = true, 8, 2, 12_900_000_000
	active, ok := m.moeActiveVRAMGB("Q4_K_M")
	// 12.9B x 0.58 bytes x 1.1.
	if !ok || !near(active, 12.9e9*0.58/(1<<30)*1.1, 1e-9) {
		t.Fatalf("active VRAM = %v %v", active, ok)
	}
	off, ok := m.moeOffloadedRAMGB()
	if !ok || !near(off, (46.7e9-12.9e9)*0.58/(1<<30), 1e-6) {
		t.Fatalf("offloaded RAM = %v %v", off, ok)
	}
}

func TestUseCaseInference(t *testing.T) {
	cases := map[UseCase][]Model{
		UseCoding:     {{Name: "Qwen/Qwen2.5-Coder-7B-Instruct"}, {Name: "x/y", Purpose: "Code generation and completion"}},
		UseEmbedding:  {{Name: "BAAI/bge-large-en-v1.5"}, {Name: "x/y", Purpose: "Text embeddings for RAG"}},
		UseReasoning:  {{Name: "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B"}, {Name: "x/y", Purpose: "Advanced reasoning, chain-of-thought"}},
		UseMultimodal: {{Name: "x/y", Purpose: "Multimodal, vision and text"}},
		UseChat:       {{Name: "x/y", Purpose: "Instruction following, chat"}},
		UseGeneral:    {{Name: "x/y", Purpose: "General purpose text generation"}},
	}
	for want, models := range cases {
		for _, m := range models {
			if got := m.UseCase(); got != want {
				t.Errorf("%s / %q: use case %s, want %s", m.Name, m.Purpose, got, want)
			}
		}
	}
	for _, s := range []string{"code", "Vision", " embed "} {
		if _, err := ParseUseCase(s); err != nil {
			t.Errorf("ParseUseCase(%q): %v", s, err)
		}
	}
	if _, err := ParseUseCase("poetry"); err == nil {
		t.Error("ParseUseCase accepted an unknown use case")
	}
}

func TestCapabilityInference(t *testing.T) {
	m := Model{Name: "Qwen/Qwen2.5-VL-7B-Instruct", Purpose: "Multimodal, vision and text"}
	m.inferCapabilities()
	if !m.HasCapability(CapVision) || !m.HasCapability(CapToolUse) {
		t.Fatalf("capabilities = %v", m.Capabilities)
	}
	tts := Model{Name: "hexgrad/Kokoro-82M", Purpose: "Text-to-speech"}
	tts.inferCapabilities()
	if !tts.HasCapability(CapTTS) || !tts.requiresSpecializedRuntime() {
		t.Fatalf("TTS capabilities = %v", tts.Capabilities)
	}
}

func TestGenerationBonus(t *testing.T) {
	cases := []struct {
		arch, name string
		want       float64
	}{
		{"qwen2", "Qwen/Qwen2.5-7B", 4.5},    // explicit 2.5 in the name
		{"qwen3_5", "Qwen/Qwen3.8-27B", 8.4}, // name beats the shared arch string
		{"qwen3_next", "Qwen/Qwen3-Next-80B-A3B", 8.4},
		{"llama", "meta-llama/Llama-3.1-8B-Instruct", 6.3}, // plain "llama" falls through to the name
		{"gemma3", "google/gemma-3-12b-it", 6},
		{"deepseek_v3", "deepseek-ai/DeepSeek-V3", 6},
		{"llama4", "meta-llama/Llama-4-Scout", 9},
		{"", "acme/unknown-model", 0},
		{"mistral", "mistralai/Mistral-7B", 0},
	}
	for _, c := range cases {
		if got := generationBonus(c.arch, c.name); !near(got, c.want, 1e-9) {
			t.Errorf("generationBonus(%q, %q) = %v, want %v", c.arch, c.name, got, c.want)
		}
	}
}

func TestQuantTables(t *testing.T) {
	// The Unsloth dynamic quants behave like the K-quant of their width.
	for _, q := range []string{"UD-Q4_K_XL", "UD-Q4_K_S"} {
		if quantBPP(q) != quantBPP("Q4_K_M") || quantSpeedMultiplier(q) != quantSpeedMultiplier("Q4_K_M") {
			t.Errorf("%s does not match Q4_K_M", q)
		}
	}
	if quantBPP("mlx-4bit") != 0.55 || quantBPP("mlx-8bit") != 1.0 || quantBPP("nonsense") != 0.58 {
		t.Error("unexpected bytes per parameter")
	}
	prev := 100.0
	for _, q := range quantHierarchy {
		if p := quantQualityPenalty(q); p > prev {
			t.Errorf("%s penalty %v exceeds the better quant's %v", q, p, prev)
		} else {
			prev = p
		}
	}
	if !strings.HasPrefix(quantHierarchy[0], "Q8") {
		t.Error("ladder must start at the best quality")
	}
}
