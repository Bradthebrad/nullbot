package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

func TestSubmissionPauseDedupQueueRemovalAndHistoryCAS(t *testing.T) {
	a := submissionTestApp(t)
	a.modelFactory = func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(context.Context, []lc.BaseMessage, []tcagent.Tool) (lc.BaseMessage, error) {
			return lc.AI("done"), nil
		}), nil
	}
	a.setPaused(true)
	first := a.SubmitRoutedRequest(context.Background(), "first", "queue", "request-one", "")
	duplicate := a.SubmitRoutedRequest(context.Background(), "different", "also", "request-one", "")
	if first != duplicate || first.Status != "queued" {
		t.Fatalf("receipts: %+v %+v", first, duplicate)
	}
	second := a.SubmitRoutedRequest(context.Background(), "second", "queue", "request-two", "")
	if a.TryReplaceHistory(nil) {
		t.Fatal("replaced held queue history")
	}
	if !a.CancelQueuedSubmission("request-two") || a.CancelQueuedSubmission(second.ID) {
		t.Fatal("queue removal not once")
	}
	if r := a.SubmitRouted(context.Background(), "not admitted", "send"); r.Status != "rejected" {
		t.Fatal(r)
	}
	a.setPaused(false)
	completed := waitSubmission(t, a, first.ID)
	if completed.RequestID != "request-one" || completed.Status != "done" {
		t.Fatal(completed)
	}
	before := a.State().History
	if !a.TryReplaceHistoryIfUnchanged(before, []Message{{Role: "user", Content: "replacement"}}) {
		t.Fatal("idle CAS refused")
	}
	if a.TryReplaceHistoryIfUnchanged(before, nil) {
		t.Fatal("stale compaction overwrote history")
	}
	completed.Reply.Message = "mutated"
	completed.Reply.History = nil
	if got := waitSubmission(t, a, first.ID); got.Reply.Message != "done" || len(got.Reply.History) == 0 {
		t.Fatal("snapshot aliases stored reply")
	}
}

func TestSubmissionPublicOutputPersistenceAndLaneIsolation(t *testing.T) {
	a := submissionTestApp(t)
	primary := &invocationScope{id: "p", lane: "primary"}
	ctx := context.WithValue(context.Background(), invocationKey{}, primary)
	a.retainPublicCallback(ctx, "manager", callbacks.LLMReasoning("run", "Visible "))
	a.retainPublicCallback(ctx, "manager", callbacks.LLMReasoning("run", "summary"))
	a.retainPublicCallback(ctx, "manager", callbacks.Event{Event: callbacks.EventLLMCommentary, AgentID: "main", Data: callbacks.EventData{Token: "Checking files"}})
	a.retainPublicCallback(ctx, "manager", callbacks.Event{Event: callbacks.EventLLMEnd})
	h := a.State().History
	if len(h) != 2 || h[0].Content != "Visible summary" || h[0].Role != "reasoning" || h[1].Role != "commentary" {
		t.Fatalf("history=%+v", h)
	}
	also := context.WithValue(context.Background(), invocationKey{}, &invocationScope{id: "a", lane: "also"})
	a.retainPublicCallback(also, "worker", callbacks.Event{Event: callbacks.EventLLMCommentary, AgentID: "researcher", Data: callbacks.EventData{Token: "Independent public output"}})
	if len(a.State().History) != 2 {
		t.Fatal("Also leaked into primary history")
	}
	var journal string
	filepath.WalkDir(a.Config().AppDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			b, _ := os.ReadFile(path)
			journal += string(b)
		}
		return nil
	})
	for _, want := range []string{"Visible summary", "Checking files", "Independent public output", `"submission_id":"a"`, `"lane":"also"`} {
		if !strings.Contains(journal, want) {
			t.Fatalf("journal missing %s: %s", want, journal)
		}
	}
}

