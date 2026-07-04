package app

import (
	"fmt"
	"strconv"
	"strings"
)

func (a *App) agentsCommand(rest string) Reply {
	fields := strings.Fields(rest)
	if len(fields) >= 2 && fields[0] == "available" {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 1 {
			return a.agentsPanelReply("Usage: /agents available <number>")
		}
		if n > 64 {
			n = 64
		}
		if err := a.UpdateConfig(func(config *Config) {
			config.Agent.MaxSubagents = n
		}); err != nil {
			return a.agentsPanelReply("Could not update available subagents: " + err.Error())
		}
		a.MarkRuntimeDirty(fmt.Sprintf("max subagents changed to %d", n))
		return a.agentsPanelReply(fmt.Sprintf("Max subagents set to %d.", n))
	}
	if len(fields) >= 2 && fields[0] == "cancel" {
		id := fields[1]
		if ok := a.CancelTask(id); !ok {
			return a.agentsPanelReply("Agent task not cancelable or not found: " + id)
		}
		return a.agentsPanelReply("Cancel requested for " + id + ".")
	}
	if len(fields) >= 2 && fields[0] == "detail" {
		id := fields[1]
		task, ok := a.TaskDetails(id)
		reply := a.agentsPanelReply("Agent details opened.")
		if ok {
			reply.Data["detail"] = task
		} else {
			reply.Message = "Agent task not found: " + id
		}
		return reply
	}
	return a.agentsPanelReply("Agents dashboard opened.")
}

func (a *App) thoughtsCommand(rest string) Reply {
	reply := a.agentsPanelReply("Thoughts dashboard opened.")
	reply.Command = "/thoughts"
	reply.OpenPanel = "thoughts"
	reply.Data["thoughts"] = a.ThoughtSnapshots()
	return reply
}

func (a *App) agentsPanelReply(message string) Reply {
	config := a.Config()
	tasks := a.TaskSnapshots()
	reply := a.reply(message, "/agents", "agents")
	reply.Data = map[string]any{
		"tasks":         tasks,
		"summary":       taskSummary(tasks),
		"usage":         a.UsageSnapshot(),
		"max_subagents": config.Agent.MaxSubagents,
		"runtime":       RuntimeStatus(config),
	}
	return reply
}

func (a *App) nameCommand(name string) Reply {
	name = strings.TrimSpace(name)
	if name == "" {
		return a.reply("Usage: /name <bot-name>", "/name", "config")
	}
	if len([]rune(name)) > 40 {
		return a.reply("Bot name must be 40 characters or fewer.", "/name", "config")
	}
	if err := a.UpdateConfig(func(config *Config) {
		config.BotName = name
	}); err != nil {
		return a.reply("Could not save bot name: "+err.Error(), "/name", "config")
	}
	a.MarkRuntimeDirty("bot name changed to " + name)
	return a.reply("Bot name set to "+name+".", "/name", "")
}
