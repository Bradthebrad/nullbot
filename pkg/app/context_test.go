package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

func contextTestConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AppDir = t.TempDir()
	cfg.WorkspaceDir = t.TempDir()
	cfg.EnabledMCPServers = nil
	cfg.Model = ModelConfig{Provider: "anthropic", Model: "claude-sonnet-4-20250514"}
	cfg.SubagentModel = ModelConfig{Provider: "openai", Model: "gpt-4o-mini"}
	return cfg
}

func TestContextWindowResolutionOrder(t *testing.T) {
	cfg := contextTestConfig(t)
	model := cfg.Model
	if w := ResolveContextWindow(cfg, model); w.Tokens != 200000 || w.Source != "built-in" {
		t.Fatalf("family size: %+v", w)
	}
	recordContextWindows(cfg, "anthropic", map[string]int{model.Model: 180000}, "provider", "Reported by Anthropic")
	if w := ResolveContextWindow(cfg, model); w.Tokens != 180000 || w.Source != "provider" {
		t.Fatalf("reported size: %+v", w)
	}
	recordContextWindows(cfg, "anthropic", map[string]int{model.Model: 150000}, "learned", "Named by the provider in an error")
	recordContextWindows(cfg, "anthropic", map[string]int{model.Model: 180000}, "provider", "Reported by Anthropic")
	if w := ResolveContextWindow(cfg, model); w.Tokens != 150000 || w.Source != "learned" {
		t.Fatalf("a larger reported size replaced what the provider actually accepted: %+v", w)
	}
	cfg.Compaction.ContextWindows = map[string]int{ModelKey("anthropic", model.Model): 90000}
	if w := ResolveContextWindow(cfg, model); w.Tokens != 90000 || w.Source != "override" {
		t.Fatalf("override: %+v", w)
	}
	if w := ResolveContextWindow(cfg, ModelConfig{Provider: "openrouter", Model: "someone/unknown-model"}); w.Tokens != 64000 || w.Source != "default" {
		t.Fatalf("unknown model: %+v", w)
	}
	if w := ResolveContextWindow(cfg, ModelConfig{Provider: "openrouter", Model: "openai/gpt-5.2"}); w.Tokens != 272000 {
		t.Fatalf("OpenRouter id with a provider prefix: %+v", w)
	}
	// Sizes survive a restart.
	windowStore.Lock()
	delete(windowStore.loaded, contextWindowsPath(cfg))
	windowStore.Unlock()
	cfg.Compaction.ContextWindows = nil
	if w := ResolveContextWindow(cfg, model); w.Tokens != 150000 {
		t.Fatalf("saved size not reloaded: %+v", w)
	}
}

func TestContextPolicyFollowsEachAgentsModel(t *testing.T) {
	cfg := contextTestConfig(t)
	main := agentContextPolicy(cfg, cfg.Model)
	sub := agentContextPolicy(cfg, cfg.SubagentModel)
	if main.MaxTokens != 200000 || sub.MaxTokens != 128000 {
		t.Fatalf("windows: main %d, subagent %d", main.MaxTokens, sub.MaxTokens)
	}
	if main.SoftRatio != 0.60 || main.HardRatio != 0.85 || main.ToolDigestChars != 600 {
		t.Fatalf("ratios: %+v", main)
	}
	if len(main.PreserveTools) == 0 || main.PreserveTools[0] != "skill_read" || !strings.Contains(main.Note, "history_session_read") {
		t.Fatalf("skills not preserved or no history note: %+v", main)
	}
}

func TestOldThresholdMigrates(t *testing.T) {
	if c := normalizeCompaction(CompactionConfig{ThresholdRatio: 0.75}); c.SummarizeRatio != 0.85 || c.ThresholdRatio != 0 {
		t.Fatalf("old default kept: %+v", c)
	}
	if c := normalizeCompaction(CompactionConfig{ThresholdRatio: 0.7}); c.SummarizeRatio != 0.7 || c.ShortenRatio >= 0.7 {
		t.Fatalf("customized threshold lost: %+v", c)
	}
}

