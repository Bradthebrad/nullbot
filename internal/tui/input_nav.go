package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (m *Model) rememberInput(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != value {
		m.history = append(m.history, value)
	}
	m.historyAt = len(m.history)
	m.draft = ""
}

func (m *Model) historyPrev() {
	if len(m.history) == 0 {
		return
	}
	if m.historyAt < 0 || m.historyAt > len(m.history) {
		m.historyAt = len(m.history)
	}
	if m.historyAt == len(m.history) {
		m.draft = m.input.Value()
	}
	if m.historyAt > 0 {
		m.historyAt--
	}
	m.setInputValue(m.history[m.historyAt])
	m.updateInlineSuggestion()
}

func (m *Model) historyNext() {
	if len(m.history) == 0 || m.historyAt < 0 {
		return
	}
	if m.historyAt < len(m.history)-1 {
		m.historyAt++
		m.setInputValue(m.history[m.historyAt])
		m.updateInlineSuggestion()
		return
	}
	m.historyAt = len(m.history)
	m.setInputValue(m.draft)
	m.draft = ""
	m.updateInlineSuggestion()
}

func (m *Model) setInputValue(value string) {
	m.input.SetValue(value)
	m.input.CursorEnd()
}

func (m Model) inputCursorAtEnd() bool {
	if m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if len(lines) == 0 {
		return true
	}
	line := lines[min(m.input.Line(), len(lines)-1)]
	info := m.input.LineInfo()
	return info.StartColumn+info.ColumnOffset >= len([]rune(line))
}

func (m Model) inputAtFirstVisualLine() bool {
	if m.input.Line() != 0 {
		return false
	}
	info := m.input.LineInfo()
	return info.RowOffset <= 0
}

func (m Model) inputAtLastVisualLine() bool {
	if m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	info := m.input.LineInfo()
	return info.RowOffset+1 >= max(1, info.Height)
}

func (m *Model) inputPageUp() {
	for i := 0; i < max(1, m.input.LineCount()/2); i++ {
		m.input.CursorUp()
	}
}

func (m *Model) inputPageDown() {
	for i := 0; i < max(1, m.input.LineCount()/2); i++ {
		m.input.CursorDown()
	}
}

func (m *Model) completePathInput() bool {
	value := m.input.Value()
	if value == "" || strings.Contains(value, "\n") {
		return false
	}
	prefix, pathPart, root, dirsOnly, ok := pathCompletionSpec(value, m.app.Config().WorkspaceDir)
	if !ok {
		return false
	}
	completed, ok := completePath(root, pathPart, dirsOnly)
	if !ok || completed == pathPart {
		return false
	}
	m.setInputValue(prefix + completed)
	m.updateInlineSuggestion()
	return true
}

func pathCompletionSpec(value, workspace string) (prefix, pathPart, root string, dirsOnly bool, ok bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	if value == "/files workspace" || strings.HasPrefix(value, "/files workspace ") {
		return "/files workspace ", strings.TrimLeft(strings.TrimPrefix(value, "/files workspace"), " "), "", true, true
	}
	for _, cmd := range []string{"/ls", "/dir", "/rm", "/rmdir"} {
		if value == cmd {
			return cmd + " ", "", workspace, cmd != "/rm", true
		}
		if strings.HasPrefix(value, cmd+" ") {
			return cmd + " ", strings.TrimPrefix(value, cmd+" "), workspace, cmd != "/rm", true
		}
	}
	return "", "", "", false, false
}

func completePath(root, input string, dirsOnly bool) (string, bool) {
	dir, base, displayDir := completionDir(root, input)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var matches []string
	for _, entry := range entries {
		if dirsOnly && !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}
		if entry.IsDir() {
			name += string(os.PathSeparator)
		}
		matches = append(matches, displayDir+name)
	}
	if len(matches) == 0 {
		return "", false
	}
	sort.Strings(matches)
	return longestCompletion(input, matches), true
}

func completionDir(root, input string) (dir, base, displayDir string) {
	if root == "" {
		root = "."
	}
	if input == "" {
		return root, "", ""
	}
	if strings.HasSuffix(input, string(os.PathSeparator)) || strings.HasSuffix(input, "/") || strings.HasSuffix(input, "\\") {
		dir = input
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		return dir, "", input
	}
	displayDir = filepath.Dir(input)
	if displayDir == "." {
		displayDir = ""
	} else if !strings.HasSuffix(displayDir, string(os.PathSeparator)) {
		displayDir += string(os.PathSeparator)
	}
	dir = filepath.Dir(input)
	if dir == "." {
		dir = root
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return dir, filepath.Base(input), displayDir
}

func longestCompletion(input string, matches []string) string {
	if len(matches) == 1 {
		return matches[0]
	}
	common := matches[0]
	for _, match := range matches[1:] {
		common = commonPrefix(common, match)
	}
	if len(common) > len(input) {
		return common
	}
	return input
}

func commonPrefix(a, b string) string {
	ar := []rune(a)
	br := []rune(b)
	i := 0
	for i < len(ar) && i < len(br) && ar[i] == br[i] {
		i++
	}
	return string(ar[:i])
}
