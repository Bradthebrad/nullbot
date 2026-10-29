package hfhub

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultQuant is what Recommend aims for when the caller states no
// preference: the usual balance of size and quality for local use.
const DefaultQuant = "Q4_K_M"

// splitRe matches llama.cpp's split naming, "<name>-00001-of-00003.gguf".
var splitRe = regexp.MustCompile(`(?i)^(.*)-(\d{5})-of-(\d{5})\.gguf$`)

// quantRe finds a quantization label between separators. The label set
// follows llama.cpp's type names plus the variants publishers add: _L/_XL
// (some tensors kept at higher precision) and unsloth's "UD-" prefix.
var quantRe = regexp.MustCompile(`(?i)(?:^|[-._ ])((?:UD-)?(?:IQ[1-4]_(?:XXS|XS|NL|S|M)|Q[2-8]_K(?:_(?:XXL|XL|L|M|S))?|Q[4-8]_[01](?:_[48]_[48])?|TQ[12]_0|MXFP4(?:_MOE)?|BF16|FP16|F16|FP32|F32))(?:$|[-._ ])`)

// quantBits holds approximate bits per weight. They are only used to order
// quantizations from smaller to larger, so rough values are enough; they
// follow the figures llama.cpp's quantize tool prints.
var quantBits = map[string]float64{
	"F32": 32, "F16": 16, "BF16": 16,
	"Q8_K_XL": 9, "Q8_0": 8.5,
	"Q6_K_XL": 6.9, "Q6_K_L": 6.7, "Q6_K": 6.56,
	"Q5_1": 6.0, "Q5_K_XL": 5.9, "Q5_K_L": 5.8, "Q5_K_M": 5.67, "Q5_K": 5.67, "Q5_K_S": 5.52, "Q5_0": 5.5,
	"Q4_1": 5.0, "Q4_K_XL": 5.0, "Q4_K_L": 4.95, "Q4_K_M": 4.83, "Q4_K": 4.83, "Q4_K_S": 4.57,
	"Q4_0": 4.5, "Q4_0_4_4": 4.5, "Q4_0_4_8": 4.5, "Q4_0_8_8": 4.5, "IQ4_NL": 4.5,
	"IQ4_XS": 4.25, "MXFP4": 4.25, "MXFP4_MOE": 4.25,
	"Q3_K_XL": 4.4, "Q3_K_L": 4.27, "Q3_K_M": 3.91, "Q3_K": 3.91, "IQ3_M": 3.66, "Q3_K_S": 3.5,
	"IQ3_S": 3.44, "IQ3_XS": 3.3, "IQ3_XXS": 3.06,
	"Q2_K_XL": 3.2, "Q2_K_L": 3.1, "Q2_K": 2.96, "Q2_K_S": 2.8, "IQ2_M": 2.7, "IQ2_S": 2.5,
	"IQ2_XS": 2.31, "IQ2_XXS": 2.06, "TQ2_0": 2.06, "IQ1_M": 1.75, "TQ1_0": 1.69, "IQ1_S": 1.56,
}

// ParseQuant returns the quantization named in a GGUF file path, such as
// "Q4_K_M", "IQ4_XS", "F16" or "UD-Q4_K_XL", in upper case, or "" when the
// name does not say. The file name is tried first, then its folder, since
// some repos keep each quantization in a folder of its own.
func ParseQuant(p string) string {
	base, dir := path.Base(p), path.Base(path.Dir(p))
	if q := quantIn(trimGGUFName(base)); q != "" {
		return q
	}
	if dir != "." && dir != "/" {
		return quantIn(dir)
	}
	return ""
}

func trimGGUFName(name string) string {
	if m := splitRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if len(name) > 5 && strings.EqualFold(name[len(name)-5:], ".gguf") {
		return name[:len(name)-5]
	}
	return name
}

func quantIn(s string) string {
	// The last label wins: model names come first and quant labels last.
	all := quantRe.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return ""
	}
	return normalizeQuant(all[len(all)-1][1])
}

func normalizeQuant(q string) string {
	q = strings.ToUpper(strings.TrimSpace(q))
	switch q {
	case "FP16":
		return "F16"
	case "FP32":
		return "F32"
	}
	return q
}

// QuantBits returns the approximate bits per weight of a quantization label
// as ParseQuant returns it, or 0 when unknown. Multiplying by a parameter
// count and dividing by 8 gives a rough file size.
func QuantBits(quant string) float64 {
	return quantBits[strings.TrimPrefix(normalizeQuant(quant), "UD-")]
}

// GGUFModel is one installable GGUF build: a single file, or the parts of a
// split file taken together.
type GGUFModel struct {
	// Name is the logical file name: the path of an unsplit file, or the
	// path of a split one without its "-00001-of-0000N" part.
	Name  string
	Quant string // as ParseQuant returns it; "" when unknown
	Files []File // the parts in order; one entry for an unsplit file
	Size  int64  // total bytes of the parts present
	Parts int    // how many parts the build should have
	// Complete is false when parts of a split build are missing from the
	// listing; such a build cannot be loaded.
	Complete bool
	// Projector marks a multimodal projector (an "mmproj" file). It goes
	// alongside a model for image input and is not a model by itself.
	Projector bool
}

