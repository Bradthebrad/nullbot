package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// Cloud infrastructure: plans of commands for the aws and gcloud tools, run
// only after a person approves them, with every command in a ledger and
// everything created in an inventory. See pkg/infra and docs/INFRA.md.

// InfraConfig controls cloud infrastructure.
type InfraConfig struct {
	// AgentTools lets agents propose plans (infra_plan, infra_destroy) and
	// run approved ones. Off by default. A plan never runs without a
	// person's approval either way.
	AgentTools bool `json:"agent_tools,omitempty"`
}

// ChangeInfra: a plan was proposed, decided or applied, or the inventory
// changed.
const ChangeInfra = "infra"

func infraStore(config Config) *infra.Store {
	return infra.Open(filepath.Join(config.AppDir, "infra"))
}

// infraCLIs are the command-line tools plans use, found on PATH or named by
// NULLBOT_AWS_BIN / NULLBOT_GCLOUD_BIN.
func infraCLI(name string) (string, error) {
	env := map[string]string{"aws": "NULLBOT_AWS_BIN", "gcloud": "NULLBOT_GCLOUD_BIN"}[name]
	if env == "" {
		return "", fmt.Errorf("plans run only aws and gcloud, not %s", name)
	}
	if path := strings.TrimSpace(os.Getenv(env)); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		install := map[string]string{
			"aws":    "the AWS CLI v2 (https://aws.amazon.com/cli/)",
			"gcloud": "the Google Cloud CLI (https://cloud.google.com/sdk/docs/install)",
		}[name]
		return "", fmt.Errorf("%s was not found; install %s, or set %s to its path", name, install, env)
	}
	return path, nil
}

func cliFor(cloud infra.Cloud) string {
	if cloud == infra.GCP {
		return "gcloud"
	}
	return "aws"
}

// infraCredential finds the vault secret a plan uses and checks its kind.
func infraCredential(config Config, cloud infra.Cloud, name string) (Secret, error) {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return Secret{}, fmt.Errorf("the vault could not be opened for the %s credentials: %w", cloud, err)
	}
	want := map[infra.Cloud]string{infra.AWS: SecretAWS, infra.GCP: SecretGCP}[cloud]
	for _, s := range payload.Secrets {
		if s.Name != name {
			continue
		}
		if s.Kind != want {
			return Secret{}, fmt.Errorf("secret %s is a %s, not %s credentials", name, s.Kind, cloud)
		}
		return s, nil
	}
	var names []string
	for _, s := range payload.Secrets {
		if s.Kind == want {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return Secret{}, fmt.Errorf("there are no %s credentials in the vault; add some in Settings > Secrets", cloud)
	}
	return Secret{}, fmt.Errorf("there is no secret %s; %s credentials in the vault: %s", name, cloud, strings.Join(names, ", "))
}

// gcpProject is the project a Google Cloud key file belongs to.
func gcpProject(s Secret) string {
	var key struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal([]byte(s.Values["credentials_json"]), &key)
	return key.ProjectID
}

// publicIPFunc finds this computer's public address, for "my-ip" in a
// plan; tests replace it.
var publicIPFunc = func(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://checkip.amazonaws.com", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("this computer's public address could not be found (%v); give allow_from as an address range", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(data))
	if !infra.ValidCIDR(ip + "/32") {
		return "", errors.New("this computer's public address could not be found; give allow_from as an address range")
	}
	return ip, nil
}

// InfraRequest is a spec as agents and people give it: SSH keys by vault
// secret name, and "my-ip" for this computer's address.
type InfraRequest struct {
	infra.Spec
	// SSHKeySecret names an SSH key secret whose public key goes on every
	// machine.
	SSHKeySecret string `json:"ssh_key_secret,omitempty"`
}

