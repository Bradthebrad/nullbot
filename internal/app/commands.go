package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type Command struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases,omitempty"`
}

var commands = []Command{
	{Name: "/help", Usage: "/help", Description: "Show commands."},
	{Name: "/plan", Usage: "/plan [focus]", Description: "Open or update a plan explicitly."},
	{Name: "/plan", Usage: "/plan focus <focus>", Description: "Set the plan focus."},
	{Name: "/plan", Usage: "/plan execute", Description: "Execute the current plan."},
	{Name: "/also", Usage: "/also <message>", Description: "Inject additional guidance into the current workflow."},
	{Name: "/pause", Usage: "/pause", Description: "Pause active work without discarding state."},
	{Name: "/mcp", Usage: "/mcp [enable|disable|remove <id>]", Description: "Manage MCP servers."},
	{Name: "/models", Usage: "/models", Description: "Select provider and model."},
	{Name: "/market", Usage: "/market [refresh|install <id>[,<id>...] [small] [enable]]", Description: "Browse and install MCP tool packages or skills."},
	{Name: "/skills", Usage: "/skills [list|add|remove|open|reload]", Description: "Manage skills."},
	{Name: "/analyze", Usage: "/analyze [focus]", Description: "Analyze the current session or a focused topic."},
	{Name: "/compact", Usage: "/compact [focus]", Description: "Compact history with an optional focus."},
	{Name: "/init", Usage: "/init", Description: "Initialize app config and default skill."},
	{Name: "/files", Usage: "/files [open|view|recent]", Description: "View files in app or external editor."},
	{Name: "/clear", Usage: "/clear", Description: "Clear visible chat history."},
	{Name: "/copy", Usage: "/copy", Description: "Copy the last assistant output to the clipboard."},
	{Name: "/history", Usage: "/history", Description: "Open recent session history."},
	{Name: "/logs", Usage: "/logs", Description: "Open runtime logs."},
	{Name: "/reset", Usage: "/reset", Description: "Reset current session state."},
	{Name: "/config", Usage: "/config [key=value]", Description: "Open settings or update a supported config key."},
	{Name: "/ls", Usage: "/ls", Description: "List files when file tools are installed.", Aliases: []string{"/dir"}},
	{Name: "/rm", Usage: "/rm <path>", Description: "Remove files when file tools are installed."},
	{Name: "/rmdir", Usage: "/rmdir <path>", Description: "Remove directories when file tools are installed."},
}

func (a *App) Execute(ctx context.Context, input string) Reply {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return a.reply("Type a message or slash command.", "", "")
	}
	if strings.HasPrefix(trimmed, "/") {
		return a.executeSlash(ctx, trimmed)
	}
	skills := inlineSkillHints(trimmed)
	return a.runAgent(ctx, skills)
}

