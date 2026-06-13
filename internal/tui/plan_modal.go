package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func (m *Model) openPlanModal(reply app.Reply) {
	m.panel = "plan"
	m.mode = ModeModal
	m.input.Blur()
	m.plans = plansFromReply(reply)
	if selected, ok := reply.Data["selected"].(app.Plan); ok {
		for i, summary := range m.plans {
			if summary.ID == selected.ID {
				m.planIndex = i
				break
			}
		}
	}
	if m.planIndex >= len(m.plans) {
		m.planIndex = max(0, len(m.plans)-1)
	}
	m.modal.SetContent(m.renderPlanModal(reply))
	m.syncPlanModalViewport()
}

func (m *Model) handlePlanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "plan" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.planIndex = max(0, m.planIndex-1)
		m.planDetails = false
	case "down", "j":
		m.planIndex = min(len(m.plans)-1, m.planIndex+1)
		m.planDetails = false
	case "enter", "d":
		m.planDetails = !m.planDetails
	case "r":
		reply := m.app.Execute(context.Background(), "/plan")
		m.plans = plansFromReply(reply)
	case "e":
		return m.startPlanEdit()
	case "x":
		if len(m.plans) == 0 {
			return m, nil, true
		}
		id := m.plans[m.planIndex].ID
		m.mode = ModeChat
		return m, m.submit("/plan execute " + id), true
	}
	m.modal.SetContent(m.renderPlanModal(app.Reply{}))
	m.syncPlanModalViewport()
	return m, nil, true
}

func (m *Model) startPlanEdit() (tea.Model, tea.Cmd, bool) {
	if len(m.plans) == 0 || m.planIndex >= len(m.plans) {
		m.status = "No plan selected."
		return m, nil, true
	}
	selected := m.plans[m.planIndex]
	plan, ok := m.selectedPlan()
	if !ok {
		m.status = "Could not load selected plan."
		return m, nil, true
	}
	m.planID = selected.ID
	m.mode = ModePlanEdit
	m.planEdit.SetValue(planJSONForEdit(plan))
	m.planEdit.Focus()
	return m, textarea.Blink, true
}

func (m *Model) renderPlanModal(reply app.Reply) string {
	var b strings.Builder
	if reply.Message != "" {
		b.WriteString(renderMarkdown(reply.Message, m.modal.Width))
		b.WriteString("\n\n")
	}
	if dir, ok := reply.Data["dir"].(string); ok && dir != "" {
		b.WriteString(renderMarkdown("Plans directory: `"+dir+"`", m.modal.Width))
		b.WriteString("\n\n")
	}
	if len(m.plans) == 0 {
		return renderMarkdown("No plans yet.\n\nCreate one with `/plan <goal>`, for example `/plan figure out if we can add charts and graphs to the project`.", m.modal.Width)
	}
	nameW := min(28, max(12, m.modal.Width/4))
	goalW := max(18, m.modal.Width-nameW-32)
	fmt.Fprintf(&b, "%-2s %-12s %-9s %-*s %s\n", "", "STATUS", "PROGRESS", nameW, "NAME", "GOAL")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(20, m.modal.Width-2)))
	for i, plan := range m.plans {
		cursor := "  "
		if i == m.planIndex {
			cursor = "> "
		}
		fmt.Fprintf(&b, "%s%-12s %-9s %-*s %s\n", cursor, plan.Status, plan.Progress, nameW, quoteCompact(plan.Name, nameW), quoteCompact(plan.Goal, goalW))
	}
	if m.planDetails {
		if plan, ok := m.selectedPlan(); ok {
			b.WriteString("\n")
			b.WriteString(renderMarkdown(planMarkdownForView(plan), m.modal.Width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) selectedPlan() (app.Plan, bool) {
	if len(m.plans) == 0 || m.planIndex >= len(m.plans) {
		return app.Plan{}, false
	}
	plan, err := m.app.PlanByID(m.plans[m.planIndex].ID)
	return plan, err == nil
}

func plansFromReply(reply app.Reply) []app.PlanSummary {
	if reply.Data == nil {
		return nil
	}
	if plans, ok := reply.Data["plans"].([]app.PlanSummary); ok {
		return plans
	}
	return nil
}

func planMarkdownForView(plan app.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", plan.Name)
	fmt.Fprintf(&b, "- **ID:** `%s`\n", plan.ID)
	fmt.Fprintf(&b, "- **Status:** `%s`\n", plan.Status)
	if plan.Goal != "" {
		fmt.Fprintf(&b, "- **Goal:** %s\n", plan.Goal)
	}
	if plan.CurrentStep != "" {
		fmt.Fprintf(&b, "- **Current Step:** `%s`\n", plan.CurrentStep)
	}
	b.WriteString("\n### Checklist\n\n")
	writePlanStepsForTUI(&b, plan.Steps, 0)
	if len(plan.Notes) > 0 {
		b.WriteString("\n### Notes\n\n")
		for _, note := range plan.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return b.String()
}

func writePlanStepsForTUI(b *strings.Builder, steps []app.PlanStep, depth int) {
	prefix := strings.Repeat("  ", depth)
	for _, step := range steps {
		box := "[ ]"
		if step.Status == "complete" || step.Status == "completed" || step.Status == "done" {
			box = "[x]"
		}
		fmt.Fprintf(b, "%s- %s `%s` **%s**", prefix, box, step.ID, step.Title)
		if step.Description != "" {
			fmt.Fprintf(b, " - %s", strings.ReplaceAll(step.Description, "\n", " "))
		}
		b.WriteByte('\n')
		writePlanStepsForTUI(b, step.Substeps, depth+1)
	}
}

func planJSONForEdit(plan app.Plan) string {
	// Re-marshal via app-visible fields so manual edits stay close to the stored schema.
	return fmt.Sprintf(`{
  "id": %q,
  "name": %q,
  "goal": %q,
  "status": %q,
  "current_step": %q,
  "steps": %s,
  "notes": %s
}`, plan.ID, plan.Name, plan.Goal, plan.Status, plan.CurrentStep, mustJSON(plan.Steps), mustJSON(plan.Notes))
}

func mustJSON(value any) string {
	data, _ := json.MarshalIndent(value, "  ", "  ")
	return string(data)
}

func (m *Model) syncPlanModalViewport() {
	m.keepModalLineVisible(5 + m.planIndex)
}
