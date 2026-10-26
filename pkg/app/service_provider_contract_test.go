package app

import (
	"strings"
	"testing"
)

// Boundary vectors mirror the connectors' credential gates, not provider network
// behavior. Keep in sync with messages internal/safe/providers.go, phonePattern
// in internal/messages/messages.go, and emails internal/emails/gmail_imap.go.
// In particular, do not loosen these limits just to accept UI/test fixtures.
func TestProviderConnectorValidationContract(t *testing.T) {
	cases := []struct {
		name    string
		fixture int
		field   string
		value   string
		valid   bool
	}{
		{"telegram minimum", 2, "bot_token", "1:" + strings.Repeat("a", 30), true},
		{"telegram maximum", 2, "bot_token", strings.Repeat("9", 20) + ":" + strings.Repeat("_", 128), true},
		{"telegram zero ID", 2, "bot_token", "0:" + strings.Repeat("a", 30), false},
		{"telegram leading zero", 2, "bot_token", "01:" + strings.Repeat("a", 30), false},
		{"telegram long ID", 2, "bot_token", strings.Repeat("9", 21) + ":" + strings.Repeat("a", 30), false},
		{"telegram short secret", 2, "bot_token", "1:" + strings.Repeat("a", 29), false},
		{"telegram long secret", 2, "bot_token", "1:" + strings.Repeat("a", 129), false},
		{"telegram URL safe", 2, "bot_token", "1:" + strings.Repeat("Az09_-", 5), true},
		{"telegram slash", 2, "bot_token", "1:" + strings.Repeat("a", 30) + "/", false},
		{"discord minimum", 1, "bot_token", "a.b." + strings.Repeat("c", 16), true},
		{"discord maximum", 1, "bot_token", "a.b." + strings.Repeat("c", 252), true},
		{"discord too short", 1, "bot_token", "a.b." + strings.Repeat("c", 15), false},
		{"discord too long", 1, "bot_token", "a.b." + strings.Repeat("c", 253), false},
		{"discord URL safe", 1, "bot_token", "Az09_-.Az09_-.Az09_-Az09_-", true},
		{"discord MFA", 1, "bot_token", "mfa.b." + strings.Repeat("c", 20), false},
		{"discord one segment", 1, "bot_token", strings.Repeat("a", 30), false},
		{"discord two segments", 1, "bot_token", "a." + strings.Repeat("b", 30), false},
		{"discord four segments", 1, "bot_token", "a.b.c." + strings.Repeat("d", 30), false},
		{"discord empty segment", 1, "bot_token", "a.." + strings.Repeat("c", 30), false},
		{"discord slash", 1, "bot_token", "a.b." + strings.Repeat("c", 30) + "/", false},
		{"discord padding", 1, "bot_token", "a.b." + strings.Repeat("c", 30) + "=", false},
		{"signal IPv4 slash", 3, "bridge_url", "http://127.0.0.1:8080/", false},
		{"signal IPv6 slash", 3, "bridge_url", "http://[::1]:8080/", false},
		{"signal minimum number", 3, "number", "+1234567", true},
		{"signal maximum number", 3, "number", "+123456789012345", true},
		{"signal short number", 3, "number", "+123456", false},
		{"signal long number", 3, "number", "+1234567890123456", false},
		{"signal zero prefix", 3, "number", "+0234567", false},
		{"signal empty bearer", 3, "api_token", "", true},
		{"signal bearer alphabet", 3, "api_token", "Az09._~+/-==", true},
		{"signal bearer maximum", 3, "api_token", strings.Repeat("a", 4096), true},
		{"signal bearer too long", 3, "api_token", strings.Repeat("a", 4097), false},
		{"signal bearer embedded padding", 3, "api_token", "abc=def", false},
		{"signal bearer padding only", 3, "api_token", "=", false},
		{"signal bearer colon", 3, "api_token", "abc:def", false},
		{"signal bearer space", 3, "api_token", "abc def", false},
		{"signal bearer Unicode", 3, "api_token", "abcé", false},
		{"gmail ASCII address", 0, "email", "fixture+test@example.com", true},
		{"gmail maximum email", 0, "email", strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), true},
		{"gmail long email", 0, "email", strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62), false},
		{"gmail Unicode local part", 0, "email", "fixturé@example.com", false},
		{"gmail Unicode domain", 0, "email", "fixture@exämple.com", false},
		{"gmail password minimum", 0, "app_password", "!", true},
		{"gmail password maximum", 0, "app_password", strings.Repeat("x", 256), true},
		{"gmail password grouping", 0, "app_password", " abcd efgh ijkl mnop ", true},
		{"gmail normalized maximum", 0, "app_password", strings.Repeat(" x ", 256), true},
		{"gmail password too long", 0, "app_password", strings.Repeat("x", 257), false},
		{"gmail normalized too long", 0, "app_password", strings.Repeat("x ", 257), false},
		{"gmail password empty", 0, "app_password", "", false},
		{"gmail password spaces only", 0, "app_password", "    ", false},
		{"gmail password Unicode", 0, "app_password", "syntheticé", false},
		{"gmail password nonbreaking space", 0, "app_password", "abcd\u00a0efgh", false},
		{"gmail password tab", 0, "app_password", "abcd\tefgh", false},
		{"gmail password DEL", 0, "app_password", "abcd\x7fefgh", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := newProviderFixtures()[tc.fixture]
			input.Fields[tc.field] = tc.value
			err := validateServiceAccount(input)
			if (err == nil) != tc.valid {
				t.Fatalf("connector contract drift: accepted=%v, want %v", err == nil, tc.valid)
			}
			if err != nil && err != errServiceInput {
				t.Fatal("credential validation must return the sanitized input error")
			}
		})
	}
}
