package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"yourbot/internal/app"
)

type Mode int

const (
	ModeChat Mode = iota
	ModeModal
	ModePlanEdit
)

type Model struct {
	app       *app.App
	input     textarea.Model
	selectAll bool
	history   []string
	historyAt int
	draft     string
	output    viewport.Model
	activity  viewport.Model
	modal     viewport.Model
	width     int
	height    int
	mode      Mode
	panel     string
	messages  []app.Message
	events    []activityEvent
	status    string
	busy      bool
	spinner   spinner.Model
	planEdit  textarea.Model

	configFields    []configField
	configIndex     int
	configEditing   bool
	configEditValue string
	modelOptions    []app.ModelOption
	modelGroups     []app.ModelGroup
	modelIndex      int

	marketPackages []app.MarketPackage
	marketIndex    int
	marketSelected map[string]bool
	marketDetails  bool

	mcpPackages []app.MarketPackage
	mcpIndex    int
	mcpDetails  bool
}

type replyMsg app.Reply
type alsoReplyMsg app.Reply
type liveActivityMsg struct {
	Record app.ActivityRecord
	Ch     <-chan app.ActivityRecord
}
type liveActivityDoneMsg struct{}

type activityEvent struct {
	Time    time.Time
	Input   string
	Command string
	Panel   string
	Status  string
	Detail  string
}

func New(a *app.App) Model {
	input := textarea.New()
	input.Placeholder = "Message NullBot or type /help"
	input.Prompt = "| "
	input.CharLimit = 0
	input.SetHeight(3)
	input.Focus()

	planEdit := textarea.New()
	planEdit.Placeholder = "Write or edit the plan..."
	planEdit.Prompt = "| "
	planEdit.CharLimit = 0
	planEdit.SetHeight(10)

	state := a.State()
	spin := spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: []string{"*", "o", "O", "o"},
		FPS:    time.Second / 8,
	}), spinner.WithStyle(spinnerStyle))
	return Model{
		app:       a,
		input:     input,
		historyAt: -1,
		output:    viewport.New(20, 10),
		activity:  viewport.New(20, 10),
		modal:     viewport.New(20, 10),
		planEdit:  planEdit,
		messages:  state.History,
		events: []activityEvent{
			{Time: time.Now(), Status: "NullBot started", Detail: "Press /help for commands."},
			{Time: time.Now(), Status: "Shortcuts ready", Detail: "Ctrl+Q quit, Ctrl+O full activity, Ctrl+J newline."},
		},
		status:  "ready",
		spinner: spin,
	}
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case replyMsg:
		reply := app.Reply(msg)
		m.busy = false
		m.applyReply(reply)
		if text, ok := reply.Data["copy"].(string); ok {
			if err := clipboard.WriteAll(text); err != nil {
				m.status = "Copy failed: " + err.Error()
			}
		}
		m.refreshContent(reply)
		if reply.OpenPanel != "" {
			m.openModal(reply.OpenPanel, reply)
		}
		return m, nil
	case alsoReplyMsg:
		reply := app.Reply(msg)
		m.applyReply(reply)
		m.refreshContent(reply)
		m.openModal("also", reply)
		return m, nil
	case liveActivityMsg:
		m.events = append(m.events, activityEventFromRecord(msg.Record))
		m.refreshContent(app.Reply{})
		return m, waitActivity(msg.Ch)
	case liveActivityDoneMsg:
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		if m.busy {
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}

	var cmd tea.Cmd
	if m.mode == ModePlanEdit {
		m.planEdit, cmd = m.planEdit.Update(msg)
		return m, cmd
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting NullBot..."
	}
	header := m.headerView()
	main := lipgloss.JoinHorizontal(lipgloss.Top, m.outputPanel(), m.activityPanel())
	status := m.statusView()
	input := inputBoxStyle.Width(max(1, m.width-2)).Render(m.inputView())
	view := lipgloss.JoinVertical(lipgloss.Left, header, main, status, input)
	if m.mode == ModeModal || m.mode == ModePlanEdit {
		return placeModal(m.width, m.height, view, m.modalView())
	}
	return view
}

