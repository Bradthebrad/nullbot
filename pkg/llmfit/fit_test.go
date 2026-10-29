package llmfit

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixedNow pins the recency bonus so scores don't drift with the calendar.
var fixedNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func gpuMachine(name string, vram, ram float64) Hardware {
	return Hardware{
		OS: "linux", Arch: "amd64", CPUName: "Test CPU", CPUCores: 8,
		TotalRAMGB: ram, AvailableRAMGB: ram * 0.8,
		GPUs:    []GPU{{Name: name, VRAMGB: vram, Count: 1, Backend: BackendCUDA}},
		Backend: BackendCUDA,
	}
}

func cpuMachine(ram float64) Hardware {
	return Hardware{OS: "linux", Arch: "amd64", CPUName: "Test CPU", CPUCores: 8,
		TotalRAMGB: ram, AvailableRAMGB: ram * 0.8, Backend: BackendCPU}
}

func appleMachine(chip string, ram float64) Hardware {
	return Hardware{
		OS: "darwin", Arch: "arm64", CPUName: chip, CPUCores: 10,
		TotalRAMGB: ram, AvailableRAMGB: ram * 0.6,
		GPUs:    []GPU{{Name: chip, VRAMGB: ram, Count: 1, Backend: BackendMetal, UnifiedMemory: true}},
		Backend: BackendMetal, UnifiedMemory: true,
	}
}

func opts() Options { return Options{Now: fixedNow} }

func ladderIndex(q string) int { return slices.Index(quantHierarchy, q) }

func TestSevenBQ4FitsEightGBGPU(t *testing.T) {
	m := testModel("acme/Model-7B-Instruct", 7)
	hw := gpuMachine("NVIDIA GeForce RTX 4060", 8, 16)
	if mem := m.EstimateMemoryGB("Q4_K_M", 4096); mem > quantBudget(8) {
		t.Fatalf("7B Q4_K_M needs %.2f GB, more than an 8 GB card allows", mem)
	}
	f := Analyze(m, hw, opts())
	if f.RunMode != RunGPU || f.Level.rank() < FitGood.rank() || f.Runtime != RuntimeLlamaCpp {
		t.Fatalf("7B on 8 GB: %s / %s / %s, notes %v", f.RunMode, f.Level, f.Runtime, f.Notes)
	}
	// With room left it takes a better quantization than the Q4 default.
	if i := ladderIndex(f.Quant); i < 0 || i > ladderIndex("Q4_K_M") {
		t.Fatalf("quant %s, want Q4_K_M or better", f.Quant)
	}
	if f.MemoryAvailableGB != 8 || f.MemoryRequiredGB > 8 || f.TokensPerSecond < 20 || f.EstimateMethod != "gpu_bandwidth_roofline" {
		t.Fatalf("got %+v", f)
	}
}

func TestSeventyBNeedsMoreThanSixteenGBRAM(t *testing.T) {
	m := testModel("acme/Model-70B-Instruct", 70)
	f := Analyze(m, cpuMachine(16), opts())
	if f.Level != FitTooTight || f.RunMode != RunCPUOnly {
		t.Fatalf("70B on 16 GB RAM: %s / %s", f.Level, f.RunMode)
	}
	// A GPU too small for it still lets it run by spilling into a large RAM.
	f = Analyze(m, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), opts())
	if f.RunMode != RunCPUOffload || f.Level == FitTooTight || f.Level == FitPerfect {
		t.Fatalf("70B on 24 GB + 64 GB: %s / %s, notes %v", f.RunMode, f.Level, f.Notes)
	}
	if f.EstimateMethod != "gpu_ddr_offload_roofline" || f.TokensPerSecond >= 10 {
		t.Fatalf("offloaded 70B at %.1f tok/s via %s", f.TokensPerSecond, f.EstimateMethod)
	}
}

