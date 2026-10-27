package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

func skillInvocationApp(t *testing.T) (*App, string) {
	t.Helper()
	a := submissionTestApp(t)
	dir := filepath.Join(t.TempDir(), "skills")
	skill := filepath.Join(dir, "release-notes", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: release-notes\ndescription: Draft release notes\n---\nGroup changes by area. MARKER-7731\n"
	if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.SkillDirs = []string{dir}
	a.mu.Lock()
	a.config = cfg
	a.mu.Unlock()
	return a, skill
}

func TestSkillInvocationSplitsLeadingSkillName(t *testing.T) {
	a, skill := skillInvocationApp(t)
	cfg := a.Config()
	rest, path, ok := SkillInvocation(cfg, "  /release-notes draft v2\nwith fixes ")
	if !ok || rest != "draft v2\nwith fixes" {
		t.Fatalf("got %q %q %v", rest, path, ok)
	}
	if real, _ := filepath.EvalSymlinks(path); real != mustEval(t, skill) {
		t.Fatalf("path %q, want %q", path, skill)
	}
	for _, input := range []string{"/help", "/plan ship it", "/unknown-skill hi", "release-notes hi", "/"} {
		if _, _, ok := SkillInvocation(cfg, input); ok {
			t.Errorf("%q treated as a skill", input)
		}
	}
	if _, _, err := withSkillInvocation(context.Background(), cfg, "/release-notes"); err != errSkillNeedsMessage {
		t.Fatalf("bare skill: %v", err)
	}
}

func TestLeadingSkillNameRunsAsMessageWithSkill(t *testing.T) {
	a, _ := skillInvocationApp(t)
	var seen []lc.BaseMessage
	a.modelFactory = func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(_ context.Context, m []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
			seen = m
			return lc.AI("drafted"), nil
		}), nil
	}
	receipt := a.SubmitRouted(context.Background(), "/release-notes draft v2", "send")
	if receipt.Status == "rejected" {
		t.Fatalf("rejected: %+v", receipt)
	}
	waitSubmission(t, a, receipt.ID)
	var all strings.Builder
	for _, m := range seen {
		all.WriteString(lcContentText(m.Content))
		all.WriteString("\n")
	}
	if !strings.Contains(all.String(), "MARKER-7731") {
		t.Fatalf("skill content not sent to the model:\n%s", all.String())
	}
	if text := lcContentText(seen[len(seen)-1].Content); !strings.Contains(text, "draft v2") || strings.Contains(text, "/release-notes") {
		t.Fatalf("last message %q", text)
	}
	reply := a.Submit(context.Background(), "/release-notes")
	if !strings.Contains(reply.Message, "Add a message") {
		t.Fatalf("bare skill reply %q", reply.Message)
	}
}

func TestSkillSelectionsAddUp(t *testing.T) {
	a, skill := skillInvocationApp(t)
	ctx, err := WithSelectedSkills(context.Background(), a.Config(), []string{skill})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = WithSelectedSkills(ctx, a.Config(), []string{skill})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(selectedSkillsPrompt(ctx), "MARKER-7731"); n != 1 {
		t.Fatalf("skill included %d times", n)
	}
}

func TestCommandMessagesOnlyMatchBuiltins(t *testing.T) {
	if !isCommandMessage("/help me") || isCommandMessage("/etc/hosts looks wrong") || isCommandMessage("hello /help") {
		t.Fatal("isCommandMessage")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
