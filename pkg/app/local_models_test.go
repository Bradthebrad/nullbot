package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOllama answers Ollama's API and its OpenAI-compatible chat, and
// records the chat requests.
type fakeOllama struct {
	*httptest.Server
	mu       sync.Mutex
	chats    []map[string]any
	auth     []string
	requireK string
}

func newFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		need := f.requireK
		f.mu.Unlock()
		if need != "" && r.Header.Get("Authorization") != "Bearer "+need {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"0.12.0"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"qwen2.5:7b","details":{"parameter_size":"7.6B","quantization_level":"Q4_K_M"}},{"name":"nomic-embed-text:latest"}]}`)
		case "/api/ps":
			fmt.Fprint(w, `{"models":[]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen2.5:7b"}]}`)
		case "/api/show":
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.HasPrefix(body.Model, "nomic") {
				fmt.Fprint(w, `{"capabilities":["embedding"]}`)
				return
			}
			fmt.Fprint(w, `{"parameters":"num_ctx 32768","model_info":{"qwen2.context_length":32768},"capabilities":["completion","tools"]}`)
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.chats = append(f.chats, body)
			f.mu.Unlock()
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello from qwen\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello from qwen"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestLocalServerRunsTheManager(t *testing.T) {
	srv := newFakeOllama(t)
	config := projectTestConfig(t, "read-only")
	a := New(config)
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{Kind: "ollama", BaseURL: srv.URL + "/v1/"}, ""); err != nil {
		t.Fatal(err)
	}
	endpoints := a.LocalEndpoints(context.Background(), true)
	if len(endpoints) != 1 || endpoints[0].ID != "ollama" || endpoints[0].BaseURL != srv.URL || !endpoints[0].Status.Reachable || len(endpoints[0].Models) != 2 {
		t.Fatalf("endpoints %+v", endpoints)
	}
	if window := ResolveContextWindow(a.Config(), ModelConfig{Provider: "local:ollama", Model: "qwen2.5:7b"}); window.Tokens != 32768 || window.Source != "provider" {
		t.Fatalf("window %+v", window)
	}
	groups := DiscoverModelGroups(context.Background(), a.Config())
	var local *ModelGroup
	for i := range groups {
		if groups[i].Provider == "local:ollama" {
			local = &groups[i]
		}
	}
	if local == nil || local.Title != "Ollama" || len(local.Models) != 1 || local.Models[0].ID != "qwen2.5:7b" || !strings.Contains(local.Models[0].Description, "tools") {
		t.Fatalf("local group %+v", local)
	}
	if err := a.UpdateConfig(func(c *Config) { c.Model = ModelConfig{Provider: "local:ollama", Model: "qwen2.5:7b"} }); err != nil {
		t.Fatal(err)
	}
	reply := a.Submit(context.Background(), "say hello")
	if !strings.Contains(reply.Message, "Hello from qwen") {
		t.Fatalf("reply %q", reply.Message)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.chats) == 0 || srv.chats[0]["model"] != "qwen2.5:7b" {
		t.Fatalf("chats %v", srv.chats)
	}
	if _, sent := srv.chats[0]["reasoning_effort"]; sent {
		t.Fatal("reasoning effort sent to a local server")
	}
	if err := a.RemoveLocalEndpoint("ollama"); err == nil || !strings.Contains(err.Error(), "manager") {
		t.Fatalf("removed a server in use: %v", err)
	}
}

func TestLocalServerKeyFromTheVaultAndErrors(t *testing.T) {
	srv := newFakeOllama(t)
	srv.requireK = "local-server-key-0001"
	config := projectTestConfig(t, "read-only")
	a := New(config)
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "Lab GPU", Kind: "openai", BaseURL: srv.URL}, ""); err == nil {
		t.Fatal("a bad id was accepted")
	}
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "lab", Kind: "openai", BaseURL: srv.URL, KeySecret: "lab-key"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := modelFromModelConfig(a.Config(), ModelConfig{Provider: "local:lab", Model: "qwen2.5:7b"}); err == nil || !strings.Contains(err.Error(), "lab-key") {
		t.Fatalf("missing secret: %v", err)
	}
	if _, err := SaveSecret(a.Config(), SecretInput{Name: "lab-key", Kind: SecretAPIKey, Public: map[string]string{"provider": "lab"}, Values: map[string]string{"value": "local-server-key-0001"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := modelFromModelConfig(a.Config(), ModelConfig{Provider: "local:lab", Model: "qwen2.5:7b"}); err != nil {
		t.Fatal(err)
	}
	info := a.LocalEndpoints(context.Background(), true)
	if !info[0].Status.Reachable || info[0].Insecure {
		t.Fatalf("info %+v", info[0])
	}
	// The key is bound to the server's name: renaming needs a key for the
	// new name, or one for any local server.
	_ = a.UpdateConfig(func(c *Config) {
		c.SubagentModel = ModelConfig{Provider: "local:lab", Model: "qwen2.5:7b"}
		c.Compaction.ContextWindows = map[string]int{"local:lab/qwen2.5:7b": 24576, "openai/gpt-5": 100000}
	})
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "gpu", Kind: "openai", BaseURL: srv.URL, KeySecret: "lab-key"}, "lab"); err == nil || !strings.Contains(err.Error(), `provider "local" or "gpu"`) {
		t.Fatalf("a key for lab went to gpu: %v", err)
	}
	if _, err := SaveSecret(a.Config(), SecretInput{Name: "lab-key", Kind: SecretAPIKey, Public: map[string]string{"provider": "local"}, Values: map[string]string{"value": "local-server-key-0001"}}); err != nil {
		t.Fatal(err)
	}
	// Renaming keeps the models chosen on it, and their context sizes.
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "gpu", Kind: "openai", BaseURL: srv.URL, KeySecret: "lab-key"}, "lab"); err != nil {
		t.Fatal(err)
	}
	if w := a.Config().Compaction.ContextWindows; w["local:gpu/qwen2.5:7b"] != 24576 || w["openai/gpt-5"] != 100000 || len(w) != 2 {
		t.Fatalf("windows after rename %v", w)
	}
	if c := a.Config(); c.SubagentModel.Provider != "local:gpu" || len(c.LocalEndpoints) != 1 {
		t.Fatalf("after rename %+v %+v", c.SubagentModel, c.LocalEndpoints)
	}
	srv.Close()
	localStatusCache.Delete(srv.URL)
	if _, err := modelFromModelConfig(a.Config(), ModelConfig{Provider: "local:gpu", Model: "qwen2.5:7b"}); err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Fatalf("down server: %v", err)
	}
	if _, err := modelFromModelConfig(a.Config(), ModelConfig{Provider: "local:nope", Model: "x"}); err == nil {
		t.Fatal("unknown server accepted")
	}
	if window := ResolveContextWindow(a.Config(), ModelConfig{Provider: "local:gpu", Model: "unlisted"}); window.Tokens != 8192 {
		t.Fatalf("unknown local window %+v", window)
	}
}

