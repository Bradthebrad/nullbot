package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Bradthebrad/tinychain/agent"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// Agents' cloud tools, present only when Settings > Infrastructure allows
// them. An agent proposes; a person approves; then the agent (or the
// person) runs the plan.

const infraGuidance = "Cloud infrastructure: infra_status lists what NullBot created on AWS and Google Cloud, with plans and their state. infra_plan proposes machines or buckets as a plan of aws/gcloud commands; nothing runs until the user approves it in NullBot. infra_apply waits for that approval and then runs the plan. infra_destroy proposes removing resources NullBot made. Prefer the smallest machine that does the job, open only the ports needed, from the narrowest addresses (\"my-ip\" is the user's address), and remove what is no longer needed. Credentials are vault secrets of kind aws or gcp, named in the plan; secrets_list shows them.\n"

func infraTools(state *App) []agent.Tool {
	return []agent.Tool{infraStatusTool(state), infraPlanTool(state), infraApplyTool(state), infraDestroyTool(state)}
}

func infraStatusTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "infra_status",
		Description: "List the cloud resources NullBot created (machines with their addresses, buckets, firewall rules), their state and hourly cost, and recent plans with their state.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			st, err := state.InfraState()
			if err != nil {
				return "", err
			}
			type plan struct {
				ID, Title, Kind, State string
				CostHourly           float64 `json:"cost_hourly,omitempty"`
				Error                string  `json:"error,omitempty"`
			}
			var plans []plan
			for _, p := range st.Plans {
				if len(plans) == 15 {
					break
				}
				plans = append(plans, plan{p.ID, p.Title, p.Kind, p.State, p.CostHourly, p.Error})
			}
			var live []infra.Resource
			for _, r := range st.Resources {
				if r.Live() {
					live = append(live, r)
				}
			}
			return prettyJSON(map[string]any{"resources": live, "cost_hourly": st.CostHourly, "plans": plans, "tools": st.Tools}), nil
		},
	}
}

func infraPlanTool(state *App) agent.Tool {
	machine := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       agent.StringProperty("Lowercase letters, digits and dashes."),
			"type":       agent.StringProperty("Machine type: t3.medium, g6.xlarge (AWS); e2-standard-4, g2-standard-4 (Google Cloud)."),
			"disk_gb":    agent.NumberProperty("Boot disk size; 30 when unset."),
			"ports":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "TCP ports to open besides SSH (22)."},
			"allow_from": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Who may connect: IPv4 ranges such as 203.0.113.4/32, or \"my-ip\" for the user's address."},
			"user_data":  agent.StringProperty("A first-boot (cloud-init) script, optional."),
			"spot":       map[string]any{"type": "boolean", "description": "Use cheaper spare capacity the provider may take back."},
		},
		"required": []string{"name", "type", "allow_from"},
	}
	return agent.ToolFunc{
		Name:        "infra_plan",
		Description: "Propose creating machines and/or buckets on AWS or Google Cloud. Returns the plan: each aws/gcloud command it would run, and the approximate hourly cost. Nothing runs until the user approves; then call infra_apply.",
		Schema: agent.ToolSchema(map[string]any{
			"cloud":          map[string]any{"type": "string", "enum": []string{"aws", "gcp"}},
			"credential":     agent.StringProperty("The vault secret (kind aws or gcp) to use."),
			"region":         agent.StringProperty("AWS region (default: the secret's, or us-east-1); for Google Cloud, the bucket location."),
			"zone":           agent.StringProperty("Google Cloud zone (default us-central1-a)."),
			"project":        agent.StringProperty("Google Cloud project (default: the key file's)."),
			"ssh_key_secret": agent.StringProperty("An SSH key secret whose public key goes on every machine, so it can be reached with secret_run."),
			"machines":       map[string]any{"type": "array", "items": machine},
			"buckets":        map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": agent.StringProperty("Bucket name.")}, "required": []string{"name"}}},
			"reason":         agent.StringProperty("Why, in a sentence the user reads when approving."),
		}, "cloud", "credential", "reason"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			var req InfraRequest
			data, _ := json.Marshal(args)
			if err := json.Unmarshal(data, &req); err != nil {
				return "", fmt.Errorf("the plan request is malformed: %w", err)
			}
			if err := json.Unmarshal(data, &req.Spec); err != nil {
				return "", fmt.Errorf("the plan request is malformed: %w", err)
			}
			plan, err := state.PlanInfra(ctx, req, runtimeAgentName(ctx))
			if err != nil {
				return "", err
			}
			return planSummary(plan, "Waiting for the user's approval in NullBot (Infrastructure). Call infra_apply with this plan to run it once approved."), nil
		},
	}
}

