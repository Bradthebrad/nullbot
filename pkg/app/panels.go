package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) mcpCommand(sub string) Reply {
	fields := strings.Fields(sub)
	if len(fields) >= 2 {
		id := fields[1]
		switch fields[0] {
		case "enable":
			config, err := EnableMCPServer(a.config, id)
			if err != nil {
				return a.reply("MCP enable failed: "+err.Error(), "/mcp", "mcp")
			}
			a.mu.Lock()
			a.config = config
			a.mu.Unlock()
			a.MarkRuntimeDirty("enabled MCP server " + id)
			return a.mcpPanelReply("Enabled MCP server " + id + ".")
		case "disable":
			config, err := DisableMCPServer(a.config, id)
			if err != nil {
				return a.reply("MCP disable failed: "+err.Error(), "/mcp", "mcp")
			}
			a.mu.Lock()
			a.config = config
			a.mu.Unlock()
			a.MarkRuntimeDirty("disabled MCP server " + id)
			return a.mcpPanelReply("Disabled MCP server " + id + ".")
		case "remove":
			config, err := RemoveMCPServer(a.config, id)
			if err != nil {
				return a.reply("MCP remove failed: "+err.Error(), "/mcp", "mcp")
			}
			a.mu.Lock()
			a.config = config
			a.mu.Unlock()
			a.MarkRuntimeDirty("removed MCP server " + id)
			return a.mcpPanelReply("Removed MCP server " + id + ".")
		}
	}
	return a.mcpPanelReply("MCP panel opened.")
}

func (a *App) mcpPanelReply(message string) Reply {
	manifest, _ := LoadMarketManifest(a.config)
	data := map[string]any{
		"servers":  a.config.EnabledMCPServers,
		"dir":      filepath.Join(a.config.AppDir, "mcp"),
		"packages": manifest.Packages,
	}
	reply := a.reply(message, "/mcp", "mcp")
	reply.Data = data
	return reply
}

func (a *App) planCommand(sub string) Reply {
	sub = strings.TrimSpace(sub)
	if strings.HasPrefix(sub, "focus ") {
		sub = strings.TrimSpace(strings.TrimPrefix(sub, "focus "))
	}
	if strings.HasPrefix(sub, "execute") {
		id := strings.TrimSpace(strings.TrimPrefix(sub, "execute"))
		taskID, planID, err := a.StartPlanExecutor(id)
		if err != nil {
			reply := a.planPanelReply("Plan execution failed: " + err.Error())
			reply.Data["error"] = err.Error()
			return reply
		}
		reply := a.planPanelReply("Plan execution started for " + planID + ".")
		reply.Data["task_id"] = taskID
		reply.Data["plan_id"] = planID
		if plan, err := a.PlanByID(planID); err == nil {
			reply.Data["selected"] = plan
		}
		return reply
	}
	if sub != "" {
		plan, err := a.runPlanner(contextOrBackground(), sub)
		if err != nil {
			reply := a.planPanelReply("Plan creation failed: " + err.Error())
			reply.Data["error"] = err.Error()
			return reply
		}
		reply := a.planPanelReply("Created plan " + plan.ID + ".")
		reply.Data["selected"] = plan
		return reply
	}
	return a.planPanelReply("Plan panel opened.")
}

func (a *App) planPanelReply(message string) Reply {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	plans := listPlans(config)
	reply := a.reply(message, "/plan", "plan")
	reply.Data = map[string]any{
		"plans": plans,
		"dir":   plansDir(config),
	}
	if len(plans) > 0 {
		if plan, err := loadPlan(config, plans[0].ID); err == nil {
			reply.Data["selected"] = plan
		}
	}
	return reply
}

func (a *App) PlanPanel() Reply {
	return a.planPanelReply("Plan panel opened.")
}

func (a *App) marketCommand(sub string) Reply {
	fields := strings.Fields(sub)
	if len(fields) > 0 {
		switch fields[0] {
		case "refresh":
			manifest, err := RefreshMarket(contextOrBackground(), a.config)
			if err != nil {
				return a.reply("Market refresh failed: "+err.Error(), "/market", "market")
			}
			return a.marketPanelReply("Market refreshed.", manifest)
		case "install":
			if len(fields) < 2 {
				return a.reply("Usage: /market install <package-id>[,<package-id>...] [small] [enable]", "/market", "market")
			}
			small := containsField(fields[2:], "small")
			enable := containsField(fields[2:], "enable")
			ids := splitMarketPackageIDs(fields[1])
			if len(ids) == 0 {
				return a.reply("Usage: /market install <package-id>[,<package-id>...] [small] [enable]", "/market", "market")
			}
			installed := make([]string, 0, len(ids))
			for _, id := range ids {
				pkg, err := InstallMarketPackage(contextOrBackground(), a.config, id, small)
				if err != nil {
					return a.reply("Market install failed: "+err.Error(), "/market", "market")
				}
				if enable && pkg.Kind == "mcp_server" {
					config, err := EnableMCPServer(a.config, pkg.ID)
					if err != nil {
						return a.reply("Installed "+pkg.ID+" but enable failed: "+err.Error(), "/market", "market")
					}
					a.mu.Lock()
					a.config = config
					a.mu.Unlock()
					a.MarkRuntimeDirty("installed and enabled MCP package " + pkg.ID)
				}
				installed = append(installed, pkg.ID)
			}
			manifest, _ := LoadMarketManifest(a.config)
			return a.marketPanelReply("Installed "+strings.Join(installed, ", ")+".", manifest)
		}
	}
	manifest, err := LoadMarketManifest(a.config)
	if err != nil {
		return a.reply("Market panel failed: "+err.Error(), "/market", "market")
	}
	return a.marketPanelReply("Market panel opened.", manifest)
}

