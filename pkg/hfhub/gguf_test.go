package hfhub

import (
	"testing"
)

func TestParseQuant(t *testing.T) {
	cases := map[string]string{
		"Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf":                "Q4_K_M",
		"llama-2-7b.Q5_K_S.gguf":                                "Q5_K_S",
		"qwen2.5-7b-instruct-q8_0-00001-of-00003.gguf":          "Q8_0",
		"Qwen3-30B-A3B-UD-Q4_K_XL.gguf":                         "UD-Q4_K_XL",
		"model-IQ4_XS.gguf":                                     "IQ4_XS",
		"model-IQ2_XXS.gguf":                                    "IQ2_XXS",
		"model_IQ4_NL.gguf":                                     "IQ4_NL",
		"ggml-model-f16.gguf":                                   "F16",
		"phi-3-mini-fp16.gguf":                                  "F16",
		"gemma-2b-it-BF16.gguf":                                 "BF16",
		"Model-F32.gguf":                                        "F32",
		"Mistral-7B-v0.3-Q4_0_4_4.gguf":                         "Q4_0_4_4",
		"Meta-Llama-3-8B-Q6_K.gguf":                             "Q6_K",
		"Meta-Llama-3-8B-Q3_K_XL.gguf":                          "Q3_K_XL",
		"Meta-Llama-3-8B-Q2_K_L.gguf":                           "Q2_K_L",
		"gpt-oss-20b-MXFP4.gguf":                                "MXFP4",
		"BitNet-TQ1_0.gguf":                                     "TQ1_0",
		"Q6_K/Model-00001-of-00002.gguf":                        "Q6_K",
		"Q4_K_M-imatrix/Model-Q8_0.gguf":                        "Q8_0",
		"mmproj-model-f16.gguf":                                 "F16",
		"model.gguf":                                            "",
		"phi-3-mini-q4.gguf":                                    "",
		"bf16-but-really-model.gguf":                            "BF16",
		"Qwen2.5-Coder-32B-Instruct-Q5_K_M-00002-of-00002.gguf": "Q5_K_M",
	}
	for name, want := range cases {
		if got := ParseQuant(name); got != want {
			t.Errorf("ParseQuant(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestQuantBitsOrder(t *testing.T) {
	order := []string{"IQ1_S", "IQ2_XXS", "Q2_K", "IQ3_XXS", "Q3_K_M", "IQ4_XS", "Q4_K_S", "Q4_K_M", "Q5_K_M", "Q6_K", "Q8_0", "F16", "F32"}
	for i := 1; i < len(order); i++ {
		if QuantBits(order[i-1]) >= QuantBits(order[i]) {
			t.Errorf("%s should be smaller than %s", order[i-1], order[i])
		}
	}
	if QuantBits("ud-q4_k_xl") == 0 || QuantBits("nonsense") != 0 {
		t.Error("UD prefix or unknown quant handled wrongly")
	}
}

func testListing() []File {
	return []File{
		{Path: "README.md", Size: 10},
		{Path: "big/model-Q8_0-00002-of-00002.gguf", Size: 400, LFS: true},
		{Path: "big/model-Q8_0-00001-of-00002.gguf", Size: 600, LFS: true},
		{Path: "model-Q4_K_M.gguf", Size: 500, LFS: true},
		{Path: "model-Q2_K-00001-of-00003.gguf", Size: 100, LFS: true},
		{Path: "model-Q2_K-00003-of-00003.gguf", Size: 100, LFS: true},
		{Path: "mmproj-model-f16.gguf", Size: 50, LFS: true},
		{Path: "model-F16.GGUF", Size: 1900, LFS: true},
	}
}

func TestGroupGGUF(t *testing.T) {
	models := GroupGGUF(testListing())
	names := []string{"mmproj-model-f16.gguf", "model-Q2_K.gguf", "model-Q4_K_M.gguf", "big/model-Q8_0.gguf", "model-F16.GGUF"}
	if len(models) != len(names) {
		t.Fatalf("got %d models: %+v", len(models), models)
	}
	for i, m := range models {
		if m.Name != names[i] {
			t.Errorf("model %d = %q, want %q", i, m.Name, names[i])
		}
	}

	proj, q2, q4, q8, f16 := models[0], models[1], models[2], models[3], models[4]
	if !proj.Projector || proj.Quant != "F16" || !proj.Complete {
		t.Errorf("projector: %+v", proj)
	}
	if q2.Complete || q2.Parts != 3 || len(q2.Files) != 2 || q2.Size != 200 {
		t.Errorf("incomplete split: %+v", q2)
	}
	if !q4.Complete || q4.Parts != 1 || q4.Quant != "Q4_K_M" || q4.Projector {
		t.Errorf("single file: %+v", q4)
	}
	if !q8.Complete || q8.Parts != 2 || q8.Size != 1000 || q8.Quant != "Q8_0" ||
		q8.Files[0].Path != "big/model-Q8_0-00001-of-00002.gguf" || q8.Files[1].Path != "big/model-Q8_0-00002-of-00002.gguf" {
		t.Errorf("split: %+v", q8)
	}
	if f16.Quant != "F16" {
		t.Errorf("upper-case extension: %+v", f16)
	}
}

func TestGroupGGUFKeepsDifferentSplitSetsApart(t *testing.T) {
	models := GroupGGUF([]File{
		{Path: "m-Q4_K_M-00001-of-00002.gguf", Size: 1},
		{Path: "m-Q4_K_M-00002-of-00002.gguf", Size: 1},
		{Path: "m-Q4_K_M-00001-of-00003.gguf", Size: 1},
	})
	if len(models) != 2 {
		t.Fatalf("got %+v", models)
	}
	complete := 0
	for _, m := range models {
		if m.Complete {
			complete++
		}
	}
	if complete != 1 {
		t.Errorf("expected exactly one complete set: %+v", models)
	}
}

func TestRecommend(t *testing.T) {
	models := GroupGGUF(testListing())
	cases := []struct {
		name string
		pref Preference
		want string // "" means nothing fits
	}{
		{"default aims for Q4_K_M", Preference{}, "model-Q4_K_M.gguf"},
		{"exact quant", Preference{Quant: "q8_0"}, "big/model-Q8_0.gguf"},
		{"missing quant takes the nearest below", Preference{Quant: "Q5_K_M"}, "model-Q4_K_M.gguf"},
		{"nothing below takes the nearest above (incomplete Q2_K skipped)", Preference{Quant: "IQ1_S"}, "model-Q4_K_M.gguf"},
		{"budget picks the best that fits", Preference{MaxSize: 999}, "model-Q4_K_M.gguf"},
		{"budget prefers Q8_0 over F16", Preference{MaxSize: 5000}, "big/model-Q8_0.gguf"},
		{"quant within budget", Preference{Quant: "F16", MaxSize: 1000}, "big/model-Q8_0.gguf"},
		{"nothing fits", Preference{MaxSize: 60}, ""},
	}
	for _, tc := range cases {
		got, ok := Recommend(models, tc.pref)
		if tc.want == "" {
			if ok {
				t.Errorf("%s: got %q, want nothing", tc.name, got.Name)
			}
			continue
		}
		if !ok || got.Name != tc.want {
			t.Errorf("%s: got %q (%v), want %q", tc.name, got.Name, ok, tc.want)
		}
	}

	onlyF16 := GroupGGUF([]File{{Path: "m-F16.gguf", Size: 10}, {Path: "mmproj-Q8_0.gguf", Size: 1}})
	if got, ok := Recommend(onlyF16, Preference{MaxSize: 100}); !ok || got.Name != "m-F16.gguf" {
		t.Errorf("full precision as a last resort: %+v %v", got, ok)
	}
	unlabeled := GroupGGUF([]File{{Path: "b.gguf", Size: 20}, {Path: "a.gguf", Size: 10}})
	if got, ok := Recommend(unlabeled, Preference{}); !ok || got.Name != "a.gguf" {
		t.Errorf("unlabeled builds: %+v %v", got, ok)
	}
}
