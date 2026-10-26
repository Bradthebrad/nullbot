package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

// loopModel asks for history_recent until told to stop, then answers. It
// signals each call on calls.
type loopModel struct {
	mu    sync.Mutex
	n     int
	calls chan int
	steps int
	// hold, when set, keeps the first call from returning until closed.
	hold chan struct{}
}

func (m *loopModel) Call(_ context.Context, messages []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	select {
	case m.calls <- n:
	default:
	}
	if n == 1 && m.hold != nil {
		<-m.hold
	}
	last := lcContentText(messages[len(messages)-1].Content)
	if strings.Contains(last, "asked you to stop") {
		return lc.AI("Wrapped up: read the history twice."), nil
	}
	if n <= m.steps {
		return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "h" + string(rune('0'+n)), Name: "history_recent", Args: map[string]any{}}}}, nil
	}
	return lc.AI("finished"), nil
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lastAssistant(a *App) string {
	history := a.State().History
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			return history[i].Content
		}
	}
	return ""
}

func TestPauseHoldsTheRunAtItsNextStepAndResumeCarriesOn(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 2, hold: make(chan struct{})}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	receipt := a.SubmitRouted(context.Background(), "read history", "send")
	<-model.calls
	a.PauseRuns()
	close(model.hold)
	time.Sleep(60 * time.Millisecond)
	model.mu.Lock()
	held := model.n
	model.mu.Unlock()
	state := a.SubmissionState()
	if !state.RunPaused || state.PrimaryID != receipt.ID {
		for _, m := range a.State().History {
			t.Logf("%s: %s", m.Role, m.Content)
		}
		model.mu.Lock()
		t.Logf("model calls %d", model.n)
		model.mu.Unlock()
		t.Fatalf("state while paused: %+v", state)
	}
	time.Sleep(60 * time.Millisecond)
	model.mu.Lock()
	if model.n != held {
		t.Fatalf("the model was called while paused (%d → %d)", held, model.n)
	}
	model.mu.Unlock()
	a.ResumeRuns()
	waitSubmission(t, a, receipt.ID)
	if got := lastAssistant(a); got != "finished" {
		t.Fatalf("after resume the run ended with %q", got)
	}
}

func TestStopWrapsUpAndTheQueueContinues(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 100}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	first := a.SubmitRouted(context.Background(), "keep reading", "send")
	<-model.calls
	queued := a.SubmitRouted(context.Background(), "then this", "queue")
	if err := a.StopCurrent(); err != nil {
		t.Fatal(err)
	}
	if !a.SubmissionState().Stopping {
		t.Fatal("not reported as stopping")
	}
	if r := a.SubmitRoutedRequest(context.Background(), "more", "steer", "", first.ID); r.Status != "rejected" {
		t.Fatal("steering accepted while stopping")
	}
	if done := waitSubmission(t, a, first.ID); done.Status != "done" {
		t.Fatalf("stopped request ended %q", done.Status)
	}
	if !strings.Contains(strings.Join(historyContents(a), "\n"), "Wrapped up: read the history twice.") {
		t.Fatal("no wrap-up answer saved")
	}
	waitSubmission(t, a, queued.ID)
}

func historyContents(a *App) []string {
	var out []string
	for _, m := range a.State().History {
		out = append(out, m.Content)
	}
	return out
}

func TestStopEverythingCancelsAndHoldsTheQueue(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 100}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	first := a.SubmitRouted(context.Background(), "keep reading", "send")
	<-model.calls
	queued := a.SubmitRouted(context.Background(), "then this", "queue")
	a.StopEverything()
	if done := waitSubmission(t, a, first.ID); done.Status != "canceled" && done.Status != "error" {
		t.Fatalf("first ended %q", done.Status)
	}
	time.Sleep(50 * time.Millisecond)
	state := a.SubmissionState()
	if !state.Paused || len(state.Queue) != 1 || state.Queue[0].ID != queued.ID {
		t.Fatalf("queue not held: %+v", state)
	}
	a.ResumeRuns()
	waitSubmission(t, a, queued.ID)
}

