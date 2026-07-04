package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Plan struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Goal        string     `json:"goal"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CurrentStep string     `json:"current_step,omitempty"`
	Steps       []PlanStep `json:"steps"`
	Notes       []string   `json:"notes,omitempty"`
}

func (p *Plan) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Goal        string          `json:"goal"`
		Status      string          `json:"status"`
		CreatedAt   time.Time       `json:"created_at"`
		UpdatedAt   time.Time       `json:"updated_at"`
		CurrentStep string          `json:"current_step,omitempty"`
		Steps       json.RawMessage `json:"steps"`
		Notes       json.RawMessage `json:"notes,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	steps, err := decodePlanSteps(raw.Steps)
	if err != nil {
		return err
	}
	notes, err := decodeStringList(raw.Notes)
	if err != nil {
		return err
	}
	*p = Plan{
		ID:          raw.ID,
		Name:        raw.Name,
		Goal:        raw.Goal,
		Status:      raw.Status,
		CreatedAt:   raw.CreatedAt,
		UpdatedAt:   raw.UpdatedAt,
		CurrentStep: raw.CurrentStep,
		Steps:       steps,
		Notes:       notes,
	}
	return nil
}

type PlanStep struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	Substeps    []PlanStep `json:"substeps,omitempty"`
}

func (s *PlanStep) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = PlanStep{Title: strings.TrimSpace(text), Status: "pending"}
		return nil
	}
	var raw struct {
		ID          string          `json:"id"`
		Title       string          `json:"title"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Status      string          `json:"status"`
		Substeps    json.RawMessage `json:"substeps,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	substeps, err := decodePlanSteps(raw.Substeps)
	if err != nil {
		return err
	}
	*s = PlanStep{
		ID:          raw.ID,
		Title:       firstNonEmptyPlanValue(raw.Title, raw.Name),
		Description: raw.Description,
		Status:      raw.Status,
		Substeps:    substeps,
	}
	return nil
}

type PlanSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Goal        string    `json:"goal"`
	Status      string    `json:"status"`
	Progress    string    `json:"progress"`
	CurrentStep string    `json:"current_step,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Path        string    `json:"path"`
}

func plansDir(config Config) string {
	return filepath.Join(config.AppDir, "plans")
}

func planPath(config Config, id string) (string, error) {
	id = slugifyPlanID(id)
	if id == "" {
		return "", fmt.Errorf("plan id is required")
	}
	return filepath.Join(plansDir(config), id+".json"), nil
}

func listPlans(config Config) []PlanSummary {
	dir := plansDir(config)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var summaries []PlanSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		plan, err := loadPlanFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		summaries = append(summaries, summarizePlan(config, plan))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries
}

func latestPlanID(config Config) string {
	plans := listPlans(config)
	if len(plans) == 0 {
		return ""
	}
	return plans[0].ID
}

func loadPlan(config Config, id string) (Plan, error) {
	path, err := planPath(config, id)
	if err != nil {
		return Plan{}, err
	}
	return loadPlanFile(path)
}

func loadPlanFile(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &plan); err != nil {
		return Plan{}, err
	}
	return normalizePlan(plan), nil
}

func savePlan(config Config, plan Plan) error {
	if err := os.MkdirAll(plansDir(config), 0700); err != nil {
		return err
	}
	plan = normalizePlan(plan)
	path, err := planPath(config, plan.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func (a *App) SavePlanJSON(id, raw string) error {
	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return err
	}
	if strings.TrimSpace(plan.ID) == "" {
		plan.ID = id
	}
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	return savePlan(config, plan)
}

func (a *App) PlanByID(id string) (Plan, error) {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	return loadPlan(config, id)
}

func normalizePlan(plan Plan) Plan {
	now := time.Now().UTC()
	plan.ID = slugifyPlanID(firstNonEmptyPlanValue(plan.ID, plan.Name, plan.Goal, "plan"))
	if strings.TrimSpace(plan.Name) == "" {
		plan.Name = strings.ReplaceAll(plan.ID, "-", " ")
	}
	if strings.TrimSpace(plan.Status) == "" {
		plan.Status = "planned"
	}
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = now
	}
	plan.UpdatedAt = now
	assignStepIDs(plan.Steps, "")
	completed, total := countPlanSteps(plan.Steps)
	if total > 0 && completed == total {
		plan.Status = "complete"
	} else if plan.Status == "complete" && completed < total {
		plan.Status = "in_progress"
	}
	if plan.CurrentStep == "" {
		if next := nextPlanStep(plan.Steps); next != nil {
			plan.CurrentStep = next.ID
		}
	}
	return plan
}

func assignStepIDs(steps []PlanStep, prefix string) {
	for i := range steps {
		if strings.TrimSpace(steps[i].ID) == "" {
			if prefix == "" {
				steps[i].ID = fmt.Sprintf("%d", i+1)
			} else {
				steps[i].ID = fmt.Sprintf("%s.%d", prefix, i+1)
			}
		}
		if strings.TrimSpace(steps[i].Status) == "" {
			steps[i].Status = "pending"
		}
		assignStepIDs(steps[i].Substeps, steps[i].ID)
	}
}

func summarizePlan(config Config, plan Plan) PlanSummary {
	completed, total := countPlanSteps(plan.Steps)
	progress := "0/0"
	if total > 0 {
		progress = fmt.Sprintf("%d/%d", completed, total)
	}
	path, _ := planPath(config, plan.ID)
	return PlanSummary{
		ID:          plan.ID,
		Name:        plan.Name,
		Goal:        plan.Goal,
		Status:      plan.Status,
		Progress:    progress,
		CurrentStep: plan.CurrentStep,
		UpdatedAt:   plan.UpdatedAt,
		Path:        path,
	}
}

func countPlanSteps(steps []PlanStep) (completed, total int) {
	for _, step := range steps {
		total++
		if step.Status == "complete" || step.Status == "completed" || step.Status == "done" {
			completed++
		}
		subDone, subTotal := countPlanSteps(step.Substeps)
		completed += subDone
		total += subTotal
	}
	return completed, total
}

func nextPlanStep(steps []PlanStep) *PlanStep {
	for i := range steps {
		if steps[i].Status != "complete" && steps[i].Status != "completed" && steps[i].Status != "done" {
			return &steps[i]
		}
		if next := nextPlanStep(steps[i].Substeps); next != nil {
			return next
		}
	}
	return nil
}

func updatePlanStepStatus(plan *Plan, stepID, status, note string) bool {
	updated := updatePlanStepStatusIn(plan.Steps, stepID, status, note)
	if updated {
		if next := nextPlanStep(plan.Steps); next != nil {
			plan.CurrentStep = next.ID
			if plan.Status == "planned" {
				plan.Status = "in_progress"
			}
		} else {
			plan.CurrentStep = ""
			plan.Status = "complete"
		}
		if strings.TrimSpace(note) != "" {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s: %s", stepID, truncate(note, 2000)))
		}
		plan.UpdatedAt = time.Now().UTC()
	}
	return updated
}

func updatePlanStepStatusIn(steps []PlanStep, stepID, status, note string) bool {
	for i := range steps {
		if steps[i].ID == stepID {
			steps[i].Status = status
			if note != "" {
				steps[i].Description = strings.TrimSpace(steps[i].Description + "\n\nUpdate: " + truncate(note, 2500))
			}
			return true
		}
		if updatePlanStepStatusIn(steps[i].Substeps, stepID, status, note) {
			return true
		}
	}
	return false
}

func planMarkdown(plan Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", plan.Name)
	fmt.Fprintf(&b, "- **ID:** `%s`\n", plan.ID)
	fmt.Fprintf(&b, "- **Status:** `%s`\n", plan.Status)
	if plan.Goal != "" {
		fmt.Fprintf(&b, "- **Goal:** %s\n", plan.Goal)
	}
	if plan.CurrentStep != "" {
		fmt.Fprintf(&b, "- **Current Step:** `%s`\n", plan.CurrentStep)
	}
	b.WriteString("\n## Checklist\n\n")
	writePlanStepsMarkdown(&b, plan.Steps, 0)
	if len(plan.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, note := range plan.Notes {
			fmt.Fprintf(&b, "- %s\n", truncate(note, 1600))
		}
	}
	return strings.TrimSpace(b.String())
}

func writePlanStepsMarkdown(b *strings.Builder, steps []PlanStep, depth int) {
	prefix := strings.Repeat("  ", depth)
	for _, step := range steps {
		box := "[ ]"
		if step.Status == "complete" || step.Status == "completed" || step.Status == "done" {
			box = "[x]"
		}
		fmt.Fprintf(b, "%s- %s `%s` **%s**", prefix, box, step.ID, step.Title)
		if step.Description != "" {
			fmt.Fprintf(b, " - %s", truncate(strings.ReplaceAll(step.Description, "\n", " "), 1600))
		}
		b.WriteByte('\n')
		writePlanStepsMarkdown(b, step.Substeps, depth+1)
	}
}

func planJSON(plan Plan) string {
	data, _ := json.MarshalIndent(normalizePlan(plan), "", "  ")
	return string(data)
}

func extractPlanJSON(text string) (Plan, error) {
	text = strings.TrimSpace(text)
	candidates := []string{text}
	if start := strings.Index(text, "```"); start >= 0 {
		rest := text[start+3:]
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			candidates = append([]string{rest[:end]}, candidates...)
		}
	}
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			candidates = append([]string{text[start : end+1]}, candidates...)
		}
	}
	var lastErr error
	for _, candidate := range candidates {
		var plan Plan
		if err := json.Unmarshal([]byte(candidate), &plan); err != nil {
			lastErr = err
			continue
		}
		return normalizePlan(plan), nil
	}
	return Plan{}, lastErr
}

var planIDClean = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyPlanID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = planIDClean.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 64 {
		value = strings.Trim(value[:64], "-")
	}
	return value
}

func firstNonEmptyPlanValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func decodePlanSteps(raw json.RawMessage) ([]PlanStep, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var steps []PlanStep
	if err := json.Unmarshal(raw, &steps); err == nil {
		return steps, nil
	}
	var step PlanStep
	if err := json.Unmarshal(raw, &step); err == nil {
		if strings.TrimSpace(step.Title) == "" && strings.TrimSpace(step.Description) == "" && len(step.Substeps) == 0 {
			return nil, nil
		}
		return []PlanStep{step}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	return planStepsFromText(text), nil
}

func planStepsFromText(text string) []PlanStep {
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(text, "none") || strings.EqualFold(text, "n/a") {
		return nil
	}
	var out []PlanStep
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*0123456789.) \t")
		if line == "" {
			continue
		}
		out = append(out, PlanStep{Title: line, Status: "pending"})
	}
	if len(out) == 0 {
		return []PlanStep{{Title: text, Status: "pending"}}
	}
	return out
}

func decodeStringList(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	return []string{text}, nil
}
