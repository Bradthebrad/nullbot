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
	planID    string

	configFields    []configField
	configIndex     int
	configEditing   bool
	configEditValue string
	modelOptions    []app.ModelOption
	modelGroups     []app.ModelGroup
	modelIndex      int
	modelTarget     string

	marketPackages []app.MarketPackage
	marketIndex    int
	marketSelected map[string]bool
	marketDetails  bool

	mcpPackages []app.MarketPackage
	mcpIndex    int
	mcpDetails  bool

	tasks       []app.AgentTask
	taskIndex   int
	taskDetails bool

	agents        []app.AgentTask
	agentIndex    int
	agentTab      int
	agentDetails  bool
	thoughts      []app.ThoughtSnapshot
	thoughtIndex  int
	effortOptions []app.EffortOption
	effortIndex   int
	skills        []app.SkillSummary
	skillIndex    int
	skillDetails  bool
	skillRaw      string

	plans       []app.PlanSummary
	planIndex   int
	planDetails bool

	usage            app.UsageSnapshot
	usageTab         int
	usageModelFilter string
	themeIndex       int
	themeDetails     bool

	completionOpen    bool
	completionPrefix  string
	completionOptions []completionOption
	completionIndex   int
	inlineSuggestion  string
	lastInputAt       time.Time
	rapidInputCount   int
	pasteProtectUntil time.Time
	pasteNotice       string
	pendingPaste      string
}

type replyMsg app.Reply
type alsoReplyMsg app.Reply
type liveActivityMsg struct {
	Record app.ActivityRecord
	Ch     <-chan app.ActivityRecord
}
type liveActivityDoneMsg struct{}
type pasteNoticeDoneMsg struct{}
type dashboardTickMsg struct{}

type activityEvent struct {
	Time    time.Time
	Input   string
	Command string
	Panel   string
	Status  string
	Detail  string
}

