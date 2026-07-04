package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"yourbot/internal/app"
)

func renderMessages(messages []app.Message, width int) string {
	if len(messages) == 0 {
		return mutedStyle.Render("No messages yet. Type /help to start.")
	}
	entries := make([]logEntry, 0, len(messages))
	for _, msg := range messages {
		role := userStyle
		switch msg.Role {
		case "assistant":
			role = botStyle
		case "reasoning":
			role = reasoningStyle
		}
		entries = append(entries, logEntry{
			Title: strings.ToUpper(msg.Role),
			Body:  msg.Content,
			Style: role,
		})
	}
	return renderRichLog(entries, width)
}

func renderActivity(events []activityEvent, reply app.Reply, width int) string {
	var b strings.Builder
	start := max(0, len(events)-40)
	for _, event := range events[start:] {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(activityLine(event, width))
	}
	if reply.OpenPanel != "" {
		fmt.Fprintf(&b, "\n\n%s\n", renderMarkdown("- Panel: `"+reply.OpenPanel+"`", width))
	}
	return strings.TrimSpace(b.String())
}

func renderFullActivity(events []activityEvent, width int) string {
	if len(events) == 0 {
		return "No activity yet."
	}
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "### %s\n", event.Time.Format("15:04:05"))
		if event.Input != "" {
			fmt.Fprintf(&b, "- **input:** %s\n", event.Input)
		}
		if event.Command != "" {
			fmt.Fprintf(&b, "- **command:** `%s`\n", event.Command)
		}
		if event.Panel != "" {
			fmt.Fprintf(&b, "- **panel:** `%s`\n", event.Panel)
		}
		if event.Status != "" {
			fmt.Fprintf(&b, "- **status:** %s\n", event.Status)
		}
		if event.Detail != "" && event.Detail != event.Status {
			fmt.Fprintf(&b, "- **detail:** %s\n", event.Detail)
		}
		b.WriteByte('\n')
	}
	return renderMarkdown(strings.TrimSpace(b.String()), width)
}

func activityLine(event activityEvent, width int) string {
	if strings.Contains(event.Status, "tool") || event.Status == "agent complete" || event.Status == "model start" || event.Status == "model error" {
		return renderToolActivityLine(event, width)
	}
	label := "event"
	if event.Input != "" {
		label = "input"
	}
	if event.Command != "" {
		label = event.Command
	}
	status := event.Status
	if status == "" {
		status = "event"
	}
	header := fmt.Sprintf("`%s` `%s` %s", event.Time.Format("15:04:05"), label, status)
	detail := event.Detail
	if event.Input != "" {
		detail = event.Input
	}
	if event.Panel != "" {
		if detail != "" {
			detail += " "
		}
		detail += "panel=" + event.Panel
	}
	if detail == "" {
		return renderMarkdown(header, width)
	}
	return renderMarkdown(header+"\n"+quoteCompact(detail, max(32, width*2)), width)
}

func renderToolActivityLine(event activityEvent, width int) string {
	agentName, toolName := splitAgentTool(event.Command)
	switch event.Status {
	case "tool start":
		args := strings.TrimPrefix(event.Detail, "args: ")
		header := fmt.Sprintf("`%s` `%s` called `%s`", event.Time.Format("15:04:05"), agentName, toolName)
		if args == "" {
			return renderMarkdown(header, width)
		}
		return renderMarkdown(header+"\nargs: `"+quoteCompact(args, max(48, width*2))+"`", width)
	case "tool complete":
		return renderMarkdown(fmt.Sprintf("`%s` `%s` `%s` complete", event.Time.Format("15:04:05"), agentName, toolName), width)
	case "tool error":
		detail := strings.TrimPrefix(event.Detail, "error: ")
		return renderMarkdown(fmt.Sprintf("`%s` `%s` `%s` error\n%s", event.Time.Format("15:04:05"), agentName, toolName, quoteCompact(detail, max(48, width*2))), width)
	case "agent complete":
		return renderMarkdown(fmt.Sprintf("`%s` `%s` Agent completed task.", event.Time.Format("15:04:05"), agentName), width)
	case "model start":
		return renderMarkdown(fmt.Sprintf("`%s` `%s` Agent started.", event.Time.Format("15:04:05"), agentName), width)
	case "model error":
		return renderMarkdown(fmt.Sprintf("`%s` `%s` Agent error.\n%s", event.Time.Format("15:04:05"), agentName, quoteCompact(event.Detail, max(48, width*2))), width)
	default:
		return renderMarkdown(fmt.Sprintf("`%s` `%s` %s", event.Time.Format("15:04:05"), agentName, event.Status), width)
	}
}