func planSummary(plan infra.Plan, next string) string {
	type step struct {
		Summary string `json:"summary"`
		Command string `json:"command,omitempty"`
		State   string `json:"state,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	var steps []step
	for _, s := range plan.Steps {
		steps = append(steps, step{s.Summary, strings.Join(s.Argv, " "), s.State, s.Error})
	}
	out := map[string]any{"plan": plan.ID, "title": plan.Title, "state": plan.State, "steps": steps, "cost_hourly": fmt.Sprintf("$%.3f", plan.CostHourly), "cost_note": plan.CostNote}
	if len(plan.Notes) > 0 {
		out["notes"] = plan.Notes
	}
	if plan.Error != "" {
		out["error"] = plan.Error
	}
	if next != "" {
		out["next"] = next
	}
	return prettyJSON(out)
}

func infraApplyTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "infra_apply",
		Description: "Run a plan the user approved. If it is still waiting, this waits for the decision (up to wait_minutes, default 10). A rejected plan does not run.",
		Schema: agent.ToolSchema(map[string]any{
			"plan":         agent.StringProperty("The plan id from infra_plan or infra_destroy."),
			"wait_minutes": agent.NumberProperty("How long to wait for approval, 0 to 60."),
		}, "plan"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "plan"))
			wait := time.Duration(min(max(intArg(args, "wait_minutes", 10), 0), 60)) * time.Minute
			store := infraStore(state.Config())
			deadline := time.Now().Add(wait)
			for {
				plan, err := store.Plan(id)
				if err != nil {
					return "", err
				}
				switch plan.State {
				case infra.PlanApproved:
					done, err := state.ApplyInfraPlan(ctx, id, "agent "+runtimeAgentName(ctx))
					if err != nil {
						return planSummary(done, "It stopped at the failed step. What it created is in infra_status; propose removing it with infra_destroy if it is not wanted."), nil
					}
					return planSummary(done, ""), nil
				case infra.PlanProposed:
					if time.Now().After(deadline) {
						return planSummary(plan, "Still waiting for the user's approval. Tell them, and call infra_apply again later."), nil
					}
				case infra.PlanRejected:
					return "", errors.New("the user rejected this plan; it will not run")
				default:
					return planSummary(plan, "This plan has already run."), nil
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
}

func infraDestroyTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "infra_destroy",
		Description: "Propose removing resources NullBot created, by resource id (nb-…), name, or a plan id (everything it made). Machines go first, then their rules and keys. Buckets are removed only if empty unless delete_contents is set. Runs after the user approves, via infra_apply.",
		Schema: agent.ToolSchema(map[string]any{
			"ids":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"delete_contents": map[string]any{"type": "boolean", "description": "Also delete everything in the buckets."},
			"reason":          agent.StringProperty("Why, for the user."),
		}, "ids", "reason"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			var ids []string
			if raw, ok := args["ids"].([]any); ok {
				for _, v := range raw {
					ids = append(ids, fmt.Sprint(v))
				}
			}
			contents, _ := args["delete_contents"].(bool)
			plan, err := state.PlanInfraDestroy(ids, contents, runtimeAgentName(ctx), stringArg(args, "reason"))
			if err != nil {
				return "", err
			}
			return planSummary(plan, "Waiting for the user's approval. Call infra_apply with this plan to run it once approved."), nil
		},
	}
}
