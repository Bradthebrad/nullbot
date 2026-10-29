package llmfit

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Model is one catalog entry, in the schema of llmfit's hf_models.json.
// Numeric metadata the catalog does not know is zero.
type Model struct {
	// Name is the Hugging Face repository, such as "Qwen/Qwen3-8B".
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// ParameterCount is the display size, such as "8.2B"; ParametersRaw is
	// the exact count when known.
	ParameterCount string `json:"parameter_count"`
	ParametersRaw  uint64 `json:"parameters_raw,omitempty"`
	// MinRAMGB, RecommendedRAMGB and MinVRAMGB are the catalog's own
	// rough requirements; the fit analysis estimates memory itself.
	MinRAMGB         float64 `json:"min_ram_gb"`
	RecommendedRAMGB float64 `json:"recommended_ram_gb"`
	MinVRAMGB        float64 `json:"min_vram_gb,omitempty"`
	// Quantization is the catalog default (Q4_K_M for GGUF models), or the
	// fixed format of a pre-quantized repo such as "AWQ-4bit".
	Quantization string `json:"quantization"`
	// Format is the weight format: gguf (the default), mlx, awq, gptq,
	// autoround, safetensors or onnx.
	Format        string `json:"format,omitempty"`
	ContextLength int    `json:"context_length"`
	// Purpose is the catalog's free-text use case, such as "Instruction
	// following, chat". UseCase derives the scoring category from it.
	Purpose      string   `json:"use_case"`
	PipelineTag  string   `json:"pipeline_tag,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	License      string   `json:"license,omitempty"`
	ReleaseDate  string   `json:"release_date,omitempty"`
	Downloads    uint64   `json:"hf_downloads,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// GGUFSources lists known GGUF repackagings on Hugging Face.
	GGUFSources []GGUFSource `json:"gguf_sources,omitempty"`

	IsMoE            bool   `json:"is_moe,omitempty"`
	NumExperts       int    `json:"num_experts,omitempty"`
	ActiveExperts    int    `json:"active_experts,omitempty"`
	ActiveParameters uint64 `json:"active_parameters,omitempty"`

	NumHiddenLayers              int `json:"num_hidden_layers,omitempty"`
	NumAttentionHeads            int `json:"num_attention_heads,omitempty"`
	NumKeyValueHeads             int `json:"num_key_value_heads,omitempty"`
	HeadDim                      int `json:"head_dim,omitempty"`
	HiddenSize                   int `json:"hidden_size,omitempty"`
	VocabSize                    int `json:"vocab_size,omitempty"`
	MoEIntermediateSize          int `json:"moe_intermediate_size,omitempty"`
	SharedExpertIntermediateSize int `json:"shared_expert_intermediate_size,omitempty"`
}

// GGUFSource is a GGUF build of a model on Hugging Face.
type GGUFSource struct {
	Repo     string `json:"repo"`
	Provider string `json:"provider"`
}

// Capability names as they appear in Model.Capabilities.
const (
	CapVision  = "vision"
	CapToolUse = "tool_use"
	CapAudio   = "audio"
	CapTTS     = "tts"
)

// HasCapability reports whether the model lists capability c.
func (m Model) HasCapability(c string) bool { return slices.Contains(m.Capabilities, c) }

// UseCase is the category a model is scored as. Each has its own weighting
// of quality, speed, fit and context.
type UseCase string

const (
	UseGeneral    UseCase = "general"
	UseCoding     UseCase = "coding"
	UseReasoning  UseCase = "reasoning"
	UseChat       UseCase = "chat"
	UseMultimodal UseCase = "multimodal"
	UseEmbedding  UseCase = "embedding"
)

// ParseUseCase accepts the names and aliases llmfit's CLI accepts.
func ParseUseCase(s string) (UseCase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "general":
		return UseGeneral, nil
	case "coding", "code":
		return UseCoding, nil
	case "reasoning", "reason":
		return UseReasoning, nil
	case "chat":
		return UseChat, nil
	case "multimodal", "vision":
		return UseMultimodal, nil
	case "embedding", "embed":
		return UseEmbedding, nil
	}
	return "", fmt.Errorf("unknown use case %q (want general, coding, reasoning, chat, multimodal or embedding)", s)
}

