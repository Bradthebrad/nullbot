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
	case "brand_prefix", "tagline", "editor", "model", "provider", "reasoning_effort":
	default:
		return a.reply("Unsupported config key: "+key, "/config", "config")
	}
	err := a.UpdateConfig(func(config *Config) {
		switch key {
		case "brand_prefix":
			config.BrandPrefix = value
		case "tagline":
			config.Tagline = value
		case "editor":
			config.Editor.Command = value
		case "model":
			config.Model.Model = value
		case "provider":
			config.Model.Provider = value
		case "reasoning_effort":
			config.Model.ReasoningEffort = value
		}
	})
	if err != nil {
		return a.reply("Could not save config: "+err.Error(), "/config", "config")
	}
	return a.reply(fmt.Sprintf("Updated %s.", key), "/config", "config")
}
