package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
)

// runControl is the step gate every agent consults before each model call
// and tool call: the manager, its subagents, plans and side questions.
//
//   - Pause holds every agent at its next step. What is already running (a
//     model reply, a tool call) finishes first; nothing new starts until
//     Resume, and the same run carries on from where it was.
//   - Stop asks one request to wrap up: its agents make no more tool calls
//     and the manager ends with a short account of what it did and what is
//     left. Queued requests then continue.
//   - Stop everything cancels all running work at once, including tool calls
//     in flight, and holds the queue until Resume.
type runControl struct {
	mu      sync.Mutex
	paused  bool
	stop    map[string]bool // submission ids asked to wrap up
	changed chan struct{}
}

func newRunControl() *runControl {
	return &runControl{stop: map[string]bool{}, changed: make(chan struct{})}
}

func (c *runControl) update(f func()) {
	c.mu.Lock()
	f()
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

// Wait implements tcagent.StepGate.
func (c *runControl) Wait(ctx context.Context) error {
	id := ""
	if scope := scopeFrom(ctx); scope != nil {
		id = scope.id
	}
	for {
		c.mu.Lock()
		stop, paused, changed := id != "" && c.stop[id], c.paused, c.changed
		c.mu.Unlock()
		if stop {
			return tcagent.ErrWrapUp
		}
		if !paused {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// watch returns a channel closed at the next pause, resume or stop.
func (c *runControl) watch() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

func (c *runControl) isPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

func (c *runControl) stopping(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stop[id]
}

func (c *runControl) clear(id string) {
	c.mu.Lock()
	_, ok := c.stop[id]
	c.mu.Unlock()
	if ok {
		c.update(func() { delete(c.stop, id) })
	}
}

// gate returns the app's run control, creating it on first use.
func (a *App) gate() *runControl {
	a.controlOnce.Do(func() { a.control = newRunControl() })
	return a.control
}

var errNothingRunning = errors.New("nothing is running")

// PauseRuns holds every agent at its next step and holds queued requests.
// Steps already under way finish first.
func (a *App) PauseRuns() {
	a.gate().update(func() { a.control.paused = true })
	a.mu.Lock()
	a.paused = true
	id := ""
	if a.primaryJob != nil {
		id = a.primaryJob.ID
	}
	a.mu.Unlock()
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "runtime", Name: "runtime", SubmissionID: id, Status: "runs paused", Detail: "Steps already under way finish; nothing new starts until you resume."})
	a.logInfo("runs paused")
	a.notifyChange(ChangeSubmissions)
}

// ResumeRuns lets paused agents carry on and starts the next queued
// request if nothing is running.
func (a *App) ResumeRuns() {
	wasPaused := a.gate().isPaused()
	a.gate().update(func() { a.control.paused = false })
	a.mu.Lock()
	id := ""
	if a.primaryJob != nil {
		id = a.primaryJob.ID
	}
	a.mu.Unlock()
	a.mu.Lock()
	a.halted = false
	a.mu.Unlock()
	a.setPaused(false)
	if wasPaused {
		a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "runtime", Name: "runtime", SubmissionID: id, Status: "runs resumed", Detail: "Carrying on."})
	}
	a.logInfo("runs resumed")
	a.notifyChange(ChangeSubmissions)
}

// StopCurrent asks the running request to wrap up: no more tool calls, and
// a short account of what was done. Its subagents wrap up too.
func (a *App) StopCurrent() error {
	return a.StopRequest("")
}

// StopRequest is StopCurrent for one request: when targetID is set and a
// different request is running by the time this arrives, nothing stops.
func (a *App) StopRequest(targetID string) error {
	a.mu.Lock()
	job := a.primaryJob
	a.mu.Unlock()
	if job == nil {
		return errNothingRunning
	}
	if targetID != "" && targetID != job.ID {
		return errors.New("that request already finished")
	}
	a.gate().update(func() { a.control.stop[job.ID] = true })
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "runtime", Name: "runtime", SubmissionID: job.ID, Status: "stop requested", Detail: "Finishing the current step, then wrapping up."})
	a.logInfo("stop requested", "submission", job.ID)
	a.notifyChange(ChangeSubmissions)
	return nil
}

// StopEverything cancels all running work now, tool calls included, and
// holds queued requests until Resume.
func (a *App) StopEverything() {
	a.mu.Lock()
	a.ensureTasksLocked()
	var cancels []context.CancelFunc
	for id, task := range a.tasks {
		if task.Status != TaskRunning && task.Status != TaskCanceling {
			continue
		}
		if cancel := a.taskCancels[id]; cancel != nil {
			task.Status = TaskCanceling
			task.Current = "stopping"
			task.UpdatedAt = time.Now().UTC()
			cancels = append(cancels, cancel)
		}
	}
	id := ""
	if a.primaryJob != nil {
		id = a.primaryJob.ID
	}
	a.mu.Unlock()
	a.mu.Lock()
	a.halted = true // a run that starts in the meantime is cancelled too
	a.mu.Unlock()
	a.setPaused(true) // holds the queue and cancels the manager's run
	for _, cancel := range cancels {
		cancel()
	}
	a.gate().update(func() { a.control.paused = false })
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "runtime", Name: "runtime", SubmissionID: id, Status: "stopped everything", Detail: "Stopped all running work. Queued requests wait until you resume."})
	a.logInfo("stopped everything", "tasks", len(cancels))
	a.releaseIdlePause()
	a.notifyChange(ChangeTasks)
	a.notifyChange(ChangeSubmissions)
}

// releaseIdlePause ends a pause or stop that has nothing left to hold: no
// request running or waiting and no agent at work. Without this, a pause
// that lands as the last answer is written would outlive the run, and the
// next message would be refused.
func (a *App) releaseIdlePause() {
	a.mu.Lock()
	idle := a.primaryJob == nil && len(a.submissionQueue) == 0
	for _, job := range a.submissions {
		if job.Status == "running" {
			idle = false
		}
	}
	a.ensureTasksLocked()
	for _, task := range a.tasks {
		if task.Status == TaskRunning || task.Status == TaskCanceling {
			idle = false
		}
	}
	held := a.paused
	if idle {
		a.paused = false
		a.halted = false
	}
	a.mu.Unlock()
	if !idle || (!held && !a.gate().isPaused()) {
		return
	}
	a.gate().update(func() { a.control.paused = false })
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "runtime", Name: "runtime", Status: "pause ended", Detail: "Nothing was left to pause."})
	a.notifyChange(ChangeSubmissions)
}

// SteerTask sends guidance to one running subagent only. Guidance sent to
// the request with Steer reaches the manager and all of its subagents.
func (a *App) SteerTask(taskID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("guidance is empty")
	}
	a.mu.Lock()
	var scopes []*invocationScope
	for _, job := range a.submissions {
		if job.Status == "running" && job.scope != nil {
			scopes = append(scopes, job.scope)
		}
	}
	a.submissionSeq++
	id := fmt.Sprintf("instruction-%06d", a.submissionSeq)
	a.mu.Unlock()
	for _, scope := range scopes {
		scope.steerMu.Lock()
		inbox := scope.workerInboxes[taskID]
		scope.steerMu.Unlock()
		if inbox == nil {
			continue
		}
		if a.gate().stopping(scope.id) {
			return errors.New("this request is stopping")
		}
		message := humanMessageWithAttachments(text, a.Config())
		return inbox.Submit(tcagent.Instruction{ID: id, Text: text, Message: &message})
	}
	return errors.New("that subagent isn't running")
}
