package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func (m *Model) openMarketModal(reply app.Reply) {
	m.panel = "market"
	m.mode = ModeModal
	m.input.Blur()
	m.marketPackages = marketPackagesFromReply(reply)
	m.marketIndex = min(m.marketIndex, max(0, len(m.marketPackages)-1))
	if m.marketSelected == nil {
		m.marketSelected = map[string]bool{}
	}
	m.modal.SetContent(m.renderMarketModal(reply.Message))
	m.syncMarketModalViewport()
}

func (m *Model) openMCPModal(reply app.Reply) {
	m.panel = "mcp"
	m.mode = ModeModal
	m.input.Blur()
	m.mcpPackages = mcpPackagesFromReply(reply)
	m.mcpIndex = min(m.mcpIndex, max(0, len(m.mcpPackages)-1))
	m.modal.SetContent(m.renderMCPModal(reply.Message))
	m.syncMCPModalViewport()
}

func (m *Model) handleMarketKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "market" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.marketIndex = max(0, m.marketIndex-1)
	case "down", "j":
		m.marketIndex = min(max(0, len(m.marketPackages)-1), m.marketIndex+1)
	case "home":
		m.marketIndex = 0
	case "end":
		m.marketIndex = max(0, len(m.marketPackages)-1)
	case " ", "space", "x":
		m.toggleMarketSelection()
	case "d":
		m.marketDetails = !m.marketDetails
	case "r":
		return m, m.submit("/market refresh"), true
	case "i", "enter":
		return m, m.submit(m.marketInstallCommand(false)), true
	case "s":
		return m, m.submit(m.marketInstallCommand(true)), true
	case "e":
		return m, m.submit(m.marketInstallEnableCommand()), true
	}
	m.modal.SetContent(m.renderMarketModal("Market panel opened."))
	m.syncMarketModalViewport()
	return m, nil, true
}

func (m *Model) handleMCPKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "mcp" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.mcpIndex = max(0, m.mcpIndex-1)
	case "down", "j":
		m.mcpIndex = min(max(0, len(m.mcpPackages)-1), m.mcpIndex+1)
	case "home":
		m.mcpIndex = 0
	case "end":
		m.mcpIndex = max(0, len(m.mcpPackages)-1)
	case "d":
		m.mcpDetails = !m.mcpDetails
	case "e", "enter":
		if pkg, ok := m.currentMCPPackage(); ok {
			return m, m.submit("/mcp enable " + pkg.ID), true
		}
	case "x":
		if pkg, ok := m.currentMCPPackage(); ok {
			return m, m.submit("/mcp disable " + pkg.ID), true
		}
	case "r":
		if pkg, ok := m.currentMCPPackage(); ok {
			return m, m.submit("/mcp remove " + pkg.ID), true
		}
	}
	m.modal.SetContent(m.renderMCPModal("MCP panel opened."))
	m.syncMCPModalViewport()
	return m, nil, true
}

func (m *Model) handleMarketMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "market" {
		return m, nil, false
	}
	row, ok := m.marketRowAt(msg)
	if !ok {
		return m, nil, false
	}
	m.marketIndex = row
	m.toggleMarketSelection()
	m.modal.SetContent(m.renderMarketModal("Market panel opened."))
	m.syncMarketModalViewport()
	return m, nil, true
}

func (m *Model) handleMCPMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "mcp" {
		return m, nil, false
	}
	row, ok := m.mcpRowAt(msg)
	if !ok {
		return m, nil, false
	}
	m.mcpIndex = row
	m.modal.SetContent(m.renderMCPModal("MCP panel opened."))
	m.syncMCPModalViewport()
	return m, nil, true
}

func (m *Model) toggleMarketSelection() {
	if pkg, ok := m.currentMarketPackage(); ok {
		m.marketSelected[pkg.ID] = !m.marketSelected[pkg.ID]
	}
}

func (m *Model) currentMarketPackage() (app.MarketPackage, bool) {
	if len(m.marketPackages) == 0 || m.marketIndex < 0 || m.marketIndex >= len(m.marketPackages) {
		return app.MarketPackage{}, false
	}
	return m.marketPackages[m.marketIndex], true
}

func (m *Model) currentMCPPackage() (app.MarketPackage, bool) {
	if len(m.mcpPackages) == 0 || m.mcpIndex < 0 || m.mcpIndex >= len(m.mcpPackages) {
		return app.MarketPackage{}, false
	}
	return m.mcpPackages[m.mcpIndex], true
}