func splitAgentTool(command string) (agentName, toolName string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "agent", "tool"
	}
	if before, after, ok := strings.Cut(command, "/"); ok {
		if before != "" && after != "" {
			return before, after
		}
	}
	if command == "agent" {
		return "agent", "model"
	}
	return "agent", command
}

func compactStatus(text string) string {
	return quoteCompact(strings.ReplaceAll(text, "\n", " "), 52)
}

func quoteCompact(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		text = text[:limit] + "..."
	}
	return text
}

func renderModal(panel string, reply app.Reply, width int) string {
	switch panel {
	case "also":
		return renderAlsoModal(reply, width)
	case "help":
		return renderHelpModal(reply, width)
	case "files":
		return renderFilesModal(reply, width)
	case "history":
		return renderHistoryModal(reply, width)
	case "logs":
		return renderLogsModal(reply, width)
	case "market":
		return renderMarketModal(reply, width)
	case "mcp":
		return renderMCPModal(reply, width)
	case "plan":
		if plans, ok := reply.Data["plans"].([]app.PlanSummary); ok {
			return renderMarkdown(planSummaryMarkdown(plans), width)
		}
	case "usage":
		if usage, ok := reply.Data["usage"].(app.UsageSnapshot); ok {
			return renderMarkdown(fmt.Sprintf("# Usage\n\n- Session tokens: `%d`\n- Total tokens: `%d`\n- Total cost: `%s`\n", usage.Session.TotalTokens, usage.Total.TotalTokens, formatUsageCost(usage.Total.CostUSD)), width)
		}
	case "tasks":
		if tasks, ok := reply.Data["tasks"].([]app.AgentTask); ok {
			return renderMarkdown(taskSummaryMarkdown(tasks), width)
		}
	}
	if reply.Data != nil {
		data, _ := json.MarshalIndent(reply.Data, "", "  ")
		return renderMarkdown(reply.Message+"\n\n```json\n"+string(data)+"\n```", width)
	}
	return renderMarkdown(reply.Message, width)
}

func planSummaryMarkdown(plans []app.PlanSummary) string {
	if len(plans) == 0 {
		return "No plans yet. Create one with `/plan <goal>`."
	}
	var b strings.Builder
	b.WriteString("# Plans\n\n")
	for _, plan := range plans {
		fmt.Fprintf(&b, "- `%s` [%s] %s - %s\n", plan.ID, plan.Progress, plan.Name, quoteCompact(plan.Goal, 140))
	}
	return b.String()
}

func taskSummaryMarkdown(tasks []app.AgentTask) string {
	if len(tasks) == 0 {
		return "No tasks recorded yet."
	}
	var b strings.Builder
	b.WriteString("# Tasks\n\n")
	for _, task := range tasks {
		fmt.Fprintf(&b, "- `%s` [%s/%s] %s - %s\n", task.ID, task.Role, task.Status, task.Name, quoteCompact(task.Current, 120))
	}
	return b.String()
}

func renderHelpModal(reply app.Reply, width int) string {
	return renderMarkdown(reply.Message, width)
}

func renderAlsoModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString("# Also\n\n")
	if question, ok := reply.Data["question"].(string); ok && question != "" {
		fmt.Fprintf(&b, "**Question:** %s\n\n", question)
	}
	if active, ok := reply.Data["active"].(bool); ok {
		fmt.Fprintf(&b, "**Main run active when captured:** `%t`\n\n", active)
	}
	b.WriteString(reply.Message)
	if activity, ok := reply.Data["activity"].([]app.ActivityRecord); ok && len(activity) > 0 {
		b.WriteString("\n\n## Snapshot Activity\n\n")
		start := max(0, len(activity)-8)
		for _, record := range activity[start:] {
			fmt.Fprintf(&b, "- `%s` `%s` %s - %s\n", record.Time.Format("15:04:05"), record.Name, record.Status, quoteCompact(record.Detail, 120))
		}
	}
	return renderMarkdown(b.String(), width)
}

func renderFilesModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString("# Files\n\n")
	b.WriteString(reply.Message)
	if reply.Data == nil {
		return renderMarkdown(b.String(), width)
	}
	if workspace, ok := reply.Data["workspace"].(string); ok && workspace != "" {
		fmt.Fprintf(&b, "\n\n- **Workspace:** `%s`", workspace)
	}
	if editor, ok := reply.Data["editor"].(app.EditorConfig); ok && editor.Command != "" {
		fmt.Fprintf(&b, "\n- **Editor:** `%s`", editor.Command)
	}
	if counts, ok := reply.Data["counts"].(map[string]int); ok {
		fmt.Fprintf(&b, "\n- **Entries:** %d total, %d directories, %d files", counts["total"], counts["directories"], counts["files"])
	}
	b.WriteString("\n\n## Useful Commands\n\n")
	b.WriteString("- `/files workspace <path>` sets the workspace.\n")
	b.WriteString("- `/ls [path]` lists a directory in the output panel.\n")
	b.WriteString("- `/rm <path>` removes a file; `/rmdir <path>` removes a directory.\n")
	b.WriteString("- Enable code MCP tools for read/write/search/edit/run-command capabilities.\n")
	if listing, ok := reply.Data["listing"].(string); ok && strings.TrimSpace(listing) != "" {
		b.WriteString("\n\n## Top Level\n\n")
		b.WriteString(listing)
	}
	return renderMarkdown(b.String(), width)
}

func renderMarketModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString(reply.Message)
	b.WriteString("\n\n# Marketplace\n")
	if summary, ok := reply.Data["summary"].(string); ok && summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if packages, ok := reply.Data["packages"].([]app.MarketPackage); ok {
		b.WriteString("\n## Packages\n")
		if len(packages) == 0 {
			b.WriteString("- none found\n")
		}
		for _, pkg := range packages {
			state := pkg.Status
			if pkg.Enabled {
				state = "enabled"
			} else if pkg.Installed {
				state = "installed"
			}
			fmt.Fprintf(&b, "- `%s` [%s/%s] %s\n", pkg.ID, pkg.Kind, state, pkg.Description)
			if len(pkg.Permissions) > 0 {
				fmt.Fprintf(&b, "  permissions: `%s`\n", strings.Join(pkg.Permissions, "`, `"))
			}
			if pkg.Error != "" {
				fmt.Fprintf(&b, "  error: %s\n", pkg.Error)
			}
		}
	}
	b.WriteString("\n## Commands\n")
	b.WriteString("- `/market refresh`\n")
	b.WriteString("- `/market install <package-id>`\n")
	b.WriteString("- `/market install <package-id> small`\n")
	b.WriteString("- `/market install <package-id> enable`\n")
	b.WriteString("- `/mcp enable <server-id>`\n")
	return renderMarkdown(b.String(), width)
}

func renderMCPModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString(reply.Message)
	b.WriteString("\n\n# MCP Servers\n")
	if servers, ok := reply.Data["servers"].(map[string]app.MCPEntry); ok && len(servers) > 0 {
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			server := servers[name]
			state := "disabled"
			if server.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(&b, "- `%s` [%s] %s `%s`\n", name, state, server.Transport, server.Command)
		}
	} else {
		b.WriteString("- no enabled MCP servers\n")
	}
	if packages, ok := reply.Data["packages"].([]app.MarketPackage); ok {
		b.WriteString("\n## Installed Packages\n")
		found := false
		for _, pkg := range packages {
			if pkg.Kind != "mcp_server" || !pkg.Installed {
				continue
			}
			found = true
			state := "installed"
			if pkg.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(&b, "- `%s` [%s] %s\n", pkg.ID, state, pkg.InstallDir)
		}
		if !found {
			b.WriteString("- none installed\n")
		}
	}
	b.WriteString("\n## Commands\n")
	b.WriteString("- `/mcp enable <server-id>`\n")
	b.WriteString("- `/mcp disable <server-id>`\n")
	b.WriteString("- `/mcp remove <server-id>`\n")
	return renderMarkdown(b.String(), width)
}

func renderHistoryModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString(reply.Message)
	if files, ok := reply.Data["history_files"].([]app.HistoryFile); ok {
		b.WriteString("\n\n# History Files\n")
		if len(files) == 0 {
			b.WriteString("- none yet\n")
		}
		for _, file := range files {
			fmt.Fprintf(&b, "- %s (%d bytes)\n", file.Name, file.Size)
		}
	}
	if artifacts, ok := reply.Data["artifacts"].([]app.HistoryFile); ok {
		b.WriteString("\n# Artifacts\n")
		if len(artifacts) == 0 {
			b.WriteString("- none yet\n")
		}
		for _, file := range artifacts {
			fmt.Fprintf(&b, "- %s (%d bytes)\n", file.Name, file.Size)
		}
	}
	if messages, ok := reply.Data["history"].([]app.Message); ok {
		b.WriteString("\n# Visible Session\n")
		start := max(0, len(messages)-12)
		for _, message := range messages[start:] {
			fmt.Fprintf(&b, "- %s: %s\n", message.Role, quoteCompact(message.Content, 160))
		}
	}
	return renderMarkdown(b.String(), width)
}

func renderLogsModal(reply app.Reply, width int) string {
	var b strings.Builder
	b.WriteString(reply.Message)
	if logs, ok := reply.Data["logs"].([]string); ok {
		b.WriteString("\n\n```text\n")
		for _, line := range logs {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString("```")
	}
	return renderMarkdown(b.String(), width)
}
