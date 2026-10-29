package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

type APIKeys struct {
	OpenAI     string            `json:"openai,omitempty"`
	Anthropic  string            `json:"anthropic,omitempty"`
	OpenRouter string            `json:"openrouter,omitempty"`
	Google     string            `json:"google,omitempty"`
	Other      map[string]string `json:"other,omitempty"`
}

// KeysPath is the plain-text keys file older versions wrote. Keys are now in
// the vault; this file is read only to move it there, or on a system that
// cannot encrypt (no keychain and no passphrase).
func KeysPath(config Config) string {
	return filepath.Join(config.AppDir, "api", "keys.json")
}

// LoadAPIKeys returns the provider keys from the vault. Without a usable
// vault it falls back to the legacy plain-text file, if there is one.
func LoadAPIKeys(config Config) (APIKeys, error) {
	payload, err := loadSecretsPayload(config)
	if err == nil {
		keys := apiKeysFromPayload(payload)
		if legacy, lerr := readLegacyAPIKeys(config); lerr == nil {
			// The vault could not take them (no keychain): use them as before.
			keys = mergeMissing(keys, legacy)
		}
		return keys, nil
	}
	if legacy, lerr := readLegacyAPIKeys(config); lerr == nil {
		return legacy, err
	}
	return APIKeys{}, err
}

func mergeMissing(keys, extra APIKeys) APIKeys {
	if keys.OpenAI == "" {
		keys.OpenAI = extra.OpenAI
	}
	if keys.Anthropic == "" {
		keys.Anthropic = extra.Anthropic
	}
	if keys.OpenRouter == "" {
		keys.OpenRouter = extra.OpenRouter
	}
	if keys.Google == "" {
		keys.Google = extra.Google
	}
	for p, v := range extra.Other {
		if keys.ForProvider(p) == "" {
			if keys.Other == nil {
				keys.Other = map[string]string{}
			}
			keys.Other[p] = v
		}
	}
	return keys
}

// SaveAPIKeys stores the provider keys in the vault, creating it if needed.
// Only on a system that cannot encrypt at all (no keychain and no
// passphrase configured) does it keep writing the legacy plain-text file;
// VaultStatus reports that file so the UI can say so.
func SaveAPIKeys(config Config, keys APIKeys) error {
	if _, err := wantedAPIKeys(keys); err != nil {
		return err
	}
	legacy, legacyErr := readLegacyAPIKeys(config)
	legacyDone := false
	err := updateSecrets(config, func(payload *vaultPayload) error {
		if legacyErr == nil {
			// A mask stands for the key the form showed. That is the
			// legacy file's key only where the vault has none and the mask
			// matches it; otherwise the mask means "unchanged".
			inVault := apiKeysFromPayload(*payload)
			keys = replaceMasks(keys, func(provider, mask string) string {
				old := legacy.forNormalized(provider)
				if inVault.ForProvider(provider) == "" && old != "" && MaskSecret(old) == mask {
					return old
				}
				return mask
			})
		}
		if err := setAPIKeysInPayload(payload, keys, "user"); err != nil {
			return err
		}
		legacyDone = legacyErr == nil && legacyHandled(*payload, keys, legacy)
		return nil
	})
	if err == nil {
		// The legacy file goes once each key in it is in the vault or was
		// cleared by this save.
		if legacyDone {
			removeLegacyFile(KeysPath(config))
		}
		return nil
	}
	if !errors.Is(err, vault.ErrNoKeychain) {
		return err
	}
	// No way to encrypt on this system: the legacy file, as before. A mask
	// keeps the key it stands for, and is never written as a key.
	keys = replaceMasks(keys, func(provider, _ string) string { return legacy.forNormalized(provider) })
	if err := os.MkdirAll(filepath.Dir(KeysPath(config)), 0700); err != nil {
		return err
	}
	data, merr := json.MarshalIndent(keys, "", "  ")
	if merr != nil {
		return merr
	}
	defer clear(data)
	err = os.WriteFile(KeysPath(config), append(data, '\n'), 0600)
	invalidateRedactor(config)
	return err
}