// UseCase infers the scoring category from the model's name and purpose.
func (m Model) UseCase() UseCase {
	name := strings.ToLower(m.Name)
	purpose := strings.ToLower(m.Purpose)
	switch {
	case strings.Contains(purpose, "embedding") || strings.Contains(name, "embed") || strings.Contains(name, "bge"):
		return UseEmbedding
	case strings.Contains(name, "code") || strings.Contains(purpose, "code"):
		return UseCoding
	case strings.Contains(purpose, "vision") || strings.Contains(purpose, "multimodal"):
		return UseMultimodal
	case strings.Contains(purpose, "reason") || strings.Contains(purpose, "chain-of-thought") || strings.Contains(name, "deepseek-r1"):
		return UseReasoning
	case strings.Contains(purpose, "chat") || strings.Contains(purpose, "instruction"):
		return UseChat
	}
	return UseGeneral
}

// inferCapabilities adds the capabilities llmfit infers from names and
// purposes, since the scraped catalog does not always record them.
func (m *Model) inferCapabilities() {
	name := strings.ToLower(m.Name)
	purpose := strings.ToLower(m.Purpose)
	arch := strings.ToLower(m.Architecture)
	add := func(c string, cond bool) {
		if cond && !m.HasCapability(c) {
			m.Capabilities = append(m.Capabilities, c)
		}
	}
	add(CapVision, strings.Contains(name, "vision") || strings.Contains(name, "-vl-") ||
		strings.HasSuffix(name, "-vl") || strings.Contains(name, "llava") ||
		strings.Contains(name, "onevision") || strings.Contains(name, "pixtral") ||
		strings.Contains(purpose, "vision") || strings.Contains(purpose, "multimodal"))
	add(CapToolUse, strings.Contains(purpose, "tool") || strings.Contains(purpose, "function call") ||
		strings.Contains(name, "qwen3") || strings.Contains(name, "qwen2.5") ||
		strings.Contains(name, "command-r") || strings.Contains(name, "hermes") ||
		(strings.Contains(name, "llama-3") && strings.Contains(name, "instruct")) ||
		(strings.Contains(name, "mistral") && strings.Contains(name, "instruct")) ||
		(strings.Contains(name, "gemma-3") && strings.HasSuffix(name, "-it")) ||
		(strings.Contains(name, "gemma-4") && strings.HasSuffix(name, "-it")))
	add(CapAudio, strings.Contains(arch, "whisper") || strings.Contains(name, "whisper") ||
		strings.Contains(purpose, "text-to-speech") || strings.Contains(purpose, "transcription") ||
		strings.Contains(purpose, "speech") || strings.Contains(purpose, "audio"))
	add(CapTTS, strings.Contains(purpose, "text-to-speech"))
}

func (m Model) format() string {
	if m.Format == "" {
		return "gguf"
	}
	return strings.ToLower(m.Format)
}

// isPrequantized: AWQ, GPTQ and AutoRound repos have a fixed bit width,
// can't be re-quantized and run under vLLM.
func (m Model) isPrequantized() bool {
	switch m.format() {
	case "awq", "gptq", "autoround":
		return true
	}
	return false
}

// requiresSpecializedRuntime marks text-to-speech entries, which none of
// the modelled runtimes can serve.
func (m Model) requiresSpecializedRuntime() bool { return m.HasCapability(CapTTS) }

// isMLXModel detects Apple-only MLX builds by the "-MLX-" naming convention
// or the mlx-community organization.
func (m Model) isMLXModel() bool {
	n := strings.ToLower(m.Name)
	return strings.Contains(n, "-mlx-") || strings.HasSuffix(n, "-mlx") || strings.HasPrefix(n, "mlx-community/")
}

// isMLXOnly is llmfit's looser check used to hide MLX repos off Apple Silicon.
func (m Model) isMLXOnly() bool {
	n := strings.ToUpper(m.Name)
	return strings.Contains(n, "-MLX") || strings.HasPrefix(n, "MLX-COMMUNITY/")
}

