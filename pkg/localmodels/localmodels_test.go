package localmodels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"localhost:11434":               "http://localhost:11434",
		"http://localhost:8080/v1/":     "http://localhost:8080",
		"https://llm.corp.example/api/": "https://llm.corp.example/api",
		" http://10.0.0.5:8000/v1 ":     "http://10.0.0.5:8000",
	} {
		got, err := NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "http://user:pass@host:1", "http://"} {
		if _, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !IsLocalHost("http://127.0.0.1:1") || !IsLocalHost("http://localhost:1") || IsLocalHost("http://192.168.1.4:1") {
		t.Fatal("IsLocalHost")
	}
}

func ollamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"0.12.3"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3.1:8b","size":4900000000,"details":{"family":"llama","parameter_size":"8.0B","quantization_level":"Q4_K_M"}},{"name":"qwen-32k:latest","details":{"parameter_size":"7.6B"}},{"name":"nomic-embed-text:latest"}]}`)
		case "/api/ps":
			fmt.Fprint(w, `{"models":[{"name":"qwen-32k:latest","context_length":32768}]}`)
		case "/api/show":
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Model {
			case "llama3.1:8b":
				fmt.Fprint(w, `{"parameters":"stop \"<|eot_id|>\"","model_info":{"general.architecture":"llama","llama.context_length":131072},"capabilities":["completion","tools"]}`)
			case "qwen-32k:latest":
				fmt.Fprint(w, `{"parameters":"num_ctx 32768","model_info":{"qwen2.context_length":32768},"capabilities":["completion","tools"]}`)
			default:
				fmt.Fprint(w, `{"model_info":{"nomic-bert.context_length":2048},"capabilities":["embedding"]}`)
			}
		case "/api/pull":
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model == "missing:latest" {
				fmt.Fprintln(w, `{"status":"pulling manifest"}`)
				fmt.Fprintln(w, `{"error":"pull model manifest: file does not exist"}`)
				return
			}
			for _, line := range []string{`{"status":"pulling manifest"}`, `{"status":"pulling abc","digest":"sha256:abc","total":100,"completed":40}`, `{"status":"pulling abc","digest":"sha256:abc","total":100,"completed":100}`, `{"status":"success"}`} {
				fmt.Fprintln(w, line)
			}
		case "/api/create":
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), `"num_ctx":32768`) || !strings.Contains(string(data), `"from":"llama3.1:8b"`) {
				http.Error(w, `{"error":"bad create"}`, 400)
				return
			}
			fmt.Fprintln(w, `{"status":"success"}`)
		case "/api/delete":
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestOllamaModelsAndContext(t *testing.T) {
	srv := ollamaServer(t)
	defer srv.Close()
	e := Endpoint{Kind: Ollama, BaseURL: srv.URL}
	if s := e.Check(context.Background()); !s.Reachable || s.Version != "0.12.3" {
		t.Fatalf("status %+v", s)
	}
	models, err := e.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	llama := byID["llama3.1:8b"]
	if llama.ContextWindow != OllamaDefaultContext || llama.MaxContext != 131072 || llama.ContextNote == "" || llama.Quantization != "Q4_K_M" {
		t.Fatalf("llama %+v", llama)
	}
	if q := byID["qwen-32k:latest"]; q.ContextWindow != 32768 || !q.Loaded {
		t.Fatalf("qwen %+v", q)
	}
	if emb := byID["nomic-embed-text:latest"]; !emb.Embedding || emb.ContextWindow != 2048 {
		t.Fatalf("embedding %+v", emb)
	}
}

func TestOllamaPullCreateDelete(t *testing.T) {
	srv := ollamaServer(t)
	defer srv.Close()
	e := Endpoint{Kind: Ollama, BaseURL: srv.URL}
	var steps []Progress
	if err := e.Pull(context.Background(), "llama3.1:8b", func(p Progress) { steps = append(steps, p) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 || steps[2].Completed != 100 {
		t.Fatalf("steps %+v", steps)
	}
	if err := e.Pull(context.Background(), "missing:latest", nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing model: %v", err)
	}
	for _, bad := range []string{"../etc", "a b", "", "x//y"} {
		if err := e.Pull(context.Background(), bad, nil); err == nil {
			t.Errorf("%q pulled", bad)
		}
	}
	if err := e.CreateWithContext(context.Background(), "llama3.1:8b", "llama3.1-32k", 32768, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(context.Background(), "llama3.1-32k"); err != nil {
		t.Fatal(err)
	}
	if err := (Endpoint{Kind: LlamaCpp, BaseURL: srv.URL}).Pull(context.Background(), "x", nil); err == nil {
		t.Fatal("llama.cpp pulled")
	}
}

func TestLlamaCppLMStudioVLLM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, `{"error":{"message":"invalid key"}}`, 401)
			return
		}
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/props":
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":16384},"total_slots":1}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen2.5-7b-instruct-q4_k_m.gguf","meta":{"n_ctx_train":32768,"n_params":7615616512,"size":4683073504},"max_model_len":0}]}`)
		case "/api/v0/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen2.5-7b","type":"llm","arch":"qwen2","quantization":"Q4_K_M","state":"loaded","max_context_length":32768,"loaded_context_length":8192},{"id":"text-embedding-nomic","type":"embeddings","state":"not-loaded","max_context_length":2048}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	llama := Endpoint{Kind: LlamaCpp, BaseURL: srv.URL, APIKey: "k"}
	models, err := llama.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ContextWindow != 16384 || models[0].MaxContext != 32768 || models[0].Parameters != "7.6B" {
		t.Fatalf("llama.cpp %+v %v", models, err)
	}
	lms := Endpoint{Kind: LMStudio, BaseURL: srv.URL, APIKey: "k"}
	models, err = lms.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ContextWindow != 8192 || !models[1].Embedding {
		t.Fatalf("lm studio %+v %v", models, err)
	}
	if _, err := (Endpoint{Kind: VLLM, BaseURL: srv.URL}).Models(context.Background()); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("no key: %v", err)
	}
	if got := llama.OpenAIBaseURL(); got != srv.URL+"/v1" {
		t.Fatal(got)
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s := (Endpoint{Kind: Ollama, BaseURL: url}).Check(context.Background())
	if s.Reachable || !strings.Contains(s.Error, "not answering") {
		t.Fatalf("%+v", s)
	}
}