func TestAppleUnifiedMemoryCountsAsVRAM(t *testing.T) {
	m := testModel("acme/Model-14B-Instruct", 14)
	mac := Analyze(m, appleMachine("Apple M2 Pro", 32), opts())
	if mac.RunMode != RunGPU || mac.MemoryAvailableGB != 32 || mac.Runtime != RuntimeMLX || mac.Level != FitPerfect {
		t.Fatalf("Mac: %s / %.0f GB / %s / %s", mac.RunMode, mac.MemoryAvailableGB, mac.Runtime, mac.Level)
	}
	if !strings.HasPrefix(mac.Quant, "mlx-") || !slices.ContainsFunc(mac.Notes, func(n string) bool { return strings.Contains(n, "Unified memory") }) {
		t.Fatalf("Mac quant %s, notes %v", mac.Quant, mac.Notes)
	}
	// The same RAM without a GPU is a CPU run.
	pc := Analyze(m, cpuMachine(32), opts())
	if pc.RunMode != RunCPUOnly || pc.TokensPerSecond >= mac.TokensPerSecond {
		t.Fatalf("PC: %s at %.1f tok/s vs Mac %.1f", pc.RunMode, pc.TokensPerSecond, mac.TokensPerSecond)
	}
	// Ollama on a Mac plans for llama.cpp's GGUF ladder instead.
	ollama := Analyze(m, appleMachine("Apple M2 Pro", 32), Options{Runtime: RuntimeOllama, Now: fixedNow})
	if ollama.Runtime != RuntimeOllama || ladderIndex(ollama.Quant) < 0 || ollama.RunMode != RunGPU {
		t.Fatalf("Ollama on Mac: %s %s %s", ollama.Runtime, ollama.Quant, ollama.RunMode)
	}
}

// llmfit weights speed heavily (30% for general use), so the very top of a
// big machine's list is still fast mid-size models at a higher quant. What
// more memory buys is room: bigger models become runnable and fill the list.
func TestBiggerMachineRanksBiggerModels(t *testing.T) {
	models := Catalog()
	sizes := func(fits []Fit) (median, largest float64) {
		var p []float64
		for _, f := range fits {
			p = append(p, f.Model.ParamsB())
		}
		sort.Float64s(p)
		return p[len(p)/2], p[len(p)-1]
	}
	find := func(fits []Fit, name string) (Fit, bool) {
		i := slices.IndexFunc(fits, func(f Fit) bool { return f.Model.Name == name })
		if i < 0 {
			return Fit{}, false
		}
		return fits[i], true
	}
	o := Options{Now: fixedNow}
	laptop := Rank(gpuMachine("NVIDIA GeForce RTX 4060", 8, 16), models, o)
	workstation := Rank(Hardware{
		OS: "linux", Arch: "amd64", CPUName: "Test CPU", CPUCores: 32, TotalRAMGB: 256, AvailableRAMGB: 200,
		GPUs:    []GPU{{Name: "NVIDIA GeForce RTX 4090", VRAMGB: 24, Count: 4, Backend: BackendCUDA}},
		Backend: BackendCUDA,
	}, models, o)
	if len(workstation) <= len(laptop) {
		t.Fatalf("%d models run on 96 GB of VRAM, %d on 8 GB", len(workstation), len(laptop))
	}
	smallMedian, smallLargest := sizes(laptop)
	bigMedian, bigLargest := sizes(workstation)
	if bigMedian <= smallMedian || bigLargest < 4*smallLargest {
		t.Fatalf("sizes ranked: median %.1fB / largest %.1fB on 96 GB vs %.1fB / %.1fB on 8 GB",
			bigMedian, bigLargest, smallMedian, smallLargest)
	}
	if _, ok := find(laptop, "Qwen/Qwen3-32B"); ok {
		t.Error("a 32B model can't run on an 8 GB laptop")
	}
	if f, ok := find(workstation, "Qwen/Qwen3-32B"); !ok || f.Level != FitPerfect || f.RunMode != RunGPU {
		t.Errorf("Qwen3-32B on the workstation: %+v", f)
	}
	// Mac unified memory goes further still: the top of a 128 GB Mac's
	// list includes 100B-class MoE models.
	mac := Rank(appleMachine("Apple M4 Max", 128), models, Options{Limit: 20, Now: fixedNow})
	if _, largest := sizes(mac); largest < 100 {
		t.Errorf("largest of a 128 GB Mac's top 20 is %.1fB", largest)
	}

	for _, f := range laptop[:20] {
		if f.Level == FitTooTight || f.MemoryRequiredGB > f.MemoryAvailableGB {
			t.Errorf("%s doesn't fit but was ranked: %+v", f.Model.Name, f)
		}
	}
	for i := 1; i < len(laptop); i++ {
		if laptop[i].Score > laptop[i-1].Score {
			t.Fatalf("not sorted by score at %d", i)
		}
	}
}