func TestSubmissionOwnedWorkerLookup(t *testing.T) {
	a := submissionTestApp(t)
	one := context.WithValue(context.Background(), invocationKey{}, &invocationScope{id: "one", lane: "primary"})
	two := context.WithValue(context.Background(), invocationKey{}, &invocationScope{id: "two", lane: "also"})
	id := a.startTask("worker", "subagent", "private result", nil)
	a.assignTaskScope(two, id)
	if _, ok := a.ownedTaskDetails(one, id); ok {
		t.Fatal("cross-lane worker exposed")
	}
	if _, ok := a.ownedTaskDetails(two, id); !ok {
		t.Fatal("own worker unavailable")
	}
}

func TestSubmissionSteerTargetAndSelectedSkills(t *testing.T) {
	a := submissionTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	a.modelFactory = func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
			calls++
			if calls == 1 {
				close(started)
				<-release
				return lc.AI("old answer"), nil
			}
			text := lcContentText(m[len(m)-1].Content)
			if !strings.Contains(text, "correct course") || !strings.Contains(text, "selected skill payload") {
				t.Errorf("prepared instruction missing skill: %s", text)
			}
			return lc.AI("updated answer"), nil
		}), nil
	}
	first := a.SubmitRouted(context.Background(), "begin", "send")
	awaitSignal(t, started)
	ctx := context.WithValue(context.Background(), selectedSkillsKey{}, "selected skill payload")
	if r := a.SubmitRoutedRequest(ctx, "correct course", "steer", "wrong", "other-job"); r.Status != "rejected" {
		t.Fatal(r)
	}
	accepted := a.SubmitRoutedRequest(ctx, "correct course", "steer", "right", first.ID)
	if accepted.Status != "accepted" {
		t.Fatal(accepted)
	}
	if duplicate := a.SubmitRoutedRequest(ctx, "correct course", "steer", "right", first.ID); duplicate != accepted {
		t.Fatal("steer not idempotent")
	}
	close(release)
	waitSubmission(t, a, first.ID)
	count := 0
	for _, m := range a.State().History {
		if m.Content == "correct course" {
			count++
		}
		if strings.Contains(m.Content, "selected skill payload") {
			t.Fatal("skill payload exposed in visible history")
		}
	}
	if count != 1 || calls != 2 {
		t.Fatalf("count=%d calls=%d", count, calls)
	}
	if r := a.SubmitRoutedRequest(ctx, "too late", "steer", "late", first.ID); r.Status != "rejected" {
		t.Fatal(r)
	}
}

func TestSubmissionCanceledHeldContext(t *testing.T) {
	a := submissionTestApp(t)
	a.setPaused(true)
	ctx, cancel := context.WithCancel(context.Background())
	receipt := a.SubmitRouted(ctx, "held", "queue")
	cancel()
	if s := waitSubmission(t, a, receipt.ID); s.Status != "canceled" {
		t.Fatal(s)
	}
	if state := a.SubmissionState(); state.Busy || len(state.Queue) != 0 {
		t.Fatal(state)
	}
}

func TestSubmissionSteerBroadcastActiveAndFutureWorkers(t *testing.T) {
	a := submissionTestApp(t)
	scope := &invocationScope{id: "primary", lane: "primary", inbox: &tcagent.InstructionInbox{}}
	ctx := context.WithValue(context.Background(), invocationKey{}, scope)
	active, release := a.workerInstructionInbox(ctx, "active")
	defer release()
	if err := scope.broadcastInstruction(tcagent.Instruction{ID: "instruction", Text: "new direction"}); err != nil {
		t.Fatal(err)
	}
	future, releaseFuture := a.workerInstructionInbox(ctx, "future")
	defer releaseFuture()
	for _, inbox := range []*tcagent.InstructionInbox{scope.inbox, active, future} {
		model := submissionModelFunc(func(_ context.Context, m []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
			count := 0
			for _, message := range m {
				if lcContentText(message.Content) == "new direction" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("recipient received instruction %d times", count)
			}
			return lc.AI("done"), nil
		})
		if _, err := tcagent.New(tcagent.Config{Model: model}).InvokeMessagesWithInbox(ctx, []lc.BaseMessage{lc.Human("task")}, inbox); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.broadcastInstruction(tcagent.Instruction{Text: "too late"}); err == nil {
		t.Fatal("terminal manager accepted broadcast")
	}
}
