package app

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Bradthebrad/tinychain/agent"
)

// External MCP semantics cannot be inferred from a name/schema. Deny every
// external call unless ALL projects explicitly permit unsandboxed execution.
func guardExternalTools(state *App, tools []agent.Tool) []agent.Tool {
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &projectExternalTool{Tool: tool, state: state})
	}
	return out
}

type projectExternalTool struct {
	agent.Tool
	state *App
}

func (t *projectExternalTool) Call(ctx context.Context, args map[string]any) (string, error) {
	if !allProjectsFull(t.state.Config()) {
		return "", fmt.Errorf("external tool denied: %s", FullAccessWarning)
	}
	return t.Tool.Call(ctx, args)
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

// Builtins have known implementations; never infer trust from an MCP tool name.
func guardBuiltinTools(state *App, tools []agent.Tool) []agent.Tool {
	if state == nil {
		return tools
	}
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &projectBuiltinTool{Tool: tool, state: state})
	}
	return out
}

type projectBuiltinTool struct {
	agent.Tool
	state *App
}

func (t *projectBuiltinTool) Call(ctx context.Context, args map[string]any) (string, error) {
	name := t.Tool.Definition().Name
	switch name {
	case "list_dir":
		return listWorkspaceDir(t.state.Config(), stringArg(args, "path"), 200)
	case "workspace_info", "config_dir_list", "config_dir_read", "skills_list", "skill_references", "skill_read", "plans_list", "plan_read", "plan_update_step", "history_recent", "history_sessions", "history_session_read", "logs_recent", "market_list_available", "market_read_package", "mcp_list_servers", "market_list", "mcp_list", "spawn_subagent", "subagent_status", "subagent_wait", "project_read_file", "project_write_file":
		return t.Tool.Call(ctx, args)
	default:
		if !allProjectsFull(t.state.Config()) {
			return "", fmt.Errorf("tool %s requires Full access: %s", name, FullAccessWarning)
		}
		return t.Tool.Call(ctx, args)
	}
}
