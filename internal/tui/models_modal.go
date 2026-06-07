package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func (m *Model) openModelsModal() {
	m.panel = "models"
	m.mode = ModeModal
	m.input.Blur()
	m.modelGroups = app.DiscoverModelGroups(context.Background(), m.app.Config())
	m.modelOptions = app.FlattenModelGroups(m.modelGroups)
	m.modelIndex = app.CurrentModelIndexIn(m.app.Config(), m.modelOptions)
	m.modal.SetContent(m.renderModelsModal())
	m.syncModelsModalViewport()
}

func (m *Model) handleModelsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "models" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.modelIndex = max(0, m.modelIndex-1)
	case "down", "j":
		m.modelIndex = min(len(m.modelOptions)-1, m.modelIndex+1)
	case "enter", "s":
		if len(m.modelOptions) == 0 {
			return m, nil, true
		}
		option := m.modelOptions[m.modelIndex]
		err := m.app.UpdateConfig(func(config *app.Config) {
			config.Model.Provider = option.Provider
			config.Model.Model = option.ID
			config.Agent.UseResponses = option.Responses
		})
		if err != nil {
			m.status = "Model save failed: " + err.Error()
		} else {
			m.status = "Model set to " + option.Name
			m.events = append(m.events, activityEvent{Time: now(), Command: "/models", Status: "selected " + option.ID})
		}
	}
	m.modal.SetContent(m.renderModelsModal())
	m.syncModelsModalViewport()
	return m, nil, true
}

func (m *Model) renderModelsModal() string {
	var b strings.Builder
	config := m.app.Config()
	b.WriteString(renderMarkdown(fmt.Sprintf("Current: `%s / %s`\n\nUse `up`/`down`. `Enter` or `s` selects and saves. Models refresh from providers when keys are set.", config.Model.Provider, config.Model.Model), m.modal.Width))
	b.WriteString("\n\n")
	flat := 0
	for _, group := range m.modelGroups {
		b.WriteString(renderMarkdown("### "+strings.ToUpper(group.Provider), m.modal.Width))
		b.WriteByte('\n')
		if group.Error != "" {
			for _, line := range wrapPlain("discovery failed: "+group.Error, max(12, m.modal.Width-4), "  ") {
				fmt.Fprintf(&b, "%s\n", line)
			}
		}
		for _, option := range group.Models {
			cursor := "  "
			if flat == m.modelIndex {
				cursor = "> "
			}
			keyState := "missing key"
			if keyAvailable(m.app.Config(), option) {
				keyState = "key set"
			}
			nameW := min(30, max(12, m.modal.Width/3))
			fmt.Fprintf(&b, "%s%-*s %s [%s]\n", cursor, nameW, option.Name, option.ID, keyState)
			if flat == m.modelIndex {
				for _, line := range wrapPlain(option.Description, max(12, m.modal.Width-5), "   ") {
					fmt.Fprintf(&b, "%s\n", mutedStyle.Render(line))
				}
			}
			flat++
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) syncModelsModalViewport() {
	m.keepModalLineVisible(m.selectedModelLine())
}

func (m *Model) selectedModelLine() int {
	line := 5
	flat := 0
	for _, group := range m.modelGroups {
		line++
		if group.Error != "" {
			line++
		}
		for range group.Models {
			if flat == m.modelIndex {
				return line
			}
			line++
			flat++
		}
		line++
	}
	return line
}

func keyAvailable(config app.Config, option app.ModelOption) bool {
	keys, _ := app.LoadAPIKeys(config)
	if keys.ForProvider(option.Provider) != "" {
		return true
	}
	return os.Getenv(option.APIKeyEnv) != ""
}
