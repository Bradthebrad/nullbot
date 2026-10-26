package app

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Synthetic fixtures only; these tests never contact a provider or bridge.
func newProviderFixtures() []ServiceAccountInput {
	return []ServiceAccountInput{
		{ID: "imap", Service: "gmail_imap", Label: "Mail", Fields: map[string]string{"email": "fixture@example.com", "app_password": "synthetic-app-password"}},
		{ID: "discord", Service: "discord", Label: "Bot", Fields: map[string]string{"bot_token": "synthetic_discord.segment.signature"}},
		{ID: "telegram", Service: "telegram", Label: "Bot", Fields: map[string]string{"bot_token": "123456:synthetic_URL-safe-token_0123456789"}},
		{ID: "signal", Service: "signal", Label: "Bridge", Fields: map[string]string{"bridge_url": "http://127.0.0.1:8080", "number": "+15551234567", "api_token": "synthetic-bridge-token"}},
	}
}

func TestNewProviderCatalogContracts(t *testing.T) {
	want := map[string]ServiceDefinition{
		"gmail_imap": {Group: "emails", ServerID: "nullbot-emails-mcp", Fields: []ServiceField{{Key: "email", Required: true}, {Key: "app_password", Secret: true, Required: true}}},
		"discord":    {Group: "messages", ServerID: "nullbot-messages-mcp", Fields: []ServiceField{{Key: "bot_token", Secret: true, Required: true}}},
		"telegram":   {Group: "messages", ServerID: "nullbot-messages-mcp", Fields: []ServiceField{{Key: "bot_token", Secret: true, Required: true}}},
		"signal":     {Group: "messages", ServerID: "nullbot-messages-mcp", Fields: []ServiceField{{Key: "bridge_url", Required: true}, {Key: "number", Required: true}, {Key: "api_token", Secret: true}}},
	}
	for id, expected := range want {
		got, ok := serviceDefinition(id)
		if !ok || got.Group != expected.Group || got.ServerID != expected.ServerID {
			t.Fatalf("missing or misrouted provider %s", id)
		}
		for i := range got.Fields {
			got.Fields[i].Label = ""
		}
		if !reflect.DeepEqual(got.Fields, expected.Fields) {
			t.Fatalf("field contract changed for %s", id)
		}
	}
	gmail, ok := serviceDefinition("gmail")
	if !ok || len(gmail.Fields) != 1 || gmail.Fields[0].Key != "access_token" || !gmail.Fields[0].Secret {
		t.Fatal("existing Gmail REST contract changed")
	}
}

func TestNewProviderRequiredAndUnknownFields(t *testing.T) {
	for _, input := range newProviderFixtures() {
		t.Run(input.Service, func(t *testing.T) {
			if err := validateServiceAccount(input); err != nil {
				t.Fatal("valid synthetic fixture rejected")
			}
			definition, _ := serviceDefinition(input.Service)
			for _, field := range definition.Fields {
				original := input.Fields[field.Key]
				delete(input.Fields, field.Key)
				if (validateServiceAccount(input) != nil) != field.Required {
					t.Fatalf("incorrect missing-field validation for %s", field.Key)
				}
				input.Fields[field.Key] = ""
				if (validateServiceAccount(input) != nil) != field.Required {
					t.Fatalf("incorrect blank-field validation for %s", field.Key)
				}
				invalidValues := []string{" leading", "trailing ", "embedded\ncontrol", strings.Repeat("x", 16385)}
				if input.Service == "gmail_imap" && field.Key == "app_password" {
					// ASCII spaces are normalized by the mail connector.
					invalidValues = []string{"\tleading", "trailing\t", "embedded\ncontrol", strings.Repeat("x", 16385)}
				}
				for _, invalid := range invalidValues {
					input.Fields[field.Key] = invalid
					if validateServiceAccount(input) != errServiceInput {
						t.Fatalf("unsafe field accepted: %s", field.Key)
					}
				}
				input.Fields[field.Key] = original
			}
			for _, key := range []string{"host", "smtp_host", "imap_host", "offset", "user_token", "password", "session"} {
				input.Fields[key] = "synthetic"
				if validateServiceAccount(input) != errServiceInput {
					t.Fatalf("unsupported field accepted: %s", key)
				}
				delete(input.Fields, key)
			}
		})
	}
}

