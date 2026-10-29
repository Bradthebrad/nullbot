package infra

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey nullbot"

func awsSpec() Spec {
	return Spec{Cloud: AWS, Credential: "aws-prod", Region: "eu-west-1", Reason: "a GPU box for the eval run",
		Machines: []MachineSpec{{Name: "gpu-box", Type: "g6.xlarge", DiskGB: 100, SSHKey: pub, Ports: []int{443}, AllowFrom: []string{"203.0.113.4/32"}, UserData: "#!/bin/bash\necho hi\n"}},
		Buckets:  []BucketSpec{{Name: "acme-eval-scratch"}}}
}

func TestSpecValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"no cloud":       func(s *Spec) { s.Cloud = "azure" },
		"no credential":  func(s *Spec) { s.Credential = "" },
		"open to all":    func(s *Spec) { s.Machines[0].AllowFrom = nil },
		"bad cidr":       func(s *Spec) { s.Machines[0].AllowFrom = []string{"0.0.0.0/33"} },
		"bad name":       func(s *Spec) { s.Machines[0].Name = "GPU box" },
		"bad type":       func(s *Spec) { s.Machines[0].Type = "g6.xlarge; rm -rf /" },
		"bad key":        func(s *Spec) { s.Machines[0].SSHKey = "-----BEGIN PRIVATE KEY-----" },
		"key body":       func(s *Spec) { s.Machines[0].SSHKey = "ssh-ed25519 !!!!" },
		"key mismatch":   func(s *Spec) { s.Machines[0].SSHKey = "ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAITestKey" },
		"host bits":      func(s *Spec) { s.Machines[0].AllowFrom = []string{"203.0.113.4/0"} },
		"bad port":       func(s *Spec) { s.Machines[0].Ports = []int{70000} },
		"bad bucket":     func(s *Spec) { s.Buckets[0].Name = "Bad_Bucket" },
		"gcp no project": func(s *Spec) { s.Cloud = GCP; s.Region = ""; s.Project = "" },
		"bad region":     func(s *Spec) { s.Region = "eu west" },
		"huge disk":      func(s *Spec) { s.Machines[0].DiskGB = 100000 },
	} {
		spec := awsSpec()
		mutate(&spec)
		if _, err := BuildCreatePlan(spec, "agent"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAWSCreatePlan(t *testing.T) {
	plan, err := BuildCreatePlan(awsSpec(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != PlanProposed || plan.Kind != "create" || len(plan.Resources) != 4 || plan.CostHourly < 0.8 {
		t.Fatalf("plan %+v", plan)
	}
	joined := ""
	for _, s := range plan.Steps {
		joined += strings.Join(s.Argv, " ") + "\n"
	}
	for _, want := range []string{
		"aws ec2 import-key-pair --key-name nb-gpu-box",
		"aws ec2 create-security-group --group-name nb-gpu-box",
		"IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=203.0.113.4/32}]",
		"IpProtocol=tcp,FromPort=443,ToPort=443",
		"--instance-type g6.xlarge",
		"resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id",
		"HttpTokens=required",
		"Encrypted=true",
		"--user-data file://{{input_file}}",
		"{Key=nullbot-managed,Value=true}",
		"aws ec2 wait instance-running",
		"aws s3api create-bucket --bucket acme-eval-scratch --region eu-west-1",
		"LocationConstraint=eu-west-1",
		"--region eu-west-1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "0.0.0.0/0") {
		t.Fatal("opened to everyone")
	}
	if !armType("t4g.small") || !armType("c7gd.large") || armType("g6.xlarge") || armType("t3.micro") {
		t.Fatal("armType")
	}
}

func TestGCPCreatePlan(t *testing.T) {
	spec := Spec{Cloud: GCP, Credential: "gcp", Project: "acme-lab-123", Zone: "us-central1-a",
		Machines: []MachineSpec{{Name: "gpu", Type: "g2-standard-4", SSHKey: pub, AllowFrom: []string{"198.51.100.0/24"}, Spot: true}},
		Buckets:  []BucketSpec{{Name: "acme-lab-scratch"}}}
	plan, err := BuildCreatePlan(spec, "main")
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, s := range plan.Steps {
		joined += strings.Join(s.Argv, " ") + "\n"
	}
	for _, want := range []string{
		"gcloud compute firewall-rules create nb-", "--source-ranges 198.51.100.0/24", "--allow tcp:22",
		"gcloud compute instances create gpu --zone us-central1-a --machine-type g2-standard-4",
		"--image-family ubuntu-2404-lts-amd64", "--maintenance-policy TERMINATE", "--provisioning-model SPOT",
		"--labels nullbot-managed=true,nullbot-id=nb-", "--metadata block-project-ssh-keys=TRUE,ssh-keys=nullbot:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey --project",
		"--no-service-account --no-scopes", "--action DENY --rules all --source-ranges 0.0.0.0/0 --priority 1000", "--priority 900",
		"gcloud storage buckets create gs://acme-lab-scratch --location us-central1", "--public-access-prevention",
		"--project acme-lab-123",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan lacks %q:\n%s", want, joined)
		}
	}
	if len(plan.Resources) != 4 {
		t.Fatalf("resources %+v", plan.Resources)
	}
	for _, r := range plan.Resources {
		if r.CloudID != "" {
			t.Fatalf("%s has a cloud id before it exists", r.Name)
		}
	}
}

// fakeRunner answers commands from a table of prefixes.
type fakeRunner struct {
	answers map[string]string
	fail    map[string]string
	ran     [][]string
}

func (f *fakeRunner) Run(_ context.Context, argv []string) (string, string, error) {
	f.ran = append(f.ran, argv)
	line := strings.Join(argv, " ")
	for prefix, stderr := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return "", stderr, errors.New("exit status 254")
		}
	}
	for prefix, out := range f.answers {
		if strings.HasPrefix(line, prefix) {
			return out, "", nil
		}
	}
	return "{}", "", nil
}

func TestApplyNeedsApprovalThenRunsAndTearsDown(t *testing.T) {
	store := Open(t.TempDir())
	plan, _ := BuildCreatePlan(awsSpec(), "main")
	if err := store.Propose(plan); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{answers: map[string]string{
		"aws ec2 import-key-pair":       `{"KeyPairId": "key-0abc"}`,
		"aws ec2 create-security-group": `{"GroupId": "sg-0abc"}`,
		"aws ec2 run-instances":         `{"Instances": [{"InstanceId": "i-0abc", "PrivateIpAddress": "10.0.0.5"}]}`,
		"aws ec2 describe-instances":    `{"public_ip": "34.1.2.3", "private_ip": "10.0.0.5", "state": "running"}`,
	}, fail: map[string]string{"aws s3api head-bucket": headBucket404}}
	if _, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner}); err == nil || !strings.Contains(err.Error(), "waiting for approval") {
		t.Fatalf("ran unapproved: %v", err)
	}
	if _, err := store.Decide(plan.ID, true, "you", "", testKey); err != nil {
		t.Fatal(err)
	}
	var userData string
	runner.answers["aws ec2 run-instances"] = `{"Instances": [{"InstanceId": "i-0abc", "PrivateIpAddress": "10.0.0.5"}]}`
	done, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runnerFunc(func(argv []string) (string, string, error) {
		for i, a := range argv {
			if a == "--user-data" {
				data, _ := os.ReadFile(strings.TrimPrefix(argv[i+1], "file://"))
				userData = string(data)
			}
			if strings.Contains(a, "{{") {
				t.Errorf("unresolved placeholder in %v", argv)
			}
		}
		return runner.Run(context.Background(), argv)
	}), Redact: func(s string) string { return strings.ReplaceAll(s, "34.1.2.3", "34.1.2.3") }})
	if err != nil || done.State != PlanApplied {
		t.Fatalf("apply: %v %+v", err, done)
	}
	if userData != "#!/bin/bash\necho hi\n" {
		t.Fatalf("user data %q", userData)
	}
	resources, _ := store.Resources()
	var inst Resource
	for _, r := range resources {
		if r.Kind == KindInstance {
			inst = r
		}
		if r.State != StateReady {
			t.Errorf("%s is %s", r.Name, r.State)
		}
	}
	if inst.CloudID != "i-0abc" || inst.Details["public_ip"] != "34.1.2.3" {
		t.Fatalf("instance %+v", inst)
	}
	// The security group step got the real group id.
	found := false
	for _, argv := range runner.ran {
		if strings.Join(argv[:3], " ") == "aws ec2 run-instances" && strings.Contains(strings.Join(argv, " "), "--security-group-ids sg-0abc") {
			found = true
		}
	}
	if !found {
		t.Fatal("placeholder not filled")
	}
	ledger, _ := store.Ledger(plan.ID, 0)
	if len(ledger) < 8 || ledger[0].Event != "proposed" || ledger[1].Event != "approved" || ledger[len(ledger)-1].Event != "applied" {
		t.Fatalf("ledger %+v", ledger)
	}

	// Tear it all down; the key pair is already gone.
	destroy, err := BuildDestroyPlan(resources, false, "main", "done with it")
	if err != nil {
		t.Fatal(err)
	}
	if destroy.Steps[0].Argv[2] != "terminate-instances" {
		t.Fatalf("machine not first: %v", destroy.Steps[0].Argv)
	}
	_ = store.Propose(destroy)
	_, _ = store.Decide(destroy.ID, true, "you", "", testKey)
	runner.fail = map[string]string{"aws ec2 delete-key-pair": "An error occurred (InvalidKeyPair.NotFound) when calling the DeleteKeyPair operation"}
	runner.answers["aws s3api get-bucket-tagging"] = `{"TagSet": [{"Key": "nullbot-id", "Value": "` + bucketID(resources) + `"}]}`
	if _, err := store.Apply(context.Background(), destroy.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner}); err != nil {
		t.Fatal(err)
	}
	resources, _ = store.Resources()
	for _, r := range resources {
		if r.State != StateDeleted {
			t.Errorf("%s is %s after removal", r.Name, r.State)
		}
	}
	if _, err := BuildDestroyPlan(resources, false, "main", ""); err == nil {
		t.Fatal("planned to remove what is gone")
	}
}

