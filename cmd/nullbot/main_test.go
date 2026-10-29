package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