func TestCarriedHistoryFitsABudgetAndPointsAtTheRest(t *testing.T) {
	cfg := contextTestConfig(t)
	cfg.Compaction.ContextWindows = map[string]int{ModelKey(cfg.Model.Provider, cfg.Model.Model): 20000}
	cfg.Compaction.HistoryRatio = 0.3 // 6,000 tokens
	var history []Message
	for i := 0; i < 40; i++ {
		history = append(history,
			Message{Role: "user", Content: fmt.Sprintf("question %d %s", i, strings.Repeat("q", 1200))},
			Message{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, strings.Repeat("a", 1200))})
	}
	out := carriedHistory(cfg, history, "s1")
	if tokens := tcagent.EstimateTokens(out); tokens > 6500 {
		t.Fatalf("carried %d tokens, budget is 6,000", tokens)
	}
	if out[0].Type != lc.RoleSystem || !strings.Contains(lcContentText(out[0].Content), "history_session_read reads them from session s1") {
		t.Fatalf("no note about the messages left out: %q", lcContentText(out[0].Content))
	}
	if out[1].Type != lc.RoleHuman {
		t.Fatal("carried history must start with a request")
	}
	if last := lcContentText(out[len(out)-1].Content); !strings.HasPrefix(last, "answer 39") {
		t.Fatalf("newest message missing: %q", last[:20])
	}
	// Fewer than 12 messages fit here, but a bigger window carries more than
	// the old 12-message cutoff.
	cfg.Compaction.ContextWindows = nil
	if out := carriedHistory(cfg, history, "s1"); len(out) <= 13 {
		t.Fatalf("a 200,000-token window carried only %d messages", len(out))
	}
}

// summaryModel answers summary requests (no tools) and normal requests.
type summaryModel struct {
	mu       sync.Mutex
	requests [][]lc.BaseMessage
}

func (m *summaryModel) Call(_ context.Context, messages []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(tools) == 0 {
		return lc.BaseMessage{Type: lc.RoleAI, Content: lc.TextContent("SUMMARY of the database migration"), UsageMetadata: &lc.UsageMetadata{InputTokens: 900, OutputTokens: 40, TotalTokens: 940}}, nil
	}
	m.requests = append(m.requests, messages)
	return lc.AI("next answer"), nil
}