func (a *App) executeSlash(ctx context.Context, input string) Reply {
	name, rest, _ := strings.Cut(input, " ")
	switch name {
	case "/help":
		return Reply{Message: formatHelp(), Command: name, OpenPanel: "help", Config: a.config, Suggestions: commandNames()}
	case "/plan":
		return a.planCommand(strings.TrimSpace(rest))
	case "/also":
		if strings.TrimSpace(rest) == "" {
			return a.reply("Usage: /also <message>", name, "")
		}
		return a.reply("Added guidance: "+strings.TrimSpace(rest), name, "")
	case "/pause":
		a.setPaused(true)
		return a.reply("Paused. Tool calls and results remain in session state.", name, "")
	case "/mcp":
		return a.mcpCommand(strings.TrimSpace(rest))
	case "/models":
		return a.modelsCommand()
	case "/market":
		return a.marketCommand(strings.TrimSpace(rest))
	case "/skills":
		return a.skillsCommand(strings.TrimSpace(rest))
	case "/analyze":
		return a.reply(focused("Analysis panel opened", rest), name, "analyze")
	case "/compact":
		return a.compactCommand(strings.TrimSpace(rest))
	case "/init":
		if err := EnsureAppDir(a.config); err != nil {
			return a.reply("Init failed: "+err.Error(), name, "")
		}
		return a.reply("Initialized "+a.config.AppDir, name, "config")
	case "/files":
		return a.filesCommand(strings.TrimSpace(rest))
	case "/clear":
		a.clearHistory()
		return a.reply("Cleared visible chat history.", name, "")
	case "/copy":
		output := a.LastOutput()
		if output == "" {
			return a.reply("No assistant output to copy yet.", name, "")
		}
		return a.reply("Copied last assistant output.", name, "", map[string]any{"copy": output})
	case "/history":
		reply := a.reply("History panel opened.", name, "history")
		state := a.State()
		reply.Data = map[string]any{
			"history":       state.History,
			"history_files": listHistoryFiles(a.config, a.config.HistoryRecentLimit),
			"artifacts":     listArtifactFiles(a.config, a.config.ArtifactRecentLimit),
		}
		return reply
	case "/logs":
		reply := a.reply("Logs panel opened.", name, "logs")
		reply.Data = map[string]any{"logs": a.Logs()}
		return reply
	case "/reset":
		a.clearHistory()
		a.setPaused(false)
		return a.reply("Reset current session state.", name, "")
	case "/config":
		return a.configCommand(strings.TrimSpace(rest))
	case "/ls", "/dir", "/rm", "/rmdir":
		return a.reply("File and coding commands are disabled until you install and enable a file/coding MCP server from /market.", name, "market")
	default:
		return a.reply(fmt.Sprintf("Unknown command %s. Try /help.", name), name, "help")
	}
}

func (a *App) reply(message, command, panel string, data ...map[string]any) Reply {
	reply := Reply{Message: message, Command: command, OpenPanel: panel, Config: a.config}
	if len(data) > 0 {
		reply.Data = data[0]
	}
	return reply
}

func formatHelp() string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "%s - %s\n", cmd.Usage, cmd.Description)
	}
	b.WriteString("\nKeybinds:\n")
	for _, line := range []string{
		"F1 - open help",
		"Ctrl+Q - quit",
		"Ctrl+C - pause active work",
		"Ctrl+J - insert newline",
		"Ctrl+O - open full activity log",
		"Ctrl+K - clear output panel",
		"Ctrl+L - clear activity panel",
		"Ctrl+A - select current input",
		"Ctrl+V - paste",
		"Ctrl+Z - clear current input",
		"Home/End - move input cursor or modal scroll",
		"PageUp/PageDown - scroll output",
		"Shift+Up/Shift+Down - scroll activity panel",
		"Tab/Right - complete slash command or recent input",
		"Up/Down or k/j - move modal selection or scroll modal",
		"Esc/q - close modal",
	} {
		fmt.Fprintf(&b, "%s\n", line)
	}
	return strings.TrimSpace(b.String())
}

func commandNames() []string {
	names := make([]string, 0, len(commands))
	for _, cmd := range commands {
		names = append(names, cmd.Name)
	}
	sort.Strings(names)
	return names
}

func focused(prefix, focus string) string {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return prefix + "."
	}
	return prefix + " with focus: " + focus
}

func inlineSkillHints(input string) []string {
	seen := map[string]bool{}
	var hints []string
	for _, field := range strings.Fields(input) {
		if !strings.HasPrefix(field, "/") || strings.Count(field, "/") != 1 {
			continue
		}
		token := strings.Trim(field, ".,;:!?()[]{}\"'")
		if token == "" || isSlashCommand(token) {
			continue
		}
		if !seen[token] {
			seen[token] = true
			hints = append(hints, token)
		}
	}
	return hints
}

func isSlashCommand(token string) bool {
	for _, cmd := range commands {
		if token == cmd.Name {
			return true
		}
		for _, alias := range cmd.Aliases {
			if token == alias {
				return true
			}
		}
	}
	return false
}
