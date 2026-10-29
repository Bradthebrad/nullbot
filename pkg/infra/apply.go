package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Runner runs one command of a plan with the plan's credentials in its
// environment, and returns what it printed.
type Runner interface {
	Run(ctx context.Context, argv []string) (stdout, stderr string, err error)
}

// ApplyOptions are the caller's parts of applying a plan.
type ApplyOptions struct {
	Runner Runner
	// Redact removes secret values from output before it is stored.
	Redact func(string) string
	// Progress is told about the plan after each step.
	Progress func(Plan)
	// By names who applied it, for the ledger.
	By string
	// TempDir holds first-boot scripts while a command reads them.
	TempDir string
}

// Apply runs an approved plan, one step at a time, recording each in the
// ledger and the inventory. It stops at the first failure; what was
// created before it stays in the inventory, so it can be removed.
func (s *Store) Apply(ctx context.Context, id string, opts ApplyOptions) (Plan, error) {
	if opts.Redact == nil {
		opts.Redact = func(s string) string { return s }
	}
	unlock, err := s.lock()
	if err != nil {
		return Plan{}, err
	}
	plan, err := s.Plan(id)
	if err == nil && plan.State != PlanApproved {
		err = fmt.Errorf("plan %s is %s; only an approved plan runs", id, plan.State)
		if plan.State == PlanProposed {
			err = fmt.Errorf("plan %s is waiting for approval", id)
		}
	}
	if err == nil && (plan.ApprovedHash == "" || plan.ApprovedHash != plan.ContentHash()) {
		// The plan file changed after it was approved: what would run is
		// not what was approved.
		err = fmt.Errorf("plan %s changed after it was approved, so it will not run; propose it again", id)
		plan.State, plan.Error, plan.Finished = PlanFailed, err.Error(), time.Now().UTC()
		_ = s.savePlanLocked(plan)
		_ = s.logLocked(LedgerEntry{Plan: plan.ID, Event: "failed", Error: err.Error(), By: opts.By})
	}
	if err != nil {
		unlock()
		return plan, err
	}
	plan.State = PlanApplying
	plan.RunnerPID, plan.RunnerHost = os.Getpid(), hostName()
	err = s.savePlanLocked(plan)
	if err == nil && plan.Kind == "create" {
		// The resources go in first, so a failure halfway leaves a record of
		// everything that may exist.
		err = s.updateResourcesLocked(func(current []Resource) []Resource {
			for _, r := range plan.Resources {
				r.State, r.Updated = StateCreating, time.Now().UTC()
				current = append(current, r)
			}
			return current
		})
	}
	unlock()
	if err != nil {
		return plan, err
	}

	fail := func(step *Step, msg string) (Plan, error) {
		plan.State, plan.Error, plan.Finished = PlanFailed, msg, time.Now().UTC()
		if step != nil {
			step.State, step.Error = "failed", msg
		}
		for i := range plan.Steps {
			if plan.Steps[i].State == "pending" {
				plan.Steps[i].State = "skipped"
			}
		}
		_ = s.SavePlan(plan)
		_ = s.settleAfterFailure(plan)
		_ = s.Log(LedgerEntry{Plan: plan.ID, Event: "failed", Error: msg, By: opts.By})
		if opts.Progress != nil {
			opts.Progress(plan)
		}
		return plan, errors.New(msg)
	}

	// gone marks a resource as not there (never created, or already
	// removed) and skips its remaining steps.
	gone := func(i int, why string) {
		for j := i + 1; j < len(plan.Steps) && plan.Steps[j].Resource == plan.Steps[i].Resource; j++ {
			plan.Steps[j].State, plan.Steps[j].Output = "skipped", why
		}
		_ = s.setState(plan.Steps[i].Resource, StateDeleted, nil)
	}
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step.State == "done" || step.State == "skipped" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(step, "stopped before this step")
		}
		step.State, step.Ran = "running", time.Now().UTC()
		_ = s.SavePlan(plan)
		if opts.Progress != nil {
			opts.Progress(plan)
		}
		if step.Action == "delete" {
			_ = s.setState(step.Resource, StateDeleting, nil)
		}
		if len(step.Argv) == 0 {
			step.State = "done"
			if step.Action == "delete" {
				_ = s.setState(step.Resource, StateDeleted, nil)
			}
			continue
		}
		argv, cleanup, err := s.resolve(step, opts.TempDir)
		if err != nil {
			return fail(step, err.Error())
		}
		stdout, stderr, runErr := opts.Runner.Run(ctx, argv)
		cleanup()
		stdout, stderr = opts.Redact(stdout), opts.Redact(stderr)
		entry := LedgerEntry{Plan: plan.ID, Event: "step", Step: step.ID, Summary: step.Summary, Argv: step.Argv, Resource: step.Resource, By: opts.By}
		switch {
		case step.Action == "absent" && runErr == nil:
			msg := fmt.Sprintf("%s already exists; NullBot creates only new ones, so choose another name", resourceName(plan, step.Resource))
			entry.Error = msg
			_ = s.Log(entry)
			return fail(step, msg)
		case step.Action == "absent" && notFound(stderr+" "+stdout):
			entry.Output = "not there yet"
			_ = s.Log(entry)
			step.State, step.Output = "done", "not there yet"
			continue
		case (step.Action == "verify" || step.Action == "find") && runErr != nil && notFound(stderr+" "+stdout):
			entry.Output = "already gone"
			_ = s.Log(entry)
			step.State, step.Output = "done", "already gone"
			gone(i, "already gone")
			continue
		case step.Action == "verify" && runErr == nil && !strings.Contains(stdout, step.Resource):
			msg := fmt.Sprintf("%s does not carry NullBot's tag %s, so NullBot will not remove it; if NullBot made it and its tagging step failed, remove it yourself", resourceName(plan, step.Resource), step.Resource)
			entry.Error = msg
			_ = s.Log(entry)
			return fail(step, msg)
		}
		if runErr != nil {
			if step.IgnoreMissing && notFound(stderr+" "+stdout) {
				entry.Output = "already gone"
				_ = s.Log(entry)
				step.State, step.Output = "done", "already gone"
				if step.Action == "delete" {
					_ = s.setState(step.Resource, StateDeleted, nil)
				}
				continue
			}
			msg := firstLines(firstNonEmpty(stderr, stdout, runErr.Error()), 6)
			entry.Error = msg
			_ = s.Log(entry)
			if ctx.Err() != nil {
				msg = "stopped while it ran: " + msg
			}
			return fail(step, msg)
		}
		entry.Output = excerpt(stdout)
		_ = s.Log(entry)
		step.State, step.Output = "done", excerpt(stdout)
		found, err := s.parse(step, stdout)
		if err != nil {
			return fail(step, "the command succeeded, but its answer could not be read: "+err.Error())
		}
		if step.Action == "find" && !found {
			step.Output = "not found: NullBot never created it"
			gone(i, "nothing to remove")
			continue
		}
		if plan.Kind == "destroy" && (i+1 == len(plan.Steps) || plan.Steps[i+1].Resource != step.Resource) {
			_ = s.setState(step.Resource, StateDeleted, nil)
		}
	}
	if plan.Kind == "create" {
		_ = s.setStates(plan, func(r Resource) string {
			if r.State == StateCreating {
				return StateReady
			}
			return r.State
		})
	}
	plan.State, plan.Finished = PlanApplied, time.Now().UTC()
	_ = s.SavePlan(plan)
	_ = s.Log(LedgerEntry{Plan: plan.ID, Event: "applied", Summary: plan.Title, By: opts.By})
	if opts.Progress != nil {
		opts.Progress(plan)
	}
	return plan, nil
}

