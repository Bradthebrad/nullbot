package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tinychain/agent"
)

func BuiltinTools(config Config, state *App) []agent.Tool {
	return []agent.Tool{
		configDirListTool(config),
		configDirReadTool(config),
		skillsListTool(config),
		historyRecentTool(state),
		marketListTool(config),
		mcpListTool(config),
	}
}

func configDirListTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "config_dir_list",
		Description: "List files inside the bot config directory.",
		Schema:      agent.ToolSchema(map[string]any{"path": agent.StringProperty("Optional path relative to the config directory.")}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			dir, err := safeConfigPath(config.AppDir, stringArg(args, "path"))
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return "", err
			}
			var names []string
			for _, entry := range entries {
				if entry.IsDir() {
					names = append(names, entry.Name()+"/")
				} else {
					names = append(names, entry.Name())
				}
			}
			return strings.Join(names, "\n"), nil
		},
	}
}

func configDirReadTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "config_dir_read",
		Description: "Read a small UTF-8 text file inside the bot config directory.",
		Schema: agent.ToolSchema(map[string]any{
			"path": agent.StringProperty("Path relative to the config directory."),
		}, "path"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			path, err := safeConfigPath(config.AppDir, stringArg(args, "path"))
			if err != nil {
				return "", err
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory", path)
			}
			if info.Size() > 128*1024 {
				return "", fmt.Errorf("%s is too large for config_dir_read", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

func skillsListTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "skills_list",
		Description: "List installed skill files.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return strings.Join(scanSkillFiles(config), "\n"), nil
		},
	}
}

func historyRecentTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "history_recent",
		Description: "Return a compact summary of recent visible chat messages.",
		Schema: agent.ToolSchema(map[string]any{
			"limit": agent.NumberProperty("Maximum number of recent messages."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			defer state.mu.Unlock()
			limit := state.config.HistoryRecentLimit
			if raw, ok := args["limit"].(float64); ok && raw > 0 {
				limit = int(raw)
			}
			if limit > len(state.history) {
				limit = len(state.history)
			}
			start := len(state.history) - limit
			var b strings.Builder
			for _, msg := range state.history[start:] {
				fmt.Fprintf(&b, "- %s: %s\n", msg.Role, truncate(msg.Content, 180))
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func marketListTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_list",
		Description: "List cached MCP market packages.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			packages := readMarketCache(filepath.Join(config.AppDir, "market", "index.json"))
			if len(packages) == 0 {
				return "No cached market packages found.", nil
			}
			var b strings.Builder
			for _, pkg := range packages {
				fmt.Fprintf(&b, "- %v: %v\n", pkg["name"], pkg["description"])
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func mcpListTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "mcp_list",
		Description: "List configured MCP servers.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			if len(config.EnabledMCPServers) == 0 {
				return "No MCP servers configured.", nil
			}
			var b strings.Builder
			for name, server := range config.EnabledMCPServers {
				fmt.Fprintf(&b, "- %s (%s): %s\n", name, server.Transport, server.Command)
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func safeConfigPath(root, rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	full := filepath.Clean(filepath.Join(root, rel))
	rootClean := filepath.Clean(root)
	if full != rootClean && !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes config directory")
	}
	return full, nil
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
