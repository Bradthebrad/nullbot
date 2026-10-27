package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/agent"
)

func boolPtr(v bool) *bool { return &v }

func TestClassifyTool(t *testing.T) {
	cases := []struct {
		name, server string
		info         agent.ToolInfo
		override     string
		want         Capability
		source       string
	}{
		{name: "web_search", server: "web", want: CapRead, source: "name"},
		{name: "fetch_url", server: "web", want: CapRead, source: "name"},
		{name: "email_list", server: "emails", want: CapRead, source: "name"},
		{name: "email_send", server: "emails", want: CapWrite, source: "name"},
		{name: "drives_download", server: "drives", want: CapWrite, source: "name"},
		{name: "delete_path", server: "code", want: CapDelete, source: "name"},
		{name: "run_command", server: "code", want: CapExecute, source: "name"},
		{name: "browser_eval", server: "web", want: CapExecute, source: "name"},
		{name: "browser_click", server: "web", want: CapWrite, source: "name"},
		{name: "resize_image", server: "img", want: CapWrite, source: "name"},
		{name: "extract_tables", server: "parsers", want: CapRead, source: "name"},
		{name: "frobnicate", server: "x", want: CapWrite, source: "default"},
		{name: "frobnicate", server: "x", info: agent.ToolInfo{ReadOnly: boolPtr(true)}, want: CapRead, source: "annotation"},
		{name: "delete_everything", server: "x", info: agent.ToolInfo{ReadOnly: boolPtr(true)}, want: CapDelete, source: "name"},
		{name: "sync_docs", server: "x", info: agent.ToolInfo{Destructive: boolPtr(true)}, want: CapDelete, source: "annotation"},
		{name: "web_search", server: "web", override: "execute", want: CapExecute, source: "override"},
		{name: "config_dir_read", want: CapRead, source: "builtin"},
		{name: "market_install_package", want: CapExecute, source: "builtin"},
		{name: "getUserProfile", server: "x", want: CapRead, source: "name"},
	}
	for _, tc := range cases {
		got := ClassifyTool(tc.name, tc.server, tc.info, tc.override)
		if got.Capability != tc.want || got.Source != tc.source {
			t.Errorf("%s/%s: got %s via %s (%s), want %s via %s", tc.server, tc.name, got.Capability, got.Source, got.Reason, tc.want, tc.source)
		}
	}
	if blocked := ClassifyTool("web_search", "web", agent.ToolInfo{}, "blocked"); !blocked.Blocked {
		t.Fatal("blocked override not applied")
	}
}

func TestFindPathArgs(t *testing.T) {
	refs := FindPathArgs(map[string]any{
		"path":        "notes/today.md",
		"url":         "https://example.com/a/b",
		"query":       "/r/golang posts",
		"output_path": "out.png",
		"source":      "plain words not a path",
		"files":       []any{"a.txt", "b.txt"},
		"options":     map[string]any{"cwd": "sub"},
		"subject":     "Hello",
	})
	got := map[string]string{}
	for _, ref := range refs {
		got[ref.Arg] = ref.Value + "|" + ref.Role
	}
	for arg, want := range map[string]string{"path": "notes/today.md|", "output_path": "out.png|write", "files[0]": "a.txt|", "files[1]": "b.txt|", "options.cwd": "sub|"} {
		if got[arg] != want {
			t.Errorf("%s = %q, want %q (all: %v)", arg, got[arg], want, got)
		}
	}
	for _, arg := range []string{"url", "query", "source", "subject"} {
		if _, ok := got[arg]; ok {
			t.Errorf("%s should not be treated as a path: %v", arg, got)
		}
	}
	if runtime.GOOS != "windows" {
		tmp := t.TempDir()
		refs := FindPathArgs(map[string]any{"command": filepath.Join(tmp, "x.sh")})
		if len(refs) != 1 {
			t.Fatalf("absolute path in free-form arg not found: %v", refs)
		}
	}
}

func twoProjectConfig(t *testing.T, primaryPerm, otherPerm string) (Config, string, string) {
	t.Helper()
	c := projectTestConfig(t, primaryPerm)
	other := t.TempDir()
	c.Projects = append(c.Projects, Project{ID: "two", Name: "Two", Path: other, Permission: otherPerm})
	return c, c.WorkspaceDir, other
}

