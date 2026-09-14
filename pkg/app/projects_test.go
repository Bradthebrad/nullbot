package app

import (
	"context"
	"github.com/Bradthebrad/tinychain/agent"
	"os"
	"path/filepath"
	"testing"
)

func projectTestConfig(t *testing.T, permission string) Config {
	t.Helper()
	root := t.TempDir()
	c := DefaultConfig()
	c.AppDir = t.TempDir()
	c.WorkspaceDir = root
	c.Projects = []Project{{ID: "one", Name: "One", Path: root, Permission: permission}}
	c.PrimaryProjectID = "one"
	return c
}
func TestProjectPaths(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	if _, err := CheckProjectPath(c, "new.txt", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckProjectPath(c, "new.txt", true, false); err == nil {
		t.Fatal("read-only write permitted")
	}
	if _, err := CheckProjectPath(c, "../escape.txt", false, false); err == nil {
		t.Fatal("escape permitted")
	}
	c.Projects[0].Permission = "read-write"
	if _, err := CheckProjectPath(c, "new.txt", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckProjectPath(c, c.WorkspaceDir, true, true); err == nil {
		t.Fatal("root deletion permitted")
	}
	nested := filepath.Join(c.WorkspaceDir, "nested")
	os.Mkdir(nested, 0700)
	c.Projects = append(c.Projects, Project{ID: "two", Name: "Two", Path: nested, Permission: "read-only"})
	if _, err := CheckProjectPath(c, filepath.Join(nested, "x"), true, false); err == nil {
		t.Fatal("overlap granted write")
	}
}
func TestProjectSymlinkEscape(t *testing.T) {
	c := projectTestConfig(t, "read-write")
	link := filepath.Join(c.WorkspaceDir, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := CheckProjectPath(c, filepath.Join(link, "new.txt"), true, false); err == nil {
		t.Fatal("symlink escape permitted")
	}
}
func TestExternalDispatchRevocationAndUnknownTool(t *testing.T) {
	c := projectTestConfig(t, "full")
	a := New(c)
	called := 0
	tools := guardExternalTools(a, []agent.Tool{agent.ToolFunc{Name: "read_file", Func: func(context.Context, map[string]any) (string, error) { called++; return "ok", nil }}})
	if _, err := tools[0].Call(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	a.config.Projects[0].Permission = "read-only"
	if _, err := tools[0].Call(context.Background(), nil); err == nil {
		t.Fatal("stale MCP capability remained live")
	}
	if called != 1 {
		t.Fatal("denied tool executed")
	}
	for _, manager := range []bool{true, false} {
		for _, tool := range BuiltinToolsFor(a.Config(), a, manager) {
			if tool.Definition().Name == "create_skill" {
				if _, err := tool.Call(context.Background(), nil); err == nil {
					t.Fatal("restricted builtin executed")
				}
			}
		}
	}
}
func TestPrimaryProjectSync(t *testing.T) {
	c := projectTestConfig(t, "read-write")
	second := t.TempDir()
	c.Projects = append(c.Projects, Project{ID: "two", Name: "Two", Path: second, Permission: "read-only"})
	c.PrimaryProjectID = "two"
	c.EnabledMCPServers = map[string]MCPEntry{"nullbot-code-mcp": {Args: []string{"--workspace", "old"}}}
	c = normalizeConfig(c)
	if c.WorkspaceDir != second || c.EnabledMCPServers["nullbot-code-mcp"].Args[1] != second {
		t.Fatal("primary workspace not synchronized")
	}
	if allProjectsFull(c) {
		t.Fatal("mixed permissions permit external commands")
	}
	c.Projects[0].Permission = "invalid"
	if ValidateProjects(c) == nil {
		t.Fatal("invalid permission accepted")
	}
}