func splitMarketPackageIDs(input string) []string {
	var ids []string
	for _, raw := range strings.Split(input, ",") {
		id := strings.TrimSpace(raw)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (a *App) skillsCommand(sub string) Reply {
	reply := a.reply("Skills panel opened.", "/skills", "skills")
	summaries := scanSkillSummaries(a.config)
	reply.Data = map[string]any{
		"skills":       summaries,
		"skill_paths":  scanSkillFiles(a.config),
		"skill_dirs":   a.config.SkillDirs,
		"selected_idx": 0,
	}
	if sub != "" {
		fields := strings.Fields(sub)
		if len(fields) >= 2 && (fields[0] == "open" || fields[0] == "read") {
			result, err := readSkillMarkdown(a.config, fields[1], strings.Join(fields[2:], " "))
			if err != nil {
				reply.Message = "Skill read failed: " + err.Error()
			} else {
				reply.Message = "Skill opened: " + result.SkillName
				reply.Data["selected"] = result
			}
		} else {
			reply.Message = "Skills " + sub + " panel opened."
		}
	}
	return reply
}

func (a *App) compactCommand(focus string) Reply {
	summary := a.compactSummary(focus)
	reply := a.reply(summary, "/compact", "compact")
	reply.Data = map[string]any{"summary": summary, "focus": focus}
	return reply
}

func (a *App) filesCommand(sub string) Reply {
	fields := strings.Fields(sub)
	if len(fields) >= 2 && fields[0] == "workspace" {
		path := strings.TrimSpace(strings.TrimPrefix(sub, "workspace"))
		if err := a.UpdateConfig(func(config *Config) {
			config.WorkspaceDir = path
		}); err != nil {
			return a.reply("Workspace update failed: "+err.Error(), "/files", "files")
		}
		a.MarkRuntimeDirty("workspace changed")
		return a.filesCommand("")
	}
	root, err := workspaceRoot(a.config)
	message := "Files panel opened."
	if err != nil {
		message = "Files panel opened, but workspace is invalid: " + err.Error()
	}
	reply := a.reply(message, "/files", "files")
	summary := map[string]any{"subcommand": sub, "editor": a.config.Editor, "workspace": root}
	if err == nil {
		if counts, countErr := workspaceCounts(root); countErr == nil {
			summary["counts"] = counts
		}
		if listing, listErr := listWorkspaceDir(a.config, ".", 40); listErr == nil {
			summary["listing"] = listing
		}
	}
	reply.Data = summary
	return reply
}

func (a *App) listFilesCommand(command, path string) Reply {
	output, err := listWorkspaceDir(a.config, path, 200)
	if err != nil {
		return a.reply("List failed: "+err.Error(), command, "")
	}
	return a.reply(output, command, "", map[string]any{"workspace": a.config.WorkspaceDir, "path": path})
}

func workspaceCounts(root string) (map[string]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"files": 0, "directories": 0}
	for _, entry := range entries {
		if entry.IsDir() {
			counts["directories"]++
		} else {
			counts["files"]++
		}
	}
	counts["total"] = len(entries)
	return counts, nil
}

func (a *App) removeFileCommand(command, sub string, dirsOnly bool) Reply {
	fields := strings.Fields(sub)
	if len(fields) == 0 {
		return a.reply("Usage: "+command+" <path> [--recursive]", command, "files")
	}
	recursive := containsField(fields[1:], "--recursive") || containsField(fields[1:], "-r")
	message, err := removeWorkspacePath(a.config, fields[0], recursive, dirsOnly)
	if err != nil {
		return a.reply("Remove failed: "+err.Error(), command, "files")
	}
	return a.reply(message, command, "files")
}

func (a *App) compactSummary(focus string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	limit := a.config.Compaction.KeepLastMessages
	if limit <= 0 || limit > len(a.history) {
		limit = len(a.history)
	}
	start := len(a.history) - limit
	var b strings.Builder
	if strings.TrimSpace(focus) != "" {
		fmt.Fprintf(&b, "Compaction focus: %s\n\n", strings.TrimSpace(focus))
	}
	fmt.Fprintf(&b, "Kept last %d messages verbatim out of %d visible messages.\n", limit, len(a.history))
	for _, msg := range a.history[start:] {
		fmt.Fprintf(&b, "- %s: %s\n", msg.Role, truncate(msg.Content, 220))
	}
	return strings.TrimSpace(b.String())
}

func scanSkillFiles(config Config) []string {
	var skills []string
	for _, candidate := range config.SkillDirs {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			skills = append(skills, candidate)
			continue
		}
		entries, err := os.ReadDir(candidate)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				path := filepath.Join(candidate, entry.Name(), "SKILL.md")
				if _, err := os.Stat(path); err == nil {
					skills = append(skills, path)
				}
			}
		}
	}
	return skills
}

func (a *App) marketPanelReply(message string, manifest MarketManifest) Reply {
	reply := a.reply(message, "/market", "market")
	reply.Data = map[string]any{
		"dir":       filepath.Join(a.config.AppDir, "market"),
		"manifest":  manifest,
		"packages":  manifest.Packages,
		"summary":   MarketSummary(manifest),
		"refreshed": manifest.UpdatedAt,
	}
	return reply
}

func containsField(fields []string, target string) bool {
	for _, field := range fields {
		if strings.EqualFold(field, target) {
			return true
		}
	}
	return false
}

func contextOrBackground() context.Context {
	return context.Background()
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
