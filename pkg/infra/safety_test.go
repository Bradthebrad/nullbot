package infra

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gcpSpec() Spec {
	return Spec{Cloud: GCP, Credential: "gcp", Project: "acme-lab-123", Zone: "us-central1-a",
		Machines: []MachineSpec{{Name: "prod-db", Type: "e2-standard-4", SSHKey: pub, AllowFrom: []string{"198.51.100.7/32"}}},
		Buckets:  []BucketSpec{{Name: "acme-prod-data"}}}
}

func approveAndApply(t *testing.T, store *Store, plan Plan, runner Runner) (Plan, error) {
	t.Helper()
	if err := store.Propose(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(plan.ID, true, "you", "", testKey); err != nil {
		t.Fatal(err)
	}
	return store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner})
}

func ranLines(r *fakeRunner) string {
	var out []string
	for _, argv := range r.ran {
		out = append(out, strings.Join(argv, " "))
	}
	return strings.Join(out, "\n")
}

// A plan that failed before creating a machine or bucket must never lead
// to removing a machine or bucket of that name: it may be someone else's.
func TestFailedCreateNeverRemovesWhatItDidNotMake(t *testing.T) {
	store := Open(t.TempDir())
	plan, err := BuildCreatePlan(gcpSpec(), "agent main")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{fail: map[string]string{"gcloud compute firewall-rules create": "ERROR: Quota 'FIREWALLS' exceeded"}}
	if _, err := approveAndApply(t, store, plan, runner); err == nil {
		t.Fatal("the plan did not fail")
	}
	resources, _ := store.Resources()
	destroy, err := BuildDestroyPlan(resources, true, "agent main", "")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, s := range destroy.Steps {
		lines = append(lines, strings.Join(s.Argv, " "))
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "instances delete prod-db") || strings.Contains(joined, "gs://acme-prod-data") {
		t.Fatalf("would remove things NullBot never made:\n%s", joined)
	}
	if !strings.Contains(joined, "instances list --filter labels.nullbot-id=nb-") {
		t.Fatalf("does not look for the machine by its label:\n%s", joined)
	}
	// Nothing found: nothing removed, and the inventory is cleared.
	runner = &fakeRunner{answers: map[string]string{"gcloud compute instances list": "[]", "gcloud compute firewall-rules list": "[]"}}
	if _, err := approveAndApply(t, store, destroy, runner); err != nil {
		t.Fatal(err)
	}
	if ran := ranLines(runner); strings.Contains(ran, " delete ") {
		t.Fatalf("deleted something:\n%s", ran)
	}
	resources, _ = store.Resources()
	for _, r := range resources {
		if r.State != StateDeleted {
			t.Errorf("%s is %s", r.Name, r.State)
		}
	}
}

// A machine whose id was never recorded (the run was cut off) is found by
// NullBot's tag and removed, instead of being forgotten while it bills.
func TestUnrecordedMachineIsFoundByTag(t *testing.T) {
	store := Open(t.TempDir())
	spec := awsSpec()
	spec.Buckets = nil
	plan, _ := BuildCreatePlan(spec, "main")
	runner := &fakeRunner{answers: map[string]string{
		"aws ec2 import-key-pair":       `{"KeyPairId": "key-0abc"}`,
		"aws ec2 create-security-group": `{"GroupId": "sg-0abc"}`,
		"aws ec2 run-instances":         `not json: the CLI was cut off`,
	}}
	if _, err := approveAndApply(t, store, plan, runner); err == nil {
		t.Fatal("the plan did not fail")
	}
	resources, _ := store.Resources()
	destroy, _ := BuildDestroyPlan(resources, false, "main", "")
	runner = &fakeRunner{answers: map[string]string{"aws ec2 describe-instances --filters Name=tag:nullbot-id": `["i-0orphan"]`}}
	if _, err := approveAndApply(t, store, destroy, runner); err != nil {
		t.Fatal(err)
	}
	if ran := ranLines(runner); !strings.Contains(ran, "terminate-instances --instance-ids i-0orphan") {
		t.Fatalf("the machine was not removed:\n%s", ran)
	}
}

