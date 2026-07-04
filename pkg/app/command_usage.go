package app

import "strings"

func (a *App) usageCommand(rest string) Reply {
	if strings.EqualFold(strings.TrimSpace(rest), "clear") {
		if err := a.ClearUsage(); err != nil {
			return a.reply("Usage clear failed: "+err.Error(), "/usage", "usage", map[string]any{
				"usage": a.UsageSnapshot(),
			})
		}
		return a.reply("Usage history cleared.", "/usage", "usage", map[string]any{
			"usage": a.UsageSnapshot(),
		})
	}
	return a.reply("Usage panel opened.", "/usage", "usage", map[string]any{
		"usage": a.UsageSnapshot(),
	})
}
