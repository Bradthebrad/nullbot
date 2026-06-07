package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func TestViewShowsTitleAndFitsHeight(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	rendered := updated.(Model).View()
	if !strings.Contains(rendered, "It's just a client") {
		t.Fatalf("rendered view missing header tagline:\n%s", rendered)
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) > 30 {
		t.Fatalf("rendered too many lines: got %d want <= 30", len(lines))
	}
}

func TestBlockTitleComposesCustomBrand(t *testing.T) {
	rendered := blockTitle("BradBot")
	lines := strings.Split(rendered, "\n")
	if len(lines) != bannerHeight {
		t.Fatalf("banner height = %d", len(lines))
	}
	if strings.Contains(rendered, "BradBot") {
		t.Fatalf("banner should render block glyphs, got plain text:\n%s", rendered)
	}
	if strings.TrimSpace(rendered) == "" {
		t.Fatalf("banner is empty")
	}
	compact := compactBlockTitle("BradBot")
	if compact == rendered || strings.TrimSpace(compact) == "" {
		t.Fatalf("compact banner should be non-empty and distinct")
	}
}

func TestStyledBlockTitleAddsLayeredShadow(t *testing.T) {
	rendered := styledBlockTitle("Bot")
	lines := strings.Split(rendered, "\n")
	if len(lines) != bannerHeight+1 {
		t.Fatalf("styled banner height = %d", len(lines))
	}
	if strings.TrimSpace(stripANSI(rendered)) == "" {
		t.Fatalf("styled banner visible text is empty")
	}
}

func TestSanitizeConfigValueRemovesNulls(t *testing.T) {
	got := sanitizeConfigValue("\x00sk-test\x00\r\n")
	if got != "sk-test" {
		t.Fatalf("sanitized = %q", got)
	}
}

func TestRenderMarkdownLiteWrapsLongParagraph(t *testing.T) {
	rendered := renderMarkdownLite("This is a very long model response line that should wrap inside the output viewport instead of running through the right border.", 32)
	for _, line := range strings.Split(rendered, "\n") {
		if len(line) > 48 {
			t.Fatalf("line did not wrap: %q", line)
		}
	}
}

func TestRenderMarkdownUnwrapsWholeMarkdownFence(t *testing.T) {
	input := "```markdown\n# More Markdown Tests\n\n**Bold text**\n\n- One\n- Two\n\n```python\ndef add(a, b):\n    return a + b\n```\n```\n\nTrailing note."
	rendered := renderMarkdown(input, 80)
	plain := stripANSI(rendered)
	if strings.Contains(rendered, "```markdown") || strings.Contains(rendered, "**Bold text**") {
		t.Fatalf("markdown was not rendered:\n%s", rendered)
	}
	if !strings.Contains(plain, "More Markdown Tests") || !strings.Contains(plain, "Bold text") || !strings.Contains(plain, "def add") || !strings.Contains(plain, "Trailing note") {
		t.Fatalf("rendered output missing markdown content:\n%s", rendered)
	}
}

func TestRenderMarkdownDoesNotShowHeadingMarkers(t *testing.T) {
	rendered := renderMarkdown("### Heading\n\nThis is **bold**.", 80)
	plain := strings.TrimSpace(stripANSI(rendered))
	if strings.Contains(plain, "### Heading") || strings.Contains(plain, "**bold**") {
		t.Fatalf("markdown markers leaked:\n%s", rendered)
	}
	if !strings.Contains(plain, "Heading") || !strings.Contains(plain, "bold") {
		t.Fatalf("rendered output missing content:\n%s", rendered)
	}
}

func TestRenderFullActivityUsesMarkdownPresentation(t *testing.T) {
	rendered := renderFullActivity([]activityEvent{{
		Time:   now(),
		Input:  "tell me **bold** things with a long enough line that it should wrap inside the activity popup nicely",
		Status: "completed",
		Detail: "Rendered **detail**",
	}}, 48)
	plain := stripANSI(rendered)
	if strings.Contains(plain, "### ") || strings.Contains(plain, "**detail**") {
		t.Fatalf("activity markdown markers leaked:\n%s", rendered)
	}
	if !strings.Contains(plain, "detail") {
		t.Fatalf("activity missing detail:\n%s", rendered)
	}
}

func TestMarketModalRendersInteractiveRows(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 100
	model.height = 36
	model.resize()
	model.openMarketModal(app.Reply{
		Message: "Market panel opened.",
		Data: map[string]any{"packages": []app.MarketPackage{{
			ID:          "nullbot-code-mcp",
			Kind:        "mcp_server",
			Description: "Coding tools",
			Status:      "available",
		}}},
	})
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "[ ]  nullbot-code-mcp") {
		t.Fatalf("market modal missing selectable row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PACKAGE") || !strings.Contains(rendered, "DESCRIPTION") {
		t.Fatalf("market modal missing table headers:\n%s", rendered)
	}
	if !strings.Contains(strings.ToLower(rendered), "click") {
		t.Fatalf("market modal missing interaction hint:\n%s", rendered)
	}
}

func TestMCPModalRendersInteractiveRows(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 100
	model.height = 36
	model.resize()
	model.openMCPModal(app.Reply{
		Message: "MCP panel opened.",
		Data: map[string]any{"packages": []app.MarketPackage{{
			ID:          "nullbot-code-mcp",
			Kind:        "mcp_server",
			Description: "Coding tools",
			Installed:   true,
			Enabled:     true,
		}}},
	})
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "nullbot-code-mcp") || !strings.Contains(rendered, "enabled") {
		t.Fatalf("mcp modal missing interactive row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PACKAGE") || !strings.Contains(rendered, "DESCRIPTION") {
		t.Fatalf("mcp modal missing table headers:\n%s", rendered)
	}
	if !strings.Contains(strings.ToLower(rendered), "click") {
		t.Fatalf("mcp modal missing interaction hint:\n%s", rendered)
	}
}

func stripANSI(text string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
}
