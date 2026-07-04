package app

import "testing"

func TestExtractPlanJSONAllowsStringSubsteps(t *testing.T) {
	raw := `{
		"id": "demo",
		"name": "Demo",
		"goal": "Make a plan",
		"status": "planned",
		"steps": [
			{
				"title": "Inspect workspace",
				"description": "Read the important files.",
				"status": "pending",
				"substeps": "Read README.md"
			}
		]
	}`
	plan, err := extractPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %d", len(plan.Steps))
	}
	if len(plan.Steps[0].Substeps) != 1 {
		t.Fatalf("substeps = %#v", plan.Steps[0].Substeps)
	}
	if plan.Steps[0].Substeps[0].Title != "Read README.md" {
		t.Fatalf("substep title = %q", plan.Steps[0].Substeps[0].Title)
	}
}

func TestExtractPlanJSONAllowsStringStepsAndNotes(t *testing.T) {
	raw := `{
		"id": "demo",
		"name": "Demo",
		"goal": "Make a plan",
		"status": "planned",
		"steps": ["Read README.md", "Update implementation"],
		"notes": "Keep it scoped."
	}`
	plan, err := extractPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps = %#v", plan.Steps)
	}
	if plan.Steps[1].Title != "Update implementation" {
		t.Fatalf("second step = %#v", plan.Steps[1])
	}
	if len(plan.Notes) != 1 || plan.Notes[0] != "Keep it scoped." {
		t.Fatalf("notes = %#v", plan.Notes)
	}
}
