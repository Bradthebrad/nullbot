package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestHeadlessCommands(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "NULLBOT_APP_DIR"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	if code, out, _ := runCLI(t, "", "version"); code != 0 || !strings.HasPrefix(out, "nullbot ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, "", "--app-dir", dir, "help"); code != 0 || !strings.Contains(out, "nullbot serve") {
		t.Fatalf("help: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "", "launch"); code != 2 || !strings.Contains(errOut, `unknown command "launch"`) {
		t.Fatalf("unknown command: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "", "--app-dir", dir, "--model", "gpt", "-p", "x"); code != 1 || !strings.Contains(errOut, "provider/model") {
		t.Fatalf("bad model: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "", "--app-dir", dir, "--project", "nowhere", "-p", "x"); code != 1 || !strings.Contains(errOut, `no project "nowhere"`) {
		t.Fatalf("unknown project: %d %q", code, errOut)
	}
	// A slash command runs without a model.
	code, out, _ := runCLI(t, "", "--app-dir", dir, "--json", "-p", "/skills")
	var reply map[string]any
	if err := json.Unmarshal([]byte(out), &reply); err != nil || code != 0 || reply["status"] != "done" || reply["answer"] == "" {
		t.Fatalf("json reply: %d %q %v", code, out, err)
	}
	// The request can come from standard input.
	if code, out, _ := runCLI(t, "/skills\n", "--app-dir", dir, "-p", "-"); code != 0 || !strings.Contains(out, "Skills") {
		t.Fatalf("stdin request: %d %q", code, out)
	}
	// A request that fails exits 1 with the reason (no model key here).
	if code, out, _ := runCLI(t, "", "--app-dir", dir, "--model", "openai/gpt-5.1", "say hi"); code != 1 || out == "" {
		t.Fatalf("failing request: %d %q", code, out)
	}
	for _, empty := range []string{"   ", ""} {
		// An empty -p is an empty request, not a request to open the terminal UI.
		if code, _, _ := runCLI(t, "", "--app-dir", dir, "-p", empty); code != 2 {
			t.Fatalf("empty request %q: %d", empty, code)
		}
	}
}

func TestSecretsCommands(t *testing.T) {
	// A passphrase from the environment, as on a server; it also keeps the
	// test away from any real keychain.
	t.Setenv("NULLBOT_VAULT_PASSPHRASE", "test passphrase for the cli")
	t.Setenv("NULLBOT_APP_DIR", "")
	dir := t.TempDir()
	token := "ghp_cliTestToken0000000000"
	if code, out, errOut := runCLI(t, token+"\n", "secrets", "set", "github", "--kind", "github_token", "--agent", "--app-dir", dir); code != 0 || !strings.Contains(out, "Provides GH_TOKEN") {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	code, out, _ := runCLI(t, "", "--app-dir", dir, "secrets")
	if code != 0 || !strings.Contains(out, "github") || !strings.Contains(out, "agents may use") || strings.Contains(out, token) {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, "", "--app-dir="+dir, "vault"); code != 0 || !strings.Contains(out, "protected by a passphrase") {
		t.Fatalf("vault: %d %q", code, out)
	}
	aws := `{"access_key_id":"AKIACLITEST000000000","secret_access_key":"cli/secret/access/key/000000000000000"}`
	if code, out, errOut := runCLI(t, aws, "secrets", "set", "aws-prod", "--kind", "aws", "--set", "region=eu-west-1", "--app-dir", dir); code != 0 || !strings.Contains(out, "AWS_REGION") {
		t.Fatalf("aws: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "secrets", "ssh-keygen", "deploy", "--host", "github.com", "--app-dir", dir); code != 0 || !strings.Contains(out, "ssh-ed25519 ") {
		t.Fatalf("ssh-keygen: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "", "secrets", "set", "x", "--app-dir", dir); code != 1 || !strings.Contains(errOut, "--kind") {
		t.Fatalf("no kind: %d %q", code, errOut)
	}
	// Rotating a token keeps who may use it.
	if code, _, errOut := runCLI(t, "ghp_rotated0000000000000\n", "--app-dir", dir, "secrets", "set", "github", "--kind", "github_token"); code != 0 {
		t.Fatalf("rotate: %d %q", code, errOut)
	}
	if _, out, _ := runCLI(t, "", "secrets", "--app-dir", dir, "list"); !strings.Contains(out, "agents may use") {
		t.Fatalf("rotating dropped agent use: %q", out)
	}
	if code, _, errOut := runCLI(t, "", "secrets", "set", "x", "--kind", "github_token", "--set", "token=ghp_inargv", "--app-dir", dir); code != 1 || !strings.Contains(errOut, "secret value") {
		t.Fatalf("--set of a secret field: %d %q", code, errOut)
	}
	if code, out, _ := runCLI(t, "", "secrets", "remove", "github", "--app-dir", dir); code != 0 || !strings.Contains(out, "Removed github") {
		t.Fatalf("remove: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, "", "secrets", "kinds"); code != 0 || !strings.Contains(out, "github_token") {
		t.Fatalf("kinds: %d %q", code, out)
	}
	// Without the passphrase the vault stays shut.
	t.Setenv("NULLBOT_VAULT_PASSPHRASE", "")
	app.LockVault(app.Config{AppDir: dir})
	if code, _, errOut := runCLI(t, "", "--app-dir", dir, "secrets"); code != 1 || !strings.Contains(errOut, "locked") {
		t.Fatalf("locked: %d %q", code, errOut)
	}
}

// fakeChatServer is an OpenAI-compatible server that answers every chat
// with "pong" after delay, and records each request's messages.
func fakeChatServer(t *testing.T, delay time.Duration) (*httptest.Server, *[][]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m","max_model_len":131072}]}`)
		case "/v1/chat/completions":
			var body struct {
				Messages []map[string]any `json:"messages"`
				Stream   bool             `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			seen = append(seen, body.Messages)
			mu.Unlock()
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestHeadlessRunOptions(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "NULLBOT_APP_DIR"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	srv, seen := fakeChatServer(t, 0)
	if code, _, errOut := runCLI(t, "", "models", "local", "add", "openai", "--id", "fake", "--url", srv.URL, "--app-dir", dir); code != 0 {
		t.Fatalf("add server: %q", errOut)
	}
	model := "local:fake/m"

	// --output: the reply goes to a file only you can read; --json says
	// which session it was saved to.
	out := filepath.Join(t.TempDir(), "reply.json")
	code, stdout, errOut := runCLI(t, "", "--app-dir", dir, "--model", model, "--json", "--output", out, "-p", "first question")
	if code != 0 || stdout != "" {
		t.Fatalf("output: %d %q %q", code, stdout, errOut)
	}
	data, _ := os.ReadFile(out)
	var reply map[string]any
	if err := json.Unmarshal(data, &reply); err != nil || reply["answer"] != "pong" || reply["session"] == "" {
		t.Fatalf("reply file %q %v", data, err)
	}
	if info, _ := os.Stat(out); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("reply file mode %v", info.Mode())
	}

	// --continue: the next request carries on the conversation.
	if code, _, errOut := runCLI(t, "", "--app-dir", dir, "--model", model, "--continue", "-p", "second question"); code != 0 {
		t.Fatalf("continue: %d %q", code, errOut)
	}
	last := (*seen)[len(*seen)-1]
	text, _ := json.Marshal(last)
	if !strings.Contains(string(text), "first question") || !strings.Contains(string(text), "second question") {
		t.Fatalf("the conversation was not continued: %s", text)
	}
	// --session: that conversation; a fresh run does not see it.
	if code, _, _ := runCLI(t, "", "--app-dir", dir, "--model", model, "-p", "unrelated"); code != 0 {
		t.Fatal("fresh run")
	}
	if text, _ := json.Marshal((*seen)[len(*seen)-1]); strings.Contains(string(text), "first question") {
		t.Fatalf("a fresh run saw an old conversation: %s", text)
	}
	if code, _, errOut := runCLI(t, "", "--app-dir", dir, "--model", model, "--session", reply["session"].(string), "-p", "third"); code != 0 {
		t.Fatalf("session: %q", errOut)
	}
	if text, _ := json.Marshal((*seen)[len(*seen)-1]); !strings.Contains(string(text), "first question") {
		t.Fatalf("--session did not continue: %s", text)
	}

	// --timeout stops a slow request with exit status 124.
	slow, _ := fakeChatServer(t, 5*time.Second)
	if code, _, errOut := runCLI(t, "", "models", "local", "add", "openai", "--id", "slow", "--url", slow.URL, "--app-dir", dir); code != 0 {
		t.Fatalf("add slow server: %q", errOut)
	}
	start := time.Now()
	code, _, errOut = runCLI(t, "", "--app-dir", dir, "--model", "local:slow/m", "--timeout", "300ms", "-p", "take your time")
	if code != 124 || !strings.Contains(errOut, "stopped after 300ms") || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %d %q after %s", code, errOut, time.Since(start))
	}

	// Usage errors stop before anything runs.
	for _, args := range [][]string{
		{"--effort", "ludicrous", "-p", "x"},
		{"--max-iterations", "-1", "-p", "x"},
		{"--continue", "--session", "20261028-101500", "-p", "x"},
		{"--output", out, "serve"},
		{"--effort", "high"},
	} {
		if code, _, errOut := runCLI(t, "", append([]string{"--app-dir", dir}, args...)...); code != 2 {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

func TestRunOptionsChangeTheRunOnly(t *testing.T) {
	t.Setenv("NULLBOT_APP_DIR", "")
	dir := t.TempDir()
	config, err := loadConfig(options{appDir: dir, model: "openai/gpt-5.1", subagentModel: "anthropic/claude-haiku-5-5", effort: "big-brain", maxIterations: 40, maxSubagents: 6})
	if err != nil {
		t.Fatal(err)
	}
	if config.SubagentModel.Provider != "anthropic" || config.SubagentModel.Model != "claude-haiku-5-5" || config.Model.ReasoningEffort != "xhigh" || config.SubagentModel.ReasoningEffort != "xhigh" || config.Agent.MaxIterations != 40 || config.Agent.MaxSubagents != 6 {
		t.Fatalf("overrides %+v %+v %+v", config.Model, config.SubagentModel, config.Agent)
	}
	saved, _ := loadConfig(options{appDir: dir})
	if saved.Agent.MaxIterations == 40 || saved.SubagentModel.Model == "claude-haiku-5-5" {
		t.Fatal("the overrides were saved")
	}
	if _, err := loadConfig(options{appDir: dir, subagentModel: "haiku"}); err == nil {
		t.Fatal("a model without a provider was accepted")
	}
}
