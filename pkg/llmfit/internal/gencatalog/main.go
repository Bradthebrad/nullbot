// Command gencatalog writes the model catalog embedded in package llmfit:
// a curated subset of llmfit's data/hf_models.json (about 17,700 entries
// and 16 MB, too big to embed). Run it from the repository root:
//
//	go run ./pkg/llmfit/internal/gencatalog path/to/hf_models.json
//
// A model is eligible when all of these hold:
//
//   - It generates text: the Hub pipeline is text-generation,
//     image-text-to-text, any-to-any or missing (several Mistral releases
//     have none), and it is not an embedding, speech or transcription
//     model.
//   - Its weights are an original release that llama.cpp, Ollama and MLX
//     quantize themselves (format gguf in llmfit's catalog), not an
//     AWQ/GPTQ/AutoRound/MLX repack, and the name carries no repack marker
//     such as FP8, NVFP4, GGUF, MLX or 4bit.
//   - It has at least 100 million parameters and a context window of at
//     least 2,048 tokens.
//   - It passes llmfit's sanity checks (no speculative-decoding draft
//     heads, no size/name mismatch, no impossible memory figure).
//   - It is not an uncensored, abliterated or NSFW variant: a default list
//     an assistant recommends from should hold the original releases.
//
// An eligible model is kept when any of these hold:
//
//   - A well-known model maker published it (makers below).
//   - llmfit maps it to an Ollama tag or a curated GGUF repository.
//   - It is among the 10 most downloaded of its architecture family (the
//     Hub model_type, such as llama or qwen3), with at least 50,000
//     downloads, a text-generation or vision pipeline tag on the Hub, and
//     at most 3 from one publisher.
//
// Re-uploads of one model (same llmfit.CanonicalSlug) become one entry
// named after the original maker, keeping the GGUF sources of all of them.
package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Bradthebrad/nullbot/pkg/llmfit"
)

// makers publish the base models people run locally; everything they
// release that is eligible is kept.
var makers = orgSet(`
	Qwen meta-llama google mistralai microsoft deepseek-ai openai ibm-granite
	allenai nvidia HuggingFaceTB tiiuae 01-ai CohereLabs CohereForAI LiquidAI
	zai-org THUDM moonshotai MiniMaxAI internlm baidu tencent stabilityai
	bigcode upstage NousResearch openbmb LGAI-EXAONE apple Salesforce
	XiaomiMiMo inclusionAI ByteDance-Seed swiss-ai arcee-ai ai21labs Snowflake
	stepfun-ai rednote-hilab meituan-longcat PrimeIntellect sarvamai kakaocorp
	OpenGVLab`)

const (
	minParameters      = 100_000_000
	minContext         = 2048
	popularDownloads   = 50_000
	popularPerFamily   = 10
	popularPerProvider = 3
)

// taggedPipelines are the Hub pipeline tags of text-generating models.
// Makers' and mapped models may also have none ("unknown" or ""), as
// several Mistral releases do.
var (
	taggedPipelines = []string{"text-generation", "image-text-to-text", "any-to-any"}
	textPipelines   = append(slices.Clone(taggedPipelines), "unknown", "")
)

// excludedMarkers in a name mean the repo re-encodes another model's
// weights, or strips or replaces its safety tuning.
var excludedMarkers = []string{
	"fp8", "fp4", "mxfp8", "awq", "gptq", "int4", "int8", "w4a16", "w8a16", "w8a8",
	"gguf", "mlx", "exl2", "exl3", "bnb", "4bit", "8bit", "autoround", "quantized",
	"abliterat", "uncensor", "unfiltered", "heretic", "heresy", "obliterat", "dealign", "jailbr",
	"crack", "nsfw", "erotic", "lewd", "-sex",
}

func orgSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, o := range strings.Fields(s) {
		set[strings.ToLower(o)] = true
	}
	return set
}

func org(m llmfit.Model) string {
	o, _, _ := strings.Cut(m.Name, "/")
	return strings.ToLower(o)
}

