package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"tinychain/callbacks"
	"tinychain/lc"
)

type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskDone      TaskStatus = "done"
	TaskError     TaskStatus = "error"
	TaskCanceled  TaskStatus = "canceled"
	TaskCanceling TaskStatus = "canceling"
)

type AgentTask struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Role       string           `json:"role"`
	Prompt     string           `json:"prompt,omitempty"`
	Status     TaskStatus       `json:"status"`
	Current    string           `json:"current,omitempty"`
	Result     string           `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	FinishedAt time.Time        `json:"finished_at,omitempty"`
	Activity   []ActivityRecord `json:"activity,omitempty"`
	ToolCalls  []TaskToolCall   `json:"tool_calls,omitempty"`
	Tokens     TaskTokens       `json:"tokens,omitempty"`
	Cancelable bool             `json:"cancelable"`
	CancelHint string           `json:"cancel_hint,omitempty"`
}

type TaskToolCall struct {
	Time   time.Time `json:"time"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Detail string    `json:"detail,omitempty"`
}

type TaskTokens struct {
	Input              int `json:"input,omitempty"`
	Output             int `json:"output,omitempty"`
	Total              int `json:"total,omitempty"`
	CachedInput        int `json:"cached_input,omitempty"`
	CacheCreationInput int `json:"cache_creation_input,omitempty"`
	ReasoningOutput    int `json:"reasoning_output,omitempty"`
}

type ThoughtSnapshot struct {
	TaskID    string    `json:"task_id"`
	Agent     string    `json:"agent"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Current   string    `json:"current,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Notes     []string  `json:"notes,omitempty"`
}

func (a *App) startTask(name, role, prompt string, cancel func()) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureTasksLocked()
	a.taskSeq++
	id := fmt.Sprintf("task-%04d", a.taskSeq)
	now := time.Now().UTC()
	a.tasks[id] = &AgentTask{
		ID:         id,
		Name:       strings.TrimSpace(name),
		Role:       strings.TrimSpace(role),
		Prompt:     strings.TrimSpace(prompt),
		Status:     TaskRunning,
		Current:    "starting",
		StartedAt:  now,
		UpdatedAt:  now,
		Cancelable: cancel != nil,
		CancelHint: "Press c in /tasks or use /tasks cancel " + id,
	}
	if a.tasks[id].Name == "" {
		a.tasks[id].Name = id
	}
	if a.tasks[id].Role == "" {
		a.tasks[id].Role = "agent"
	}
	if cancel != nil {
		a.taskCancels[id] = cancel
	}
	return id
}

func (a *App) finishTask(id, result string, err error) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureTasksLocked()
	task, ok := a.tasks[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	task.UpdatedAt = now
	task.FinishedAt = now
	task.Cancelable = false
	delete(a.taskCancels, id)
	if err != nil {
		if task.Status == TaskCanceling {
			task.Status = TaskCanceled
			task.Current = "canceled"
		} else {
			task.Status = TaskError
			task.Current = "error"
		}
		task.Error = err.Error()
		return
	}
	if task.Status == TaskCanceling {
		task.Status = TaskCanceled
		task.Current = "canceled"
		return
	}
	task.Status = TaskDone
	task.Current = "done"
	task.Result = truncate(result, 2000)
}

func (a *App) CancelTask(id string) bool {
	a.mu.Lock()
	a.ensureTasksLocked()
	cancel := a.taskCancels[id]
	if task := a.tasks[id]; task != nil {
		task.Status = TaskCanceling
		task.Current = "cancel requested"
		task.UpdatedAt = time.Now().UTC()
	}
	a.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	a.logInfo("task cancel requested", "task", id)
	return true
}

func (a *App) TaskSnapshots() []AgentTask {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taskSnapshotsLocked()
}

func (a *App) TaskDetails(id string) (AgentTask, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	task, ok := a.tasks[id]
	if !ok {
		return AgentTask{}, false
	}
	return cloneTask(*task), true
}