// resolve fills in a step's placeholders: other resources' cloud ids, and
// its input as a private file.
func (s *Store) resolve(step *Step, tempDir string) ([]string, func(), error) {
	resources, err := s.Resources()
	if err != nil {
		return nil, func() {}, err
	}
	ids := map[string]string{}
	for _, r := range resources {
		ids[r.ID] = r.CloudID
	}
	cleanup := func() {}
	argv := make([]string, len(step.Argv))
	for i, arg := range step.Argv {
		for strings.Contains(arg, "{{cloud_id:") {
			start := strings.Index(arg, "{{cloud_id:")
			end := strings.Index(arg[start:], "}}")
			if end < 0 {
				return nil, cleanup, fmt.Errorf("step %s has a broken placeholder", step.ID)
			}
			ref := arg[start+len("{{cloud_id:") : start+end]
			id := ids[ref]
			if id == "" {
				return nil, cleanup, fmt.Errorf("step %s needs %s, which was not created", step.ID, ref)
			}
			arg = arg[:start] + id + arg[start+end+2:]
		}
		if strings.Contains(arg, fileArg) {
			if tempDir == "" {
				tempDir = os.TempDir()
			}
			f, err := os.CreateTemp(tempDir, "nullbot-infra-*")
			if err != nil {
				return nil, cleanup, err
			}
			_ = os.Chmod(f.Name(), 0o600)
			_, werr := f.WriteString(step.Input)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				os.Remove(f.Name())
				return nil, cleanup, errors.Join(werr, cerr)
			}
			path := f.Name()
			cleanup = func() { os.Remove(path) }
			arg = strings.ReplaceAll(arg, fileArg, filepath.ToSlash(path))
		}
		argv[i] = arg
	}
	return argv, cleanup, nil
}

