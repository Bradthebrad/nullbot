package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const serviceAccountsEnv = "NULLBOT_SERVICE_ACCOUNTS"

func serviceServerID(id string) bool {
	return id == "nullbot-emails-mcp" || id == "nullbot-drives-mcp" || id == "nullbot-messages-mcp"
}

// Read the manifest without creating files, refreshing metadata or making network
// requests. A matching ID/basename is NOT enough to authorize credentials.
func installedServicePackage(config Config, id string) (MarketPackage, bool) {
	if !serviceServerID(id) {
		return MarketPackage{}, false
	}
	f, err := os.Open(manifestPath(config))
	if err != nil {
		return MarketPackage{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return MarketPackage{}, false
	}
	var manifest MarketManifest
	if json.Unmarshal(data, &manifest) != nil {
		return MarketPackage{}, false
	}
	for _, pkg := range manifest.Packages {
		if pkg.ID != id || pkg.Kind != "mcp_server" || !pkg.Installed || pkg.InstalledAsset == "" {
			continue
		}
		if filepath.Base(pkg.InstalledAsset) != pkg.InstalledAsset {
			return MarketPackage{}, false
		}
		expected, err := filepath.Abs(filepath.Join(config.AppDir, "mcp", id))
		if err != nil {
			return MarketPackage{}, false
		}
		actual, err := filepath.Abs(pkg.InstallDir)
		if err != nil || !strings.EqualFold(expected, actual) {
			return MarketPackage{}, false
		}
		path := filepath.Join(expected, pkg.InstalledAsset)
		if rejectCredentialLinks(path) != nil {
			return MarketPackage{}, false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return MarketPackage{}, false
		}
		return pkg, true
	}
	return MarketPackage{}, false
}
func serviceServerInstalled(config Config, id string) bool {
	_, ok := installedServicePackage(config, id)
	return ok
}

func trustedServiceServer(config Config, id string, entry MCPEntry) bool {
	if !entry.Enabled || (entry.Transport != "" && entry.Transport != "stdio") || !filepath.IsAbs(entry.Command) {
		return false
	}
	// Only the default stdio invocation is trusted; flags must not redirect account
	// data to a remote listener, load plugins, or change executable behavior.
	if len(entry.Args) != 0 && !(len(entry.Args) == 2 && entry.Args[0] == "--transport" && entry.Args[1] == "stdio") {
		return false
	}
	pkg, ok := installedServicePackage(config, id)
	if !ok {
		return false
	}
	if pkg.Repo != "Bradthebrad/"+id {
		return false
	}
	expected := filepath.Join(pkg.InstallDir, pkg.InstalledAsset)
	if !strings.EqualFold(filepath.Clean(entry.Command), filepath.Clean(expected)) {
		return false
	}
	digest := strings.ToLower(pkg.InstalledSHA256)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	f, err := os.Open(expected)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}

func reservedServiceEnv(key string) bool {
	key = strings.ToUpper(key)
	if strings.HasPrefix(key, "NULLBOT_SERVICE_") {
		return true
	}
	for _, prefix := range []string{"GMAIL_", "OUTLOOK_", "YAHOO_", "ICLOUD_", "GOOGLE_DRIVE_", "ONEDRIVE_", "TWILIO_", "WHATSAPP_", "DISCORD_", "TELEGRAM_", "SIGNAL_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
func inheritedCredentialEnv(key string) bool {
	key = strings.ToUpper(key)
	return reservedServiceEnv(key) || strings.HasPrefix(key, "NULLBOT_VAULT") || strings.Contains(key, "TOKEN") || strings.Contains(key, "SECRET") || strings.Contains(key, "PASSWORD") || strings.HasSuffix(key, "_API_KEY") || strings.HasSuffix(key, "_AUTH") || strings.HasSuffix(key, "_CREDENTIALS")
}

// tinychain merges the map over os.Environ. Explicit empty overrides are needed
// to suppress inherited values, including mixed-case keys on Windows. Never set
// process-global environment values (concurrent server launches must be isolated).
func mcpEnvKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func scopedMCPBaseEnv(entry MCPEntry) map[string]string {
	env := map[string]string{serviceAccountsEnv: ""}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && inheritedCredentialEnv(key) {
			env[mcpEnvKey(key)] = ""
		}
	}
	// Windows exec deduplicates case-insensitively, retaining the last value.
	// Emit one canonical spelling, not competing aliases in an unordered map.
	keys := make([]string, 0, len(entry.Env))
	for key := range entry.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !reservedServiceEnv(key) {
			env[mcpEnvKey(key)] = entry.Env[key]
		}
	}
	return env
}
func addScopedServiceAccounts(config Config, id string, entry MCPEntry, env map[string]string) {
	if !trustedServiceServer(config, id, entry) {
		return
	}
	accounts, err := loadServiceAccounts(config)
	if err != nil {
		return
	}
	selected := []ServiceAccountInput{}
	for _, account := range accounts {
		d, ok := serviceDefinition(account.Service)
		if ok && d.ServerID == id {
			selected = append(selected, account)
		}
	}
	data, err := json.Marshal(selected)
	if err == nil {
		env[serviceAccountsEnv] = string(data)
	}
}
