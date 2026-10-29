package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// fakeAWS is an aws command that records its arguments and environment and
// answers like the real one for the calls a plan makes.
func fakeAWS(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$*" >> "` + dir + `/calls"
env | grep -E '^AWS_' | sort > "` + dir + `/env"
case "$2" in
  import-key-pair) echo '{"KeyPairId":"key-0abc"}' ;;
  create-security-group) echo '{"GroupId":"sg-0abc"}' ;;
  run-instances) echo '{"Instances":[{"InstanceId":"i-0abc","PrivateIpAddress":"10.0.0.5"}]}' ;;
  describe-instances) echo '{"public_ip":"34.1.2.3","private_ip":"10.0.0.5","state":"running"}' ;;
  *) echo '{}' ;;
esac
`
	path := filepath.Join(dir, "aws")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NULLBOT_AWS_BIN", path)
	return dir
}

func TestInfraPlanApproveApply(t *testing.T) {
	dir := fakeAWS(t)
	t.Setenv("AWS_PROFILE", "personal")
	config := projectTestConfig(t, "read-only")
	config.Infra.AgentTools = true
	a := New(config)
	if _, err := SaveSecret(config, SecretInput{Name: "aws-lab", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIAINFRATEST0000000", "secret_access_key": "infra/secret/key/000000000000000000000"}, Public: map[string]string{"region": "eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GenerateSSHKey("deploy", "", "", false); err != nil {
		t.Fatal(err)
	}
	old := publicIPFunc
	publicIPFunc = func(context.Context) (string, error) { return "198.51.100.7", nil }
	defer func() { publicIPFunc = old }()

	var plan, apply, status func(context.Context, map[string]any) (string, error)
	for _, tool := range BuiltinTools(a.Config(), a) {
		switch tool.Definition().Name {
		case "infra_plan":
			plan = tool.Call
		case "infra_apply":
			apply = tool.Call
		case "infra_status":
			status = tool.Call
		}
	}
	if plan == nil || apply == nil || status == nil {
		t.Fatal("infra tools missing")
	}
	out, err := plan(context.Background(), map[string]any{
		"cloud": "aws", "credential": "aws-lab", "ssh_key_secret": "deploy", "reason": "eval box",
		"machines": []any{map[string]any{"name": "eval", "type": "t3.medium", "allow_from": []any{"my-ip"}, "ports": []any{8080}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "198.51.100.7/32") || !strings.Contains(out, "--region eu-west-1") || strings.Contains(out, "AKIA") {
		t.Fatalf("plan:\n%s", out)
	}
	st, _ := a.InfraState()
	id := st.Plans[0].ID
	// Not approved: nothing runs, and the agent is told it is waiting.
	if out, err := apply(context.Background(), map[string]any{"plan": id, "wait_minutes": 0}); err != nil || !strings.Contains(out, "Still waiting") {
		t.Fatalf("unapproved apply: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); err == nil {
		t.Fatal("a command ran before approval")
	}
	// Approved while the agent waits.
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = a.DecideInfraPlan(id, true, "you")
	}()
	out, err = apply(context.Background(), map[string]any{"plan": id, "wait_minutes": 1})
	if err != nil || !strings.Contains(out, `"state": "applied"`) {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if !strings.Contains(string(env), "AWS_ACCESS_KEY_ID=AKIAINFRATEST0000000") || strings.Contains(string(env), "AWS_PROFILE") || !strings.Contains(string(env), "AWS_CONFIG_FILE=") {
		t.Fatalf("environment:\n%s", env)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "--security-group-ids sg-0abc") {
		t.Fatalf("calls:\n%s", calls)
	}
	out, _ = status(context.Background(), nil)
	if !strings.Contains(out, "34.1.2.3") || !strings.Contains(out, `"state": "ready"`) {
		t.Fatalf("status:\n%s", out)
	}
	// The ledger has the commands, never the secret.
	ledger, _ := a.InfraLedger(id, 0)
	for _, e := range ledger {
		if strings.Contains(strings.Join(e.Argv, " ")+e.Output, "infra/secret/key") {
			t.Fatal("secret in the ledger")
		}
	}
	// Removing it is a plan of its own.
	destroy, err := a.PlanInfraDestroy([]string{id}, false, "main", "done")
	if err != nil || destroy.Kind != "destroy" || destroy.State != infra.PlanProposed {
		t.Fatalf("%v %+v", err, destroy)
	}
}

func TestInfraToolsOffByDefault(t *testing.T) {
	a := New(projectTestConfig(t, "full"))
	for _, tool := range BuiltinTools(a.Config(), a) {
		if strings.HasPrefix(tool.Definition().Name, "infra_") {
			t.Fatalf("%s present while off", tool.Definition().Name)
		}
	}
}

func TestInfraRefusesWrongCredentials(t *testing.T) {
	a := New(projectTestConfig(t, "full"))
	if _, err := SaveSecret(a.Config(), SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_infra000000000000"}}); err != nil {
		t.Fatal(err)
	}
	_, err := a.PlanInfra(context.Background(), InfraRequest{Spec: infra.Spec{Cloud: infra.AWS, Credential: "gh", Buckets: []infra.BucketSpec{{Name: "acme-x"}}}}, "main")
	if err == nil || !strings.Contains(err.Error(), "not aws credentials") {
		t.Fatalf("%v", err)
	}
}