// PlanInfra checks a request and saves it as a plan waiting for approval.
func (a *App) PlanInfra(ctx context.Context, req InfraRequest, requester string) (infra.Plan, error) {
	config := a.Config()
	spec := req.Spec
	cred, err := infraCredential(config, spec.Cloud, spec.Credential)
	if err != nil {
		return infra.Plan{}, err
	}
	if spec.Cloud == infra.GCP && spec.Project == "" {
		spec.Project = gcpProject(cred)
	}
	if spec.Cloud == infra.AWS && spec.Region == "" {
		spec.Region = cred.Public["region"]
	}
	pub := ""
	if req.SSHKeySecret != "" {
		key, err := a.sshPublicKey(req.SSHKeySecret)
		if err != nil {
			return infra.Plan{}, err
		}
		pub = key
	}
	myIP := ""
	for i := range spec.Machines {
		m := &spec.Machines[i]
		if m.SSHKey == "" {
			m.SSHKey = pub
		}
		for j, from := range m.AllowFrom {
			if strings.EqualFold(from, "my-ip") || strings.EqualFold(from, "me") {
				if myIP == "" {
					if myIP, err = publicIPFunc(ctx); err != nil {
						return infra.Plan{}, err
					}
				}
				m.AllowFrom[j] = myIP + "/32"
			}
		}
	}
	plan, err := infra.BuildCreatePlan(spec, requester)
	if err != nil {
		return plan, err
	}
	if _, err := infraCLI(cliFor(plan.Cloud)); err != nil {
		plan.Notes = append(plan.Notes, err.Error())
	}
	if err := infraStore(config).Propose(plan); err != nil {
		return plan, err
	}
	a.logInfo("infrastructure plan proposed", "plan", plan.ID, "by", requester, "title", plan.Title)
	a.notifyChange(ChangeInfra)
	return plan, nil
}

func (a *App) sshPublicKey(name string) (string, error) {
	payload, err := loadSecretsPayload(a.Config())
	if err != nil {
		return "", err
	}
	for _, s := range payload.Secrets {
		if s.Name == name {
			if s.Kind != SecretSSHKey || s.Public["public_key"] == "" {
				return "", fmt.Errorf("secret %s is not an SSH key", name)
			}
			return s.Public["public_key"], nil
		}
	}
	return "", fmt.Errorf("there is no SSH key secret %s; make one with nullbot secrets ssh-keygen %s", name, name)
}

