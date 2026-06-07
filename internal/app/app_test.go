package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineSkillHints(t *testing.T) {
	hints := inlineSkillHints("check /email for anything new and then /calendar.")
	if len(hints) != 2 || hints[0] != "/email" || hints[1] != "/calendar" {
		t.Fatalf("hints = %#v", hints)
	}
}

func TestSplitMarketPackageIDs(t *testing.T) {
	ids := splitMarketPackageIDs("nullbot-code-mcp, nullbot-parsers-mcp,,api-probe")
	want := []string{"nullbot-code-mcp", "nullbot-parsers-mcp", "api-probe"}
	if strings.Join(ids, "|") != strings.Join(want, "|") {
		t.Fatalf("ids = %#v, want %#v", ids, want)
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

func TestHelpCommandDoesNotRenderIntoHistory(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	app := New(config)
	reply := app.Submit(context.Background(), "/help")
	if reply.OpenPanel != "help" {
		t.Fatalf("panel = %q", reply.OpenPanel)
	}
	if len(reply.History) != 0 {
		t.Fatalf("help should not be appended to visible history: %#v", reply.History)
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

func TestEnsureAppDirSeedsDefaultSkillUnderSkillsDir(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(config.AppDir, "skills", "nullbot-basics", "SKILL.md")
	if _, err := os.Stat(filepath.Join(config.AppDir, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("root SKILL.md should not be created, err = %v", err)
	}
	if defaultSkillPath(config) != expected {
		t.Fatalf("defaultSkillPath = %q, want %q", defaultSkillPath(config), expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("nested default skill missing: %v", err)
	}
	skills := scanSkillFiles(config)
	if len(skills) != 1 || !strings.HasSuffix(skills[0], filepath.Join("skills", "nullbot-basics", "SKILL.md")) {
		t.Fatalf("skills = %#v", skills)
	}
}

func TestSubmitPersistsHistoryAndArtifacts(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	app := New(config)
	reply := app.Submit(context.Background(), "/pause")
	if !strings.Contains(reply.Message, "Paused") {
		t.Fatalf("reply = %q", reply.Message)
	}
	if files := listHistoryFiles(config, 10); len(files) != 1 {
		t.Fatalf("history files = %#v", files)
	}
	if artifacts := listArtifactFiles(config, 10); len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
}

func TestCreateSkillToolWritesSingleAndBatchSkills(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	tool := createSkillTool(config)
	if _, err := tool.Call(context.Background(), map[string]any{
		"name":        "Email Helper",
		"description": "Helps with email workflows.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.AppDir, "skills", "email-helper", "SKILL.md")); err != nil {
		t.Fatalf("single skill missing: %v", err)
	}
	if _, err := tool.Call(context.Background(), map[string]any{
		"skills": []any{
			map[string]any{"name": "PDF Parse", "content": "---\nname: pdf-parse\ndescription: Parse PDFs.\n---\n\n# PDF Parse"},
			map[string]any{"name": "CSV Tools", "description": "Work with CSV files."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"pdf-parse", "csv-tools"} {
		if _, err := os.Stat(filepath.Join(config.AppDir, "skills", slug, "SKILL.md")); err != nil {
			t.Fatalf("batch skill %s missing: %v", slug, err)
		}
	}
}

func TestHistorySessionToolsReadPersistedSessions(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	app := New(config)
	app.Submit(context.Background(), "/pause")
	sessions := listHistorySessionSummaries(config, 5)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	messages, err := readHistorySession(config, sessions[0].SessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestLogsRecentToolReadsNullBotLogs(t *testing.T) {
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	app := New(config)
	app.logInfo("diagnostic marker", "case", "logs_recent")
	output, err := logsRecentTool(app).Call(context.Background(), map[string]any{"limit": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "diagnostic marker") || !strings.Contains(output, "logs_recent") {
		t.Fatalf("logs output = %q", output)
	}
}

func TestModelGroupsFlatten(t *testing.T) {
	groups := []ModelGroup{{Provider: "x", Models: []ModelOption{{Provider: "x", ID: "a"}}}}
	flat := FlattenModelGroups(groups)
	if len(flat) != 1 || flat[0].ID != "a" {
		t.Fatalf("flat = %#v", flat)
	}
}

func TestBaseSystemPromptDoesNotClaimAdminTools(t *testing.T) {
	prompt := baseSystemPrompt(DefaultConfig(), nil, nil, nil)
	if strings.Contains(strings.ToLower(prompt), "admin") {
		t.Fatalf("prompt should not claim admin tools:\n%s", prompt)
	}
	if !strings.Contains(prompt, "No MCP servers are currently configured.") {
		t.Fatalf("prompt missing MCP inventory:\n%s", prompt)
	}
}

func TestSafeConfigPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeConfigPath(root, filepath.Join("..", "outside.txt")); err == nil {
		t.Fatal("expected escape error")
	}
}