func TestGmailIMAPAddressValidation(t *testing.T) {
	input := newProviderFixtures()[0]
	for _, email := range []string{"not-an-email", "Name <fixture@example.com>", "fixture@example.com,other@example.com", "fixture@example.com (comment)", "a b@example.com", "fixture@"} {
		input.Fields["email"] = email
		if validateServiceAccount(input) != errServiceInput {
			t.Fatal("invalid mailbox accepted")
		}
	}
}

func TestTelegramBotTokenValidation(t *testing.T) {
	input := newProviderFixtures()[2]
	for _, token := range []string{"", "12345", ":abc", "abc:token", "123:", "123:a:b", "123:abc/def", "123:abc+def", "123:abc=", "123:abc.def", "123:abc def", "１２３:abc"} {
		input.Fields["bot_token"] = token
		if validateServiceAccount(input) != errServiceInput {
			t.Fatal("invalid bot token accepted")
		}
	}
}

func TestSignalLoopbackURLValidation(t *testing.T) {
	input := newProviderFixtures()[3]
	for _, url := range []string{"http://127.0.0.1:1", "http://127.0.0.1:65535", "http://[::1]:8080", "http://[::1]:00080"} {
		input.Fields["bridge_url"] = url
		if validateServiceAccount(input) != nil {
			t.Fatalf("literal loopback rejected: %s", url)
		}
	}
	for _, url := range []string{
		"http://127.0.0.1:65535/", "http://[::1]:8080/", "http://127.0.0.1:000080",
		"http://127.0.0.1", "http://[::1]", "http://127.0.0.1:", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:-1", "http://127.0.0.1:+80",
		"https://127.0.0.1:8080", "HTTP://127.0.0.1:8080", "http://localhost:8080", "http://127.0.0.2:8080", "http://0.0.0.0:8080", "http://192.168.1.2:8080", "http://example.com:8080",
		"http://127.1:8080", "http://2130706433:8080", "http://0177.0.0.1:8080", "http://[::ffff:127.0.0.1]:8080", "http://[::1%25lo]:8080", "http://::1:8080",
		"http://user:pass@127.0.0.1:8080", "http://127.0.0.1:8080@evil.example:80", "http://127.0.0.1:8080/path", "http://127.0.0.1:8080//", "http://127.0.0.1:8080/../", "http://127.0.0.1:8080/%2f",
		"http://127.0.0.1:8080?", "http://127.0.0.1:8080/?x=1", "http://127.0.0.1:8080#", "http://127.0.0.1:8080/#fragment", "http://127.0.0.1:8080\\evil",
	} {
		input.Fields["bridge_url"] = url
		if validateServiceAccount(input) != errServiceInput {
			t.Fatalf("unsafe bridge URL accepted: %s", url)
		}
	}
	input.Fields["bridge_url"] = "http://127.0.0.1:8080"
	for _, number := range []string{"15551234567", "+05551234567", "+1 5551234567", "+1234567890123456", "+", "+abc", "+1"} {
		input.Fields["number"] = number
		if validateServiceAccount(input) != errServiceInput {
			t.Fatal("invalid E.164 number accepted")
		}
	}
}

func TestNewProviderEnvironmentIsolation(t *testing.T) {
	keys := []string{"Gmail_Imap_Email", "Gmail_App_Password", "Discord_Bot_Token", "Discord_Channel", "Telegram_Bot_Token", "Telegram_Chat_ID", "Signal_Bridge_URL", "Signal_Number", "Signal_Api_Token"}
	entry := MCPEntry{Env: map[string]string{"ordinary": "kept"}}
	for _, key := range keys {
		t.Setenv(key, "synthetic-parent")
		entry.Env[key] = "synthetic-override"
		entry.Env[strings.ToUpper(key)] = "synthetic-alias"
		if !reservedServiceEnv(key) || !inheritedCredentialEnv(key) {
			t.Fatalf("unprotected service prefix: %s", key)
		}
	}
	for _, id := range []string{"unrelated", "nullbot-emails-mcp", "nullbot-messages-mcp", "nullbot-drives-mcp"} {
		env := mcpRuntimeEnv(Config{}, id, entry)
		if env[serviceAccountsEnv] != "" || env[mcpEnvKey("ordinary")] != "kept" {
			t.Fatal("untrusted launch environment incorrect")
		}
		for _, key := range keys {
			if env[mcpEnvKey(key)] != "" || env[strings.ToUpper(key)] != "" {
				t.Fatalf("service environment leaked: %s", key)
			}
			if os.Getenv(key) != "synthetic-parent" {
				t.Fatal("parent process environment mutated")
			}
		}
	}
}
