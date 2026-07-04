package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

type configField struct {
	Label string
	Key   string
	Value string
}

func configFields(config app.Config) []configField {
	keys, _ := app.LoadAPIKeys(config)
	return []configField{
		{Label: "OpenAI API Key", Key: "key_openai", Value: app.MaskSecret(keys.OpenAI)},
		{Label: "Anthropic API Key", Key: "key_anthropic", Value: app.MaskSecret(keys.Anthropic)},
		{Label: "OpenRouter API Key", Key: "key_openrouter", Value: app.MaskSecret(keys.OpenRouter)},
		{Label: "Brand Prefix", Key: "brand_prefix", Value: config.BrandPrefix},
		{Label: "Bot Name", Key: "bot_name", Value: config.BotName},
		{Label: "Tagline", Key: "tagline", Value: config.Tagline},
		{Label: "Provider", Key: "provider", Value: config.Model.Provider},
		{Label: "Model", Key: "model", Value: config.Model.Model},
		{Label: "Subagent Provider", Key: "subagent_provider", Value: config.SubagentModel.Provider},
		{Label: "Subagent Model", Key: "subagent_model", Value: config.SubagentModel.Model},
		{Label: "Reasoning Effort", Key: "reasoning_effort", Value: config.Model.ReasoningEffort},
		{Label: "Temperature", Key: "temperature", Value: fmt.Sprintf("%g", config.Model.Temperature)},
		{Label: "Max Tokens", Key: "max_tokens", Value: strconv.Itoa(config.Model.MaxTokens)},
		{Label: "Max Iterations", Key: "max_iterations", Value: strconv.Itoa(config.Agent.MaxIterations)},
		{Label: "Max Subagents", Key: "max_subagents", Value: strconv.Itoa(config.Agent.MaxSubagents)},
		{Label: "Use Responses", Key: "use_responses", Value: strconv.FormatBool(config.Agent.UseResponses)},
		{Label: "Auto Compact", Key: "compact_enabled", Value: strconv.FormatBool(config.Compaction.Enabled)},
		{Label: "Token Threshold", Key: "compact_tokens", Value: strconv.Itoa(config.Compaction.ApproxTokenLimit)},
		{Label: "Keep Last Messages", Key: "keep_last", Value: strconv.Itoa(config.Compaction.KeepLastMessages)},
		{Label: "Editor", Key: "editor", Value: config.Editor.Command},
		{Label: "Workspace", Key: "workspace", Value: config.WorkspaceDir},
	}
}

func (m *Model) openConfigModal() {
	m.panel = "config"
	m.mode = ModeModal
	m.input.Blur()
	m.configFields = configFields(m.app.Config())
	m.configIndex = 0
	m.configEditing = false
	m.configEditValue = ""
	m.modal.SetContent(m.renderConfigModal())
	m.syncConfigModalViewport()
}

func (m *Model) handleConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "config" {
		return m, nil, false
	}
	if m.configEditing {
		switch msg.String() {
		case "esc":
			m.configEditing = false
		case "enter":
			m.configFields[m.configIndex].Value = sanitizeConfigValue(m.configEditValue)
			m.configEditing = false
		case "backspace":
			if len(m.configEditValue) > 0 {
				m.configEditValue = m.configEditValue[:len(m.configEditValue)-1]
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.configEditValue += sanitizeConfigValue(msg.String())
			}
		}
		m.modal.SetContent(m.renderConfigModal())
		m.syncConfigModalViewport()
		return m, nil, true
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.configIndex = max(0, m.configIndex-1)
	case "down", "j":
		m.configIndex = min(len(m.configFields)-1, m.configIndex+1)
	case "enter", "e":
		m.configEditing = true
		m.configEditValue = m.configFields[m.configIndex].Value
	case "ctrl+s", "s":
		if err := m.saveConfigFields(); err != nil {
			m.status = "Config save failed: " + err.Error()
		} else {
			m.status = "Config saved."
			m.events = append(m.events, activityEvent{Time: now(), Command: "/config", Status: "saved"})
			m.configFields = configFields(m.app.Config())
			m.modal.SetContent(m.renderConfigModal())
			m.syncConfigModalViewport()
			return m, tea.SetWindowTitle(app.DisplayName(m.app.Config())), true
		}
	}
	m.modal.SetContent(m.renderConfigModal())
	m.syncConfigModalViewport()
	return m, nil, true
}

