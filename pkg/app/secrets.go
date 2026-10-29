package app

// Secrets live in one encrypted vault (pkg/vault) at <AppDir>/vault/vault.json:
// API keys, the ChatGPT sign-in, GitHub tokens, SSH keys, cloud credentials
// and anything else the user adds. Agents never read a value. They list
// secrets by name and use them through secret_run, which puts the values in a
// command's environment and removes them from everything the command prints;
// a tool server the user binds a secret to gets it in its environment.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

// Secret kinds.
const (
	SecretAPIKey      = "api_key"
	SecretGitHubToken = "github_token"
	SecretSSHKey      = "ssh_key"
	SecretAWS         = "aws"
	SecretGCP         = "gcp"
	SecretGeneric     = "generic"
)

// Secret is one stored credential. Values never leave NullBot; Public holds
// what may be shown (a login, a public key, a region).
type Secret struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Description string            `json:"description,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	Public      map[string]string `json:"public,omitempty"`
	// AgentUse lets agents run commands with this secret (secret_run).
	AgentUse bool `json:"agent_use"`
	// Servers are tool servers that get this secret in their environment
	// when they start.
	Servers   []string  `json:"servers,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// SecretInfo is a secret without its values: what the UI, the CLI and agents
// see.
type SecretInfo struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Public      map[string]string `json:"public,omitempty"`
	// Set lists the secret fields that hold a value.
	Set []string `json:"set"`
	// Provides lists the environment variables a command gets.
	Provides  []string  `json:"provides"`
	AgentUse  bool      `json:"agent_use"`
	Servers   []string  `json:"servers,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	// Hint says what the last-four of the main value are, for recognizing
	// a key without showing it.
	Hint string `json:"hint,omitempty"`
}

// SecretInput creates or changes a secret. Values left empty keep what is
// stored; ClearValues removes stored fields.
type SecretInput struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Description string            `json:"description,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	Public      map[string]string `json:"public,omitempty"`
	ClearValues []string          `json:"clear_values,omitempty"`
	AgentUse    bool              `json:"agent_use"`
	Servers     []string          `json:"servers,omitempty"`
}

// SecretField describes one field of a kind, for forms.
type SecretField struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Secret    bool   `json:"secret"`
	Required  bool   `json:"required,omitempty"`
	Multiline bool   `json:"multiline,omitempty"`
	Hint      string `json:"hint,omitempty"`
	// Derived fields are filled in by NullBot (a fingerprint, a login).
	Derived bool `json:"derived,omitempty"`
}

