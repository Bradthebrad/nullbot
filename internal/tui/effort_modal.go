package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func (m *Model) openEffortModal(reply app.Reply) {
	m.panel = "effort"
	m.mode = ModeModal
	m.input.Blur()
	m.effortOptions = app.EffortOptions()
	current := app.NormalizeEffort(m.app.Config().Model.ReasoningEffort)
	for i, option := range m.effortOptions {
		if option.ID == current {
			m.effortIndex = i
			break
		}
	}
	m.modal.SetContent(m.renderEffortModal())
	m.syncEffortModalViewport()
}

func (m *Model) handleEffortKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "effort" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.effortIndex = max(0, m.effortIndex-1)
	case "down", "j":
		m.effortIndex = min(len(m.effortOptions)-1, m.effortIndex+1)
	case "home":
		m.effortIndex = 0
	case "end":
		m.effortIndex = max(0, len(m.effortOptions)-1)
	case "enter", "s":
		m.applyEffortSelection()
	}
	m.modal.SetContent(m.renderEffortModal())
	m.syncEffortModalViewport()
	return m, nil, true
}

func (m Model) handleEffortMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "effort" {
		return m, nil, false
	}
	row, ok := modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.effortRowStarts())
	if !ok {
		return m, nil, false
	}
	m.effortIndex = row
	m.applyEffortSelection()
	m.modal.SetContent(m.renderEffortModal())
	m.syncEffortModalViewport()
	return m, nil, true
}

func (m *Model) applyEffortSelection() {
	if len(m.effortOptions) == 0 || m.effortIndex < 0 || m.effortIndex >= len(m.effortOptions) {
		return
	}
	option := m.effortOptions[m.effortIndex]
	if err := m.app.UpdateConfig(func(config *app.Config) {
		config.Model.ReasoningEffort = option.ID
		config.SubagentModel.ReasoningEffort = option.ID
	}); err != nil {
		m.status = "Effort save failed: " + err.Error()
		return
	}
	m.app.MarkRuntimeDirty("reasoning effort changed to " + option.ID)
	m.status = "Effort set to " + option.Label + "."
	m.events = append(m.events, activityEvent{Time: now(), Command: "/effort", Status: "selected " + option.ID})
}

func (m Model) renderEffortModal() string {
	config := m.app.Config()
	var b strings.Builder
	mapped := app.ProviderEffort(config.Model.Provider, config.Model.ReasoningEffort)
	if mapped == "" {
		mapped = "provider default"
	}
	b.WriteString(renderMarkdown(fmt.Sprintf("Current provider: `%s`\nCurrent model effort: `%s`\nProvider value: `%s`", config.Model.Provider, config.Model.ReasoningEffort, mapped), m.modal.Width))
	b.WriteString("\n\n")
	nameW := min(22, max(14, m.modal.Width/4))
	idW := 9
	fmt.Fprintf(&b, "%-2s %-*s %-*s %s\n", "", nameW, "LEVEL", idW, "ID", "DESCRIPTION")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(24, m.modal.Width-2)))
	for i, option := range m.effortOptions {
		cursor := "  "
		if i == m.effortIndex {
			cursor = "> "
		}
		descW := max(16, m.modal.Width-nameW-idW-8)
		lines := wrapPlain(option.Description, descW, "")
		if len(lines) == 0 {
			lines = []string{""}
		}
		for j, desc := range lines {
			label := ""
			id := ""
			if j == 0 {
				label = padCell(option.Label, nameW)
				id = padCell(option.ID, idW)
			} else {
				label = strings.Repeat(" ", nameW)
				id = strings.Repeat(" ", idW)
			}
			line := fmt.Sprintf("%s%-*s %-*s %s", cursor, nameW, label, idW, id, desc)
			if i == m.effortIndex {
				line = selectedRowStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i == m.effortIndex {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("   maps: openai=%q anthropic=%q openrouter=%q", option.OpenAI, emptyAsDefault(option.Anthropic), option.OpenRouter)))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) effortRowStarts() []int {
	start := len(strings.Split(renderMarkdown(fmt.Sprintf("Current provider: `%s`\nCurrent model effort: `%s`\nProvider value: `%s`", m.app.Config().Model.Provider, m.app.Config().Model.ReasoningEffort, app.ProviderEffort(m.app.Config().Model.Provider, m.app.Config().Model.ReasoningEffort)), m.modal.Width), "\n")) + 3
	start += 2
	starts := make([]int, 0, len(m.effortOptions))
	for i, option := range m.effortOptions {
		starts = append(starts, start)
		descW := max(16, m.modal.Width-min(22, max(14, m.modal.Width/4))-9-8)
		start += max(1, len(wrapPlain(option.Description, descW, "")))
		if i == m.effortIndex {
			start++
		}
	}
	return starts
}

func (m *Model) syncEffortModalViewport() {
	starts := m.effortRowStarts()
	if m.effortIndex >= 0 && m.effortIndex < len(starts) {
		m.keepModalLineVisible(starts[m.effortIndex])
	}
}

func emptyAsDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}