const headBucket404 = "An error occurred (404) when calling the HeadBucket operation: Not Found"

func bucketID(resources []Resource) string {
	for _, r := range resources {
		if r.Kind == KindBucket {
			return r.ID
		}
	}
	return ""
}

type runnerFunc func([]string) (string, string, error)

func (f runnerFunc) Run(_ context.Context, argv []string) (string, string, error) { return f(argv) }

func TestApplyFailureLeavesARecord(t *testing.T) {
	store := Open(t.TempDir())
	plan, _ := BuildCreatePlan(awsSpec(), "main")
	_ = store.Propose(plan)
	if _, err := store.Decide(plan.ID, false, "you", "", testKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(plan.ID, true, "you", "", testKey); err == nil {
		t.Fatal("a rejected plan was approved")
	}
	plan, _ = BuildCreatePlan(awsSpec(), "main")
	_ = store.Propose(plan)
	_, _ = store.Decide(plan.ID, true, "you", "", testKey)
	runner := &fakeRunner{answers: map[string]string{
		"aws ec2 import-key-pair":       `{"KeyPairId": "key-0abc"}`,
		"aws ec2 create-security-group": `{"GroupId": "sg-0abc"}`,
	}, fail: map[string]string{"aws ec2 run-instances": "An error occurred (InsufficientInstanceCapacity): no g6.xlarge capacity", "aws s3api head-bucket": headBucket404}}
	done, err := store.Apply(context.Background(), plan.ID, ApplyOptions{ApprovalKey: testKey, Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "InsufficientInstanceCapacity") || done.State != PlanFailed {
		t.Fatalf("%v %+v", err, done.State)
	}
	resources, _ := store.Resources()
	states := map[string]string{}
	for _, r := range resources {
		states[r.Kind] = r.State + ":" + r.CloudID
	}
	if states[KindSecurityGroup] != "failed:sg-0abc" || states[KindKeyPair] != "failed:key-0abc" {
		t.Fatalf("states %v", states)
	}
	// What was made can still be removed.
	destroy, err := BuildDestroyPlan(resources, false, "main", "")
	if err != nil || len(destroy.Steps) < 3 {
		t.Fatalf("%v %+v", err, destroy)
	}
}

func TestBootstrapScript(t *testing.T) {
	b := Bootstrap{Ollama: true, OllamaModels: []string{"qwen2.5:7b"}, GPUDrivers: true, NullBot: true, NullBotVersion: "v0.5.0"}
	script, err := b.Script("gpu", "ubuntu", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#!/bin/bash", "ubuntu-drivers install --gpgpu", "reboot", "OLLAMA_HOST=127.0.0.1:11434", "OLLAMA_CONTEXT_LENGTH=16384", "ollama pull qwen2.5:7b",
		"releases/download/v0.5.0/nullbot-linux-amd64", "sha256sum -c -", "models use local:ollama/qwen2.5:7b", "ExecStart=/usr/local/bin/nullbot serve", BootstrapDone} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for _, bad := range []Bootstrap{
		{Ollama: true, OllamaModels: []string{"x; rm -rf /"}},
		{Packages: []string{"curl && wget evil"}},
		{NullBot: true, NullBotVersion: "latest; id"},
		{OllamaModels: []string{"qwen"}},
	} {
		if _, err := bad.Script("m", "ubuntu", false); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	spec := awsSpec()
	spec.Machines[0].UserData = ""
	spec.Machines[0].Bootstrap = &Bootstrap{Ollama: true, OllamaModels: []string{"qwen2.5:7b"}}
	spec.Machines[0].DiskGB = 30
	plan, err := BuildCreatePlan(spec, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(strings.Join(s.Argv, " "), "run-instances") && (!strings.Contains(s.Input, "ollama pull qwen2.5:7b") || !strings.Contains(strings.Join(s.Argv, " "), "VolumeSize=60")) {
			t.Fatalf("bootstrap not in the plan: %v", s.Argv)
		}
	}
}
