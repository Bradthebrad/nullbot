package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func (m *Model) openAgentsModal(reply app.Reply) {
	m.panel = "agents"
	m.mode = ModeModal
	m.input.Blur()
	m.agents = tasksFromReply(reply)
	if m.agentTab >= len(m.agentTabLabels()) {
		m.agentTab = max(0, len(m.agentTabLabels())-1)
	}
	if m.agentIndex >= len(m.visibleAgentTasks()) {
		m.agentIndex = max(0, len(m.visibleAgentTasks())-1)
	}
	m.modal.SetContent(m.renderAgentsModal())
	m.syncAgentsModalViewport()
}

func (m *Model) openThoughtsModal(reply app.Reply) {
	m.panel = "thoughts"
	m.mode = ModeModal
	m.input.Blur()
	m.agents = tasksFromReply(reply)
	m.thoughts = thoughtsFromReply(reply)
	if m.agentTab >= len(m.agentTabLabels()) {
		m.agentTab = max(0, len(m.agentTabLabels())-1)
	}
	if m.thoughtIndex >= len(m.visibleThoughts()) {
		m.thoughtIndex = max(0, len(m.visibleThoughts())-1)
	}
	m.modal.SetContent(m.renderThoughtsModal())
	m.syncThoughtsModalViewport()
}

func (m *Model) handleAgentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "agents" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "tab", "right", "l":
		m.agentTab = (m.agentTab + 1) % len(m.agentTabLabels())
		m.agentIndex = 0
	case "shift+tab", "left", "h":
		m.agentTab--
		if m.agentTab < 0 {
			m.agentTab = len(m.agentTabLabels()) - 1
		}
		m.agentIndex = 0
	case "up", "k":
		m.agentIndex = max(0, m.agentIndex-1)
	case "down", "j":
		m.agentIndex = min(len(m.visibleAgentTasks())-1, m.agentIndex+1)
	case "enter", "d":
		m.agentDetails = !m.agentDetails
	case "r":
		reply := m.app.Execute(context.Background(), "/agents")
		m.agents = tasksFromReply(reply)
	case "c":
		tasks := m.visibleAgentTasks()
		if len(tasks) > 0 {
			reply := m.app.Execute(context.Background(), "/agents cancel "+tasks[m.agentIndex].ID)
			m.agents = tasksFromReply(reply)
			m.status = reply.Message
		}
	}
	m.modal.SetContent(m.renderAgentsModal())
	m.syncAgentsModalViewport()
	return m, nil, true
}

func (m *Model) handleThoughtsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "thoughts" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "tab", "right", "l":
		m.agentTab = (m.agentTab + 1) % len(m.agentTabLabels())
		m.thoughtIndex = 0
	case "shift+tab", "left", "h":
		m.agentTab--
		if m.agentTab < 0 {
			m.agentTab = len(m.agentTabLabels()) - 1
		}
		m.thoughtIndex = 0
	case "up", "k":
		m.thoughtIndex = max(0, m.thoughtIndex-1)
	case "down", "j":
		m.thoughtIndex = min(len(m.visibleThoughts())-1, m.thoughtIndex+1)
	case "r":
		reply := m.app.Execute(context.Background(), "/thoughts")
		m.agents = tasksFromReply(reply)
		m.thoughts = thoughtsFromReply(reply)
	}
	m.modal.SetContent(m.renderThoughtsModal())
	m.syncThoughtsModalViewport()
	return m, nil, true
}

func (m Model) handleAgentsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "agents" && m.panel != "thoughts" {
		return m, nil, false
	}
	starts := m.agentRowStarts()
	if m.panel == "thoughts" {
		starts = m.thoughtRowStarts()
	}
	row, ok := modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, starts)
	if !ok {
		return m, nil, false
	}
	if m.panel == "thoughts" {
		m.thoughtIndex = row
		m.modal.SetContent(m.renderThoughtsModal())
		m.syncThoughtsModalViewport()
		return m, nil, true
	}
	m.agentIndex = row
	m.modal.SetContent(m.renderAgentsModal())
	m.syncAgentsModalViewport()
	return m, nil, true
}

