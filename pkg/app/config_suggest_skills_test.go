package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSuggestMatchingSkillsPersistence(t *testing.T) {
	if DefaultConfig().UI.SuggestMatchingSkills {
		t.Fatal("matching skill suggestions must default to disabled")
	}
	for _, tc := range []struct {
		name  string
		field string
		want  bool
	}{
		{name: "legacy omitted"},
		{name: "explicit false", field: `,"suggest_matching_skills":false`},
		{name: "existing true", field: `,"suggest_matching_skills":true`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			legacy := `{"bot_name":"Persistence Test","ui":{"theme":"steel"` + tc.field + `}}`
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadOrInitConfigAt(dir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.SuggestMatchingSkills != tc.want {
				t.Fatalf("loaded suggestions = %v, want %v", cfg.UI.SuggestMatchingSkills, tc.want)
			}
			// Exercise both toggle directions and a restart after each save.
			for _, enabled := range []bool{true, false} {
				cfg.UI.SuggestMatchingSkills = enabled
				if err := SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				cfg, err = LoadOrInitConfigAt(dir)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.UI.SuggestMatchingSkills != enabled {
					t.Fatalf("reloaded suggestions = %v, want %v", cfg.UI.SuggestMatchingSkills, enabled)
				}
				if cfg.UI.Theme != "steel" || cfg.BotName != "Persistence Test" {
					t.Fatal("saving suggestion preference changed unrelated settings")
				}
				data, err := os.ReadFile(filepath.Join(dir, "config.json"))
				if err != nil {
					t.Fatal(err)
				}
				var stored struct {
					UI struct {
						SuggestMatchingSkills *bool `json:"suggest_matching_skills"`
					} `json:"ui"`
				}
				if err := json.Unmarshal(data, &stored); err != nil {
					t.Fatal(err)
				}
				if stored.UI.SuggestMatchingSkills == nil || *stored.UI.SuggestMatchingSkills != enabled {
					t.Fatal("config JSON must explicitly persist the boolean under ui.suggest_matching_skills")
				}
			}
		})
	}
}
