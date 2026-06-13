package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	tea "github.com/charmbracelet/bubbletea"

	"yourbot/internal/app"
)

var usageTabs = []string{"Summary", "Chart", "Models", "Recent", "Pricing"}

func (m *Model) openUsageModal(reply app.Reply) {
	m.panel = "usage"
	m.mode = ModeModal
	m.input.Blur()
	m.usage = usageFromReply(reply)
	m.modal.SetContent(m.renderUsageModal())
	m.modal.GotoTop()
}

func (m *Model) handleUsageKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "usage" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "tab", "right", "l":
		m.usageTab = (m.usageTab + 1) % len(usageTabs)
	case "shift+tab", "left", "h":
		m.usageTab--
		if m.usageTab < 0 {
			m.usageTab = len(usageTabs) - 1
		}
	case "r":
		reply := m.app.Execute(context.Background(), "/usage")
		m.usage = usageFromReply(reply)
	case "c":
		reply := m.app.Execute(context.Background(), "/usage clear")
		m.usage = usageFromReply(reply)
		m.status = reply.Message
	case "f":
		m.cycleUsageModelFilter()
	case "up", "k":
		m.modal.LineUp(1)
		return m, nil, true
	case "down", "j":
		m.modal.LineDown(1)
		return m, nil, true
	case "home":
		m.modal.GotoTop()
		return m, nil, true
	case "end":
		m.modal.GotoBottom()
		return m, nil, true
	}
	m.modal.SetContent(m.renderUsageModal())
	m.modal.GotoTop()
	return m, nil, true
}