// isTernaryNative reports BitNet-style 1.58-bit models, which load only under
// bitnet.cpp. Full-precision masters and re-quantized repacks of them are
// excluded because bitnet.cpp can't load those.
func (m Model) isTernaryNative() bool {
	n := strings.ToLower(m.Name)
	if strings.Contains(n, "bf16") || strings.Contains(n, "unpacked") || strings.Contains(n, "-mlx-") ||
		strings.HasSuffix(n, "-mlx") || strings.Contains(n, "mlx-community") || strings.Contains(n, "prequantized") {
		return false
	}
	if strings.EqualFold(m.Architecture, "bitnet") {
		return true
	}
	for _, s := range []string{"bitnet", "ternary", "1.58b", "-1.58", "b1.58", "b1_58"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// isMXFP4Native reports gpt-oss models whose released weights are MXFP4, so
// llama.cpp is sized at MXFP4 instead of along the K-quant ladder. Repacks
// into another format are excluded.
func (m Model) isMXFP4Native() bool {
	arch := strings.ToLower(m.Architecture)
	if !strings.HasPrefix(arch, "gpt_oss") && !strings.HasPrefix(arch, "gptoss") {
		return false
	}
	n := strings.ToLower(m.Name)
	for _, s := range []string{"bf16", "-mlx", "mlx-community", "awq", "gptq", "nvfp4", "autoround"} {
		if strings.Contains(n, s) {
			return false
		}
	}
	return true
}

// basename is the repository part of the name, after the organization.
func (m Model) basename() string {
	if i := strings.LastIndexByte(m.Name, '/'); i >= 0 {
		return m.Name[i+1:]
	}
	return m.Name
}

// ParamsB is the parameter count in billions. Repacked quantization repos
// (NVFP4, AWQ, ...) often report the packed tensor count, a fraction of the
// real one, so for those the size in the name wins when it is clearly
// larger. A model with no size anywhere is assumed to be 7B, as in llmfit.
func (m Model) ParamsB() float64 {
	scraped, haveScraped := m.paramsBScraped()
	named, haveNamed := 0.0, false
	if isQuantRepack(m.Name) && !isDraftHeadName(m.basename()) {
		named, haveNamed = paramsBFromName(m.Name)
	}
	switch {
	case haveScraped && haveNamed && named-scraped > named*0.25:
		// Deliberately one-directional: packing can only undercount.
		return named
	case haveScraped:
		return scraped
	case haveNamed:
		return named
	}
	return 7.0
}

// paramsBScraped is the size from the catalog fields alone.
func (m Model) paramsBScraped() (float64, bool) {
	if m.ParametersRaw > 0 {
		return float64(m.ParametersRaw) / 1e9, true
	}
	s := strings.ToUpper(strings.TrimSpace(m.ParameterCount))
	if num, ok := strings.CutSuffix(s, "B"); ok {
		v, err := strconv.ParseFloat(num, 64)
		return v, err == nil
	}
	if num, ok := strings.CutSuffix(s, "M"); ok {
		v, err := strconv.ParseFloat(num, 64)
		return v / 1000, err == nil
	}
	return 0, false
}

var repackMarkers = []string{"nvfp4", "mxfp4", "mxfp8", "awq", "gptq", "int4", "int8", "w4a16", "w8a16"}

// isQuantRepack: the name says the weights were re-encoded, so the parent's
// size in the name still describes the model.
func isQuantRepack(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range repackMarkers {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// paramsBFromName reads the largest total-size token such as "27b" from a
// name. It skips MoE active counts ("a3b"), expert products ("8x7b") and
// Llama 4 style active-then-experts names ("17b-16e").
func paramsBFromName(name string) (float64, bool) {
	chars := []rune(strings.ToLower(name))
	best, found := 0.0, false
	isAlnum := func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	for i, c := range chars {
		if c != 'b' || (i+1 < len(chars) && isAlnum(chars[i+1])) {
			continue
		}
		start, seenDot := i, false
		for start > 0 {
			p := chars[start-1]
			if isDigit(p) {
				start--
			} else if (p == '.' || p == '_') && !seenDot && start >= 2 && isDigit(chars[start-2]) {
				// "1_6b" is 1.6B, but "internlm2_5-7b" still ends at the '-'.
				seenDot = true
				start--
			} else {
				break
			}
		}
		if start == i {
			continue
		}
		if start > 0 && (chars[start-1] == 'x' || isAlnum(chars[start-1])) {
			continue
		}
		if followedByExpertCount(chars, i) {
			continue
		}
		token := strings.ReplaceAll(string(chars[start:i]), "_", ".")
		if v, err := strconv.ParseFloat(token, 64); err == nil && v > 0 && (!found || v > best) {
			best, found = v, true
		}
	}
	return best, found
}

// followedByExpertCount: "17B-16E" names the active count and then the
// number of experts, so the 17 is not the model's total size.
func followedByExpertCount(chars []rune, b int) bool {
	j := b + 1
	if j >= len(chars) || (chars[j] != '-' && chars[j] != '_' && chars[j] != '.') {
		return false
	}
	j++
	digits := j
	for j < len(chars) && chars[j] >= '0' && chars[j] <= '9' {
		j++
	}
	if j == digits || j >= len(chars) || chars[j] != 'e' {
		return false
	}
	return j+1 >= len(chars) || !(chars[j+1] >= 'a' && chars[j+1] <= 'z' || chars[j+1] >= '0' && chars[j+1] <= '9')
}

var (
	draftTokenRE  = regexp.MustCompile(`(?i)^(eagle|dflash|dspark)\d*$`)
	sizeTokenRE   = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)b$`)
	activeTokenRE = regexp.MustCompile(`(?i)^a\d+(?:\.\d+)?b$`)
)

// isDraftHeadName reports speculative-decoding draft heads (EAGLE, DFlash,
// DSpark), which are not standalone models. MTP is deliberately not a marker.
func isDraftHeadName(basename string) bool {
	for _, tok := range strings.FieldsFunc(basename, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if draftTokenRE.MatchString(tok) {
			return true
		}
	}
	return false
}

// nameDerivedParamsB is the largest "<n>B" token in a basename, ignoring MoE
// active markers such as "A3B". Used only by the sanity check.
func nameDerivedParamsB(basename string) (float64, bool) {
	best, found := 0.0, false
	consider := func(tok string) {
		if activeTokenRE.MatchString(tok) {
			return
		}
		if mm := sizeTokenRE.FindStringSubmatch(tok); mm != nil {
			if v, err := strconv.ParseFloat(mm[1], 64); err == nil && (!found || v > best) {
				best, found = v, true
			}
		}
	}
	for _, tok := range strings.FieldsFunc(basename, func(r rune) bool { return r == '-' || r == '_' || r == ' ' }) {
		if sizeTokenRE.MatchString(tok) {
			consider(tok)
			continue
		}
		// Split on dots, but rejoin "1" + "7B" so a decimal size survives.
		parts := strings.Split(tok, ".")
		for i := 0; i < len(parts); i++ {
			if i+1 < len(parts) && parts[i] != "" && strings.Trim(parts[i], "0123456789") == "" {
				if joined := parts[i] + "." + parts[i+1]; sizeTokenRE.MatchString(joined) {
					consider(joined)
					i++
					continue
				}
			}
			consider(parts[i])
		}
	}
	return best, found
}

// SanityIssue explains why llmfit keeps this entry out of rankings, or
// returns "" for a clean entry: a speculative-decoding draft head, a name
// whose size disagrees with the catalog by 4x or more, or a minimum RAM
// figure implying an impossible number of bits per parameter.
func (m Model) SanityIssue() string {
	base := m.basename()
	if isDraftHeadName(base) {
		return fmt.Sprintf("%q looks like a speculative-decoding draft head, not a standalone model", m.Name)
	}
	actual, haveActual := m.paramsBScraped()
	if named, ok := nameDerivedParamsB(base); ok && haveActual && named > 0 && actual > 0 {
		if ratio := math.Max(named/actual, actual/named); ratio >= 4 {
			return fmt.Sprintf("name implies ~%.1fB parameters but the catalog declares %.1fB (%.1fx apart)", named, actual, ratio)
		}
	}
	if haveActual && actual > 0 {
		bpp := m.MinRAMGB * (1 << 30) * 8 / (actual * 1e9)
		// The scraper floors min_ram_gb at 1 GB, which can't prove a model
		// too large for its size, only too small.
		atFloor := m.MinRAMGB <= 1.0
		if bpp < 1 || (bpp > 33 && !atFloor) {
			return fmt.Sprintf("min_ram_gb %.2f GB implies %.2f bits per parameter at %.1fB, outside 1 to 33", m.MinRAMGB, bpp, m.ParamsB())
		}
	}
	return ""
}

// attentionLayout splits hybrid models into full-attention layers, which
// keep a context-sized KV cache, and linear or state-space layers, which keep
// a fixed-size state.
type attentionLayout struct{ full, linear int }

func (l attentionLayout) fraction() float64 {
	total := l.full + l.linear
	if total == 0 {
		return 1
	}
	return float64(l.full) / float64(total)
}

// normalizedFor scales a family ratio template to a model's layer count:
// 16/48 on a 24-layer model becomes 6/18.
func (l attentionLayout) normalizedFor(layers int) attentionLayout {
	total := l.full + l.linear
	if total == 0 {
		return attentionLayout{full: layers}
	}
	if total == layers {
		return l
	}
	full := min((layers*l.full+total/2)/total, layers)
	return attentionLayout{full: full, linear: layers - full}
}

// layoutFromName knows the published layer splits of the hybrid families.
func layoutFromName(name string) (attentionLayout, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "qwen3-next") || strings.Contains(n, "qwen3.5-next"):
		return attentionLayout{10, 30}, true
	case strings.Contains(n, "qwen3.5-") || strings.Contains(n, "qwen3.6-") || strings.Contains(n, "qwen3.8-"):
		switch {
		case strings.Contains(n, "-a95b"):
			return attentionLayout{23, 69}, true
		case strings.Contains(n, "-a3b") || strings.Contains(n, "-a10b") || strings.Contains(n, "-a17b"):
			return attentionLayout{10, 30}, true
		}
		return attentionLayout{16, 48}, true
	case strings.Contains(n, "jamba"):
		return attentionLayout{4, 28}, true
	case strings.Contains(n, "zamba"):
		return attentionLayout{2, 54}, true
	case strings.Contains(n, "mamba") || strings.Contains(n, "rwkv"):
		return attentionLayout{0, 1}, true
	}
	return attentionLayout{}, false
}

// effectiveLayout returns the hybrid layout, if any, scaled to the model.
// A name match that would erase attention the metadata says exists (such as
// a Llama fine-tune with "mamba" in its name) is ignored.
func (m Model) effectiveLayout() (attentionLayout, bool) {
	l, ok := layoutFromName(m.Name)
	if !ok {
		return l, false
	}
	if l.full == 0 && !m.zeroAttentionPlausible() {
		return l, false
	}
	if m.NumHiddenLayers > 0 {
		l = l.normalizedFor(m.NumHiddenLayers)
	}
	return l, true
}

func (m Model) zeroAttentionPlausible() bool {
	if m.NumAttentionHeads > 0 || m.NumKeyValueHeads > 0 {
		return false
	}
	if m.Architecture == "" {
		return true
	}
	a := strings.ToLower(m.Architecture)
	recurrent := strings.Contains(a, "mamba") || strings.HasPrefix(a, "rwkv")
	hybrid := strings.Contains(a, "hybrid") || strings.Contains(a, "llama") || strings.Contains(a, "mistral") ||
		strings.Contains(a, "qwen") || strings.Contains(a, "gemma")
	return recurrent && !hybrid
}

// mlaKVWidth: Multi-head Latent Attention models cache one compressed
// latent (kv_lora_rank 512 + rope 64) per token and layer.
func (m Model) mlaKVWidth() int {
	switch strings.ToLower(m.Architecture) {
	case "deepseek_v2", "deepseek_v3", "kimi_k2", "kimi_k25", "kimi_vl", "glm4_moe_lite":
		return 576
	}
	return 0
}

// kvCacheGB is the fp16 KV cache at ctx tokens. With layer, head and
// head-size metadata it is exact (2 * layers * kv_heads * head_dim * ctx *
// 2 bytes, full-attention layers only); otherwise llmfit's coarse
// 0.000008 * params_b * ctx approximation.
func (m Model) kvCacheGB(ctx int) float64 {
	const fp16 = 2.0
	layout, hybrid := m.effectiveLayout()
	fullLayers := m.NumHiddenLayers
	if hybrid {
		fullLayers = layout.full
	}
	if width := m.mlaKVWidth(); m.NumHiddenLayers > 0 && width > 0 {
		return float64(width) * float64(ctx) * fp16 * float64(fullLayers) / (1 << 30)
	}
	if m.NumHiddenLayers > 0 && m.HeadDim > 0 {
		kvHeads := m.NumKeyValueHeads
		if kvHeads == 0 {
			kvHeads = m.NumAttentionHeads
		}
		if kvHeads == 0 {
			kvHeads = 8
		}
		perLayer := 2 * float64(kvHeads) * float64(m.HeadDim) * float64(ctx) * fp16
		return perLayer * float64(fullLayers) / (1 << 30)
	}
	frac := 1.0
	if hybrid {
		frac = layout.fraction()
	}
	return 0.000008 * m.ParamsB() * float64(ctx) * frac
}

// runtimeOverheadGB covers the CUDA/Metal context, buffers and any fixed
// recurrent state.
const runtimeOverheadGB = 0.5

// EstimateMemoryGB is the memory needed to run the model at a quantization
// with ctx tokens of context: weights + fp16 KV cache + runtime overhead.
func (m Model) EstimateMemoryGB(quant string, ctx int) float64 {
	return m.ParamsB()*quantBPP(quant) + m.kvCacheGB(ctx) + runtimeOverheadGB
}

// bestQuantForBudget walks a ladder best-first for a level that fits the
// budget, then tries once more at half the context (not below 1024 tokens).
func (m Model) bestQuantForBudget(budget float64, ctx int, ladder []string) (string, float64, bool) {
	for _, q := range ladder {
		if mem := m.EstimateMemoryGB(q, ctx); mem <= budget {
			return q, mem, true
		}
	}
	if half := ctx / 2; half >= 1024 {
		for _, q := range ladder {
			if mem := m.EstimateMemoryGB(q, half); mem <= budget {
				return q, mem, true
			}
		}
	}
	return "", 0, false
}

// moeActiveVRAMGB is the VRAM for the active experts only, at quant.
func (m Model) moeActiveVRAMGB(quant string) (float64, bool) {
	if !m.IsMoE || m.ActiveParameters == 0 {
		return 0, false
	}
	size := float64(m.ActiveParameters) * quantBPP(quant) / (1 << 30)
	return max(size*1.1, 0.5), true
}

// moeOffloadedRAMGB is the RAM for the inactive experts at the catalog quant.
func (m Model) moeOffloadedRAMGB() (float64, bool) {
	if !m.IsMoE || m.ActiveParameters == 0 || m.ParametersRaw == 0 {
		return 0, false
	}
	inactive := float64(m.ParametersRaw) - float64(m.ActiveParameters)
	if inactive <= 0 {
		return 0, true
	}
	return inactive * quantBPP(m.Quantization) / (1 << 30), true
}

// moeFixedEffectiveBPP converts the per-token fixed MoE work (attention,
// router, shared experts, head) into bandwidth-equivalent bytes. Fitted to
// OLMoE on an RX 6900 XT.
const moeFixedEffectiveBPP = 3.2

// moeBandwidthDecomposition splits an MoE model's per-token reads into the
// active expert FFN weights (which scale with quantization) and the fixed
// part, both in billions of parameters. It needs architecture metadata.
func (m Model) moeBandwidthDecomposition() (activeFFN, fixed float64, ok bool) {
	if !m.IsMoE || m.HiddenSize == 0 || m.NumHiddenLayers == 0 || m.ActiveExperts == 0 ||
		m.MoEIntermediateSize == 0 || m.VocabSize == 0 {
		return 0, 0, false
	}
	hidden := float64(m.HiddenSize)
	layers := float64(m.NumHiddenLayers)
	experts := float64(m.NumExperts)
	if m.NumExperts == 0 {
		experts = 8
	}
	heads := float64(max(m.NumAttentionHeads, 1))
	kv := heads
	if m.NumKeyValueHeads > 0 {
		kv = float64(m.NumKeyValueHeads)
	}
	headDim := hidden / heads
	if m.HeadDim > 0 {
		headDim = float64(m.HeadDim)
	}
	// SwiGLU experts have three projections each.
	active := layers * float64(m.ActiveExperts) * 3 * hidden * float64(m.MoEIntermediateSize)
	attention := layers * (2*heads*headDim*hidden + 2*kv*headDim*hidden)
	shared := layers * 3 * hidden * float64(m.SharedExpertIntermediateSize)
	router := layers * experts * hidden
	headAndEmbedding := 2 * float64(m.VocabSize) * hidden
	return active / 1e9, (attention + shared + router + headAndEmbedding) / 1e9, true
}

// qwenMinorGeneration reads an explicit minor version such as Qwen3.8.
func qwenMinorGeneration(lower string) (float64, bool) {
	for _, v := range []struct {
		dotted, underscored string
		gen                 float64
	}{{"qwen3.8", "qwen3_8", 3.8}, {"qwen3.6", "qwen3_6", 3.6}, {"qwen3.5", "qwen3_5", 3.5}, {"qwen2.5", "qwen2_5", 2.5}} {
		if strings.Contains(lower, v.dotted) || strings.Contains(lower, v.underscored) {
			return v.gen, true
		}
	}
	return 0, false
}

// parseGeneration reads a family generation (2 for qwen2, 3.5 for
// qwen3_5_moe, 4 for llama4) from the architecture string, then the name.
func parseGeneration(architecture, name string) (float64, bool) {
	lowerName := strings.ToLower(name)
	if architecture != "" {
		a := strings.ToLower(architecture)
		after := func(prefix string) (string, bool) { return strings.CutPrefix(a, prefix) }
		if strings.HasPrefix(a, "deepseek") {
			switch {
			case strings.Contains(a, "v4"):
				return 4, true
			case strings.Contains(a, "v3"):
				return 3, true
			case strings.Contains(a, "v2"):
				return 2, true
			}
			return 1, true
		}
		if s, ok := after("qwen"); ok {
			if g, ok := qwenMinorGeneration(lowerName); ok {
				return g, true
			}
			switch {
			case strings.HasPrefix(s, "3_5") || strings.HasPrefix(s, "3.5"):
				return 3.5, true
			case strings.HasPrefix(s, "3_next") || strings.HasPrefix(s, "3next"):
				return 3.8, true
			case strings.HasPrefix(s, "3"):
				return 3, true
			case strings.HasPrefix(s, "2"):
				return 2, true
			}
			return 1, true
		}
		if s, ok := after("llama"); ok && strings.HasPrefix(s, "4") {
			return 4, true
		}
		if s, ok := after("gemma"); ok {
			for _, g := range []string{"4", "3", "2"} {
				if strings.HasPrefix(s, g) {
					v, _ := strconv.ParseFloat(g, 64)
					return v, true
				}
			}
			return 1, true
		}
		if s, ok := after("phi"); ok {
			switch {
			case strings.HasPrefix(s, "4"):
				return 4, true
			case strings.HasPrefix(s, "3") || strings.HasPrefix(s, "moe"):
				return 3, true
			case strings.HasPrefix(s, "2"):
				return 2, true
			}
			return 1, true
		}
		if strings.HasPrefix(a, "mistral") || strings.HasPrefix(a, "mixtral") {
			return 1, true
		}
		if s, ok := after("cohere"); ok {
			if strings.HasPrefix(s, "2") {
				return 2, true
			}
			return 1, true
		}
		if s, ok := after("falcon"); ok {
			if strings.HasPrefix(s, "3") {
				return 3, true
			}
			return 1, true
		}
		if s, ok := after("granite"); ok {
			if strings.HasPrefix(s, "4") {
				return 4, true
			}
			return 1, true
		}
	}
	if g, ok := qwenMinorGeneration(lowerName); ok {
		return g, true
	}
	for _, v := range []struct {
		subs []string
		gen  float64
	}{
		{[]string{"qwen3"}, 3}, {[]string{"qwen2"}, 2},
		{[]string{"llama-4", "llama4"}, 4}, {[]string{"llama-3.3", "llama3.3"}, 3.3},
		{[]string{"llama-3.2", "llama3.2"}, 3.2}, {[]string{"llama-3.1", "llama3.1"}, 3.1},
		{[]string{"llama-3", "llama3"}, 3}, {[]string{"llama-2", "llama2"}, 2},
		{[]string{"gemma-4", "gemma4"}, 4}, {[]string{"gemma-3", "gemma3"}, 3}, {[]string{"gemma-2", "gemma2"}, 2},
		{[]string{"deepseek-v4", "deepseekv4"}, 4}, {[]string{"deepseek-v3", "deepseekv3"}, 3},
		{[]string{"deepseek-v2", "deepseekv2"}, 2},
		{[]string{"phi-4", "phi4"}, 4}, {[]string{"phi-3", "phi3"}, 3},
	} {
		for _, s := range v.subs {
			if strings.Contains(lowerName, s) {
				return v.gen, true
			}
		}
	}
	return 0, false
}

// generationBonus adds 3 quality points per generation above the first,
// capped at 9: newer generations do more per parameter.
func generationBonus(architecture, name string) float64 {
	g, ok := parseGeneration(architecture, name)
	if !ok {
		return 0
	}
	return math.Min(math.Max((g-1)*3, 0), 9)
}