func (m Model) headerView() string {
	name := "NULLBOT"
	state := m.app.State()
	if state.Config.BrandPrefix != "" {
		name = strings.ToUpper(state.Config.BrandPrefix + "Bot")
	}
	tagline := state.Config.Tagline
	if tagline == "" {
		tagline = "It's just a client - no magic here."
	}
	banner := styledBlockTitle(name)
	lines := strings.Split(banner, "\n")
	if bannerTooWide(lines, m.width) {
		lines = strings.Split(styledCompactBlockTitle(name), "\n")
	}
	if bannerTooWide(lines, m.width) {
		lines = []string{blockShadowStyle.Render(name)}
	}
	for i, line := range lines {
		lines[i] = centerText(line, m.width)
	}
	tag := headerStyle.Width(m.width).Render(centerText(tagline, m.width))
	return lipgloss.JoinVertical(lipgloss.Left, append(lines, tag)...)
}

func bannerTooWide(lines []string, width int) bool {
	for _, line := range lines {
		if lipgloss.Width(line) > width {
			return true
		}
	}
	return false
}

func (m Model) inputView() string {
	if !m.selectAll {
		return m.input.View()
	}
	value := m.input.Value()
	if value == "" {
		return m.input.View()
	}
	return selectedInputStyle.Render(value)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == ModePlanEdit {
		switch msg.String() {
		case "esc":
			m.mode = ModeModal
			m.input.Focus()
			return m, nil
		case "ctrl+s":
			m.app.SetPlan(m.planEdit.Value())
			m.mode = ModeModal
			reply := app.Reply{Message: "Plan saved.", OpenPanel: "plan", Data: map[string]any{"plan": m.app.Plan()}}
			m.openModal("plan", reply)
			return m, nil
		}
		var cmd tea.Cmd
		m.planEdit, cmd = m.planEdit.Update(msg)
		return m, cmd
	}

	if m.mode == ModeModal {
		if next, cmd, handled := m.handleConfigKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleModelsKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleMarketKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleMCPKey(msg); handled {
			return next, cmd
		}
		switch msg.String() {
		case "esc", "q":
			m.mode = ModeChat
			m.input.Focus()
			return m, nil
		case "up", "k":
			m.modal.LineUp(1)
			return m, nil
		case "down", "j":
			m.modal.LineDown(1)
			return m, nil
		case "home":
			m.modal.GotoTop()
			return m, nil
		case "end":
			m.modal.GotoBottom()
			return m, nil
		case "e":
			if m.panel == "plan" {
				m.mode = ModePlanEdit
				m.planEdit.SetValue(m.app.Plan())
				m.planEdit.Focus()
				return m, textarea.Blink
			}
		case "ctrl+o":
			m.openActivityModal()
			return m, nil
		}
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "f1":
		return m, m.submit("/help")
	case "ctrl+c":
		if m.busy {
			m.app.RequestPause()
			m.status = "Pause requested."
			m.events = append(m.events, activityEvent{Time: time.Now(), Command: "/pause", Status: "pause requested"})
			m.refreshContent(app.Reply{})
			return m, nil
		}
		return m, m.submit("/pause")
	case "ctrl+v":
		if text, err := clipboard.ReadAll(); err == nil {
			if m.selectAll {
				m.input.Reset()
				m.selectAll = false
			}
			m.input.InsertString(text)
		} else {
			m.status = "Paste failed: " + err.Error()
		}
		return m, nil
	case "ctrl+a":
		m.selectAll = true
		m.status = "Input selected. Type or paste to replace."
		return m, nil
	case "home":
		m.input.SetCursor(0)
		m.selectAll = false
		return m, nil
	case "end":
		m.input.SetCursor(len(m.input.Value()))
		m.selectAll = false
		return m, nil
	case "ctrl+z":
		m.input.Reset()
		m.selectAll = false
		m.status = "Input cleared."
		return m, nil
	case "ctrl+q":
		return m, tea.Quit
	case "ctrl+o":
		m.openActivityModal()
		return m, nil
	case "ctrl+k":
		m.messages = nil
		m.output.SetContent(mutedStyle.Render("Output cleared. History is still available with /history."))
		m.status = "Output area cleared."
		return m, nil
	case "ctrl+l":
		m.events = nil
		m.activity.SetContent(mutedStyle.Render("Activity panel cleared."))
		m.status = "Activity panel cleared."
		return m, nil
	case "enter":
		if m.input.Value() == "" {
			return m, nil
		}
		value := m.input.Value()
		m.rememberInput(value)
		m.input.Reset()
		m.selectAll = false
		return m, m.submit(value)
	case "ctrl+j":
		m.input.InsertString("\n")
		return m, nil
	case "tab":
		m.completeInput()
		return m, nil
	case "right":
		m.completeInput()
		return m, nil
	case "up":
		if m.input.Line() == 0 {
			m.historyPrev()
			return m, nil
		}
	case "down":
		if m.input.Line() >= m.input.LineCount()-1 {
			m.historyNext()
			return m, nil
		}
	case "ctrl+h":
		return m, m.submit("/history")
	case "ctrl+p":
		return m, m.submit("/plan")
	case "pgup":
		m.output.PageUp()
		return m, nil
	case "pgdown":
		m.output.PageDown()
		return m, nil
	case "shift+up":
		m.activity.LineUp(3)
		return m, nil
	case "shift+down":
		m.activity.LineDown(3)
		return m, nil
	}

	var cmd tea.Cmd
	if m.selectAll && isReplacingKey(msg) {
		m.input.Reset()
		m.selectAll = false
	}
	if isReplacingKey(msg) {
		m.historyAt = len(m.history)
		m.draft = ""
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.MouseLeft && m.mode == ModeModal {
		if next, cmd, handled := m.handleMarketMouse(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleMCPMouse(msg); handled {
			return next, cmd
		}
	}
	if msg.Type != tea.MouseWheelUp && msg.Type != tea.MouseWheelDown {
		return m, nil
	}
	delta := 3
	if msg.Type == tea.MouseWheelUp {
		delta = -3
	}
	if m.mode == ModeModal || m.mode == ModePlanEdit {
		if delta < 0 {
			m.modal.LineUp(-delta)
		} else {
			m.modal.LineDown(delta)
		}
		return m, nil
	}
	if m.mouseInActivity(msg) {
		if delta < 0 {
			m.activity.LineUp(-delta)
		} else {
			m.activity.LineDown(delta)
		}
		return m, nil
	}
	if m.mouseInOutput(msg) {
		if delta < 0 {
			m.output.LineUp(-delta)
		} else {
			m.output.LineDown(delta)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) resize() {
	leftW, rightW, panelH := m.layoutSize()
	m.output.Width = max(1, leftW-2)
	m.output.Height = max(1, panelH-3)
	m.activity.Width = max(1, rightW-2)
	m.activity.Height = max(1, panelH-3)
	m.modal.Width = max(40, int(float64(m.width)*0.68)-4)
	m.modal.Height = max(8, int(float64(m.height)*0.60)-4)
	m.input.SetWidth(max(1, m.width-6))
	m.planEdit.SetWidth(m.modal.Width - 2)
	m.planEdit.SetHeight(max(8, m.modal.Height-4))
	m.refreshContent(app.Reply{})
}

func (m *Model) refreshContent(reply app.Reply) {
	m.output.SetContent(renderMessages(m.messages, m.output.Width))
	m.output.GotoBottom()
	m.activity.SetContent(renderActivity(m.events, reply, m.activity.Width))
	m.activity.GotoBottom()
}

func (m *Model) submit(input string) tea.Cmd {
	if question, ok := alsoQuestion(input); ok {
		return m.submitAlso(question)
	}
	m.busy = true
	ch := make(chan app.ActivityRecord, 64)
	m.events = append(m.events, activityEvent{
		Time:   time.Now(),
		Input:  input,
		Status: "submitted",
	})
	m.refreshContent(app.Reply{})
	return tea.Batch(m.spinner.Tick, waitActivity(ch), func() tea.Msg {
		reply := m.app.SubmitWithActivity(context.Background(), input, func(record app.ActivityRecord) {
			select {
			case ch <- record:
			default:
			}
		})
		close(ch)
		return replyMsg(reply)
	})
}

func (m *Model) submitAlso(question string) tea.Cmd {
	m.events = append(m.events, activityEvent{
		Time:    time.Now(),
		Command: "/also",
		Status:  "observer submitted",
		Detail:  question,
	})
	m.status = "Also observer working..."
	m.refreshContent(app.Reply{})
	return func() tea.Msg {
		return alsoReplyMsg(m.app.RunAlsoObserver(context.Background(), question))
	}
}

func (m *Model) applyReply(reply app.Reply) {
	if reply.Command != "/also" || len(reply.History) > 0 {
		m.messages = reply.History
	}
	m.status = reply.Message
	for _, record := range reply.Activity {
		m.events = append(m.events, activityEventFromRecord(record))
	}
	m.events = append(m.events, activityEvent{
		Time:    time.Now(),
		Command: reply.Command,
		Panel:   reply.OpenPanel,
		Status:  compactStatus(reply.Message),
		Detail:  reply.Message,
	})
}

func alsoQuestion(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "/also" {
		return "", true
	}
	if strings.HasPrefix(trimmed, "/also ") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/also")), true
	}
	return "", false
}

func waitActivity(ch <-chan app.ActivityRecord) tea.Cmd {
	return func() tea.Msg {
		record, ok := <-ch
		if !ok {
			return liveActivityDoneMsg{}
		}
		return liveActivityMsg{Record: record, Ch: ch}
	}
}

func activityEventFromRecord(record app.ActivityRecord) activityEvent {
	return activityEvent{
		Time:    record.Time,
		Command: record.Name,
		Status:  record.Status,
		Detail:  record.Detail,
	}
}

func (m *Model) openModal(panel string, reply app.Reply) {
	if panel == "config" {
		m.openConfigModal()
		return
	}
	if panel == "models" {
		m.openModelsModal()
		return
	}
	if panel == "market" {
		m.openMarketModal(reply)
		return
	}
	if panel == "mcp" {
		m.openMCPModal(reply)
		return
	}
	m.mode = ModeModal
	m.panel = panel
	m.input.Blur()
	m.modal.SetContent(renderModal(panel, reply, m.modal.Width))
	m.modal.GotoTop()
}

func (m *Model) openActivityModal() {
	m.mode = ModeModal
	m.panel = "activity"
	m.input.Blur()
	m.modal.SetContent(renderFullActivity(m.events, m.modal.Width))
	m.modal.GotoBottom()
}

func (m *Model) completeInput() {
	if m.completePathInput() {
		return
	}
	value := m.input.Value()
	if value == "" {
		return
	}
	for _, candidate := range completions(m.app.State()) {
		if strings.HasPrefix(candidate, value) && candidate != value {
			m.input.SetValue(candidate)
			m.input.SetCursor(len(candidate))
			return
		}
	}
}

func (m Model) outputPanel() string {
	leftW, _, panelH := m.layoutSize()
	label := labelStyle.Width(leftW).Render("Output")
	body := panelStyle.Width(max(1, leftW-2)).Height(max(1, panelH-3)).Render(m.output.View())
	return lipgloss.JoinVertical(lipgloss.Left, label, body)
}

func (m Model) activityPanel() string {
	_, rightW, panelH := m.layoutSize()
	label := labelStyle.Width(rightW).Render("Activity Panel")
	body := activityStyle.Width(max(1, rightW-2)).Height(max(1, panelH-3)).Render(m.activity.View())
	return lipgloss.JoinVertical(lipgloss.Left, label, body)
}

func (m Model) layoutSize() (leftW int, rightW int, panelH int) {
	width := max(1, m.width)
	leftW = int(float64(width) * 0.70)
	rightW = width - leftW
	if width >= 54 && rightW < 24 {
		rightW = 24
		leftW = width - rightW
	}
	if width >= 54 && leftW < 30 {
		leftW = 30
		rightW = max(20, width-leftW)
	}
	if width < 54 {
		leftW = max(1, width/2)
		rightW = max(1, width-leftW)
	}
	// Header 9 + panel area + status 2 + bordered 3-line input 6.
	panelH = max(5, m.height-17)
	return leftW, rightW, panelH
}

func (m Model) mouseInOutput(msg tea.MouseMsg) bool {
	leftW, _, panelH := m.layoutSize()
	// y: header 0, label 1, panel rows start 2.
	return msg.X >= 0 && msg.X < leftW && msg.Y >= 2 && msg.Y < 2+panelH
}

func (m Model) mouseInActivity(msg tea.MouseMsg) bool {
	leftW, rightW, panelH := m.layoutSize()
	return msg.X >= leftW && msg.X < leftW+rightW && msg.Y >= 2 && msg.Y < 2+panelH
}

func (m Model) statusView() string {
	return lipgloss.JoinVertical(
		lipgloss.Left,
		statusStyle.Width(m.width).Render(m.statusTopLine()),
		statusWorkStyle.Width(m.width).Render(m.statusWorkLine()),
	)
}

func (m Model) statusTopLine() string {
	state := m.app.State()
	runtime, _ := state.Data["runtime"].(map[string]any)
	provider := fmt.Sprint(runtime["provider"])
	model := fmt.Sprint(runtime["model"])
	return fmt.Sprintf(" %s | %s | F1 /help | Ctrl+O activity | Ctrl+Q quit | Ctrl+J newline ", provider, model)
}

func (m Model) statusWorkLine() string {
	if m.busy {
		return fmt.Sprintf(" %s working | %s ", m.spinner.View(), compactStatus(m.status))
	}
	return fmt.Sprintf(" ready | %s ", compactStatus(m.status))
}

func (m Model) modalView() string {
	title := modalTitleStyle.Render(strings.ToUpper(m.panel))
	footer := "Esc close"
	if m.panel == "plan" && m.mode == ModeModal {
		footer += " | e edit | /plan focus <topic> | /plan execute"
	}
	if m.panel == "market" {
		footer += " | up/down move | space select | i install | s small | e install+enable | r refresh | d details"
	}
	if m.panel == "mcp" {
		footer += " | up/down move | e enable | x disable | r remove | d details"
	}
	if m.mode == ModePlanEdit {
		title = modalTitleStyle.Render("EDIT PLAN")
		footer = "Ctrl+S save | Esc cancel"
		return modalStyle.Width(m.modal.Width + 2).Height(m.modal.Height + 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, m.planEdit.View(), footerStyle.Render(footer)))
	}
	return modalStyle.Width(m.modal.Width + 2).Height(m.modal.Height + 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, m.modal.View(), footerStyle.Render(footer)))
}

func (m *Model) keepModalLineVisible(line int) {
	if line < 0 {
		line = 0
	}
	top := m.modal.YOffset
	bottom := top + max(1, m.modal.Height) - 1
	if line < top+1 {
		m.modal.SetYOffset(max(0, line-1))
		return
	}
	if line > bottom-1 {
		m.modal.SetYOffset(line - max(1, m.modal.Height) + 2)
	}
}

func completions(state app.Reply) []string {
	var out []string
	out = append(out, state.Suggestions...)
	for i := len(state.History) - 1; i >= 0 && len(out) < 80; i-- {
		if state.History[i].Role == "user" {
			out = append(out, state.History[i].Content)
		}
	}
	return out
}

func placeModal(width, height int, base, modal string) string {
	lines := strings.Split(base, "\n")
	boxLines := strings.Split(modal, "\n")
	top := max(1, height/2-len(boxLines)/2)
	left := max(2, width/2-lipgloss.Width(modal)/2)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func centerText(text string, width int) string {
	textWidth := lipgloss.Width(text)
	if textWidth >= width {
		return text
	}
	left := (width - textWidth) / 2
	return strings.Repeat(" ", left) + text
}

func isReplacingKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace, tea.KeyDelete:
		return true
	default:
		return false
	}
}
