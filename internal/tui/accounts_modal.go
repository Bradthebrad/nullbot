package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/pkg/app"
)

func (m *Model) openAccountsModal(reply app.Reply) {
	m.panel = "accounts"
	m.mode = ModeModal
	m.input.Blur()
	m.accounts = accountsFromReply(reply)
	if flow, ok := reply.Data["flow"].(app.CodexDeviceFlow); ok {
		m.codexFlow = flow
		m.codexLoginText = "Codex login started. Open the URL and enter code " + flow.UserCode + "."
	}
	if m.accountIndex >= len(m.accounts.Accounts) {
		m.accountIndex = max(0, len(m.accounts.Accounts)-1)
	}
	m.modal.SetContent(m.renderAccountsModal())
	m.syncAccountsModalViewport()
}

func (m *Model) handleAccountsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.panel != "accounts" {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q":
		return m, nil, false
	case "up", "k":
		m.accountIndex = max(0, m.accountIndex-1)
	case "down", "j":
		m.accountIndex = min(max(0, len(m.accounts.Accounts)-1), m.accountIndex+1)
	case "home":
		m.accountIndex = 0
	case "end":
		m.accountIndex = max(0, len(m.accounts.Accounts)-1)
	case "r":
		m.accounts = m.app.AccountStatus()
		m.codexLoginText = "Account status refreshed."
	case "o":
		if m.codexFlow.VerificationURI == "" {
			m.codexLoginText = "No active Codex login URL. Press c to start sign-in."
		} else if err := openExternalURL(m.codexFlow.VerificationURI); err != nil {
			m.codexLoginText = "Open browser failed: " + err.Error()
		} else {
			m.codexLoginText = "Opened " + m.codexFlow.VerificationURI
		}
	case "c", "enter":
		account, ok := m.currentAccount()
		if !ok || account.ID != "codex" {
			m.codexLoginText = "Select Codex subscription, then press c to sign in. API keys are edited in /config."
			break
		}
		m.codexLoginText = "Starting Codex login..."
		m.modal.SetContent(m.renderAccountsModal())
		return m, accountsLoginCmd(m.app), true
	case "p":
		if m.codexFlow.DeviceAuthID == "" {
			m.codexLoginText = "No active Codex login to poll. Press c to start sign-in."
			break
		}
		m.codexLoginText = "Checking Codex login..."
		m.modal.SetContent(m.renderAccountsModal())
		return m, accountsPollNowCmd(m.app, m.codexFlow), true
	}
	m.modal.SetContent(m.renderAccountsModal())
	m.syncAccountsModalViewport()
	return m, nil, true
}