// In us-east-1, creating a bucket you already own succeeds; NullBot checks
// first, so it never takes one over.
func TestExistingBucketIsNotAdopted(t *testing.T) {
	store := Open(t.TempDir())
	spec := Spec{Cloud: AWS, Credential: "aws", Region: "us-east-1", Buckets: []BucketSpec{{Name: "acme-prod-data"}}}
	plan, _ := BuildCreatePlan(spec, "agent main")
	runner := &fakeRunner{}
	done, err := approveAndApply(t, store, plan, runner)
	if err == nil || !strings.Contains(err.Error(), "already exists") || done.State != PlanFailed {
		t.Fatalf("%v %s", err, done.State)
	}
	if ran := ranLines(runner); strings.Contains(ran, "create-bucket") || strings.Contains(ran, "put-bucket-tagging") {
		t.Fatalf("touched the bucket:\n%s", ran)
	}
	resources, _ := store.Resources()
	destroy, _ := BuildDestroyPlan(resources, true, "agent main", "")
	if len(destroy.Steps) != 1 || len(destroy.Steps[0].Argv) != 0 {
		t.Fatalf("would remove it: %+v", destroy.Steps)
	}
}

// Before removing a bucket, NullBot checks it still carries its tag.
func TestRemovalChecksTheTag(t *testing.T) {
	store := Open(t.TempDir())
	r := Resource{ID: "nb-11111111", Cloud: AWS, Kind: KindBucket, Name: "acme-data", Region: "us-east-1", CloudID: "acme-data", State: StateReady, PlanID: "plan-0", Credential: "aws"}
	_ = store.UpdateResources(func([]Resource) []Resource { return []Resource{r} })
	destroy, _ := BuildDestroyPlan([]Resource{r}, true, "you", "")
	runner := &fakeRunner{answers: map[string]string{"aws s3api get-bucket-tagging": `{"TagSet": [{"Key": "team", "Value": "data"}]}`}}
	if _, err := approveAndApply(t, store, destroy, runner); err == nil || !strings.Contains(err.Error(), "does not carry NullBot's tag") {
		t.Fatalf("removed an untagged bucket: %v", err)
	}
	if strings.Contains(ranLines(runner), "s3 rb") {
		t.Fatal("ran the removal")
	}
}

// A plan changed on disk after approval does not run, and approving a plan
// that changed since it was shown fails.
func TestApprovalCoversExactlyWhatWasShown(t *testing.T) {
	store := Open(t.TempDir())
	plan, _ := BuildCreatePlan(awsSpec(), "agent main")
	_ = store.Propose(plan)
	shown, _ := store.Plan(plan.ID)
	path := filepath.Join(store.Dir(), "plans", plan.ID+".json")
	edit := func(fn func(*Plan)) {
		data, _ := os.ReadFile(path)
		var p Plan
		_ = json.Unmarshal(data, &p)
		fn(&p)
		data, _ = json.Marshal(p)
		_ = os.WriteFile(path, data, 0o600)
	}
	edit(func(p *Plan) { p.Steps[0].Argv = []string{"aws", "iam", "create-access-key"} })
	if _, err := store.Decide(plan.ID, true, "you", shown.Hash, testKey); err == nil || !strings.Contains(err.Error(), "changed since you looked") {
		t.Fatalf("approved a changed plan: %v", err)
	}
	now, _ := store.Plan(plan.ID)
	if _, err := store.Decide(plan.ID, true, "you", now.Hash, testKey); err != nil {
		t.Fatal(err)
	}
	edit(func(p *Plan) { p.Steps[1].Argv = []string{"aws", "iam", "create-user", "--user-name", "x"} })
	runner := &fakeRunner{}
	done, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "does not match what was approved") || len(runner.ran) != 0 || done.State != PlanFailed {
		t.Fatalf("ran a changed plan: %v %v", err, runner.ran)
	}
}

