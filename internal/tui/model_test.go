package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestInputHistoryNavigationRestoresDraft(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.rememberInput("/help")
	model.rememberInput("/ls")
	model.input.SetValue("draft message")

	model.historyPrev()
	if got := model.input.Value(); got != "/ls" {
		t.Fatalf("prev = %q", got)
	}
	model.historyPrev()
	if got := model.input.Value(); got != "/help" {
		t.Fatalf("second prev = %q", got)
	}
	model.historyNext()
	model.historyNext()
	if got := model.input.Value(); got != "draft message" {
		t.Fatalf("draft restored = %q", got)
	}
}

func TestPathCompletionForFilesWorkspace(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "workspace-target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("/files workspace " + filepath.Join(parent, "work"))
	model.completeInput()
	want := "/files workspace " + target + string(os.PathSeparator)
	if got := model.input.Value(); got != want {
		t.Fatalf("completion = %q, want %q", got, want)
	}
}

func TestPathCompletionForWorkspaceListCommand(t *testing.T) {
	config := app.DefaultConfig()
	config.WorkspaceDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(config.WorkspaceDir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	model := New(app.New(config))
	model.input.SetValue("/ls s")
	model.completeInput()
	if got, want := model.input.Value(), "/ls src"+string(os.PathSeparator); got != want {
		t.Fatalf("completion = %q, want %q", got, want)
	}
}

func TestPathCompletionPickerForMultipleDirectories(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"Users", "Windows"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("/files workspace " + parent + string(os.PathSeparator))

	model.completeInput()
	if !model.completionOpen {
		t.Fatal("completion picker did not open")
	}
	model.completionIndex = 0
	model.applyCompletion()
	if got := model.input.Value(); !strings.HasPrefix(got, "/files workspace "+parent) || !strings.HasSuffix(got, string(os.PathSeparator)) {
		t.Fatalf("selected completion = %q", got)
	}
}

func TestInlineHistorySuggestionCompletesFrequentCommand(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.rememberInput("/files workspace C:\\Users\\brada")
	model.rememberInput("/files workspace C:\\Users\\brada")
	model.input.SetValue("/files")
	model.updateInlineSuggestion()
	if got := model.inlineSuggestion; got != "/files workspace C:\\Users\\brada" {
		t.Fatalf("suggestion = %q", got)
	}
	model.completeInput()
	if got := model.input.Value(); got != "/files workspace C:\\Users\\brada" {
		t.Fatalf("completed = %q", got)
	}
}

func TestAttachmentTokensFromPastedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	got := attachmentTokensFromText(path)
	if len(got) != 1 {
		t.Fatal("expected pasted path to become attachment token")
	}
	want := attachmentToken(path)
	if got[0] != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
}

func TestNormalizeAttachmentTextPreservesQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wedding.jpg")
	if err := os.WriteFile(path, []byte("jpg"), 0600); err != nil {
		t.Fatal(err)
	}
	got, count := normalizeAttachmentText(path + " what's this a picture of?")
	if count != 1 {
		t.Fatalf("count = %d, text = %q", count, got)
	}
	if !strings.Contains(got, attachmentToken(path)) || !strings.Contains(got, "what's this a picture of?") {
		t.Fatalf("normalized = %q", got)
	}
}

func TestNormalizeInputAttachmentsConvertsDroppedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drop.png")
	if err := os.WriteFile(path, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	config := app.DefaultConfig()
	config.WorkspaceDir = t.TempDir()
	model := New(app.New(config))
	model.input.SetValue(path)
	model.normalizeInputAttachments()
	got := model.input.Value()
	if strings.Contains(got, path) {
		t.Fatalf("input still references original path: %q", got)
	}
	paths := appAttachmentPathsForTest(got)
	if len(paths) != 1 {
		t.Fatalf("expected one copied attachment path in %q", got)
	}
	if !strings.Contains(paths[0], filepath.Join(config.WorkspaceDir, ".nullbot", "attachments")) {
		t.Fatalf("attachment not copied into workspace attachments: %q", paths[0])
	}
	if data, err := os.ReadFile(paths[0]); err != nil || string(data) != "png" {
		t.Fatalf("copied data = %q, err = %v", data, err)
	}
}

