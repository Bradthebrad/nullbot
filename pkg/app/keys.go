package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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
	err := updateSecrets(config, func(payload *vaultPayload) error {
		setAPIKeysInPayload(payload, keys, "user")
		return nil
	})
	if err == nil {
		if _, statErr := os.Lstat(KeysPath(config)); statErr == nil {
			removeLegacyFile(KeysPath(config))
		}
		return nil
	}
	if !errors.Is(err, vault.ErrNoKeychain) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(KeysPath(config)), 0700); err != nil {
		return err
	}
	data, merr := json.MarshalIndent(keys, "", "  ")
	if merr != nil {
		return merr
	}
	defer clear(data)
	return os.WriteFile(KeysPath(config), append(data, '\n'), 0600)
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
	return "…" + secret[len(secret)-4:]
}