func TestSteerTaskReachesOnlyThatSubagent(t *testing.T) {
	a := submissionTestApp(t)
	scope := &invocationScope{id: "job", lane: "primary", inbox: &tcagent.InstructionInbox{}}
	ctx := context.WithValue(context.Background(), invocationKey{}, scope)
	a.mu.Lock()
	a.submissions = map[string]*submissionJob{"job": {Submission: Submission{ID: "job", Status: "running"}, scope: scope}}
	a.mu.Unlock()
	one, releaseOne := a.workerInstructionInbox(ctx, "task-1")
	defer releaseOne()
	two, releaseTwo := a.workerInstructionInbox(ctx, "task-2")
	defer releaseTwo()
	if err := a.SteerTask("task-1", "check the logs too"); err != nil {
		t.Fatal(err)
	}
	if err := a.SteerTask("task-9", "x"); err == nil {
		t.Fatal("steered a subagent that isn't running")
	}
	for name, inbox := range map[string]*tcagent.InstructionInbox{"task-1": one, "task-2": two} {
		var got int
		model := submissionModelFunc(func(_ context.Context, m []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
			for _, msg := range m {
				if lcContentText(msg.Content) == "check the logs too" {
					got++
				}
			}
			return lc.AI("ok"), nil
		})
		if _, err := tcagent.New(tcagent.Config{Model: model}).InvokeMessagesWithInbox(ctx, []lc.BaseMessage{lc.Human("work")}, inbox); err != nil {
			t.Fatal(err)
		}
		if want := map[string]int{"task-1": 1, "task-2": 0}[name]; got != want {
			t.Fatalf("%s received the guidance %d times", name, got)
		}
	}
}

func TestSubagentModelCanBeTheManagers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model = ModelConfig{Provider: "anthropic", Model: "claude-a"}
	cfg.SubagentModel = ModelConfig{}
	cfg = normalizeConfig(cfg)
	if cfg.SubagentModel.Provider != "" || effectiveModelConfig(cfg, cfg.SubagentModel).Model != "claude-a" {
		t.Fatalf("same-as-manager not kept: %+v", cfg.SubagentModel)
	}
	cfg.Model.Model = "claude-b"
	if effectiveModelConfig(cfg, cfg.SubagentModel).Model != "claude-b" {
		t.Fatal("subagents don't follow the manager")
	}
	cfg.SubagentModel = ModelConfig{Provider: "openai", Model: "gpt-5-mini"}
	if got := effectiveModelConfig(normalizeConfig(cfg), cfg.SubagentModel); got.Provider != "openai" {
		t.Fatalf("a different provider for subagents: %+v", got)
	}
}

func TestAPauseWithNothingLeftEndsByItself(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 0, hold: make(chan struct{})}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	receipt := a.SubmitRouted(context.Background(), "just answer", "send")
	<-model.calls
	a.PauseRuns() // lands while the final answer is being written
	close(model.hold)
	waitSubmission(t, a, receipt.ID)
	waitFor(t, "the pause to end", func() bool {
		s := a.SubmissionState()
		return !s.Paused && !s.RunPaused
	})
	next := a.SubmitRouted(context.Background(), "next", "send")
	if next.Status == "rejected" {
		t.Fatalf("the next message was refused: %+v", next)
	}
	waitSubmission(t, a, next.ID)
}

func TestStopEverythingWithAnEmptyQueueLeavesNothingHeld(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 100}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	first := a.SubmitRouted(context.Background(), "keep reading", "send")
	<-model.calls
	a.StopEverything()
	waitSubmission(t, a, first.ID)
	waitFor(t, "nothing held", func() bool { return !a.SubmissionState().Paused })
}

func TestStopOnlyStopsTheRequestItWasMeantFor(t *testing.T) {
	a := submissionTestApp(t)
	model := &loopModel{calls: make(chan int, 16), steps: 100}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return model, nil }
	first := a.SubmitRouted(context.Background(), "keep reading", "send")
	<-model.calls
	if err := a.StopRequest("some-earlier-request"); err == nil {
		t.Fatal("stopped a request other than the one named")
	}
	if a.SubmissionState().Stopping {
		t.Fatal("marked stopping")
	}
	if err := a.StopRequest(first.ID); err != nil {
		t.Fatal(err)
	}
	waitSubmission(t, a, first.ID)
}
