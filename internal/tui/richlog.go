package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type logEntry struct {
	Title string
	Body  string
	Style lipgloss.Style
}

func renderRichLog(entries []logEntry, width int) string {
	if len(entries) == 0 {
		return mutedStyle.Render("No messages yet. Type /help to start.")
	}
	contentW := max(8, width-1)
	var b strings.Builder
	for i, entry := range entries {
		if strings.TrimSpace(entry.Title) != "" {
			b.WriteString(entry.Style.Render(entry.Title))
			b.WriteByte('\n')
		}
		b.WriteString(renderMarkdown(entry.Body, contentW))
		if i < len(entries)-1 {
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func renderMarkdown(text string, width int) string {
	width = max(8, width)
	text = unwrapMarkdownFence(text)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(nullbotMarkdownStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return renderMarkdownLite(text, width)
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return renderMarkdownLite(text, width)
	}
	return strings.TrimSpace(rendered)
}

func nullbotMarkdownStyle() ansi.StyleConfig {
	style := glamstyles.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""

	style.H1.Prefix = ""
	style.H1.Suffix = ""
	style.H1.BackgroundColor = strPtr(currentThemePalette.CodeBG)
	style.H1.Color = strPtr(currentThemePalette.Accent)
	style.H1.Bold = boolPtr(true)
	style.H2.Prefix = ""
	style.H2.Color = strPtr(currentThemePalette.Accent2)
	style.H2.Bold = boolPtr(true)
	style.H3.Prefix = ""
	style.H3.Color = strPtr(currentThemePalette.Accent)
	style.H3.Bold = boolPtr(true)
	style.H4.Prefix = ""
	style.H5.Prefix = ""
	style.H6.Prefix = ""

	style.CodeBlock.Margin = &zero
	style.CodeBlock.Color = strPtr(currentThemePalette.CodeFG)
	style.CodeBlock.BackgroundColor = strPtr(currentThemePalette.CodeBG)
	style.Code.Color = strPtr(currentThemePalette.CodeFG)
	style.Code.BackgroundColor = strPtr(currentThemePalette.CodeBG)
	style.BlockQuote.Color = strPtr(currentThemePalette.InputBorder)
	style.List.Color = strPtr(currentThemePalette.Text)
	centerSep := " | "
	columnSep := " | "
	rowSep := "-"
	style.Table.CenterSeparator = &centerSep
	style.Table.ColumnSeparator = &columnSep
	style.Table.RowSeparator = &rowSep
	return style
}

func strPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func unwrapMarkdownFence(text string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 || !isMarkdownFence(lines[0]) {
		return text
	}
	end := -1
	for i := len(lines) - 1; i > 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	if end < 0 {
		return text
	}
	body := append([]string{}, lines[1:end]...)
	body = append(body, lines[end+1:]...)
	return strings.TrimSpace(strings.Join(body, "\n"))
}

func isMarkdownFence(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "```markdown", "```md":
		return true
	default:
		return false
	}
}

func renderMarkdownLite(text string, width int) string {
	width = max(8, width)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	var paragraph []string
	inCode := false

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out = append(out, wrapStyled(strings.Join(paragraph, " "), width, ""))
		paragraph = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			inCode = !inCode
			continue
		}
		if inCode {
			for _, wrapped := range wrapRawLine(line, width-2, "  ") {
				out = append(out, codeStyle.Render(wrapped))
			}
			continue
		}
		if trimmed == "" {
			flushParagraph()
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			flushParagraph()
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out = append(out, headingStyle.Render(wrapSingleLine(title, width)))
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out = append(out, wrapStyled(quote, width-2, "| "))
			continue
		}
		if bullet, ok := parseBullet(trimmed); ok {
			flushParagraph()
			out = append(out, wrapStyled(bullet, width-2, "- "))
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseBullet(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

func wrapStyled(text string, width int, prefix string) string {
	lines := wrapPlain(text, max(4, width-runewidth.StringWidth(prefix)), prefix)
	for i, line := range lines {
		if strings.HasPrefix(prefix, "-") {
			lines[i] = bulletStyle.Render(line)
		}
		if strings.HasPrefix(prefix, "|") {
			lines[i] = quoteStyle.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func wrapPlain(text string, width int, prefix string) []string {
	width = max(4, width)
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{prefix}
	}
	var lines []string
	current := prefix
	currentW := runewidth.StringWidth(prefix)
	continuation := strings.Repeat(" ", runewidth.StringWidth(prefix))
	for _, word := range words {
		wordW := runewidth.StringWidth(word)
		sep := ""
		sepW := 0
		if strings.TrimSpace(current) != "" && current != prefix && current != continuation {
			sep = " "
			sepW = 1
		}
		if currentW+sepW+wordW > width && strings.TrimSpace(current) != "" {
			lines = append(lines, current)
			current = continuation + word
			currentW = runewidth.StringWidth(current)
			continue
		}
		current += sep + word
		currentW += sepW + wordW
	}
	if strings.TrimSpace(current) != "" {
		lines = append(lines, current)
	}
	return lines
}

func wrapSingleLine(text string, width int) string {
	lines := wrapPlain(text, width, "")
	return strings.Join(lines, "\n")
}

func wrapRawLine(text string, width int, prefix string) []string {
	width = max(4, width)
	if text == "" {
		return []string{prefix}
	}
	var lines []string
	current := prefix
	currentW := runewidth.StringWidth(prefix)
	for _, r := range text {
		next := string(r)
		nextW := runewidth.StringWidth(next)
		if currentW+nextW > width && current != prefix {
			lines = append(lines, current)
			current = prefix + next
			currentW = runewidth.StringWidth(current)
			continue
		}
		current += next
		currentW += nextW
	}
	if current != prefix {
		lines = append(lines, current)
	}
	return lines
}
