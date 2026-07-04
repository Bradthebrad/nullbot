package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ScheduledTaskStatus string

const (
	ScheduleActive   ScheduledTaskStatus = "active"
	ScheduleRunning  ScheduledTaskStatus = "running"
	ScheduleDone     ScheduledTaskStatus = "done"
	ScheduleCanceled ScheduledTaskStatus = "canceled"
	ScheduleError    ScheduledTaskStatus = "error"
)

type ScheduledTask struct {
	ID          string              `json:"id"`
	Name        string              `json:"name,omitempty"`
	Prompt      string              `json:"prompt"`
	Status      ScheduledTaskStatus `json:"status"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	NextRunAt   time.Time           `json:"next_run_at,omitempty"`
	LastRunAt   time.Time           `json:"last_run_at,omitempty"`
	FinishedAt  time.Time           `json:"finished_at,omitempty"`
	RepeatEvery string              `json:"repeat_every,omitempty"`
	RunCount    int                 `json:"run_count"`
	LastError   string              `json:"last_error,omitempty"`
}

type ScheduleRequest struct {
	Mode   string `json:"mode"`
	At     string `json:"at,omitempty"`
	Every  string `json:"every,omitempty"`
	Delay  string `json:"delay,omitempty"`
	Name   string `json:"name,omitempty"`
	Prompt string `json:"prompt"`
}

func (a *App) StartScheduler() {
	a.mu.Lock()
	if a.schedulerCancel != nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.schedulerCancel = cancel
	a.mu.Unlock()

	go a.schedulerLoop(ctx)
	a.appendActivity(ActivityRecord{
		Time:   time.Now().UTC(),
		Kind:   "schedule",
		Name:   "scheduler",
		Status: "scheduler started",
	})
}

func (a *App) StopScheduler() {
	a.mu.Lock()
	cancel := a.schedulerCancel
	a.schedulerCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) ScheduledTasks() []ScheduledTask {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scheduledSnapshotsLocked()
}

func (a *App) CreateScheduledTask(req ScheduleRequest) (ScheduledTask, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return ScheduledTask{}, fmt.Errorf("schedule prompt is required")
	}
	nextRun, repeat, err := parseScheduleRequest(req)
	if err != nil {
		return ScheduledTask{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureSchedulesLocked()
	a.scheduleSeq++
	now := time.Now().UTC()
	task := &ScheduledTask{
		ID:          fmt.Sprintf("schedule-%04d", a.scheduleSeq),
		Name:        scheduleName(req.Name, req.Prompt),
		Prompt:      req.Prompt,
		Status:      ScheduleActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		NextRunAt:   nextRun.UTC(),
		RepeatEvery: repeatString(repeat),
	}
	a.schedules[task.ID] = task
	if err := a.persistSchedulesLocked(); err != nil {
		delete(a.schedules, task.ID)
		return ScheduledTask{}, err
	}
	return cloneScheduledTask(*task), nil
}

func (a *App) CancelScheduledTask(id string) (ScheduledTask, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureSchedulesLocked()
	task := a.schedules[strings.TrimSpace(id)]
	if task == nil {
		return ScheduledTask{}, false
	}
	now := time.Now().UTC()
	task.Status = ScheduleCanceled
	task.UpdatedAt = now
	task.FinishedAt = now
	_ = a.persistSchedulesLocked()
	return cloneScheduledTask(*task), true
}

func (a *App) DeleteScheduledTask(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureSchedulesLocked()
	if a.schedules[strings.TrimSpace(id)] == nil {
		return false
	}
	delete(a.schedules, strings.TrimSpace(id))
	_ = a.persistSchedulesLocked()
	return true
}

func (a *App) RunScheduledTaskNow(id string) (ScheduledTask, error) {
	a.mu.Lock()
	a.ensureSchedulesLocked()
	task := a.schedules[strings.TrimSpace(id)]
	if task == nil {
		a.mu.Unlock()
		return ScheduledTask{}, fmt.Errorf("schedule not found: %s", id)
	}
	task.Status = ScheduleRunning
	task.UpdatedAt = time.Now().UTC()
	snapshot := cloneScheduledTask(*task)
	_ = a.persistSchedulesLocked()
	a.mu.Unlock()

	a.dispatchScheduledTask(snapshot.ID, snapshot.Prompt, true)
	return snapshot, nil
}

func (a *App) schedulerLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		a.runDueSchedules()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runDueSchedules() {
	now := time.Now().UTC()
	var due []ScheduledTask

	a.mu.Lock()
	a.ensureSchedulesLocked()
	for _, task := range a.schedules {
		if task.Status != ScheduleActive || task.NextRunAt.IsZero() || task.NextRunAt.After(now) {
			continue
		}
		task.Status = ScheduleRunning
		task.UpdatedAt = now
		due = append(due, cloneScheduledTask(*task))
	}
	if len(due) > 0 {
		_ = a.persistSchedulesLocked()
	}
	a.mu.Unlock()

	for _, task := range due {
		a.dispatchScheduledTask(task.ID, task.Prompt, false)
	}
}

func (a *App) dispatchScheduledTask(id, prompt string, manual bool) {
	go func() {
		now := time.Now().UTC()
		a.mu.Lock()
		task := a.schedules[id]
		if task != nil {
			task.LastRunAt = now
			task.RunCount++
			task.UpdatedAt = now
			_ = a.persistSchedulesLocked()
		}
		a.mu.Unlock()

		a.appendActivity(ActivityRecord{
			Time:   now,
			Kind:   "schedule",
			Name:   id,
			Status: "schedule started",
			Detail: truncate(prompt, 220),
		})
		reply := a.SubmitWithActivity(context.Background(), prompt, nil)

		status := "schedule complete"
		detail := ""
		a.mu.Lock()
		task = a.schedules[id]
		if task == nil || task.Status == ScheduleCanceled {
			a.mu.Unlock()
			return
		}
		finished := time.Now().UTC()
		task.UpdatedAt = finished
		task.FinishedAt = finished
		task.LastError = ""
		if strings.HasPrefix(reply.Message, "Agent error:") {
			task.LastError = reply.Message
			task.Status = ScheduleError
			status = "schedule error"
			detail = reply.Message
		} else if repeat, ok := scheduledRepeatDuration(task); ok && repeat > 0 {
			task.Status = ScheduleActive
			task.FinishedAt = time.Time{}
			task.NextRunAt = finished.Add(repeat).UTC()
		} else if manual && !task.NextRunAt.IsZero() && task.NextRunAt.After(finished) {
			task.Status = ScheduleActive
			task.FinishedAt = time.Time{}
		} else {
			task.Status = ScheduleDone
		}
		_ = a.persistSchedulesLocked()
		a.mu.Unlock()
		a.appendActivity(ActivityRecord{
			Time:   finished,
			Kind:   "schedule",
			Name:   id,
			Status: status,
			Detail: detail,
		})
	}()
}

func (a *App) scheduledSnapshotsLocked() []ScheduledTask {
	a.ensureSchedulesLocked()
	out := make([]ScheduledTask, 0, len(a.schedules))
	for _, task := range a.schedules {
		out = append(out, cloneScheduledTask(*task))
	}
	sort.Slice(out, func(i, j int) bool {
		leftRank := scheduleStatusRank(out[i].Status)
		rightRank := scheduleStatusRank(out[j].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !out[i].NextRunAt.Equal(out[j].NextRunAt) {
			if out[i].NextRunAt.IsZero() {
				return false
			}
			if out[j].NextRunAt.IsZero() {
				return true
			}
			return out[i].NextRunAt.Before(out[j].NextRunAt)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (a *App) ensureSchedulesLocked() {
	if a.schedules == nil {
		a.schedules = map[string]*ScheduledTask{}
	}
}

func (a *App) persistSchedulesLocked() error {
	if err := EnsureAppDir(a.config); err != nil {
		return err
	}
	tasks := a.scheduledSnapshotsLocked()
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(schedulePath(a.config), append(data, '\n'), 0600)
}

func loadScheduledTasks(config Config, logger *Logger) (map[string]*ScheduledTask, int) {
	tasks := map[string]*ScheduledTask{}
	data, err := os.ReadFile(schedulePath(config))
	if err != nil {
		if err != nil && !os.IsNotExist(err) && logger != nil {
			logger.Error("schedule load failed", "error", err)
		}
		return tasks, 0
	}
	var saved []ScheduledTask
	if err := json.Unmarshal(data, &saved); err != nil {
		if logger != nil {
			logger.Error("schedule decode failed", "error", err)
		}
		return tasks, 0
	}
	seq := 0
	now := time.Now().UTC()
	for i := range saved {
		task := saved[i]
		task.ID = strings.TrimSpace(task.ID)
		if task.ID == "" {
			continue
		}
		if task.Status == "" {
			task.Status = ScheduleActive
		}
		if task.Status == ScheduleRunning {
			task.Status = ScheduleActive
			task.UpdatedAt = now
		}
		tasks[task.ID] = &task
		if n := scheduleIDNumber(task.ID); n > seq {
			seq = n
		}
	}
	return tasks, seq
}

func schedulePath(config Config) string {
	return filepath.Join(config.AppDir, "schedules.json")
}

func parseScheduleRequest(req ScheduleRequest) (time.Time, time.Duration, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "in"
	}
	switch mode {
	case "in", "after":
		delay, err := parseFlexibleDuration(firstNonEmpty(req.Delay, req.At, req.Every))
		if err != nil {
			return time.Time{}, 0, err
		}
		return time.Now().Add(delay).UTC(), 0, nil
	case "at", "on":
		at, err := parseScheduleTime(req.At)
		if err != nil {
			return time.Time{}, 0, err
		}
		return at.UTC(), 0, nil
	case "every", "repeat", "recurring":
		repeat, err := parseFlexibleDuration(firstNonEmpty(req.Every, req.Delay, req.At))
		if err != nil {
			return time.Time{}, 0, err
		}
		return time.Now().Add(repeat).UTC(), repeat, nil
	default:
		return time.Time{}, 0, fmt.Errorf("unknown schedule mode %q", req.Mode)
	}
}

func parseFlexibleDuration(input string) (time.Duration, error) {
	raw := strings.TrimSpace(strings.ToLower(input))
	if raw == "" {
		return 0, fmt.Errorf("duration is required")
	}
	switch raw {
	case "hourly":
		return time.Hour, nil
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d, nil
	}
	units := map[string]time.Duration{
		"s": time.Second,
		"m": time.Minute,
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	}
	for suffix, unit := range units {
		if strings.HasSuffix(raw, suffix) {
			number := strings.TrimSpace(strings.TrimSuffix(raw, suffix))
			value, err := strconv.ParseFloat(number, 64)
			if err != nil || value <= 0 {
				return 0, fmt.Errorf("invalid duration %q", input)
			}
			return time.Duration(value * float64(unit)), nil
		}
	}
	return 0, fmt.Errorf("invalid duration %q; try 10m, 2h, 1d, hourly, daily, or weekly", input)
}

func parseScheduleTime(input string) (time.Time, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return time.Time{}, fmt.Errorf("time is required")
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	localLayouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
	}
	for _, layout := range localLayouts {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed, nil
		}
	}
	if parsed, err := time.ParseInLocation("15:04", raw, time.Local); err == nil {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.Local)
		if next.Before(now) {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q; try 2026-07-04 15:04, 2026-07-04T15:04, 15:04, or RFC3339", input)
}

func scheduledRepeatDuration(task *ScheduledTask) (time.Duration, bool) {
	if task == nil || strings.TrimSpace(task.RepeatEvery) == "" {
		return 0, false
	}
	d, err := parseFlexibleDuration(task.RepeatEvery)
	return d, err == nil
}

func scheduleStatusRank(status ScheduledTaskStatus) int {
	switch status {
	case ScheduleRunning:
		return 0
	case ScheduleActive:
		return 1
	case ScheduleError:
		return 2
	case ScheduleDone:
		return 3
	case ScheduleCanceled:
		return 4
	default:
		return 5
	}
}

func scheduleIDNumber(id string) int {
	_, suffix, ok := strings.Cut(id, "-")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(suffix)
	return n
}

func scheduleName(name, prompt string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	prompt = strings.ReplaceAll(strings.TrimSpace(prompt), "\n", " ")
	return truncate(prompt, 60)
}

func repeatString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func cloneScheduledTask(task ScheduledTask) ScheduledTask {
	return task
}

func scheduleSummary(tasks []ScheduledTask) string {
	if len(tasks) == 0 {
		return "No scheduled tasks yet."
	}
	var b strings.Builder
	for _, task := range tasks {
		when := "no next run"
		if !task.NextRunAt.IsZero() {
			when = task.NextRunAt.Local().Format("2006-01-02 15:04:05")
		}
		repeat := ""
		if task.RepeatEvery != "" {
			repeat = " every " + task.RepeatEvery
		}
		fmt.Fprintf(&b, "- `%s` [%s] %s at `%s`%s - %s\n", task.ID, task.Status, task.Name, when, repeat, truncate(task.Prompt, 120))
	}
	return strings.TrimSpace(b.String())
}