func New(a *app.App) Model {
	applyTheme(a.Config().UI.Theme)
	input := textarea.New()
	input.Placeholder = "Message " + app.DisplayName(a.Config()) + " or type /help"
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
		status:     "ready",
		spinner:    spin,
		themeIndex: themeIndexByID(a.Config().UI.Theme),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tea.EnableBracketedPaste, tea.SetWindowTitle(app.DisplayName(m.app.Config())))
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
		m.input.Placeholder = "Message " + app.DisplayName(m.app.Config()) + " or type /help"
		if text, ok := reply.Data["copy"].(string); ok {
			if err := clipboard.WriteAll(text); err != nil {
				m.status = "Copy failed: " + err.Error()
			}
		}
		m.refreshContent(reply)
		if reply.OpenPanel != "" {
			m.openModal(reply.OpenPanel, reply)
			if reply.OpenPanel == "agents" || reply.OpenPanel == "thoughts" {
				return m, dashboardTick()
			}
		}
		if reply.Command == "/name" {
			return m, tea.SetWindowTitle(app.DisplayName(m.app.Config()))
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
		if message, ok := app.ReasoningMessageFromRecord(msg.Record); ok {
			m.messages = append(m.messages, message)
		}
		m.refreshContent(app.Reply{})
		return m, waitActivity(msg.Ch)
	case liveActivityDoneMsg:
		return m, nil
	case pasteNoticeDoneMsg:
		if time.Now().After(m.pasteProtectUntil) {
			m.pasteNotice = ""
		}
		return m, nil
	case dashboardTickMsg:
		if m.mode == ModeModal && (m.panel == "agents" || m.panel == "thoughts") {
			if m.panel == "agents" {
				reply := m.app.Execute(context.Background(), "/agents")
				m.agents = tasksFromReply(reply)
				m.modal.SetContent(m.renderAgentsModal())
				m.syncAgentsModalViewport()
			} else {
				reply := m.app.Execute(context.Background(), "/thoughts")
				m.agents = tasksFromReply(reply)
				m.thoughts = thoughtsFromReply(reply)
				m.modal.SetContent(m.renderThoughtsModal())
				m.syncThoughtsModalViewport()
			}
			if m.busy || m.hasRunningAgentTasks() {
				return m, dashboardTick()
			}
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		if m.busy {
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyMsg:
		if msg.Paste {
			if m.mode == ModeChat {
				if len(msg.Runes) > 0 {
					m.capturePaste(string(msg.Runes))
				} else if msg.String() == "enter" {
					m.input.InsertString("\n")
				}
				m.updateInlineSuggestion()
				return m, nil
			}
		}
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
	if m.completionOpen && m.mode == ModeChat {
		view = placeCompletion(m.width, m.height, view, m.completionView())
	}
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
		base := m.input.View()
		if m.pendingPaste != "" {
			base += "\n" + m.pasteChip()
		}
		if m.pasteNotice != "" {
			base += "\n" + mutedStyle.Render(m.pasteNotice)
		}
		if m.inlineSuggestion != "" && m.input.Value() != "" && !m.completionOpen {
			base += "\n" + mutedStyle.Render("→ "+m.inlineSuggestion)
		}
		return base
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
			if m.panel == "plan" && m.planID != "" {
				if err := m.app.SavePlanJSON(m.planID, m.planEdit.Value()); err != nil {
					m.status = "Plan save failed: " + err.Error()
					return m, nil
				}
				m.status = "Plan saved."
				reply := m.app.Execute(context.Background(), "/plan")
				m.mode = ModeModal
				m.openPlanModal(reply)
				return m, nil
			}
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
		if next, cmd, handled := m.handleTasksKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleAgentsKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleThoughtsKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleEffortKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleSkillsKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handlePlanKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleUsageKey(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleThemesKey(msg); handled {
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
				next, cmd, _ := m.startPlanEdit()
				return next, cmd
			}
		case "ctrl+o":
			m.openActivityModal()
			return m, nil
		}
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(msg)
		return m, cmd
	}

	if m.completionOpen {
		if next, cmd, handled := m.handleCompletionKey(msg); handled {
			return next, cmd
		}
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
	case "ctrl+v", "alt+v":
		if pasted, err := m.pasteClipboard(); err == nil && pasted {
			m.updateInlineSuggestion()
		} else {
			if err != nil {
				m.status = "Paste failed: " + err.Error()
			} else {
				m.status = "Nothing pasteable found on clipboard."
			}
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
		m.pendingPaste = ""
		m.pasteNotice = ""
		m.selectAll = false
		m.closeCompletion()
		m.updateInlineSuggestion()
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
		if m.capturePastedLineIfNeeded() {
			m.updateInlineSuggestion()
			return m, nil
		}
		if m.captureInputAsPasteIfNeeded() {
			m.updateInlineSuggestion()
			return m, nil
		}
		if m.input.Value() == "" && m.pendingPaste == "" {
			return m, nil
		}
		m.closeCompletion()
		value := m.input.Value()
		if m.pendingPaste != "" {
			value = combineInputAndPaste(value, m.pendingPaste)
			m.pendingPaste = ""
			m.pasteNotice = ""
			m.pasteProtectUntil = time.Time{}
		}
		if strings.TrimSpace(value) == "/paste" {
			m.input.Reset()
			m.selectAll = false
			if pasted, err := m.pasteClipboard(); err == nil && pasted {
				m.updateInlineSuggestion()
			} else if err != nil {
				m.status = "Paste failed: " + err.Error()
			} else {
				m.status = "Nothing pasteable found on clipboard."
			}
			return m, nil
		}
		if normalized, count := normalizeAttachmentText(value); count > 0 {
			value = normalized
			m.status = fmt.Sprintf("Attached %d file(s).", count)
		}
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
		if m.inputCursorAtEnd() {
			m.completeInput()
			return m, nil
		}
	case "up":
		if m.inputAtFirstVisualLine() {
			m.closeCompletion()
			m.historyPrev()
			return m, nil
		}
	case "down":
		if m.inputAtLastVisualLine() {
			m.closeCompletion()
			m.historyNext()
			return m, nil
		}
	case "ctrl+h":
		return m, m.submit("/history")
	case "ctrl+p":
		return m, m.submit("/plan")
	case "pgup":
		if m.input.LineCount() > 1 {
			m.inputPageUp()
			return m, nil
		}
		m.output.PageUp()
		return m, nil
	case "pgdown":
		if m.input.LineCount() > 1 {
			m.inputPageDown()
			return m, nil
		}
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
	before := m.input.Value()
	if m.selectAll && isReplacingKey(msg) {
		m.input.Reset()
		m.pendingPaste = ""
		m.selectAll = false
	}
	if isReplacingKey(msg) {
		m.historyAt = len(m.history)
		m.draft = ""
		m.closeCompletion()
	}
	m.input, cmd = m.input.Update(msg)
	m.normalizeInputAttachments()
	pasteCmd := m.observePossiblePaste(msg, before)
	m.updateInlineSuggestion()
	return m, tea.Batch(cmd, pasteCmd)
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.MouseLeft && m.mode == ModeModal {
		if next, cmd, handled := m.handleMarketMouse(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleMCPMouse(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleEffortMouse(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleSkillsMouse(msg); handled {
			return next, cmd
		}
		if next, cmd, handled := m.handleAgentsMouse(msg); handled {
			return next, cmd
		}
	}
	if msg.Type == tea.MouseLeft && m.mode == ModeChat {
		if m.mouseInInput(msg) {
			m.input.Focus()
			return m, nil
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
		Status:  replyActivityStatus(reply),
		Detail:  replyActivityDetail(reply),
	})
}

func replyActivityStatus(reply app.Reply) string {
	if reply.Command == "" && reply.OpenPanel == "" {
		return "Agent completed task."
	}
	return compactStatus(reply.Message)
}

func replyActivityDetail(reply app.Reply) string {
	if reply.Command == "" && reply.OpenPanel == "" {
		return ""
	}
	return reply.Message
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
	if panel == "tasks" {
		m.openTasksModal(reply)
		return
	}
	if panel == "agents" {
		m.openAgentsModal(reply)
		return
	}
	if panel == "thoughts" {
		m.openThoughtsModal(reply)
		return
	}
	if panel == "effort" {
		m.openEffortModal(reply)
		return
	}
	if panel == "skills" {
		m.openSkillsModal(reply)
		return
	}
	if panel == "plan" {
		m.openPlanModal(reply)
		return
	}
	if panel == "usage" {
		m.openUsageModal(reply)
		return
	}
	if panel == "themes" {
		m.openThemesModal()
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
	if m.completionOpen {
		m.applyCompletion()
		return
	}
	if m.openPathCompletionPicker() {
		return
	}
	value := m.input.Value()
	if value == "" {
		return
	}
	if m.inlineSuggestion != "" && strings.HasPrefix(m.inlineSuggestion, value) && m.inlineSuggestion != value {
		m.setInputValue(m.inlineSuggestion)
		m.updateInlineSuggestion()
		return
	}
	for _, candidate := range completions(m.app.State()) {
		if strings.HasPrefix(candidate, value) && candidate != value {
			m.input.SetValue(candidate)
			m.input.SetCursor(len(candidate))
			m.updateInlineSuggestion()
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

func (m Model) mouseInInput(msg tea.MouseMsg) bool {
	inputTop := max(0, m.height-5)
	return msg.Y >= inputTop && msg.Y < m.height && msg.X >= 0 && msg.X < m.width
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
	return fmt.Sprintf(" %s | %s | F1 /help | Alt+V paste | Ctrl+O activity | Ctrl+Q quit | Ctrl+J newline ", provider, model)
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
		footer += " | up/down move | enter details | e edit | x execute | r refresh"
	}
	if m.panel == "market" {
		footer += " | up/down move | space select | i install | s small | e install+enable | r refresh | d details"
	}
	if m.panel == "mcp" {
		footer += " | up/down move | e enable | x disable | r remove | d details"
	}
	if m.panel == "tasks" {
		footer += " | up/down move | enter details | c cancel | r refresh | d details"
	}
	if m.panel == "agents" {
		footer += " | tab/left/right tabs | up/down move | enter details | c cancel | r refresh"
	}
	if m.panel == "thoughts" {
		footer += " | tab/left/right tabs | up/down move | r refresh"
	}
	if m.panel == "effort" {
		footer += " | up/down move | enter apply | click row"
	}
	if m.panel == "skills" {
		footer += " | up/down move | enter raw | d details | r reload"
	}
	if m.panel == "usage" {
		footer += " | tab/left/right tabs | f model filter | c clear | r refresh"
	}
	if m.panel == "themes" {
		footer += " | up/down move | d details | enter/Ctrl+S apply"
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

func (m *Model) observePossiblePaste(msg tea.KeyMsg, before string) tea.Cmd {
	if msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace {
		return nil
	}
	now := time.Now()
	batched := len(msg.Runes) > 1
	rapid := !m.lastInputAt.IsZero() && now.Sub(m.lastInputAt) <= 35*time.Millisecond
	m.lastInputAt = now
	if batched {
		m.rapidInputCount = 4
	} else if rapid {
		m.rapidInputCount++
	} else {
		m.rapidInputCount = 0
	}
	if !batched && m.rapidInputCount < 4 {
		return nil
	}
	m.pasteProtectUntil = now.Add(650 * time.Millisecond)
	lines := strings.Count(m.input.Value(), "\n") + 1
	if lines > 1 {
		m.pasteNotice = fmt.Sprintf("[Pasted +%d lines. Press Enter to submit.]", lines)
	} else if len(m.input.Value())-len(before) > 20 || batched {
		m.pasteNotice = "[Pasted text detected. Press Enter to submit.]"
	}
	return clearPasteNoticeAfter(700 * time.Millisecond)
}

func (m Model) pasteProtected() bool {
	return !m.pasteProtectUntil.IsZero() && time.Now().Before(m.pasteProtectUntil)
}

func clearPasteNoticeAfter(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return pasteNoticeDoneMsg{}
	})
}

func dashboardTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return dashboardTickMsg{}
	})
}

func (m Model) hasRunningAgentTasks() bool {
	for _, task := range m.agents {
		if task.Status == app.TaskRunning || task.Status == app.TaskCanceling {
			return true
		}
	}
	return false
}

func isReplacingKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace, tea.KeyDelete:
		return true
	default:
		return false
	}
}
