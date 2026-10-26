//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedServiceStore(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	old := serviceDataDir
	serviceDataDir = func() (string, error) { return root, nil }
	t.Cleanup(func() { serviceDataDir = old })
	return testMarketConfig(t)
}

func TestServiceDPAPIRoundTripAndCorruption(t *testing.T) {
	plain := []byte("synthetic credential - never a real token")
	sealed, err := protectServiceData(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("plaintext in sealed output")
	}
	out, err := unprotectServiceData(sealed)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatal("DPAPI roundtrip failed")
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := unprotectServiceData(sealed); err == nil {
		t.Fatal("corrupt DPAPI accepted")
	}
}

func TestServiceVaultLifecycleAndScopedInjection(t *testing.T) {
	config := isolatedServiceStore(t)
	state, err := ServiceAccountStatus(config)
	if err != nil || len(state.Accounts) != 0 {
		t.Fatalf("empty state: %v", err)
	}
	path, err := serviceStorePath(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("status created vault")
	}
	first := ServiceAccountInput{ID: "mail", Service: "gmail", Label: "Personal", Fields: map[string]string{"access_token": "synthetic-mail-token"}, AllowWrite: true}
	second := ServiceAccountInput{ID: "drive", Service: "google_drive", Label: "Drive", Fields: map[string]string{"access_token": "synthetic-drive-token"}}
	if err := SaveServiceAccount(config, first); err != nil {
		t.Fatal(err)
	}
	if err := SaveServiceAccount(config, second); err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("synthetic")) {
		t.Fatal("vault contains plaintext")
	}
	state, err = ServiceAccountStatus(config)
	if err != nil || len(state.Accounts) != 2 {
		t.Fatalf("saved state: %v", err)
	}
	raw, _ := json.Marshal(state)
	if bytes.Contains(raw, []byte("synthetic")) || bytes.Contains(raw, []byte("access_token")) {
		t.Fatal("status leaked secret fields")
	}
	if state.Accounts[0].Status != "not_installed" {
		t.Fatal("incorrect missing MCP status")
	}
	id := "nullbot-emails-mcp"
	entry := fakeServiceInstall(t, config, id)
	config.EnabledMCPServers = map[string]MCPEntry{id: entry}
	state, err = ServiceAccountStatus(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Accounts[0].Status != "configured_unverified" {
		t.Fatal("incorrect configured status")
	}
	env := mcpRuntimeEnv(config, id, entry)
	var got []ServiceAccountInput
	if err := json.Unmarshal([]byte(env[serviceAccountsEnv]), &got); err != nil || len(got) != 1 || got[0].ID != "mail" {
		t.Fatal("credentials not scoped to connector")
	}
	if mcpRuntimeEnv(config, "unrelated", entry)[serviceAccountsEnv] != "" {
		t.Fatal("credentials leaked to unrelated server")
	}
	disabled := entry
	disabled.Enabled = false
	if mcpRuntimeEnv(config, id, disabled)[serviceAccountsEnv] != "" {
		t.Fatal("credentials leaked to disabled server")
	}
	first.Fields = map[string]string{"access_token": ""}
	if SaveServiceAccount(config, first) == nil {
		t.Fatal("blank replacement accepted")
	}
	first.Fields = map[string]string{"access_token": "synthetic-replacement"}
	if err := SaveServiceAccount(config, first); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadServiceAccounts(config)
	if err != nil || accounts[0].Fields["access_token"] != "synthetic-replacement" {
		t.Fatal("replacement failed")
	}
	first.Service = "outlook"
	if SaveServiceAccount(config, first) == nil {
		t.Fatal("cross-service replacement accepted")
	}
	if err := DeleteServiceAccount(config, "mail"); err != nil {
		t.Fatal(err)
	}
	accounts, err = loadServiceAccounts(config)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "drive" {
		t.Fatal("delete failed")
	}
	if err := os.WriteFile(path, []byte("corrupt synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceAccounts(config); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("corruption not safely rejected")
	}
	if SaveServiceAccount(config, second) == nil {
		t.Fatal("corrupt store overwritten")
	}
}

func TestServiceVaultRejectsSymlink(t *testing.T) {
	config := isolatedServiceStore(t)
	path, err := serviceStorePath(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	if _, err := loadServiceAccounts(config); err == nil {
		t.Fatal("symlink accepted")
	}
	input := ServiceAccountInput{ID: "mail", Service: "gmail", Label: "Test", Fields: map[string]string{"access_token": "synthetic"}}
	if SaveServiceAccount(config, input) == nil {
		t.Fatal("save followed symlink")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "untouched" {
		t.Fatal("link target changed")
	}
}

func TestServiceVaultRejectsJunction(t *testing.T) {
	config := isolatedServiceStore(t)
	path, err := serviceStorePath(config)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Dir(parent), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	makeProjectTestJunction(t, parent, outside)
	if _, err := loadServiceAccounts(config); err == nil {
		t.Fatal("vault followed directory junction")
	}
	input := ServiceAccountInput{ID: "mail", Service: "gmail", Label: "Test", Fields: map[string]string{"access_token": "synthetic"}}
	if SaveServiceAccount(config, input) == nil {
		t.Fatal("save followed directory junction")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("junction target changed")
	}
}

func TestServiceEnvironmentReachesChild(t *testing.T) {
	t.Setenv("Nullbot_Service_Accounts", "synthetic-parent-secret")
	t.Setenv("Twilio_Auth_Token", "synthetic-parent-secret")
	for _, key := range []string{"Discord_Bot_Token", "Discord_Channel", "Telegram_Bot_Token", "Telegram_Chat_ID", "Signal_Bridge_URL", "Signal_Number", "Signal_Api_Token", "Gmail_Imap_Email"} {
		t.Setenv(key, "synthetic-parent-secret")
	}
	t.Setenv("Example_Api_Key", "synthetic-parent-secret")
	config := testMarketConfig(t)
	env := mcpRuntimeEnv(config, "unrelated", MCPEntry{Env: map[string]string{"Example_Api_Key": "synthetic-explicit"}})
	env["NULLBOT_TEST_SERVICE_CHILD"] = "1"
	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceEnvironmentChild$")
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child environment failed: %v %s", err, out)
	}
}

func TestServiceEnvironmentChild(t *testing.T) {
	if os.Getenv("NULLBOT_TEST_SERVICE_CHILD") != "1" {
		return
	}
	if os.Getenv("NULLBOT_SERVICE_ACCOUNTS") != "" || os.Getenv("TWILIO_AUTH_TOKEN") != "" || os.Getenv("EXAMPLE_API_KEY") != "synthetic-explicit" {
		os.Exit(23)
	}
	for _, key := range []string{"DISCORD_BOT_TOKEN", "DISCORD_CHANNEL", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "SIGNAL_BRIDGE_URL", "SIGNAL_NUMBER", "SIGNAL_API_TOKEN", "GMAIL_IMAP_EMAIL"} {
		if os.Getenv(key) != "" {
			os.Exit(24)
		}
	}
	os.Exit(0)
}