// Writing a plan file as approved does not make it run: the seal needs the
// key from the vault.
func TestApprovalCannotBeForgedInTheFile(t *testing.T) {
	store := Open(t.TempDir())
	plan, _ := BuildCreatePlan(awsSpec(), "agent main")
	// Approved with a seal made with another key.
	plan.State, plan.DecidedBy = PlanApproved, "you"
	plan.Approval = plan.Seal([]byte("a key the agent made up"), "you")
	_ = store.SavePlan(plan)
	runner := &fakeRunner{}
	if _, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner}); err == nil || len(runner.ran) != 0 {
		t.Fatalf("a forged approval ran: %v", err)
	}
	// Approved without any seal: it goes back to waiting for a person.
	plan, _ = BuildCreatePlan(awsSpec(), "agent main")
	plan.State, plan.DecidedBy = PlanApproved, "you"
	_ = store.SavePlan(plan)
	if _, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner}); err == nil || len(runner.ran) != 0 {
		t.Fatalf("an unsealed approval ran: %v", err)
	}
	if p, _ := store.Plan(plan.ID); p.State != PlanProposed {
		t.Fatalf("unsealed plan is %s", p.State)
	}
	// Changing who approved it breaks the seal too.
	plan, _ = BuildCreatePlan(awsSpec(), "agent main")
	_ = store.Propose(plan)
	_, _ = store.Decide(plan.ID, true, "you", "", testKey)
	p, _ := store.Plan(plan.ID)
	p.DecidedBy = "someone else"
	_ = store.SavePlan(p)
	if _, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner}); err == nil || len(runner.ran) != 0 {
		t.Fatalf("a re-attributed approval ran: %v", err)
	}
	if _, err := store.Decide(plan.ID, true, "you", "", nil); err == nil {
		t.Fatal("approved without a key")
	}
}

// A removal that failed partway can be tried again; its resources go back
// to how they were.
func TestFailedRemovalCanBeRetried(t *testing.T) {
	store := Open(t.TempDir())
	r := Resource{ID: "nb-22222222", Cloud: AWS, Kind: KindInstance, Name: "gpu", Region: "us-east-1", CloudID: "i-0a404b", State: StateReady, PlanID: "plan-0", Credential: "aws"}
	_ = store.UpdateResources(func([]Resource) []Resource { return []Resource{r} })
	destroy, _ := BuildDestroyPlan([]Resource{r}, false, "you", "")
	// An error that mentions 404 (in an id) is not "already gone".
	runner := &fakeRunner{fail: map[string]string{"aws ec2 terminate-instances": "An error occurred (UnauthorizedOperation) when calling the TerminateInstances operation: account 404404404404 is not allowed"}}
	if _, err := approveAndApply(t, store, destroy, runner); err == nil {
		t.Fatal("an unauthorized removal counted as done")
	}
	resources, _ := store.Resources()
	if resources[0].State != StateReady {
		t.Fatalf("state after a failed removal: %s", resources[0].State)
	}
	if _, err := BuildDestroyPlan(resources, false, "you", ""); err != nil {
		t.Fatalf("cannot try again: %v", err)
	}
}

func TestNotFoundMeansNotFound(t *testing.T) {
	for _, text := range []string{
		"An error occurred (InvalidInstanceID.NotFound) when calling the TerminateInstances operation: The instance ID 'i-1' does not exist",
		"remove_bucket failed: s3://x An error occurred (NoSuchBucket) when calling the DeleteBucket operation: The specified bucket does not exist",
		"An error occurred (404) when calling the HeadBucket operation: Not Found",
		"ERROR: (gcloud.compute.instances.delete) Could not fetch resource:\n - The resource 'projects/p/zones/z/instances/x' was not found",
		"ERROR: (gcloud.storage.buckets.delete) gs://x not found: 404.",
		"ERROR: (gcloud.storage.rm) The following URLs matched no objects or files:\ngs://x/**",
	} {
		if !notFound(text) {
			t.Errorf("not counted as missing: %s", text)
		}
	}
	for _, text := range []string{
		"An error occurred (DependencyViolation) when calling the DeleteSecurityGroup operation: resource sg-0a404 has a dependent object",
		"An error occurred (UnauthorizedOperation): account 404404404404",
		"An error occurred (InvalidParameterValue): the key pair does not exist in the request format",
		"NotFound",
	} {
		if notFound(text) {
			t.Errorf("counted as missing: %s", text)
		}
	}
}

