package llmfit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// FitLevel grades how comfortably a model fits the memory it runs from.
type FitLevel string

const (
	FitPerfect  FitLevel = "perfect"   // at most 60% of the pool, on the GPU
	FitGood     FitLevel = "good"      // at most 85%, or comfortable off the GPU
	FitMarginal FitLevel = "marginal"  // at most 98%: loads, but tight
	FitTooTight FitLevel = "too_tight" // does not fit
)

func (l FitLevel) rank() int {
	switch l {
	case FitPerfect:
		return 3
	case FitGood:
		return 2
	case FitMarginal:
		return 1
	}
	return 0
}

// RunMode is how inference would run.
type RunMode string

const (
	RunGPU        RunMode = "gpu"         // all weights in VRAM or unified memory
	RunMoEOffload RunMode = "moe_offload" // active experts in VRAM, the rest in RAM
	RunCPUOffload RunMode = "cpu_offload" // partly in VRAM, spilling to RAM
	RunCPUOnly    RunMode = "cpu_only"    // all in system RAM
)

// Runtime is the inference software. As an option it picks what to plan
// for; on a Fit it says what the estimate assumes.
type Runtime string

const (
	RuntimeAuto     Runtime = ""
	RuntimeLlamaCpp Runtime = "llama.cpp"
	// RuntimeOllama is estimated as llama.cpp, which Ollama runs.
	RuntimeOllama Runtime = "ollama"
	RuntimeMLX    Runtime = "mlx"
	// RuntimeVLLM serves pre-quantized AWQ/GPTQ/AutoRound repos.
	RuntimeVLLM Runtime = "vllm"
	// RuntimeBitNet is the only runtime for native ternary models.
	RuntimeBitNet Runtime = "bitnet.cpp"
	// RuntimeUnsupported marks models no modelled runtime can serve.
	RuntimeUnsupported Runtime = "unsupported"
)

// ParseRuntime accepts runtime names and llmfit's aliases; "" and "auto"
// mean RuntimeAuto.
func ParseRuntime(s string) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto", "any":
		return RuntimeAuto, nil
	case "llama.cpp", "llamacpp", "llama_cpp":
		return RuntimeLlamaCpp, nil
	case "ollama":
		return RuntimeOllama, nil
	case "mlx":
		return RuntimeMLX, nil
	case "vllm":
		return RuntimeVLLM, nil
	case "bitnet.cpp", "bitnetcpp", "bitnet":
		return RuntimeBitNet, nil
	}
	return "", fmt.Errorf("unknown runtime %q (want ollama, llama.cpp, mlx, vllm or bitnet.cpp)", s)
}

// engine is the runtime whose memory and speed model applies.
func (r Runtime) engine() Runtime {
	if r == RuntimeOllama {
		return RuntimeLlamaCpp
	}
	return r
}

// Scores are the four 0-100 components of a Fit's score.
type Scores struct {
	Quality float64 `json:"quality"` // size, family, generation, recency, quantization, task strength
	Speed   float64 `json:"speed"`   // estimated tok/s against the use case's target
	Fit     float64 `json:"fit"`     // memory headroom
	Context float64 `json:"context"` // context window against the use case's target
}

