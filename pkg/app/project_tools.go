package app

import (
	"context"
	"io"
	"os"

	"github.com/Bradthebrad/tinychain/agent"
)

// guardExternalTools wraps tools from one MCP server so every call is
// authorized against the project permissions in effect at call time.
func guardExternalTools(state *App, server string, remote bool, tools []agent.Tool) []agent.Tool {
	return guardTools(state, server, remote, tools)
}

func projectFileTools(state *App) []agent.Tool {
	if state == nil {
		return nil
	}
	schema := agent.ToolSchema(map[string]any{"path": agent.StringProperty("Absolute path in any configured project, or relative to primary workspace."), "content": agent.StringProperty("Text for write."), "overwrite": map[string]any{"type": "boolean"}}, "path")
	return []agent.Tool{
		agent.ToolFunc{Name: "project_read_file", Description: "Read up to 512 KiB from a permitted project file.", Schema: schema, Func: func(ctx context.Context, args map[string]any) (string, error) {
			p, err := CheckProjectPath(state.Config(), stringArg(args, "path"), false, false)
			if err != nil {
				return "", err
			}
			f, err := os.Open(p)
			if err != nil {
				return "", err
			}
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, 512*1024))
			return string(b), err
		}},
		agent.ToolFunc{Name: "project_write_file", Description: "Write text to a read-write/full project. Parent must exist. Explicit overwrite required for existing files.", Schema: schema, Func: func(ctx context.Context, args map[string]any) (string, error) {
			p, err := CheckProjectPath(state.Config(), stringArg(args, "path"), true, false)
			if err != nil {
				return "", err
			}
			flags := os.O_CREATE | os.O_WRONLY | os.O_EXCL
			if yes, _ := args["overwrite"].(bool); yes {
				flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			}
			f, err := os.OpenFile(p, flags, 0600)
			if err != nil {
				return "", err
			}
			defer f.Close()
			_, err = f.WriteString(stringArg(args, "content"))
			return p, err
		}},
	}
}

// guardBuiltinTools authorizes NullBot's own tools. list_dir keeps its
// project-aware implementation; everything else runs through the same
// per-call policy as external tools.
func guardBuiltinTools(state *App, tools []agent.Tool) []agent.Tool {
	if state == nil {
		return tools
	}
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Definition().Name == "list_dir" {
			tool = listDirTool{Tool: tool, state: state}
		}
		out = append(out, &policyTool{inner: tool, state: state})
	}
	return out
}

type listDirTool struct {
	agent.Tool
	state *App
}

func (t listDirTool) Call(ctx context.Context, args map[string]any) (string, error) {
	return listWorkspaceDir(t.state.Config(), stringArg(args, "path"), 200)
}