func eligible(m llmfit.Model) bool {
	name := strings.ToLower(m.Name)
	purpose := strings.ToLower(m.Purpose)
	switch {
	case !slices.Contains(textPipelines, m.PipelineTag),
		m.UseCase() == llmfit.UseEmbedding,
		strings.Contains(purpose, "speech"), strings.Contains(purpose, "transcription"),
		m.HasCapability(llmfit.CapTTS),
		m.Format != "" && !strings.EqualFold(m.Format, "gguf"),
		m.ParametersRaw < minParameters,
		m.ContextLength < minContext,
		m.SanityIssue() != "":
		return false
	}
	for _, s := range excludedMarkers {
		if strings.Contains(name, s) {
			return false
		}
	}
	return true
}

func mapped(m llmfit.Model) bool {
	return llmfit.OllamaTag(m.Name) != "" || llmfit.GGUFRepo(llmfit.Model{Name: m.Name}) != ""
}

// selectModels applies the rules in the package comment.
func selectModels(all []llmfit.Model) (kept []llmfit.Model, fromMakers, fromMappings, popular int) {
	var rest []llmfit.Model
	for _, m := range all {
		switch {
		case !eligible(m):
		case makers[org(m)]:
			kept = append(kept, m)
			fromMakers++
		case mapped(m):
			kept = append(kept, m)
			fromMappings++
		default:
			rest = append(rest, m)
		}
	}
	byDownloads := func(a, b llmfit.Model) int {
		return cmp.Or(cmp.Compare(b.Downloads, a.Downloads), strings.Compare(a.Name, b.Name))
	}
	slices.SortFunc(rest, byDownloads)
	perFamily, perProvider := map[string]int{}, map[string]int{}
	for _, m := range rest {
		family := cmp.Or(strings.ToLower(m.Architecture), "unknown")
		if m.Downloads < popularDownloads || !slices.Contains(taggedPipelines, m.PipelineTag) ||
			perFamily[family] >= popularPerFamily || perProvider[org(m)] >= popularPerProvider {
			continue
		}
		perFamily[family]++
		perProvider[org(m)]++
		kept = append(kept, m)
		popular++
	}

	// Merge re-uploads into one entry, the maker's own if there is one,
	// else the most downloaded.
	slices.SortStableFunc(kept, func(a, b llmfit.Model) int {
		if ma, mb := makers[org(a)], makers[org(b)]; ma != mb {
			if ma {
				return -1
			}
			return 1
		}
		return byDownloads(a, b)
	})
	index := map[string]int{}
	var merged []llmfit.Model
	for _, m := range kept {
		key := llmfit.CanonicalSlug(m.Name)
		i, seen := index[key]
		if !seen {
			index[key] = len(merged)
			merged = append(merged, m)
			continue
		}
		e := &merged[i]
		e.Downloads = max(e.Downloads, m.Downloads)
		for _, s := range m.GGUFSources {
			if !slices.ContainsFunc(e.GGUFSources, func(x llmfit.GGUFSource) bool { return x.Repo == s.Repo }) {
				e.GGUFSources = append(e.GGUFSources, s)
			}
		}
	}
	slices.SortFunc(merged, func(a, b llmfit.Model) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name))
	})
	return merged, fromMakers, fromMappings, popular
}

func main() {
	out := flag.String("o", "pkg/llmfit/catalog.json.gz", "output file")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gencatalog [-o catalog.json.gz] path/to/hf_models.json")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *out); err != nil {
		fmt.Fprintln(os.Stderr, "gencatalog:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var all []llmfit.Model
	if err := json.Unmarshal(data, &all); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	models, fromMakers, fromMappings, popular := selectModels(all)

	// One model per line keeps the uncompressed form readable.
	var plain bytes.Buffer
	plain.WriteString("[\n")
	for i, m := range models {
		line, err := json.Marshal(m)
		if err != nil {
			return err
		}
		plain.Write(line)
		if i < len(models)-1 {
			plain.WriteByte(',')
		}
		plain.WriteByte('\n')
	}
	plain.WriteString("]\n")

	// No name or timestamp in the gzip header, so the output depends only
	// on the input.
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(plain.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(out, gz.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d of %d models (%d from makers, %d mapped, %d popular, before merging re-uploads): %d KB, %d KB uncompressed\n",
		len(models), len(all), fromMakers, fromMappings, popular, gz.Len()/1024, plain.Len()/1024)
	return nil
}
