package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) mcpCommand(sub string) Reply {
	data := map[string]any{
		"servers": a.config.EnabledMCPServers,
		"dir":     filepath.Join(a.config.AppDir, "mcp"),
	}
	msg := "MCP panel opened."
	if sub != "" {
		msg = "MCP " + sub + " panel opened."
	}
	reply := a.reply(msg, "/mcp", "mcp")
	reply.Data = data
	return reply
}

func (a *App) planCommand(sub string) Reply {
	if strings.HasPrefix(sub, "focus ") {
		focus := strings.TrimSpace(strings.TrimPrefix(sub, "focus "))
		a.SetPlan("Focus: " + focus + "\n\n1. Clarify the goal.\n2. Gather available tools.\n3. Execute the smallest useful next step.\n4. Report results.")
		reply := a.reply("Plan focus updated.", "/plan", "plan")
		reply.Data = map[string]any{"plan": a.Plan()}
		return reply
	}
	if sub == "execute" {
		reply := a.reply("Plan execution requested. Agent execution wiring will use the current plan as workflow guidance.", "/plan", "plan")
		reply.Data = map[string]any{"plan": a.Plan()}
		return reply
	}
	reply := a.reply(focused("Plan panel opened", sub), "/plan", "plan")
	reply.Data = map[string]any{"plan": a.Plan()}
	return reply
}

func (a *App) marketCommand() Reply {
	reply := a.reply("Market panel opened. Initial market support reads cached metadata from the app data market directory.", "/market", "market")
	reply.Data = map[string]any{
		"dir":      filepath.Join(a.config.AppDir, "market"),
		"packages": readMarketCache(filepath.Join(a.config.AppDir, "market", "index.json")),
	}
	return reply
}

func (a *App) skillsCommand(sub string) Reply {
	reply := a.reply("Skills panel opened.", "/skills", "skills")
	reply.Data = map[string]any{"skills": scanSkillFiles(a.config)}
	if sub != "" {
		reply.Message = "Skills " + sub + " panel opened."
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
	reply := a.reply("Files panel opened. External editor: "+a.config.Editor.Command, "/files", "files")
	reply.Data = map[string]any{"subcommand": sub, "editor": a.config.Editor}
	return reply
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
	candidates := append([]string{filepath.Join(config.AppDir, "SKILL.md")}, config.SkillDirs...)
	for _, candidate := range candidates {
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

func readMarketCache(path string) []map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var packages []map[string]any
	if err := json.Unmarshal(data, &packages); err != nil {
		return nil
	}
	return packages
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
