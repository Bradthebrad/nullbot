package app

import (
	"fmt"
	"strings"
)

type EffortOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	OpenAI      string `json:"openai,omitempty"`
	Codex       string `json:"codex,omitempty"`
	Anthropic   string `json:"anthropic,omitempty"`
	OpenRouter  string `json:"openrouter,omitempty"`
}

func EffortOptions() []EffortOption {
	return []EffortOption{
		{
			ID:          "minimal",
			Label:       "Empty Vessel",
			Description: "Barely any deliberation; best for tiny edits and quick answers.",
			OpenAI:      "minimal",
			Codex:       "minimal",
			Anthropic:   "",
			OpenRouter:  "low",
		},
		{
			ID:          "low",
			Label:       "One Coffee In",
			Description: "Light reasoning for straightforward tasks.",
			OpenAI:      "low",
			Codex:       "low",
			Anthropic:   "low",
			OpenRouter:  "low",
		},
		{
			ID:          "medium",
			Label:       "Committee of One",
			Description: "Balanced reasoning for normal agent work.",
			OpenAI:      "medium",
			Codex:       "medium",
			Anthropic:   "medium",
			OpenRouter:  "medium",
		},
		{
			ID:          "high",
			Label:       "Corkboard Detective",
			Description: "Careful reasoning for larger refactors and multi-step work.",
			OpenAI:      "high",
			Codex:       "high",
			Anthropic:   "high",
			OpenRouter:  "high",
		},
		{
			ID:          "xhigh",
			Label:       "Big Brain",
			Description: "Maximum practical reasoning; maps down when a provider has no higher tier.",
			OpenAI:      "high",
			Codex:       "high",
			Anthropic:   "xhigh",
			OpenRouter:  "high",
		},
	}
}

func NormalizeEffort(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	input = strings.ReplaceAll(input, "_", "-")
	input = strings.ReplaceAll(input, " ", "-")
	switch input {
	case "", "default", "auto":
		return ""
	case "none", "no", "off", "empty", "empty-vessel", "minimal", "min":
		return "minimal"
	case "low", "light", "coffee", "one-coffee-in":
		return "low"
	case "medium", "med", "normal", "committee", "committee-of-one":
		return "medium"
	case "high", "deep", "detective", "corkboard", "corkboard-detective":
		return "high"
	case "extra", "extra-high", "xhigh", "x-high", "max", "big", "big-brain":
		return "xhigh"
	default:
		return input
	}
}

func EffortByID(id string) (EffortOption, bool) {
	id = NormalizeEffort(id)
	for _, option := range EffortOptions() {
		if option.ID == id {
			return option, true
		}
	}
	return EffortOption{}, false
}

func ProviderEffort(provider, effort string) string {
	option, ok := EffortByID(effort)
	if !ok {
		return NormalizeEffort(effort)
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return option.Codex
	case "anthropic":
		return option.Anthropic
	case "openrouter":
		return option.OpenRouter
	default:
		return option.OpenAI
	}
}

func (a *App) effortCommand(rest string) Reply {
	if strings.TrimSpace(rest) == "" {
		return a.effortPanelReply("Effort panel opened.")
	}
	level := NormalizeEffort(rest)
	option, ok := EffortByID(level)
	if !ok {
		return a.effortPanelReply("Unknown effort level: " + rest)
	}
	if err := a.UpdateConfig(func(config *Config) {
		config.Model.ReasoningEffort = option.ID
		config.SubagentModel.ReasoningEffort = option.ID
	}); err != nil {
		return a.effortPanelReply("Could not save effort: " + err.Error())
	}
	a.MarkRuntimeDirty("reasoning effort changed to " + option.ID)
	return a.effortPanelReply(fmt.Sprintf("Reasoning effort set to %s.", option.Label))
}

func (a *App) effortPanelReply(message string) Reply {
	config := a.Config()
	reply := a.reply(message, "/effort", "effort")
	reply.Data = map[string]any{
		"options":  EffortOptions(),
		"current":  config.Model.ReasoningEffort,
		"provider": config.Model.Provider,
		"mapped":   ProviderEffort(config.Model.Provider, config.Model.ReasoningEffort),
	}
	return reply
}
