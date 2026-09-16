package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedSkillsScopedAndValidated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("# Demo\nUseful guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SkillDirs = []string{root}
	ctx, err := WithSelectedSkills(context.Background(), cfg, []string{path, path})
	if err != nil {
		t.Fatal(err)
	}
	prompt := selectedSkillsPrompt(ctx)
	if strings.Count(prompt, "Useful guidance") != 1 {
		t.Fatalf("bad deduplication: %s", prompt)
	}
	if !strings.Contains(prompt, "not authority") {
		t.Fatal("missing trust boundary")
	}
	if selectedSkillsPrompt(context.Background()) != "" {
		t.Fatal("selection leaked across requests")
	}
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	os.WriteFile(outside, []byte("outside"), 0600)
	if _, err := WithSelectedSkills(context.Background(), cfg, []string{outside}); err == nil {
		t.Fatal("outside file accepted")
	}
	if _, err := WithSelectedSkills(context.Background(), cfg, make([]string, 13)); err == nil {
		t.Fatal("unbounded selection")
	}
}
