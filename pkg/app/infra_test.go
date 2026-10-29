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
	args := map[string]any{
		"cloud": "aws", "credential": "aws-lab", "ssh_key_secret": "deploy", "reason": "eval box",
		"machines": []any{map[string]any{"name": "eval", "type": "t3.medium", "allow_from": []any{"my-ip"}, "ports": []any{8080}}},
	}
	// Credentials not allowed for agents are refused, as for secret_run.
	if _, err := plan(context.Background(), args); err == nil || !strings.Contains(err.Error(), "not allowed for agents") {
		t.Fatalf("an agent used credentials it may not: %v", err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "aws-lab", Kind: SecretAWS, AgentUse: true, Public: map[string]string{"region": "eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	// An agent cannot bring an SSH key of its own.
	own := map[string]any{"cloud": "aws", "credential": "aws-lab", "machines": []any{map[string]any{"name": "x", "type": "t3.micro", "allow_from": []any{"198.51.100.7/32"}, "ssh_public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey"}}}
	if _, err := plan(context.Background(), own); err == nil || !strings.Contains(err.Error(), "ssh_key_secret") {
		t.Fatalf("an agent's own key was accepted: %v", err)
	}
	out, err := plan(context.Background(), args)
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
		_, _ = a.DecideInfraPlan(id, true, "you", "")
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
	// The user approves and runs it while the agent waits on it: whichever
	// starts first runs it, and the agent reports that run, not a failure.
	_, _ = a.DecideInfraPlan(destroy.ID, true, "you", "")
	userDone := make(chan struct{})
	go func() {
		defer close(userDone)
		_, _ = a.ApplyInfraPlan(context.Background(), destroy.ID, "you")
	}()
	out, err = apply(context.Background(), map[string]any{"plan": destroy.ID, "wait_minutes": 1})
	<-userDone
	if err != nil || strings.Contains(out, "failed step") || !strings.Contains(out, `"state": "applied"`) {
		t.Fatalf("agent alongside the user: %v\n%s", err, out)
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

func TestParsePortListings(t *testing.T) {
	l, _ := parseSSLine(`LISTEN 0      4096       127.0.0.1:11434      0.0.0.0:*    users:(("ollama",pid=812,fd=3))`)
	if l.Port != 11434 || l.Address != "127.0.0.1" || l.Process != "ollama" || l.PID != 812 || l.Public || l.Known != "Ollama" {
		t.Fatalf("ss: %+v", l)
	}
	lsof := parseLsof("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nnginx 99 root 6u IPv4 0x1 0t0 TCP *:443 (LISTEN)\n")
	if len(lsof) != 1 || lsof[0].Port != 443 || !lsof[0].Public || lsof[0].Known != "nginx" {
		t.Fatalf("lsof: %+v", lsof)
	}
	netstat := parseNetstat("  TCP    0.0.0.0:22     0.0.0.0:0   LISTENING   1100\r\n  TCP    127.0.0.1:1234  0.0.0.0:0   LISTENING   2200\r\n  TCP    10.0.0.2:50000  1.2.3.4:443  ESTABLISHED 3\r\n", map[int]string{2200: "LM Studio"})
	if len(netstat) != 2 || netstat[1].Process != "LM Studio" || netstat[0].Known != "SSH" || !netstat[0].Public {
		t.Fatalf("netstat: %+v", netstat)
	}
}

func TestInspectNode(t *testing.T) {
	fakeAWS(t)
	bin := t.TempDir()
	probe := `##ports
LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
LISTEN 0 4096 [::]:22 [::]:* users:(("sshd",pid=1,fd=4))
LISTEN 0 4096 127.0.0.1:11434 0.0.0.0:* users:(("ollama",pid=812,fd=3))
LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=900,fd=6))
##services
nginx.service
ollama.service
nullbot.service
##nginx
    server {
        listen 443 ssl;
        server_name chat.example.com;
            proxy_pass http://127.0.0.1:3000;
##docker
web (acme/web:1.2) 0.0.0.0:3000->3000/tcp
##setup
done
##system
 10:00:00 up 2 days
`
	_ = os.WriteFile(filepath.Join(bin, "probe.txt"), []byte(probe), 0o644)
	_ = os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho \"$*\" > \""+bin+"/args\"\ncat >/dev/null\ncat \""+bin+"/probe.txt\"\n"), 0o755)
	t.Setenv("NULLBOT_SSH_BIN", filepath.Join(bin, "ssh"))
	config := projectTestConfig(t, "read-only")
	a := New(config)
	_, _ = SaveSecret(config, SecretInput{Name: "aws-lab", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIAINSPECT000000000", "secret_access_key": "inspect/secret/000000000000000000000"}})
	if _, err := a.GenerateSSHKey("deploy", "", "", false); err != nil {
		t.Fatal(err)
	}
	plan, err := a.PlanInfra(context.Background(), InfraRequest{Spec: infra.Spec{Cloud: infra.AWS, Credential: "aws-lab", Machines: []infra.MachineSpec{{Name: "web", Type: "t3.small", AllowFrom: []string{"203.0.113.1/32"}}}}, SSHKeySecret: "deploy"}, "you")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = a.DecideInfraPlan(plan.ID, true, "you", "")
	if _, err := a.ApplyInfraPlan(context.Background(), plan.ID, "you"); err != nil {
		t.Fatal(err)
	}
	report, err := a.InspectNode(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Listeners) != 3 || report.Listeners[0].Port != 22 || !report.Listeners[0].Public || report.Listeners[1].Port != 443 || report.Listeners[2].Known != "Ollama" || report.Listeners[2].Public {
		t.Fatalf("listeners %+v", report.Listeners)
	}
	if len(report.Nginx) != 1 || report.Nginx[0].ServerNames[0] != "chat.example.com" || report.Nginx[0].ProxyPass[0] != "http://127.0.0.1:3000" {
		t.Fatalf("nginx %+v", report.Nginx)
	}
	if report.Setup != "done" || len(report.Containers) != 1 || len(report.Services) != 3 {
		t.Fatalf("report %+v", report)
	}
	// Agents need the SSH key to be allowed for them.
	if _, err := a.inspectNode(context.Background(), "web", true); err == nil || !strings.Contains(err.Error(), "not allowed for agents") {
		t.Fatalf("an agent used a key it may not: %v", err)
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	if !strings.Contains(string(args), "-F none") || !strings.Contains(string(args), `UserKnownHostsFile="`) {
		t.Fatalf("ssh reads the user's configuration: %s", args)
	}
	if !strings.Contains(string(args), "-l ubuntu") || !strings.Contains(string(args), "34.1.2.3") || !strings.Contains(string(args), "StrictHostKeyChecking=accept-new") {
		t.Fatalf("ssh args %s", args)
	}
	// A tunnel through an ssh that fails says why.
	_ = os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0o755)
	if _, err := a.OpenTunnel("web", 11434, 0); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("tunnel: %v", err)
	}
	// Removing the machine by name takes its security group and key pair
	// along, and forgets its host key.
	known := filepath.Join(config.AppDir, "infra", "known_hosts")
	_ = os.WriteFile(known, []byte("34.1.2.3 ssh-ed25519 AAAAhost\n10.9.9.9 ssh-ed25519 AAAAother\n"), 0o600)
	destroy, err := a.PlanInfraDestroy([]string{"web"}, false, "you", "")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, r := range destroy.Resources {
		kinds[r.Kind] = true
	}
	if !kinds[infra.KindInstance] || !kinds[infra.KindSecurityGroup] || !kinds[infra.KindKeyPair] {
		t.Fatalf("removal leaves things behind: %+v", destroy.Resources)
	}
	_, _ = a.DecideInfraPlan(destroy.ID, true, "you", "")
	if _, err := a.ApplyInfraPlan(context.Background(), destroy.ID, "you"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(known); strings.Contains(string(data), "34.1.2.3") || !strings.Contains(string(data), "10.9.9.9") {
		t.Fatalf("known_hosts after removal: %q", data)
	}
}

func TestConnectingANodeNeverReplacesYourServer(t *testing.T) {
	config := Config{}
	if id, previous, err := nodeServerID(config, "gpu-box"); err != nil || id != "gpu-box" || previous != "" {
		t.Fatalf("first connect %q %q %v", id, previous, err)
	}
	config.LocalEndpoints = []LocalEndpoint{{ID: "gpu-box", Name: "gpu-box (cloud)", Kind: "ollama"}}
	if id, previous, err := nodeServerID(config, "gpu-box"); err != nil || id != "gpu-box" || previous != "gpu-box" {
		t.Fatalf("reconnect %q %q %v", id, previous, err)
	}
	// Your own server named like the machine stays; the tunnel gets its own.
	config.LocalEndpoints = []LocalEndpoint{{ID: "ollama", Kind: "ollama"}}
	if id, previous, err := nodeServerID(config, "Ollama"); err != nil || id != "ollama-cloud" || previous != "" {
		t.Fatalf("clash %q %q %v", id, previous, err)
	}
	config.LocalEndpoints = append(config.LocalEndpoints, LocalEndpoint{ID: "ollama-cloud", Kind: "vllm"})
	if _, _, err := nodeServerID(config, "Ollama"); err == nil {
		t.Fatal("a server was replaced")
	}
}

func TestBatchFilesGetOnlySafeArguments(t *testing.T) {
	if err := checkBatchArgs(`C:\gcloud\bin\gcloud.cmd`, []string{"compute", "instances", "create", "web", "--description", "NullBot plan-1 for web"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"us&calc", `a"b`, "x|y", "50%", "a^b", "line\nbreak"} {
		if err := checkBatchArgs(`C:\gcloud\bin\gcloud.CMD`, []string{bad}); err == nil {
			t.Errorf("%q passed to a batch file", bad)
		}
	}
	if err := checkBatchArgs("/usr/bin/gcloud", []string{"us&calc"}); err != nil {
		t.Fatal("a program (not a batch file) was checked")
	}
}
