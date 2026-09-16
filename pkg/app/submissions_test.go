package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

type submissionModelFunc func(context.Context, []lc.BaseMessage, []tcagent.Tool) (lc.BaseMessage, error)

func (f submissionModelFunc) Call(c context.Context, m []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	return f(c, m, tools)
}
func submissionTestApp(t *testing.T) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AppDir = t.TempDir()
	cfg.WorkspaceDir = t.TempDir()
	cfg.EnabledMCPServers = nil
	cfg.Agent.MaxSubagents = 4
	return New(cfg)
}
func waitSubmission(t *testing.T, a *App, id string) Submission {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, s := range a.SubmissionState().Jobs {
			if s.ID == id && (s.Status == "done" || s.Status == "error" || s.Status == "canceled") {
				return s
			}
		}
		select {
		case <-deadline:
			t.Fatalf("submission %s did not finish: %#v", id, a.SubmissionState())
		case <-time.After(time.Millisecond):
		}
	}
}
func awaitSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("signal timed out")
	}
}

func TestSubmissionFIFOWaitsWorkersAndHistory(t *testing.T) {
	a := submissionTestApp(t)
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	rootFinal := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(releaseWorker) })
	var mu sync.Mutex
	var starts []string
	a.modelFactory = func(_ Config, worker bool) (tcagent.Model, error) {
		calls := 0
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
			if worker {
				close(workerStarted)
				select {
				case <-releaseWorker:
				case <-ctx.Done():
					return lc.BaseMessage{}, ctx.Err()
				}
				return lc.AI("worker done"), nil
			}
			calls++
			text := lcContentText(m[len(m)-1].Content)
			if calls == 1 {
				mu.Lock()
				starts = append(starts, text)
				mu.Unlock()
				if text == "first" {
					return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "spawn", Name: "spawn_subagent", Args: map[string]any{"name": "worker", "task": "finish later"}}}}, nil
				}
				if text == "second" {
					found := false
					for _, msg := range m {
						if lcContentText(msg.Content) == "first done" {
							found = true
						}
					}
					if !found {
						t.Error("next turn did not see committed primary history")
					}
				}
				return lc.AI(text + " done"), nil
			}
			close(rootFinal)
			return lc.AI("first done"), nil
		}), nil
	}
	first := a.SubmitRouted(context.Background(), "first", "send")
	awaitSignal(t, workerStarted)
	awaitSignal(t, rootFinal)
	if r := a.SubmitRouted(context.Background(), "must reject", "send"); r.Status != "rejected" {
		t.Fatalf("busy send=%#v", r)
	}
	second := a.SubmitRouted(context.Background(), "second", "queue")
	third := a.SubmitRouted(context.Background(), "third", "queue")
	if state := a.SubmissionState(); !state.Busy || len(state.Queue) != 2 {
		t.Fatalf("state=%#v", state)
	}
	if r := a.SubmitRouted(context.Background(), "too late", "steer"); r.Status != "rejected" {
		t.Fatalf("terminal steer=%#v", r)
	}
	once.Do(func() { close(releaseWorker) })
	for _, r := range []Submission{first, second, third} {
		if s := waitSubmission(t, a, r.ID); s.Status != "done" {
			t.Fatalf("job=%#v", s)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(starts, ",") != "first,second,third" {
		t.Fatalf("order=%v", starts)
	}
	if a.SubmissionState().Busy {
		t.Fatal("busy after commit")
	}
}

func TestSubmissionAlsoHasToolsWorkersAndSeparateIdentity(t *testing.T) {
	a := submissionTestApp(t)
	primaryStarted := make(chan struct{})
	releasePrimary := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(releasePrimary) })
	a.modelFactory = func(_ Config, worker bool) (tcagent.Model, error) {
		calls := 0
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
			scope := scopeFrom(ctx)
			if worker {
				if scope.lane != "also" {
					t.Error("wrong worker lane")
				}
				return lc.AI("also worker result"), nil
			}
			if scope.lane == "primary" {
				close(primaryStarted)
				<-releasePrimary
				return lc.AI("primary done"), nil
			}
			calls++
			if calls == 1 {
				names := map[string]bool{}
				for _, tool := range tools {
					names[tool.Definition().Name] = true
				}
				if !names["spawn_subagent"] || !names["workspace_info"] {
					t.Error("Also runtime lacks full tools")
				}
				return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "worker", Name: "spawn_subagent", Args: map[string]any{"name": "also worker", "task": "independent task"}}, {ID: "workspace", Name: "workspace_info"}}}, nil
			}
			return lc.AI("independent answer"), nil
		}), nil
	}
	primary := a.SubmitRouted(context.Background(), "primary question", "send")
	awaitSignal(t, primaryStarted)
	also := a.SubmitRouted(context.Background(), "also question", "also")
	if result := waitSubmission(t, a, also.ID); result.Status != "done" || result.Reply.Message != "independent answer" {
		t.Fatalf("also=%#v", result)
	}
	if s := a.SubmissionState(); !s.Busy || s.PrimaryID != primary.ID {
		t.Fatalf("Also stole primary state: %#v", s)
	}
	for _, msg := range a.State().History {
		if strings.Contains(msg.Content, "independent") || strings.Contains(msg.Content, "also question") {
			t.Fatalf("Also polluted primary history: %#v", msg)
		}
	}
	foundWorker := false
	foundTool := false
	for _, task := range a.TaskSnapshots() {
		if task.SubmissionID == also.ID {
			if task.Lane != "also" {
				t.Error("wrong task lane")
			}
			if task.Role == "subagent" {
				foundWorker = true
				if task.ParentTaskID == "" {
					t.Error("missing worker parent")
				}
			}
		}
	}
	a.mu.Lock()
	for _, record := range a.activity {
		if record.SubmissionID == also.ID && record.Status == "tool complete" {
			foundTool = true
			if record.Lane != "also" || record.TaskID == "" || record.AgentID == "" {
				t.Errorf("unscoped event: %#v", record)
			}
		}
	}
	a.mu.Unlock()
	if !foundWorker || !foundTool {
		t.Fatalf("worker=%v tool=%v", foundWorker, foundTool)
	}
	once.Do(func() { close(releasePrimary) })
	waitSubmission(t, a, primary.ID)
}