// SecretKindInfo describes a kind of secret.
type SecretKindInfo struct {
	Kind        string        `json:"kind"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Fields      []SecretField `json:"fields"`
	// FileBased kinds are written to a temporary file for a command; tool
	// servers cannot get them.
	FileBased bool `json:"file_based,omitempty"`
}

var secretKinds = []SecretKindInfo{
	{Kind: SecretAPIKey, Label: "API key", Description: "A model or service provider's API key, used by NullBot itself and, when you allow it, by commands.", Fields: []SecretField{
		{Name: "provider", Label: "Provider", Required: true, Hint: "openai, anthropic, openrouter, google, brave or another name"},
		{Name: "value", Label: "Key", Secret: true, Required: true},
	}},
	{Kind: SecretGitHubToken, Label: "GitHub token", Description: "A personal access token. Commands get GH_TOKEN and GITHUB_TOKEN, and git over HTTPS signs in with it.", Fields: []SecretField{
		{Name: "token", Label: "Token", Secret: true, Required: true, Hint: "A fine-grained or classic personal access token"},
		{Name: "host", Label: "Host", Hint: "github.com, or your GitHub Enterprise host"},
		{Name: "login", Label: "Signed in as", Derived: true},
		{Name: "scopes", Label: "Scopes", Derived: true},
	}},
	{Kind: SecretSSHKey, Label: "SSH key", Description: "A private key without a passphrase. Commands get it as a temporary file, and git uses it through GIT_SSH_COMMAND.", FileBased: true, Fields: []SecretField{
		{Name: "private_key", Label: "Private key", Secret: true, Required: true, Multiline: true, Hint: "OpenSSH or PEM, without a passphrase"},
		{Name: "public_key", Label: "Public key", Derived: true},
		{Name: "fingerprint", Label: "Fingerprint", Derived: true},
		{Name: "host", Label: "Used for", Hint: "Optional: github.com, a server's name"},
	}},
	{Kind: SecretAWS, Label: "AWS credentials", Description: "An access key for the AWS CLI and SDKs.", Fields: []SecretField{
		{Name: "access_key_id", Label: "Access key ID", Secret: true, Required: true},
		{Name: "secret_access_key", Label: "Secret access key", Secret: true, Required: true},
		{Name: "session_token", Label: "Session token", Secret: true, Hint: "Only for temporary credentials"},
		{Name: "region", Label: "Region", Hint: "us-east-1"},
	}},
	{Kind: SecretGCP, Label: "Google Cloud service account", Description: "A service account key file. Commands get it as a temporary file through GOOGLE_APPLICATION_CREDENTIALS.", FileBased: true, Fields: []SecretField{
		{Name: "credentials_json", Label: "Key file (JSON)", Secret: true, Required: true, Multiline: true},
		{Name: "project_id", Label: "Project", Hint: "Taken from the key file when empty"},
		{Name: "client_email", Label: "Service account", Derived: true},
	}},
	{Kind: SecretGeneric, Label: "Other secret", Description: "Any value, given to commands in an environment variable you name, or as a temporary file.", Fields: []SecretField{
		{Name: "value", Label: "Value", Secret: true, Required: true, Multiline: true},
		{Name: "env", Label: "Environment variable", Required: true, Hint: "MY_SERVICE_TOKEN"},
		{Name: "as_file", Label: "Pass as a file", Hint: "yes to write the value to a temporary file and put its path in the variable"},
	}},
}

// SecretKinds lists the kinds of secret NullBot understands.
func SecretKinds() []SecretKindInfo { return append([]SecretKindInfo(nil), secretKinds...) }

func secretKind(kind string) (SecretKindInfo, bool) {
	for _, k := range secretKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return SecretKindInfo{}, false
}

// vaultPayload is what the vault encrypts.
type vaultPayload struct {
	Version int       `json:"version"`
	Secrets []Secret  `json:"secrets"`
	System  systemSet `json:"system,omitempty"`
}

// systemSet holds credentials NullBot manages itself; agents never see them.
type systemSet struct {
	CodexAuth *codexAuthStore `json:"codex_auth,omitempty"`
}

var (
	secretNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	providerPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// VaultPath is where the vault is kept.
func VaultPath(config Config) string {
	return filepath.Join(config.AppDir, "vault", "vault.json")
}

var (
	vaultsMu sync.Mutex
	vaults   = map[string]*vault.Vault{}
	// vaultOptions lets tests replace the keychain.
	vaultOptions = vault.Options{}
)

// openVault returns the process-wide handle for this configuration's vault,
// so an unlocked key is reused.
func openVault(config Config) *vault.Vault {
	path := VaultPath(config)
	vaultsMu.Lock()
	defer vaultsMu.Unlock()
	if v, ok := vaults[path]; ok {
		return v
	}
	v := vault.New(path, vaultOptions)
	vaults[path] = v
	return v
}

func resetVaultCache() {
	vaultsMu.Lock()
	vaults = map[string]*vault.Vault{}
	vaultsMu.Unlock()
	redactorsMu.Lock()
	redactors = map[string]*secretRedactor{}
	redactorsMu.Unlock()
}

// VaultInfo is the vault's state for the UI and CLI.
type VaultInfo struct {
	vault.Status
	Secrets int `json:"secrets"`
	// Plaintext lists files that still hold secrets in plain text because
	// they could not be moved into the vault.
	Plaintext []string `json:"plaintext,omitempty"`
	// ProtectionLabel says in words what protects the vault.
	ProtectionLabel string `json:"protection_label,omitempty"`
	KeychainLabel   string `json:"keychain_label,omitempty"`
}

func protectionLabel(p vault.Protection) string {
	switch p {
	case vault.DPAPI:
		return "Windows (DPAPI), for your Windows account"
	case vault.Keychain:
		return "the macOS keychain"
	case vault.SecretService:
		return "the system keyring (Secret Service)"
	case vault.Passphrase:
		return "a passphrase"
	}
	return ""
}

// VaultStatus reports the vault's state. Opening the vault may move legacy
// plain-text secrets into it.
func VaultStatus(config Config) VaultInfo {
	payload, _ := loadSecretsPayload(config)
	info := VaultInfo{Status: openVault(config).Status()}
	info.Secrets = len(payload.Secrets)
	info.ProtectionLabel = protectionLabel(info.Protection)
	info.KeychainLabel = protectionLabel(info.Keychain)
	for _, path := range []string{KeysPath(config), CodexAuthPath(config)} {
		if _, err := os.Stat(path); err == nil {
			info.Plaintext = append(info.Plaintext, path)
		}
	}
	return info
}

// CreateVault makes the vault. protection is "", "dpapi", "keychain",
// "secret-service" or "passphrase".
func CreateVault(config Config, protection, passphrase string) error {
	v := openVault(config)
	if err := v.Create(vault.Protection(protection), passphrase); err != nil {
		return err
	}
	invalidateRedactor(config)
	_, err := loadSecretsPayload(config) // moves legacy files in
	return err
}

// UnlockVault opens a passphrase vault for this process.
func UnlockVault(config Config, passphrase string) error {
	if err := openVault(config).Unlock(passphrase); err != nil {
		return err
	}
	// The values just became known: redact them from now on.
	invalidateRedactor(config)
	_, err := loadSecretsPayload(config)
	return err
}

// LockVault forgets the vault key in this process; a passphrase vault then
// needs the passphrase again.
func LockVault(config Config) {
	openVault(config).Lock()
	invalidateRedactor(config)
}

// ReprotectVault moves the vault to another protection or passphrase.
func ReprotectVault(config Config, protection, passphrase string) error {
	err := openVault(config).Reprotect(vault.Protection(protection), passphrase)
	if err == nil {
		invalidateRedactor(config)
	}
	return err
}

// loadSecretsPayload decrypts the vault. A missing vault is empty. Legacy
// plain-text files are moved in whenever the vault can be written.
func loadSecretsPayload(config Config) (vaultPayload, error) {
	v := openVault(config)
	var payload vaultPayload
	if legacyPresent(config) {
		if err := migrateLegacySecrets(config, v); err != nil && !errors.Is(err, vault.ErrNoKeychain) {
			// Keep going: the vault may still be readable.
			_ = err
		}
	}
	plain, err := v.Read()
	if errors.Is(err, vault.ErrNotFound) {
		return payload, nil
	}
	if err != nil {
		return payload, err
	}
	defer clear(plain)
	if err := json.Unmarshal(plain, &payload); err != nil {
		return vaultPayload{}, vault.ErrCorrupt
	}
	rememberSecrets(config, payload)
	return payload, nil
}

// updateSecrets changes the vault under its lock, creating it if needed.
func updateSecrets(config Config, fn func(*vaultPayload) error) error {
	v := openVault(config)
	if !v.Exists() {
		if err := v.Create(vault.Auto, ""); err != nil && !errors.Is(err, vault.ErrExists) {
			return err
		}
	}
	err := v.Update(func(plain []byte) ([]byte, error) {
		var payload vaultPayload
		if len(plain) > 0 {
			if err := json.Unmarshal(plain, &payload); err != nil {
				return nil, vault.ErrCorrupt
			}
		}
		if err := fn(&payload); err != nil {
			return nil, err
		}
		payload.Version = 1
		rememberSecrets(config, payload)
		return json.Marshal(payload)
	})
	if err == nil {
		invalidateRedactor(config)
	}
	return err
}

func legacyPresent(config Config) bool {
	for _, path := range []string{KeysPath(config), CodexAuthPath(config)} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

// migrateLegacySecrets moves api/keys.json and api/codex_auth.json into the
// vault and deletes them. Values already in the vault win.
func migrateLegacySecrets(config Config, v *vault.Vault) error {
	keys, keysErr := readLegacyAPIKeys(config)
	codex, codexErr := readLegacyCodexAuth(config)
	if keysErr != nil && codexErr != nil {
		return nil
	}
	if !v.Exists() {
		if err := v.Create(vault.Auto, ""); err != nil && !errors.Is(err, vault.ErrExists) {
			return err
		}
	}
	keysMoved, codexMoved := false, false
	err := v.Update(func(plain []byte) ([]byte, error) {
		var payload vaultPayload
		if len(plain) > 0 {
			if err := json.Unmarshal(plain, &payload); err != nil {
				return nil, vault.ErrCorrupt
			}
		}
		if keysErr == nil {
			if err := addMissingAPIKeys(&payload, keys, "migration"); err != nil {
				// Keep what can be kept; the file stays for the rest.
				migrationNote(config, err)
			}
			keysMoved = apiKeysStored(payload, keys)
		}
		if codexErr == nil && payload.System.CodexAuth == nil {
			payload.System.CodexAuth = &codex
		}
		codexMoved = codexErr == nil && payload.System.CodexAuth != nil
		payload.Version = 1
		return json.Marshal(payload)
	})
	if err != nil {
		return err
	}
	invalidateRedactor(config)
	// A file is deleted only once everything in it is in the vault.
	if keysMoved {
		removeLegacyFile(KeysPath(config))
	}
	if codexMoved {
		removeLegacyFile(CodexAuthPath(config))
	}
	return nil
}

var migrationNotes sync.Map

// migrationNote logs, once per folder, why a legacy key stayed behind.
func migrationNote(config Config, err error) {
	if _, seen := migrationNotes.LoadOrStore(config.AppDir+"|"+err.Error(), true); !seen {
		NewLogger(config).Info("legacy key left in api/keys.json", "reason", err.Error())
	}
}

// removeLegacyFile overwrites a plain-text secrets file before deleting it.
// On SSDs and copy-on-write file systems the old blocks may survive; the
// overwrite only helps on simple disks.
func removeLegacyFile(path string) {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			_, _ = f.Write(make([]byte, info.Size()))
			_ = f.Sync()
			_ = f.Close()
		}
	}
	_ = os.Remove(path)
}

// --- API keys in the vault ------------------------------------------------

var namedProviders = []string{"openai", "anthropic", "openrouter", "google"}

func apiKeysFromPayload(payload vaultPayload) APIKeys {
	var keys APIKeys
	set := func(provider, value string) {
		switch provider {
		case "openai":
			keys.OpenAI = value
		case "anthropic":
			keys.Anthropic = value
		case "openrouter":
			keys.OpenRouter = value
		case "google", "gemini":
			keys.Google = value
		default:
			if keys.Other == nil {
				keys.Other = map[string]string{}
			}
			keys.Other[provider] = value
		}
	}
	// A secret named after its provider is the provider's key; other API key
	// secrets fill in providers without one.
	for _, pass := range []bool{true, false} {
		for _, s := range payload.Secrets {
			if s.Kind != SecretAPIKey || s.Values["value"] == "" {
				continue
			}
			provider := s.Public["provider"]
			if (s.Name == provider) != pass {
				continue
			}
			if !pass && keys.ForProvider(provider) != "" {
				continue
			}
			set(provider, s.Values["value"])
		}
	}
	return keys
}

// NormalizeProvider turns a provider name into the form secrets use:
// lowercase, with anything but letters, digits, dashes and underscores made
// a dash ("Perplexity AI" -> "perplexity-ai", "x.ai" -> "x-ai"). Empty when
// nothing usable is left.
func NormalizeProvider(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			r = '-'
		}
		if r == '-' {
			if dash || b.Len() == 0 {
				continue
			}
			dash = true
		} else {
			dash = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-")
	}
	if !providerPattern.MatchString(out) {
		return ""
	}
	return out
}

// canonicalProvider is the provider a provider-named API key secret serves;
// "gemini" is the Google key.
func canonicalProvider(s Secret) (string, bool) {
	if s.Kind != SecretAPIKey || s.Name != s.Public["provider"] {
		return "", false
	}
	if s.Name == "gemini" {
		return "google", true
	}
	return s.Name, true
}

// wantedAPIKeys is keys by normalized provider. It fails on a name that
// cannot be normalized, or two different keys for one provider.
func wantedAPIKeys(keys APIKeys) (map[string]string, error) {
	want := map[string]string{
		"openai":     keys.OpenAI,
		"anthropic":  keys.Anthropic,
		"openrouter": keys.OpenRouter,
		"google":     keys.Google,
	}
	names := make([]string, 0, len(keys.Other))
	for name := range keys.Other {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := strings.TrimSpace(keys.Other[name])
		provider := NormalizeProvider(name)
		if provider == "gemini" {
			provider = "google"
		}
		if provider == "" {
			if value == "" {
				continue
			}
			return nil, fmt.Errorf("%q is not a usable provider name; use letters and digits", name)
		}
		if value == "" {
			if _, ok := want[provider]; !ok {
				want[provider] = ""
			}
			continue
		}
		if prev := strings.TrimSpace(want[provider]); prev != "" && prev != value {
			return nil, fmt.Errorf("two different keys were given for %s", provider)
		}
		want[provider] = value
	}
	return want, nil
}

// setAPIKeysInPayload makes the provider-named API key secrets ("openai",
// "brave") match keys: a provider left empty loses its secret. API key
// secrets with other names ("openai-work") are left alone, and a key that
// one of them already holds is not copied. A value that is a mask (what a
// form shows instead of the key) never replaces a key.
func setAPIKeysInPayload(payload *vaultPayload, keys APIKeys, by string) error {
	want, err := wantedAPIKeys(keys)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	kept := payload.Secrets[:0]
	for _, s := range payload.Secrets {
		if provider, ok := canonicalProvider(s); ok {
			value := strings.TrimSpace(want[provider])
			delete(want, provider)
			switch {
			case looksMasked(value):
				// Unchanged.
			case value == "":
				clear(s.Values)
				continue
			case s.Values["value"] != value:
				s.Values = map[string]string{"value": value}
				s.Updated = now
			}
		}
		kept = append(kept, s)
	}
	payload.Secrets = kept
	providers := make([]string, 0, len(want))
	for p := range want {
		providers = append(providers, p)
	}
	sort.Strings(providers)
next:
	for _, provider := range providers {
		value := strings.TrimSpace(want[provider])
		if value == "" || looksMasked(value) {
			continue
		}
		for _, s := range payload.Secrets {
			if s.Kind == SecretAPIKey && apiProvider(s) == provider && s.Values["value"] == value {
				continue next
			}
		}
		if i := secretIndex(*payload, provider); i >= 0 {
			return fmt.Errorf("a %s secret is already named %s; rename it in Secrets to save the %s API key", payload.Secrets[i].Kind, provider, provider)
		}
		payload.Secrets = append(payload.Secrets, Secret{
			Name: provider, Kind: SecretAPIKey,
			Values: map[string]string{"value": value}, Public: map[string]string{"provider": provider},
			CreatedBy: by, Created: now, Updated: now,
		})
	}
	return nil
}

func apiProvider(s Secret) string {
	if p := s.Public["provider"]; p != "gemini" {
		return p
	}
	return "google"
}

// addMissingAPIKeys adds keys for providers that have no API key secret
// yet. It reports the providers it could not store.
func addMissingAPIKeys(payload *vaultPayload, keys APIKeys, by string) error {
	merged := apiKeysFromPayload(*payload)
	if merged.Other == nil {
		merged.Other = map[string]string{}
	}
	fill := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	fill(&merged.OpenAI, keys.OpenAI)
	fill(&merged.Anthropic, keys.Anthropic)
	fill(&merged.OpenRouter, keys.OpenRouter)
	fill(&merged.Google, keys.Google)
	for name, v := range keys.Other {
		provider := NormalizeProvider(name)
		if provider == "" {
			return fmt.Errorf("%q is not a usable provider name", name)
		}
		if merged.ForProvider(provider) == "" {
			merged.Other[provider] = v
		}
	}
	return setAPIKeysInPayload(payload, merged, by)
}

// apiKeysStored reports whether every key in keys is now in payload.
func apiKeysStored(payload vaultPayload, keys APIKeys) bool {
	values := map[string]bool{}
	for _, s := range payload.Secrets {
		if s.Kind == SecretAPIKey {
			values[s.Values["value"]] = true
		}
	}
	stored := func(v string) bool { return strings.TrimSpace(v) == "" || values[strings.TrimSpace(v)] }
	ok := stored(keys.OpenAI) && stored(keys.Anthropic) && stored(keys.OpenRouter) && stored(keys.Google)
	for _, v := range keys.Other {
		ok = ok && stored(v)
	}
	return ok
}

func secretIndex(payload vaultPayload, name string) int {
	for i, s := range payload.Secrets {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// --- listing and editing ---------------------------------------------------

// ListSecrets returns every secret without its values.
func ListSecrets(config Config) ([]SecretInfo, error) {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return nil, err
	}
	out := make([]SecretInfo, 0, len(payload.Secrets))
	for _, s := range payload.Secrets {
		out = append(out, secretInfo(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func secretInfo(s Secret) SecretInfo {
	kind, _ := secretKind(s.Kind)
	info := SecretInfo{
		Name: s.Name, Kind: s.Kind, Label: kind.Label, Description: s.Description,
		Public: map[string]string{}, Set: []string{}, Provides: secretEnvNames(s),
		AgentUse: s.AgentUse, Servers: append([]string(nil), s.Servers...),
		CreatedBy: s.CreatedBy, Created: s.Created, Updated: s.Updated,
	}
	for k, v := range s.Public {
		info.Public[k] = v
	}
	for k, v := range s.Values {
		if v != "" {
			info.Set = append(info.Set, k)
		}
	}
	sort.Strings(info.Set)
	main := ""
	switch s.Kind {
	case SecretAPIKey, SecretGeneric:
		main = s.Values["value"]
	case SecretGitHubToken:
		main = s.Values["token"]
	case SecretAWS:
		main = s.Values["access_key_id"]
	}
	if len(main) >= 12 && !strings.Contains(main, "\n") {
		info.Hint = "ends in " + main[len(main)-4:]
	}
	return info
}

// SaveSecret creates or changes a secret.
func SaveSecret(config Config, input SecretInput) (SecretInfo, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !secretNamePattern.MatchString(input.Name) {
		return SecretInfo{}, fmt.Errorf("name a secret with lowercase letters, digits, dots, dashes or underscores (up to 63), such as github or aws-prod")
	}
	kind, ok := secretKind(input.Kind)
	if !ok {
		return SecretInfo{}, fmt.Errorf("unknown kind of secret %q", input.Kind)
	}
	var saved Secret
	err := updateSecrets(config, func(payload *vaultPayload) error {
		now := time.Now().UTC().Truncate(time.Second)
		i := secretIndex(*payload, input.Name)
		s := Secret{Name: input.Name, Kind: input.Kind, CreatedBy: "user", Created: now}
		if i >= 0 {
			s = payload.Secrets[i]
			if s.Kind != input.Kind {
				return fmt.Errorf("%s is a %s; remove it first to store a %s under that name", s.Name, s.Kind, input.Kind)
			}
		}
		if s.Values == nil {
			s.Values = map[string]string{}
		}
		if s.Public == nil {
			s.Public = map[string]string{}
		}
		before := s.Values["token"]
		for _, field := range input.ClearValues {
			delete(s.Values, field)
		}
		for _, field := range kind.Fields {
			if field.Derived {
				continue
			}
			if field.Secret {
				if v, ok := input.Values[field.Name]; ok && strings.TrimSpace(v) != "" {
					s.Values[field.Name] = normalizeSecretValue(field, v)
				}
				continue
			}
			if v, ok := input.Public[field.Name]; ok {
				v = strings.TrimSpace(v)
				if v == "" {
					delete(s.Public, field.Name)
				} else {
					s.Public[field.Name] = v
				}
			}
		}
		if s.Kind == SecretGitHubToken && s.Values["token"] != before {
			// A new token may belong to someone else; verify it again.
			for _, k := range []string{"login", "scopes", "verified"} {
				delete(s.Public, k)
			}
		}
		s.Description = strings.TrimSpace(input.Description)
		s.AgentUse = input.AgentUse
		s.Servers = cleanServerList(input.Servers)
		if err := deriveSecretPublic(&s); err != nil {
			return err
		}
		if err := validateSecret(s); err != nil {
			return err
		}
		s.Updated = now
		if i >= 0 {
			payload.Secrets[i] = s
		} else {
			payload.Secrets = append(payload.Secrets, s)
		}
		saved = s
		return nil
	})
	if err != nil {
		return SecretInfo{}, err
	}
	return secretInfo(saved), nil
}

func normalizeSecretValue(field SecretField, v string) string {
	if field.Multiline {
		v = strings.ReplaceAll(v, "\r\n", "\n")
		v = strings.TrimSpace(v)
		if strings.Contains(v, "PRIVATE KEY-----") {
			v += "\n"
		}
		return v
	}
	return strings.TrimSpace(v)
}

func cleanServerList(servers []string) []string {
	var out []string
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// RemoveSecret deletes a secret.
func RemoveSecret(config Config, name string) error {
	return updateSecrets(config, func(payload *vaultPayload) error {
		i := secretIndex(*payload, name)
		if i < 0 {
			return fmt.Errorf("there is no secret named %s", name)
		}
		clear(payload.Secrets[i].Values)
		payload.Secrets = append(payload.Secrets[:i], payload.Secrets[i+1:]...)
		return nil
	})
}

// deriveSecretPublic fills in what can be worked out from a secret's values.
func deriveSecretPublic(s *Secret) error {
	switch s.Kind {
	case SecretAPIKey:
		s.Public["provider"] = NormalizeProvider(s.Public["provider"])
	case SecretSSHKey:
		if key := s.Values["private_key"]; key != "" {
			pub, err := sshPublicKeyFromPrivate(key)
			if err != nil {
				return err
			}
			s.Public["public_key"] = pub.authorizedKey(firstNonEmpty(pub.comment, s.Name+"@nullbot"))
			s.Public["fingerprint"] = pub.fingerprint()
		}
	case SecretGitHubToken:
		if s.Public["host"] == "" {
			s.Public["host"] = "github.com"
		}
		s.Public["host"] = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(s.Public["host"]), "https://"), "http://"), "/")
	case SecretGCP:
		if raw := s.Values["credentials_json"]; raw != "" {
			var key struct {
				Type        string `json:"type"`
				ProjectID   string `json:"project_id"`
				ClientEmail string `json:"client_email"`
				PrivateKey  string `json:"private_key"`
			}
			if err := json.Unmarshal([]byte(raw), &key); err != nil {
				// The parser's message can quote the key's text; leave it out.
				return errors.New("the Google Cloud key is not valid JSON; paste the whole key file")
			}
			if key.Type == "" || (key.Type == "service_account" && key.PrivateKey == "") {
				return errors.New("the Google Cloud key file is missing its type or private key; download a JSON key for the service account")
			}
			if key.ClientEmail != "" {
				s.Public["client_email"] = key.ClientEmail
			}
			if s.Public["project_id"] == "" && key.ProjectID != "" {
				s.Public["project_id"] = key.ProjectID
			}
		}
	case SecretGeneric:
		if v := strings.ToLower(s.Public["as_file"]); v != "" {
			if v == "yes" || v == "true" || v == "1" {
				s.Public["as_file"] = "yes"
			} else {
				delete(s.Public, "as_file")
			}
		}
	}
	return nil
}

func validateSecret(s Secret) error {
	kind, _ := secretKind(s.Kind)
	for _, field := range kind.Fields {
		if !field.Required {
			continue
		}
		if field.Secret && s.Values[field.Name] == "" {
			return fmt.Errorf("%s needs a %s", s.Name, strings.ToLower(field.Label))
		}
		if !field.Secret && s.Public[field.Name] == "" {
			return fmt.Errorf("%s needs a %s", s.Name, strings.ToLower(field.Label))
		}
	}
	for k, v := range s.Values {
		if len(v) > 64*1024 {
			return fmt.Errorf("%s's %s is larger than 64 KiB", s.Name, k)
		}
	}
	switch s.Kind {
	case SecretAPIKey:
		if !providerPattern.MatchString(s.Public["provider"]) {
			return fmt.Errorf("name the provider with lowercase letters, digits, dashes or underscores")
		}
	case SecretGeneric:
		if err := validateEnvName(s.Public["env"]); err != nil {
			return err
		}
	}
	if len(s.Servers) > 0 && (kind.FileBased || s.Public["as_file"] == "yes") {
		return fmt.Errorf("%s is passed as a file, which tool servers cannot get; use it with secret_run instead", s.Name)
	}
	return nil
}

// Environment variables a secret may not set: they change how programs run
// or belong to NullBot.
var reservedEnvNames = map[string]bool{
	"PATH": true, "PATHEXT": true, "COMSPEC": true, "SYSTEMROOT": true, "WINDIR": true,
	"HOME": true, "USERPROFILE": true, "SHELL": true, "ENV": true, "BASH_ENV": true, "IFS": true, "PS4": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true, "NODE_OPTIONS": true, "PYTHONPATH": true,
	"PYTHONSTARTUP": true, "PERL5OPT": true, "RUBYOPT": true, "JAVA_TOOL_OPTIONS": true,
	"GIT_SSH_COMMAND": true, "GIT_SSH": true, "GIT_ASKPASS": true, "SSH_ASKPASS": true, "GIT_EXEC_PATH": true,
	"TMP": true, "TEMP": true, "TMPDIR": true,
}

func validateEnvName(name string) error {
	if !envNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not an environment variable name; use letters, digits and underscores", name)
	}
	upper := strings.ToUpper(name)
	if reservedEnvNames[upper] || strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "NULLBOT_") || strings.HasPrefix(upper, "GIT_CONFIG") {
		return fmt.Errorf("%s changes how programs run, so a secret cannot set it", name)
	}
	return nil
}

// secretEnvNames lists the variables a command gets from s.
func secretEnvNames(s Secret) []string {
	var names []string
	switch s.Kind {
	case SecretAPIKey:
		names = providerEnvNames(s.Public["provider"])
	case SecretGitHubToken:
		names = []string{"GH_TOKEN", "GITHUB_TOKEN"}
		if h := s.Public["host"]; h != "" && h != "github.com" {
			names = append(names, "GH_ENTERPRISE_TOKEN", "GH_HOST")
		}
		names = append(names, "git credentials for https://"+firstNonEmpty(s.Public["host"], "github.com"))
	case SecretSSHKey:
		names = []string{"NULLBOT_SSH_KEY_FILE", "GIT_SSH_COMMAND"}
	case SecretAWS:
		names = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}
		if s.Values["session_token"] != "" {
			names = append(names, "AWS_SESSION_TOKEN")
		}
		if s.Public["region"] != "" {
			names = append(names, "AWS_REGION", "AWS_DEFAULT_REGION")
		}
	case SecretGCP:
		names = []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"}
		if s.Public["project_id"] != "" {
			names = append(names, "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT")
		}
	case SecretGeneric:
		if s.Public["env"] != "" {
			names = []string{s.Public["env"]}
		}
	}
	return names
}

func providerEnvNames(provider string) []string {
	switch provider {
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY"}
	case "openrouter":
		return []string{"OPENROUTER_API_KEY"}
	case "google", "gemini":
		return []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}
	case "":
		return nil
	}
	return []string{strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"}
}

// --- legacy files -----------------------------------------------------------

func readLegacyAPIKeys(config Config) (APIKeys, error) {
	var keys APIKeys
	data, err := readSmallRegularFile(KeysPath(config), 1<<20)
	if err != nil {
		return keys, err
	}
	defer clear(data)
	if err := json.Unmarshal(data, &keys); err != nil {
		return keys, err
	}
	return keys, nil
}

func readLegacyCodexAuth(config Config) (codexAuthStore, error) {
	var auth codexAuthStore
	data, err := readSmallRegularFile(CodexAuthPath(config), 1<<20)
	if err != nil {
		return auth, err
	}
	defer clear(data)
	if err := json.Unmarshal(data, &auth); err != nil {
		return auth, err
	}
	if auth.AccessToken == "" && auth.RefreshToken == "" {
		return auth, errors.New("empty codex auth")
	}
	return auth, nil
}

func readSmallRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path, limit)
	}
	return os.ReadFile(path)
}

// secretsSummary is the /secrets command's text.
func (a *App) secretsSummary() string {
	config := a.Config()
	info := VaultStatus(config)
	var b strings.Builder
	switch {
	case !info.Exists:
		b.WriteString("No secrets are saved yet. The vault is made when the first one is.\n")
	case !info.Unlocked:
		fmt.Fprintf(&b, "The vault is locked: %s\n", info.Error)
	default:
		fmt.Fprintf(&b, "%d secret(s), protected by %s.\n", info.Secrets, info.ProtectionLabel)
	}
	for _, path := range info.Plaintext {
		fmt.Fprintf(&b, "Still in plain text (no keychain on this system; set NULLBOT_VAULT_PASSPHRASE to encrypt): %s\n", path)
	}
	if secrets, err := ListSecrets(config); err == nil {
		for _, s := range secrets {
			use := "NullBot only"
			if s.AgentUse {
				use = "agents may use"
			}
			fmt.Fprintf(&b, "- %s (%s, %s): %s\n", s.Name, s.Label, use, strings.Join(s.Provides, ", "))
		}
	}
	b.WriteString("\nAdd or change secrets with `nullbot secrets set NAME --kind KIND`, or in the desktop app under Settings > Secrets.")
	return b.String()
}