// replaceMasks replaces each masked value in keys with what fn returns for
// its provider (normalized) and the mask.
func replaceMasks(keys APIKeys, fn func(provider, mask string) string) APIKeys {
	swap := func(provider string, value *string) {
		if v := strings.TrimSpace(*value); looksMasked(v) {
			*value = fn(provider, v)
		}
	}
	swap("openai", &keys.OpenAI)
	swap("anthropic", &keys.Anthropic)
	swap("openrouter", &keys.OpenRouter)
	swap("google", &keys.Google)
	if len(keys.Other) > 0 {
		other := make(map[string]string, len(keys.Other))
		for name, v := range keys.Other {
			provider := NormalizeProvider(name)
			if provider == "gemini" {
				provider = "google"
			}
			swap(provider, &v)
			other[name] = v
		}
		keys.Other = other
	}
	return keys
}

// forNormalized is the key for a normalized provider name, wherever keys
// holds it ("Perplexity AI" serves perplexity-ai).
func (k APIKeys) forNormalized(provider string) string {
	if v := k.ForProvider(provider); v != "" {
		return v
	}
	for name, v := range k.Other {
		p := NormalizeProvider(name)
		if p == "gemini" {
			p = "google"
		}
		if p == provider {
			return v
		}
	}
	return ""
}

// legacyHandled reports whether every key in legacy is now in payload, or
// was cleared on purpose in keys (the full set being saved).
func legacyHandled(payload vaultPayload, keys, legacy APIKeys) bool {
	stored := map[string]bool{}
	for _, s := range payload.Secrets {
		if s.Kind == SecretAPIKey {
			stored[strings.TrimSpace(s.Values["value"])] = true
		}
	}
	given := map[string]string{
		"openai": keys.OpenAI, "anthropic": keys.Anthropic, "openrouter": keys.OpenRouter, "google": keys.Google,
	}
	for name, v := range keys.Other {
		if p := NormalizeProvider(name); p != "" && strings.TrimSpace(given[p]) == "" {
			given[p] = v
		}
	}
	handled := func(provider, value string) bool {
		value = strings.TrimSpace(value)
		if value == "" || stored[value] {
			return true
		}
		if provider == "" {
			return false // a name the vault cannot take; the file keeps it
		}
		if provider == "gemini" {
			provider = "google"
		}
		return strings.TrimSpace(given[provider]) == ""
	}
	ok := handled("openai", legacy.OpenAI) && handled("anthropic", legacy.Anthropic) &&
		handled("openrouter", legacy.OpenRouter) && handled("google", legacy.Google)
	for name, v := range legacy.Other {
		ok = ok && handled(NormalizeProvider(name), v)
	}
	return ok
}

func (k APIKeys) ForProvider(provider string) string {
	switch provider {
	case "openai":
		return k.OpenAI
	case "anthropic":
		return k.Anthropic
	case "openrouter":
		return k.OpenRouter
	case "google", "gemini":
		return k.Google
	default:
		if k.Other != nil {
			return k.Other[provider]
		}
		return ""
	}
}

func MaskSecret(secret string) string {
	if secret == "" {
		return "(not set)"
	}
	if len(secret) <= 12 {
		return "********"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}

// LooksMasked reports a value that is MaskSecret's output rather than a key.
func LooksMasked(value string) bool { return looksMasked(value) }

// looksMasked reports a value that is MaskSecret's output, not a key: a
// form sent back unchanged must never replace the real key.
func looksMasked(value string) bool {
	value = strings.TrimSpace(value)
	return value == "(not set)" || strings.Contains(value, "...") || strings.HasPrefix(value, "…") ||
		(value != "" && strings.Trim(value, "*") == "")
}
