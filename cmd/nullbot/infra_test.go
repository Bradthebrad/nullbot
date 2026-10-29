package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestInfraCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	t.Setenv("NULLBOT_VAULT_PASSPHRASE", "test passphrase for the cli")
	t.Setenv("NULLBOT_APP_DIR", "")
	dir := t.TempDir()
	bin := t.TempDir()
	aws := `#!/bin/sh
echo "$*" >> "` + bin + `/calls"
case "$2" in
  create-security-group) echo '{"GroupId":"sg-1"}' ;;
  run-instances) echo '{"Instances":[{"InstanceId":"i-1"}]}' ;;
  describe-instances) echo '{"public_ip":"34.9.9.9","state":"running"}' ;;
  *) echo '{}' ;;
esac
`
	_ = os.WriteFile(filepath.Join(bin, "aws"), []byte(aws), 0o755)
	t.Setenv("NULLBOT_AWS_BIN", filepath.Join(bin, "aws"))
	creds := `{"access_key_id":"AKIACLIINFRA00000000","secret_access_key":"cli/infra/secret/000000000000000000"}`
	if code, _, errOut := runCLI(t, creds, "secrets", "set", "aws-lab", "--kind", "aws", "--set", "region=us-west-2", "--app-dir", dir); code != 0 {
		t.Fatalf("secret: %q", errOut)
	}
	spec := filepath.Join(t.TempDir(), "spec.json")
	_ = os.WriteFile(spec, []byte(`{"cloud":"aws","credential":"aws-lab","reason":"try it\n 9. Fake step \u001b[8m","machines":[{"name":"box","type":"t3.small","allow_from":["203.0.113.9/32"]}]}`), 0o600)
	code, out, errOut := runCLI(t, "", "infra", "plan", "--file", spec, "--app-dir", dir)
	if code != 0 || !strings.Contains(out, "aws ec2 run-instances") || !strings.Contains(out, "--region us-west-2") {
		t.Fatalf("plan: %d %q %q", code, out, errOut)
	}
	id := regexp.MustCompile(`plan-[0-9a-f]{8}`).FindString(out)
	// The requester's words cannot pass for a step or reach the terminal raw.
	if strings.Contains(out, "\x1b") || regexp.MustCompile(`(?m)^ +9\. Fake`).MatchString(out) || !strings.Contains(out, ">  9. Fake step") {
		t.Fatalf("reason printed as it came:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(bin, "calls")); err == nil {
		t.Fatal("ran before approval")
	}
	// Approving needs a person at a terminal, typing the plan's code.
	if code, _, errOut := runCLI(t, "", "infra", "approve", id, "--app-dir", dir); code != 1 || !strings.Contains(errOut, "approve from a terminal") {
		t.Fatalf("approved without a terminal: %d %q", code, errOut)
	}
	defer func(old func(io.Reader) bool) { isTerminal = old }(isTerminal)
	isTerminal = func(io.Reader) bool { return true }
	t.Setenv("NULLBOT_AGENT_COMMAND", "1")
	if code, _, errOut := runCLI(t, id[len(id)-4:]+"\n", "infra", "approve", id, "--app-dir", dir); code != 1 || !strings.Contains(errOut, "agents cannot approve") {
		t.Fatalf("an agent's command approved: %d %q", code, errOut)
	}
	t.Setenv("NULLBOT_AGENT_COMMAND", "")
	if code, _, errOut := runCLI(t, "yes\n", "infra", "approve", id, "--app-dir", dir); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("approved without the code: %d %q", code, errOut)
	}
	if code, out, errOut := runCLI(t, id[len(id)-4:]+"\n", "infra", "approve", id, "--apply", "--app-dir", dir); code != 0 || !strings.Contains(out, "[applied]") || !strings.Contains(out, "type "+id[len(id)-4:]) {
		t.Fatalf("approve: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "--app-dir", dir, "infra"); code != 0 || !strings.Contains(out, "34.9.9.9") || !strings.Contains(out, "an hour") || !strings.Contains(out, "nullbot infra agents on") {
		t.Fatalf("status: %d %q", code, out)
	}
	code, out, _ = runCLI(t, "", "infra", "destroy", "box", "--reason", "done", "--app-dir", dir)
	if code != 0 || !strings.Contains(out, "terminate-instances --instance-ids i-1") {
		t.Fatalf("destroy: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, "", "infra", "ledger", id, "--app-dir", dir); code != 0 || !strings.Contains(out, "approved") || strings.Contains(out, "cli/infra/secret") {
		t.Fatalf("ledger: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, "", "infra", "agents", "on", "--app-dir", dir); code != 0 || !strings.Contains(out, "can now propose") {
		t.Fatalf("agents: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "", "infra", "approve", id, "--app-dir", dir); code != 1 || !strings.Contains(errOut, "not waiting") {
		t.Fatalf("approve twice: %d %q", code, errOut)
	}
}