func TestScoreFitBands(t *testing.T) {
	cases := []struct {
		req, avail float64
		mode       RunMode
		want       FitLevel
	}{
		{10, 8, RunGPU, FitTooTight},
		{6, 10, RunGPU, FitPerfect}, // exactly 60%
		{8, 10, RunGPU, FitGood},
		{8.5, 10, RunGPU, FitGood}, // exactly 85%
		{9.5, 10, RunGPU, FitMarginal},
		{9.81, 10, RunGPU, FitTooTight}, // a pool full to the brim does not load
		{4, 16, RunCPUOnly, FitGood},    // Perfect needs a GPU
		{9, 10, RunCPUOnly, FitMarginal},
		{4, 16, RunCPUOffload, FitGood},
		{4, 16, RunMoEOffload, FitGood},
		{1, 0, RunGPU, FitTooTight}, // unknown pool
	}
	for _, c := range cases {
		if got := scoreFit(c.req, c.avail, c.mode); got != c.want {
			t.Errorf("scoreFit(%v, %v, %s) = %s, want %s", c.req, c.avail, c.mode, got, c.want)
		}
	}
}

func TestScoreComponents(t *testing.T) {
	if fitScore(5, 10) != 100 || fitScore(11, 10) != 0 || fitScore(1, 0) != 0 {
		t.Error("fitScore endpoints")
	}
	// No cliff: the score eases down as the pool fills.
	prev := 100.0
	for r := 0.70; r <= 1.0; r += 0.01 {
		s := fitScore(r*10, 10)
		if s > prev || prev-s > 5 {
			t.Fatalf("fitScore jumps at %.2f: %v -> %v", r, prev, s)
		}
		prev = s
	}
	if speedScore(20, UseGeneral) != 50 || speedScore(100, UseChat) != 100 || speedScore(25, UseReasoning) != 100 {
		t.Error("speedScore")
	}
	m := Model{ContextLength: 4096}
	if contextScore(m, UseGeneral) != 100 || contextScore(m, UseCoding) != 70 || contextScore(Model{ContextLength: 1024}, UseCoding) != 30 {
		t.Error("contextScore")
	}
	s := Scores{Quality: 80, Speed: 60, Fit: 100, Context: 100}
	if got := weightedScore(s, UseGeneral); got != 79 { // 36 + 18 + 15 + 10
		t.Errorf("weightedScore = %v", got)
	}
}

func TestQualityScore(t *testing.T) {
	old := Model{Name: "meta-llama/Llama-2-13b-chat-hf", ParametersRaw: 13e9}
	newer := Model{Name: "Qwen/Qwen3-14B", ParametersRaw: 14.8e9, Architecture: "qwen3", ReleaseDate: "2026-08-01"}
	if qualityScore(newer, "Q4_K_M", UseGeneral, fixedNow) <= qualityScore(old, "Q4_K_M", UseGeneral, fixedNow) {
		t.Error("a newer generation of the same size should score higher")
	}
	if qualityScore(newer, "Q8_0", UseGeneral, fixedNow) <= qualityScore(newer, "Q2_K", UseGeneral, fixedNow) {
		t.Error("heavier quantization should cost quality")
	}
	// MoE models are rated by their active parameters.
	moe := Model{Name: "acme/moe", ParametersRaw: 80e9, ActiveParameters: 3e9, IsMoE: true}
	dense := Model{Name: "acme/dense", ParametersRaw: 80e9}
	if qualityScore(moe, "Q4_K_M", UseGeneral, fixedNow) >= qualityScore(dense, "Q4_K_M", UseGeneral, fixedNow) {
		t.Error("an 80B-A3B MoE should not rate like an 80B dense model")
	}
	// Curated task strength: a coder family beats its generalist sibling at coding.
	if s, _ := taskBenchScore("qwen/qwen3-coder-30b-a3b-instruct", UseCoding); s != 90 {
		t.Errorf("longest pattern should win, got %v", s)
	}
	if _, ok := taskBenchScore("acme/customnet-7b", UseCoding); ok {
		t.Error("unknown family has no bench score")
	}
	if m, ok := monthsSince("2026-07-15", fixedNow); !ok || m != 3 {
		t.Errorf("monthsSince = %v %v", m, ok)
	}
	if m, _ := monthsSince("2027-01-01", fixedNow); m != 0 {
		t.Error("future dates count as zero months")
	}
}

