package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/mcp"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

func TestMain(m *testing.M) {
	if os.Getenv("NULLBOT_TEST_MCP_SERVER") == "1" {
		runTestMCPServer()
		return
	}
	// Tests never touch the real keychain.
	vaultOptions = vault.Options{Keyring: testKeyring, Passphrase: func() (string, bool) { return "", false }, Iterations: 1000}
	os.Exit(m.Run())
}

// runTestMCPServer is a small stdio MCP server used by pool tests.
func runTestMCPServer() {
	server := mcp.NewServer("pool-test")
	server.AddTool(mcp.Tool{Name: "echo_text", Title: "Echo text", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true)}, Handler: func(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
		return mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: fmt.Sprint(args["text"])}}, StructuredContent: map[string]any{"items": []any{1, 2, 3}}}, nil
	}})
	server.AddTool(mcp.Tool{Name: "write_note", Handler: func(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
		path := fmt.Sprint(args["path"])
		if err := os.WriteFile(path, []byte("note"), 0600); err != nil {
			return mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		return mcp.Text("wrote " + path), nil
	}})
	server.AddTool(mcp.Tool{Name: "whoami", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true)}, Handler: func(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
		cwd, _ := os.Getwd()
		data, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "cwd": cwd, "primary": os.Getenv("NULLBOT_PRIMARY_PROJECT"), "projects": os.Getenv("NULLBOT_PROJECTS")})
		return mcp.Text(string(data)), nil
	}})
	server.AddTool(mcp.Tool{Name: "crash_now", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true)}, Handler: func(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
		os.Exit(3)
		return mcp.ToolResult{}, nil
	}})
	_ = server.RunStdio(context.Background(), os.Stdin, os.Stdout)
	os.Exit(0)
}

func poolTestApp(t *testing.T, permission string) *App {
	t.Helper()
	c := projectTestConfig(t, permission)
	c.EnabledMCPServers = map[string]MCPEntry{"pool": {Name: "Pool test", Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: map[string]string{"NULLBOT_TEST_MCP_SERVER": "1"}, Transport: "stdio", Enabled: true}}
	a := New(c)
	t.Cleanup(a.Shutdown)
	return a
}

func toolByName(tools []tcagent.Tool, name string) tcagent.Tool {
	for _, tool := range tools {
		if tool.Definition().Name == name {
			return tool
		}
	}
	return nil
}

func callJSON(t *testing.T, tool tcagent.Tool, args map[string]any) map[string]any {
	t.Helper()
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return data
}

func TestMCPPoolReusesOneProcessAcrossTurns(t *testing.T) {
	a := poolTestApp(t, "read-only")
	ctx := context.Background()
	first, _ := a.loadMCPTools(ctx, a.Config())
	if len(first) != 4 {
		t.Fatalf("tools = %d", len(first))
	}
	who := callJSON(t, toolByName(first, "whoami"), nil)
	second, _ := a.loadMCPTools(ctx, a.Config())
	again := callJSON(t, toolByName(second, "whoami"), nil)
	if who["pid"] != again["pid"] {
		t.Fatalf("server restarted between turns: %v vs %v", who["pid"], again["pid"])
	}
	wantCwd, _ := filepath.EvalSymlinks(a.Config().WorkspaceDir)
	gotCwd, _ := filepath.EvalSymlinks(fmt.Sprint(who["cwd"]))
	if gotCwd != wantCwd {
		t.Fatalf("server cwd = %q, want primary project %q", gotCwd, wantCwd)
	}
	if who["primary"] != a.Config().WorkspaceDir || !strings.Contains(fmt.Sprint(who["projects"]), `"permission":"read-only"`) {
		t.Fatalf("project env not passed: %#v", who)
	}
	status := a.MCPStatus(true)
	if len(status) != 1 || status[0].State != MCPStateReady || status[0].ToolCount != 4 || status[0].Calls != 2 {
		t.Fatalf("status = %#v", status)
	}
	for _, tool := range status[0].Tools {
		if tool.Name == "write_note" && tool.Requirement.Capability != CapWrite {
			t.Fatalf("write_note requirement = %#v", tool.Requirement)
		}
	}
}

func TestMCPPoolEnforcesProjectPermissionsPerCall(t *testing.T) {
	a := poolTestApp(t, "read-only")
	tools, _ := a.loadMCPTools(context.Background(), a.Config())
	echo := toolByName(tools, "echo_text").(tcagent.MetaTool)
	out, meta, err := echo.CallWithMeta(context.Background(), map[string]any{"text": "hi"})
	if err != nil || out != "hi" {
		t.Fatalf("echo = %q %v", out, err)
	}
	if meta["items"] != 3 {
		t.Fatalf("result meta not forwarded: %#v", meta)
	}
	target := filepath.Join(a.Config().WorkspaceDir, "note.txt")
	if _, err := toolByName(tools, "write_note").Call(context.Background(), map[string]any{"path": target}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write in read-only project: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("denied call reached the server")
	}
	if err := a.UpdateConfig(func(c *Config) {
		projects := append([]Project(nil), c.Projects...)
		projects[0].Permission = "read-write"
		c.Projects = projects
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolByName(tools, "write_note").Call(context.Background(), map[string]any{"path": target}); err != nil {
		t.Fatalf("write after granting read-write: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "note" {
		t.Fatalf("note not written: %q", data)
	}
}

func TestMCPPoolDisableStopsAndToolPolicyOverrides(t *testing.T) {
	a := poolTestApp(t, "full")
	tools, _ := a.loadMCPTools(context.Background(), a.Config())
	if err := a.SetMCPToolPolicy("pool", "echo_text", "blocked"); err != nil {
		t.Fatal(err)
	}
	if _, err := toolByName(tools, "echo_text").Call(context.Background(), map[string]any{"text": "x"}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked policy not applied: %v", err)
	}
	if err := a.UpdateConfig(func(c *Config) {
		servers := map[string]MCPEntry{}
		for k, v := range c.EnabledMCPServers {
			v.Enabled = false
			servers[k] = v
		}
		c.EnabledMCPServers = servers
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status := a.MCPStatus(false); len(status) == 1 && status[0].State == MCPStateDisabled {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server not disabled: %#v", a.MCPStatus(false))
}

func TestMCPPoolRestartsCrashedServer(t *testing.T) {
	a := poolTestApp(t, "full")
	tools, _ := a.loadMCPTools(context.Background(), a.Config())
	before := callJSON(t, toolByName(tools, "whoami"), nil)
	_, _ = toolByName(tools, "crash_now").Call(context.Background(), nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status := a.MCPStatus(false); status[0].State == MCPStateError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	after := callJSON(t, toolByName(tools, "whoami"), nil)
	if before["pid"] == after["pid"] {
		t.Fatal("expected a new process after the crash")
	}
	if status := a.MCPStatus(false); status[0].Restarts < 1 || status[0].State != MCPStateReady {
		t.Fatalf("status after restart = %#v", status[0])
	}
}