func (m *Model) selectedMarketPackages() []app.MarketPackage {
	var out []app.MarketPackage
	for _, pkg := range m.marketPackages {
		if m.marketSelected[pkg.ID] {
			out = append(out, pkg)
		}
	}
	if len(out) == 0 {
		if pkg, ok := m.currentMarketPackage(); ok {
			out = append(out, pkg)
		}
	}
	return out
}

func (m *Model) marketInstallCommand(small bool) string {
	pkgs := m.selectedMarketPackages()
	if len(pkgs) == 0 {
		return "/market"
	}
	ids := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		ids = append(ids, pkg.ID)
	}
	suffix := ""
	if small {
		suffix = " small"
	}
	return "/market install " + strings.Join(ids, ",") + suffix
}

func (m *Model) marketInstallEnableCommand() string {
	pkgs := m.selectedMarketPackages()
	if len(pkgs) == 0 {
		return "/market"
	}
	ids := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		ids = append(ids, pkg.ID)
	}
	return "/market install " + strings.Join(ids, ",") + " enable"
}

func (m *Model) renderMarketModal(message string) string {
	var b strings.Builder
	b.WriteString(renderMarkdown(message, m.modal.Width))
	b.WriteString("\n\n")
	b.WriteString(renderMarketButtons(m.modal.Width))
	b.WriteByte('\n')
	b.WriteString(mutedStyle.Render("Click a row to select it. Use up/down to move through the table."))
	b.WriteString("\n\n")
	if len(m.marketPackages) == 0 {
		b.WriteString(mutedStyle.Render("No packages found. Press r to refresh."))
		return b.String()
	}
	widths := modalTableWidths(m.modal.Width, true)
	b.WriteString(tableHeader(widths, true))
	b.WriteByte('\n')
	for i, pkg := range m.marketPackages {
		lines := marketTableRow(pkg, widths, i == m.marketIndex, m.marketSelected[pkg.ID])
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i == m.marketIndex {
			b.WriteString(marketPackageDetails(pkg, m.marketDetails, m.modal.Width))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderMCPModal(message string) string {
	var b strings.Builder
	b.WriteString(renderMarkdown(message, m.modal.Width))
	b.WriteString("\n\n")
	b.WriteString(renderMCPButtons(m.modal.Width))
	b.WriteByte('\n')
	b.WriteString(mutedStyle.Render("Click a row to focus it. Use up/down to move through the table."))
	b.WriteString("\n\n")
	if len(m.mcpPackages) == 0 {
		b.WriteString(mutedStyle.Render("No MCP packages installed yet. Use /market to install one."))
		return b.String()
	}
	widths := modalTableWidths(m.modal.Width, false)
	b.WriteString(tableHeader(widths, false))
	b.WriteByte('\n')
	for i, pkg := range m.mcpPackages {
		lines := mcpTableRow(pkg, widths, i == m.mcpIndex)
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i == m.mcpIndex {
			b.WriteString(mcpPackageDetails(pkg, m.mcpDetails, m.modal.Width))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMarketButtons(width int) string {
	buttons := []string{
		buttonStyle.Render("[ Space Select ]"),
		buttonStyle.Render("[ I Install ]"),
		buttonStyle.Render("[ S Small ]"),
		buttonStyle.Render("[ E Install+Enable ]"),
		buttonStyle.Render("[ R Refresh ]"),
		buttonStyle.Render("[ D Details ]"),
	}
	return renderButtonGroup(buttons, width)
}

func renderMCPButtons(width int) string {
	buttons := []string{
		buttonStyle.Render("[ E Enable ]"),
		buttonStyle.Render("[ X Disable ]"),
		buttonStyle.Render("[ R Remove ]"),
		buttonStyle.Render("[ D Details ]"),
	}
	return renderButtonGroup(buttons, width)
}

func renderButtonGroup(buttons []string, width int) string {
	if width <= 0 {
		return strings.Join(buttons, " ")
	}
	var lines []string
	current := ""
	for _, button := range buttons {
		next := button
		if current != "" {
			next = current + " " + button
		}
		if lipgloss.Width(next) > width && current != "" {
			lines = append(lines, current)
			current = button
			continue
		}
		current = next
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}

type modalTable struct {
	Check int
	ID    int
	State int
	Desc  int
}

func modalTableWidths(width int, hasCheck bool) modalTable {
	checkW := 0
	if hasCheck {
		checkW = 4
	}
	idW := 22
	stateW := 11
	// cursor + gaps between cursor/check/id/state/description.
	fixed := 2 + checkW + 1 + idW + 2 + stateW + 2
	descW := max(18, width-fixed)
	if width < 72 {
		idW = 18
		stateW = 10
		fixed = 2 + checkW + 1 + idW + 1 + stateW + 1
		descW = max(12, width-fixed)
	}
	return modalTable{Check: checkW, ID: idW, State: stateW, Desc: descW}
}

func tableHeader(widths modalTable, hasCheck bool) string {
	check := ""
	if hasCheck {
		check = padCell("SEL", widths.Check) + " "
	}
	line := "  " + check + padCell("PACKAGE", widths.ID) + "  " + padCell("STATE", widths.State) + "  " + "DESCRIPTION"
	return mutedStyle.Render(line)
}

func marketTableRow(pkg app.MarketPackage, widths modalTable, selected bool, checked bool) []string {
	check := "[ ]"
	if checked {
		check = "[x]"
	}
	return packageTableRow(pkg, widths, selected, check)
}

func mcpTableRow(pkg app.MarketPackage, widths modalTable, selected bool) []string {
	return packageTableRow(pkg, widths, selected, "")
}

func packageTableRow(pkg app.MarketPackage, widths modalTable, selected bool, check string) []string {
	cursor := "  "
	if selected {
		cursor = "> "
	}
	descLines := wrapPlain(pkg.Description, widths.Desc, "")
	if len(descLines) == 0 {
		descLines = []string{""}
	}
	out := make([]string, 0, len(descLines))
	for i, desc := range descLines {
		rowCheck := strings.Repeat(" ", widths.Check)
		id := ""
		state := ""
		if i == 0 {
			rowCheck = padCell(check, widths.Check)
			id = padCell(pkg.ID, widths.ID)
			state = padCell(packageState(pkg), widths.State)
		} else {
			id = strings.Repeat(" ", widths.ID)
			state = strings.Repeat(" ", widths.State)
		}
		if widths.Check > 0 {
			out = append(out, cursor+rowCheck+" "+id+"  "+state+"  "+desc)
		} else {
			out = append(out, cursor+id+"  "+state+"  "+desc)
		}
	}
	if selected {
		for i, line := range out {
			out[i] = selectedRowStyle.Render(line)
		}
	}
	return out
}

func padCell(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) > width {
		runes := []rune(text)
		for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
			runes = runes[:len(runes)-1]
		}
		text = string(runes) + "…"
	}
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

func marketPackageDetails(pkg app.MarketPackage, expanded bool, width int) string {
	var b strings.Builder
	detailWidth := max(12, width-4)
	for _, line := range wrapPlain(fmt.Sprintf("kind: %s  repo: %s", pkg.Kind, pkg.Repo), detailWidth, "    ") {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(pkg.Permissions) > 0 {
		for _, line := range wrapPlain("permissions: "+strings.Join(pkg.Permissions, ", "), detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if pkg.InstalledAsset != "" {
		for _, line := range wrapPlain("installed asset: "+pkg.InstalledAsset, detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if pkg.Error != "" {
		for _, line := range wrapPlain("error: "+pkg.Error, detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if expanded {
		for _, asset := range pkg.Assets {
			text := "asset: " + asset.Name
			if asset.Compressed {
				text += " small/upx"
			}
			for _, line := range wrapPlain(text, detailWidth, "    ") {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		for _, skill := range pkg.SkillFiles {
			for _, line := range wrapPlain(fmt.Sprintf("skill: %s %s", skill.Name, skill.Path), detailWidth, "    ") {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	return mutedStyle.Render(strings.TrimRight(b.String(), "\n")) + "\n"
}

func mcpPackageDetails(pkg app.MarketPackage, expanded bool, width int) string {
	var b strings.Builder
	detailWidth := max(12, width-4)
	for _, line := range wrapPlain("install dir: "+pkg.InstallDir, detailWidth, "    ") {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range wrapPlain("asset: "+pkg.InstalledAsset, detailWidth, "    ") {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(pkg.Permissions) > 0 {
		for _, line := range wrapPlain("permissions: "+strings.Join(pkg.Permissions, ", "), detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if expanded {
		for _, line := range wrapPlain("transport: "+pkg.DefaultTransport, detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		for _, line := range wrapPlain("repo: "+pkg.Repo, detailWidth, "    ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return mutedStyle.Render(strings.TrimRight(b.String(), "\n")) + "\n"
}

func packageState(pkg app.MarketPackage) string {
	switch {
	case pkg.Enabled:
		return "enabled"
	case pkg.Installed:
		return "installed"
	case pkg.Status != "":
		return pkg.Status
	default:
		return "available"
	}
}

func marketPackagesFromReply(reply app.Reply) []app.MarketPackage {
	if packages, ok := reply.Data["packages"].([]app.MarketPackage); ok {
		return packages
	}
	return nil
}

func mcpPackagesFromReply(reply app.Reply) []app.MarketPackage {
	packages, ok := reply.Data["packages"].([]app.MarketPackage)
	if !ok {
		return nil
	}
	var out []app.MarketPackage
	for _, pkg := range packages {
		if pkg.Kind == "mcp_server" && pkg.Installed {
			out = append(out, pkg)
		}
	}
	return out
}

func (m *Model) syncMarketModalViewport() {
	starts := m.marketRowStarts("Market panel opened.")
	if m.marketIndex >= 0 && m.marketIndex < len(starts) {
		m.keepModalLineVisible(starts[m.marketIndex])
	}
}

func (m *Model) syncMCPModalViewport() {
	starts := m.mcpRowStarts("MCP panel opened.")
	if m.mcpIndex >= 0 && m.mcpIndex < len(starts) {
		m.keepModalLineVisible(starts[m.mcpIndex])
	}
}

func (m *Model) marketRowAt(msg tea.MouseMsg) (int, bool) {
	return modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.marketRowStarts("Market panel opened."))
}

func (m *Model) mcpRowAt(msg tea.MouseMsg) (int, bool) {
	return modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.mcpRowStarts("MCP panel opened."))
}

func (m *Model) marketHeaderLines(message string) int {
	header := renderMarkdown(message, m.modal.Width)
	buttons := renderMarketButtons(m.modal.Width)
	return len(strings.Split(header, "\n")) + len(strings.Split(buttons, "\n")) + 4
}

func (m *Model) mcpHeaderLines(message string) int {
	header := renderMarkdown(message, m.modal.Width)
	buttons := renderMCPButtons(m.modal.Width)
	return len(strings.Split(header, "\n")) + len(strings.Split(buttons, "\n")) + 4
}

func (m *Model) marketRowStarts(message string) []int {
	widths := modalTableWidths(m.modal.Width, true)
	start := m.marketHeaderLines(message)
	starts := make([]int, 0, len(m.marketPackages))
	for i, pkg := range m.marketPackages {
		starts = append(starts, start)
		start += packageTableLineCount(pkg, widths, i == m.marketIndex, m.marketDetails, true) + 1
	}
	return starts
}

func (m *Model) mcpRowStarts(message string) []int {
	widths := modalTableWidths(m.modal.Width, false)
	start := m.mcpHeaderLines(message)
	starts := make([]int, 0, len(m.mcpPackages))
	for i, pkg := range m.mcpPackages {
		starts = append(starts, start)
		start += packageTableLineCount(pkg, widths, i == m.mcpIndex, m.mcpDetails, false) + 1
	}
	return starts
}

func packageTableLineCount(pkg app.MarketPackage, widths modalTable, selected bool, details bool, market bool) int {
	count := len(wrapPlain(pkg.Description, widths.Desc, ""))
	if selected {
		detailText := ""
		if market {
			detailText = marketPackageDetails(pkg, details, widths.Desc+widths.ID+widths.State+12)
		} else {
			detailText = mcpPackageDetails(pkg, details, widths.Desc+widths.ID+widths.State+12)
		}
		detailText = strings.TrimRight(detailText, "\n")
		if detailText != "" {
			count += len(strings.Split(detailText, "\n"))
		}
	}
	return max(1, count)
}

func modalRowAt(msg tea.MouseMsg, width, height int, modalView string, yOffset int, starts []int) (int, bool) {
	if len(starts) == 0 {
		return 0, false
	}
	boxLines := strings.Split(modalView, "\n")
	top := max(1, height/2-len(boxLines)/2)
	modalWidth := lipgloss.Width(modalView)
	left := max(2, width/2-modalWidth/2)
	if msg.X < left || msg.X > left+modalWidth || msg.Y < top {
		return 0, false
	}
	contentLine := msg.Y - top - 3 + yOffset
	for i, start := range starts {
		next := start + 1
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		if contentLine >= start && contentLine < next {
			return i, true
		}
	}
	return 0, false
}