func TestCompactConversationSummarizesAndLaterRequestsStartThere(t *testing.T) {
	cfg := contextTestConfig(t)
	a := New(cfg)
	model := &summaryModel{}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	if _, err := a.CompactConversation(context.Background(), ""); err != errNothingToCompact {
		t.Fatalf("empty conversation: %v", err)
	}
	a.mu.Lock()
	a.history = []Message{
		{Role: "user", Content: "migrate the database"},
		{Role: "assistant", Content: "Done: moved 14 tables."},
		{Role: "assistant", Content: "Files panel opened.", VisibleOnly: true},
	}
	a.mu.Unlock()
	reply := a.Submit(context.Background(), "/compact table names")
	if !strings.Contains(reply.Message, "Compacted 2 messages") {
		t.Fatalf("reply %q", reply.Message)
	}
	history := a.State().History
	var record *Message
	for i := range history {
		if history[i].Role == compactionRole {
			record = &history[i]
		}
	}
	if record == nil || !strings.Contains(record.Content, "SUMMARY of the database migration") || !strings.Contains(record.Content, "history_search") {
		t.Fatalf("no compaction record: %#v", history)
	}
	if len(history) < 3 || history[0].Content != "migrate the database" {
		t.Fatal("messages before the compaction must stay visible")
	}
	carried := a.langChainHistory()
	if len(carried) != 1 || carried[0].Type != lc.RoleSystem || !strings.Contains(lcContentText(carried[0].Content), "SUMMARY") {
		t.Fatalf("next request should start from the summary: %#v", carried)
	}
	// The summary call's usage is recorded.
	found := false
	for _, r := range readUsageRecords(usagePath(cfg)) {
		if strings.Contains(r.Agent, "summary") && r.InputTokens == 900 {
			found = true
		}
	}
	if !found {
		t.Fatal("summary usage not recorded")
	}
	// A second compaction folds the first summary in.
	a.mu.Lock()
	a.history = append(a.history, Message{Role: "user", Content: "now add indexes"}, Message{Role: "assistant", Content: "Added 3 indexes."})
	a.mu.Unlock()
	if _, err := a.CompactConversation(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	carried = a.langChainHistory()
	if len(carried) != 1 || strings.Count(lcContentText(carried[0].Content), "Summary of earlier work") != 1 {
		t.Fatalf("summaries stacked: %#v", carried)
	}
}

func TestCompactRefusesWhileARequestRuns(t *testing.T) {
	a := New(contextTestConfig(t))
	a.mu.Lock()
	a.history = []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	a.primaryJob = &submissionJob{}
	a.mu.Unlock()
	if _, err := a.CompactConversation(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("compaction during a request: %v", err)
	}
}

func TestContextActivityRecordsSayWhatHappened(t *testing.T) {
	record := activityRecordFromCallback(callbacks.Event{Event: callbacks.EventChainEnd, Name: "context_shorten", Data: callbacks.EventData{Extra: map[string]any{"shortened": 6, "before_tokens": 142300, "after_tokens": 81000, "context_window": 200000}}})
	if record.Status != "context shortened" || record.Detail != "Shortened 6 earlier tool results: 142,300 → 81,000 tokens." || record.Context["context_window"] != 200000 {
		t.Fatalf("%+v", record)
	}
	start := activityRecordFromCallback(callbacks.Event{Event: callbacks.EventChatModelStart, Data: callbacks.EventData{Extra: map[string]any{"context_tokens": 5000, "context_window": 200000}}})
	if start.Context["context_tokens"] != 5000 {
		t.Fatalf("model start lacks context use: %+v", start)
	}
}

func TestContextWindowOverrideIsSetAndCleared(t *testing.T) {
	cfg := contextTestConfig(t)
	a := New(cfg)
	if err := a.SetContextWindowOverride("anthropic", cfg.Model.Model, 500); err == nil {
		t.Fatal("tiny override accepted")
	}
	if err := a.SetContextWindowOverride("anthropic", cfg.Model.Model, 120000); err != nil {
		t.Fatal(err)
	}
	if w := a.ContextWindowFor("anthropic", cfg.Model.Model); w.Tokens != 120000 || w.Source != "override" {
		t.Fatalf("%+v", w)
	}
	groups, _ := PeekModelGroups(a.Config())
	for _, group := range groups {
		for _, model := range group.Models {
			if model.Provider == "anthropic" && model.ID == cfg.Model.Model && model.ContextWindow != 120000 {
				t.Fatalf("catalog shows %d", model.ContextWindow)
			}
		}
	}
	if err := a.SetContextWindowOverride("anthropic", cfg.Model.Model, 0); err != nil {
		t.Fatal(err)
	}
	if w := a.ContextWindowFor("anthropic", cfg.Model.Model); w.Source == "override" {
		t.Fatalf("override not cleared: %+v", w)
	}
}

func TestOldThresholdMigratesWhenLoadedFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"app_dir":`+strconv.Quote(dir)+`,"compaction":{"enabled":true,"threshold_ratio":0.5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.AppDir = dir
	loaded, err := loadOrInit(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := loaded.Compaction; c.SummarizeRatio != 0.5 || c.ShortenRatio >= 0.5 {
		t.Fatalf("customized threshold lost on load: %+v", c)
	}
}

func TestNormalizingNeverChangesASharedMap(t *testing.T) {
	shared := map[string]int{"openai/a": 500, "openai/b": 90000}
	c := normalizeCompaction(CompactionConfig{ContextWindows: shared})
	if len(shared) != 2 || len(c.ContextWindows) != 1 {
		t.Fatalf("shared %v, normalized %v", shared, c.ContextWindows)
	}
}

type failingSummary struct{}

func (failingSummary) Call(_ context.Context, _ []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	if len(tools) == 0 {
		return lc.BaseMessage{}, fmt.Errorf("401 unauthorized")
	}
	return lc.AI("ok"), nil
}

func TestCompactSavesNothingWhenTheModelFails(t *testing.T) {
	a := New(contextTestConfig(t))
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return failingSummary{}, nil }
	a.mu.Lock()
	a.history = []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	a.mu.Unlock()
	if _, err := a.CompactConversation(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v", err)
	}
	for _, msg := range a.State().History {
		if msg.Role == compactionRole {
			t.Fatal("a compaction was saved although the summary failed")
		}
	}
}

// switchingModel opens another session while it summarizes.
type switchingModel struct{ app *App }

func (m switchingModel) Call(_ context.Context, _ []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	m.app.mu.Lock()
	m.app.sessionID = "another-session"
	m.app.mu.Unlock()
	return lc.AI("SUMMARY"), nil
}

func TestCompactDoesNotLandInAnotherSession(t *testing.T) {
	a := New(contextTestConfig(t))
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return switchingModel{app: a}, nil }
	a.mu.Lock()
	a.history = []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	a.mu.Unlock()
	if _, err := a.CompactConversation(context.Background(), ""); err == nil {
		t.Fatal("compaction applied to the session opened meanwhile")
	}
}

func TestOldLearnedSizesExpire(t *testing.T) {
	cfg := contextTestConfig(t)
	key := ModelKey(cfg.Model.Provider, cfg.Model.Model)
	windowStore.Lock()
	windowStore.loaded[contextWindowsPath(cfg)] = map[string]savedWindow{key: {ContextWindow: ContextWindow{Tokens: 50000, Source: "learned"}, Time: time.Now().Add(-8 * 24 * time.Hour)}}
	windowStore.Unlock()
	if w := ResolveContextWindow(cfg, cfg.Model); w.Tokens != 200000 {
		t.Fatalf("an old learned size still applies: %+v", w)
	}
}
