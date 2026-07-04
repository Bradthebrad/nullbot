package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func (m *Model) openThemesModal() {
	m.panel = "themes"
	m.mode = ModeModal
	m.input.Blur()
	m.themeIndex = themeIndexByID(m.app.Config().UI.Theme)
	m.modal.SetContent(m.renderThemesModal())
	m.syncThemesModalViewport()
}

func (m *Model) handleThemesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "themes" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.themeIndex = max(0, m.themeIndex-1)
	case "down", "j":
		m.themeIndex = min(len(themes)-1, m.themeIndex+1)
	case "d":
		m.themeDetails = !m.themeDetails
	case "enter", "ctrl+s":
		theme := themes[m.themeIndex]
		if err := m.app.UpdateConfig(func(config *app.Config) {
			config.UI.Theme = theme.ID
		}); err != nil {
			m.status = "Theme save failed: " + err.Error()
		} else {
			applyTheme(theme.ID)
			m.status = "Theme set to " + theme.Name
			m.events = append(m.events, activityEvent{Time: now(), Command: "/themes", Status: "selected " + theme.ID})
		}
	}
	m.modal.SetContent(m.renderThemesModal())
	m.syncThemesModalViewport()
	return m, nil, true
}

func (m *Model) renderThemesModal() string {
	var b strings.Builder
	current := m.app.Config().UI.Theme
	fmt.Fprintf(&b, "%s\n\n", renderMarkdown(fmt.Sprintf("Current theme: `%s`\n\nUse `up/down` to move, `d` to toggle descriptions, `Enter` or `Ctrl+S` to apply.", current), m.modal.Width))
	for i, theme := range themes {
		cursor := "  "
		if i == m.themeIndex {
			cursor = "> "
		}
		active := " "
		if theme.ID == current {
			active = "*"
		}
		row := fmt.Sprintf("%s[%s] %-18s %s  %s", cursor, active, theme.Name, themeSwatch(theme), theme.ID)
		if i == m.themeIndex {
			b.WriteString(selectedRowStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteByte('\n')
		if m.themeDetails && i == m.themeIndex {
			for _, line := range wrapPlain(theme.Description, max(12, m.modal.Width-6), "   ") {
				b.WriteString(mutedStyle.Render(line))
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func themeSwatch(theme themePalette) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.StatusFG)).Background(lipgloss.Color(theme.Accent)).Bold(true).Render(" A ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Text)).Background(lipgloss.Color(theme.Border)).Render(" B ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(theme.StatusFG)).Background(lipgloss.Color(theme.Accent2)).Render(" C ")
}

func (m *Model) syncThemesModalViewport() {
	m.keepModalLineVisible(m.selectedThemeLine())
}

func (m *Model) selectedThemeLine() int {
	line := 3 + m.themeIndex
	if m.themeDetails {
		line += m.themeIndex
	}
	return line
}
