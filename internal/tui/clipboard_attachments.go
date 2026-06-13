package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

type pastedAttachment struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Name string `json:"name"`
}

func (m *Model) pasteClipboard() (bool, error) {
	var attachmentErr error
	if runtime.GOOS == "windows" {
		if attachments, err := clipboardAttachments(m.app.Config().AppDir); err != nil {
			attachmentErr = err
		} else if m.insertAttachments(attachments) {
			return true, nil
		}
	}
	if text, err := clipboard.ReadAll(); err == nil && strings.TrimSpace(text) != "" {
		m.capturePaste(text)
		return true, nil
	}
	if runtime.GOOS == "windows" {
		return false, attachmentErr
	}
	attachments, err := clipboardAttachments(m.app.Config().AppDir)
	if err != nil {
		return false, err
	}
	if len(attachments) == 0 {
		return false, nil
	}
	return m.insertAttachments(attachments), nil
}

func (m *Model) insertAttachments(attachments []pastedAttachment) bool {
	var tokens []string
	for _, attachment := range attachments {
		if attachment.Path == "" {
			continue
		}
		tokens = append(tokens, attachmentToken(attachment.Path))
	}
	if len(tokens) == 0 {
		return false
	}
	m.insertPastedText(strings.Join(tokens, " "))
	m.status = fmt.Sprintf("Attached %d clipboard item(s).", len(tokens))
	return true
}

func (m *Model) insertPastedText(text string) {
	if m.selectAll {
		m.input.Reset()
		m.selectAll = false
	}
	if normalized, count := normalizeAttachmentText(text); count > 0 {
		text = normalized
		m.status = fmt.Sprintf("Attached %d file(s).", count)
	}
	m.input.InsertString(text)
	m.normalizeInputAttachments()
}

func (m *Model) capturePaste(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\x00")
	if text == "" {
		return
	}
	if normalized, count := normalizeAttachmentText(text); count > 0 {
		m.insertPastedText(normalized)
		m.status = fmt.Sprintf("Attached %d file(s).", count)
		return
	}
	if m.selectAll {
		m.input.Reset()
		m.pendingPaste = ""
		m.selectAll = false
	}
	m.pendingPaste = appendPendingPaste(m.pendingPaste, text)
	m.pasteNotice = m.pasteSummary()
	m.status = "Paste captured. Press Enter to send or keep typing."
}

func attachmentTokensFromText(text string) []string {
	var tokens []string
	for _, path := range attachmentPathsFromText(text) {
		tokens = append(tokens, attachmentToken(path))
	}
	return tokens
}

func normalizeAttachmentText(text string) (string, int) {
	if strings.Contains(text, "@file(") {
		return text, 0
	}
	paths := attachmentPathsFromText(text)
	normalized := text
	for _, path := range paths {
		normalized = strings.Replace(normalized, path, attachmentToken(path), 1)
	}
	return normalized, len(paths)
}

func (m *Model) normalizeInputAttachments() {
	value := m.input.Value()
	normalized, count := normalizeAttachmentText(value)
	if count == 0 || normalized == value {
		return
	}
	m.input.SetValue(normalized)
	m.input.CursorEnd()
	m.status = fmt.Sprintf("Attached %d file(s).", count)
}

func (m *Model) captureInputAsPasteIfNeeded() bool {
	value := m.input.Value()
	if m.pendingPaste != "" || !looksLikePastedBlock(value) {
		return false
	}
	m.input.Reset()
	m.capturePaste(value)
	return true
}

func (m *Model) capturePastedLineIfNeeded() bool {
	value := m.input.Value()
	if strings.TrimSpace(value) == "" || !m.pasteProtected() {
		return false
	}
	m.input.Reset()
	m.pendingPaste = appendPendingPaste(m.pendingPaste, value+"\n")
	m.pasteNotice = m.pasteSummary()
	m.status = "Paste captured. Press Enter after paste completes to send."
	m.pasteProtectUntil = time.Now().Add(350 * time.Millisecond)
	return true
}

func looksLikePastedBlock(value string) bool {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.Count(value, "\n") >= 1 && len(value) >= 24
}

func appendPendingPaste(existing, incoming string) string {
	if strings.TrimSpace(existing) == "" {
		return incoming
	}
	if strings.TrimSpace(incoming) == "" {
		return existing
	}
	return strings.TrimRight(existing, "\n") + "\n" + incoming
}

func combineInputAndPaste(input, paste string) string {
	input = strings.TrimSpace(input)
	paste = strings.TrimSpace(paste)
	if input == "" {
		return paste
	}
	if paste == "" {
		return input
	}
	return input + "\n\n" + paste
}

func (m Model) pasteSummary() string {
	lines := strings.Count(strings.TrimRight(m.pendingPaste, "\n"), "\n") + 1
	bytes := len(m.pendingPaste)
	if lines > 1 {
		return fmt.Sprintf("[Pasted +%d lines, %d bytes. Press Enter to send.]", lines, bytes)
	}
	return fmt.Sprintf("[Pasted text, %d bytes. Press Enter to send.]", bytes)
}

func (m Model) pasteChip() string {
	return selectedInputStyle.Render(m.pasteSummary()) + " " + mutedStyle.Render("Ctrl+Z clears")
}

func attachmentPathsFromText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if path, ok := attachmentPath(text); ok {
		return []string{path}
	}
	var paths []string
	for _, raw := range strings.Fields(text) {
		path := strings.Trim(raw, "\"'`")
		path = strings.TrimRight(path, ".,;:!?")
		if found, ok := attachmentPath(path); ok {
			paths = append(paths, found)
		}
	}
	return paths
}

func attachmentPath(path string) (string, bool) {
	if path == "" || strings.HasPrefix(path, "@file(") {
		return "", false
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return filepath.Clean(path), true
	}
	return "", false
}

func attachmentToken(path string) string {
	return fmt.Sprintf("@file(%q)", filepath.Clean(path))
}

func clipboardAttachments(appDir string) ([]pastedAttachment, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	dir := filepath.Join(appDir, "artifacts", "clipboard")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	script := windowsClipboardScript(dir)
	out, err := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(out))
	if text == "" || text == "null" {
		return nil, nil
	}
	var many []pastedAttachment
	if err := json.Unmarshal([]byte(text), &many); err == nil {
		return many, nil
	}
	var one pastedAttachment
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		return nil, err
	}
	return []pastedAttachment{one}, nil
}

func windowsClipboardScript(dir string) string {
	dir = strings.ReplaceAll(dir, "'", "''")
	return `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$out = @()
if ([System.Windows.Forms.Clipboard]::ContainsFileDropList()) {
  foreach ($p in [System.Windows.Forms.Clipboard]::GetFileDropList()) {
    $out += [pscustomobject]@{ kind = 'file'; path = [string]$p; name = [System.IO.Path]::GetFileName([string]$p) }
  }
}
if ([System.Windows.Forms.Clipboard]::ContainsImage()) {
  $dir = '` + dir + `'
  [System.IO.Directory]::CreateDirectory($dir) | Out-Null
  $path = Join-Path $dir ('clipboard-' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff') + '.png')
  $img = [System.Windows.Forms.Clipboard]::GetImage()
  $img.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
  $out += [pscustomobject]@{ kind = 'image'; path = [string]$path; name = [System.IO.Path]::GetFileName([string]$path) }
}
if ($out.Count -gt 0) { $out | ConvertTo-Json -Compress }
`
}
