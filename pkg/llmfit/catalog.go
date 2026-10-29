package llmfit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
)

// catalogGz is the curated subset of llmfit's hf_models.json, written by
// internal/gencatalog. That program's doc comment lists how models are
// chosen; regenerate with:
//
//	go run ./pkg/llmfit/internal/gencatalog path/to/hf_models.json
//
//go:embed catalog.json.gz
var catalogGz []byte

var embeddedCatalog = sync.OnceValues(func() ([]Model, error) {
	return ParseCatalog(bytes.NewReader(catalogGz))
})

// Catalog returns the embedded curated model list. Each call returns a new
// slice, so callers may sort or filter it freely.
func Catalog() []Model {
	models, err := embeddedCatalog()
	if err != nil {
		// The file is generated and checked by the tests, so this is a
		// build defect rather than something a caller can handle.
		panic("llmfit: embedded catalog is invalid: " + err.Error())
	}
	return slices.Clone(models)
}

// LoadCatalog reads a model list in llmfit's hf_models.json format, such as
// the full file from llmfit's repository. Gzip-compressed files are accepted
// too.
func LoadCatalog(path string) ([]Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	models, err := ParseCatalog(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return models, nil
}

// ParseCatalog decodes a JSON array of hf_models.json entries, plain or
// gzip-compressed. Like llmfit it merges entries that name the same model
// under different organizations or spellings, infers capabilities the
// catalog leaves out, and corrects known-wrong MoE active-parameter counts.
func ParseCatalog(r io.Reader) ([]Model, error) {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	} else {
		r = br
	}
	var models []Model
	if err := json.NewDecoder(r).Decode(&models); err != nil {
		return nil, fmt.Errorf("decoding model catalog: %w", err)
	}
	models = dedupeModels(models)
	for i := range models {
		models[i].inferCapabilities()
		applyPublishedMoEActive(&models[i])
	}
	return models, nil
}

// CanonicalSlug is the key llmfit deduplicates on: the repository name
// without its organization, lowercased, with '-', '_' and '.' removed, so
// "meta-llama/Llama-3.1-8B" and "unsloth/llama-3.1-8b" are one model.
func CanonicalSlug(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(name))
}

// dedupeModels merges entries with the same CanonicalSlug, keeping the
// first one's name and position. Like llmfit it keeps the larger sizes and
// context, the later release date, the union of capabilities and GGUF
// sources, and the first known architecture details.
func dedupeModels(models []Model) []Model {
	index := make(map[string]int, len(models))
	out := make([]Model, 0, len(models))
	for _, m := range models {
		key := CanonicalSlug(m.Name)
		i, seen := index[key]
		if !seen {
			index[key] = len(out)
			m.Capabilities = slices.Clone(m.Capabilities)
			m.GGUFSources = slices.Clone(m.GGUFSources)
			out = append(out, m)
			continue
		}
		e := &out[i]
		if m.ParametersRaw > e.ParametersRaw {
			e.ParameterCount, e.ParametersRaw = m.ParameterCount, m.ParametersRaw
		}
		e.MinRAMGB = math.Max(e.MinRAMGB, m.MinRAMGB)
		e.RecommendedRAMGB = math.Max(e.RecommendedRAMGB, m.RecommendedRAMGB)
		e.MinVRAMGB = math.Max(e.MinVRAMGB, m.MinVRAMGB)
		e.ContextLength = max(e.ContextLength, m.ContextLength)
		if m.IsMoE && !e.IsMoE {
			e.IsMoE, e.NumExperts, e.ActiveExperts, e.ActiveParameters = true, m.NumExperts, m.ActiveExperts, m.ActiveParameters
		}
		// Dates are YYYY-MM-DD, so string order is date order.
		if m.ReleaseDate > e.ReleaseDate {
			e.ReleaseDate = m.ReleaseDate
		}
		for _, c := range m.Capabilities {
			if !slices.Contains(e.Capabilities, c) {
				e.Capabilities = append(e.Capabilities, c)
			}
		}
		for _, s := range m.GGUFSources {
			if !slices.ContainsFunc(e.GGUFSources, func(x GGUFSource) bool { return x.Repo == s.Repo }) {
				e.GGUFSources = append(e.GGUFSources, s)
			}
		}
		e.Downloads = max(e.Downloads, m.Downloads)
		if e.NumAttentionHeads == 0 {
			e.NumAttentionHeads = m.NumAttentionHeads
		}
		if e.NumKeyValueHeads == 0 {
			e.NumKeyValueHeads = m.NumKeyValueHeads
		}
		if e.NumHiddenLayers == 0 {
			e.NumHiddenLayers = m.NumHiddenLayers
		}
		if e.HeadDim == 0 {
			e.HeadDim = m.HeadDim
		}
		if e.License == "" {
			e.License = m.License
		}
	}
	return out
}

// publishedMoEActive overrules catalog active-parameter counts that
// contradict a model card. The scraper double-counts gpt-oss-120b's SwiGLU
// expert projections (9.31B instead of the published 5.1B), which would
// halve its speed estimate. Matched on architecture and expert geometry so
// re-uploads and fine-tunes are corrected too.
var publishedMoEActive = []struct {
	archPrefix            string
	experts, activeExpert int
	total, active         float64
}{
	// https://huggingface.co/openai/gpt-oss-120b: 116.8B total, 5.1B active.
	{"gpt_oss", 128, 4, 116.8e9, 5.1e9},
}

func applyPublishedMoEActive(m *Model) {
	if m.Architecture == "" || m.ActiveParameters == 0 {
		return
	}
	arch := strings.ToLower(m.Architecture)
	total := m.ParamsB() * 1e9
	for _, k := range publishedMoEActive {
		if !strings.HasPrefix(arch, k.archPrefix) || m.NumExperts != k.experts || m.ActiveExperts != k.activeExpert {
			continue
		}
		// Within 10% of the published total: same model, not a pruned one.
		if math.Abs(total-k.total) > k.total*0.10 {
			continue
		}
		current := float64(m.ActiveParameters)
		if math.Max(current/k.active, k.active/current) >= 1.2 {
			m.ActiveParameters = uint64(k.active)
		}
		return
	}
}
