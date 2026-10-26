package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

type Submission struct {
	RequestID string `json:"request_id,omitempty"`
	ID        string `json:"id"`
	Mode      string `json:"mode"`
	Input     string `json:"input,omitempty"`
	Status    string `json:"status"`
	Reply     *Reply `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
}
type SubmissionSnapshot struct {
	Paused bool `json:"paused"`
	// RunPaused is set while running agents are held at their next step.
	RunPaused bool `json:"run_paused,omitempty"`
	// Stopping is set while the running request wraps up after Stop.
	Stopping  bool         `json:"stopping,omitempty"`
	Busy      bool         `json:"busy"`
	PrimaryID string       `json:"primary_id,omitempty"`
	Queue     []Submission `json:"queue"`
	Jobs      []Submission `json:"jobs"`
}
type submissionJob struct {
	Submission
	ctx   context.Context
	sink  func(ActivityRecord)
	done  chan struct{}
	scope *invocationScope
	// full holds the complete reply (history, config, activity) only for a
	// synchronous caller waiting on this job; it is released once taken.
	wantFull bool
	full     *Reply
}

const (
	retainedSubmissions = 32
	retainedReceipts    = 256
)

// slimReply keeps what UIs need to report a finished job. History and config
// have one owner (the App) and are never copied into retained jobs.
func slimReply(reply Reply) *Reply {
	slim := Reply{Message: reply.Message, Command: reply.Command, OpenPanel: reply.OpenPanel}
	if len(reply.Data) > 0 {
		slim.Data = map[string]any{}
		for k, v := range reply.Data {
			switch k {
			case "submissions", "history", "tasks", "schedules", "runtime", "activity":
				continue
			}
			slim.Data[k] = v
		}
	}
	return &slim
}

// takeFullReply returns the waiter's complete reply, falling back to the slim
// reply plus current state.
func (a *App) takeFullReply(job *submissionJob) Reply {
	a.mu.Lock()
	full := job.full
	job.full = nil
	slim := job.Reply
	a.mu.Unlock()
	if full != nil {
		return *full
	}
	reply := a.State()
	if slim != nil {
		reply.Message, reply.Command, reply.OpenPanel = slim.Message, slim.Command, slim.OpenPanel
	}
	return reply
}

type invocationScope struct {
	id, lane, taskID string
	inbox            *tcagent.InstructionInbox
	public           map[string]*publicOutput // protected by App.mu
	steerMu          sync.Mutex
	instructions     []tcagent.Instruction
	workerInboxes    map[string]*tcagent.InstructionInbox
	workers          sync.WaitGroup
	err              error // root invocation only; read after its return
}
type invocationKey struct{}

func scopeFrom(ctx context.Context) *invocationScope {
	scope, _ := ctx.Value(invocationKey{}).(*invocationScope)
	return scope
}

// SubmitRouted accepts a submission without blocking the UI. The context and its
// selected skills belong to this submission, including while it waits in FIFO.
func (a *App) SubmitRouted(ctx context.Context, input, mode string) Submission {
	return a.SubmitRoutedRequest(ctx, input, mode, "", "")
}

// SubmitRoutedRequest deduplicates request IDs, including rejected requests, and
// atomically validates a steer target against the active job at acceptance.
func (a *App) SubmitRoutedRequest(ctx context.Context, input, mode, requestID, targetJobID string) Submission {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	if requestID != "" {
		if receipt, ok := a.requestReceipts[requestID]; ok {
			return receipt
		}
	}
	var receipt Submission
	if mode == "steer" {
		receipt = a.steerPrimary(ctx, input, targetJobID)
	} else {
		_, receipt = a.enqueueSubmission(ctx, input, mode, nil, requestID)
	}
	receipt.RequestID = requestID
	if requestID != "" {
		if a.requestReceipts == nil {
			a.requestReceipts = map[string]Submission{}
		}
		a.requestReceipts[requestID] = receipt
		a.receiptOrder = append(a.receiptOrder, requestID)
		if over := len(a.receiptOrder) - retainedReceipts; over > 0 {
			for _, old := range a.receiptOrder[:over] {
				delete(a.requestReceipts, old)
			}
			a.receiptOrder = append([]string(nil), a.receiptOrder[over:]...)
		}
	}
	return receipt
}

func (a *App) enqueueSubmission(ctx context.Context, input, mode string, sink func(ActivityRecord), requestIDs ...string) (*submissionJob, Submission) {
	return a.enqueueSubmissionWith(ctx, input, mode, sink, false, requestIDs...)
}

func (a *App) enqueueSubmissionWith(ctx context.Context, input, mode string, sink func(ActivityRecord), wantFull bool, requestIDs ...string) (*submissionJob, Submission) {
	if mode == "" {
		mode = "send"
	}
	reject := func(message string) (*submissionJob, Submission) {
		return nil, Submission{Mode: mode, Status: "rejected", Error: message}
	}
	if mode != "send" && mode != "queue" && mode != "also" {
		return reject("Unknown submission mode")
	}
	if strings.TrimSpace(input) == "" {
		return reject("Message is empty")
	}
	ctx, input, err := withSkillInvocation(ctx, a.Config(), input)
	if err != nil {
		return reject(err.Error())
	}
	if strings.HasPrefix(strings.TrimSpace(input), "/") {
		return reject("Use the command API for slash commands")
	}
	if err := ctx.Err(); err != nil {
		return reject(err.Error())
	}
	a.mu.Lock()
	if mode == "send" && (a.primaryJob != nil || a.paused || len(a.submissionQueue) > 0) {
		a.mu.Unlock()
		return reject("Primary is busy; choose Queue, Steer, or Also")
	}
	a.submissionSeq++
	id := fmt.Sprintf("submission-%x-%06d", time.Now().UnixNano(), a.submissionSeq)
	lane := "primary"
	if mode == "also" {
		lane = "also"
	}
	scope := &invocationScope{id: id, lane: lane}
	scope.inbox = &tcagent.InstructionInbox{OnDelivery: func(in tcagent.Instruction, applied bool) {
		status := "instruction applied"
		if !applied {
			status = "instruction rejected"
		}
		if applied && lane == "primary" {
			message := Message{Role: "user", Content: in.Text, Time: time.Now().UTC(), SubmissionID: id, Lane: lane}
			a.mu.Lock()
			a.history = append(a.history, message)
			a.mu.Unlock()
			a.persistMessage(message)
		}
		a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "instruction", Status: status, Detail: in.Text, RunID: in.ID, SubmissionID: id, Lane: lane})
	}}
	job := &submissionJob{Submission: Submission{ID: id, Mode: mode, Input: input, Status: "queued"}, ctx: context.WithValue(ctx, invocationKey{}, scope), sink: sink, done: make(chan struct{}), scope: scope, wantFull: wantFull}
	if len(requestIDs) > 0 {
		job.RequestID = requestIDs[0]
	}
	if a.submissions == nil {
		a.submissions = make(map[string]*submissionJob)
	}
	a.submissions[id] = job
	a.submissionOrder = append(a.submissionOrder, id)
	start := mode == "also" || (a.primaryJob == nil && !a.paused && len(a.submissionQueue) == 0)
	if start {
		job.Status = "running"
		if mode != "also" {
			a.primaryJob = job
		}
	} else {
		a.submissionQueue = append(a.submissionQueue, job)
	}
	receipt := job.Submission
	a.mu.Unlock()
	a.notifyChange(ChangeSubmissions)
	if start {
		go a.executeSubmission(job)
	} else if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				a.CancelQueuedSubmission(id)
			case <-job.done:
			}
		}()
	}
	return job, receipt
}

func (a *App) executeSubmission(job *submissionJob) {
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "submission", SubmissionID: job.ID, Lane: job.scope.lane, Status: "submission running"})
	var reply Reply
	if err := job.ctx.Err(); err != nil {
		job.scope.err = err
		reply = a.reply("Canceled before starting: "+err.Error(), "", "")
	} else if job.Mode == "also" {
		reply = a.runAlso(job.ctx, job.Input)
	} else {
		reply = a.submitDirect(job.ctx, job.Input, job.sink)
	}
	job.scope.inbox.Close()
	a.gate().clear(job.ID)
	if job.Mode == "also" {
		if reply.Data == nil {
			reply.Data = map[string]any{}
		}
		reply.Command = "/also"
		reply.Data["also"] = true
		reply.Data["answer"] = reply.Message
		reply.Data["submission_id"] = job.ID
	}
	// runAgent/runAlso join their workers before returning; submitDirect has now
	// appended AND persisted the final visible history before the next admission.
	a.mu.Lock()
	if reply.Data != nil {
		delete(reply.Data, "submissions")
	}
	job.Reply = slimReply(reply)
	if job.wantFull {
		full := reply
		job.full = &full
	}
	job.Status = "done"
	if job.scope.err != nil {
		job.Status = "error"
		job.Error = job.scope.err.Error()
	}
	if errors.Is(job.scope.err, context.Canceled) || errors.Is(job.scope.err, context.DeadlineExceeded) {
		job.Status = "canceled"
	}
	if job.ctx.Err() != nil {
		job.Status = "canceled"
		job.Error = job.ctx.Err().Error()
	}
	var next *submissionJob
	if a.primaryJob == job {
		a.primaryJob = nil
		if !a.paused && len(a.submissionQueue) > 0 {
			next = a.submissionQueue[0]
			a.submissionQueue = a.submissionQueue[1:]
			next.Status = "running"
			a.primaryJob = next
		}
	}
	job.ctx = nil
	job.sink = nil
	a.pruneSubmissionsLocked()
	close(job.done)
	a.mu.Unlock()
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "submission", SubmissionID: job.ID, Lane: job.scope.lane, Status: "submission " + job.Status})
	a.notifyChange(ChangeSubmissions)
	a.notifyChange(ChangeHistory)
	if next != nil {
		go a.executeSubmission(next)
	}
}

func (a *App) steerPrimary(ctx context.Context, text, targetID string) Submission {
	receipt := Submission{Mode: "steer", Status: "rejected"}
	if strings.TrimSpace(text) == "" {
		receipt.Error = "Instruction is empty"
		return receipt
	}
	if err := ctx.Err(); err != nil {
		receipt.Error = err.Error()
		return receipt
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		receipt.Error = "Use the command API for slash commands"
		return receipt
	}
	prepared := humanMessageWithAttachments(text, a.Config())
	if skills := selectedSkillsPrompt(ctx); skills != "" {
		prepared = appendInstructionSkills(prepared, skills)
	}
	a.mu.Lock()
	job := a.primaryJob
	if job == nil {
		a.mu.Unlock()
		receipt.Error = "No active primary invocation"
		return receipt
	}
	if a.gate().stopping(job.ID) {
		a.mu.Unlock()
		receipt.Error = "The current request is stopping"
		return receipt
	}
	if targetID != "" && targetID != job.ID {
		a.mu.Unlock()
		receipt.Error = "Primary target changed"
		return receipt
	}
	a.submissionSeq++
	receipt.ID = fmt.Sprintf("instruction-%06d", a.submissionSeq)
	a.mu.Unlock()
	if err := job.scope.broadcastInstruction(tcagent.Instruction{ID: receipt.ID, Text: text, Message: &prepared}); err != nil {
		receipt.Error = err.Error()
		return receipt
	}
	receipt.Status = "accepted"
	return receipt
}

func (a *App) SubmissionState() SubmissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.submissionStateLocked()
}
func (a *App) submissionStateLocked() SubmissionSnapshot {
	out := SubmissionSnapshot{Paused: a.paused, RunPaused: a.gate().isPaused(), Busy: a.primaryJob != nil || len(a.submissionQueue) > 0, Queue: []Submission{}, Jobs: []Submission{}}
	if a.primaryJob != nil {
		out.PrimaryID = a.primaryJob.ID
		out.Stopping = a.gate().stopping(a.primaryJob.ID)
	}
	for _, job := range a.submissionQueue {
		out.Queue = append(out.Queue, job.Submission)
	}
	for _, id := range a.submissionOrder {
		out.Jobs = append(out.Jobs, cloneSubmission(a.submissions[id].Submission))
	}
	return out
}

func (a *App) assignTaskScope(ctx context.Context, taskID string) {
	scope := scopeFrom(ctx)
	if scope == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if task := a.tasks[taskID]; task != nil {
		task.SubmissionID = scope.id
		task.Lane = scope.lane
		if taskID != scope.taskID {
			task.ParentTaskID = scope.taskID
		}
	}
}
func scopeActivity(ctx context.Context, record *ActivityRecord) {
	if scope := scopeFrom(ctx); scope != nil {
		record.SubmissionID = scope.id
		record.Lane = scope.lane
	}
}

// CancelQueuedSubmission accepts either a job ID or its client request ID.
func (a *App) CancelQueuedSubmission(id string) bool {
	a.mu.Lock()
	var removed *submissionJob
	for i, job := range a.submissionQueue {
		if job.ID == id || (job.RequestID != "" && job.RequestID == id) {
			removed = job
			a.submissionQueue = append(a.submissionQueue[:i], a.submissionQueue[i+1:]...)
			job.Status = "canceled"
			job.Error = "Removed from queue"
			job.Reply = &Reply{Message: "Removed from queue", Config: a.config}
			job.ctx = nil
			job.sink = nil
			close(job.done)
			break
		}
	}
	a.mu.Unlock()
	if removed != nil {
		removed.scope.inbox.Close()
		a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "submission", SubmissionID: removed.ID, Lane: "primary", Status: "submission canceled"})
		a.notifyChange(ChangeSubmissions)
	}
	return removed != nil
}

// TryReplaceHistory prevents session changes from crossing any live invocation.
func (a *App) TryReplaceHistory(messages []Message) bool {
	return a.tryReplaceHistory(nil, messages, false)
}

func (a *App) TryReplaceHistoryIfUnchanged(before, messages []Message) bool {
	return a.tryReplaceHistory(before, messages, true)
}

func (a *App) tryReplaceHistory(before, messages []Message, compare bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if compare && !reflect.DeepEqual(before, a.history) {
		return false
	}
	if a.primaryJob != nil || len(a.submissionQueue) > 0 {
		return false
	}
	for _, job := range a.submissions {
		if job.Status == "running" {
			return false
		}
	}
	a.history = append([]Message{}, messages...)
	go a.notifyChange(ChangeHistory)
	return true
}

func appendInstructionSkills(message lc.BaseMessage, skills string) lc.BaseMessage {
	if len(message.Content.Parts) == 0 {
		message.Content = lc.TextContent(lcContentText(message.Content) + skills)
	} else {
		extra := lc.Human(skills)
		message.Content.Parts = append(message.Content.Parts, lc.ContentPart{Type: "text", Text: lcContentText(extra.Content)})
	}
	return message
}

func taskOwnedBy(ctx context.Context, task AgentTask) bool {
	scope := scopeFrom(ctx)
	return scope == nil || task.SubmissionID == scope.id
}
func (a *App) ownedTaskDetails(ctx context.Context, id string) (AgentTask, bool) {
	task, ok := a.TaskDetails(id)
	return task, ok && task.Role == "subagent" && taskOwnedBy(ctx, task)
}

// Public snapshots own their reply maps/slices. Execution contexts are never exposed.
func cloneSubmission(in Submission) Submission {
	if in.Reply == nil {
		return in
	}
	data, err := json.Marshal(in.Reply)
	if err != nil {
		copy := *in.Reply
		copy.Data = nil
		in.Reply = &copy
		return in
	}
	var reply Reply
	if json.Unmarshal(data, &reply) == nil {
		in.Reply = &reply
	}
	return in
}
func (a *App) pruneSubmissionsLocked() {
	const retained = retainedSubmissions
	completed := 0
	for _, id := range a.submissionOrder {
		s := a.submissions[id].Status
		if s != "running" && s != "queued" {
			completed++
		}
	}
	order := a.submissionOrder[:0]
	for _, id := range a.submissionOrder {
		job := a.submissions[id]
		if completed > retained && job.Status != "running" && job.Status != "queued" {
			delete(a.submissions, id)
			completed--
			continue
		}
		order = append(order, id)
	}
	a.submissionOrder = order
}
