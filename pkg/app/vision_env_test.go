package app

import "testing"

func TestVisionModelPassedOnlyForProvidersTheToolsCall(t *testing.T) {
	entry := MCPEntry{Command: "nullbot-parsers-mcp.exe"}
	for _, tc := range []struct {
		provider, model string
		want            bool
	}{
		{"openai", "gpt-5.1", true},
		{"openrouter", "openai/gpt-5.1", true},
		{"anthropic", "claude-sonnet-5-5", false},
		{"codex", "gpt-5.1-codex", false},
	} {
		config := DefaultConfig()
		config.AppDir = t.TempDir()
		config.Model.Provider, config.Model.Model = tc.provider, tc.model
		env := mcpRuntimeEnv(config, "nullbot-parsers-mcp", entry)
		if got := env["NULLBOT_VISION_MODEL"] == tc.model; got != tc.want {
			t.Errorf("%s: NULLBOT_VISION_MODEL = %q, want passed %v", tc.provider, env["NULLBOT_VISION_MODEL"], tc.want)
		}
		if !tc.want && env["NULLBOT_VISION_PROVIDER"] != "" {
			t.Errorf("%s: NULLBOT_VISION_PROVIDER = %q", tc.provider, env["NULLBOT_VISION_PROVIDER"])
		}
	}
}