func (a *App) taskSnapshotsLocked() []AgentTask {
	a.ensureTasksLocked()
	tasks := make([]AgentTask, 0, len(a.tasks))
	for _, task := range a.tasks {
		tasks = append(tasks, cloneTask(*task))
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Status == TaskRunning && tasks[j].Status != TaskRunning {
			return true
		}
		if tasks[i].Status != TaskRunning && tasks[j].Status == TaskRunning {
			return false
		}
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	if len(tasks) > 80 {
		tasks = tasks[:80]
	}
	return tasks
}

func cloneTask(task AgentTask) AgentTask {
	task.Activity = append([]ActivityRecord{}, task.Activity...)
	task.ToolCalls = append([]TaskToolCall{}, task.ToolCalls...)
	return task
}

func (a *App) recordTaskActivity(id string, record ActivityRecord) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recordTaskActivityLocked(id, record)
}

func (a *App) recordTaskActivityLocked(id string, record ActivityRecord) {
	a.ensureTasksLocked()
	task := a.tasks[id]
	if task == nil {
		return
	}
	if record.Time.IsZero() {
		record.Time = time.Now().UTC()
	}
	task.Activity = append(task.Activity, record)
	if len(task.Activity) > 120 {
		task.Activity = task.Activity[len(task.Activity)-120:]
	}
	task.Current = strings.TrimSpace(record.Status)
	if record.Detail != "" {
		task.Current += ": " + truncate(record.Detail, 160)
	}
	task.UpdatedAt = record.Time
	if strings.Contains(record.Status, "tool") {
		task.ToolCalls = append(task.ToolCalls, TaskToolCall{
			Time:   record.Time,
			Name:   record.Name,
			Status: record.Status,
			Detail: record.Detail,
		})
		if len(task.ToolCalls) > 80 {
			task.ToolCalls = task.ToolCalls[len(task.ToolCalls)-80:]
		}
	}
}

func (a *App) recordTaskCallback(id string, event callbacks.Event) {
	record := activityRecordFromCallback(event)
	a.recordTaskActivity(id, record)
	if event.Event == callbacks.EventLLMEnd {
		a.addTaskUsage(id, usageFromLLMEnd(event))
	}
}

func (a *App) addTaskUsage(id string, usage TaskTokens) {
	if id == "" || (usage.Input == 0 && usage.Output == 0 && usage.Total == 0) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	task := a.tasks[id]
	if task == nil {
		return
	}
	task.Tokens.Input += usage.Input
	task.Tokens.Output += usage.Output
	task.Tokens.Total += usage.Total
	task.Tokens.CachedInput += usage.CachedInput
	task.Tokens.CacheCreationInput += usage.CacheCreationInput
	task.Tokens.ReasoningOutput += usage.ReasoningOutput
	if task.Tokens.Total == 0 {
		task.Tokens.Total = task.Tokens.Input + task.Tokens.Output
	}
	task.UpdatedAt = time.Now().UTC()
}

