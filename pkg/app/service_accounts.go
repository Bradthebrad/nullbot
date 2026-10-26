package app

// Service account APIs are for the local settings UI, not model-callable tools.
import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/mail"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type ServiceField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
}
type ServiceDefinition struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Group    string         `json:"group"`
	ServerID string         `json:"server_id"`
	Detail   string         `json:"detail"`
	Fields   []ServiceField `json:"fields"`
}
type ServiceAccountInput struct {
	ID         string            `json:"id"`
	Service    string            `json:"service"`
	Label      string            `json:"label"`
	Fields     map[string]string `json:"fields"`
	AllowWrite bool              `json:"allow_write"`
}
type ServiceAccountInfo struct {
	ID         string `json:"id"`
	Service    string `json:"service"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	AllowWrite bool   `json:"allow_write"`
}
type ServiceStatus struct {
	Service  string `json:"service"`
	ServerID string `json:"server_id"`
	Status   string `json:"status"`
}
type ServiceAccountState struct {
	Accounts []ServiceAccountInfo `json:"accounts"`
	Services []ServiceStatus      `json:"services"`
}

// ServiceAccountCatalog returns a fresh catalog. No network or OAuth flow is started.
func ServiceAccountCatalog() []ServiceDefinition {
	oauth := func() []ServiceField {
		return []ServiceField{
			{"access_token", "Access token", true, true},
		}
	}
	return []ServiceDefinition{
		{"gmail", "Gmail", "emails", "nullbot-emails-mcp", "Bring your own OAuth access token. Saving does not verify access or refresh tokens.", oauth()},
		{"gmail_imap", "Gmail (IMAP/SMTP)", "emails", "nullbot-emails-mcp", "Gmail app password with fixed TLS Gmail IMAP/SMTP endpoints; not an OAuth or personal-password login.", []ServiceField{{"email", "Gmail email address", false, true}, {"app_password", "Google app password", true, true}}},
		{"outlook", "Outlook", "emails", "nullbot-emails-mcp", "Bring your own Microsoft Graph access token.", oauth()},
		{"google_drive", "Google Drive", "drives", "nullbot-drives-mcp", "Bring your own OAuth access token.", oauth()},
		{"onedrive", "OneDrive", "drives", "nullbot-drives-mcp", "Bring your own Microsoft Graph access token.", oauth()},
		{"icloud_drive", "iCloud Drive (local)", "drives", "nullbot-drives-mcp", "Local synced folder only; does not sign in to Apple or provide a cloud API.", []ServiceField{{"root", "Local synced folder", false, true}}},
		{"twilio", "Twilio", "messages", "nullbot-messages-mcp", "Twilio API credentials. Sending requires explicit write permission and confirmation.", []ServiceField{{"account_sid", "Account SID", true, true}, {"auth_token", "Auth token", true, true}, {"from", "From number", false, true}}},
		{"whatsapp", "WhatsApp Business", "messages", "nullbot-messages-mcp", "Meta WhatsApp Business Cloud API; not personal WhatsApp login.", []ServiceField{{"access_token", "Access token", true, true}, {"phone_number_id", "Phone number ID", false, true}, {"api_version", "Graph API version", false, true}}},
		{"discord", "Discord (bot)", "messages", "nullbot-messages-mcp", "Bot channel read/send only, subject to Discord permissions and message-content access; no selfbot or personal account login.", []ServiceField{{"bot_token", "Discord bot token", true, true}}},
		{"telegram", "Telegram (bot)", "messages", "nullbot-messages-mcp", "Bot pending updates (not chat history; no acknowledgments or offset) and sending to numeric chat IDs; no personal account login.", []ServiceField{{"bot_token", "Telegram bot token", true, true}}},
		{"signal", "Signal (unofficial bridge)", "messages", "nullbot-messages-mcp", "Outbound only via a separately installed signal-cli-rest-api loopback bridge. Unofficial; no installs, linking, or inbox access.", []ServiceField{{"bridge_url", "Loopback bridge URL with explicit port", false, true}, {"number", "Signal sender (E.164 number)", false, true}, {"api_token", "Bridge API token", true, false}}},
	}
}

func serviceDefinition(id string) (ServiceDefinition, bool) {
	for _, d := range ServiceAccountCatalog() {
		if d.ID == id {
			return d, true
		}
	}
	return ServiceDefinition{}, false
}

var serviceAccountID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Keep these credential contracts aligned with nullbot-messages-mcp/internal/safe/providers.go
// and internal/messages/messages.go (phonePattern). Boundary cases live in service_provider_contract_test.go.
var telegramBotToken = regexp.MustCompile(`^[1-9][0-9]{0,19}:[A-Za-z0-9_-]{30,128}$`)
var discordBotToken = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
var signalBearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
var signalBridgeURL = regexp.MustCompile(`^http://(?:127\.0\.0\.1|\[::1\]):([0-9]{1,5})$`)
var serviceE164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// Match the entire literal URL, not a DNS name or a parsed/normalized alias.
// No trailing slash is accepted; ports must be explicit and usable.
func validSignalBridgeURL(value string) bool {
	match := signalBridgeURL.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	port, err := strconv.Atoi(match[1])
	return err == nil && port >= 1 && port <= 65535
}

func servicePrintableASCII(value string) bool {
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

var errServiceInput = errors.New("invalid service account: check service, identifier, label and required fields")

func validateServiceAccount(input ServiceAccountInput) error {
	d, ok := serviceDefinition(input.Service)
	if !ok || !serviceAccountID.MatchString(input.ID) || len(input.Label) > 120 || strings.TrimSpace(input.Label) == "" || strings.ContainsAny(input.Label, "\r\n\x00") {
		return errServiceInput
	}
	keys := map[string]ServiceField{}
	for _, field := range d.Fields {
		keys[field.Key] = field
		if field.Required && strings.TrimSpace(input.Fields[field.Key]) == "" {
			return errServiceInput
		}
	}
	for key, value := range input.Fields {
		// Gmail's connector strips ASCII spaces before checking app passwords,
		// including Google's grouping spaces and spaces at either end.
		if input.Service == "gmail_imap" && key == "app_password" {
			value = strings.ReplaceAll(value, " ", "")
		}
		if _, ok := keys[key]; !ok || len(value) > 16384 || strings.IndexFunc(value, unicode.IsControl) >= 0 || value != strings.TrimSpace(value) {
			return errServiceInput
		}
	}
	switch input.Service {
	case "gmail_imap":
		// Match nullbot-emails-mcp/internal/emails/gmail_imap.go imapCredentials.
		email := input.Fields["email"]
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || len(email) > 254 || !strings.Contains(email, "@") || !servicePrintableASCII(email) {
			return errServiceInput
		}
		password := strings.ReplaceAll(input.Fields["app_password"], " ", "")
		if len(password) == 0 || len(password) > 256 || !servicePrintableASCII(password) {
			return errServiceInput
		}
	case "discord":
		token := input.Fields["bot_token"]
		if len(token) < 20 || len(token) > 256 || strings.HasPrefix(token, "mfa.") || !discordBotToken.MatchString(token) {
			return errServiceInput
		}
	case "telegram":
		if !telegramBotToken.MatchString(input.Fields["bot_token"]) {
			return errServiceInput
		}
	case "signal":
		token := input.Fields["api_token"]
		if !validSignalBridgeURL(input.Fields["bridge_url"]) || !serviceE164.MatchString(input.Fields["number"]) || (token != "" && (len(token) > 4096 || !signalBearerToken.MatchString(token))) {
			return errServiceInput
		}
	case "icloud_drive":
		root := input.Fields["root"]
		if !filepath.IsAbs(root) || strings.HasPrefix(root, `\\`) || strings.HasPrefix(root, "//") {
			return errServiceInput
		}
	case "twilio":
		if !regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`).MatchString(input.Fields["account_sid"]) || !regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`).MatchString(input.Fields["from"]) {
			return errServiceInput
		}
	case "whatsapp":
		if !regexp.MustCompile(`^[1-9][0-9]{0,30}$`).MatchString(input.Fields["phone_number_id"]) || !regexp.MustCompile(`^v[1-9][0-9]{0,2}\.0$`).MatchString(input.Fields["api_version"]) {
			return errServiceInput
		}
	}
	return nil
}

func (a *App) ServiceAccountStatus() (ServiceAccountState, error) {
	return ServiceAccountStatus(a.Config())
}
func ServiceAccountStatus(config Config) (ServiceAccountState, error) {
	state := ServiceAccountState{Accounts: []ServiceAccountInfo{}, Services: []ServiceStatus{}}
	accounts, err := loadServiceAccounts(config)
	configured := map[string]bool{}
	for _, account := range accounts {
		configured[account.Service] = true
	}
	statuses := map[string]string{}
	for _, d := range ServiceAccountCatalog() {
		status := "not_installed"
		if serviceServerInstalled(config, d.ServerID) {
			status = "disabled"
			if config.EnabledMCPServers[d.ServerID].Enabled {
				status = "not_configured"
				if configured[d.ID] {
					status = "configured_unverified"
				}
			}
		}
		statuses[d.ID] = status
		state.Services = append(state.Services, ServiceStatus{d.ID, d.ServerID, status})
	}
	for _, account := range accounts {
		state.Accounts = append(state.Accounts, ServiceAccountInfo{account.ID, account.Service, account.Label, statuses[account.Service], account.AllowWrite})
	}
	return state, err
}

// SaveServiceAccount replaces all fields for an existing ID. Empty ID creates a new
// random ID, discoverable by refreshing status. Blank fields never retain secrets.
func (a *App) SaveServiceAccount(input ServiceAccountInput) error {
	if err := SaveServiceAccount(a.Config(), input); err != nil {
		return err
	}
	a.MarkRuntimeDirty("Service account settings changed; restart active runs to apply")
	return nil
}
func SaveServiceAccount(config Config, input ServiceAccountInput) error {
	if input.ID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return errServiceStore
		}
		input.ID = hex.EncodeToString(b[:])
	}
	input.Label = strings.TrimSpace(input.Label)
	if err := validateServiceAccount(input); err != nil {
		return err
	}
	serviceStoreMu.Lock()
	defer serviceStoreMu.Unlock()
	accounts, err := readServiceAccounts(config)
	if err != nil {
		return err
	}
	found := false
	for i := range accounts {
		if accounts[i].ID == input.ID {
			if accounts[i].Service != input.Service {
				return errServiceInput
			}
			accounts[i] = input
			found = true
			break
		}
	}
	if !found {
		if len(accounts) >= 100 {
			return errors.New("service account limit reached")
		}
		accounts = append(accounts, input)
	}
	return writeServiceAccounts(config, accounts)
}
func (a *App) DeleteServiceAccount(id string) error {
	if err := DeleteServiceAccount(a.Config(), id); err != nil {
		return err
	}
	a.MarkRuntimeDirty("Service account removed; restart active runs to revoke runtime access")
	return nil
}
func DeleteServiceAccount(config Config, id string) error {
	if !serviceAccountID.MatchString(id) {
		return errServiceInput
	}
	serviceStoreMu.Lock()
	defer serviceStoreMu.Unlock()
	accounts, err := readServiceAccounts(config)
	if err != nil {
		return err
	}
	out := make([]ServiceAccountInput, 0, len(accounts))
	for _, account := range accounts {
		if account.ID != id {
			out = append(out, account)
		}
	}
	return writeServiceAccounts(config, out)
}
