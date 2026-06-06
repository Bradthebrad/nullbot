package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type APIKeys struct {
	OpenAI     string `json:"openai,omitempty"`
	Anthropic  string `json:"anthropic,omitempty"`
	OpenRouter string `json:"openrouter,omitempty"`
}

func KeysPath(config Config) string {
	return filepath.Join(config.AppDir, "api", "keys.json")
}

func LoadAPIKeys(config Config) (APIKeys, error) {
	var keys APIKeys
	data, err := os.ReadFile(KeysPath(config))
	if err != nil {
		if os.IsNotExist(err) {
			return keys, nil
		}
		return keys, err
	}
	if err := json.Unmarshal(data, &keys); err != nil {
		return keys, err
	}
	return keys, nil
}

func SaveAPIKeys(config Config, keys APIKeys) error {
	if err := os.MkdirAll(filepath.Dir(KeysPath(config)), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
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
	default:
		return ""
	}
}

func MaskSecret(secret string) string {
	if secret == "" {
		return "(not set)"
	}
	if len(secret) <= 8 {
		return "********"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}
