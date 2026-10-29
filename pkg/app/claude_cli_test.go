package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/streaming"
)

// fakeClaude installs a claude command that records its arguments, input
// and environment, and prints the stream-json lines it is given.
type fakeClaude struct {
	dir string
}

func newFakeClaude(t *testing.T) *fakeClaude {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
dir="` + dir + `"
n=$(cat "$dir/count" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$dir/count"
printf '%s\n' "$@" > "$dir/args.$n"
cat > "$dir/stdin.$n"
env > "$dir/env.$n"
if [ -f "$dir/out.$n" ]; then cat "$dir/out.$n"; else cat "$dir/out"; fi
`
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := claudeCommandFunc
	claudeCommandFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { claudeCommandFunc = old })
	claudeSessions.Lock()
	claudeSessions.ids = map[string]string{}
	claudeSessions.order = nil
	claudeSessions.Unlock()
	return &fakeClaude{dir: dir}
}

func (f *fakeClaude) output(t *testing.T, run int, events ...any) {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		data, _ := json.Marshal(e)
		b.Write(data)
		b.WriteByte('\n')
	}
	name := "out"
	if run > 0 {
		name = fmt.Sprintf("out.%d", run)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeClaude) file(t *testing.T, name string, run int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("%s.%d", name, run)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func streamEvent(event map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "session_id": "sess-1", "parent_tool_use_id": nil, "event": event}
}

func textDelta(text string) map[string]any {
	return streamEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
}

// aRun is a run where Claude says something, reads a file, then answers.
func aRun(answer string) []any {
	return []any{
		map[string]any{"type": "system", "subtype": "init", "session_id": "sess-1", "model": "claude-sonnet-5-5"},
		streamEvent(map[string]any{"type": "message_start"}),
		textDelta("Let me "),
		textDelta("look."),
		streamEvent(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use"}}),
		map[string]any{"type": "assistant", "session_id": "sess-1", "parent_tool_use_id": nil, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"file_path": "/p/a.go"}}}}},
		map[string]any{"type": "user", "session_id": "sess-1", "parent_tool_use_id": nil, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "package a"}}}},
		// A subagent's own steps stay inside its call.
		map[string]any{"type": "assistant", "session_id": "sess-1", "parent_tool_use_id": "toolu_9", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_x", "name": "Bash", "input": map[string]any{"command": "ls"}}}}},
		streamEvent(map[string]any{"type": "message_start"}),
		streamEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "It is Go."}}),
		textDelta(answer),
		map[string]any{"type": "result", "subtype": "success", "session_id": "sess-1", "is_error": false, "result": answer, "total_cost_usd": 0.01,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 90, "output_tokens": 5}},
	}
}

type collected struct {
	mu     sync.Mutex
	deltas []streaming.Delta
}

func (c *collected) ctx() context.Context {
	return streaming.WithSink(context.Background(), func(d streaming.Delta) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.deltas = append(c.deltas, d)
	})
}

func (c *collected) text(kind string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, d := range c.deltas {
		switch {
		case kind == "answer" && d.Step == nil && !d.Commentary && !d.Summary,
			kind == "commentary" && d.Commentary,
			kind == "thinking" && d.Summary:
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

func TestClaudeCLIStreamsStepsAndContinuesTheSession(t *testing.T) {
	fake := newFakeClaude(t)
	config := projectTestConfig(t, "read-write")
	fake.output(t, 0, aRun("It is a Go package.")...)
	var c collected
	model := ClaudeCLIModel{Config: config, Model: "sonnet", ReasoningEffort: "high"}
	messages := []lc.BaseMessage{lc.System("NullBot instructions"), lc.Human("What is in a.go?")}
	reply, err := model.Call(c.ctx(), messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lcContentText(reply.Content) != "It is a Go package." || reply.UsageMetadata == nil || reply.UsageMetadata.InputTokens != 100 || reply.UsageMetadata.OutputTokens != 5 {
		t.Fatalf("reply %+v usage %+v", reply, reply.UsageMetadata)
	}
	if got := c.text("commentary"); got != "Let me look." {
		t.Fatalf("commentary %q", got)
	}
	if got := c.text("thinking"); got != "It is Go." {
		t.Fatalf("thinking %q", got)
	}
	if got := c.text("answer"); got != "Let me look.It is a Go package." {
		t.Fatalf("streamed text %q", got)
	}
	var steps []string
	for _, d := range c.deltas {
		if d.Step != nil {
			steps = append(steps, fmt.Sprintf("%s:%s:%t:%s", d.Step.ID, d.Step.Name, d.Step.Done, d.Step.Output))
			if d.Step.Metadata != nil && d.Step.Metadata["source"] != "claude-cli" {
				t.Fatalf("metadata %v", d.Step.Metadata)
			}
		}
	}
	if strings.Join(steps, "|") != "toolu_1:Read:false:|toolu_1:Read:true:package a" {
		t.Fatalf("steps %v", steps)
	}
	args := fake.file(t, "args", 1)
	for _, want := range []string{"-p\n", "stream-json", "--include-partial-messages", "--strict-mcp-config", "--permission-mode\nacceptEdits", "--model\nsonnet", "--effort\nhigh", "--append-system-prompt-file"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args lack %q:\n%s", want, args)
		}
	}
	if !strings.Contains(args, "Edit,Write,NotebookEdit") || strings.Contains(args, "Bash") {
		t.Fatalf("read-write tools wrong:\n%s", args)
	}
	if stdin := fake.file(t, "stdin", 1); stdin != "What is in a.go?" {
		t.Fatalf("stdin %q", stdin)
	}

	// The next request continues the session with only what is new.
	fake.output(t, 2, aRun("Two functions.")...)
	next := append(append(messages, reply), lc.Human("How many functions?"))
	if _, err := model.Call(c.ctx(), next, nil); err != nil {
		t.Fatal(err)
	}
	if args := fake.file(t, "args", 2); !strings.Contains(args, "--resume\nsess-1") || strings.Contains(args, "--append-system-prompt-file") {
		t.Fatalf("second call args:\n%s", args)
	}
	if stdin := fake.file(t, "stdin", 2); stdin != "How many functions?" {
		t.Fatalf("second stdin %q", stdin)
	}
	// A conversation it has not seen starts a new session with all of it.
	other := []lc.BaseMessage{lc.Human("hello"), lc.AI("something else"), lc.Human("and now?")}
	fake.output(t, 3, aRun("ok")...)
	if _, err := model.Call(c.ctx(), other, nil); err != nil {
		t.Fatal(err)
	}
	if args := fake.file(t, "args", 3); strings.Contains(args, "--resume") {
		t.Fatalf("resumed an unknown conversation:\n%s", args)
	}
	if stdin := fake.file(t, "stdin", 3); !strings.Contains(stdin, "[Assistant]\nsomething else") || !strings.HasSuffix(stdin, "and now?") {
		t.Fatalf("transcript %q", stdin)
	}
}

func TestClaudeCLIPermissionsAndEnvironment(t *testing.T) {
	fake := newFakeClaude(t)
	fake.output(t, 0, aRun("done")...)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api-key-should-not-pass")
	t.Setenv("DEPLOY_TOKEN", "deploy-token-should-not-pass")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "claude-oauth-token-passes")
	t.Setenv("CLAUDECODE", "1")
	for i, tc := range []struct{ permission, mode string }{{"read-only", "dontAsk"}, {"full", "acceptEdits"}} {
		config := projectTestConfig(t, tc.permission)
		if _, err := (ClaudeCLIModel{Config: config}).Call(context.Background(), []lc.BaseMessage{lc.Human("go")}, nil); err != nil {
			t.Fatal(err)
		}
		args := fake.file(t, "args", i+1)
		if !strings.Contains(args, "--permission-mode\n"+tc.mode) || strings.Contains(args, "--model") {
			t.Fatalf("%s args:\n%s", tc.permission, args)
		}
		hasBash := strings.Contains(args, "Bash")
		hasEdit := strings.Contains(args, "Edit")
		if (tc.permission == "read-only") == (hasBash || hasEdit) {
			t.Fatalf("%s tools:\n%s", tc.permission, args)
		}
		env := fake.file(t, "env", i+1)
		if strings.Contains(env, "sk-ant-api-key") || strings.Contains(env, "deploy-token") || strings.Contains(env, "CLAUDECODE=") {
			t.Fatalf("credentials reached the CLI:\n%s", env)
		}
		if !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=claude-oauth-token-passes") {
			t.Fatal("the CLI's own token was removed")
		}
	}
}

func TestClaudeCLIRedactsSecretsSplitAcrossPieces(t *testing.T) {
	fake := newFakeClaude(t)
	config := projectTestConfig(t, "full")
	secret := "ghp_streamsecret0123456789"
	if _, err := SaveSecret(config, SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": secret}}); err != nil {
		t.Fatal(err)
	}
	answer := "token is " + secret + " ok"
	events := []any{map[string]any{"type": "system", "subtype": "init", "session_id": "s"}, streamEvent(map[string]any{"type": "message_start"})}
	for _, piece := range []string{"token is ghp_str", "eamsecret01", "23456789 ok"} {
		events = append(events, textDelta(piece))
	}
	events = append(events,
		map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "echo " + secret}}}}},
		map[string]any{"type": "user", "parent_tool_use_id": nil, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{map[string]any{"type": "text", "text": secret}}}}}},
		map[string]any{"type": "result", "subtype": "success", "result": answer})
	fake.output(t, 0, events...)
	var c collected
	reply, err := (ClaudeCLIModel{Config: config}).Call(c.ctx(), []lc.BaseMessage{lc.Human("show")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := lcContentText(reply.Content) + c.text("answer") + c.text("commentary")
	for _, d := range c.deltas {
		if d.Step != nil {
			all += fmt.Sprint(d.Step.Input) + d.Step.Output
		}
	}
	if strings.Contains(all, "streamsecret") || !strings.Contains(c.text("answer"), "[secret:gh]") {
		t.Fatalf("leaked: %q", all)
	}
}

func TestStreamRedactorAtEverySplit(t *testing.T) {
	config := projectTestConfig(t, "full")
	secrets := []string{"sk-proj-aaaaaaaaaaaa1111", "sk-proj-aaaabbbbbbbb2222", "xyzxyzxyzxyz-3333"}
	for i, v := range secrets {
		if _, err := SaveSecret(config, SecretInput{Name: fmt.Sprintf("s%d", i), Kind: SecretGeneric, Values: map[string]string{"value": v}, Public: map[string]string{"env": fmt.Sprintf("S%d", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	text := "a sk-proj-aaaa then " + secrets[0] + " and " + secrets[1] + secrets[2] + " end sk-pro"
	r := currentRedactor(config)
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 300; trial++ {
		s := newStreamRedactor(r)
		var out strings.Builder
		for rest := text; rest != ""; {
			n := 1 + rng.Intn(min(9, len(rest)))
			out.WriteString(s.write(rest[:n]))
			rest = rest[n:]
		}
		out.WriteString(s.flush())
		got := out.String()
		for _, v := range secrets {
			if strings.Contains(got, v[4:len(v)-2]) {
				t.Fatalf("trial %d leaked %q in %q", trial, v, got)
			}
		}
		if !strings.Contains(got, "a sk-proj-aaaa then [secret:s0] and [secret:s1][secret:s2] end") {
			t.Fatalf("trial %d mangled: %q", trial, got)
		}
	}
}

func TestClaudeCLIErrors(t *testing.T) {
	fake := newFakeClaude(t)
	config := projectTestConfig(t, "read-only")
	fake.output(t, 0, map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "Not logged in · Please run /login"})
	_, err := (ClaudeCLIModel{Config: config}).Call(context.Background(), []lc.BaseMessage{lc.Human("hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "setup-token") {
		t.Fatalf("login error: %v", err)
	}
	claudeCommandFunc = func() (string, error) { return "", errClaudeNotFound }
	if _, err := modelFromModelConfig(config, ModelConfig{Provider: ClaudeCLIProvider, Model: "opus"}); err == nil || !strings.Contains(err.Error(), "NULLBOT_CLAUDE_BIN") {
		t.Fatalf("missing CLI: %v", err)
	}
}

func TestClaudeCLIAccountStatus(t *testing.T) {
	claudeStatusCache.Lock()
	claudeStatusCache.at = claudeStatusCache.at.AddDate(-1, 0, 0)
	claudeStatusCache.Unlock()
	old, oldStatus := claudeCommandFunc, claudeAuthStatusFunc
	t.Cleanup(func() {
		claudeCommandFunc, claudeAuthStatusFunc = old, oldStatus
		claudeStatusCache.Lock()
		claudeStatusCache.at = claudeStatusCache.at.AddDate(-1, 0, 0)
		claudeStatusCache.Unlock()
	})
	claudeCommandFunc = func() (string, error) { return "/usr/local/bin/claude", nil }
	claudeAuthStatusFunc = func(context.Context, string) ([]byte, error) {
		return []byte(`{"loggedIn": true, "authMethod": "claude.ai", "email": "someone@example.com"}`), nil
	}
	account := claudeAccountInfo(context.Background())
	if account.Status != "signed in" || strings.Contains(account.Detail, "someone@") {
		t.Fatalf("account %+v", account)
	}
}

func TestClaudeCLIStepsShowInTheChat(t *testing.T) {
	fake := newFakeClaude(t)
	fake.output(t, 0, aRun("It is a Go package.")...)
	config := projectTestConfig(t, "read-write")
	config.Model = ModelConfig{Provider: ClaudeCLIProvider, Model: "sonnet"}
	a := New(config)
	reply := a.Submit(context.Background(), "What is in a.go?")
	if !strings.Contains(reply.Message, "It is a Go package.") {
		t.Fatalf("reply %q", reply.Message)
	}
	a.mu.Lock()
	activity := append([]ActivityRecord(nil), a.activity...)
	history := append([]Message(nil), a.history...)
	a.mu.Unlock()
	var tools []string
	for _, r := range activity {
		if r.Tool != "" {
			tools = append(tools, r.Status+":"+r.Tool+":"+r.Source)
		}
	}
	if strings.Join(tools, "|") != "tool start:Read:claude-cli|tool complete:Read:claude-cli" {
		t.Fatalf("tool activity %v", tools)
	}
	var roles []string
	for _, m := range history {
		roles = append(roles, m.Role+":"+m.Content)
	}
	joined := strings.Join(roles, "|")
	if !strings.Contains(joined, "commentary:Let me look.") || !strings.Contains(joined, "assistant:It is a Go package.") {
		t.Fatalf("history %v", roles)
	}
}