func (m *Model) renderUsageModal() string {
	var b strings.Builder
	b.WriteString(m.renderUsageTabs())
	b.WriteString("\n\n")
	switch usageTabs[m.usageTab] {
	case "Summary":
		b.WriteString(m.renderUsageSummary())
	case "Chart":
		b.WriteString(m.renderUsageChart())
	case "Models":
		b.WriteString(m.renderUsageModels())
	case "Recent":
		b.WriteString(m.renderUsageRecent())
	case "Pricing":
		b.WriteString(m.renderUsagePricing())
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderUsageTabs() string {
	var parts []string
	for i, tab := range usageTabs {
		label := " " + tab + " "
		if i == m.usageTab {
			parts = append(parts, selectedRowStyle.Render(label))
		} else {
			parts = append(parts, mutedStyle.Render(label))
		}
	}
	return strings.Join(parts, " ")
}

func (m *Model) renderUsageSummary() string {
	var b strings.Builder
	b.WriteString(renderMarkdown("# Usage Summary\n\n", m.modal.Width))
	b.WriteString(usageTotalsTable("This Session", m.usage.Session))
	b.WriteString("\n\n")
	b.WriteString(usageTotalsTable("All Time", m.usage.Total))
	b.WriteString("\n\n")
	b.WriteString(renderMarkdown("Use `Tab`/arrows for tabs. `f` filters chart by model. `c` clears local usage history.", m.modal.Width))
	if len(m.usage.ByModel) > 0 {
		b.WriteString("\n\n## Top Models\n\n")
		for i, summary := range m.usage.ByModel {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "- `%s/%s`: %d tokens, %s\n", summary.Provider, summary.Model, summary.Totals.TotalTokens, formatUsageCost(summary.Totals.CostUSD))
		}
	}
	return b.String()
}

func usageTotalsTable(label string, totals app.UsageTotals) string {
	return fmt.Sprintf("## %s\n\nRequests: `%d`\nInput: `%d`\nOutput: `%d`\nTotal Tokens: `%d`\nCost: `%s`\nEstimated Rows: `%d`\n",
		label, totals.Requests, totals.InputTokens, totals.OutputTokens, totals.TotalTokens, formatUsageCost(totals.CostUSD), totals.Estimated)
}

func (m *Model) renderUsageChart() string {
	filter := m.usageModelFilter
	if filter == "" {
		filter = "all models"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Daily Token Usage\n\nFilter: `%s`\n\n", filter)
	points := usageChartPoints(m.usage.Daily, m.usageModelFilter)
	if len(points) == 0 {
		b.WriteString("No usage records yet.")
		return renderMarkdown(b.String(), m.modal.Width)
	}
	width := max(28, min(m.modal.Width-4, 72))
	height := 12
	chart := timeserieslinechart.New(width, height)
	for _, point := range points {
		chart.Push(timeserieslinechart.TimePoint{Time: point.Day, Value: float64(point.Tokens)})
	}
	chart.DrawBraille()
	b.WriteString("```text\n")
	b.WriteString(chart.View())
	b.WriteString("\n```\n")
	b.WriteString("\n`f` cycles model filters. Values are total tokens per day.")
	return renderMarkdown(b.String(), m.modal.Width)
}

type usageChartPoint struct {
	Day    time.Time
	Tokens int
}

func usageChartPoints(days []app.UsageDailySummary, filter string) []usageChartPoint {
	byDay := map[string]int{}
	for _, day := range days {
		key := day.Provider + "/" + day.Model
		if filter != "" && key != filter {
			continue
		}
		byDay[day.Day] += day.Totals.TotalTokens
	}
	keys := make([]string, 0, len(byDay))
	for day := range byDay {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	out := make([]usageChartPoint, 0, len(keys))
	for _, day := range keys {
		t, err := time.Parse("2006-01-02", day)
		if err != nil {
			continue
		}
		out = append(out, usageChartPoint{Day: t, Tokens: byDay[day]})
	}
	return out
}

func (m *Model) renderUsageModels() string {
	var b strings.Builder
	b.WriteString("# Cost By Model\n\n")
	if len(m.usage.ByModel) == 0 {
		b.WriteString("No usage records yet.")
		return renderMarkdown(b.String(), m.modal.Width)
	}
	fmt.Fprintf(&b, "%-22s %8s %8s %8s %10s\n", "MODEL", "INPUT", "OUTPUT", "TOTAL", "COST")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(40, min(m.modal.Width-4, 76))))
	for _, summary := range m.usage.ByModel {
		name := quoteCompact(summary.Provider+"/"+summary.Model, 22)
		fmt.Fprintf(&b, "%-22s %8d %8d %8d %10s\n", name, summary.Totals.InputTokens, summary.Totals.OutputTokens, summary.Totals.TotalTokens, formatUsageCost(summary.Totals.CostUSD))
	}
	return b.String()
}

func (m *Model) renderUsageRecent() string {
	var b strings.Builder
	b.WriteString("# Recent Usage\n\n")
	if len(m.usage.Recent) == 0 {
		b.WriteString("No usage records yet.")
		return renderMarkdown(b.String(), m.modal.Width)
	}
	for i := len(m.usage.Recent) - 1; i >= 0; i-- {
		record := m.usage.Recent[i]
		estimated := ""
		if record.Estimated {
			estimated = " estimated"
		}
		fmt.Fprintf(&b, "- `%s` `%s` `%s/%s` in `%d` out `%d` total `%d` cost `%s`%s\n",
			record.Time.Format("01-02 15:04"), record.Agent, record.Provider, record.Model, record.InputTokens, record.OutputTokens, record.TotalTokens, formatUsageCost(record.CostUSD), estimated)
	}
	return renderMarkdown(b.String(), m.modal.Width)
}

func (m *Model) renderUsagePricing() string {
	var b strings.Builder
	b.WriteString("# Pricing Table\n\n")
	b.WriteString("Costs are local estimates using known per-1M-token text rates where available. Unknown models record tokens with `$0` until a price pattern is added.\n\n")
	fmt.Fprintf(&b, "%-12s %-24s %10s %10s\n", "PROVIDER", "PATTERN", "INPUT/1M", "OUTPUT/1M")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", max(42, min(m.modal.Width-4, 70))))
	for _, price := range m.usage.Pricing {
		mark := ""
		if price.EstimatedPrice {
			mark = "*"
		}
		fmt.Fprintf(&b, "%-12s %-24s %10s %10s%s\n", price.Provider, quoteCompact(price.ModelPattern, 24), formatUsageCost(price.InputPerMTok), formatUsageCost(price.OutputPerMTok), mark)
	}
	b.WriteString("\n`*` means the price row is a fallback estimate.")
	return b.String()
}

func (m *Model) cycleUsageModelFilter() {
	models := []string{""}
	for _, summary := range m.usage.ByModel {
		models = append(models, summary.Provider+"/"+summary.Model)
	}
	current := 0
	for i, model := range models {
		if model == m.usageModelFilter {
			current = i
			break
		}
	}
	m.usageModelFilter = models[(current+1)%len(models)]
}

func usageFromReply(reply app.Reply) app.UsageSnapshot {
	if reply.Data == nil {
		return app.UsageSnapshot{}
	}
	if usage, ok := reply.Data["usage"].(app.UsageSnapshot); ok {
		return usage
	}
	return app.UsageSnapshot{}
}

func formatUsageCost(cost float64) string {
	if cost < 0.0001 {
		return fmt.Sprintf("$%.6f", cost)
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