func TestBandwidthEstimates(t *testing.T) {
	c := newCalc(Hardware{}, Options{RAMBandwidthGBps: 50})
	tps := func(params float64, quant, gpu string, vram float64, mode RunMode) float64 {
		hw := gpuMachine(gpu, vram, 64)
		c := c
		c.gpuBW, c.haveGPUBW = GPUBandwidthGBps(gpu)
		return estimateTPS(testModel("acme/x", params), quant, hw, mode, RuntimeLlamaCpp, 4096, c)
	}
	// Measured: Qwen3.5-27B Q4 on an RTX 4090 runs at ~40 tok/s.
	if v := tps(27, "Q4_K_M", "NVIDIA GeForce RTX 4090", 24, RunGPU); v < 25 || v > 55 {
		t.Errorf("4090 27B Q4 = %.1f tok/s", v)
	}
	// ggerganov's T4 benchmark: 7B F16 at ~16 tok/s.
	if v := tps(7, "F16", "Tesla T4", 16, RunGPU); v < 8 || v > 30 {
		t.Errorf("T4 7B F16 = %.1f tok/s", v)
	}
	if fast, slow := tps(27, "Q4_K_M", "NVIDIA GeForce RTX 4090", 24, RunGPU), tps(27, "Q4_K_M", "NVIDIA GeForce RTX 3060", 12, RunGPU); fast <= 2*slow {
		t.Errorf("4090 %.1f should be well over twice a 3060's %.1f", fast, slow)
	}
	if v := tps(7, "Q4_K_M", "Some Unknown GPU", 10, RunGPU); v <= 0 {
		t.Error("unknown GPU should fall back to the backend constant")
	}
	// CPU-only ignores whatever GPU is present.
	if a, b := tps(7, "Q4_K_M", "NVIDIA GeForce RTX 4090", 24, RunCPUOnly), tps(7, "Q4_K_M", "Unknown GPU", 24, RunCPUOnly); a != b {
		t.Errorf("CPU-only: %v vs %v", a, b)
	}
}

func qwen3MoE() Model {
	return Model{
		Name: "Qwen/Qwen3-30B-A3B", ParametersRaw: 30_500_000_000, MinRAMGB: 18, Quantization: "Q4_K_M",
		ContextLength: 40960, Purpose: "General purpose text generation", Architecture: "qwen3_moe",
		IsMoE: true, NumExperts: 128, ActiveExperts: 8, ActiveParameters: 3_300_000_000,
	}
}

func TestMoEOffload(t *testing.T) {
	f := Analyze(qwen3MoE(), gpuMachine("NVIDIA GeForce RTX 4060", 8, 64), Options{RAMBandwidthGBps: 50, Now: fixedNow})
	if f.RunMode != RunMoEOffload || f.Quant != "Q8_0" || f.MoEOffloadedGB < 20 || f.MemoryRequiredGB > 8 {
		t.Fatalf("got %s %s offloaded %.1f GB, notes %v", f.RunMode, f.Quant, f.MoEOffloadedGB, f.Notes)
	}
	if f.TokensPerSecond <= 0 || f.UsableContext <= 0 {
		t.Fatalf("tps %.1f, usable context %d", f.TokensPerSecond, f.UsableContext)
	}
	// With enough VRAM all experts load and it runs on the GPU.
	f = Analyze(qwen3MoE(), gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), opts())
	if f.RunMode != RunGPU || f.MoEOffloadedGB != 0 {
		t.Fatalf("24 GB: %s, offloaded %.1f", f.RunMode, f.MoEOffloadedGB)
	}
}