func TestInsertAttachmentsCopiesClipboardFileIntoWorkspace(t *testing.T) {
	external := t.TempDir()
	source := filepath.Join(external, "List for Cruise.docx")
	if err := os.WriteFile(source, []byte("docx"), 0600); err != nil {
		t.Fatal(err)
	}
	config := app.DefaultConfig()
	config.WorkspaceDir = t.TempDir()
	model := New(app.New(config))
	if !model.insertAttachments([]pastedAttachment{{Kind: "file", Path: source, Name: filepath.Base(source)}}) {
		t.Fatal("insertAttachments returned false")
	}
	got := model.input.Value()
	if strings.Contains(got, source) {
		t.Fatalf("input still references original path: %q", got)
	}
	paths := appAttachmentPathsForTest(got)
	if len(paths) != 1 {
		t.Fatalf("expected one attachment token in %q", got)
	}
	if filepath.Base(paths[0]) != "List for Cruise.docx" {
		t.Fatalf("copied filename = %q", filepath.Base(paths[0]))
	}
}

func appAttachmentPathsForTest(text string) []string {
	re := regexp.MustCompile(`@file\("([^"]+)"\)|@file\(([^)]+)\)`)
	var paths []string
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		path := strings.TrimSpace(match[1])
		if path == "" && len(match) > 2 {
			path = strings.TrimSpace(match[2])
		}
		if unquoted, err := strconv.Unquote(`"` + strings.Trim(path, "\"") + `"`); err == nil {
			path = unquoted
		}
		paths = append(paths, strings.Trim(path, "\"'"))
	}
	return paths
}

func TestPasteProtectedEnterCapturesBlock(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("first line\nsecond line that is long enough")
	model.pasteProtectUntil = time.Now().Add(time.Second)
	next, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)
	if updated.input.Value() != "" || !strings.Contains(updated.pendingPaste, "second line") {
		t.Fatalf("input=%q pending=%q", updated.input.Value(), updated.pendingPaste)
	}
	if len(updated.messages) != 0 {
		t.Fatalf("enter submitted during paste guard: %#v", updated.messages)
	}
}

func TestPasteProtectedEnterCapturesSingleInjectedLine(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("first pasted line")
	model.pasteProtectUntil = time.Now().Add(time.Second)
	next, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)
	if updated.input.Value() != "" || !strings.Contains(updated.pendingPaste, "first pasted line") {
		t.Fatalf("input=%q pending=%q", updated.input.Value(), updated.pendingPaste)
	}
	if len(updated.messages) != 0 {
		t.Fatalf("line submitted during paste guard: %#v", updated.messages)
	}
}

func TestPasteProtectedEnterDoesNotCaptureSlashCommand(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("/config")
	model.pasteProtectUntil = time.Now().Add(time.Second)
	next, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)
	if updated.pendingPaste != "" {
		t.Fatalf("slash command captured as paste: %q", updated.pendingPaste)
	}
	if cmd == nil {
		t.Fatal("slash command did not produce submit command")
	}
}

func TestCapturePasteShowsChipWithoutInputText(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.capturePaste("alpha\nbeta\ngamma")
	if model.input.Value() != "" {
		t.Fatalf("paste rendered in input: %q", model.input.Value())
	}
	if !strings.Contains(model.pasteChip(), "Pasted +3 lines") {
		t.Fatalf("paste chip = %q", model.pasteChip())
	}
}

func TestEnterConvertsPastedBlockBeforeSubmit(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.input.SetValue("alpha\nbeta\ngamma delta epsilon zeta")
	next, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)
	if updated.pendingPaste == "" || updated.input.Value() != "" {
		t.Fatalf("pending=%q input=%q", updated.pendingPaste, updated.input.Value())
	}
}

func TestActivityToolRendererOmitsToolOutput(t *testing.T) {
	events := []activityEvent{
		{Time: now(), Command: "agent/read_file", Status: "tool start", Detail: `args: {"path":"README.md","max_bytes":2000}`},
		{Time: now(), Command: "agent/read_file", Status: "tool complete", Detail: `output: very noisy file contents`},
		{Time: now(), Command: "agent", Status: "agent complete", Detail: "Agent completed task."},
	}
	rendered := stripANSI(renderActivity(events, app.Reply{}, 80))
	if strings.Contains(rendered, "very noisy file contents") || strings.Contains(rendered, "output:") {
		t.Fatalf("activity leaked tool output:\n%s", rendered)
	}
	for _, want := range []string{"called", "read_file", "complete", "Agent completed task"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("activity missing %q:\n%s", want, rendered)
		}
	}
}

func TestLiveReasoningActivityAppearsInOutput(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 100
	model.height = 32
	model.resize()
	ch := make(chan app.ActivityRecord)
	close(ch)
	next, _ := model.Update(liveActivityMsg{
		Record: app.ActivityRecord{Time: now(), Kind: "reasoning", Name: "agent", Status: "reasoning", Detail: "checked constraints"},
		Ch:     ch,
	})
	updated := next.(Model)
	rendered := stripANSI(updated.output.View())
	if !strings.Contains(rendered, "REASONING") || !strings.Contains(rendered, "checked constraints") {
		t.Fatalf("output missing live reasoning:\n%s", rendered)
	}
}

