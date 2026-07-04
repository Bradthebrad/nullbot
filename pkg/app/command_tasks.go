package app

import (
	"strings"
)

func (a *App) tasksCommand(rest string) Reply {
	fields := strings.Fields(rest)
	if len(fields) >= 2 && fields[0] == "cancel" {
		id := fields[1]
		if ok := a.CancelTask(id); !ok {
			return a.reply("Task not cancelable or not found: "+id, "/tasks", "tasks", map[string]any{
				"tasks": a.TaskSnapshots(),
			})
		}
		return a.reply("Cancel requested for "+id+".", "/tasks", "tasks", map[string]any{
			"tasks": a.TaskSnapshots(),
		})
	}
	if len(fields) >= 2 && fields[0] == "detail" {
		id := fields[1]
		task, ok := a.TaskDetails(id)
		if !ok {
			return a.reply("Task not found: "+id, "/tasks", "tasks", map[string]any{
				"tasks": a.TaskSnapshots(),
			})
		}
		return a.reply("Task details opened.", "/tasks", "tasks", map[string]any{
			"tasks":  a.TaskSnapshots(),
			"detail": task,
		})
	}
	tasks := a.TaskSnapshots()
	return a.reply("Tasks panel opened.", "/tasks", "tasks", map[string]any{
		"tasks":   tasks,
		"summary": taskSummary(tasks),
	})
}
