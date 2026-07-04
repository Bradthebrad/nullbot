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
	{Name: "/tasks", Usage: "/tasks [cancel <id>|detail <id>]", Description: "Inspect and cancel running agent tasks."},
	{Name: "/schedule", Usage: "/schedule [list|in <duration>|at <time>|every <duration>|run <id>|cancel <id>|delete <id>]", Description: "Create, inspect, run, and cancel scheduled agent tasks."},
	{Name: "/agents", Usage: "/agents [available <number>|cancel <id>|detail <id>]", Description: "Open the live manager/subagent dashboard or update the subagent limit."},
	{Name: "/thoughts", Usage: "/thoughts", Description: "Open the manager/subagent reasoning and progress dashboard."},
	{Name: "/usage", Usage: "/usage [clear]", Description: "Show local token and cost usage tracking."},
	{Name: "/effort", Usage: "/effort [level]", Description: "Choose reasoning effort with provider-aware mapping."},
	{Name: "/pause", Usage: "/pause", Description: "Pause active work without discarding state."},
	{Name: "/mcp", Usage: "/mcp [enable|disable|remove <id>]", Description: "Manage MCP servers."},
	{Name: "/models", Usage: "/models", Description: "Select provider and model."},
	{Name: "/accounts", Usage: "/accounts [codex login]", Description: "View API key and subscription account status, or sign in to Codex."},
	{Name: "/themes", Usage: "/themes", Description: "Choose a TUI color theme."},
	{Name: "/market", Usage: "/market [refresh|install <id>[,<id>...] [small] [enable]]", Description: "Browse and install MCP tool packages or skills."},
	{Name: "/skills", Usage: "/skills [list|add|remove|open|reload]", Description: "Manage skills."},
	{Name: "/analyze", Usage: "/analyze [focus]", Description: "Analyze the current session or a focused topic."},
	{Name: "/compact", Usage: "/compact [focus]", Description: "Compact history with an optional focus."},
	{Name: "/init", Usage: "/init", Description: "Initialize app config and default skill."},
	{Name: "/files", Usage: "/files [workspace <path>|open|view|recent]", Description: "View and configure workspace files."},
	{Name: "/clear", Usage: "/clear", Description: "Clear visible chat history."},
	{Name: "/copy", Usage: "/copy", Description: "Copy the last assistant output to the clipboard."},
	{Name: "/paste", Usage: "/paste", Description: "Attach clipboard images/files or capture multiline clipboard text. Same as Alt+V."},
	{Name: "/history", Usage: "/history", Description: "Open recent session history."},
	{Name: "/logs", Usage: "/logs", Description: "Open runtime logs."},
	{Name: "/reset", Usage: "/reset", Description: "Reset current session state."},
	{Name: "/name", Usage: "/name <bot-name>", Description: "Change the displayed bot name."},
	{Name: "/config", Usage: "/config [key=value]", Description: "Open settings or update a supported config key."},
	{Name: "/ls", Usage: "/ls [path]", Description: "List files in the configured workspace.", Aliases: []string{"/dir"}},
	{Name: "/rm", Usage: "/rm <path> [--recursive]", Description: "Remove a workspace file or directory."},
	{Name: "/rmdir", Usage: "/rmdir <path> [--recursive]", Description: "Remove a workspace directory."},
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
		return a.RunAlsoObserver(ctx, rest)
	case "/tasks":
		return a.tasksCommand(strings.TrimSpace(rest))
	case "/schedule":
		return a.scheduleCommand(strings.TrimSpace(rest))
	case "/agents":
		return a.agentsCommand(strings.TrimSpace(rest))
	case "/thoughts":
		return a.thoughtsCommand(strings.TrimSpace(rest))
	case "/usage":
		return a.usageCommand(strings.TrimSpace(rest))
	case "/effort":
		return a.effortCommand(strings.TrimSpace(rest))
	case "/pause":
		a.setPaused(true)
		return a.reply("Paused. Tool calls and results remain in session state.", name, "")
	case "/mcp":
		return a.mcpCommand(strings.TrimSpace(rest))
	case "/models":
		return a.modelsCommand()
	case "/accounts":
		return a.accountsCommand(strings.TrimSpace(rest))
	case "/themes":
		return a.reply("Themes panel opened.", name, "themes")
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
	case "/name":
		return a.nameCommand(strings.TrimSpace(rest))
	case "/config":
		return a.configCommand(strings.TrimSpace(rest))
	case "/ls", "/dir":
		return a.listFilesCommand(name, strings.TrimSpace(rest))
	case "/rm":
		return a.removeFileCommand(name, strings.TrimSpace(rest), false)
	case "/rmdir":
		return a.removeFileCommand(name, strings.TrimSpace(rest), true)
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
	b.WriteString("# Help\n\n")
	b.WriteString("NullBot slash commands are UI controls. They are not sent to the agent as chat instructions.\n\n")
	b.WriteString("## Commands\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "- `%s` - %s\n", cmd.Usage, cmd.Description)
	}
	b.WriteString("\n## Keybinds\n\n")
	for _, line := range []string{
		"F1 - open help",
		"Ctrl+Q - quit",
		"Ctrl+C - pause active work",
		"Ctrl+J - insert newline",
		"Ctrl+O - open full activity log",
		"Ctrl+K - clear output panel",
		"Ctrl+L - clear activity panel",
		"Ctrl+A - select current input",
		"Ctrl+V - terminal paste",
		"Alt+V - NullBot paste for clipboard images, files, and multiline text",
		"Ctrl+Z - clear current input",
		"Home/End - move input cursor or modal scroll",
		"PageUp/PageDown - scroll output",
		"Shift+Up/Shift+Down - scroll activity panel",
		"Tab/Right - complete slash command or recent input",
		"Up/Down or k/j - move modal selection or scroll modal",
		"Esc/q - close modal",
	} {
		key, desc, ok := strings.Cut(line, " - ")
		if ok {
			fmt.Fprintf(&b, "- `%s` - %s\n", key, desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	b.WriteString("\n## Notes\n\n")
	b.WriteString("- Use `/files workspace <path>` to set the current workspace.\n")
	b.WriteString("- Use `Alt+V` or `/paste` to attach clipboard images/files and capture multiline clipboard text as a paste chip.\n")
	b.WriteString("- Use `/market` to install optional MCP tool packs.\n")
	b.WriteString("- Use `/also <question>` during a run to ask a side-channel observer without steering the active model call.\n")
	b.WriteString("- Use `/tasks` to inspect the primary agent, side observers, and spawned subagents.\n")
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
