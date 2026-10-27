package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

func answeringApp(t *testing.T) *App {
	t.Helper()
	a := submissionTestApp(t)
	a.modelFactory = func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
			return lc.AI(strings.Repeat("answer ", 40)), nil
		}), nil
	}
	return a
}

// A long session must not grow retained state with the number of turns:
// jobs never copy history, tasks/logs/receipts are pruned, and streamed
// token fragments never enter the activity ring.
func TestLongSessionStateStaysBounded(t *testing.T) {
	a := answeringApp(t)
	const turns = 300
	for i := 0; i < turns; i++ {
		receipt := a.SubmitRoutedRequest(context.Background(), fmt.Sprintf("question %d", i), "send", fmt.Sprintf("req-%d", i), "")
		if receipt.Status == "rejected" {
			t.Fatalf("turn %d rejected: %s", i, receipt.Error)
		}
		waitSubmission(t, a, receipt.ID)
		for j := 0; j < 20; j++ {
			a.appendActivity(ActivityRecord{Kind: "on_llm_new_token", Status: "response delta", Detail: "tok"})
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if got := len(a.history); got < turns*2 {
		t.Fatalf("history = %d, want at least %d", got, turns*2)
	}
	if len(a.submissions) > retainedSubmissions {
		t.Fatalf("retained submissions = %d", len(a.submissions))
	}
	for id, job := range a.submissions {
		if job.Reply == nil || len(job.Reply.History) != 0 || job.full != nil {
			t.Fatalf("job %s retains history: reply=%v full=%v", id, job.Reply != nil, job.full != nil)
		}
	}
	if len(a.tasks) > retainedFinishedTasks+1 {
		t.Fatalf("tasks = %d", len(a.tasks))
	}
	if len(a.activity) > activityRingSize {
		t.Fatalf("activity = %d", len(a.activity))
	}
	for _, record := range a.activity {
		if record.IsStreamDelta() {
			t.Fatal("stream delta stored in activity ring")
		}
	}
	if len(a.logs) > logRingSize {
		t.Fatalf("logs = %d", len(a.logs))
	}
	if len(a.requestReceipts) > retainedReceipts || len(a.receiptOrder) > retainedReceipts {
		t.Fatalf("receipts = %d/%d", len(a.requestReceipts), len(a.receiptOrder))
	}
	data, _ := json.Marshal(a.submissionStateLocked())
	if len(data) > 64*1024 {
		t.Fatalf("submission snapshot is %d bytes", len(data))
	}
}

func TestSynchronousSubmitStillReturnsFullHistory(t *testing.T) {
	a := answeringApp(t)
	reply := a.SubmitWithActivity(context.Background(), "hello", nil)
	if len(reply.History) < 2 || !strings.Contains(reply.Message, "answer") {
		t.Fatalf("reply = %q history=%d", reply.Message, len(reply.History))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, job := range a.submissions {
		if job.full != nil || (job.Reply != nil && len(job.Reply.History) > 0) {
			t.Fatal("full reply retained after the waiter took it")
		}
	}
}

func TestTaskActivitySkipsStreamDeltas(t *testing.T) {
	a := submissionTestApp(t)
	id := a.startTask("main", "primary", "x", nil)
	a.mu.Lock()
	for i := 0; i < 500; i++ {
		a.recordTaskActivityLocked(id, ActivityRecord{Status: "response delta", Detail: "tok"})
	}
	a.recordTaskActivityLocked(id, ActivityRecord{Status: "tool start", Name: "web_search", Args: map[string]any{"query": "x"}})
	task := a.tasks[id]
	stored, current := len(task.Activity), task.Current
	a.mu.Unlock()
	if stored != 1 {
		t.Fatalf("task stored %d records", stored)
	}
	if !strings.HasPrefix(current, "tool start") {
		t.Fatalf("current = %q", current)
	}
}

func TestToolRecordsAreStructured(t *testing.T) {
	long := strings.Repeat("x", 5000)
	record := activityRecordFromCallback(callbacksEventForTest(long))
	if record.CallID != "call_9" || record.Tool != "web_search" || record.Server != "web" || record.Title != "Search the web" {
		t.Fatalf("record = %#v", record)
	}
	query, _ := record.Args["query"].(string)
	if len(query) > toolArgStringChars+10 || record.Args["limit"] != float64(5) {
		t.Fatalf("args not bounded per field: %#v", record.Args)
	}
	if _, err := json.Marshal(record.Args); err != nil {
		t.Fatal(err)
	}
}

func callbacksEventForTest(long string) callbacks.Event {
	return callbacks.Event{
		Event:    callbacks.EventToolStart,
		Name:     "web_search",
		RunID:    "call_9",
		Metadata: map[string]any{"title": "Search the web", "server": "web", "read_only": true},
		Data:     callbacks.EventData{Input: map[string]any{"query": long, "limit": float64(5)}},
	}
}
