package llmfit

import (
	"regexp"
	"strings"
)

// repoKey is the lookup key of the mapping tables.
func repoKey(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

// OllamaTag returns the Ollama library tag for a model, such as
// "qwen3:8b" for Qwen/Qwen3-8B, or "" when llmfit knows none.
func OllamaTag(name string) string { return ollamaTags[repoKey(name)] }

// GGUFRepo returns a Hugging Face repository with GGUF builds of the model:
// the first GGUF source the catalog records, else llmfit's curated table,
// else "". llama.cpp loads these directly, and Ollama can pull them as
// hf.co/<repo>.
func GGUFRepo(m Model) string {
	if len(m.GGUFSources) > 0 && m.GGUFSources[0].Repo != "" {
		return m.GGUFSources[0].Repo
	}
	return ggufRepos[repoKey(m.Name)]
}

// MLXRepo names the MLX build to pull for a model on Apple Silicon. A repo
// that already is an MLX build is returned as is; otherwise this is
// llmfit's guess at the 4-bit mlx-community conversion, which may not
// exist (llmfit checks before downloading). Pre-quantized AWQ/GPTQ repos
// have no MLX twin and return "".
//
// llmfit lowercases the guess; this keeps the original spelling, which
// Hugging Face resolves the same way and reads better.
func MLXRepo(name string) string {
	owner, repo, hasOwner := strings.Cut(name, "/")
	if !hasOwner {
		owner, repo = "", name
	}
	if hasOwner && !strings.Contains(repo, "/") && owner != "" && repo != "" && isLikelyMLXRepo(owner, repo) {
		return name
	}
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		repo = name[i+1:]
	}
	lower := strings.ToLower(repo)
	if !strings.Contains(lower, "mlx") && isLikelyPrequantizedRepo(lower) {
		return ""
	}
	base := stripMLXQuantSuffix(repo)
	if base == "" {
		return ""
	}
	return "mlx-community/" + base + "-4bit"
}

func isLikelyMLXRepo(owner, repo string) bool {
	o, r := strings.ToLower(owner), strings.ToLower(repo)
	if strings.Contains(r, "-gguf") || strings.HasSuffix(r, "gguf") {
		return false
	}
	return o == "mlx-community" || strings.Contains(r, "-mlx-") || strings.HasSuffix(r, "-mlx") ||
		strings.Contains(r, "mlx-") || strings.HasSuffix(r, "mlx")
}

func isLikelyPrequantizedRepo(lower string) bool {
	for _, s := range []string{"awq", "gptq", "autoround", "auto-round"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// mlxQuantSuffixRE matches mlx-community's trailing quant scheme, such as
// "-4bit", "-4bit-dwq" or "-mxfp4-q8".
var mlxQuantSuffixRE = regexp.MustCompile(`(?i)-(?:\d+bit(?:-[a-z0-9]+)*|mxfp4(?:-[a-z0-9]+)*|fp16)$`)

// stripMLXQuantSuffix reduces an MLX repo name to its model base:
// "Llama-3.2-1B-Instruct-MLX-4bit" becomes "Llama-3.2-1B-Instruct".
func stripMLXQuantSuffix(repo string) string {
	base := repo
	if loc := mlxQuantSuffixRE.FindStringIndex(base); loc != nil && loc[0] > 0 {
		base = base[:loc[0]]
	}
	if len(base) > len("-mlx") && strings.EqualFold(base[len(base)-len("-mlx"):], "-mlx") {
		base = base[:len(base)-len("-mlx")]
	}
	return strings.Trim(base, "-")
}