func TestUsageModalRendersTabsAndChart(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 120
	model.height = 40
	model.resize()
	nowTime := now()
	reply := app.Reply{
		Message:   "Usage panel opened.",
		OpenPanel: "usage",
		Data: map[string]any{"usage": app.UsageSnapshot{
			Total:   app.UsageTotals{Requests: 1, InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.001},
			Session: app.UsageTotals{Requests: 1, InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.001},
			ByModel: []app.UsageModelSummary{{
				Provider: "openai",
				Model:    "gpt-5-mini",
				Totals:   app.UsageTotals{Requests: 1, TotalTokens: 150, CostUSD: 0.001},
			}},
			Daily: []app.UsageDailySummary{{
				Day:      nowTime.Format("2006-01-02"),
				Provider: "openai",
				Model:    "gpt-5-mini",
				Totals:   app.UsageTotals{TotalTokens: 150},
			}},
		}},
	}
	model.openUsageModal(reply)
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "Usage Summary") || !strings.Contains(rendered, "This Session") {
		t.Fatalf("usage summary missing:\n%s", rendered)
	}
	model.usageTab = 1
	model.modal.SetContent(model.renderUsageModal())
	rendered = stripANSI(model.modal.View())
	if !strings.Contains(rendered, "Daily Token Usage") {
		t.Fatalf("usage chart missing:\n%s", rendered)
	}
}

func TestEffortModalRendersFunnyLabels(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 100
	model.height = 36
	model.resize()
	model.openEffortModal(app.Reply{Message: "Effort panel opened."})
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "Empty Vessel") || !strings.Contains(rendered, "Big Brain") {
		t.Fatalf("effort labels missing:\n%s", rendered)
	}
}

func TestAgentsModalRendersTokensAndToolsWithoutOutput(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 120
	model.height = 40
	model.resize()
	model.openAgentsModal(app.Reply{
		Message: "Agents dashboard opened.",
		Data: map[string]any{"tasks": []app.AgentTask{{
			ID:      "task-0001",
			Name:    "Manager",
			Role:    "primary",
			Status:  app.TaskRunning,
			Current: "tool start",
			Tokens:  app.TaskTokens{Input: 10, Output: 5, CachedInput: 3, ReasoningOutput: 2},
			ToolCalls: []app.TaskToolCall{{
				Time:   now(),
				Name:   "read_file",
				Status: "tool complete",
				Detail: "output: secret file contents",
			}},
		}}},
	})
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "CACHE") || !strings.Contains(rendered, "THINK") || !strings.Contains(rendered, "Manager") {
		t.Fatalf("agents modal missing dashboard fields:\n%s", rendered)
	}
	if strings.Contains(rendered, "secret file contents") {
		t.Fatalf("agents modal leaked tool output:\n%s", rendered)
	}
}

func TestSkillsModalRendersReferences(t *testing.T) {
	model := New(app.New(app.DefaultConfig()))
	model.width = 110
	model.height = 40
	model.resize()
	model.openSkillsModal(app.Reply{
		Message: "Skills panel opened.",
		Data: map[string]any{"skills": []app.SkillSummary{{
			Name:        "multi",
			Description: "Multi tier skill.",
			Path:        "skills/multi/SKILL.md",
			References:  []app.SkillReference{{Path: "references/guide.md", Exists: true}},
		}}},
	})
	model.skillDetails = true
	model.modal.SetContent(model.renderSkillsModal())
	rendered := stripANSI(model.modal.View())
	if !strings.Contains(rendered, "multi") || !strings.Contains(rendered, "references/guide.md") {
		t.Fatalf("skills modal missing references:\n%s", rendered)
	}
}

func TestThemesModalRendersAndAppliesTheme(t *testing.T) {
	if len(themes) != 20 {
		t.Fatalf("theme count = %d", len(themes))
	}
	for _, theme := range themes {
		if theme.BannerTop == "" {
			t.Fatalf("theme %s missing banner top color", theme.ID)
		}
	}
	config := app.DefaultConfig()
	config.AppDir = t.TempDir()
	model := New(app.New(config))
	model.width = 120
	model.height = 40
	model.resize()
	model.openThemesModal()
	rendered := stripANSI(model.renderThemesModal())
	if !strings.Contains(rendered, "Classic NullBot") || !strings.Contains(rendered, "Neon Noir") {
		t.Fatalf("themes missing:\n%s", rendered)
	}
	model.themeIndex = themeIndexByID("neon")
	next, _, handled := model.handleThemesKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("theme key was not handled")
	}
	updated := next.(*Model)
	if got := updated.app.Config().UI.Theme; got != "neon" {
		t.Fatalf("theme = %q", got)
	}
}

func stripANSI(text string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
}
