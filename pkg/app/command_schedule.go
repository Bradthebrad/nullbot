package app

import (
	"fmt"
	"strings"
)

func (a *App) scheduleCommand(rest string) Reply {
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "list" {
		return a.schedulePanelReply("Schedule panel opened.")
	}

	action := strings.ToLower(fields[0])
	switch action {
	case "in", "after":
		if len(fields) < 3 {
			return a.reply("Usage: /schedule in <duration> <message>", "/schedule", "schedule", map[string]any{
				"schedules": a.ScheduledTasks(),
			})
		}
		return a.createScheduleReply(ScheduleRequest{Mode: "in", Delay: fields[1], Prompt: strings.Join(fields[2:], " ")})
	case "every", "repeat":
		if len(fields) < 3 {
			return a.reply("Usage: /schedule every <duration> <message>", "/schedule", "schedule", map[string]any{
				"schedules": a.ScheduledTasks(),
			})
		}
		return a.createScheduleReply(ScheduleRequest{Mode: "every", Every: fields[1], Prompt: strings.Join(fields[2:], " ")})
	case "at", "on":
		at, prompt := parseScheduleAtRest(strings.TrimSpace(strings.TrimPrefix(rest, fields[0])))
		if at == "" || prompt == "" {
			return a.reply("Usage: /schedule at <YYYY-MM-DD HH:MM|HH:MM|RFC3339> <message>", "/schedule", "schedule", map[string]any{
				"schedules": a.ScheduledTasks(),
			})
		}
		return a.createScheduleReply(ScheduleRequest{Mode: "at", At: at, Prompt: prompt})
	case "run":
		if len(fields) < 2 {
			return a.reply("Usage: /schedule run <id>", "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		if _, err := a.RunScheduledTaskNow(fields[1]); err != nil {
			return a.reply("Schedule run failed: "+err.Error(), "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		return a.reply("Scheduled task started: "+fields[1], "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks(), "summary": scheduleSummary(a.ScheduledTasks())})
	case "cancel":
		if len(fields) < 2 {
			return a.reply("Usage: /schedule cancel <id>", "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		if _, ok := a.CancelScheduledTask(fields[1]); !ok {
			return a.reply("Schedule not found: "+fields[1], "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		return a.reply("Schedule canceled: "+fields[1], "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks(), "summary": scheduleSummary(a.ScheduledTasks())})
	case "delete", "remove", "rm":
		if len(fields) < 2 {
			return a.reply("Usage: /schedule delete <id>", "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		if !a.DeleteScheduledTask(fields[1]) {
			return a.reply("Schedule not found: "+fields[1], "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks()})
		}
		return a.reply("Schedule deleted: "+fields[1], "/schedule", "schedule", map[string]any{"schedules": a.ScheduledTasks(), "summary": scheduleSummary(a.ScheduledTasks())})
	default:
		return a.reply(fmt.Sprintf("Unknown /schedule action %q. Try /schedule, /schedule in 10m <message>, /schedule every 1h <message>, or /schedule at 15:04 <message>.", fields[0]), "/schedule", "schedule", map[string]any{
			"schedules": a.ScheduledTasks(),
		})
	}
}

func (a *App) createScheduleReply(req ScheduleRequest) Reply {
	task, err := a.CreateScheduledTask(req)
	if err != nil {
		return a.reply("Schedule failed: "+err.Error(), "/schedule", "schedule", map[string]any{
			"schedules": a.ScheduledTasks(),
		})
	}
	tasks := a.ScheduledTasks()
	return a.reply("Scheduled "+task.ID+" for "+task.NextRunAt.Local().Format("2006-01-02 15:04:05")+".", "/schedule", "schedule", map[string]any{
		"schedules": tasks,
		"summary":   scheduleSummary(tasks),
		"selected":  task,
	})
}

func (a *App) schedulePanelReply(message string) Reply {
	tasks := a.ScheduledTasks()
	return a.reply(message, "/schedule", "schedule", map[string]any{
		"schedules": tasks,
		"summary":   scheduleSummary(tasks),
	})
}

func parseScheduleAtRest(rest string) (string, string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", ""
	}
	parts := strings.Fields(rest)
	if len(parts) < 2 {
		return "", ""
	}
	if strings.Contains(parts[0], "T") || strings.Contains(parts[0], ":") {
		return parts[0], strings.Join(parts[1:], " ")
	}
	if len(parts) >= 3 && strings.Contains(parts[1], ":") {
		return parts[0] + " " + parts[1], strings.Join(parts[2:], " ")
	}
	return "", ""
}
