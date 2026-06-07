package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tinychain/agent"
)

func BuiltinTools(config Config, state *App) []agent.Tool {
	return []agent.Tool{
		configDirListTool(config),
		configDirReadTool(config),
		skillsListTool(config),
		createSkillTool(config),
		historyRecentTool(state),
		historySessionsTool(config),
		historySessionReadTool(config),
		logsRecentTool(state),
		marketRefreshTool(state),
		marketListAvailableTool(state),
		marketReadPackageTool(state),
		marketInstallPackageTool(state),
		mcpListServersTool(state),
		mcpEnableServerTool(state),
		mcpDisableServerTool(state),
		mcpRemoveServerTool(state),
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

func createSkillTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "create_skill",
		Description: "Create one or more SKILL.md files under the bot skills directory. Each skill is written to its own safe subdirectory.",
		Schema: agent.ToolSchema(map[string]any{
			"name":        agent.StringProperty("Single skill name. Used as the subdirectory name after slugging."),
			"description": agent.StringProperty("Short description for generated content when content is omitted."),
			"content":     agent.StringProperty("Full SKILL.md content for a single skill. If omitted, a minimal SKILL.md is generated."),
			"overwrite":   agent.BoolProperty("Whether to overwrite existing SKILL.md files. Defaults to false."),
			"skills": map[string]any{
				"type":        "array",
				"description": "Optional batch of skill specs. Each item can include name, description, and content.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        agent.StringProperty("Skill name."),
						"description": agent.StringProperty("Skill description."),
						"content":     agent.StringProperty("Full SKILL.md content."),
					},
					"required": []string{"name"},
				},
			},
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			specs := skillSpecsFromArgs(args)
			if len(specs) == 0 {
				return "", fmt.Errorf("provide either name or skills")
			}
			if len(specs) > 20 {
				return "", fmt.Errorf("refusing to write more than 20 skills in one call")
			}
			overwrite, _ := args["overwrite"].(bool)
			root := primarySkillDir(config)
			if err := os.MkdirAll(root, 0700); err != nil {
				return "", err
			}
			var results []string
			for _, spec := range specs {
				path, created, err := writeSkillSpec(root, spec, overwrite)
				if err != nil {
					results = append(results, fmt.Sprintf("- %s: error: %v", spec.Name, err))
					continue
				}
				state := "created"
				if !created {
					state = "exists"
				}
				results = append(results, fmt.Sprintf("- %s: %s %s", spec.Name, state, path))
			}
			return strings.Join(results, "\n"), nil
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
			limit := 10
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

func historySessionsTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "history_sessions",
		Description: "List recent persisted agent session history files with compact summaries.",
		Schema: agent.ToolSchema(map[string]any{
			"limit": agent.NumberProperty("Maximum number of sessions to list."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			limit := config.HistoryRecentLimit
			if raw, ok := args["limit"].(float64); ok && raw > 0 {
				limit = int(raw)
			}
			summaries := listHistorySessionSummaries(config, limit)
			if len(summaries) == 0 {
				return "No persisted session history found.", nil
			}
			var b strings.Builder
			for _, summary := range summaries {
				fmt.Fprintf(&b, "- %s (%d messages, %s): %s\n", summary.SessionID, summary.Messages, summary.ModTime.Format("2006-01-02 15:04"), summary.Preview)
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func historySessionReadTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "history_session_read",
		Description: "Read a compact view of a persisted agent session by session_id. Use history_sessions first to discover ids.",
		Schema: agent.ToolSchema(map[string]any{
			"session_id": agent.StringProperty("Session id from history_sessions, without .jsonl."),
			"limit":      agent.NumberProperty("Maximum number of messages to return from the end of the session."),
		}, "session_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			limit := 10
			if raw, ok := args["limit"].(float64); ok && raw > 0 {
				limit = int(raw)
			}
			messages, err := readHistorySession(config, stringArg(args, "session_id"), limit)
			if err != nil {
				return "", err
			}
			if len(messages) == 0 {
				return "No messages found in that session.", nil
			}
			var b strings.Builder
			for _, msg := range messages {
				fmt.Fprintf(&b, "- %s: %s\n", msg.Role, truncate(msg.Content, 500))
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func logsRecentTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "logs_recent",
		Description: "Read recent lines from NullBot's own runtime log.",
		Schema: agent.ToolSchema(map[string]any{
			"limit": agent.NumberProperty("Maximum number of recent log lines. Defaults to 80 and caps at 200."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			limit := 80
			if raw, ok := args["limit"].(float64); ok && raw > 0 {
				limit = int(raw)
			}
			if limit > 200 {
				limit = 200
			}
			state.mu.Lock()
			logger := state.logger
			state.mu.Unlock()
			lines := logger.Recent(limit)
			if len(lines) == 0 {
				return "No log lines found.", nil
			}
			return strings.Join(lines, "\n"), nil
		},
	}
}

func marketListTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_list",
		Description: "List cached MCP market packages. Prefer market_list_available for richer package metadata.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			manifest, err := LoadMarketManifest(config)
			if err != nil {
				return "", err
			}
			return MarketSummary(manifest), nil
		},
	}
}

func marketRefreshTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_refresh",
		Description: "Refresh NullBot marketplace metadata from configured public GitHub release sources and rewrite the local market manifest.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			manifest, err := RefreshMarket(ctx, config)
			if err != nil {
				state.logError("market refresh failed", "error", err)
				return "", err
			}
			state.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "market", Name: "market_refresh", Status: "done", Detail: fmt.Sprintf("%d packages", len(manifest.Packages))})
			return MarketSummary(manifest), nil
		},
	}
}

func marketListAvailableTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_list_available",
		Description: "List available market packages with descriptions, permissions, installed state, and enabled state.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			manifest, err := LoadMarketManifest(config)
			if err != nil {
				return "", err
			}
			return MarketSummary(manifest), nil
		},
	}
}

func marketReadPackageTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_read_package",
		Description: "Read detailed metadata, README excerpt, assets, and permissions for a marketplace package.",
		Schema: agent.ToolSchema(map[string]any{
			"package_id": agent.StringProperty("Marketplace package id, such as nullbot-code-mcp, nullbot-parsers-mcp, api-probe, or mcp-skill."),
		}, "package_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			manifest, err := LoadMarketManifest(config)
			if err != nil {
				return "", err
			}
			pkg, _, err := findMarketPackage(manifest, stringArg(args, "package_id"))
			if err != nil {
				return "", err
			}
			return prettyJSON(pkg), nil
		},
	}
}

func marketInstallPackageTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "market_install_package",
		Description: "Download and install a marketplace package into NullBot app data. Set enable=true only when the user explicitly asked to enable that MCP server.",
		Schema: agent.ToolSchema(map[string]any{
			"package_id": agent.StringProperty("Marketplace package id to install."),
			"small":      agent.BoolProperty("Install UPX-compressed small binary when available. Defaults to false."),
			"enable":     agent.BoolProperty("Enable the installed MCP server immediately. Only valid for mcp_server packages."),
		}, "package_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			pkg, err := InstallMarketPackage(ctx, config, stringArg(args, "package_id"), boolArg(args, "small"))
			if err != nil {
				return "", err
			}
			if boolArg(args, "enable") && pkg.Kind == "mcp_server" {
				config, err = EnableMCPServer(config, pkg.ID)
				if err != nil {
					return "", err
				}
				state.mu.Lock()
				state.config = config
				state.mu.Unlock()
				state.MarkRuntimeDirty("agent installed and enabled MCP package " + pkg.ID)
			}
			state.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "market", Name: "market_install_package", Status: "done", Detail: pkg.ID})
			return prettyJSON(pkg), nil
		},
	}
}

func mcpListServersTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "mcp_list_servers",
		Description: "List installed and enabled MCP servers known to NullBot.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			manifest, err := LoadMarketManifest(config)
			if err != nil {
				return "", err
			}
			var servers []MarketPackage
			for _, pkg := range manifest.Packages {
				if pkg.Kind == "mcp_server" && (pkg.Installed || pkg.Enabled) {
					servers = append(servers, pkg)
				}
			}
			if len(servers) == 0 {
				return "No MCP servers installed or enabled.", nil
			}
			return prettyJSON(servers), nil
		},
	}
}

func mcpEnableServerTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "mcp_enable_server",
		Description: "Enable an installed MCP server and refresh NullBot's runtime tools on the next run. Use only when the user explicitly asks to enable it.",
		Schema: agent.ToolSchema(map[string]any{
			"server_id": agent.StringProperty("Installed MCP server id."),
		}, "server_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return state.mutateMCPServer(stringArg(args, "server_id"), "enable")
		},
	}
}

func mcpDisableServerTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "mcp_disable_server",
		Description: "Disable an enabled MCP server and refresh NullBot's runtime tools on the next run.",
		Schema: agent.ToolSchema(map[string]any{
			"server_id": agent.StringProperty("MCP server id."),
		}, "server_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return state.mutateMCPServer(stringArg(args, "server_id"), "disable")
		},
	}
}

func mcpRemoveServerTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "mcp_remove_server",
		Description: "Disable and remove an installed MCP server from NullBot app data. Use only when the user explicitly asks to remove it.",
		Schema: agent.ToolSchema(map[string]any{
			"server_id": agent.StringProperty("MCP server id."),
		}, "server_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return state.mutateMCPServer(stringArg(args, "server_id"), "remove")
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

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func (a *App) mutateMCPServer(id, action string) (string, error) {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	var err error
	switch action {
	case "enable":
		config, err = EnableMCPServer(config, id)
	case "disable":
		config, err = DisableMCPServer(config, id)
	case "remove":
		config, err = RemoveMCPServer(config, id)
	default:
		err = fmt.Errorf("unsupported MCP action %q", action)
	}
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.config = config
	a.mu.Unlock()
	a.MarkRuntimeDirty("agent " + action + "d MCP server " + id)
	return fmt.Sprintf("%s %s.", action, id), nil
}
