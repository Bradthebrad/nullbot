package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

func (m *Model) openSkillsModal(reply app.Reply) {
	m.panel = "skills"
	m.mode = ModeModal
	m.input.Blur()
	m.skills = skillsFromReply(reply)
	if m.skillIndex >= len(m.skills) {
		m.skillIndex = max(0, len(m.skills)-1)
	}
	m.skillRaw = ""
	if selected, ok := reply.Data["selected"].(app.SkillReadResult); ok {
		m.skillRaw = selected.Content
	}
	m.modal.SetContent(m.renderSkillsModal())
	m.syncSkillsModalViewport()
}

func (m *Model) handleSkillsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "skills" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.skillIndex = max(0, m.skillIndex-1)
		m.skillRaw = ""
	case "down", "j":
		m.skillIndex = min(len(m.skills)-1, m.skillIndex+1)
		m.skillRaw = ""
	case "home":
		m.skillIndex = 0
		m.skillRaw = ""
	case "end":
		m.skillIndex = max(0, len(m.skills)-1)
		m.skillRaw = ""
	case "d":
		m.skillDetails = !m.skillDetails
	case "r":
		reply := m.app.Execute(context.Background(), "/skills")
		m.skills = skillsFromReply(reply)
		m.skillRaw = ""
	case "enter", "o":
		m.loadSelectedSkillRaw("")
	}
	m.modal.SetContent(m.renderSkillsModal())
	m.syncSkillsModalViewport()
	return m, nil, true
}

func (m Model) handleSkillsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "skills" {
		return m, nil, false
	}
	row, ok := modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.skillRowStarts())
	if !ok {
		return m, nil, false
	}
	m.skillIndex = row
	m.skillRaw = ""
	m.modal.SetContent(m.renderSkillsModal())
	m.syncSkillsModalViewport()
	return m, nil, true
}

func (m *Model) loadSelectedSkillRaw(rel string) {
	if len(m.skills) == 0 || m.skillIndex < 0 || m.skillIndex >= len(m.skills) {
		return
	}
	name := m.skills[m.skillIndex].Name
	cmd := "/skills open " + name
	if strings.TrimSpace(rel) != "" {
		cmd += " " + rel
	}
	reply := m.app.Execute(context.Background(), cmd)
	if selected, ok := reply.Data["selected"].(app.SkillReadResult); ok {
		m.skillRaw = selected.Content
		m.status = "Opened " + selected.Path
		return
	}
	m.status = reply.Message
}

func (m Model) renderSkillsModal() string {
	var b strings.Builder
	b.WriteString(renderMarkdown("Installed skills are loaded as lightweight metadata. Use `Enter` to preview the selected `SKILL.md`; referenced markdown remains on-demand for agents through `skill_read`.", m.modal.Width))
	b.WriteString("\n\n")
	if len(m.skills) == 0 {
		b.WriteString(mutedStyle.Render("No skills installed. Use /market for skill packs or create_skill from the agent."))
		return b.String()
	}
	nameW := min(24, max(12, m.modal.Width/4))
	refW := 6
	descW := max(20, m.modal.Width-nameW-refW-8)
	fmt.Fprintf(&b, "%-2s %-*s %-*s %s\n", "", nameW, "SKILL", refW, "REFS", "DESCRIPTION")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(24, m.modal.Width-2)))
	for i, skill := range m.skills {
		cursor := "  "
		if i == m.skillIndex {
			cursor = "> "
		}
		lines := wrapPlain(skill.Description, descW, "")
		if len(lines) == 0 {
			lines = []string{""}
		}
		for j, desc := range lines {
			name := ""
			refs := ""
			if j == 0 {
				name = padCell(skill.Name, nameW)
				refs = padCell(fmt.Sprintf("%d", len(skill.References)), refW)
			} else {
				name = strings.Repeat(" ", nameW)
				refs = strings.Repeat(" ", refW)
			}
			line := fmt.Sprintf("%s%-*s %-*s %s", cursor, nameW, name, refW, refs, desc)
			if i == m.skillIndex {
				line = selectedRowStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i == m.skillIndex {
			b.WriteString(skillDetailBlock(skill, m.skillDetails, m.modal.Width))
			if m.skillRaw != "" {
				b.WriteString("\n")
				b.WriteString(renderMarkdown("### Preview\n\n```markdown\n"+m.skillRaw+"\n```", m.modal.Width))
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func skillDetailBlock(skill app.SkillSummary, expanded bool, width int) string {
	var b strings.Builder
	detailW := max(12, width-4)
	for _, line := range wrapPlain("path: "+skill.Path, detailW, "   ") {
		b.WriteString(mutedStyle.Render(line))
		b.WriteByte('\n')
	}
	if len(skill.AllowedTools) > 0 {
		for _, line := range wrapPlain("allowed tools: "+strings.Join(skill.AllowedTools, ", "), detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	if skill.Error != "" {
		for _, line := range wrapPlain("error: "+skill.Error, detailW, "   ") {
			b.WriteString(mutedStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	if expanded && len(skill.References) > 0 {
		for _, ref := range skill.References {
			state := "missing"
			if ref.Exists {
				state = "ok"
			}
			text := fmt.Sprintf("ref: %s [%s]", ref.Path, state)
			if ref.Description != "" {
				text += " - " + ref.Description
			}
			for _, line := range wrapPlain(text, detailW, "   ") {
				b.WriteString(mutedStyle.Render(line))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func (m Model) skillRowStarts() []int {
	header := renderMarkdown("Installed skills are loaded as lightweight metadata. Use `Enter` to preview the selected `SKILL.md`; referenced markdown remains on-demand for agents through `skill_read`.", m.modal.Width)
	start := len(strings.Split(header, "\n")) + 3
	start += 2
	nameW := min(24, max(12, m.modal.Width/4))
	refW := 6
	descW := max(20, m.modal.Width-nameW-refW-8)
	starts := make([]int, 0, len(m.skills))
	for i, skill := range m.skills {
		starts = append(starts, start)
		start += max(1, len(wrapPlain(skill.Description, descW, "")))
		if i == m.skillIndex {
			details := skillDetailBlock(skill, m.skillDetails, m.modal.Width)
			if strings.TrimSpace(details) != "" {
				start += len(strings.Split(strings.TrimRight(details, "\n"), "\n"))
			}
			if m.skillRaw != "" {
				start += len(strings.Split(renderMarkdown("### Preview\n\n```markdown\n"+m.skillRaw+"\n```", m.modal.Width), "\n")) + 1
			}
		}
	}
	return starts
}

func (m *Model) syncSkillsModalViewport() {
	starts := m.skillRowStarts()
	if m.skillIndex >= 0 && m.skillIndex < len(starts) {
		m.keepModalLineVisible(starts[m.skillIndex])
	}
}

func skillsFromReply(reply app.Reply) []app.SkillSummary {
	if reply.Data == nil {
		return nil
	}
	if skills, ok := reply.Data["skills"].([]app.SkillSummary); ok {
		return skills
	}
	return nil
}
