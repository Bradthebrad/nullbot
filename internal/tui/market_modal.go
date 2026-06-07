package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"yourbot/internal/app"
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
	b.WriteString(renderMarkdown(message+"\n\nUse `up/down`, click rows, `space` select, `r` refresh, `i` install, `s` small, `e` install+enable, `d` details, `esc` close.", m.modal.Width))
	b.WriteString("\n\n")
	if len(m.marketPackages) == 0 {
		b.WriteString(mutedStyle.Render("No packages found. Press r to refresh."))
		return b.String()
	}
	for i, pkg := range m.marketPackages {
		cursor := "  "
		if i == m.marketIndex {
			cursor = "> "
		}
		check := "[ ]"
		if m.marketSelected[pkg.ID] {
			check = "[x]"
		}
		state := packageState(pkg)
		line := fmt.Sprintf("%s%s %-24s %-12s %s", cursor, check, pkg.ID, state, pkg.Description)
		if i == m.marketIndex {
			line = selectedRowStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if i == m.marketIndex {
			b.WriteString(marketPackageDetails(pkg, m.marketDetails))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderMCPModal(message string) string {
	var b strings.Builder
	b.WriteString(renderMarkdown(message+"\n\nUse `up/down`, click rows, `e` enable, `x` disable, `r` remove, `d` details, `esc` close.", m.modal.Width))
	b.WriteString("\n\n")
	if len(m.mcpPackages) == 0 {
		b.WriteString(mutedStyle.Render("No MCP packages installed yet. Use /market to install one."))
		return b.String()
	}
	for i, pkg := range m.mcpPackages {
		cursor := "  "
		if i == m.mcpIndex {
			cursor = "> "
		}
		state := packageState(pkg)
		line := fmt.Sprintf("%s%-24s %-12s %s", cursor, pkg.ID, state, pkg.Description)
		if i == m.mcpIndex {
			line = selectedRowStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if i == m.mcpIndex {
			b.WriteString(mcpPackageDetails(pkg, m.mcpDetails))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func marketPackageDetails(pkg app.MarketPackage, expanded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "   kind: `%s` repo: `%s`\n", pkg.Kind, pkg.Repo)
	if len(pkg.Permissions) > 0 {
		fmt.Fprintf(&b, "   permissions: `%s`\n", strings.Join(pkg.Permissions, "`, `"))
	}
	if pkg.InstalledAsset != "" {
		fmt.Fprintf(&b, "   installed asset: `%s`\n", pkg.InstalledAsset)
	}
	if pkg.Error != "" {
		fmt.Fprintf(&b, "   error: %s\n", pkg.Error)
	}
	if expanded {
		for _, asset := range pkg.Assets {
			fmt.Fprintf(&b, "   asset: `%s`", asset.Name)
			if asset.Compressed {
				b.WriteString(" small/upx")
			}
			b.WriteByte('\n')
		}
		for _, skill := range pkg.SkillFiles {
			fmt.Fprintf(&b, "   skill: `%s` `%s`\n", skill.Name, skill.Path)
		}
	}
	return mutedStyle.Render(strings.TrimRight(b.String(), "\n")) + "\n"
}

func mcpPackageDetails(pkg app.MarketPackage, expanded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "   install dir: `%s`\n", pkg.InstallDir)
	fmt.Fprintf(&b, "   asset: `%s`\n", pkg.InstalledAsset)
	if len(pkg.Permissions) > 0 {
		fmt.Fprintf(&b, "   permissions: `%s`\n", strings.Join(pkg.Permissions, "`, `"))
	}
	if expanded {
		fmt.Fprintf(&b, "   transport: `%s`\n", pkg.DefaultTransport)
		fmt.Fprintf(&b, "   repo: `%s`\n", pkg.Repo)
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
	m.keepModalLineVisible(4 + m.marketIndex*3)
}

func (m *Model) syncMCPModalViewport() {
	m.keepModalLineVisible(4 + m.mcpIndex*3)
}

func (m *Model) marketRowAt(msg tea.MouseMsg) (int, bool) {
	return modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.marketHeaderLines("Market panel opened."), len(m.marketPackages))
}

func (m *Model) mcpRowAt(msg tea.MouseMsg) (int, bool) {
	return modalRowAt(msg, m.width, m.height, m.modalView(), m.modal.YOffset, m.mcpHeaderLines("MCP panel opened."), len(m.mcpPackages))
}

func (m *Model) marketHeaderLines(message string) int {
	header := renderMarkdown(message+"\n\nUse `up/down`, click rows, `space` select, `r` refresh, `i` install, `s` small, `e` install+enable, `d` details, `esc` close.", m.modal.Width)
	return len(strings.Split(header, "\n")) + 1
}

func (m *Model) mcpHeaderLines(message string) int {
	header := renderMarkdown(message+"\n\nUse `up/down`, click rows, `e` enable, `x` disable, `r` remove, `d` details, `esc` close.", m.modal.Width)
	return len(strings.Split(header, "\n")) + 1
}

func modalRowAt(msg tea.MouseMsg, width, height int, modalView string, yOffset int, headerLines int, rows int) (int, bool) {
	if rows == 0 {
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
	row := contentLine - headerLines
	if row < 0 {
		return 0, false
	}
	index := row / 3
	if index < 0 || index >= rows {
		return 0, false
	}
	return index, true
}
