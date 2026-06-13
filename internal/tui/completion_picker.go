package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type completionOption struct {
	Label string
	Value string
	IsDir bool
}

func (m *Model) openPathCompletionPicker() bool {
	value := m.input.Value()
	if value == "" || strings.Contains(value, "\n") {
		return false
	}
	prefix, pathPart, root, dirsOnly, ok := pathCompletionSpec(value, m.app.Config().WorkspaceDir)
	if !ok {
		return false
	}
	options, replacement, ok := pathCompletionOptions(root, pathPart, dirsOnly)
	if !ok {
		return false
	}
	if len(options) == 1 && replacement != pathPart {
		m.setInputValue(prefix + replacement)
		m.updateInlineSuggestion()
		return true
	}
	m.completionOpen = true
	m.completionPrefix = prefix
	m.completionOptions = options
	m.completionIndex = 0
	m.status = fmt.Sprintf("%d path suggestions. Enter selects, Esc closes.", len(options))
	return true
}

func (m *Model) handleCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.closeCompletion()
		return m, nil, true
	case "up", "shift+tab":
		m.completionIndex = max(0, m.completionIndex-1)
		return m, nil, true
	case "down", "tab":
		m.completionIndex = min(len(m.completionOptions)-1, m.completionIndex+1)
		return m, nil, true
	case "pgup":
		m.completionIndex = max(0, m.completionIndex-8)
		return m, nil, true
	case "pgdown":
		m.completionIndex = min(len(m.completionOptions)-1, m.completionIndex+8)
		return m, nil, true
	case "enter", "right":
		m.applyCompletion()
		return m, nil, true
	}
	return m, nil, false
}

func (m *Model) applyCompletion() {
	if !m.completionOpen || len(m.completionOptions) == 0 {
		return
	}
	option := m.completionOptions[m.completionIndex]
	m.setInputValue(m.completionPrefix + option.Value)
	m.closeCompletion()
	m.updateInlineSuggestion()
}

func (m *Model) closeCompletion() {
	m.completionOpen = false
	m.completionPrefix = ""
	m.completionOptions = nil
	m.completionIndex = 0
}

func (m Model) completionView() string {
	if !m.completionOpen || len(m.completionOptions) == 0 {
		return ""
	}
	width := min(max(34, m.width/3), max(34, m.width-6))
	start := max(0, m.completionIndex-5)
	end := min(len(m.completionOptions), start+10)
	if end-start < 10 {
		start = max(0, end-10)
	}
	var b strings.Builder
	b.WriteString(mutedStyle.Render("Path suggestions"))
	b.WriteByte('\n')
	for i := start; i < end; i++ {
		option := m.completionOptions[i]
		cursor := "  "
		style := lipgloss.NewStyle()
		if i == m.completionIndex {
			cursor = "> "
			style = selectedRowStyle
		}
		label := option.Label
		if option.IsDir && !strings.HasSuffix(label, string(os.PathSeparator)) {
			label += string(os.PathSeparator)
		}
		line := fmt.Sprintf("%s%s", cursor, label)
		b.WriteString(style.Width(width - 4).Render(line))
		b.WriteByte('\n')
	}
	b.WriteString(footerStyle.Render("↑/↓ move  Enter insert  Esc close"))
	return completionStyle.Width(width).Render(strings.TrimRight(b.String(), "\n"))
}

func pathCompletionOptions(root, input string, dirsOnly bool) ([]completionOption, string, bool) {
	dir, base, displayDir := completionDir(root, input)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", false
	}
	var options []completionOption
	for _, entry := range entries {
		if dirsOnly && !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}
		value := displayDir + name
		if entry.IsDir() {
			value += string(os.PathSeparator)
		}
		options = append(options, completionOption{Label: name, Value: value, IsDir: entry.IsDir()})
	}
	if len(options) == 0 {
		return nil, "", false
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].IsDir != options[j].IsDir {
			return options[i].IsDir
		}
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return options, longestCompletion(input, values), true
}

func (m *Model) updateInlineSuggestion() {
	value := m.input.Value()
	if strings.TrimSpace(value) == "" || strings.Contains(value, "\n") {
		m.inlineSuggestion = ""
		return
	}
	if suggestion := frequentCompletion(value, m.history); suggestion != "" {
		m.inlineSuggestion = suggestion
		return
	}
	for _, candidate := range completions(m.app.State()) {
		if strings.HasPrefix(candidate, value) && candidate != value {
			m.inlineSuggestion = candidate
			return
		}
	}
	m.inlineSuggestion = ""
}

func frequentCompletion(prefix string, history []string) string {
	type scored struct {
		value string
		score int
		last  int
	}
	scores := map[string]scored{}
	for i, item := range history {
		if !strings.HasPrefix(item, prefix) || item == prefix {
			continue
		}
		s := scores[item]
		s.value = item
		s.score++
		s.last = i
		scores[item] = s
	}
	var best scored
	for _, candidate := range scores {
		if candidate.score > best.score || (candidate.score == best.score && candidate.last > best.last) {
			best = candidate
		}
	}
	return best.value
}

func placeCompletion(width, height int, base, popup string) string {
	if popup == "" {
		return base
	}
	lines := strings.Split(base, "\n")
	boxLines := strings.Split(popup, "\n")
	top := max(0, height-len(boxLines)-7)
	left := 2
	if width > 90 {
		left = max(2, width/2-lipgloss.Width(popup)/2)
	}
	for i, line := range boxLines {
		idx := top + i
		if idx >= len(lines) {
			break
		}
		prefix := strings.Repeat(" ", min(left, max(0, width-1)))
		lines[idx] = prefix + line
	}
	return strings.Join(lines, "\n")
}