// GroupGGUF picks the .gguf files out of a repo listing and groups the
// parts of split files, so each result is one thing the user can install.
// Results are ordered from smallest to largest.
func GroupGGUF(files []File) []GGUFModel {
	type group struct {
		model GGUFModel
		parts map[int]File
	}
	groups := map[string]*group{}
	var order []string
	add := func(key string, g *group) {
		if _, ok := groups[key]; !ok {
			groups[key] = g
			order = append(order, key)
		}
	}

	for _, f := range files {
		base := path.Base(f.Path)
		if !strings.HasSuffix(strings.ToLower(base), ".gguf") {
			continue
		}
		dir := path.Dir(f.Path)
		join := func(name string) string {
			if dir == "." {
				return name
			}
			return dir + "/" + name
		}
		m := splitRe.FindStringSubmatch(base)
		if m == nil {
			add(f.Path, &group{model: GGUFModel{Name: f.Path, Files: []File{f}, Parts: 1}})
			continue
		}
		part, _ := strconv.Atoi(m[2])
		total, _ := strconv.Atoi(m[3])
		name := join(m[1] + ".gguf")
		// The total is part of the key: a "-of-00002" and a "-of-00003" set
		// with the same name are different uploads and must not be mixed.
		key := name + "\x00" + m[3]
		add(key, &group{model: GGUFModel{Name: name, Parts: total}, parts: map[int]File{}})
		if part >= 1 && part <= total {
			groups[key].parts[part] = f
		}
	}

	models := make([]GGUFModel, 0, len(order))
	for _, key := range order {
		g := groups[key]
		m := g.model
		if g.parts != nil {
			for i := 1; i <= m.Parts; i++ {
				if f, ok := g.parts[i]; ok {
					m.Files = append(m.Files, f)
				}
			}
		}
		for _, f := range m.Files {
			m.Size += f.Size
		}
		m.Complete = m.Parts > 0 && len(m.Files) == m.Parts
		m.Quant = ParseQuant(m.Name)
		m.Projector = strings.Contains(strings.ToLower(path.Base(m.Name)), "mmproj")
		models = append(models, m)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Size != models[j].Size {
			return models[i].Size < models[j].Size
		}
		return models[i].Name < models[j].Name
	})
	return models
}

// Preference says what the user wants from Recommend. Both fields are
// optional.
type Preference struct {
	// Quant is the quantization wanted, such as "Q5_K_M". When the repo does
	// not have it, the closest smaller one is picked, then the closest larger.
	Quant string
	// MaxSize is the most bytes the build may take in total (for example the
	// memory the user can spare); 0 means no limit.
	MaxSize int64
}

// Recommend picks the build to offer first. Projector files and incomplete
// split builds are never picked.
//
//   - With a Quant, it picks that quantization if present (and within
//     MaxSize), else the nearest one below it, else the nearest above it.
//   - With only a MaxSize, it picks the best quality that fits, preferring
//     quantized builds up to Q8_0-class over F16/BF16/F32, which take twice
//     the memory for almost no gain when running locally.
//   - With neither, it aims for DefaultQuant.
//
// It reports false when nothing fits.
func Recommend(models []GGUFModel, pref Preference) (GGUFModel, bool) {
	var cands []GGUFModel
	for _, m := range models {
		if m.Projector || !m.Complete || (pref.MaxSize > 0 && m.Size > pref.MaxSize) {
			continue
		}
		cands = append(cands, m)
	}
	if len(cands) == 0 {
		return GGUFModel{}, false
	}
	// Keep the choice independent of the order the caller passed models in.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Size != cands[j].Size {
			return cands[i].Size < cands[j].Size
		}
		return cands[i].Name < cands[j].Name
	})

	want := normalizeQuant(pref.Quant)
	if want == "" && pref.MaxSize > 0 {
		best := 0
		for i := range cands {
			if betterFit(cands[i], cands[best]) {
				best = i
			}
		}
		return cands[best], true
	}
	if want == "" {
		want = DefaultQuant
	}
	for _, m := range cands {
		if m.Quant == want {
			return m, true
		}
	}

	wantBits := QuantBits(want)
	if wantBits == 0 {
		wantBits = QuantBits(DefaultQuant)
	}
	below, above := -1, -1
	for i, m := range cands {
		b := QuantBits(m.Quant)
		switch {
		case b == 0:
			continue
		case b <= wantBits:
			if below < 0 || b > QuantBits(cands[below].Quant) {
				below = i
			}
		default:
			if above < 0 || b < QuantBits(cands[above].Quant) {
				above = i
			}
		}
	}
	switch {
	case below >= 0:
		return cands[below], true
	case above >= 0:
		return cands[above], true
	}
	return cands[0], true // only unlabeled builds: offer the smallest
}

// betterFit orders builds for a size budget: quantized beats full precision
// beats unknown, then more bits wins, then the smaller file.
func betterFit(a, b GGUFModel) bool {
	tier := func(m GGUFModel) int {
		switch bits := QuantBits(m.Quant); {
		case bits == 0:
			return 0
		case bits >= 16:
			return 1
		}
		return 2
	}
	if ta, tb := tier(a), tier(b); ta != tb {
		return ta > tb
	}
	if ba, bb := QuantBits(a.Quant), QuantBits(b.Quant); ba != bb {
		return ba > bb
	}
	return a.Size < b.Size
}