func TestRuntimeSelection(t *testing.T) {
	m := testModel("Qwen/Qwen3-8B", 8.2)
	if f := Analyze(m, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), opts()); f.Runtime != RuntimeLlamaCpp {
		t.Errorf("CUDA runtime %s", f.Runtime)
	}
	f := Analyze(m, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), Options{Runtime: RuntimeOllama, Now: fixedNow})
	if f.Runtime != RuntimeOllama || f.OllamaTag != "qwen3:8b" || f.GGUFRepo != "bartowski/Qwen3-8B-GGUF" {
		t.Errorf("Ollama: %s %q %q", f.Runtime, f.OllamaTag, f.GGUFRepo)
	}
	awq := testModel("Qwen/Qwen3-14B-AWQ", 14.8)
	awq.Format, awq.Quantization = "awq", "AWQ-4bit"
	if f := Analyze(awq, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), opts()); f.Runtime != RuntimeVLLM || f.Quant != "AWQ-4bit" {
		t.Errorf("AWQ: %s %s", f.Runtime, f.Quant)
	}

	// Ternary models run only under bitnet.cpp, on the CPU, even with a GPU.
	bitnet := testModel("microsoft/bitnet-b1.58-2B-4T", 2.4)
	f = Analyze(bitnet, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), Options{Runtime: RuntimeLlamaCpp, Now: fixedNow})
	if f.Runtime != RuntimeBitNet || f.RunMode != RunCPUOnly || f.Quant != "I2_S" {
		t.Errorf("ternary: %s %s %s", f.Runtime, f.RunMode, f.Quant)
	}
	if f := Analyze(m, cpuMachine(32), Options{Runtime: RuntimeBitNet, Now: fixedNow}); f.Runtime != RuntimeLlamaCpp {
		t.Errorf("bitnet.cpp forced on a normal model: %s", f.Runtime)
	}

	// gpt-oss keeps its MXFP4 weights under llama.cpp.
	oss := testModel("openai/gpt-oss-20b", 20.9)
	oss.Architecture = "gpt_oss"
	if f := Analyze(oss, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64), opts()); f.Quant != "MXFP4" {
		t.Errorf("gpt-oss quant %s", f.Quant)
	}

	for _, s := range []string{"llamacpp", "Ollama", "MLX", "auto", ""} {
		if _, err := ParseRuntime(s); err != nil {
			t.Errorf("ParseRuntime(%q): %v", s, err)
		}
	}
	if _, err := ParseRuntime("tgi"); err == nil {
		t.Error("ParseRuntime accepted an unknown runtime")
	}
}

func TestBackendCompatibility(t *testing.T) {
	awq := testModel("Qwen/Qwen3-14B-AWQ", 14.8)
	awq.Format, awq.Quantization = "awq", "AWQ-4bit"
	if backendCompatible(awq, cpuMachine(64)) || backendCompatible(awq, gpuMachine("Tesla V100-PCIE-32GB", 32, 64)) {
		t.Error("AWQ needs CUDA/ROCm on Turing or newer")
	}
	if !backendCompatible(awq, gpuMachine("NVIDIA GeForce RTX 3090", 24, 64)) {
		t.Error("AWQ runs on Ampere")
	}
	mlx := testModel("mlx-community/Qwen3-8B-4bit", 8.2)
	if backendCompatible(mlx, gpuMachine("NVIDIA GeForce RTX 4090", 24, 64)) || !backendCompatible(mlx, appleMachine("Apple M3", 16)) {
		t.Error("MLX repos run only on Apple Silicon")
	}
	tts := Model{Name: "hexgrad/Kokoro-82M", Capabilities: []string{CapTTS}, ParametersRaw: 82e6}
	if backendCompatible(tts, appleMachine("Apple M3", 16)) {
		t.Error("TTS needs a specialized runtime")
	}
	f := Analyze(tts, appleMachine("Apple M3", 16), opts())
	if f.Runtime != RuntimeUnsupported || f.Level != FitTooTight || f.EstimateMethod != "unsupported" {
		t.Errorf("TTS analysis: %+v", f)
	}
}

