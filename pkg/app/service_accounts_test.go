package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeServiceInstall(t *testing.T, config Config, id string) MCPEntry {
	t.Helper()
	dir := filepath.Join(config.AppDir, "mcp", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".exe")
	data := []byte("offline test fixture, never executed")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	pkg := serviceMarketPackage(strings.TrimSuffix(strings.TrimPrefix(id, "nullbot-"), "-mcp"), "test", "test", nil)
	pkg.Installed = true
	pkg.InstallDir = dir
	pkg.InstalledAsset = id + ".exe"
	pkg.InstalledSHA256 = hex.EncodeToString(sum[:])
	if err := SaveMarketManifest(config, MarketManifest{Packages: []MarketPackage{pkg}}); err != nil {
		t.Fatal(err)
	}
	return MCPEntry{Enabled: true, Transport: "stdio", Command: path, Args: []string{"--transport", "stdio"}}
}

func TestServiceExecutableTrust(t *testing.T) {
	config := testMarketConfig(t)
	id := "nullbot-emails-mcp"
	entry := fakeServiceInstall(t, config, id)
	if !trustedServiceServer(config, id, entry) {
		t.Fatal("valid installation not trusted")
	}
	for _, modify := range []func(*MCPEntry){
		func(e *MCPEntry) { e.Enabled = false },
		func(e *MCPEntry) { e.Transport = "http" },
		func(e *MCPEntry) { e.Command = filepath.Base(e.Command) },
		func(e *MCPEntry) { e.Args = []string{"--addr", "0.0.0.0:1234"} },
		func(e *MCPEntry) { e.Command = filepath.Join(t.TempDir(), filepath.Base(e.Command)) },
	} {
		bad := entry
		modify(&bad)
		if trustedServiceServer(config, id, bad) {
			t.Fatal("unsafe entry trusted")
		}
	}
	if trustedServiceServer(config, "pretend-emails", entry) {
		t.Fatal("unknown server trusted")
	}
	if err := os.WriteFile(entry.Command, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if trustedServiceServer(config, id, entry) {
		t.Fatal("changed executable trusted")
	}
}

func TestServiceTrustPinSurvivesReleaseRefresh(t *testing.T) {
	old := MarketPackage{ID: "nullbot-emails-mcp", InstalledSHA256: strings.Repeat("a", 64)}
	next := MarketPackage{ID: old.ID, Assets: []MarketAsset{{SHA256: strings.Repeat("b", 64)}}}
	merged := mergePackages(MarketManifest{Packages: []MarketPackage{old}}, MarketManifest{Packages: []MarketPackage{next}})
	if merged.Packages[0].InstalledSHA256 != old.InstalledSHA256 {
		t.Fatal("release refresh changed installed pin")
	}
}

func TestServiceEnvironmentIsolation(t *testing.T) {
	t.Setenv("Nullbot_Service_Accounts", "synthetic inherited secret")
	t.Setenv("Twilio_Auth_Token", "synthetic twilio secret")
	t.Setenv("Example_Api_Key", "synthetic API secret")
	entry := MCPEntry{Env: map[string]string{"NULLBOT_SERVICE_ACCOUNTS": "injected", "TWILIO_AUTH_TOKEN": "injected", "custom_value": "ok", "Example_Api_Key": "explicit"}}
	env := scopedMCPBaseEnv(entry)
	if env[serviceAccountsEnv] != "" || env[mcpEnvKey("Twilio_Auth_Token")] != "" {
		t.Fatal("reserved credentials leaked")
	}
	if env[mcpEnvKey("Example_Api_Key")] != "explicit" || env[mcpEnvKey("custom_value")] != "ok" {
		t.Fatal("explicit non-reserved env lost")
	}
	if runtime.GOOS == "windows" {
		for key := range env {
			if key != strings.ToUpper(key) {
				t.Fatal("competing Windows key alias")
			}
		}
	}
	if os.Getenv("Nullbot_Service_Accounts") != "synthetic inherited secret" {
		t.Fatal("process environment mutated")
	}
}

func TestServiceValidationAndCatalog(t *testing.T) {
	good := ServiceAccountInput{ID: "test", Service: "gmail", Label: "Test", Fields: map[string]string{"access_token": "synthetic-token"}}
	if err := validateServiceAccount(good); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*ServiceAccountInput){
		func(a *ServiceAccountInput) { a.Service = "yahoo" },
		func(a *ServiceAccountInput) { a.ID = "../bad" },
		func(a *ServiceAccountInput) { a.Label = "" },
		func(a *ServiceAccountInput) { a.Fields = map[string]string{"access_token": ""} },
		func(a *ServiceAccountInput) { a.Fields = map[string]string{"access_token": "token\r\nheader"} },
		func(a *ServiceAccountInput) {
			a.Fields = map[string]string{"access_token": "token", "refresh_token": "unsupported"}
		},
		func(a *ServiceAccountInput) {
			a.Service = "icloud_drive"
			a.Fields = map[string]string{"root": "relative"}
		},
		func(a *ServiceAccountInput) {
			a.Service = "twilio"
			a.Fields = map[string]string{"account_sid": "bad", "auth_token": "synthetic", "from": "+15551234567"}
		},
		func(a *ServiceAccountInput) {
			a.Service = "whatsapp"
			a.Fields = map[string]string{"access_token": "synthetic", "phone_number_id": "123", "api_version": "../../invalid"}
		},
	} {
		bad := good
		modify(&bad)
		if validateServiceAccount(bad) == nil {
			t.Fatal("invalid input accepted")
		}
	}
	catalog := ServiceAccountCatalog()
	catalog[0].Fields[0].Key = "mutated"
	if ServiceAccountCatalog()[0].Fields[0].Key != "access_token" {
		t.Fatal("catalog aliases mutable state")
	}
	for _, pkg := range defaultMarketManifest(Config{}).Packages {
		if serviceServerID(pkg.ID) {
			if pkg.Status != "unreleased" || len(pkg.Assets) != 0 {
				t.Fatal("invented release assets")
			}
		}
	}
	// Public status contract has no credential-bearing Fields member.
	data, _ := json.Marshal(ServiceAccountInfo{ID: "test", Service: "gmail", Label: "Test"})
	if strings.Contains(string(data), "fields") {
		t.Fatal("status exposes account fields")
	}
}

func TestServiceInstallRequiresChecksumBeforeNetwork(t *testing.T) {
	config := testMarketConfig(t)
	pkg := serviceMarketPackage("emails", "Email", "test", nil)
	for _, checksum := range []string{"", "not-hex", strings.Repeat("a", 62)} {
		pkg.Assets = []MarketAsset{{Name: "nullbot-emails-mcp.exe", URL: "https://invalid.invalid/must-not-request", SHA256: checksum}}
		_, err := installMCPPackage(context.Background(), config, pkg, false)
		if err == nil || !strings.Contains(err.Error(), "require a release SHA256") {
			t.Fatal("missing checksum did not fail before download")
		}
	}
	if _, err := os.Stat(filepath.Join(config.AppDir, "mcp", pkg.ID)); !os.IsNotExist(err) {
		t.Fatal("invalid metadata created install directory")
	}
}