func (m Model) renderAgentsModal() string {
	var b strings.Builder
	b.WriteString(m.renderAgentTabs())
	b.WriteString("\n\n")
	tasks := m.visibleAgentTasks()
	if len(tasks) == 0 {
		return b.String() + mutedStyle.Render("No agent tasks recorded yet.")
	}
	nameW := min(22, max(12, m.modal.Width/5))
	currentW := max(18, m.modal.Width-nameW-52)
	fmt.Fprintf(&b, "%-2s %-10s %-10s %-*s %7s %7s %7s %7s %s\n", "", "STATUS", "ROLE", nameW, "AGENT", "IN", "OUT", "CACHE", "THINK", "CURRENT")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(40, m.modal.Width-2)))
	for i, task := range tasks {
		cursor := "  "
		if i == m.agentIndex {
			cursor = "> "
		}
		line := fmt.Sprintf("%s%-10s %-10s %-*s %7d %7d %7d %7d %s",
			cursor, task.Status, task.Role, nameW, quoteCompact(task.Name, nameW),
			task.Tokens.Input, task.Tokens.Output, task.Tokens.CachedInput, task.Tokens.ReasoningOutput,
			quoteCompact(task.Current, currentW))
		if i == m.agentIndex {
			line = selectedRowStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if i == m.agentIndex {
			b.WriteString(agentTaskDetails(task, m.agentDetails, m.modal.Width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderThoughtsModal() string {
	var b strings.Builder
	b.WriteString(m.renderAgentTabs())
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("Provider-private reasoning is not exposed. This view shows available reasoning summaries, task prompts, progress notes, tool intent, and errors."))
	b.WriteString("\n\n")
	thoughts := m.visibleThoughts()
	if len(thoughts) == 0 {
		return b.String() + mutedStyle.Render("No agent progress notes recorded yet.")
	}
	nameW := min(22, max(12, m.modal.Width/4))
	fmt.Fprintf(&b, "%-2s %-10s %-10s %-*s %s\n", "", "STATUS", "ROLE", nameW, "AGENT", "CURRENT")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(32, m.modal.Width-2)))
	for i, thought := range thoughts {
		cursor := "  "
		if i == m.thoughtIndex {
			cursor = "> "
		}
		currentW := max(18, m.modal.Width-nameW-28)
		line := fmt.Sprintf("%s%-10s %-10s %-*s %s", cursor, thought.Status, thought.Role, nameW, quoteCompact(thought.Agent, nameW), quoteCompact(thought.Current, currentW))
		if i == m.thoughtIndex {
			line = selectedRowStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if i == m.thoughtIndex {
			b.WriteString(thoughtDetails(thought, m.modal.Width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderAgentTabs() string {
	var parts []string
	for i, tab := range m.agentTabLabels() {
		label := " " + tab + " "
		if i == m.agentTab {
			parts = append(parts, selectedRowStyle.Render(label))
		} else {
			parts = append(parts, mutedStyle.Render(label))
		}
	}
	return strings.Join(parts, " ")
}

func agentTaskDetails(task app.AgentTask, expanded bool, width int) string {
	var b strings.Builder
	detailW := max(12, width-4)
	summary := fmt.Sprintf("id: %s  total: %d  cache create: %d  cache read: %d", task.ID, task.Tokens.Total, task.Tokens.CacheCreationInput, task.Tokens.CachedInput)
	for _, line := range wrapPlain(summary, detailW, "   ") {
		b.WriteString(mutedStyle.Render(line))
		b.WriteByte('\n')
	}
	if task.Error != "" {
		for _, line := range wrapPlain("error: "+task.Error, detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	if len(task.ToolCalls) > 0 {
		start := max(0, len(task.ToolCalls)-8)
		for _, call := range task.ToolCalls[start:] {
			text := fmt.Sprintf("tool: %s %s %s", call.Time.Format("15:04:05"), call.Name, call.Status)
			detail := ""
			if call.Status == "tool start" {
				detail = strings.TrimPrefix(call.Detail, "args: ")
			} else if call.Status == "tool error" {
				detail = strings.TrimPrefix(call.Detail, "error: ")
			}
			if detail != "" && !strings.HasPrefix(strings.ToLower(detail), "output:") {
				text += " args=" + quoteCompact(detail, 120)
			}
			for _, line := range wrapPlain(text, detailW, "   ") {
				b.WriteString(mutedStyle.Render(line))
				b.WriteByte('\n')
			}
		}
	}
	if expanded && task.Prompt != "" {
		for _, line := range wrapPlain("task: "+task.Prompt, detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func thoughtDetails(thought app.ThoughtSnapshot, width int) string {
	var b strings.Builder
	detailW := max(12, width-4)
	if thought.Prompt != "" {
		for _, line := range wrapPlain("task: "+thought.Prompt, detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	for _, note := range thought.Notes {
		for _, line := range wrapPlain("note: "+note, detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m Model) visibleAgentTasks() []app.AgentTask {
	return filterTasksForAgentTab(m.agents, m.agentTab)
}

func (m Model) visibleThoughts() []app.ThoughtSnapshot {
	tabTasks := filterTasksForAgentTab(m.agents, m.agentTab)
	allowed := map[string]bool{}
	for _, task := range tabTasks {
		allowed[task.ID] = true
	}
	var out []app.ThoughtSnapshot
	for _, thought := range m.thoughts {
		if allowed[thought.TaskID] {
			out = append(out, thought)
		}
	}
	return out
}

func filterTasksForAgentTab(tasks []app.AgentTask, tab int) []app.AgentTask {
	subagents := subagentTasks(tasks)
	var out []app.AgentTask
	if tab == 0 {
		for _, task := range tasks {
			if task.Role == "primary" || task.Role == "planner" || task.Role == "plan-executor" || task.Role == "also" {
				out = append(out, task)
			}
		}
		return out
	}
	if tab > 0 && tab <= len(subagents) {
		return []app.AgentTask{subagents[tab-1]}
	}
	return append([]app.AgentTask{}, tasks...)
}

func (m Model) agentTabLabels() []string {
	labels := []string{"Manager"}
	for _, task := range subagentTasks(m.agents) {
		label := task.Name
		if label == "" {
			label = task.ID
		}
		labels = append(labels, quoteCompact(label, 18))
	}
	labels = append(labels, "All")
	return labels
}

func subagentTasks(tasks []app.AgentTask) []app.AgentTask {
	var out []app.AgentTask
	for _, task := range tasks {
		if task.Role == "subagent" {
			out = append(out, task)
		}
	}
	return out
}

func (m Model) agentRowStarts() []int {
	start := len(strings.Split(m.renderAgentTabs(), "\n")) + 2
	start += 2
	tasks := m.visibleAgentTasks()
	starts := make([]int, 0, len(tasks))
	for i, task := range tasks {
		starts = append(starts, start)
		start++
		if i == m.agentIndex {
			details := agentTaskDetails(task, m.agentDetails, m.modal.Width)
			if strings.TrimSpace(details) != "" {
				start += len(strings.Split(strings.TrimRight(details, "\n"), "\n"))
			}
		}
	}
	return starts
}

func (m Model) thoughtRowStarts() []int {
	start := len(strings.Split(m.renderAgentTabs(), "\n")) + 4
	start += 2
	thoughts := m.visibleThoughts()
	starts := make([]int, 0, len(thoughts))
	for i, thought := range thoughts {
		starts = append(starts, start)
		start++
		if i == m.thoughtIndex {
			details := thoughtDetails(thought, m.modal.Width)
			if strings.TrimSpace(details) != "" {
				start += len(strings.Split(strings.TrimRight(details, "\n"), "\n"))
			}
		}
	}
	return starts
}

func (m *Model) syncAgentsModalViewport() {
	starts := m.agentRowStarts()
	if m.agentIndex >= 0 && m.agentIndex < len(starts) {
		m.keepModalLineVisible(starts[m.agentIndex])
	}
}

func (m *Model) syncThoughtsModalViewport() {
	starts := m.thoughtRowStarts()
	if m.thoughtIndex >= 0 && m.thoughtIndex < len(starts) {
		m.keepModalLineVisible(starts[m.thoughtIndex])
	}
}

func thoughtsFromReply(reply app.Reply) []app.ThoughtSnapshot {
	if reply.Data == nil {
		return nil
	}
	if thoughts, ok := reply.Data["thoughts"].([]app.ThoughtSnapshot); ok {
		return thoughts
	}
	return nil
}