func TestSubmissionSteerOnceAndErrorState(t *testing.T) {
	a := submissionTestApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	a.modelFactory = func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
			calls++
			if calls == 1 {
				close(started)
				<-release
				return lc.AI("old"), nil
			}
			if lcContentText(m[len(m)-1].Content) != "new instruction" {
				t.Error("steer missing")
			}
			return lc.AI("steered"), nil
		}), nil
	}
	first := a.SubmitRouted(context.Background(), "start", "send")
	awaitSignal(t, started)
	steer := a.SubmitRouted(context.Background(), "new instruction", "steer")
	if steer.Status != "accepted" {
		t.Fatalf("steer=%#v", steer)
	}
	close(release)
	waitSubmission(t, a, first.ID)
	count := 0
	for _, m := range a.State().History {
		if m.Content == "new instruction" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("history deliveries=%d", count)
	}
	a.modelFactory = func(Config, bool) (tcagent.Model, error) { return nil, fmt.Errorf("offline failure") }
	bad := a.SubmitRouted(context.Background(), "fail", "send")
	if s := waitSubmission(t, a, bad.ID); s.Status != "error" || !strings.Contains(s.Error, "offline failure") {
		t.Fatalf("state=%#v", s)
	}
}

func TestSubmissionCancelTerminalTaskIsNoop(t *testing.T) {
	a := submissionTestApp(t)
	id := a.startTask("finished", "primary", "", func() { t.Error("canceled completed task") })
	a.finishTask(id, "done", nil)
	if a.CancelTask(id) {
		t.Fatal("terminal cancel accepted")
	}
	task, _ := a.TaskDetails(id)
	if task.Status != TaskDone {
		t.Fatalf("status=%s", task.Status)
	}
}