func TestContextLength(t *testing.T) {
	m := kvTestModel("meta-llama/Llama-3.1-8B-Instruct")
	m.ContextLength = 131072
	hw := gpuMachine("NVIDIA GeForce RTX 4090", 24, 64)
	def := Analyze(m, hw, opts())
	long := Analyze(m, hw, Options{ContextLength: 65536, Now: fixedNow})
	if def.ContextLength != 8192 || long.ContextLength != 65536 {
		t.Fatalf("contexts %d and %d", def.ContextLength, long.ContextLength)
	}
	if long.MemoryRequiredGB <= def.MemoryRequiredGB {
		t.Fatalf("64k context (%.1f GB) should need more than 8k (%.1f GB)", long.MemoryRequiredGB, def.MemoryRequiredGB)
	}
	// Asking for more than the model supports is capped at its maximum.
	if f := Analyze(m, hw, Options{ContextLength: 1 << 20, Now: fixedNow}); f.ContextLength != 131072 {
		t.Fatalf("context %d", f.ContextLength)
	}
	// A tight pool limits the usable context below the model's window.
	tight := Analyze(m, gpuMachine("NVIDIA GeForce RTX 4060", 8, 16), opts())
	if tight.UsableContext <= 0 || tight.UsableContext >= m.ContextLength {
		t.Fatalf("usable context %d on 8 GB", tight.UsableContext)
	}
	if roomy := Analyze(m, appleMachine("Apple M3 Ultra", 512), opts()); roomy.UsableContext != m.ContextLength {
		t.Fatalf("usable context %d on 512 GB", roomy.UsableContext)
	}
}

func TestRankFiltersAndOrder(t *testing.T) {
	hw := gpuMachine("NVIDIA GeForce RTX 4090", 24, 64)
	coder := testModel("acme/Tiny-Coder-1B", 1)
	coder.Purpose = "Code generation and completion"
	models := []Model{
		testModel("b/Model-7B", 7), testModel("a/Model-7B", 7), // identical but for the name
		testModel("acme/Huge-400B", 400),
		testModel("yuhuili/EAGLE-LLaMA3-Instruct-70B", 3), // draft head
		testModel("mlx-community/Qwen3-8B-4bit", 8),       // MLX off Apple Silicon
		coder,
	}
	fits := Rank(hw, models, opts())
	var names []string
	for _, f := range fits {
		names = append(names, f.Model.Name)
	}
	if slices.Contains(names, "acme/Huge-400B") || slices.Contains(names, "yuhuili/EAGLE-LLaMA3-Instruct-70B") ||
		slices.Contains(names, "mlx-community/Qwen3-8B-4bit") {
		t.Fatalf("ranked something it should skip: %v", names)
	}
	if i, j := slices.Index(names, "a/Model-7B"), slices.Index(names, "b/Model-7B"); i < 0 || j != i+1 {
		t.Fatalf("ties should order by name: %v", names)
	}

	all := Rank(hw, models, Options{MinFit: FitTooTight, Now: fixedNow})
	if all[len(all)-1].Model.Name != "acme/Huge-400B" || all[len(all)-1].Level != FitTooTight {
		t.Fatalf("models that don't fit go last: %v", all[len(all)-1].Model.Name)
	}
	if coding := Rank(hw, models, Options{UseCase: UseCoding, Now: fixedNow}); len(coding) != 1 || coding[0].Model.Name != "acme/Tiny-Coder-1B" {
		t.Fatalf("use case filter: %+v", coding)
	}
	if one := Rank(hw, models, Options{Limit: 1, Now: fixedNow}); len(one) != 1 {
		t.Fatalf("limit: %d", len(one))
	}
	// Ollama can't load an AWQ repo.
	awq := testModel("Qwen/Qwen3-14B-AWQ", 14.8)
	awq.Format, awq.Quantization = "awq", "AWQ-4bit"
	if got := Rank(hw, []Model{awq}, Options{Runtime: RuntimeOllama, Now: fixedNow}); len(got) != 0 {
		t.Fatalf("AWQ ranked for Ollama: %+v", got)
	}
	if got := Rank(hw, []Model{awq}, opts()); len(got) != 1 || got[0].Runtime != RuntimeVLLM {
		t.Fatalf("AWQ under auto: %+v", got)
	}
}

func TestFitMarshalsToJSON(t *testing.T) {
	// A pool of unknown size must not leave an infinity JSON can't encode.
	f := Analyze(testModel("acme/Model-7B", 7), Hardware{Backend: BackendCPU, CPUCores: 4}, opts())
	if f.Level != FitTooTight || f.UtilizationPct != 0 {
		t.Fatalf("got %+v", f)
	}
	if _, err := json.Marshal(f); err != nil {
		t.Fatal(err)
	}
}
