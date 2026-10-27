//go:build windows

package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNewProviderVaultIsolationAndReplacement(t *testing.T) {
	config := isolatedServiceStore(t)
	fixtures := newProviderFixtures()
	for _, input := range fixtures {
		if err := SaveServiceAccount(config, input); err != nil {
			t.Fatal(err)
		}
	}
	state, err := ServiceAccountStatus(config)
	if err != nil || len(state.Accounts) != len(fixtures) {
		t.Fatal("new provider status failed")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fields", "synthetic", "fixture@example.com", "127.0.0.1", "+15551234567", "api_token", "bot_token", "app_password"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("status leaked account fields")
		}
	}
	path, err := serviceStorePath(config)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(sealed), "synthetic") {
		t.Fatal("new provider vault not encrypted")
	}
	for _, id := range []string{"nullbot-emails-mcp", "nullbot-messages-mcp", "nullbot-drives-mcp"} {
		entry := fakeServiceInstall(t, config, id)
		var selected []ServiceAccountInput
		if err := json.Unmarshal([]byte(mcpRuntimeEnv(config, id, entry)[serviceAccountsEnv]), &selected); err != nil {
			t.Fatal("invalid scoped runtime payload")
		}
		wantCount := 0
		for _, fixture := range fixtures {
			definition, _ := serviceDefinition(fixture.Service)
			if definition.ServerID == id {
				wantCount++
			}
		}
		if len(selected) != wantCount {
			t.Fatal("incorrect connector account count")
		}
		for _, input := range selected {
			definition, _ := serviceDefinition(input.Service)
			if definition.ServerID != id {
				t.Fatal("cross-connector credential leak")
			}
		}
		for _, untrusted := range []MCPEntry{{}, {Enabled: true, Command: entry.Command, Transport: "http"}} {
			if mcpRuntimeEnv(config, id, untrusted)[serviceAccountsEnv] != "" {
				t.Fatal("untrusted connector received credentials")
			}
		}
	}
	for _, input := range fixtures {
		definition, _ := serviceDefinition(input.Service)
		for _, field := range definition.Fields {
			if !field.Secret || !field.Required {
				continue
			}
			original := input.Fields[field.Key]
			input.Fields[field.Key] = ""
			if SaveServiceAccount(config, input) == nil {
				t.Fatal("blank required secret replacement accepted")
			}
			input.Fields[field.Key] = original
			accounts, err := loadServiceAccounts(config)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, stored := range accounts {
				if stored.ID == input.ID {
					found = stored.Fields[field.Key] == original
				}
			}
			if !found {
				t.Fatal("failed replacement changed existing secret")
			}
		}
	}
	// Optional Signal token is removed, never implicitly retained, on replacement.
	signal := fixtures[3]
	for _, omit := range []bool{false, true} {
		signal.Fields["api_token"] = "synthetic-reset-token"
		if err := SaveServiceAccount(config, signal); err != nil {
			t.Fatal(err)
		}
		signal.Fields["api_token"] = ""
		if omit {
			delete(signal.Fields, "api_token")
		}
		if err := SaveServiceAccount(config, signal); err != nil {
			t.Fatal(err)
		}
		accounts, err := loadServiceAccounts(config)
		if err != nil {
			t.Fatal(err)
		}
		for _, stored := range accounts {
			if stored.ID == signal.ID && stored.Fields["api_token"] != "" {
				t.Fatal("optional secret retained on replacement")
			}
		}
	}
}
