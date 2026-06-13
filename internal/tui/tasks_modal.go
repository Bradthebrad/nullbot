package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func (m *Model) openTasksModal(reply app.Reply) {
	m.panel = "tasks"
	m.mode = ModeModal
	m.input.Blur()
	m.tasks = tasksFromReply(reply)
	if m.taskIndex >= len(m.tasks) {
		m.taskIndex = max(0, len(m.tasks)-1)
	}
	if _, ok := reply.Data["detail"].(app.AgentTask); ok {
		m.taskDetails = true
	}
	m.modal.SetContent(m.renderTasksModal())
	m.syncTasksModalViewport()
}

func (m *Model) handleTasksKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "tasks" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.taskIndex = max(0, m.taskIndex-1)
		m.taskDetails = false
	case "down", "j":
		m.taskIndex = min(len(m.tasks)-1, m.taskIndex+1)
		m.taskDetails = false
	case "enter", "d":
		m.taskDetails = !m.taskDetails
	case "r":
		reply := m.app.Execute(context.Background(), "/tasks")
		m.tasks = tasksFromReply(reply)
	case "c":
		if len(m.tasks) == 0 {
			return m, nil, true
		}
		id := m.tasks[m.taskIndex].ID
		reply := m.app.Execute(context.Background(), "/tasks cancel "+id)
		m.tasks = tasksFromReply(reply)
		m.status = reply.Message
		m.events = append(m.events, activityEvent{Time: now(), Command: "/tasks", Status: "cancel requested", Detail: id})
	}
	m.modal.SetContent(m.renderTasksModal())
	m.syncTasksModalViewport()
	return m, nil, true
}

func (m *Model) renderTasksModal() string {
	var b strings.Builder
	b.WriteString(renderMarkdown("Primary, observer, and subagent tasks. `Enter` toggles details, `c` cancels a cancelable running task, `r` refreshes.", m.modal.Width))
	b.WriteString("\n\n")
	if len(m.tasks) == 0 {
		return renderMarkdown("No tasks recorded yet.", m.modal.Width)
	}
	nameW := min(28, max(12, m.modal.Width/4))
	currentW := max(18, m.modal.Width-nameW-36)
	fmt.Fprintf(&b, "%-2s %-10s %-10s %-*s %-9s %s\n", "", "STATUS", "ROLE", nameW, "NAME", "AGE", "CURRENT")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(20, m.modal.Width-2)))
	for i, task := range m.tasks {
		cursor := "  "
		if i == m.taskIndex {
			cursor = "> "
		}
		age := taskAge(task)
		current := quoteCompact(task.Current, currentW)
		fmt.Fprintf(&b, "%s%-10s %-10s %-*s %-9s %s\n", cursor, task.Status, task.Role, nameW, quoteCompact(task.Name, nameW), age, current)
	}
	if m.taskDetails && len(m.tasks) > 0 {
		b.WriteString("\n")
		b.WriteString(taskDetailMarkdown(m.tasks[m.taskIndex], m.modal.Width))
	}
	return strings.TrimRight(b.String(), "\n")
}

func taskDetailMarkdown(task app.AgentTask, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", task.Name)
	fmt.Fprintf(&b, "- **ID:** `%s`\n", task.ID)
	fmt.Fprintf(&b, "- **Role:** `%s`\n", task.Role)
	fmt.Fprintf(&b, "- **Status:** `%s`\n", task.Status)
	fmt.Fprintf(&b, "- **Started:** `%s`\n", task.StartedAt.Format("2006-01-02 15:04:05"))
	if !task.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "- **Finished:** `%s`\n", task.FinishedAt.Format("2006-01-02 15:04:05"))
	}
	if task.Tokens.Input+task.Tokens.Output+task.Tokens.Total > 0 {
		fmt.Fprintf(&b, "- **Tokens:** input `%d`, output `%d`, total `%d`\n", task.Tokens.Input, task.Tokens.Output, task.Tokens.Total)
	} else {
		b.WriteString("- **Tokens:** not reported by provider yet\n")
	}
	if task.Prompt != "" {
		fmt.Fprintf(&b, "\n### Task\n\n%s\n", task.Prompt)
	}
	if task.Current != "" {
		fmt.Fprintf(&b, "\n### Current\n\n%s\n", task.Current)
	}
	if task.Error != "" {
		fmt.Fprintf(&b, "\n### Error\n\n%s\n", task.Error)
	}
	if task.Result != "" {
		fmt.Fprintf(&b, "\n### Result\n\n%s\n", task.Result)
	}
	if len(task.ToolCalls) > 0 {
		b.WriteString("\n### Tool Calls\n\n")
		start := max(0, len(task.ToolCalls)-20)
		for _, call := range task.ToolCalls[start:] {
			fmt.Fprintf(&b, "- `%s` `%s` %s - %s\n", call.Time.Format("15:04:05"), call.Name, call.Status, quoteCompact(call.Detail, 180))
		}
	}
	if len(task.Activity) > 0 {
		b.WriteString("\n### Recent Activity\n\n")
		start := max(0, len(task.Activity)-12)
		for _, record := range task.Activity[start:] {
			fmt.Fprintf(&b, "- `%s` `%s` %s - %s\n", record.Time.Format("15:04:05"), record.Name, record.Status, quoteCompact(record.Detail, 180))
		}
	}
	return renderMarkdown(b.String(), width)
}

func (m *Model) syncTasksModalViewport() {
	m.keepModalLineVisible(4 + m.taskIndex)
}

func tasksFromReply(reply app.Reply) []app.AgentTask {
	if reply.Data == nil {
		return nil
	}
	if tasks, ok := reply.Data["tasks"].([]app.AgentTask); ok {
		return tasks
	}
	return nil
}

func taskAge(task app.AgentTask) string {
	end := time.Now().UTC()
	if !task.FinishedAt.IsZero() {
		end = task.FinishedAt
	}
	d := end.Sub(task.StartedAt)
	if d < time.Second {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