func usageFromLLMEnd(event callbacks.Event) TaskTokens {
	if event.Data.Response == nil {
		return TaskTokens{}
	}
	var usage TaskTokens
	for _, batch := range event.Data.Response.Generations {
		for _, generation := range batch {
			meta := generation.Message.UsageMetadata
			if meta == nil {
				continue
			}
			usage.Input += meta.InputTokens
			usage.Output += meta.OutputTokens
			usage.Total += meta.TotalTokens
			usage.CachedInput += meta.InputTokenDetails["cached_tokens"]
			usage.CachedInput += meta.InputTokenDetails["cache_read_input_tokens"]
			usage.CacheCreationInput += meta.InputTokenDetails["cache_creation_input_tokens"]
			usage.ReasoningOutput += meta.OutputTokenDetails["reasoning_tokens"]
		}
	}
	if usage.Total == 0 {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func (a *App) ThoughtSnapshots() []ThoughtSnapshot {
	tasks := a.TaskSnapshots()
	out := make([]ThoughtSnapshot, 0, len(tasks))
	for _, task := range tasks {
		snap := ThoughtSnapshot{
			TaskID:    task.ID,
			Agent:     task.Name,
			Role:      task.Role,
			Status:    string(task.Status),
			Current:   task.Current,
			Prompt:    task.Prompt,
			UpdatedAt: task.UpdatedAt,
		}
		start := maxInt(0, len(task.Activity)-10)
		for _, record := range task.Activity[start:] {
			switch record.Status {
			case "model start":
				snap.Notes = append(snap.Notes, "started model call: "+record.Detail)
			case "reasoning":
				snap.Notes = append(snap.Notes, "reasoning: "+record.Detail)
			case "tool start":
				snap.Notes = append(snap.Notes, "using tool "+record.Name+" "+record.Detail)
			case "tool complete":
				snap.Notes = append(snap.Notes, "tool complete: "+record.Name)
			case "tool error", "model error":
				snap.Notes = append(snap.Notes, record.Status+": "+record.Detail)
			case "agent complete":
				snap.Notes = append(snap.Notes, "completed current turn")
			}
		}
		if len(snap.Notes) == 0 && snap.Current != "" {
			snap.Notes = append(snap.Notes, snap.Current)
		}
		out = append(out, snap)
	}
	return out
}

func taskSummary(tasks []AgentTask) string {
	if len(tasks) == 0 {
		return "No tasks recorded yet."
	}
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "- `%s` [%s/%s] %s - %s\n", task.ID, task.Role, task.Status, task.Name, truncate(task.Current, 120))
	}
	return strings.TrimSpace(b.String())
}

func (a *App) runningSubagentCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, task := range a.tasks {
		if task.Role == "subagent" && (task.Status == TaskRunning || task.Status == TaskCanceling) {
			count++
		}
	}
	return count
}

func (a *App) ensureTasksLocked() {
	if a.tasks == nil {
		a.tasks = map[string]*AgentTask{}
	}
	if a.taskCancels == nil {
		a.taskCancels = map[string]context.CancelFunc{}
	}
}

func activityRecordFromCallback(event callbacks.Event) ActivityRecord {
	record := ActivityRecord{
		Time: event.Time,
		Kind: string(event.Event),
		Name: event.Name,
	}
	if record.Time.IsZero() {
		record.Time = time.Now().UTC()
	}
	switch event.Event {
	case callbacks.EventChatModelStart:
		record.Status = "model start"
		record.Detail = fmt.Sprintf("messages=%d", callbackMessageCount(event))
	case callbacks.EventLLMReasoning:
		record.Status = "reasoning"
		record.Detail = event.Data.Token
		if record.Detail == "" && event.Data.Chunk != nil {
			record.Detail = event.Data.Chunk.Text
		}
	case callbacks.EventLLMEnd:
		record.Status = "agent complete"
		record.Detail = "Agent completed task."
	case callbacks.EventLLMError:
		record.Status = "model error"
		record.Detail = event.Data.Error
	case callbacks.EventToolStart:
		record.Status = "tool start"
		record.Detail = "args: " + compactAny(event.Data.Input, 180)
	case callbacks.EventToolEnd:
		record.Status = "tool complete"
		record.Detail = "Tool call complete."
	case callbacks.EventToolError:
		record.Status = "tool error"
		record.Detail = "error: " + event.Data.Error
	case callbacks.EventChainStart:
		if event.Name == "context_compaction" {
			record.Status = "context compact start"
			record.Detail = compactAny(event.Data.Extra, 220)
		} else {
			record.Status = "chain start"
		}
	case callbacks.EventChainEnd:
		if event.Name == "context_compaction" {
			record.Status = "context compact complete"
			record.Detail = compactAny(event.Data.Extra, 220)
		} else {
			record.Status = "chain complete"
		}
	case callbacks.EventChainError:
		record.Status = "context compact error"
		record.Detail = "error: " + event.Data.Error
	default:
		record.Status = string(event.Event)
	}
	return record
}

func lcMessagesWithTask(task string) []lc.BaseMessage {
	return []lc.BaseMessage{lc.Human(task)}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
