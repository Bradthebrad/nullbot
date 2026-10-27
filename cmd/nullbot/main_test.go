package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
	if code, _, _ := runCLI(t, "", "--app-dir", dir, "-p", "   "); code != 2 {
		t.Fatalf("empty request: %d", code)
	}
}