func TestLocalKeysStayWithTheirServer(t *testing.T) {
	config := projectTestConfig(t, "read-only")
	a := New(config)
	for _, in := range []SecretInput{
		{Name: "openai", Kind: SecretAPIKey, Public: map[string]string{"provider": "openai"}, Values: map[string]string{"value": "sk-openai-real-00001"}},
		{Name: "lan-key", Kind: SecretAPIKey, Public: map[string]string{"provider": "lan"}, Values: map[string]string{"value": "lan-server-key-00001"}},
		{Name: "pw", Kind: SecretGeneric, Public: map[string]string{"env": "PW"}, Values: map[string]string{"value": "generic-value-000001"}},
	} {
		if _, err := SaveSecret(a.Config(), in); err != nil {
			t.Fatal(err)
		}
	}
	// Another provider's key never goes to a server, even by hand-editing.
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "openai", Kind: "openai", BaseURL: "https://llm.example.test", KeySecret: "openai"}, ""); err == nil || !strings.Contains(err.Error(), "does not send") {
		t.Fatalf("the OpenAI key was bound to a server: %v", err)
	}
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "lan", Kind: "openai", BaseURL: "https://llm.example.test", KeySecret: "pw"}, ""); err == nil {
		t.Fatal("a generic secret was bound to a server")
	}
	_ = a.UpdateConfig(func(c *Config) {
		c.LocalEndpoints = []LocalEndpoint{{ID: "evil", Kind: "openai", BaseURL: "https://evil.example.test", KeySecret: "openai"}}
	})
	if _, err := localClient(a.Config(), a.Config().LocalEndpoints[0]); err == nil {
		t.Fatal("a hand-edited config sent the OpenAI key to a server")
	}
	// Plain http to another computer needs an explicit choice.
	_ = a.UpdateConfig(func(c *Config) { c.LocalEndpoints = nil })
	e := LocalEndpoint{ID: "lan", Kind: "vllm", BaseURL: "http://192.168.1.20:8000", KeySecret: "lan-key"}
	if _, err := a.SaveLocalEndpoint(e, ""); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("plain http with a key: %v", err)
	}
	e.PlainHTTP = true
	if _, err := a.SaveLocalEndpoint(e, ""); err != nil {
		t.Fatal(err)
	}
	if client, err := localClient(a.Config(), a.Config().LocalEndpoints[0]); err != nil || client.APIKey != "lan-server-key-00001" {
		t.Fatalf("allowed plain http: %v", err)
	}
	// Adding a second server under the same name is refused, not a
	// replacement.
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "lan", Kind: "ollama"}, ""); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("a server was replaced: %v", err)
	}
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "box", Kind: "ollama"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveLocalEndpoint(LocalEndpoint{ID: "lan", Kind: "ollama"}, "box"); err == nil {
		t.Fatal("renaming onto another server replaced it")
	}
	if n := len(a.Config().LocalEndpoints); n != 2 {
		t.Fatalf("%d servers", n)
	}
}

func TestOllamaNamesAndCopies(t *testing.T) {
	for in, want := range map[string]string{
		"llama3.2":                       "llama3.2:latest",
		"llama3.2:3b":                    "llama3.2:3b",
		"registry.local:5000/team/model": "registry.local:5000/team/model:latest",
		"hf.co/Qwen/Qwen2.5-GGUF:Q4_K_M": "hf.co/Qwen/Qwen2.5-GGUF:Q4_K_M",
	} {
		if got := ollamaFullName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		from string
		ctx  int
		want string
	}{
		{"qwen2.5:7b", 32768, "qwen2.5-7b-32k"},
		{"llama3.2", 32768, "llama3.2-32k"},
		{"llama3.2:latest", 33000, "llama3.2-33000"},
		{"registry.local:5000/team/model:q4", 65536, "registry.local:5000/team/model-q4-64k"},
	} {
		if got := localCopyName(c.from, c.ctx); got != c.want {
			t.Errorf("%q %d: %q, want %q", c.from, c.ctx, got, c.want)
		}
	}
}