// PlanInfraDestroy saves a plan that removes resources, by NullBot id, or
// everything a plan created when given a plan id.
func (a *App) PlanInfraDestroy(ids []string, deleteContents bool, requester, reason string) (infra.Plan, error) {
	store := infraStore(a.Config())
	all, err := store.Resources()
	if err != nil {
		return infra.Plan{}, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var chosen []infra.Resource
	for _, r := range all {
		if want[r.ID] || want[r.PlanID] || want[r.Name] {
			chosen = append(chosen, r)
		}
	}
	if len(chosen) == 0 {
		return infra.Plan{}, errors.New("none of those are resources NullBot made; infra_status lists them")
	}
	plan, err := infra.BuildDestroyPlan(chosen, deleteContents, requester, reason)
	if err != nil {
		return plan, err
	}
	if err := store.Propose(plan); err != nil {
		return plan, err
	}
	a.notifyChange(ChangeInfra)
	return plan, nil
}

// DecideInfraPlan approves or rejects a plan. People only: the user
// interfaces and the command line call it; no agent tool does.
func (a *App) DecideInfraPlan(id string, approve bool, by string) (infra.Plan, error) {
	plan, err := infraStore(a.Config()).Decide(id, approve, by)
	if err == nil {
		a.logInfo("infrastructure plan decided", "plan", id, "approved", approve, "by", by)
		a.notifyChange(ChangeInfra)
		infraDecided.Broadcast()
	}
	return plan, err
}

// infraDecided wakes agents waiting on an approval.
var infraDecided = sync.NewCond(&sync.Mutex{})

// ApplyInfraPlan runs an approved plan.
func (a *App) ApplyInfraPlan(ctx context.Context, id, by string) (infra.Plan, error) {
	config := a.Config()
	store := infraStore(config)
	plan, err := store.Plan(id)
	if err != nil {
		return plan, err
	}
	cred, err := infraCredential(config, plan.Cloud, plan.Credential)
	if err != nil {
		return plan, err
	}
	runner, cleanup, err := newInfraRunner(config, plan, cred)
	if err != nil {
		return plan, err
	}
	defer cleanup()
	a.logInfo("infrastructure plan applying", "plan", id, "by", by)
	done, err := store.Apply(ctx, id, infra.ApplyOptions{
		Runner: runner, By: by, TempDir: runner.dir,
		Redact:   func(s string) string { return RedactSecrets(config, s) },
		Progress: func(infra.Plan) { a.notifyChange(ChangeInfra) },
	})
	if err != nil {
		a.logError("infrastructure plan failed", "plan", id, "error", err)
	} else {
		a.logInfo("infrastructure plan applied", "plan", id)
	}
	a.notifyChange(ChangeInfra)
	return done, err
}

// infraRunner runs aws and gcloud with a plan's credentials and nothing
// else: an environment without the user's own cloud configuration, and a
// private configuration folder for each run.
type infraRunner struct {
	config Config
	cloud  infra.Cloud
	env    []string
	dir    string
}

func newInfraRunner(config Config, plan infra.Plan, cred Secret) (*infraRunner, func(), error) {
	secretEnv, err := buildSecretEnv([]Secret{cred}, true)
	if err != nil {
		return nil, func() {}, err
	}
	dir, err := os.MkdirTemp("", "nullbot-infra-")
	if err != nil {
		secretEnv.cleanup()
		return nil, func() {}, err
	}
	_ = os.Chmod(dir, 0o700)
	cleanup := func() {
		secretEnv.cleanup()
		os.RemoveAll(dir)
	}
	env := commandBaseEnv()
	for k, v := range secretEnv.vars {
		env[k] = v
	}
	switch plan.Cloud {
	case infra.AWS:
		empty := filepath.Join(dir, "empty")
		if err := os.WriteFile(empty, nil, 0o600); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		// The user's ~/.aws profiles and SSO sessions are not used.
		env["AWS_CONFIG_FILE"], env["AWS_SHARED_CREDENTIALS_FILE"] = empty, empty
		env["AWS_PAGER"], env["AWS_CLI_AUTO_PROMPT"], env["AWS_DEFAULT_OUTPUT"] = "", "off", "json"
		if plan.Region != "" {
			env["AWS_REGION"], env["AWS_DEFAULT_REGION"] = plan.Region, plan.Region
		}
	case infra.GCP:
		// A private gcloud configuration, so the user's own login and
		// default project are not used, and no prompt can wait for input.
		env["CLOUDSDK_CONFIG"] = filepath.Join(dir, "gcloud")
		env["CLOUDSDK_CORE_DISABLE_PROMPTS"] = "1"
		env["CLOUDSDK_CORE_PROJECT"] = plan.Project
	}
	return &infraRunner{config: config, cloud: plan.Cloud, env: envList(env), dir: dir}, cleanup, nil
}

// Run runs one command. Waits can take a while; everything else should
// not.
func (r *infraRunner) Run(ctx context.Context, argv []string) (string, string, error) {
	if len(argv) == 0 {
		return "", "", errors.New("empty command")
	}
	bin, err := infraCLI(argv[0])
	if err != nil {
		return "", "", err
	}
	timeout := 10 * time.Minute
	if len(argv) > 2 && argv[2] == "wait" {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Env = r.env
	cmd.Dir = r.dir
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = 4<<20, 256<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	proc := newSecretProcess(cmd)
	if err := proc.start(); err != nil {
		return "", "", err
	}
	err = cmd.Wait()
	proc.finish()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return stdout.buf.String(), stderr.buf.String(), err
}

// InfraState is everything the infrastructure view shows.
type InfraState struct {
	AgentTools bool             `json:"agent_tools"`
	Plans      []infra.Plan     `json:"plans"`
	Resources  []infra.Resource `json:"resources"`
	// CostHourly is what the live resources cost per hour, approximately.
	CostHourly float64           `json:"cost_hourly"`
	Tools      map[string]string `json:"tools"` // aws/gcloud: path, or why not found
}

// InfraState reads plans and resources.
func (a *App) InfraState() (InfraState, error) {
	config := a.Config()
	store := infraStore(config)
	state := InfraState{AgentTools: config.Infra.AgentTools, Tools: map[string]string{}}
	var err error
	if state.Plans, err = store.Plans(100); err != nil {
		return state, err
	}
	if state.Resources, err = store.Resources(); err != nil {
		return state, err
	}
	for _, r := range state.Resources {
		if r.Live() {
			state.CostHourly += r.CostHourly
		}
	}
	for _, name := range []string{"aws", "gcloud"} {
		if path, err := infraCLI(name); err == nil {
			state.Tools[name] = path
		} else {
			state.Tools[name] = "missing: " + err.Error()
		}
	}
	return state, nil
}

// InfraLedger reads the ledger, for one plan or all.
func (a *App) InfraLedger(plan string, limit int) ([]infra.LedgerEntry, error) {
	return infraStore(a.Config()).Ledger(plan, limit)
}
