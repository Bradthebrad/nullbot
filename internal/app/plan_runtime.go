package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tcagent "tinychain/agent"
	"tinychain/callbacks"
	"tinychain/lc"
)

func (a *App) runPlanner(ctx context.Context, focus string) (Plan, error) {
	planCtx, cancel := context.WithCancel(ctx)
	taskID := a.startTask("Planner", "planner", focus, cancel)
	defer cancel()

	a.mu.Lock()
	config := a.config
	a.mu.Unlock()

	model, err := modelFromConfig(config)
	if err != nil {
		a.finishTask(taskID, "", err)
		return Plan{}, err
	}
	skills, _ := loadSkills(config)
	tools := BuiltinToolsFor(config, a, false)
	mcpTools, closers := a.loadMCPTools(planCtx, config)
	defer closeAll(closers)
	tools = append(tools, mcpTools...)

	system := plannerSystemPrompt(config, tools, skills)
	planner := tcagent.New(tcagent.Config{
		Model:         model,
		SystemPrompt:  system,
		Tools:         tools,
		Skills:        skills,
		MaxIterations: config.Agent.MaxIterations,
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			record := activityRecordFromCallback(event)
			record.Name = "planner/" + record.Name
			a.recordTaskCallback(taskID, event)
			a.recordUsageCallback(taskID, "planner", config.Model, event)
			a.appendActivity(record)
		}),
	})
	result, err := planner.InvokeMessages(planCtx, []lc.BaseMessage{lc.Human(plannerTask(config, focus))})
	if err != nil {
		a.finishTask(taskID, "", err)
		return Plan{}, err
	}
	text := lcContentText(result.Output.Content)
	plan, err := extractPlanJSON(text)
	if err != nil {
		a.finishTask(taskID, text, err)
		return Plan{}, fmt.Errorf("planner did not return valid plan JSON: %w", err)
	}
	if plan.Goal == "" {
		plan.Goal = focus
	}
	if err := savePlan(config, plan); err != nil {
		a.finishTask(taskID, text, err)
		return Plan{}, err
	}
	a.finishTask(taskID, planMarkdown(plan), nil)
	return plan, nil
}

func (a *App) runPlanExecutor(ctx context.Context, id string) (Plan, error) {
	execCtx, cancel := context.WithCancel(ctx)
	taskID := a.startTask("Plan Executor", "plan-executor", id, cancel)
	defer cancel()

	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		id = latestPlanID(config)
	}
	if id == "" {
		err := fmt.Errorf("no plans found")
		a.finishTask(taskID, "", err)
		return Plan{}, err
	}
	plan, err := loadPlan(config, id)
	if err != nil {
		a.finishTask(taskID, "", err)
		return Plan{}, err
	}

	model, err := modelFromConfig(config)
	if err != nil {
		a.finishTask(taskID, "", err)
		return Plan{}, err
	}
	skills, _ := loadSkills(config)
	tools := BuiltinToolsFor(config, a, true)
	mcpTools, closers := a.loadMCPTools(execCtx, config)
	defer closeAll(closers)
	tools = append(tools, mcpTools...)

	executor := tcagent.New(tcagent.Config{
		Model:         model,
		SystemPrompt:  executorSystemPrompt(config, plan, tools, skills),
		Tools:         tools,
		Skills:        skills,
		MaxIterations: config.Agent.MaxIterations,
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			record := activityRecordFromCallback(event)
			record.Name = "executor/" + record.Name
			a.recordTaskCallback(taskID, event)
			a.recordUsageCallback(taskID, "plan-executor", config.Model, event)
			a.appendActivity(record)
		}),
	})

	rounds := 0
	for {
		next := nextPlanStep(plan.Steps)
		if next == nil {
			plan.Status = "complete"
			plan.CurrentStep = ""
			_ = savePlan(config, plan)
			a.finishTask(taskID, planMarkdown(plan), nil)
			return plan, nil
		}
		rounds++
		if rounds > 50 {
			err := fmt.Errorf("plan execution stopped after 50 rounds")
			a.finishTask(taskID, planMarkdown(plan), err)
			return plan, err
		}
		task := fmt.Sprintf("Execute exactly this next plan step, then summarize what changed and whether it is complete.\n\nPlan:\n%s\n\nNext step: %s - %s\n%s", planMarkdown(plan), next.ID, next.Title, next.Description)
		result, err := executor.InvokeMessages(execCtx, []lc.BaseMessage{lc.Human(task)})
		if err != nil {
			a.finishTask(taskID, planMarkdown(plan), err)
			return plan, err
		}
		output := lcContentText(result.Output.Content)
		updatePlanStepStatus(&plan, next.ID, "complete", output)
		if err := savePlan(config, plan); err != nil {
			a.finishTask(taskID, planMarkdown(plan), err)
			return plan, err
		}
		a.appendActivity(ActivityRecord{Time: nowUTC(), Kind: "plan", Name: "plan_step", Status: "complete", Detail: next.ID + " " + next.Title})
	}
}

func plannerSystemPrompt(config Config, tools []tcagent.Tool, skills []tcagent.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s's planner agent. Create practical execution plans as strict JSON only.\n", DisplayName(config))
	b.WriteString("Analyze the task and workspace with available read-only tools where useful. Do not execute the implementation. Return only one JSON object matching this schema: {id,name,goal,status,steps:[{id,title,description,status,substeps:[]}],notes:[]}.\n")
	b.WriteString("Use status `planned` for the plan and `pending` for incomplete steps. Choose the number of steps yourself. Make steps iterative: each top-level step should be independently executable in order.\n")
	if root, err := workspaceRoot(config); err == nil {
		fmt.Fprintf(&b, "Workspace: %s\n", root)
	}
	if len(tools) > 0 {
		b.WriteString("Tools available for inspection:\n")
		for _, tool := range tools {
			def := tool.Definition()
			fmt.Fprintf(&b, "- %s: %s\n", def.Name, def.Description)
		}
	}
	if len(skills) > 0 {
		b.WriteString("Installed skills:\n")
		for _, skill := range skills {
			fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
		}
	}
	return strings.TrimSpace(b.String())
}

func plannerTask(config Config, focus string) string {
	return fmt.Sprintf("Create a new executable plan for this task:\n\n%s\n\nThe plan will be saved under %s. Use a short stable id derived from the task.", focus, plansDir(config))
}

func executorSystemPrompt(config Config, plan Plan, tools []tcagent.Tool, skills []tcagent.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s's plan execution agent. Execute one plan step at a time, then report completion evidence.\n", DisplayName(config))
	b.WriteString("Use tools directly. Do not ask for slash commands. Do not skip ahead. Destructive changes still require appropriate caution from tool descriptions and permission policies.\n")
	fmt.Fprintf(&b, "Plan id: %s\n", plan.ID)
	if root, err := workspaceRoot(config); err == nil {
		fmt.Fprintf(&b, "Workspace: %s\n", root)
	}
	if len(tools) > 0 {
		b.WriteString("Available tools:\n")
		for _, tool := range tools {
			def := tool.Definition()
			fmt.Fprintf(&b, "- %s: %s\n", def.Name, def.Description)
		}
	}
	if len(skills) > 0 {
		b.WriteString("Installed skills:\n")
		for _, skill := range skills {
			fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
		}
	}
	return strings.TrimSpace(b.String())
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