func TestAuthorizeToolCallByProject(t *testing.T) {
	req := func(name string) ToolRequirement { return ClassifyTool(name, "srv", agent.ToolInfo{}, "") }
	c, primary, other := twoProjectConfig(t, "read-only", "read-write")
	outside := t.TempDir()

	check := func(label string, want bool, r ToolRequirement, args map[string]any) PermissionDecision {
		t.Helper()
		d := AuthorizeToolCall(c, r, args)
		if d.Allowed != want {
			t.Fatalf("%s: allowed=%v want %v: %s", label, d.Allowed, want, d.Reason)
		}
		return d
	}
	check("read-only primary allows network read", true, req("web_search"), map[string]any{"query": "acme"})
	check("read-only primary blocks send", false, req("email_send"), map[string]any{"to": "a@b.c"})
	check("relative read in primary", true, req("read_file"), map[string]any{"path": "a.txt"})
	d := check("relative write in read-only primary", false, req("write_file"), map[string]any{"path": "a.txt"})
	if !strings.Contains(d.Reason, "read-only") {
		t.Fatalf("reason should explain the project: %s", d.Reason)
	}
	check("write in read-write project", true, req("write_file"), map[string]any{"path": filepath.Join(other, "a.txt")})
	check("delete in read-write project", true, req("delete_path"), map[string]any{"path": filepath.Join(other, "a.txt")})
	check("delete project root", false, req("delete_path"), map[string]any{"path": other})
	check("execute in read-write project", false, req("run_command"), map[string]any{"cwd": other})
	check("outside projects", false, req("read_file"), map[string]any{"path": filepath.Join(outside, "a.txt")})
	check("output must be writable", false, req("generate_image"), map[string]any{"prompt": "cat", "output_path": filepath.Join(primary, "cat.png")})
	check("mixed source and output", true, req("resize_image"), map[string]any{"input_path": filepath.Join(primary, "in.png"), "output_path": filepath.Join(other, "out.png")})

	nested := filepath.Join(other, "locked")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	c.Projects = append(c.Projects, Project{ID: "locked", Name: "Locked", Path: nested, Permission: "read-only"})
	check("nested read-only wins", false, req("write_file"), map[string]any{"path": filepath.Join(nested, "x.txt")})
	check("deleting a parent of a protected project", false, req("delete_path"), map[string]any{"path": other + string(filepath.Separator)})

	c.Projects[0].Permission = "full"
	check("execute in full primary", true, req("run_command"), map[string]any{"command": "go test"})
	c.Projects[0].Permission = "custom"
	c.Projects[0].Allow = []string{"execute"}
	check("custom allows execute", true, req("run_command"), map[string]any{"command": "go test"})
	check("custom without write", false, req("write_file"), map[string]any{"path": "a.txt"})
}

func TestValidateProjectsAcceptsCustom(t *testing.T) {
	c := projectTestConfig(t, "custom")
	c.Projects[0].Allow = []string{"write", "execute"}
	if err := ValidateProjects(c); err != nil {
		t.Fatal(err)
	}
	c.Projects[0].Allow = []string{"teleport"}
	if err := ValidateProjects(c); err == nil {
		t.Fatal("invalid custom capability accepted")
	}
	if got := ProjectCapabilities(Project{Permission: "custom", Allow: []string{"execute"}}); len(got) != 2 || got[0] != CapRead || got[1] != CapExecute {
		t.Fatalf("custom capabilities = %v", got)
	}
}

func TestPolicyToolReportsDecisionAndForwardsInfo(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	a := New(c)
	inner := agent.ToolFunc{Name: "web_search", Func: func(context.Context, map[string]any) (string, error) { return "results", nil }}
	tool := guardExternalTools(a, "web", true, []agent.Tool{inner})[0]
	rich, ok := tool.(agent.MetaTool)
	if !ok {
		t.Fatal("guarded tool must implement MetaTool")
	}
	out, meta, err := rich.CallWithMeta(context.Background(), map[string]any{"query": "x"})
	if err != nil || out != "results" {
		t.Fatalf("call = %q %v", out, err)
	}
	perm, _ := meta["permission"].(map[string]any)
	if perm["allowed"] != true || perm["capability"] != "read" {
		t.Fatalf("permission meta = %#v", meta)
	}
	info, ok := agent.InfoOf(tool)
	if !ok || info.Server != "web" || info.Source != "mcp" {
		t.Fatalf("info = %#v", info)
	}
}