func (m *Model) renderConfigModal() string {
	var b strings.Builder
	b.WriteString(renderMarkdown("Edit config values. `Enter`/`e` edits, `Enter` accepts edit, `s` saves. `Ctrl+S` may be captured by some terminals. `Esc` closes.", m.modal.Width))
	b.WriteString("\n\n")
	labelW := 18
	valueW := max(12, m.modal.Width-labelW-5)
	for i, field := range m.configFields {
		cursor := "  "
		value := field.Value
		if i == m.configIndex {
			cursor = "> "
			if m.configEditing {
				value = selectedInputStyle.Render(m.configEditValue)
			}
		}
		lines := wrapPlain(value, valueW, "")
		if len(lines) == 0 {
			lines = []string{""}
		}
		fmt.Fprintf(&b, "%s%-*s %s\n", cursor, labelW, field.Label+":", lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(&b, "  %-*s %s\n", labelW, "", line)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) syncConfigModalViewport() {
	selected := configModalHeaderLines() + m.configIndex
	m.keepModalLineVisible(selected)
}

func configModalHeaderLines() int {
	return 4
}

func (m *Model) saveConfigFields() error {
	values := map[string]string{}
	for _, field := range m.configFields {
		values[field.Key] = field.Value
	}
	keys := m.app.APIKeys()
	openAIKey := sanitizeConfigValue(values["key_openai"])
	anthropicKey := sanitizeConfigValue(values["key_anthropic"])
	openRouterKey := sanitizeConfigValue(values["key_openrouter"])
	if !isMasked(openAIKey) {
		keys.OpenAI = openAIKey
	}
	if !isMasked(anthropicKey) {
		keys.Anthropic = anthropicKey
	}
	if !isMasked(openRouterKey) {
		keys.OpenRouter = openRouterKey
	}
	if err := m.app.SaveAPIKeys(keys); err != nil {
		return err
	}
	return m.app.UpdateConfig(func(config *app.Config) {
		config.BrandPrefix = values["brand_prefix"]
		config.BotName = values["bot_name"]
		config.Tagline = values["tagline"]
		config.Model.Provider = values["provider"]
		config.Model.Model = values["model"]
		config.SubagentModel.Provider = values["subagent_provider"]
		config.SubagentModel.Model = values["subagent_model"]
		config.Model.ReasoningEffort = app.NormalizeEffort(values["reasoning_effort"])
		config.Model.Temperature = parseFloat(values["temperature"])
		config.Model.MaxTokens = parseInt(values["max_tokens"])
		config.Agent.MaxIterations = parseIntDefault(values["max_iterations"], config.Agent.MaxIterations)
		config.Agent.MaxSubagents = parseIntDefault(values["max_subagents"], config.Agent.MaxSubagents)
		config.Agent.UseResponses = parseBool(values["use_responses"])
		config.Compaction.Enabled = parseBool(values["compact_enabled"])
		config.Compaction.ApproxTokenLimit = parseIntDefault(values["compact_tokens"], config.Compaction.ApproxTokenLimit)
		config.Compaction.KeepLastMessages = parseIntDefault(values["keep_last"], config.Compaction.KeepLastMessages)
		config.Editor.Command = values["editor"]
		config.WorkspaceDir = values["workspace"]
	})
}

func isMasked(value string) bool {
	return value == "" || value == "(not set)" || strings.Contains(value, "...")
}

func sanitizeConfigValue(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value))
}

func parseFloat(value string) float64 {
	parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func parseBool(value string) bool {
	parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
	return parsed
}

func now() time.Time {
	return time.Now()
}
