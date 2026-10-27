package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bradthebrad/tinychain/agent"
)

func historyTool(t *testing.T, config Config, name string) agent.Tool {
	t.Helper()
	for _, tool := range BuiltinToolsFor(config, New(config), false) {
		if tool.Definition().Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not registered", name)
	return nil
}

func callHistoryTool(t *testing.T, tool agent.Tool, args map[string]any) string {
	t.Helper()
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("%s(%v): %v", tool.Definition().Name, args, err)
	}
	return out
}

// Two sessions: an older one about invoices, and the current one, which has
// messages and tool calls saved by the running app.
func seedHistory(t *testing.T) (*App, Config, string) {
	t.Helper()
	a := submissionTestApp(t)
	config := a.Config()
	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	old := []Message{
		{Role: "user", Content: "Reconcile the September invoices", Time: base},
		{Role: "assistant", Content: "Found 3 invoices that don't match the ledger: INV-104, INV-117, INV-120.", Time: base.Add(time.Minute)},
	}
	for _, m := range old {
		if err := appendHistoryMessage(config, "20261020-090000", m, ""); err != nil {
			t.Fatal(err)
		}
	}
	oldFile := filepath.Join(config.AppDir, "history", "20261020-090000.jsonl")
	if err := os.Chtimes(oldFile, base.Add(time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	a.persistMessage(Message{Role: "user", Content: "Check the nightly export failure", Time: now.Add(-3 * time.Minute)})
	a.appendActivity(ActivityRecord{Time: now.Add(-2 * time.Minute), Status: "tool start", Tool: "search_issues", Args: map[string]any{"query": "nightly export"}})
	a.appendActivity(ActivityRecord{Time: now.Add(-2 * time.Minute), Status: "tool complete", Tool: "search_issues", Server: "tracker", Args: map[string]any{"query": "nightly export"}, Result: "OPS-2219 Nightly export failed (open)"})
	a.appendActivity(ActivityRecord{Time: now.Add(-90 * time.Second), Status: "tool error", Tool: "project_write_file", Args: map[string]any{"path": "D:\\docs\\status.md"}, Error: "project docs is read-only"})
	a.appendActivity(ActivityRecord{Status: "response delta", Detail: "NEVER SAVED"})
	long := "The export failed because the upload timed out. " + strings.Repeat("Details. ", 200) + "TAIL-MARKER"
	a.persistMessage(Message{Role: "assistant", Content: long, Time: now.Add(-time.Minute)})
	return a, config, a.sessionID
}

func TestFinishedToolCallsAreSavedWithTheSession(t *testing.T) {
	_, config, session := seedHistory(t)
	data, err := os.ReadFile(activityHistoryPath(config, session))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "\n") != 2 || !strings.Contains(text, "OPS-2219") || !strings.Contains(text, "read-only") {
		t.Fatalf("activity log = %s", text)
	}
	if strings.Contains(text, "NEVER SAVED") || strings.Contains(text, `"tool start"`) {
		t.Fatalf("starts or deltas saved: %s", text)
	}
	// The activity folder is not mistaken for a session.
	for _, s := range listHistorySessionSummaries(config, 0) {
		if s.SessionID == activityHistoryDir {
			t.Fatal("activity folder listed as a session")
		}
	}
}

func TestSearchHistoryFindsMessagesAndToolCallsWithFilters(t *testing.T) {
	_, config, session := seedHistory(t)

	all, err := SearchHistory(config, HistoryQuery{Text: "export"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Hits) != 3 || all.Hits[0].Kind != "message" || all.Hits[0].Role != "assistant" {
		t.Fatalf("export hits = %+v", all.Hits)
	}
	if all.Hits[1].Kind != "tool" || all.Hits[1].Tool != "search_issues" || all.Hits[1].SessionID != session {
		t.Fatalf("tool hit = %+v", all.Hits[1])
	}

	phrase, _ := SearchHistory(config, HistoryQuery{Text: `"ledger: INV-104"`})
	if len(phrase.Hits) != 1 || phrase.Hits[0].SessionID != "20261020-090000" || phrase.Hits[0].Index != 1 {
		t.Fatalf("phrase hits = %+v", phrase.Hits)
	}
	if words, _ := SearchHistory(config, HistoryQuery{Text: "invoices september"}); len(words.Hits) != 1 {
		t.Fatalf("all-words match = %+v", words.Hits)
	}

	users, _ := SearchHistory(config, HistoryQuery{Roles: []string{"user"}})
	if len(users.Hits) != 2 {
		t.Fatalf("user hits = %+v", users.Hits)
	}
	tools, _ := SearchHistory(config, HistoryQuery{Tool: "tracker"})
	if len(tools.Hits) != 1 || tools.Hits[0].Tool != "search_issues" {
		t.Fatalf("tool filter = %+v", tools.Hits)
	}
	denied, _ := SearchHistory(config, HistoryQuery{Text: "read-only", Roles: []string{"tool"}})
	if len(denied.Hits) != 1 || denied.Hits[0].Status != "tool error" {
		t.Fatalf("error hits = %+v", denied.Hits)
	}

	recent, _ := SearchHistory(config, HistoryQuery{Since: time.Now().Add(-24 * time.Hour)})
	for _, hit := range recent.Hits {
		if hit.SessionID != session {
			t.Fatalf("since filter let through %+v", hit)
		}
	}
	older, _ := SearchHistory(config, HistoryQuery{Until: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)})
	if len(older.Hits) != 2 {
		t.Fatalf("until filter = %+v", older.Hits)
	}

	page1, _ := SearchHistory(config, HistoryQuery{Limit: 2})
	page2, _ := SearchHistory(config, HistoryQuery{Limit: 2, Offset: 2})
	if !page1.More || len(page2.Hits) != 2 || page1.Hits[1] == page2.Hits[0] {
		t.Fatalf("paging: %+v then %+v", page1, page2)
	}

	if _, err := SearchHistory(config, HistoryQuery{SessionID: "../../etc/passwd"}); err == nil {
		t.Fatal("escaping session id accepted")
	}
}

func TestHistoryToolsSearchAndReadFullText(t *testing.T) {
	_, config, session := seedHistory(t)

	out := callHistoryTool(t, historyTool(t, config, "history_search"), map[string]any{"query": "export", "since": "7d"})
	if !strings.Contains(out, session+" #1") || !strings.Contains(out, "tool #0") || !strings.Contains(out, "search_issues (tracker)") {
		t.Fatalf("search output:\n%s", out)
	}
	if out := callHistoryTool(t, historyTool(t, config, "history_search"), map[string]any{"role": "tool", "limit": 1.0}); !strings.Contains(out, "More results: offset=1") {
		t.Fatalf("paging hint missing:\n%s", out)
	}

	read := historyTool(t, config, "history_session_read")
	preview := callHistoryTool(t, read, map[string]any{"session_id": session})
	if strings.Contains(preview, "TAIL-MARKER") {
		t.Fatal("preview returned the whole message")
	}
	full := callHistoryTool(t, read, map[string]any{"session_id": session, "start": 1.0, "limit": 1.0, "full": true})
	if !strings.Contains(full, "#1 ") || !strings.Contains(full, "TAIL-MARKER") || strings.Contains(full, "#0 ") {
		t.Fatalf("full read:\n%s", full)
	}
	withTools := callHistoryTool(t, read, map[string]any{"session_id": session, "start": 0.0, "include_tools": true})
	if !strings.Contains(withTools, "tool #0") || !strings.Contains(withTools, `args={"query":"nightly export"}`) || !strings.Contains(withTools, "error: project docs is read-only") {
		t.Fatalf("tools not interleaved:\n%s", withTools)
	}
	if first := callHistoryTool(t, read, map[string]any{"session_id": "20261020-090000", "start": 0.0, "limit": 1.0}); !strings.Contains(first, "More follows: start=1") {
		t.Fatalf("continuation hint missing:\n%s", first)
	}
}

func TestHistorySearchIsReadOnlyEverywhere(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	req := ClassifyTool("history_search", "", agent.ToolInfo{}, "")
	if req.Capability != CapRead || !req.Internal {
		t.Fatalf("history_search classified as %+v", req)
	}
	decision := AuthorizeToolCall(c, req, map[string]any{"query": "x"})
	if !decision.Allowed {
		t.Fatalf("history_search blocked in a read-only project: %+v", decision)
	}
}

func TestParseHistoryTime(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"90m":                  now.Add(-90 * time.Minute),
		"36h":                  now.Add(-36 * time.Hour),
		"7d":                   now.AddDate(0, 0, -7),
		"2w":                   now.AddDate(0, 0, -14),
		"2026-10-01T08:00:00Z": time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := ParseHistoryTime(in, false, now)
		if err != nil || !got.Equal(want) {
			t.Fatalf("%s = %v, %v", in, got, err)
		}
	}
	if day, _ := ParseHistoryTime("2026-10-01", true, now); day.Day() != 1 || day.Hour() != 23 {
		t.Fatalf("end of day = %v", day)
	}
	if _, err := ParseHistoryTime("last tuesday", false, now); err == nil {
		t.Fatal("nonsense accepted")
	}
}
