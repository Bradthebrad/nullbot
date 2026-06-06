package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func TestViewShowsTitleAndFitsHeight(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	rendered := updated.(Model).View()
	if !strings.Contains(rendered, "NullBot") {
		t.Fatalf("rendered view missing title:\n%s", rendered)
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) > 30 {
		t.Fatalf("rendered too many lines: got %d want <= 30", len(lines))
	}
}

func TestSanitizeConfigValueRemovesNulls(t *testing.T) {
	got := sanitizeConfigValue("\x00sk-test\x00\r\n")
	if got != "sk-test" {
		t.Fatalf("sanitized = %q", got)
	}
}