func TestStaleRunsAreRecovered(t *testing.T) {
	store := Open(t.TempDir())
	plan, _ := BuildCreatePlan(awsSpec(), "main")
	_ = store.Propose(plan)
	_, _ = store.Decide(plan.ID, true, "you", "", testKey)
	p, _ := store.Plan(plan.ID)
	p.State, p.RunnerPID, p.RunnerHost = PlanApplying, 999999, hostName()
	p.Steps[0].State = "running"
	_ = store.SavePlan(p)
	_ = store.UpdateResources(func([]Resource) []Resource {
		var out []Resource
		for _, r := range plan.Resources {
			r.State = StateCreating
			out = append(out, r)
		}
		return out
	})
	if err := store.RecoverStale(func(int) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Plan(plan.ID); p.State != PlanApplying {
		t.Fatal("a live run was stopped")
	}
	_ = store.RecoverStale(func(int) bool { return false })
	p, _ = store.Plan(plan.ID)
	if p.State != PlanFailed || p.Steps[0].State != "failed" || p.Steps[1].State != "skipped" {
		t.Fatalf("not recovered: %s %s %s", p.State, p.Steps[0].State, p.Steps[1].State)
	}
	resources, _ := store.Resources()
	for _, r := range resources {
		if r.State != StateFailed {
			t.Errorf("%s is %s", r.Name, r.State)
		}
	}
}

func TestTextAndValues(t *testing.T) {
	if got := CleanText("eval box\x1b[8m\x07 hidden‮\nnext\tline"); got != "eval box[8m hidden\nnext\tline" {
		t.Fatalf("CleanText %q", got)
	}
	for cidr, ok := range map[string]bool{"203.0.113.4/32": true, "10.0.0.0/16": true, "0.0.0.0/0": true, "203.0.113.4/0": false, "10.0.0.1/16": false, "::1/128": false, "1.2.3.4": false} {
		if ValidCIDR(cidr) != ok {
			t.Errorf("ValidCIDR(%s) = %v", cidr, !ok)
		}
	}
	if !strings.Contains(CIDRHint("203.0.113.4/0"), "203.0.113.4/32") {
		t.Fatal(CIDRHint("203.0.113.4/0"))
	}
	key, err := NormalizeSSHKey(pub + " & calc.exe")
	if err != nil || key != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey" {
		t.Fatalf("%q %v", key, err)
	}
	if !strings.HasPrefix(SSHKeyFingerprint(key), "SHA256:") {
		t.Fatal(SSHKeyFingerprint(key))
	}
	for _, loc := range []string{"US", "us-central1", "nam4", "EU"} {
		if !ValidLocation(loc) {
			t.Errorf("%s refused", loc)
		}
	}
	for _, loc := range []string{"us&calc", "us central", "", "us-central1;x"} {
		if ValidLocation(loc) {
			t.Errorf("%q accepted", loc)
		}
	}
	spec := awsSpec()
	spec.Machines[0].AllowFrom = []string{"0.0.0.0/0"}
	plan, err := BuildCreatePlan(spec, "agent main")
	if err != nil || !strings.Contains(strings.Join(plan.Notes, " "), "whole internet") {
		t.Fatalf("%v %v", err, plan.Notes)
	}
}

// Every first-boot script NullBot writes is valid bash.
func TestBootstrapScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, b := range []Bootstrap{
		{Ollama: true, OllamaModels: []string{"qwen2.5:32b", "hf.co/bartowski/Qwen2.5-7B-Instruct-GGUF:Q4_K_M"}, GPUDrivers: true, NullBot: true},
		{NullBot: true, NullBotVersion: "v0.5.0"},
		{Ollama: true, NullBot: true},
		{Packages: []string{"nginx", "htop"}},
	} {
		script, err := b.Script("gpu", "ubuntu", false)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%+v: %v\n%s\n%s", b, err, out, script)
		}
	}
}

var testKey = []byte("an approval key for tests, 32 b.")
