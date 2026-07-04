package app

import (
	"fmt"
	"strings"
)

func (a *App) configCommand(expr string) Reply {
	if expr == "" {
		reply := a.reply("Config panel opened.", "/config", "config")
		reply.Data = map[string]any{"runtime": RuntimeStatus(a.Config())}
		return reply
	}
	key, value, ok := strings.Cut(expr, "=")
	if !ok {
		return a.reply("Usage: /config key=value", "/config", "config")
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "brand_prefix", "bot_name", "name", "tagline", "editor", "model", "provider", "reasoning_effort", "workspace", "workspace_dir":
	default:
		return a.reply("Unsupported config key: "+key, "/config", "config")
	}
	err := a.UpdateConfig(func(config *Config) {
		switch key {
		case "brand_prefix":
			config.BrandPrefix = value
		case "bot_name", "name":
			config.BotName = value
		case "tagline":
			config.Tagline = value
		case "editor":
			config.Editor.Command = value
		case "model":
			config.Model.Model = value
		case "provider":
			config.Model.Provider = value
		case "reasoning_effort":
			config.Model.ReasoningEffort = NormalizeEffort(value)
		case "workspace", "workspace_dir":
			config.WorkspaceDir = value
		}
	})
	if err != nil {
		return a.reply("Could not save config: "+err.Error(), "/config", "config")
	}
	return a.reply(fmt.Sprintf("Updated %s.", key), "/config", "config")
}
