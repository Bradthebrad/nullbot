package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// Directly, not through a proxy: the address asked for is the one this
	// computer's own connections (SSH) come from.
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
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
	agent := byAgent(requester)
	if agent {
		if err := agentMayUse(cred); err != nil {
			return infra.Plan{}, err
		}
		// An agent names a key from the vault, which you can see and
		// revoke; a key of its own would let in whoever holds it.
		for _, m := range spec.Machines {
			if strings.TrimSpace(m.SSHKey) != "" {
				return infra.Plan{}, errors.New("agents give an SSH key by its vault name (ssh_key_secret), not as a key")
			}
		}
	}
	if spec.Cloud == infra.GCP && spec.Project == "" {
		spec.Project = gcpAccountProject(cred)
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
	// The spec's machines are the caller's; work on a copy.
	spec.Machines = append([]infra.MachineSpec(nil), spec.Machines...)
	for i := range spec.Machines {
		spec.Machines[i].AllowFrom = append([]string(nil), spec.Machines[i].AllowFrom...)
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
	if req.SSHKeySecret != "" {
		// So NullBot can log in later to see what runs there.
		for i := range plan.Resources {
			if plan.Resources[i].Kind == infra.KindInstance {
				plan.Resources[i].Details["ssh_key_secret"] = req.SSHKeySecret
			}
		}
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

// PlanInfraDestroy saves a plan that removes resources, by NullBot id or
// name, or everything a plan created when given a plan id. A machine goes
// with what it used (its security group or firewall rules, its key pair)
// unless another machine still uses them.
func (a *App) PlanInfraDestroy(ids []string, deleteContents bool, requester, reason string) (infra.Plan, error) {
	config := a.Config()
	store := infraStore(config)
	_ = store.RecoverStale(processAlive)
	all, err := store.Resources()
	if err != nil {
		return infra.Plan{}, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	chosen := map[string]bool{}
	for _, r := range all {
		if r.Live() && (want[r.ID] || want[r.PlanID] || want[r.Name]) {
			chosen[r.ID] = true
		}
	}
	usedElsewhere := map[string]bool{}
	for _, r := range all {
		if r.Live() && !chosen[r.ID] {
			for _, dep := range r.DependsOn {
				usedElsewhere[dep] = true
			}
		}
	}
	for _, r := range all {
		if chosen[r.ID] {
			for _, dep := range r.DependsOn {
				if !usedElsewhere[dep] {
					chosen[dep] = true
				}
			}
		}
	}
	var picked []infra.Resource
	for _, r := range all {
		if chosen[r.ID] && r.Live() {
			picked = append(picked, r)
		}
	}
	if len(picked) == 0 {
		return infra.Plan{}, errors.New("none of those are resources NullBot made that still exist; infra_status lists them")
	}
	plans, _ := store.Plans(0)
	running := map[string]bool{}
	for _, p := range plans {
		if p.State == infra.PlanApplying || p.State == infra.PlanApproved {
			running[p.ID] = true
		}
	}
	for _, r := range picked {
		// Removing something while its plan still creates it could look
		// for it before it exists, and leave it running unrecorded.
		if running[r.PlanID] {
			return infra.Plan{}, fmt.Errorf("%s is still being created by plan %s; remove it when that plan has finished", r.Name, r.PlanID)
		}
		// A rule or group a machine still uses stays with the machine: a
		// Google Cloud deny rule removed alone would open the machine to
		// the default network's SSH and RDP from anywhere.
		for _, other := range all {
			if other.Live() && !chosen[other.ID] && slices.Contains(other.DependsOn, r.ID) && r.Kind != infra.KindKeyPair {
				return infra.Plan{}, fmt.Errorf("%s %s is used by %s; remove %s instead, and it goes with it", r.Kind, r.Name, other.Name, other.Name)
			}
		}
	}
	if byAgent(requester) {
		cred, err := infraCredential(config, picked[0].Cloud, picked[0].Credential)
		if err != nil {
			return infra.Plan{}, err
		}
		if err := agentMayUse(cred); err != nil {
			return infra.Plan{}, err
		}
	}
	chosenList := picked
	plan, err := infra.BuildDestroyPlan(chosenList, deleteContents, requester, reason)
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
//
// seen is the plan's Hash as the person saw it; approving fails if the plan
// changed since. Empty skips that check (the caller showed the plan just
// now, under the same lock).
func (a *App) DecideInfraPlan(id string, approve bool, by, seen string) (infra.Plan, error) {
	var key []byte
	if approve {
		var err error
		if key, err = infraApprovalKey(a.Config(), true); err != nil {
			return infra.Plan{}, err
		}
	}
	plan, err := infraStore(a.Config()).Decide(id, approve, by, seen, key)
	clear(key)
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
	_ = store.RecoverStale(processAlive)
	plan, err := store.Plan(id)
	if err != nil {
		return plan, err
	}
	cred, err := infraCredential(config, plan.Cloud, plan.Credential)
	if err != nil {
		return plan, err
	}
	// Without a key (nothing approved yet, or the vault is shut), the store
	// says why the plan cannot run.
	key, _ := infraApprovalKey(config, false)
	defer clear(key)
	runner, cleanup, err := newInfraRunner(config, plan, cred)
	if err != nil {
		return plan, err
	}
	defer cleanup()
	a.logInfo("infrastructure plan applying", "plan", id, "by", by)
	done, err := store.Apply(ctx, id, infra.ApplyOptions{
		Runner: runner, By: by, TempDir: runner.dir, ApprovalKey: key,
		Redact:   func(s string) string { return RedactSecrets(config, s) },
		Progress: func(infra.Plan) { a.notifyChange(ChangeInfra) },
	})
	if err != nil {
		a.logError("infrastructure plan failed", "plan", id, "error", err)
	} else {
		a.logInfo("infrastructure plan applied", "plan", id)
	}
	if done.Kind == "destroy" {
		forgetHostKeys(config, done)
	}
	a.notifyChange(ChangeInfra)
	return done, err
}

// infraApprovalKey is the key that seals plan approvals, kept in the vault
// with NullBot's other system credentials, which agents and commands never
// get. It is made the first time a plan is approved.
func infraApprovalKey(config Config, create bool) ([]byte, error) {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return nil, fmt.Errorf("the vault could not be opened for the approval key: %w", err)
	}
	if payload.System.InfraKey == "" && create {
		err = updateSecrets(config, func(p *vaultPayload) error {
			if p.System.InfraKey == "" {
				var b [32]byte
				if _, err := rand.Read(b[:]); err != nil {
					return err
				}
				p.System.InfraKey = base64.StdEncoding.EncodeToString(b[:])
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if payload, err = loadSecretsPayload(config); err != nil {
			return nil, err
		}
	}
	if payload.System.InfraKey == "" {
		return nil, errors.New("no plan has been approved with this NullBot folder's vault")
	}
	return base64.StdEncoding.DecodeString(payload.System.InfraKey)
}

// byAgent reports a plan asked for by an agent rather than a person.
func byAgent(requester string) bool { return strings.HasPrefix(requester, "agent ") }

// agentMayUse checks that a secret is allowed for agents (Settings >
// Secrets), as it would be for secret_run.
func agentMayUse(s Secret) error {
	if !s.AgentUse {
		return fmt.Errorf("secret %s is not allowed for agents; allow it in Settings > Secrets, or plan this yourself (nullbot infra plan)", s.Name)
	}
	return nil
}

// forgetHostKeys removes removed machines' addresses from NullBot's
// known_hosts, so a new machine that gets the same address later is not
// refused as a changed host.
func forgetHostKeys(config Config, plan infra.Plan) {
	resources, err := infraStore(config).Resources()
	if err != nil {
		return
	}
	gone := map[string]bool{}
	for _, r := range resources {
		if r.Kind == infra.KindInstance && r.State == infra.StateDeleted && r.PlanID != "" {
			for _, p := range plan.Resources {
				if p.ID == r.ID && r.Details["public_ip"] != "" {
					gone[r.Details["public_ip"]] = true
				}
			}
		}
	}
	if len(gone) == 0 {
		return
	}
	path := filepath.Join(config.AppDir, "infra", "known_hosts")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		hosts, _, _ := strings.Cut(line, " ")
		drop := false
		for _, h := range strings.Split(hosts, ",") {
			if gone[strings.Trim(h, "[]")] || gone[strings.TrimSuffix(strings.TrimPrefix(h, "["), "]:22")] {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600)
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
	sweepSecretTemp()
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
	for k := range env {
		// Only the settings below reach the CLIs: no endpoint overrides,
		// impersonation or profiles from this computer's environment.
		if strings.HasPrefix(strings.ToUpper(k), "CLOUDSDK_") || strings.HasPrefix(strings.ToUpper(k), "AWS_") || strings.HasPrefix(strings.ToUpper(k), "GOOGLE_") {
			delete(env, k)
		}
	}
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
	// The program exec resolved, extension and all: NULLBOT_GCLOUD_BIN
	// without .cmd still runs gcloud.cmd on Windows.
	if err := checkBatchArgs(cmd.Path, argv[1:]); err != nil {
		return "", "", err
	}
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

// checkBatchArgs refuses arguments cmd.exe would read as commands. On
// Windows gcloud is gcloud.cmd, which runs through cmd.exe, and Go cannot
// quote an argument safely for it: a quote, &, |, <, >, ^, % or ! in one
// could run something else. NullBot's own arguments never contain them.
func checkBatchArgs(bin string, args []string) error {
	ext := strings.ToLower(filepath.Ext(bin))
	if ext != ".cmd" && ext != ".bat" {
		return nil
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\"&|<>^%!\r\n") || strings.ContainsFunc(arg, func(r rune) bool { return r < 0x20 }) {
			return fmt.Errorf("an argument has a character %s cannot pass on safely: %q", filepath.Base(bin), arg)
		}
	}
	return nil
}

// InfraState is everything the infrastructure view shows.
type InfraState struct {
	AgentTools bool             `json:"agent_tools"`
	Plans      []infra.Plan     `json:"plans"`
	Resources  []infra.Resource `json:"resources"`
	// CostHourly is what the live resources cost per hour, approximately.
	CostHourly float64           `json:"cost_hourly"`
	Tools      map[string]string `json:"tools"` // aws/gcloud: path, or why not found
	// Accounts are the aws and gcp secrets plans can use; AccountsError
	// says why they could not be read (a locked vault).
	Accounts      []CloudAccount `json:"accounts"`
	AccountsError string         `json:"accounts_error,omitempty"`
}

// InfraState reads plans and resources.
func (a *App) InfraState() (InfraState, error) {
	config := a.Config()
	store := infraStore(config)
	_ = store.RecoverStale(processAlive)
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
	if state.Accounts, err = CloudAccounts(config); err != nil {
		state.Accounts, state.AccountsError = []CloudAccount{}, err.Error()
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
