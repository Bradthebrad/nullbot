package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsLocalCommands(t *testing.T) {
	t.Setenv("NULLBOT_VAULT_PASSPHRASE", "test passphrase for the cli")
	t.Setenv("NULLBOT_APP_DIR", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"0.12.0"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"qwen2.5:7b"}]}`)
		case "/api/ps":
			fmt.Fprint(w, `{"models":[]}`)
		case "/api/show":
			fmt.Fprint(w, `{"model_info":{"qwen2.context_length":32768}}`)
		case "/api/pull":
			fmt.Fprintln(w, `{"status":"pulling manifest"}`)
			fmt.Fprintln(w, `{"status":"success"}`)
		case "/api/create":
			fmt.Fprintln(w, `{"status":"success"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if code, out, errOut := runCLI(t, "", "models", "local", "add", "ollama", "--url", srv.URL, "--app-dir", dir); code != 0 || !strings.Contains(out, "1 models") {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "--app-dir", dir, "models", "local"); code != 0 || !strings.Contains(out, "qwen2.5:7b  4096-token context") || !strings.Contains(out, "OLLAMA_CONTEXT_LENGTH") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, out, errOut := runCLI(t, "", "models", "pull", "qwen2.5:7b", "--app-dir", dir); code != 0 || !strings.Contains(out, "Pulled qwen2.5:7b into ollama") {
		t.Fatalf("pull: %d %q %q", code, out, errOut)
	}
	if code, out, errOut := runCLI(t, "", "models", "context", "qwen2.5:7b", "--size", "32768", "--app-dir", dir); code != 0 || !strings.Contains(out, "qwen2.5-7b-32k:latest") {
		t.Fatalf("context: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "models", "--app-dir", dir, "--refresh"); code != 0 || !strings.Contains(out, "Ollama (local:ollama)") {
		t.Fatalf("models: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "", "models", "local", "add", "nonsense", "--app-dir", dir); code != 1 || !strings.Contains(errOut, "kind") {
		t.Fatalf("bad kind: %d %q", code, errOut)
	}
	if code, out, _ := runCLI(t, "", "models", "local", "remove", "ollama", "--app-dir", dir); code != 0 || !strings.Contains(out, "Removed ollama") {
		t.Fatalf("remove: %d %q", code, out)
	}
}
