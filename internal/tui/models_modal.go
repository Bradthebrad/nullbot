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
	m.modal.GotoTop()
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
	return m, nil, true
}

func (m *Model) renderModelsModal() string {
	var b strings.Builder
	config := m.app.Config()
	fmt.Fprintf(&b, "Current: %s / %s\n", config.Model.Provider, config.Model.Model)
	b.WriteString("Use up/down. Enter or s selects and saves. Models refresh from providers when keys are set.\n\n")
	flat := 0
	for _, group := range m.modelGroups {
		fmt.Fprintf(&b, "[%s]\n", strings.ToUpper(group.Provider))
		if group.Error != "" {
			fmt.Fprintf(&b, "  discovery failed: %s\n", group.Error)
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
			fmt.Fprintf(&b, "%s%-30s %s [%s]\n", cursor, option.Name, option.ID, keyState)
			if flat == m.modelIndex {
				fmt.Fprintf(&b, "   %s\n", option.Description)
			}
			flat++
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func keyAvailable(config app.Config, option app.ModelOption) bool {
	keys, _ := app.LoadAPIKeys(config)
	if keys.ForProvider(option.Provider) != "" {
		return true
	}
	return os.Getenv(option.APIKeyEnv) != ""
}
