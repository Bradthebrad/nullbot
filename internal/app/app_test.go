package app

import (
	"context"
	"path/filepath"
	"testing"
)

func TestInlineSkillHints(t *testing.T) {
	hints := inlineSkillHints("check /email for anything new and then /calendar.")
	if len(hints) != 2 || hints[0] != "/email" || hints[1] != "/calendar" {
		t.Fatalf("hints = %#v", hints)
	}
}

func TestConfigCommandUpdatesBrand(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	app := New(config)
	reply := app.Submit(context.Background(), "/config brand_prefix=Brad")
	if reply.Config.BrandPrefix != "Brad" {
		t.Fatalf("brand = %q, message = %s", reply.Config.BrandPrefix, reply.Message)
	}
}

func TestUpdateConfigPersists(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	app := New(config)
	if err := app.UpdateConfig(func(config *Config) {
		config.Model.Provider = "openrouter"
		config.Model.Model = "openai/gpt-5.2"
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOrInitConfigAt(config.AppDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Model.Provider != "openrouter" || loaded.Model.Model != "openai/gpt-5.2" {
		t.Fatalf("loaded model = %#v", loaded.Model)
	}
}

func TestAPIKeysPersistSeparately(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	keys := APIKeys{OpenAI: "sk-test", OpenRouter: "or-test"}
	if err := SaveAPIKeys(config, keys); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAPIKeys(config)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OpenAI != "sk-test" || loaded.OpenRouter != "or-test" {
		t.Fatalf("keys = %#v", loaded)
	}
}

func TestModelGroupsFlatten(t *testing.T) {
	groups := []ModelGroup{{Provider: "x", Models: []ModelOption{{Provider: "x", ID: "a"}}}}
	flat := FlattenModelGroups(groups)
	if len(flat) != 1 || flat[0].ID != "a" {
		t.Fatalf("flat = %#v", flat)
	}
}

func TestSafeConfigPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeConfigPath(root, filepath.Join("..", "outside.txt")); err == nil {
		t.Fatal("expected escape error")
	}
}