func (s *Store) setState(id, state string, details map[string]string) error {
	return s.UpdateResources(func(current []Resource) []Resource {
		for i := range current {
			if current[i].ID == id {
				current[i].State, current[i].Updated = state, time.Now().UTC()
				for k, v := range details {
					if current[i].Details == nil {
						current[i].Details = map[string]string{}
					}
					current[i].Details[k] = v
				}
			}
		}
		return current
	})
}

func (s *Store) setStates(plan Plan, next func(Resource) string) error {
	in := map[string]bool{}
	for _, r := range plan.Resources {
		in[r.ID] = true
	}
	return s.UpdateResources(func(current []Resource) []Resource {
		for i := range current {
			if in[current[i].ID] {
				current[i].State, current[i].Updated = next(current[i]), time.Now().UTC()
			}
		}
		return current
	})
}

// parse reads what a create, read or find step printed into the
// inventory. found is false when a find step found nothing.
func (s *Store) parse(step *Step, stdout string) (found bool, err error) {
	if step.Parse == "" {
		return true, nil
	}
	details := map[string]string{}
	cloudID := ""
	// Buckets, Google Cloud machines and firewall rules are known by their
	// names; the name becomes the cloud id only once creating succeeded.
	byName := false
	switch step.Parse {
	case "bucket":
		byName = true
	case "aws_find":
		var ids []string
		if err := json.Unmarshal([]byte(stdout), &ids); err != nil {
			return false, err
		}
		if len(ids) == 0 {
			return false, nil
		}
		cloudID = ids[0]
	case "gcp_find":
		var out []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			return false, err
		}
		if len(out) == 0 || out[0].Name == "" {
			return false, nil
		}
		cloudID = out[0].Name
	case "aws_key_pair":
		var out struct{ KeyPairId string }
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.KeyPairId == "" {
			return false, fmt.Errorf("no key pair id")
		}
		cloudID = out.KeyPairId
	case "aws_security_group":
		var out struct{ GroupId string }
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.GroupId == "" {
			return false, fmt.Errorf("no security group id")
		}
		cloudID = out.GroupId
	case "aws_instance":
		var out struct {
			Instances []struct {
				InstanceId       string
				PrivateIpAddress string
			}
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || len(out.Instances) == 0 || out.Instances[0].InstanceId == "" {
			return false, fmt.Errorf("no instance id")
		}
		cloudID = out.Instances[0].InstanceId
		details["private_ip"] = out.Instances[0].PrivateIpAddress
	case "aws_instance_details":
		var out map[string]any
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			return false, err
		}
		for _, k := range []string{"public_ip", "private_ip", "state"} {
			if v, ok := out[k].(string); ok && v != "" {
				details[k] = v
			}
		}
	case "gcp_firewall":
		byName = true
	case "gcp_instance":
		byName = true
		var out []struct {
			Name              string `json:"name"`
			Status            string `json:"status"`
			NetworkInterfaces []struct {
				NetworkIP     string `json:"networkIP"`
				AccessConfigs []struct {
					NatIP string `json:"natIP"`
				} `json:"accessConfigs"`
			} `json:"networkInterfaces"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || len(out) == 0 {
			return false, fmt.Errorf("no machine in the answer")
		}
		details["state"] = strings.ToLower(out[0].Status)
		if len(out[0].NetworkInterfaces) > 0 {
			ni := out[0].NetworkInterfaces[0]
			details["private_ip"] = ni.NetworkIP
			if len(ni.AccessConfigs) > 0 {
				details["public_ip"] = ni.AccessConfigs[0].NatIP
			}
		}
	default:
		return false, fmt.Errorf("unknown parser %s", step.Parse)
	}
	return true, s.UpdateResources(func(current []Resource) []Resource {
		for i := range current {
			if current[i].ID != step.Resource {
				continue
			}
			if cloudID != "" {
				current[i].CloudID = cloudID
			} else if byName {
				current[i].CloudID = current[i].Name
			}
			if current[i].Details == nil {
				current[i].Details = map[string]string{}
			}
			for k, v := range details {
				if v != "" {
					current[i].Details[k] = v
				}
			}
			current[i].Updated = time.Now().UTC()
		}
		return current
	})
}

// notFound reports a provider error saying the thing does not exist. Only
// the providers' own not-found codes count: a number or word somewhere in
// another error (an id containing 404, a dependency that does not exist)
// must never count a failed removal as done.
func notFound(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		// AWS: InvalidInstanceID.NotFound, InvalidGroup.NotFound,
		// InvalidKeyPair.NotFound, and S3's NoSuchBucket and HeadBucket 404.
		"invalidinstanceid.notfound", "invalidgroup.notfound", "invalidkeypair.notfound",
		"(nosuchbucket)", "(404) when calling the headbucket operation",
		// Google Cloud: compute's "was not found", storage's 404s, and rm
		// with nothing to remove.
		"' was not found", "not found: 404", "notfoundexception", "matched no objects",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func resourceName(plan Plan, id string) string {
	for _, r := range plan.Resources {
		if r.ID == id {
			return r.Name
		}
	}
	return id
}

// settleAfterFailure leaves the inventory true after a plan stopped:
// what it was creating is failed (and may exist), what it was removing
// goes back to how it was, so the removal can be tried again.
func (s *Store) settleAfterFailure(plan Plan) error {
	before := map[string]string{}
	for _, r := range plan.Resources {
		before[r.ID] = r.State
	}
	return s.setStates(plan, func(r Resource) string {
		switch r.State {
		case StateCreating:
			return StateFailed
		case StateDeleting:
			if prev := before[r.ID]; prev != "" && prev != StateDeleting && prev != StatePlanned {
				return prev
			}
			return StateFailed
		}
		return r.State
	})
}

// RecoverStale fails plans left running by a process that ended (NullBot
// quit or was killed while it applied them), so their resources can be
// removed. alive reports whether a process on this computer is running.
func (s *Store) RecoverStale(alive func(pid int) bool) error {
	plans, err := s.Plans(0)
	if err != nil {
		return err
	}
	host := hostName()
	for _, p := range plans {
		if p.State != PlanApplying || p.RunnerHost != host || p.RunnerPID == 0 || p.RunnerPID == os.Getpid() || alive(p.RunnerPID) {
			continue
		}
		unlock, err := s.lock()
		if err != nil {
			return err
		}
		current, err := s.Plan(p.ID)
		if err == nil && current.State == PlanApplying && current.RunnerPID == p.RunnerPID {
			msg := "NullBot stopped while this plan ran; what it was doing may be half done"
			current.State, current.Error, current.Finished = PlanFailed, msg, time.Now().UTC()
			for i := range current.Steps {
				switch current.Steps[i].State {
				case "running":
					current.Steps[i].State, current.Steps[i].Error = "failed", "stopped while it ran"
				case "pending":
					current.Steps[i].State = "skipped"
				}
			}
			_ = s.savePlanLocked(current)
			_ = s.logLocked(LedgerEntry{Plan: current.ID, Event: "failed", Error: msg, By: "NullBot"})
		}
		unlock()
		if err == nil {
			_ = s.settleAfterFailure(current)
		}
	}
	return nil
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return excerpt(strings.Join(lines, "\n"))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