// Fit is the verdict for one model on one machine.
type Fit struct {
	Model   Model    `json:"model"`
	Level   FitLevel `json:"fit"`
	RunMode RunMode  `json:"run_mode"`
	Runtime Runtime  `json:"runtime"`
	// Quant is the best quantization that fits, such as "Q5_K_M".
	Quant string `json:"quant"`
	// MemoryRequiredGB is what the run mode needs from MemoryAvailableGB:
	// VRAM for GPU and MoE offload, system RAM for CPU offload and CPU.
	MemoryRequiredGB  float64 `json:"memory_required_gb"`
	MemoryAvailableGB float64 `json:"memory_available_gb"`
	// UtilizationPct is required/available; 0 when the pool is unknown.
	UtilizationPct float64 `json:"utilization_pct"`
	// MoEOffloadedGB is RAM holding inactive experts in MoE offload mode.
	MoEOffloadedGB float64 `json:"moe_offloaded_gb,omitempty"`
	// ContextLength is the context the memory estimate assumes;
	// UsableContext is the most that fits in the pool, up to the model's
	// own maximum.
	ContextLength int `json:"context_length"`
	UsableContext int `json:"usable_context"`
	// TokensPerSecond is the estimated single-request generation speed.
	TokensPerSecond float64 `json:"tokens_per_second"`
	// EstimateMethod says what the speed rests on: gpu_bandwidth_roofline,
	// gpu_ddr_offload_roofline, backend_constant, cpu_constant or
	// unsupported.
	EstimateMethod string  `json:"estimate_method"`
	Score          float64 `json:"score"` // 0-100, weighted by UseCase
	Scores         Scores  `json:"scores"`
	UseCase        UseCase `json:"use_case"`
	// Names to fetch the model by, where known: see OllamaTag, GGUFRepo
	// and MLXRepo.
	OllamaTag string   `json:"ollama_tag,omitempty"`
	GGUFRepo  string   `json:"gguf_repo,omitempty"`
	MLXRepo   string   `json:"mlx_repo,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

// Options adjust Analyze and Rank. The zero value matches llmfit's
// defaults.
type Options struct {
	// UseCase keeps only models of this category in Rank. Matching is
	// exact, as in llmfit: "chat" does not include "general" models.
	UseCase UseCase
	// ContextLength is the context to size the KV cache for. Zero means
	// the model's maximum capped at 8,192 tokens, which is what llama.cpp
	// and Ollama use unless told otherwise.
	ContextLength int
	// Runtime plans for one runtime; RuntimeAuto picks MLX on Apple
	// Silicon, vLLM for pre-quantized repos and llama.cpp otherwise. In
	// Rank, a runtime also drops models it can't load.
	Runtime Runtime
	// MinFit is the worst fit Rank keeps; "" means FitMarginal, which
	// drops models that don't fit.
	MinFit FitLevel
	// Limit caps Rank's result; 0 keeps everything.
	Limit int
	// Efficiency is the share of peak memory bandwidth decoding reaches;
	// 0 means 0.55.
	Efficiency float64
	// RAMBandwidthGBps is system RAM bandwidth for MoE and CPU offload; 0
	// means Hardware.RAMBandwidthGBps if measured, else 50 GB/s.
	RAMBandwidthGBps float64
	// GPUBandwidthGBps overrides the bandwidth looked up from the GPU name.
	GPUBandwidthGBps float64
	// Now dates the recency bonus; zero means time.Now().
	Now time.Time
}

// calc holds the resolved tunables.
type calc struct {
	efficiency float64
	ramBW      float64
	gpuBW      float64
	haveGPUBW  bool
	now        time.Time
}

func newCalc(hw Hardware, opts Options) calc {
	c := calc{efficiency: opts.Efficiency, ramBW: opts.RAMBandwidthGBps, now: opts.Now}
	if c.efficiency <= 0 {
		c.efficiency = 0.55
	}
	if c.ramBW <= 0 {
		c.ramBW = hw.RAMBandwidthGBps
	}
	if c.ramBW <= 0 {
		// Conservative DDR4-3200 dual channel.
		c.ramBW = 50
	}
	if opts.GPUBandwidthGBps > 0 {
		c.gpuBW, c.haveGPUBW = opts.GPUBandwidthGBps, true
	} else if hw.HasGPU() {
		c.gpuBW, c.haveGPUBW = GPUBandwidthGBps(hw.GPUName())
	}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	return c
}

// Speed multipliers per run mode, applied after the base estimate.
func runModeFactor(mode RunMode) float64 {
	switch mode {
	case RunMoEOffload:
		return 0.8
	case RunCPUOffload:
		return 0.5
	case RunCPUOnly:
		return 0.3
	}
	return 1.0
}

// defaultEstimationContext is the context runtimes use unless told
// otherwise; sizing the KV cache for a model's 262k maximum would wildly
// overstate typical use.
const defaultEstimationContext = 8192

// Memory-ratio ceilings for the verdicts. Marginal stops at 98% because a
// pool filled to the last percent has no room for allocator slack.
const (
	fitPerfectMaxRatio  = 0.60
	fitGoodMaxRatio     = 0.85
	fitMarginalMaxRatio = 0.98
)

// quantBudget is the most a quantization may use in a pool and still not
// be rated too tight.
func quantBudget(pool float64) float64 { return pool * fitMarginalMaxRatio }

// scoreFit grades memory pressure. Perfect means room to spare on the GPU,
// so offload and CPU paths stop at Good however much headroom they have.
func scoreFit(required, available float64, mode RunMode) FitLevel {
	ratio := math.Inf(1)
	if available > 0 {
		ratio = required / available
	}
	var level FitLevel
	switch {
	case math.IsInf(ratio, 0) || math.IsNaN(ratio) || ratio > fitMarginalMaxRatio:
		return FitTooTight
	case ratio <= fitPerfectMaxRatio:
		level = FitPerfect
	case ratio <= fitGoodMaxRatio:
		level = FitGood
	default:
		level = FitMarginal
	}
	if level == FitPerfect && mode != RunGPU {
		level = FitGood
	}
	return level
}

// quantLadder is the quantization ladder a model has on an engine.
func quantLadder(m Model, engine Runtime) []string {
	switch {
	case m.format() == "onnx":
		return onnxQuantHierarchy
	case engine == RuntimeMLX:
		return mlxQuantHierarchy
	case engine == RuntimeBitNet:
		return ternaryQuantHierarchy
	case engine == RuntimeLlamaCpp && m.isMXFP4Native():
		return mxfp4QuantHierarchy
	}
	return quantHierarchy
}

// bestQuantFor finds the best quantization within budget on an engine.
// vLLM can't re-quantize, so a pre-quantized repo has its one footprint.
func bestQuantFor(m Model, engine Runtime, budget float64, ctx int) (string, float64, bool) {
	if engine == RuntimeVLLM {
		mem := m.EstimateMemoryGB(m.Quantization, ctx)
		return m.Quantization, mem, mem <= budget
	}
	if q, mem, ok := m.bestQuantForBudget(budget, ctx, quantLadder(m, engine)); ok {
		return q, mem, true
	}
	if engine == RuntimeMLX {
		// MLX also loads GGUF-style quants.
		return m.bestQuantForBudget(budget, ctx, quantHierarchy)
	}
	return "", 0, false
}

// chooseEngine picks the engine to plan for. Native ternary models run only
// under bitnet.cpp and bitnet.cpp runs only them, so a request that breaks
// that is corrected with a note.
func chooseEngine(m Model, hw Hardware, requested Runtime, notes *[]string) Runtime {
	forced := requested.engine()
	if m.isTernaryNative() {
		if forced != RuntimeAuto && forced != RuntimeBitNet {
			*notes = append(*notes, fmt.Sprintf("Ignoring runtime %s: native ternary models run only under bitnet.cpp", requested))
		}
		return RuntimeBitNet
	}
	if forced == RuntimeBitNet {
		*notes = append(*notes, "Ignoring runtime bitnet.cpp: this model is not native ternary")
		forced = RuntimeAuto
	}
	switch {
	case forced != RuntimeAuto:
		return forced
	case m.isPrequantized():
		return RuntimeVLLM
	case hw.Backend == BackendMetal && hw.UnifiedMemory:
		return RuntimeMLX
	}
	return RuntimeLlamaCpp
}

// Analyze works out how a model would run on hw: run mode, best
// quantization, memory, fit level, estimated speed and score.
func Analyze(m Model, hw Hardware, opts Options) Fit {
	c := newCalc(hw, opts)
	ctx := min(m.ContextLength, defaultEstimationContext)
	if opts.ContextLength > 0 {
		ctx = min(opts.ContextLength, m.ContextLength)
	}
	f := Fit{
		Model:         m,
		UseCase:       m.UseCase(),
		ContextLength: ctx,
		OllamaTag:     OllamaTag(m.Name),
		GGUFRepo:      GGUFRepo(m),
		MLXRepo:       MLXRepo(m.Name),
	}
	note := func(format string, args ...any) { f.Notes = append(f.Notes, fmt.Sprintf(format, args...)) }
	defaultMem := m.EstimateMemoryGB(m.Quantization, ctx)
	if ctx < m.ContextLength {
		note("Context capped at %d tokens for estimation (model supports up to %d)", ctx, m.ContextLength)
	}

	if m.requiresSpecializedRuntime() {
		note("Requires a specialized text-to-speech runtime; llama.cpp, MLX and vLLM can't serve it")
		f.Level, f.RunMode, f.Runtime = FitTooTight, RunCPUOnly, RuntimeUnsupported
		f.Quant, f.MemoryRequiredGB, f.EstimateMethod = m.Quantization, defaultMem, "unsupported"
		return f
	}

	engine := chooseEngine(m, hw, opts.Runtime, &f.Notes)
	choose := func(pool float64) (string, float64, bool) {
		return bestQuantFor(m, engine, quantBudget(pool), ctx)
	}
	minVRAM := m.MinVRAMGB
	if minVRAM == 0 {
		minVRAM = m.MinRAMGB
	}

	cpuPath := func() (RunMode, float64, float64) {
		note("CPU-only: model loaded into system RAM")
		if m.IsMoE {
			note("MoE architecture, but expert offloading requires a GPU")
			return RunCPUOnly, m.MinRAMGB, hw.AvailableRAMGB
		}
		if _, mem, ok := choose(hw.AvailableRAMGB); ok {
			return RunCPUOnly, mem, hw.AvailableRAMGB
		}
		return RunCPUOnly, defaultMem, hw.AvailableRAMGB
	}

	var (
		mode              RunMode
		required, pool    float64
		moeQuant          string
		moeOffloaded      float64
		moeOffloadChoseIt bool
	)
	vram := hw.TotalVRAMGB()
	switch {
	case engine == RuntimeBitNet:
		// bitnet.cpp runs on the CPU, whatever GPU is present.
		mode, required, pool = cpuPath()
	case hw.HasGPU() && hw.UnifiedMemory:
		// One shared pool, so there is nothing to spill to.
		if unified := hw.GPUVRAMGB(); unified > 0 {
			note("Unified memory: GPU and CPU share the same pool")
			if m.IsMoE {
				note("MoE: %d/%d experts active (all share unified memory pool)", m.ActiveExperts, m.NumExperts)
			}
			mode, required, pool = RunGPU, defaultMem, unified
			if _, mem, ok := choose(unified); ok {
				required = mem
			}
		} else {
			mode, required, pool = cpuPath()
		}
	case hw.HasGPU() && vram > 0:
		// Graded against VRAM summed over cards: llama.cpp and vLLM split
		// a model across them.
		switch {
		case m.IsMoE:
			// Runtimes load every expert when the whole file fits, which
			// avoids streaming experts over the slower RAM bus.
			if q, mem, ok := choose(vram); ok {
				note("GPU: all MoE experts loaded into VRAM")
				note("MoE: all %d experts in VRAM at %s (%.1f GB)", m.NumExperts, q, mem)
				mode, required, pool = RunGPU, mem, vram
			} else {
				o := moeOffloadPath(m, hw, vram, minVRAM, engine)
				f.Notes = append(f.Notes, o.notes...)
				mode, required, pool = o.mode, o.required, o.pool
				moeQuant, moeOffloaded, moeOffloadChoseIt = o.quant, o.offloaded, o.quant != ""
			}
		default:
			if _, mem, ok := choose(vram); ok {
				note("GPU: model loaded into VRAM")
				mode, required, pool = RunGPU, mem, vram
			} else if _, mem, ok := choose(hw.AvailableRAMGB); ok {
				note("GPU: insufficient VRAM, spilling to system RAM")
				note("Performance will be significantly reduced")
				mode, required, pool = RunCPUOffload, mem, hw.AvailableRAMGB
			} else {
				note("Insufficient VRAM and system RAM")
				note("Need %.1f GB VRAM or %.1f GB system RAM", minVRAM, m.MinRAMGB)
				mode, required, pool = RunGPU, defaultMem, vram
			}
		}
	case hw.HasGPU():
		note("GPU detected but VRAM unknown")
		mode, required, pool = cpuPath()
	default:
		mode, required, pool = cpuPath()
	}

	f.RunMode, f.MemoryRequiredGB, f.MemoryAvailableGB = mode, required, pool
	f.Level = scoreFit(required, pool, mode)
	if pool > 0 {
		f.UtilizationPct = required / pool * 100
	}

	if m.isTernaryNative() {
		note("Native ternary (1.58-bit) model: i2_s weights (~2-bit) run best on CPU via bitnet.cpp")
	}
	if engine == RuntimeLlamaCpp && m.isMXFP4Native() {
		note("MXFP4-native weights: sized at the released MXFP4 precision, which GGUF builds of this model keep")
	}
	if mode == RunCPUOnly && !hw.HasGPU() {
		note("No GPU -- inference will be slow")
	}
	if (mode == RunCPUOffload || mode == RunCPUOnly) && hw.CPUCores < 4 {
		note("Low CPU core count may bottleneck inference")
	}
	if mode == RunMoEOffload {
		if moeOffloadChoseIt {
			f.MoEOffloadedGB = moeOffloaded
		} else if off, ok := m.moeOffloadedRAMGB(); ok {
			f.MoEOffloadedGB = off
		}
	}

	// The best quantization for the chosen pool. Pre-quantized repos have
	// one; MoE offload already settled its own.
	quant := m.Quantization
	switch {
	case m.isPrequantized():
	case moeOffloadChoseIt:
		quant = moeQuant
	default:
		budget := quantBudget(pool)
		if q, _, ok := m.bestQuantForBudget(budget, ctx, quantLadder(m, engine)); ok {
			quant = q
		} else if engine == RuntimeMLX {
			if q, _, ok := m.bestQuantForBudget(budget, ctx, quantHierarchy); ok {
				quant = q
			}
		}
	}
	if quant != m.Quantization {
		note("Best quantization for hardware: %s (model default: %s)", quant, m.Quantization)
	}
	f.Quant = quant

	f.TokensPerSecond = estimateTPS(m, quant, hw, mode, engine, ctx, c)
	switch {
	case mode == RunCPUOnly:
		f.EstimateMethod = "cpu_constant"
	case mode == RunCPUOffload && !m.IsMoE && c.haveGPUBW:
		f.EstimateMethod = "gpu_ddr_offload_roofline"
	case c.haveGPUBW:
		f.EstimateMethod = "gpu_bandwidth_roofline"
	default:
		f.EstimateMethod = "backend_constant"
	}
	if engine == RuntimeMLX {
		if base := estimateTPS(m, quant, hw, mode, RuntimeLlamaCpp, ctx, c); base > 0.1 {
			if speedup := math.Round((f.TokensPerSecond/base - 1) * 100); speedup > 0 {
				note("MLX runtime: ~%.0f%% faster than llama.cpp (%.1f vs %.1f tok/s)", speedup, f.TokensPerSecond, base)
			}
		}
	}

	f.Scores = Scores{
		Quality: qualityScore(m, quant, f.UseCase, c.now),
		Speed:   speedScore(f.TokensPerSecond, f.UseCase),
		Fit:     fitScore(required, pool),
		Context: contextScore(m, f.UseCase),
	}
	f.Score = weightedScore(f.Scores, f.UseCase)

	// A repack that fits can still lack the tensor cores its format was
	// built for; vLLM then runs it through a slower fallback, so it ranks
	// below a natively supported build of the same weights.
	if engine == RuntimeVLLM && kernelSupportFor(m, hw) == kernelFallback {
		f.Score = max(f.Score-8, 0)
		format := effectiveKernelFormat(m.Quantization, m.Name)
		gpuArch, nativeArch := "an older generation", "a newer generation"
		if cc, ok := gpuComputeCapability(hw.GPUName()); ok {
			gpuArch = cc.label()
		}
		if cc, ok := quantNativeMinCC(format); ok {
			nativeArch = cc.label()
		}
		note("%s kernels need %s; %s (%s) runs these weights through vLLM's slower weight-only fallback. A GGUF Q6/Q8 build of the same model will use this GPU natively",
			format, nativeArch, hw.GPUName(), gpuArch)
	}
	if f.TokensPerSecond > 0 {
		note("Baseline estimated speed: %.1f tok/s", f.TokensPerSecond)
	}

	f.UsableContext = usableContext(m, quant, mode, pool)
	f.Runtime = engine
	if opts.Runtime == RuntimeOllama && engine == RuntimeLlamaCpp {
		f.Runtime = RuntimeOllama
	}
	return f
}

// usableContext is how many tokens of KV cache fit once weights and
// overhead are resident, within the budget the verdict accepts, capped at
// the model's maximum.
func usableContext(m Model, quant string, mode RunMode, pool float64) int {
	fixed := m.EstimateMemoryGB(quant, 0)
	if mode == RunMoEOffload {
		// The pool is VRAM and holds only the active experts.
		if active, ok := m.moeActiveVRAMGB(quant); ok {
			fixed = active
		}
	}
	leftover := max(quantBudget(pool)-fixed, 0)
	const refCtx = 4096
	perToken := max(m.kvCacheGB(refCtx)-m.kvCacheGB(0), 0) / refCtx
	if perToken <= 0 {
		return m.ContextLength
	}
	if tokens := leftover / perToken; tokens < float64(m.ContextLength) {
		return int(tokens)
	}
	return m.ContextLength
}

// moeOffload is the outcome of trying to keep only active experts in VRAM.
type moeOffload struct {
	mode           RunMode
	required, pool float64
	quant          string // "" unless offloading worked
	offloaded      float64
	notes          []string
}

// moeOffloadPath tries the quantization ladder for one whose active experts
// fit VRAM and whose inactive experts fit RAM, then falls back to spilling
// the whole model into RAM.
func moeOffloadPath(m Model, hw Hardware, vram, minVRAM float64, engine Runtime) moeOffload {
	ladders := [][]string{quantLadder(m, engine)}
	if engine == RuntimeVLLM {
		ladders = [][]string{{m.Quantization}}
	}
	if engine == RuntimeMLX {
		ladders = append(ladders, quantHierarchy)
	}
	for _, ladder := range ladders {
		for _, q := range ladder {
			active, offloaded, ok := moeMemoryForQuant(m, q)
			if ok && active <= vram && offloaded <= hw.AvailableRAMGB {
				return moeOffload{
					mode: RunMoEOffload, required: active, pool: vram, quant: q, offloaded: offloaded,
					notes: []string{
						fmt.Sprintf("MoE: %d/%d experts active in VRAM (%.1f GB) at %s", m.ActiveExperts, m.NumExperts, active, q),
						fmt.Sprintf("Inactive experts offloaded to system RAM (%.1f GB)", offloaded),
					},
				}
			}
		}
	}
	if m.MinRAMGB <= hw.AvailableRAMGB {
		return moeOffload{mode: RunCPUOffload, required: m.MinRAMGB, pool: hw.AvailableRAMGB, notes: []string{
			"MoE: insufficient VRAM for expert offloading",
			"Spilling entire model to system RAM",
			"Performance will be significantly reduced",
		}}
	}
	activeAtDefault, ok := m.moeActiveVRAMGB(m.Quantization)
	if !ok {
		activeAtDefault = minVRAM
	}
	return moeOffload{mode: RunGPU, required: minVRAM, pool: vram, notes: []string{
		"Insufficient VRAM and system RAM",
		fmt.Sprintf("Need %.1f GB VRAM (full) or %.1f GB (MoE offload) + RAM", minVRAM, activeAtDefault),
	}}
}

// moeMemoryForQuant is the VRAM for active experts and the RAM for the
// inactive ones at a quantization.
func moeMemoryForQuant(m Model, quant string) (active, offloaded float64, ok bool) {
	if !m.IsMoE || m.ActiveParameters == 0 || m.ParametersRaw == 0 {
		return 0, 0, false
	}
	bpp := quantBPP(quant)
	activeParams := float64(m.ActiveParameters)
	active = max(activeParams*bpp/(1<<30)*1.1, 0.5)
	offloaded = max(float64(m.ParametersRaw)-activeParams, 0) * bpp / (1 << 30)
	return active, offloaded, true
}

// backendCompatible: MLX repos need Apple Silicon, pre-quantized repos
// need CUDA or ROCm (and, on NVIDIA, a GPU new enough for their kernels),
// and text-to-speech models need a runtime none of these provide.
func backendCompatible(m Model, hw Hardware) bool {
	switch {
	case m.requiresSpecializedRuntime():
		return false
	case m.isMLXModel():
		return hw.Backend == BackendMetal && hw.UnifiedMemory
	case m.isPrequantized():
		if hw.Backend != BackendCUDA && hw.Backend != BackendROCm {
			return false
		}
		return kernelSupportFor(m, hw) != kernelUnsupported
	}
	return true
}

// kernelSupportFor is only meaningful for vLLM on CUDA.
func kernelSupportFor(m Model, hw Hardware) kernelSupport {
	if !m.isPrequantized() || hw.Backend != BackendCUDA || hw.GPUName() == "" {
		return kernelUnknown
	}
	return quantKernelSupport(effectiveKernelFormat(m.Quantization, m.Name), hw.GPUName())
}

// estimateTPS estimates decode speed in tokens per second. Generation is
// memory-bandwidth bound: each token reads every active weight once, so
// with a known GPU bandwidth the estimate is bandwidth / model bytes x
// efficiency (validated in llmfit against an RTX 4090, a T4 and an M1
// Max). Unknown GPUs fall back to a per-backend constant.
func estimateTPS(m Model, quant string, hw Hardware, mode RunMode, engine Runtime, ctx int, c calc) float64 {
	// MoE models only run their active experts per token.
	params := m.ParamsB()
	if m.IsMoE && m.ActiveParameters > 0 {
		params = float64(m.ActiveParameters) / 1e9
	}
	params = max(params, 0.1)

	if mode != RunCPUOnly && c.haveGPUBW {
		bw := c.gpuBW
		activeGB := params * quantBytesPerParam(quant)
		if m.IsMoE && (mode == RunMoEOffload || mode == RunGPU) {
			if mode == RunMoEOffload {
				// Active experts are read from RAM by the CPU, then
				// computed on the GPU; the RAM read dominates.
				total := activeGB/c.ramBW + activeGB/(bw*c.efficiency)
				return max(1/total*runModeFactor(mode), 0.1)
			}
			pressure := vramPressure(m, quant, hw.GPUVRAMGB())
			if activeFFN, fixed, ok := m.moeBandwidthDecomposition(); ok {
				// Expert weights scale with quantization; attention,
				// router and head cost about the same time at any quant.
				perToken := activeFFN*quantBPP(quant) + fixed*moeTier1FixedBPP(m.Architecture)
				return max(bw/perToken*runModeFactor(mode)*pressure, 0.1)
			}
			eff, overhead := moeTier2Params(m.Architecture, m.NumExperts, c.efficiency)
			raw := bw / (params * quantBPP(quant)) * eff * overhead
			return max(raw*runModeFactor(mode)*pressure, 0.1)
		}
		if mode == RunCPUOffload && !m.IsMoE {
			return denseCPUOffloadTPS(m, hw, c, ctx, activeGB)
		}
		return max(bw/activeGB*c.efficiency*runModeFactor(mode), 0.1)
	}

	k := backendConstant(hw, engine)
	base := k / params * quantSpeedMultiplier(quant)
	if hw.CPUCores >= 8 {
		base *= 1.1
	}
	if mode == RunMoEOffload {
		// The same RAM-bound model as above, with a GPU bandwidth implied
		// by the constant (K = bandwidth x 0.55 / bytes per parameter).
		const fallbackEfficiency = 0.55
		bpp := quantBytesPerParam(quant)
		gpuBW := k * bpp / fallbackEfficiency
		activeGB := params * bpp
		base = max(1/(activeGB/c.ramBW+activeGB/(gpuBW*fallbackEfficiency)), 0.1)
		if hw.CPUCores >= 8 {
			base *= 1.1
		}
		return base
	}
	if mode == RunCPUOnly {
		// The CPU constant applies whatever GPU is present.
		base = cpuConstant(hw) / params * quantSpeedMultiplier(quant)
		if hw.CPUCores >= 8 {
			base *= 1.1
		}
	}
	return max(base*runModeFactor(mode), 0.1)
}

// isARMHost: the CPU constant is higher on ARM, Apple Silicon included.
func isARMHost(hw Hardware) bool {
	return hw.Arch == "arm64" || strings.Contains(strings.ToLower(hw.CPUName), "apple")
}

func cpuConstant(hw Hardware) float64 {
	if isARMHost(hw) {
		return 90
	}
	return 70
}

// backendConstant is llmfit's tok/s x billion-parameter constant per
// backend, used when the GPU's bandwidth is unknown.
func backendConstant(hw Hardware, engine Runtime) float64 {
	switch {
	case engine == RuntimeUnsupported:
		return 0
	case engine == RuntimeBitNet:
		return 75
	case hw.Backend == BackendMetal && engine == RuntimeMLX:
		return 250
	case hw.Backend == BackendMetal:
		return 160
	case hw.Backend == BackendCUDA:
		return 220
	case hw.Backend == BackendROCm:
		return 180
	case hw.Backend == BackendVulkan:
		return 150
	case hw.Backend == BackendSYCL:
		return 100
	}
	return cpuConstant(hw)
}

// vramPressure penalizes MoE models that fill VRAM: inactive experts
// compete for the GPU's cache. No penalty below 60% utilization; the floor
// keeps estimates near 100% from collapsing.
func vramPressure(m Model, quant string, vram float64) float64 {
	if vram <= 0 {
		return 1
	}
	util := m.ParamsB() * quantBPP(quant) / vram
	if util > 1 || util < 0.60 {
		return 1
	}
	ratio := 0.50
	if m.NumExperts > 0 {
		active := max(m.ActiveExperts, 1)
		ratio = 1 - float64(active)/float64(m.NumExperts)
	}
	return max(1-(util-0.60)*ratio, 0.30)
}

// moeTier1FixedBPP is the bandwidth-equivalent bytes per fixed parameter.
// gpt-oss has its own value fitted to ~50 tok/s for the 120B on 256 GB/s
// unified memory.
func moeTier1FixedBPP(architecture string) float64 {
	a := strings.ToLower(architecture)
	if strings.HasPrefix(a, "gpt_oss") || strings.HasPrefix(a, "gptoss") {
		return 1.43
	}
	return moeFixedEffectiveBPP
}

// moeTier2Params calibrates the metadata-poor MoE estimate: efficiency and
// an overhead factor, fitted per architecture where measurements exist and
// by expert count otherwise.
func moeTier2Params(architecture string, experts int, efficiency float64) (float64, float64) {
	a := strings.ToLower(architecture)
	switch {
	case strings.HasPrefix(a, "gpt_oss") || strings.HasPrefix(a, "gptoss"):
		return 0.72, 0.80
	case strings.HasPrefix(a, "deepseek_v3") || strings.HasPrefix(a, "deepseek_v4"):
		return 0.62, 0.70
	}
	switch {
	case experts == 0:
		return efficiency, 0.60
	case experts <= 8:
		return efficiency, 0.90 // Mixtral-class
	case experts <= 16:
		return efficiency, 0.85
	case experts <= 32:
		return efficiency, 0.80
	case experts <= 64:
		return efficiency, 0.70 // OLMoE, Qwen1.5-MoE, DeepSeek-V2-Lite
	}
	return efficiency, 0.40
}

// denseCPUOffloadTPS: the VRAM left after the KV cache and overhead holds
// as many whole layers as fit (llama.cpp offloads by layer); those read at
// GPU speed and the rest stream from system RAM. The KV cache is sized at
// the analysis context; llmfit uses its 8k default here unless a profile
// sets a context cap.
func denseCPUOffloadTPS(m Model, hw Hardware, c calc, ctx int, weightGB float64) float64 {
	vramForWeights := max(hw.gpuFitPoolGB()-m.kvCacheGB(ctx)-runtimeOverheadGB, 0)
	gpuGB, ramGB := min(weightGB, vramForWeights), 0.0
	if layers := float64(m.NumHiddenLayers); layers > 0 {
		slice := weightGB / layers
		onGPU := min(max(math.Floor(vramForWeights/slice), 0), layers)
		gpuGB = weightGB
		if onGPU < layers {
			gpuGB = onGPU * slice
		}
	}
	ramGB = weightGB - gpuGB
	if ramGB <= 0 {
		if weightGB <= 0 {
			return 0.1
		}
		return max(c.gpuBW/weightGB*c.efficiency, 0.1)
	}
	gpuTime := 0.0
	if gpuGB > 0 {
		gpuTime = gpuGB / (c.gpuBW * c.efficiency)
	}
	total := gpuTime + ramGB/c.ramBW
	if math.IsInf(total, 0) || math.IsNaN(total) || total <= 0 {
		return 0.1
	}
	return max(1/total, 0.1)
}

// qualityScore: a base by (active) parameter count, plus family reputation,
// generation and recency bonuses, minus the quantization penalty, plus
// task strength from curated benchmark aggregates or name heuristics.
func qualityScore(m Model, quant string, uc UseCase, now time.Time) float64 {
	params := m.ParamsB()
	// MoE models infer closer to a dense model of their active size.
	qParams := params
	if m.ActiveParameters > 0 {
		qParams = float64(m.ActiveParameters) / 1e9
	}
	var base float64
	switch {
	case qParams < 1:
		base = 30
	case qParams < 3:
		base = 45
	case qParams < 7:
		base = 60
	case qParams < 10:
		base = 75
	case qParams < 20:
		base = 82
	case qParams < 40:
		base = 89
	default:
		base = 95
	}

	name := strings.ToLower(m.Name)
	family := 0.0
	switch {
	case strings.Contains(name, "qwen"):
		family = 2
	case strings.Contains(name, "deepseek"):
		family = 3
	case strings.Contains(name, "llama"):
		family = 2
	case strings.Contains(name, "mistral") || strings.Contains(name, "mixtral"):
		family = 1
	case strings.Contains(name, "gemma"):
		family = 1
	case strings.Contains(name, "phi"):
		family = 0
	case strings.Contains(name, "starcoder"):
		family = 1
	}

	recency := 0.0
	if months, ok := monthsSince(m.ReleaseDate, now); ok {
		switch {
		case months < 3:
			recency = 3
		case months < 9:
			recency = 1.5
		}
	}

	var task float64
	if bench, ok := taskBenchScore(name, uc); ok {
		task = min(max((bench-72)*0.4, -8), 9)
	} else {
		switch uc {
		case UseCoding:
			if strings.Contains(name, "code") || strings.Contains(name, "starcoder") || strings.Contains(name, "wizard") {
				task = 6
			}
		case UseReasoning:
			if params >= 13 {
				task = 5
			}
		case UseMultimodal:
			if strings.Contains(name, "vision") || strings.Contains(strings.ToLower(m.Purpose), "vision") {
				task = 6
			}
		}
	}
	total := base + family + generationBonus(m.Architecture, m.Name) + recency + quantQualityPenalty(quant) + task
	return min(max(total, 0), 100)
}

// monthsSince counts whole months from a YYYY-MM-DD date to now; future
// dates count as zero.
func monthsSince(date string, now time.Time) (int, bool) {
	parts := strings.SplitN(date, "-", 3)
	if len(parts) < 2 {
		return 0, false
	}
	year, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	month, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, false
	}
	now = now.UTC()
	return max((now.Year()-year)*12+int(now.Month())-month, 0), true
}

// speedScore rates tok/s against what the use case needs: 40 for chat-like
// work, 25 for reasoning, 200 for embeddings.
func speedScore(tps float64, uc UseCase) float64 {
	target := 40.0
	switch uc {
	case UseReasoning:
		target = 25
	case UseEmbedding:
		target = 200
	}
	return min(max(tps/target*100, 0), 100)
}

// fitScore holds 100 up to 70% utilization, then eases off along a
// one-sided Gaussian instead of dropping off a cliff.
func fitScore(required, available float64) float64 {
	if available <= 0 || required > available {
		return 0
	}
	z := max((required/available-0.70)/0.20, 0)
	return min(max(100*math.Exp(-0.5*z*z), 0), 100)
}

// contextScore rates the context window against what the use case needs.
func contextScore(m Model, uc UseCase) float64 {
	target := 4096
	switch uc {
	case UseCoding, UseReasoning:
		target = 8192
	case UseEmbedding:
		target = 512
	}
	switch {
	case m.ContextLength >= target:
		return 100
	case m.ContextLength >= target/2:
		return 70
	}
	return 30
}

// useCaseWeights are (quality, speed, fit, context) per use case.
var useCaseWeights = map[UseCase][4]float64{
	UseGeneral:    {0.45, 0.30, 0.15, 0.10},
	UseCoding:     {0.50, 0.20, 0.15, 0.15},
	UseReasoning:  {0.55, 0.15, 0.15, 0.15},
	UseChat:       {0.40, 0.35, 0.15, 0.10},
	UseMultimodal: {0.50, 0.20, 0.15, 0.15},
	UseEmbedding:  {0.30, 0.40, 0.20, 0.10},
}

// weightedScore combines the components, rounded to one decimal.
func weightedScore(s Scores, uc UseCase) float64 {
	w, ok := useCaseWeights[uc]
	if !ok {
		w = useCaseWeights[UseGeneral]
	}
	raw := s.Quality*w[0] + s.Speed*w[1] + s.Fit*w[2] + s.Context*w[3]
	return math.Round(raw*10) / 10
}

// taskBench holds llmfit's curated per-family task strengths (0-100),
// aggregated from public leaderboards. The longest matching name pattern
// wins, so "qwen3-coder" beats "qwen3".
var taskBench = []struct {
	patterns                []string
	coding, reasoning, chat float64
}{
	{[]string{"qwen3.5-coder", "qwen3-coder", "qwen2.5-coder"}, 90, 78, 72},
	{[]string{"qwen3.5"}, 82, 84, 82},
	{[]string{"qwen3"}, 78, 80, 78},
	{[]string{"deepseek-v4"}, 90, 92, 84},
	{[]string{"deepseek-v3"}, 86, 88, 82},
	{[]string{"deepseek-r1", "deepseek-r2"}, 84, 93, 76},
	{[]string{"llama-4", "llama4"}, 74, 80, 84},
	{[]string{"llama-3.3", "llama3.3"}, 70, 76, 82},
	{[]string{"gemma-4", "gemma4"}, 72, 76, 84},
	{[]string{"gemma-3", "gemma3"}, 68, 72, 80},
	{[]string{"phi-4", "phi4"}, 74, 82, 72},
	{[]string{"codestral"}, 84, 68, 62},
	{[]string{"devstral"}, 86, 70, 60},
	{[]string{"mixtral"}, 66, 72, 74},
	{[]string{"mistral"}, 68, 70, 76},
	{[]string{"glm-4", "glm4"}, 80, 82, 78},
	{[]string{"kimi"}, 78, 84, 80},
	{[]string{"granite"}, 70, 72, 70},
	{[]string{"starcoder"}, 78, 55, 50},
	{[]string{"olmo"}, 62, 66, 70},
}

// taskBenchScore looks up a lowercased name's family strength for the
// coding, reasoning and chat use cases.
func taskBenchScore(lowerName string, uc UseCase) (float64, bool) {
	bestLen, score := -1, 0.0
	for _, e := range taskBench {
		var s float64
		switch uc {
		case UseCoding:
			s = e.coding
		case UseReasoning:
			s = e.reasoning
		case UseChat:
			s = e.chat
		default:
			return 0, false
		}
		for _, p := range e.patterns {
			if strings.Contains(lowerName, p) && len(p) > bestLen {
				bestLen, score = len(p), s
			}
		}
	}
	return score, bestLen >= 0
}
