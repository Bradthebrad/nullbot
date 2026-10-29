package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Bradthebrad/nullbot/pkg/hfhub"
)

// fakeHF is a small Hugging Face: one GGUF repo with two builds, one split.
func fakeHF(t *testing.T) (*httptest.Server, map[string][]byte, *[]string) {
	t.Helper()
	files := map[string][]byte{
		"qwen2.5-0.5b-instruct-q4_k_m.gguf":              []byte(strings.Repeat("q4", 512)),
		"qwen2.5-0.5b-instruct-q8_0-00001-of-00002.gguf": []byte(strings.Repeat("a", 700)),
		"qwen2.5-0.5b-instruct-q8_0-00002-of-00002.gguf": []byte(strings.Repeat("b", 300)),
		"README.md": []byte("readme"),
	}
	var mu sync.Mutex
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/models":
			if r.URL.Query().Get("filter") != "gguf" {
				http.Error(w, "want gguf", 400)
				return
			}
			fmt.Fprint(w, `[{"id":"Qwen/Qwen2.5-0.5B-Instruct-GGUF","author":"Qwen","downloads":1200,"likes":30,"pipeline_tag":"text-generation","tags":["gguf","license:apache-2.0"],"gated":false,"lastModified":"2026-09-01T00:00:00.000Z"}]`)
		case strings.HasPrefix(r.URL.Path, "/api/models/Qwen/Qwen2.5-0.5B-Instruct-GGUF/tree/"):
			var entries []map[string]any
			for name, content := range files {
				sum := sha256.Sum256(content)
				entry := map[string]any{"type": "file", "path": name, "size": len(content), "oid": "0000000000000000000000000000000000000000"}
				if strings.HasSuffix(name, ".gguf") {
					entry["lfs"] = map[string]any{"oid": hex.EncodeToString(sum[:]), "size": len(content)}
				}
				entries = append(entries, entry)
			}
			_ = json.NewEncoder(w).Encode(entries)
		case strings.HasPrefix(r.URL.Path, "/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/"):
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			content, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			sum := sha256.Sum256(content)
			w.Header().Set("X-Linked-Etag", `"`+hex.EncodeToString(sum[:])+`"`)
			w.Header().Set("X-Linked-Size", fmt.Sprint(len(content)))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(200)
			if r.Method != http.MethodHead {
				_, _ = w.Write(content)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := hfClientFunc
	hfClientFunc = func(config Config) *hfhub.Client {
		c := old(config)
		c.BaseURL = srv.URL
		return c
	}
	t.Cleanup(func() { hfClientFunc = old })
	return srv, files, &auth
}

func TestSearchAndInstallFromHuggingFace(t *testing.T) {
	_, files, auth := fakeHF(t)
	config := projectTestConfig(t, "read-only")
	a := New(config)
	if _, err := SaveSecret(config, SecretInput{Name: "huggingface", Kind: SecretAPIKey, Public: map[string]string{"provider": "huggingface"}, Values: map[string]string{"value": "hf_testtoken000000"}}); err != nil {
		t.Fatal(err)
	}
	found, err := a.SearchHFModels(context.Background(), "qwen", "", 10)
	if err != nil || len(found) != 1 || found[0].ID != "Qwen/Qwen2.5-0.5B-Instruct-GGUF" || found[0].NeedsToken {
		t.Fatalf("search %+v %v", found, err)
	}
	if len((*auth)) == 0 || (*auth)[0] != "Bearer hf_testtoken000000" {
		t.Fatalf("token not sent: %v", *auth)
	}
	builds, err := a.HFBuilds(context.Background(), "Qwen/Qwen2.5-0.5B-Instruct-GGUF")
	if err != nil || len(builds) != 2 {
		t.Fatalf("builds %+v %v", builds, err)
	}
	var recommended, split HFBuild
	for _, b := range builds {
		if b.Recommended {
			recommended = b
		}
		if b.Parts == 2 {
			split = b
		}
	}
	if recommended.Quant != "Q4_K_M" || split.Quant != "Q8_0" || !split.Complete || split.Size != 1000 {
		t.Fatalf("recommended %+v split %+v", recommended, split)
	}
	// Into NullBot's models folder, both parts of the split build, verified.
	var steps int
	res, err := a.InstallModel(context.Background(), InstallRequest{Repo: "Qwen/Qwen2.5-0.5B-Instruct-GGUF", Build: split.Name, Target: InstallFiles}, func(InstallProgress) { steps++ })
	if err != nil || len(res.Paths) != 2 || !strings.Contains(res.Next, "llama-server") {
		t.Fatalf("install %+v %v", res, err)
	}
	for _, p := range res.Paths {
		data, err := os.ReadFile(p)
		if err != nil || !strings.HasPrefix(p, ModelsDir(config)) {
			t.Fatalf("%s: %v", p, err)
		}
		name := p[strings.LastIndex(p, string(os.PathSeparator))+1:]
		if string(data) != string(files[name]) {
			t.Fatalf("%s content differs", name)
		}
	}
	if steps == 0 {
		t.Fatal("no progress")
	}
	if _, err := a.InstallModel(context.Background(), InstallRequest{Repo: "../etc", Target: InstallFiles}, nil); err == nil {
		t.Fatal("a bad repo id was accepted")
	}
	if _, err := a.InstallModel(context.Background(), InstallRequest{Repo: "Qwen/Qwen2.5-0.5B-Instruct-GGUF", Target: InstallOllama}, nil); err == nil || !strings.Contains(err.Error(), "Ollama") {
		t.Fatalf("no Ollama server: %v", err)
	}
}

func TestInstallIntoOllamaPullsTheHuggingFaceTag(t *testing.T) {
	fakeHF(t)
	var pulled []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pull" {
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			pulled = append(pulled, body.Model)
			fmt.Fprintln(w, `{"status":"success"}`)
			return
		}
		fmt.Fprint(w, `{"version":"0.12.0","models":[]}`)
	}))
	defer srv.Close()
	a := New(projectTestConfig(t, "read-only"))
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{Kind: "ollama", BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	res, err := a.InstallModel(context.Background(), InstallRequest{Repo: "Qwen/Qwen2.5-0.5B-Instruct-GGUF", Target: InstallOllama}, nil)
	if err != nil || res.Model != "hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF:Q4_K_M" || len(pulled) != 1 || pulled[0] != res.Model {
		t.Fatalf("%+v %v %v", res, err, pulled)
	}
}

func TestInstallRefusesWhatItCannotDoAsAsked(t *testing.T) {
	fakeHF(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"0.12.0","models":[]}`)
	}))
	defer srv.Close()
	a := New(projectTestConfig(t, "read-only"))
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{Kind: "ollama", BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	repo := "Qwen/Qwen2.5-0.5B-Instruct-GGUF"
	// An explicit quantization that is not there is not swapped for another.
	if _, err := a.InstallModel(context.Background(), InstallRequest{Repo: repo, Quant: "Q6_K", Target: InstallFiles}, nil); err == nil || !strings.Contains(err.Error(), "no Q6_K build") || !strings.Contains(err.Error(), "Q4_K_M") {
		t.Fatalf("missing quant: %v", err)
	}
	// Ollama cannot pull a build split into parts.
	if _, err := a.InstallModel(context.Background(), InstallRequest{Repo: repo, Quant: "Q8_0", Target: InstallOllama}, nil); err == nil || !strings.Contains(err.Error(), "split into 2 files") {
		t.Fatalf("split build into Ollama: %v", err)
	}
}
