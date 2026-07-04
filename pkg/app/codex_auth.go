package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultCodexBackendURL = "https://chatgpt.com/backend-api/codex"
	codexOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthIssuer       = "https://auth.openai.com"
	codexOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	codexOAuthRedirectURI  = "https://auth.openai.com/deviceauth/callback"
	codexRefreshSkew       = 120 * time.Second
)

type CodexDeviceFlow struct {
	DeviceAuthID    string `json:"device_auth_id"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	IntervalSeconds int    `json:"interval_seconds"`
	ExpiresIn       int    `json:"expires_in"`
	ExpiresAt       string `json:"expires_at"`
}

type CodexLoginPollResult struct {
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
}

type CodexRuntimeCredentials struct {
	Provider    string `json:"provider"`
	BaseURL     string `json:"base_url"`
	AccessToken string `json:"-"`
	AuthMode    string `json:"auth_mode"`
}

type codexAuthStore struct {
	Provider     string    `json:"provider"`
	AuthMode     string    `json:"auth_mode"`
	BaseURL      string    `json:"base_url"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	LastRefresh  time.Time `json:"last_refresh,omitempty"`
	Models       []string  `json:"models,omitempty"`
	Source       string    `json:"source,omitempty"`
}

type codexHTTPError struct {
	Status int
	Body   string
}

func (e codexHTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("codex auth: status %d", e.Status)
	}
	return fmt.Sprintf("codex auth: status %d: %s", e.Status, body)
}

func CodexAuthPath(config ...Config) string {
	if len(config) > 0 && strings.TrimSpace(config[0].AppDir) != "" {
		return filepath.Join(config[0].AppDir, "api", "codex_auth.json")
	}
	return filepath.Join(DefaultAppDir(DefaultPrefix), "api", "codex_auth.json")
}

func CodexCLIAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".codex", "auth.json")
	}
	if override := strings.TrimSpace(os.Getenv("CODEX_HOME")); override != "" {
		return filepath.Join(override, "auth.json")
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func CodexAuthPresent(config ...Config) bool {
	if len(config) > 0 {
		if auth, err := loadCodexAuth(config[0]); err == nil && auth.AccessToken != "" && auth.RefreshToken != "" {
			return true
		}
	}
	if auth, err := importCodexCLIAuth(); err == nil && auth.AccessToken != "" && auth.RefreshToken != "" {
		return true
	}
	return false
}

func RequestCodexDeviceCode(ctx context.Context) (CodexDeviceFlow, error) {
	var out map[string]any
	if err := codexJSON(ctx, http.MethodPost, codexOAuthIssuer+"/api/accounts/deviceauth/usercode", map[string]any{
		"client_id": codexOAuthClientID,
	}, nil, &out); err != nil {
		return CodexDeviceFlow{}, err
	}
	userCode := strings.TrimSpace(stringField(out, "user_code"))
	deviceAuthID := strings.TrimSpace(stringField(out, "device_auth_id"))
	if userCode == "" || deviceAuthID == "" {
		return CodexDeviceFlow{}, errors.New("codex auth: device-code response was missing user_code or device_auth_id")
	}
	interval := intField(out, "interval", 5)
	if interval < 3 {
		interval = 3
	}
	expiresIn := intField(out, "expires_in", 900)
	return CodexDeviceFlow{
		DeviceAuthID:    deviceAuthID,
		UserCode:        userCode,
		VerificationURI: firstNonEmpty(stringField(out, "verification_uri"), codexOAuthIssuer+"/codex/device"),
		IntervalSeconds: interval,
		ExpiresIn:       expiresIn,
		ExpiresAt:       time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

func PollCodexDeviceFlow(ctx context.Context, config Config, flow CodexDeviceFlow) (CodexLoginPollResult, error) {
	if strings.TrimSpace(flow.DeviceAuthID) == "" || strings.TrimSpace(flow.UserCode) == "" {
		return CodexLoginPollResult{}, errors.New("codex auth: device flow is missing device_auth_id or user_code")
	}
	if strings.TrimSpace(flow.ExpiresAt) != "" {
		if expiresAt, err := time.Parse(time.RFC3339, flow.ExpiresAt); err == nil && time.Now().UTC().After(expiresAt) {
			return CodexLoginPollResult{Status: "expired", Message: "The Codex login code expired. Start sign-in again."}, nil
		}
	}
	var out map[string]any
	err := codexJSON(ctx, http.MethodPost, codexOAuthIssuer+"/api/accounts/deviceauth/token", map[string]any{
		"device_auth_id": flow.DeviceAuthID,
		"user_code":      flow.UserCode,
	}, nil, &out)
	if err != nil {
		var httpErr codexHTTPError
		if errors.As(err, &httpErr) && (httpErr.Status == http.StatusForbidden || httpErr.Status == http.StatusNotFound) {
			return CodexLoginPollResult{Status: "pending", IntervalSeconds: flow.IntervalSeconds}, nil
		}
		return CodexLoginPollResult{}, err
	}
	if errorCode := strings.TrimSpace(firstNonEmpty(stringField(out, "error"), stringField(out, "status"))); errorCode != "" {
		switch errorCode {
		case "authorization_pending", "pending":
			return CodexLoginPollResult{Status: "pending", IntervalSeconds: flow.IntervalSeconds}, nil
		case "slow_down":
			return CodexLoginPollResult{Status: "slow_down", IntervalSeconds: intField(out, "interval", flow.IntervalSeconds+2), Message: "OpenAI asked us to slow polling."}, nil
		case "expired_token":
			return CodexLoginPollResult{Status: "expired", Message: "The Codex login code expired. Start sign-in again."}, nil
		case "access_denied", "denied":
			return CodexLoginPollResult{Status: "denied", Message: "Codex login was denied."}, nil
		}
	}
	authorizationCode := strings.TrimSpace(stringField(out, "authorization_code"))
	codeVerifier := strings.TrimSpace(stringField(out, "code_verifier"))
	if authorizationCode == "" || codeVerifier == "" {
		return CodexLoginPollResult{Status: "pending", IntervalSeconds: flow.IntervalSeconds}, nil
	}
	tokens, err := exchangeCodexAuthorizationCode(ctx, authorizationCode, codeVerifier)
	if err != nil {
		return CodexLoginPollResult{}, err
	}
	if err := saveCodexAuth(config, tokens); err != nil {
		return CodexLoginPollResult{}, err
	}
	return CodexLoginPollResult{Status: "authorized", Message: "Codex subscription login saved."}, nil
}

func ResolveCodexRuntimeCredentials(ctx context.Context, config Config, forceRefresh bool) (CodexRuntimeCredentials, error) {
	auth, err := loadCodexAuth(config)
	if err != nil || auth.AccessToken == "" || auth.RefreshToken == "" {
		imported, importErr := importCodexCLIAuth()
		if importErr == nil && imported.AccessToken != "" && imported.RefreshToken != "" {
			auth = imported
			auth.Source = "codex-cli"
			_ = saveCodexAuth(config, auth)
		} else if err != nil && !os.IsNotExist(err) {
			return CodexRuntimeCredentials{}, err
		} else {
			return CodexRuntimeCredentials{}, errors.New("Codex subscription is not signed in. Open Accounts, choose Codex subscription, and sign in with ChatGPT.")
		}
	}
	if forceRefresh || codexAccessTokenIsExpiring(auth.AccessToken, codexRefreshSkew) {
		refreshed, err := refreshCodexOAuthTokens(ctx, auth.RefreshToken)
		if err != nil {
			return CodexRuntimeCredentials{}, err
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = auth.RefreshToken
		}
		if refreshed.BaseURL == "" {
			refreshed.BaseURL = auth.BaseURL
		}
		if refreshed.Models == nil {
			refreshed.Models = auth.Models
		}
		auth = refreshed
		if err := saveCodexAuth(config, auth); err != nil {
			return CodexRuntimeCredentials{}, err
		}
	}
	return CodexRuntimeCredentials{
		Provider:    "codex",
		BaseURL:     firstNonEmpty(auth.BaseURL, defaultCodexBackendURL),
		AccessToken: auth.AccessToken,
		AuthMode:    firstNonEmpty(auth.AuthMode, "chatgpt"),
	}, nil
}

func FetchCodexModels(ctx context.Context, config Config) ([]string, error) {
	creds, err := ResolveCodexRuntimeCredentials(ctx, config, false)
	if err != nil {
		return nil, err
	}
	return fetchCodexModelsWithToken(ctx, creds.AccessToken)
}

func exchangeCodexAuthorizationCode(ctx context.Context, authorizationCode, codeVerifier string) (codexAuthStore, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", authorizationCode)
	values.Set("redirect_uri", codexOAuthRedirectURI)
	values.Set("client_id", codexOAuthClientID)
	values.Set("code_verifier", codeVerifier)
	var out map[string]any
	if err := codexForm(ctx, codexOAuthTokenURL, values, &out); err != nil {
		return codexAuthStore{}, err
	}
	return codexStoreFromTokenResponse(out)
}

func refreshCodexOAuthTokens(ctx context.Context, refreshToken string) (codexAuthStore, error) {
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refreshToken)
	values.Set("client_id", codexOAuthClientID)
	var out map[string]any
	if err := codexForm(ctx, codexOAuthTokenURL, values, &out); err != nil {
		return codexAuthStore{}, err
	}
	auth, err := codexStoreFromTokenResponse(out)
	if auth.RefreshToken == "" {
		auth.RefreshToken = refreshToken
	}
	return auth, err
}

func codexStoreFromTokenResponse(out map[string]any) (codexAuthStore, error) {
	accessToken := strings.TrimSpace(stringField(out, "access_token"))
	if accessToken == "" {
		return codexAuthStore{}, errors.New("codex auth: token response did not include access_token")
	}
	return codexAuthStore{
		Provider:     "codex",
		AuthMode:     "chatgpt",
		BaseURL:      defaultCodexBackendURL,
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(stringField(out, "refresh_token")),
		IDToken:      strings.TrimSpace(stringField(out, "id_token")),
		AccountID:    strings.TrimSpace(stringField(out, "account_id")),
		TokenType:    strings.TrimSpace(stringField(out, "token_type")),
		Scope:        strings.TrimSpace(stringField(out, "scope")),
		ExpiresIn:    intField(out, "expires_in", 0),
		LastRefresh:  time.Now().UTC(),
	}, nil
}

func loadCodexAuth(config Config) (codexAuthStore, error) {
	data, err := os.ReadFile(CodexAuthPath(config))
	if err != nil {
		return codexAuthStore{}, err
	}
	var auth codexAuthStore
	if err := json.Unmarshal(data, &auth); err != nil {
		return codexAuthStore{}, err
	}
	if auth.BaseURL == "" {
		auth.BaseURL = defaultCodexBackendURL
	}
	return auth, nil
}

func saveCodexAuth(config Config, auth codexAuthStore) error {
	if auth.Provider == "" {
		auth.Provider = "codex"
	}
	if auth.AuthMode == "" {
		auth.AuthMode = "chatgpt"
	}
	if auth.BaseURL == "" {
		auth.BaseURL = defaultCodexBackendURL
	}
	if auth.LastRefresh.IsZero() {
		auth.LastRefresh = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(CodexAuthPath(config)), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(CodexAuthPath(config), append(data, '\n'), 0600)
}

func importCodexCLIAuth() (codexAuthStore, error) {
	data, err := os.ReadFile(CodexCLIAuthPath())
	if err != nil {
		return codexAuthStore{}, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return codexAuthStore{}, err
	}
	accessToken := findStringField(raw, "access_token")
	refreshToken := findStringField(raw, "refresh_token")
	if accessToken == "" || refreshToken == "" {
		return codexAuthStore{}, errors.New("codex auth: local Codex auth file did not contain access_token and refresh_token")
	}
	return codexAuthStore{
		Provider:     "codex",
		AuthMode:     "chatgpt",
		BaseURL:      defaultCodexBackendURL,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      findStringField(raw, "id_token"),
		AccountID:    findStringField(raw, "account_id"),
		LastRefresh:  time.Now().UTC(),
		Source:       "codex-cli",
	}, nil
}

func fetchCodexModelsWithToken(ctx context.Context, accessToken string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultCodexBackendURL+"/models?client_version=1.0.0", nil)
	if err != nil {
		return nil, err
	}
	for key, value := range codexBackendHeaders(accessToken) {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, codexHTTPError{Status: resp.StatusCode, Body: string(data)}
	}
	var decoded struct {
		Models []struct {
			Slug       string `json:"slug"`
			Name       string `json:"name"`
			Visibility string `json:"visibility"`
			Priority   int    `json:"priority"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(decoded.Models))
	seen := map[string]bool{}
	for _, model := range decoded.Models {
		slug := strings.TrimSpace(model.Slug)
		if slug == "" || seen[slug] || strings.EqualFold(model.Visibility, "hide") || strings.EqualFold(model.Visibility, "hidden") {
			continue
		}
		seen[slug] = true
		models = append(models, slug)
	}
	return models, nil
}

func codexJSON(ctx context.Context, method, endpoint string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return doCodexHTTP(req, out)
}

func codexForm(ctx context.Context, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return doCodexHTTP(req, out)
}

func doCodexHTTP(req *http.Request, out any) error {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexHTTPError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func codexBackendHeaders(accessToken string) map[string]string {
	headers := map[string]string{
		"Accept":     "application/json, text/event-stream",
		"Origin":     "https://chatgpt.com",
		"Referer":    "https://chatgpt.com/codex",
		"User-Agent": "NullBot Codex Subscription",
	}
	if strings.TrimSpace(accessToken) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(accessToken)
	}
	return headers
}

func codexAccessTokenIsExpiring(token string, skew time.Duration) bool {
	exp, err := jwtExpiry(token)
	if err != nil {
		return true
	}
	return time.Until(exp) <= skew
}

func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, errors.New("not a jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	exp, ok := numberField(claims, "exp")
	if !ok || exp <= 0 {
		return time.Time{}, errors.New("jwt exp missing")
	}
	return time.Unix(int64(exp), 0), nil
}

func findStringField(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if direct := strings.TrimSpace(stringFromAny(typed[key])); direct != "" {
			return direct
		}
		for _, child := range typed {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func stringField(values map[string]any, key string) string {
	return stringFromAny(values[key])
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func intField(values map[string]any, key string, fallback int) int {
	number, ok := numberField(values, key)
	if !ok {
		return fallback
	}
	return int(number)
}

func numberField(values map[string]any, key string) (float64, bool) {
	switch typed := values[key].(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}