func (m *Model) renderAccountsModal() string {
	var b strings.Builder
	b.WriteString(renderMarkdown("Account status for API keys and browser-based subscription login. API keys are edited in `/config`; Codex sign-in can be started here.", m.modal.Width))
	b.WriteString("\n\n")
	if m.accounts.KeysPath != "" {
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render("API keys: "+m.accounts.KeysPath))
	}
	if m.accounts.CodexAuthPath != "" {
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render("Codex auth: "+m.accounts.CodexAuthPath))
	}
	if m.codexLoginText != "" {
		b.WriteString("\n")
		b.WriteString(renderMarkdown("**Codex:** "+m.codexLoginText, m.modal.Width))
		b.WriteString("\n")
	}
	if m.codexFlow.DeviceAuthID != "" {
		b.WriteString("\n")
		b.WriteString(renderMarkdown(fmt.Sprintf("Open `%s` and enter code `%s`.\n\nExpires: `%s`", m.codexFlow.VerificationURI, m.codexFlow.UserCode, m.codexFlow.ExpiresAt), m.modal.Width))
		if m.codexPolling {
			b.WriteString("\n")
			b.WriteString(mutedStyle.Render("Polling for authorization..."))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	labelW := min(26, max(14, m.modal.Width/4))
	statusW := 14
	kindW := 14
	detailW := max(18, m.modal.Width-labelW-statusW-kindW-8)
	fmt.Fprintf(&b, "%s\n", mutedStyle.Render(fmt.Sprintf("  %-*s  %-*s  %-*s  %s", labelW, "ACCOUNT", kindW, "TYPE", statusW, "STATUS", "DETAIL")))
	for i, account := range m.accounts.Accounts {
		cursor := "  "
		if i == m.accountIndex {
			cursor = "> "
		}
		detail := strings.TrimSpace(firstNonEmptyText(account.Masked, account.Detail))
		lines := wrapPlain(detail, detailW, "")
		if len(lines) == 0 {
			lines = []string{""}
		}
		row := fmt.Sprintf("%s%-*s  %-*s  %-*s  %s", cursor, labelW, quoteCompact(account.Label, labelW), kindW, quoteCompact(account.Kind, kindW), statusW, quoteCompact(account.Status, statusW), lines[0])
		if i == m.accountIndex {
			row = selectedRowStyle.Render(row)
		}
		b.WriteString(row)
		b.WriteByte('\n')
		for _, line := range lines[1:] {
			fmt.Fprintf(&b, "  %-*s  %-*s  %-*s  %s\n", labelW, "", kindW, "", statusW, "", line)
		}
	}
	if account, ok := m.currentAccount(); ok {
		b.WriteString("\n")
		b.WriteString(accountDetailMarkdown(account, m.modal.Width))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) currentAccount() (app.AccountInfo, bool) {
	if len(m.accounts.Accounts) == 0 || m.accountIndex < 0 || m.accountIndex >= len(m.accounts.Accounts) {
		return app.AccountInfo{}, false
	}
	return m.accounts.Accounts[m.accountIndex], true
}

func accountDetailMarkdown(account app.AccountInfo, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", account.Label)
	fmt.Fprintf(&b, "- **ID:** `%s`\n", account.ID)
	fmt.Fprintf(&b, "- **Type:** `%s`\n", account.Kind)
	fmt.Fprintf(&b, "- **Status:** `%s`\n", account.Status)
	if account.Masked != "" {
		fmt.Fprintf(&b, "- **Credential:** `%s`\n", account.Masked)
	}
	if account.Detail != "" {
		fmt.Fprintf(&b, "\n%s\n", account.Detail)
	}
	if account.ID == "codex" {
		b.WriteString("\nPress `c` to start browser sign-in. Press `p` to poll immediately while login is active.\n")
	} else if account.CanSaveKey {
		b.WriteString("\nEdit this key in `/config`.\n")
	}
	return renderMarkdown(b.String(), width)
}

func (m *Model) syncAccountsModalViewport() {
	m.keepModalLineVisible(7 + m.accountIndex)
}

func accountsFromReply(reply app.Reply) app.AccountState {
	if reply.Data != nil {
		if state, ok := reply.Data["accounts"].(app.AccountState); ok {
			return state
		}
	}
	return app.AccountState{}
}

func accountsLoginCmd(bot *app.App) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		flow, err := bot.BeginCodexLogin(ctx)
		if err == nil && flow.VerificationURI != "" {
			_ = openExternalURL(flow.VerificationURI)
		}
		return accountsLoginMsg{Flow: flow, State: bot.AccountStatus(), Err: err}
	}
}

func accountsPollNowCmd(bot *app.App, flow app.CodexDeviceFlow) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := bot.PollCodexLogin(ctx, flow)
		return accountsPollMsg{Flow: flow, Result: result, State: bot.AccountStatus(), Err: err}
	}
}

func accountsPollCmd(bot *app.App, flow app.CodexDeviceFlow, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := bot.PollCodexLogin(ctx, flow)
		return accountsPollMsg{Flow: flow, Result: result, State: bot.AccountStatus(), Err: err}
	})
}

func accountsPollDelay(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 5
	}
	if seconds < 3 {
		seconds = 3
	}
	return time.Duration(seconds) * time.Second
}

func codexPollMessage(result app.CodexLoginPollResult) string {
	switch result.Status {
	case "authorized":
		return firstNonEmptyText(result.Message, "Codex subscription login saved.")
	case "pending":
		return "Waiting for browser authorization..."
	case "slow_down":
		return firstNonEmptyText(result.Message, "OpenAI asked us to slow polling.")
	case "expired":
		return firstNonEmptyText(result.Message, "The Codex login code expired. Press c to start again.")
	case "denied":
		return firstNonEmptyText(result.Message, "Codex login was denied.")
	default:
		return firstNonEmptyText(result.Message, "Codex login status: "+result.Status)
	}
}

func openExternalURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
