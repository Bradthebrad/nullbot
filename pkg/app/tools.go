package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bradthebrad/tinychain/agent"
)

func BuiltinTools(config Config, state *App) []agent.Tool {
	return BuiltinToolsFor(config, state, true)
}

func BuiltinToolsFor(config Config, state *App, includeSpawner bool) []agent.Tool {
	tools := []agent.Tool{
		configDirListTool(config),
		configDirReadTool(config),
		skillsListTool(config),
		skillReferencesTool(config),
		skillReadTool(config),
		createSkillTool(config),
		workspaceInfoTool(config),
		workspaceListDirTool(config),
		plansListTool(state),
		planReadTool(state),
		planUpdateStepTool(state),
		historyRecentTool(state),
		historySessionsTool(config),
		historySessionReadTool(config),
		historySearchTool(config),
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
	if includeSpawner {
		tools = append(tools, spawnSubagentTool(state), subagentStatusTool(state), subagentWaitTool(state))
	}
	tools = append(tools, projectFileTools(state)...)
	return guardBuiltinTools(state, tools)
}

func plansListTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "plans_list",
		Description: "List saved NullBot plans from the plans directory with progress and current step.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			return prettyJSON(listPlans(config)), nil
		},
	}
}

func planReadTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "plan_read",
		Description: "Read a saved plan JSON by id. Use plans_list first if the id is unknown.",
		Schema: agent.ToolSchema(map[string]any{
			"plan_id": agent.StringProperty("Plan id without .json."),
		}, "plan_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			plan, err := loadPlan(config, stringArg(args, "plan_id"))
			if err != nil {
				return "", err
			}
			return planJSON(plan), nil
		},
	}
}

func planUpdateStepTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "plan_update_step",
		Description: "Update a plan step status after completing or starting work. Valid statuses are pending, in_progress, blocked, and complete.",
		Schema: agent.ToolSchema(map[string]any{
			"plan_id": agent.StringProperty("Plan id without .json."),
			"step_id": agent.StringProperty("Step id such as 1 or 2.3."),
			"status":  agent.StringProperty("New status: pending, in_progress, blocked, or complete."),
			"note":    agent.StringProperty("Optional evidence or note for the step update."),
		}, "plan_id", "step_id", "status"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			state.mu.Lock()
			config := state.config
			state.mu.Unlock()
			plan, err := loadPlan(config, stringArg(args, "plan_id"))
			if err != nil {
				return "", err
			}
			status := strings.ToLower(strings.TrimSpace(stringArg(args, "status")))
			switch status {
			case "pending", "in_progress", "blocked", "complete", "completed", "done":
			default:
				return "", fmt.Errorf("unsupported step status %q", status)
			}
			if !updatePlanStepStatus(&plan, stringArg(args, "step_id"), status, stringArg(args, "note")) {
				return "", fmt.Errorf("step %q not found", stringArg(args, "step_id"))
			}
			if err := savePlan(config, plan); err != nil {
				return "", err
			}
			return planJSON(plan), nil
		},
	}
}

func workspaceInfoTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "workspace_info",
		Description: "Describe NullBot's configured workspace directory for lightweight file browsing.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			root, err := workspaceRoot(config)
			if err != nil {
				return "", err
			}
			return prettyJSON(map[string]any{
				"workspace": root,
				"note":      "Built-in NullBot workspace browsing is limited to listing files. Install/enable nullbot-code-mcp for reading, writing, searching, editing, and commands.",
			}), nil
		},
	}
}

func workspaceListDirTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "list_dir",
		Description: "List files and directories in NullBot's configured workspace. This built-in version is read-only and lightweight.",
		Schema: agent.ToolSchema(map[string]any{
			"path":      agent.StringProperty("Optional workspace-relative directory path. Defaults to workspace root."),
			"max_items": agent.NumberProperty("Maximum entries to return. Defaults to 200."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return listWorkspaceDir(config, stringArg(args, "path"), intArg(args, "max_items", 200))
		},
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
		Description: "List installed skills with metadata and reference files. Use skill_read to load SKILL.md or referenced markdown on demand.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return prettyJSON(scanSkillSummaries(config)), nil
		},
	}
}

func skillReferencesTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "skill_references",
		Description: "List markdown files referenced by an installed skill's SKILL.md without loading their contents.",
		Schema: agent.ToolSchema(map[string]any{
			"skill": agent.StringProperty("Skill name, directory name, or SKILL.md path."),
		}, "skill"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			summary, ok := findSkillSummary(config, stringArg(args, "skill"))
			if !ok {
				return "", fmt.Errorf("skill not found: %s", stringArg(args, "skill"))
			}
			return prettyJSON(summary.References), nil
		},
	}
}

func skillReadTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name:        "skill_read",
		Description: "Read an installed skill's primary SKILL.md or a referenced markdown file under that skill directory. Use this progressively only after skills_list indicates the skill is relevant.",
		Schema: agent.ToolSchema(map[string]any{
			"skill": agent.StringProperty("Skill name, directory name, or SKILL.md path."),
			"path":  agent.StringProperty("Optional referenced markdown path relative to the skill directory. Omit to read SKILL.md."),
		}, "skill"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			result, err := readSkillMarkdown(config, stringArg(args, "skill"), stringArg(args, "path"))
			if err != nil {
				return "", err
			}
			return prettyJSON(result), nil
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
		Name: "history_session_read",
		Description: "Read part of a saved session by session_id: any range of its messages, at full length if asked, " +
			"optionally with the tool calls made during it. Messages are numbered from 0; history_search results give the number " +
			"of each match, so read around a match with start = index - 3. Without start, returns the last messages.",
		Schema: agent.ToolSchema(map[string]any{
			"session_id":    agent.StringProperty("Session id from history_sessions or history_search, without .jsonl."),
			"start":         agent.NumberProperty("First message number to return (0 is the first message). Negative counts back from the end. Omit for the last messages."),
			"limit":         agent.NumberProperty("How many messages to return. Defaults to 10, at most 100."),
			"full":          agent.BoolProperty("Return whole messages (up to 20,000 characters each) instead of 500-character previews."),
			"include_tools": agent.BoolProperty("Interleave the tool calls made in this range, with their arguments and result previews."),
		}, "session_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			limit := intArg(args, "limit", 10)
			if limit <= 0 {
				limit = 10
			}
			if limit > 100 {
				limit = 100
			}
			chars := 500
			if boolArg(args, "full") {
				chars = 20000
			}
			includeTools := boolArg(args, "include_tools")
			entries, err := HistorySessionEntries(config, stringArg(args, "session_id"), includeTools)
			if err != nil {
				return "", err
			}
			var messages []int
			for i, entry := range entries {
				if entry.Kind == "message" {
					messages = append(messages, i)
				}
			}
			total := len(messages)
			if total == 0 && !includeTools {
				return "No messages found in that session.", nil
			}
			first := total - limit
			if _, ok := args["start"]; ok {
				first = intArg(args, "start", 0)
				if first < 0 {
					first = total + first
				}
			}
			if first < 0 {
				first = 0
			}
			last := first + limit
			if last > total {
				last = total
			}
			// The range spans entries from the first chosen message up to (not
			// including) the message after the last chosen one, so tool calls made
			// between them are included.
			from, to := 0, len(entries)
			if first < total {
				from = messages[first]
			} else {
				from = len(entries)
			}
			if last < total {
				to = messages[last]
			}
			if first == 0 {
				from = 0
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Messages %d-%d of %d.\n", first, max(first, last-1), total)
			for _, entry := range entries[from:to] {
				if entry.Kind == "message" {
					m := entry.Message
					fmt.Fprintf(&b, "#%d [%s] %s: %s\n", entry.Index, historyStamp(m.Time), m.Role, clipRunes(strings.TrimSpace(m.Content), chars))
					continue
				}
				t := entry.Tool
				line := fmt.Sprintf("  tool #%d [%s] %s", entry.Index, historyStamp(t.Time), t.Tool)
				if t.Server != "" {
					line += " (" + t.Server + ")"
				}
				if t.AgentID != "" && t.AgentID != "main" {
					line += " by " + t.AgentID
				}
				if len(t.Args) > 0 {
					line += " args=" + compactAny(t.Args, 400)
				}
				switch {
				case t.Error != "":
					line += " error: " + clipRunes(t.Error, 300)
				case t.Detail != "":
					line += " " + clipRunes(t.Detail, 300)
				case t.Result != "":
					line += " result: " + clipRunes(strings.Join(strings.Fields(t.Result), " "), min(chars, 2000))
				}
				b.WriteString(line + "\n")
			}
			if last < total {
				fmt.Fprintf(&b, "More follows: start=%d.", last)
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func historySearchTool(config Config) agent.Tool {
	return agent.ToolFunc{
		Name: "history_search",
		Description: "Search every saved session: what was said (user, assistant, reasoning, commentary) and the tool calls made, " +
			"newest first. Filter by words, role, tool, session and time. Each result gives its session_id and message or tool-call " +
			"number; read around it with history_session_read.",
		Schema: agent.ToolSchema(map[string]any{
			"query":      agent.StringProperty("Words that must all appear, in any order, ignoring case. Put exact phrases in double quotes."),
			"role":       agent.StringProperty("Only these kinds, comma-separated: user, assistant, reasoning, commentary, tool."),
			"tool":       agent.StringProperty("Only tool calls whose tool or server name contains this, e.g. search_issues or gmail."),
			"session_id": agent.StringProperty("Only this session."),
			"since":      agent.StringProperty("Only after this time: a date (2026-10-01), an RFC 3339 time, or a span back from now (90m, 36h, 7d, 2w)."),
			"until":      agent.StringProperty("Only before this time, in the same forms as since. A date includes that whole day."),
			"limit":      agent.NumberProperty("Results per page. Defaults to 20, at most 100."),
			"offset":     agent.NumberProperty("Results to skip, for the next page."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			now := time.Now()
			since, err := ParseHistoryTime(stringArg(args, "since"), false, now)
			if err != nil {
				return "", err
			}
			until, err := ParseHistoryTime(stringArg(args, "until"), true, now)
			if err != nil {
				return "", err
			}
			var roles []string
			for _, role := range strings.Split(stringArg(args, "role"), ",") {
				if role = strings.ToLower(strings.TrimSpace(role)); role != "" {
					roles = append(roles, role)
				}
			}
			limit := intArg(args, "limit", defaultSearchLimit)
			if limit > 100 {
				limit = 100
			}
			offset := intArg(args, "offset", 0)
			result, err := SearchHistory(config, HistoryQuery{
				Text: stringArg(args, "query"), SessionID: stringArg(args, "session_id"), Roles: roles,
				Tool: stringArg(args, "tool"), Since: since, Until: until, Limit: limit, Offset: offset,
			})
			if err != nil {
				return "", err
			}
			if len(result.Hits) == 0 {
				return fmt.Sprintf("No matches in %d session(s).", result.Searched), nil
			}
			var b strings.Builder
			for _, hit := range result.Hits {
				if hit.Kind == "tool" {
					label := hit.Tool
					if hit.Server != "" {
						label += " (" + hit.Server + ")"
					}
					fmt.Fprintf(&b, "- %s tool #%d [%s] %s, %s: %s\n", hit.SessionID, hit.Index, historyStamp(hit.Time), label, strings.TrimPrefix(hit.Status, "tool "), hit.Snippet)
					continue
				}
				fmt.Fprintf(&b, "- %s #%d [%s] %s: %s\n", hit.SessionID, hit.Index, historyStamp(hit.Time), hit.Role, hit.Snippet)
			}
			if result.More {
				fmt.Fprintf(&b, "More results: offset=%d.", offset+len(result.Hits))
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func historyStamp(t time.Time) string {
	if t.IsZero() {
		return "time unknown"
	}
	return t.Local().Format("2006-01-02 15:04")
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

func spawnSubagentTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "spawn_subagent",
		Description: "Spawn a named subagent for a focused task. Returns immediately with a task id; use subagent_wait or subagent_status to collect results.",
		Schema: agent.ToolSchema(map[string]any{
			"name": agent.StringProperty("Short human-readable subagent name, such as PDF Inspector or API Scout."),
			"task": agent.StringProperty("Concrete task for the subagent to perform. Include relevant context and expected output."),
		}, "name", "task"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			name := strings.TrimSpace(stringArg(args, "name"))
			task := strings.TrimSpace(stringArg(args, "task"))
			if name == "" {
				name = "Subagent"
			}
			if task == "" {
				return "", fmt.Errorf("task is required")
			}
			state.mu.Lock()
			maxSubagents := state.config.Agent.MaxSubagents
			state.mu.Unlock()
			if maxSubagents <= 0 {
				maxSubagents = 1
			}
			if state.runningSubagentCount() >= maxSubagents {
				return "", fmt.Errorf("subagent limit reached (%d running)", maxSubagents)
			}
			id, err := state.startSubagent(ctx, name, task)
			if err != nil {
				return "", err
			}
			return prettyJSON(map[string]any{
				"task_id": id,
				"name":    name,
				"status":  "running",
				"note":    "Use subagent_wait with this task_id before final synthesis.",
			}), nil
		},
	}
}

func subagentStatusTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "subagent_status",
		Description: "Read current status, tool calls, token counts, and final result if available for one subagent task or all subagent tasks.",
		Schema: agent.ToolSchema(map[string]any{
			"task_id": agent.StringProperty("Optional subagent task id returned by spawn_subagent."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "task_id"))
			if id != "" {
				task, ok := state.ownedTaskDetails(ctx, id)
				if !ok {
					return "", fmt.Errorf("task not found: %s", id)
				}
				return prettyJSON(task), nil
			}
			var tasks []AgentTask
			for _, task := range state.TaskSnapshots() {
				if task.Role == "subagent" && taskOwnedBy(ctx, task) {
					tasks = append(tasks, task)
				}
			}
			return prettyJSON(tasks), nil
		},
	}
}

func subagentWaitTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "subagent_wait",
		Description: "Wait briefly for a spawned subagent task to finish and return its status/result. Use after spawn_subagent.",
		Schema: agent.ToolSchema(map[string]any{
			"task_id":         agent.StringProperty("Subagent task id returned by spawn_subagent."),
			"timeout_seconds": agent.NumberProperty("Maximum seconds to wait. Defaults to 20 and caps at 120."),
		}, "task_id"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "task_id"))
			if id == "" {
				return "", fmt.Errorf("task_id is required")
			}
			timeout := intArg(args, "timeout_seconds", 20)
			if timeout <= 0 {
				timeout = 20
			}
			if timeout > 120 {
				timeout = 120
			}
			deadline := time.NewTimer(time.Duration(timeout) * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				task, ok := state.ownedTaskDetails(ctx, id)
				if !ok {
					return "", fmt.Errorf("task not found: %s", id)
				}
				if task.Status != TaskRunning && task.Status != TaskCanceling {
					return prettyJSON(task), nil
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-deadline.C:
					return prettyJSON(task), nil
				case <-tick.C:
				}
			}
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

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return fallback
	}
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
