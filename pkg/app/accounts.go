package app

import (
	"context"
	"os"
	"strings"
)

type AccountState struct {
	Accounts      []AccountInfo `json:"accounts"`
	KeysPath      string        `json:"keys_path"`
	CodexAuthPath string        `json:"codex_auth_path"`
}

type AccountInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Masked      string `json:"masked,omitempty"`
	Detail      string `json:"detail,omitempty"`
	CanLogin    bool   `json:"can_login,omitempty"`
	CanSaveKey  bool   `json:"can_save_key,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
}

func (a *App) AccountStatus() AccountState {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	return AccountStatus(config)
}

func AccountStatus(config Config) AccountState {
	keys, _ := LoadAPIKeys(config)
	codexStatus := "not signed in"
	codexDetail := "No NullBot Codex subscription login is saved yet. Sign in with ChatGPT to use subscription-backed Codex/OpenAI models without an API key."
	if CodexAuthPresent(config) {
		codexStatus = "signed in"
		codexDetail = "NullBot can use your ChatGPT/Codex subscription directly. No Codex CLI install is required."
	}
	accounts := []AccountInfo{
		{
			ID:       "codex",
			Label:    "Codex subscription",
			Kind:     "browser login",
			Status:   codexStatus,
			Detail:   codexDetail,
			CanLogin: true,
		},
		apiAccountInfo("openai", "OpenAI API", "OPENAI_API_KEY", keys.OpenAI, "Fallback API key for OpenAI billing. When Codex subscription auth is signed in, OpenAI/Codex model selections can use subscription auth instead."),
		apiAccountInfo("anthropic", "Anthropic API", "ANTHROPIC_API_KEY", keys.Anthropic, "Use an Anthropic Console API key."),
		apiAccountInfo("openrouter", "OpenRouter API", "OPENROUTER_API_KEY", keys.OpenRouter, "Use an OpenRouter API key for OpenAI-compatible routing, including Gemini-through-OpenRouter models."),
		apiAccountInfo("google", "Google Gemini API", "GOOGLE_API_KEY / GEMINI_API_KEY", keys.Google, "Stored for MCP servers and future direct Gemini support. Current chat runtime can use Gemini through OpenRouter."),
		apiAccountInfo("brave", "Brave Search API", "BRAVE_API_KEY", keys.ForProvider("brave"), "Powers nullbot-web-mcp web_search when Brave Search is selected or auto-detected."),
	}
	for provider, key := range keys.Other {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || provider == "brave" {
			continue
		}
		label := strings.ToUpper(provider[:1]) + provider[1:] + " API"
		accounts = append(accounts, apiAccountInfo(provider, label, strings.ToUpper(provider)+"_API_KEY", key, "Stored as a named provider key. Direct model runtime support depends on provider integration."))
	}
	return AccountState{
		Accounts:      accounts,
		KeysPath:      KeysPath(config),
		CodexAuthPath: CodexAuthPath(config),
	}
}

func (a *App) BeginCodexLogin(ctx context.Context) (CodexDeviceFlow, error) {
	flow, err := RequestCodexDeviceCode(ctx)
	if err != nil {
		a.logError("codex login start failed", "error", err)
		return CodexDeviceFlow{}, err
	}
	a.logInfo("codex login started", "expires_at", flow.ExpiresAt)
	return flow, nil
}

func (a *App) PollCodexLogin(ctx context.Context, flow CodexDeviceFlow) (CodexLoginPollResult, error) {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	result, err := PollCodexDeviceFlow(ctx, config, flow)
	if err != nil {
		a.logError("codex login poll failed", "error", err)
		return result, err
	}
	if result.Status == "authorized" {
		a.MarkRuntimeDirty("Codex subscription login saved")
	}
	return result, nil
}

func (a *App) accountsCommand(rest string) Reply {
	fields := strings.Fields(strings.ToLower(rest))
	if len(fields) >= 2 && fields[0] == "codex" && fields[1] == "login" || len(fields) >= 2 && fields[0] == "login" && fields[1] == "codex" {
		flow, err := a.BeginCodexLogin(contextOrBackground())
		if err != nil {
			return a.reply("Codex login failed: "+err.Error(), "/accounts", "accounts", map[string]any{
				"accounts": a.AccountStatus(),
			})
		}
		return a.reply("Codex login started. Open "+flow.VerificationURI+" and enter code "+flow.UserCode+".", "/accounts", "accounts", map[string]any{
			"accounts": a.AccountStatus(),
			"flow":     flow,
		})
	}
	return a.reply("Accounts panel opened.", "/accounts", "accounts", map[string]any{
		"accounts": a.AccountStatus(),
	})
}

func apiAccountInfo(id, label, envName, key, detail string) AccountInfo {
	status := "not set"
	masked := MaskSecret(key)
	if strings.TrimSpace(key) != "" {
		status = "saved"
	} else if envValueForAccount(envName) != "" {
		status = "environment"
		masked = "env:" + strings.Fields(envName)[0]
	}
	return AccountInfo{
		ID:         id,
		Label:      label,
		Kind:       "api key",
		Status:     status,
		Masked:     masked,
		Detail:     detail,
		CanSaveKey: true,
	}
}

func envValueForAccount(names string) string {
	for _, name := range strings.FieldsFunc(names, func(r rune) bool {
		return r == '/' || r == ',' || r == ' '
	}) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
