package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// Cloud accounts are the aws and gcp secrets cloud plans run with. Checking
// one asks the cloud who its key signs in as, through the same aws or gcloud
// command and the same clean environment plans use. A check changes nothing
// and costs nothing: aws sts get-caller-identity needs no permission at all,
// and gcloud compute project-info describe only reads the project.

// CloudAccount is an aws or gcp secret as the Cloud view lists it, without
// its values.
type CloudAccount struct {
	Name  string `json:"name"`
	Cloud string `json:"cloud"` // aws or gcp
	// Region is an AWS account's default region; Project is a Google Cloud
	// account's project.
	Region  string `json:"region,omitempty"`
	Project string `json:"project,omitempty"`
	// Account is the AWS account ID, once checked. Identity is who the key
	// signs in as: an ARN once checked, or a service account's email.
	Account  string `json:"account,omitempty"`
	Identity string `json:"identity,omitempty"`
	// Checked is when the key last signed in (RFC 3339), empty if never or
	// since it changed. Note is what that check found the key cannot do yet.
	Checked  string `json:"checked,omitempty"`
	Note     string `json:"note,omitempty"`
	AgentUse bool   `json:"agent_use"`
}

// CloudIdentity is what checking a cloud account found.
type CloudIdentity struct {
	Name     string `json:"name"`
	Cloud    string `json:"cloud"`
	Account  string `json:"account,omitempty"`
	Identity string `json:"identity,omitempty"`
	Region   string `json:"region,omitempty"`
	Project  string `json:"project,omitempty"`
	// Note says what the key cannot do yet, when it signs in but plans
	// would still fail.
	Note string `json:"note,omitempty"`
}

// CloudAccounts lists the aws and gcp secrets in the vault.
func CloudAccounts(config Config) ([]CloudAccount, error) {
	secrets, err := ListSecrets(config)
	if err != nil {
		return nil, err
	}
	out := []CloudAccount{}
	for _, s := range secrets {
		a := CloudAccount{Name: s.Name, AgentUse: s.AgentUse, Checked: s.Public["verified"], Note: s.Public["check_note"]}
		switch s.Kind {
		case SecretAWS:
			a.Cloud, a.Region, a.Account, a.Identity = string(infra.AWS), s.Public["region"], s.Public["account"], s.Public["identity"]
		case SecretGCP:
			a.Cloud, a.Project, a.Identity = string(infra.GCP), s.Public["project_id"], s.Public["client_email"]
		default:
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// CheckCloudAccount asks AWS or Google Cloud who an account's key signs in
// as, and records it with the secret.
func (a *App) CheckCloudAccount(ctx context.Context, name string) (CloudIdentity, error) {
	id, err := CheckCloudAccount(ctx, a.Config(), name)
	if err == nil || errors.Is(err, errKeyRejected) {
		// Recorded, or an earlier check forgotten.
		a.secretsChanged(false, nil)
		a.notifyChange(ChangeInfra)
	}
	return id, err
}

// CheckCloudAccount asks AWS or Google Cloud who an account's key signs in
// as, and records it with the secret.
func CheckCloudAccount(ctx context.Context, config Config, name string) (CloudIdentity, error) {
	name = strings.TrimSpace(name)
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return CloudIdentity{}, fmt.Errorf("the vault could not be opened: %w", err)
	}
	i := secretIndex(payload, name)
	if i < 0 {
		return CloudIdentity{}, fmt.Errorf("there is no secret named %s", name)
	}
	s := payload.Secrets[i]
	id := CloudIdentity{Name: s.Name}
	var plan infra.Plan
	switch s.Kind {
	case SecretAWS:
		// STS answers in every region; us-east-1 when the account has none.
		id.Region = firstNonEmpty(s.Public["region"], "us-east-1")
		if !infra.ValidLocation(id.Region) {
			return id, fmt.Errorf("%q is not an AWS region; change %s's region", id.Region, s.Name)
		}
		plan = infra.Plan{Cloud: infra.AWS, Region: id.Region}
	case SecretGCP:
		id.Project, id.Identity = gcpAccountProject(s), s.Public["client_email"]
		if id.Project == "" {
			return id, fmt.Errorf("%s names no project and its key file has none; set its project", s.Name)
		}
		if !infra.ValidProject(id.Project) {
			return id, fmt.Errorf("%q is not a Google Cloud project id; change %s's project", id.Project, s.Name)
		}
		plan = infra.Plan{Cloud: infra.GCP, Project: id.Project}
	default:
		return id, fmt.Errorf("%s is not an AWS or Google Cloud account", s.Name)
	}
	id.Cloud = string(plan.Cloud)
	if _, err := infraCLI(cliFor(plan.Cloud)); err != nil {
		return id, err
	}
	runner, cleanup, err := newInfraRunner(config, plan, s)
	if err != nil {
		return id, err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	switch plan.Cloud {
	case infra.AWS:
		ask := func(region string) (string, string, error) {
			return runner.Run(ctx, []string{"aws", "sts", "get-caller-identity", "--output", "json", "--region", region})
		}
		out, stderr, err := ask(id.Region)
		if err != nil && ctx.Err() == nil && strings.Contains(stderr, "InvalidClientTokenId") && id.Region != "us-east-1" {
			// A region the account has not turned on refuses every key; ask
			// where every account answers before blaming the key.
			if out2, _, err2 := ask("us-east-1"); err2 == nil {
				out, err = out2, nil
				id.Note = fmt.Sprintf("The key signs in, but not in %s: turn that region on for the account in the AWS console, or change this account's region.", id.Region)
			}
		}
		if ctx.Err() != nil {
			return id, errors.New("AWS did not answer in time; check this computer's connection")
		}
		if err != nil {
			temporary := strings.HasPrefix(s.Values["access_key_id"], "ASIA") && s.Values["session_token"] == ""
			return id, forgetRejected(config, s, awsCheckError(config, stderr, err, temporary))
		}
		var who struct{ Account, Arn string }
		if json.Unmarshal([]byte(out), &who) != nil || who.Account == "" {
			return id, errors.New("aws answered in a way NullBot did not understand; is it the AWS CLI v2?")
		}
		id.Account, id.Identity = who.Account, who.Arn
	case infra.GCP:
		_, stderr, err := runner.Run(ctx, []string{"gcloud", "compute", "project-info", "describe", "--project", id.Project, "--format", "json"})
		if ctx.Err() != nil {
			return id, errors.New("Google Cloud did not answer in time; check this computer's connection")
		}
		if err != nil {
			note, err := gcpCheckError(config, id.Project, stderr, err)
			if err != nil {
				return id, forgetRejected(config, s, err)
			}
			id.Note = note
		}
	}

	err = updateSecrets(config, func(p *vaultPayload) error {
		i := secretIndex(*p, s.Name)
		if i < 0 {
			return fmt.Errorf("%s was removed while it was being checked", s.Name)
		}
		if credentialStamp(p.Secrets[i]) != credentialStamp(s) {
			return fmt.Errorf("%s changed while it was being checked; check it again", s.Name)
		}
		pub := p.Secrets[i].Public
		if pub == nil {
			pub = map[string]string{}
		}
		if s.Kind == SecretAWS {
			pub["account"], pub["identity"] = id.Account, id.Identity
		}
		pub["verified"] = time.Now().UTC().Format(time.RFC3339)
		if id.Note != "" {
			pub["check_note"] = id.Note
		} else {
			delete(pub, "check_note")
		}
		p.Secrets[i].Public = pub
		return nil
	})
	return id, err
}

// errKeyRejected marks a key the cloud refused, as opposed to a check that
// could not finish (no connection, an answer NullBot did not expect).
var errKeyRejected = errors.New("the cloud refused the key")

type rejectedError string

func (e rejectedError) Error() string        { return string(e) }
func (e rejectedError) Is(target error) bool { return target == errKeyRejected }

// forgetRejected clears an earlier check of a key the cloud now refuses, so
// the account no longer shows as signing in. It returns err.
func forgetRejected(config Config, s Secret, err error) error {
	if !errors.Is(err, errKeyRejected) {
		return err
	}
	_ = updateSecrets(config, func(p *vaultPayload) error {
		if i := secretIndex(*p, s.Name); i >= 0 && credentialStamp(p.Secrets[i]) == credentialStamp(s) {
			for _, k := range checkedFields {
				delete(p.Secrets[i].Public, k)
			}
		}
		return nil
	})
	return err
}

// gcpAccountProject is the project a Google Cloud account works in: the
// one set on the secret, or else its key file's.
func gcpAccountProject(s Secret) string {
	return firstNonEmpty(s.Public["project_id"], gcpProject(s))
}

// cliError is the part of a failed command's output worth showing: the
// error lines, without any secret.
func cliError(config Config, stderr string, err error) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(stderr, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	text := strings.Join(lines, " ")
	if text == "" && err != nil {
		text = err.Error()
	}
	text = RedactSecrets(config, text)
	if len(text) > 400 {
		text = text[:400] + "…"
	}
	return text
}

// clockMessage is for a refusal because this computer's clock is wrong, not
// because of the key.
const clockMessage = "%s refused the sign-in because this computer's clock is off; set it to update automatically, then check again"

func containsAny(text string, parts ...string) bool {
	for _, p := range parts {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func awsCheckError(config Config, stderr string, err error, temporaryWithoutToken bool) error {
	text := cliError(config, stderr, err)
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "signature expired", "signature not yet current", "requesttimetooskewed", "request has expired"):
		return fmt.Errorf(clockMessage, "AWS")
	case containsAny(lower, "could not connect to the endpoint", "ssl validation failed", "connect timeout", "read timeout", "proxy", "name resolution", "connection reset", "connection refused"):
		return fmt.Errorf("aws could not reach AWS: %s", text)
	case strings.Contains(text, "InvalidClientTokenId") && temporaryWithoutToken:
		return rejectedError("this is a temporary access key (it starts with ASIA), which works only with its session token: add the token with Replace or change")
	case strings.Contains(text, "InvalidClientTokenId"):
		return rejectedError("AWS does not recognize this access key ID: check it, and that the key is active in IAM")
	case strings.Contains(text, "SignatureDoesNotMatch"):
		return rejectedError("the secret access key does not belong to this access key ID: paste it again")
	case strings.Contains(text, "ExpiredToken"):
		return rejectedError("these temporary credentials have expired: paste fresh ones, session token included")
	}
	return fmt.Errorf("aws could not check the key: %s", text)
}

// gcpCheckError tells a key Google refused (an error that forgets the last
// check) from one that signs in but cannot use Compute Engine in the
// project yet (a note), and both from a check that could not finish.
func gcpCheckError(config Config, project, stderr string, err error) (string, error) {
	text := cliError(config, stderr, err)
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "reasonable timeframe", "used too early", "used too late"):
		return "", fmt.Errorf(clockMessage, "Google Cloud")
	// gcloud wraps any failure to get a token, the network's included, in
	// "There was a problem refreshing your current auth tokens".
	case containsAny(lower, "ssl", "certificate", "max retries", "connection reset", "connection aborted", "remotedisconnected", "remote end closed",
		"proxy", "tunnel connection failed", "failed to establish", "name resolution", "timed out", "connection refused", "network is unreachable", "temporary failure"):
		return "", fmt.Errorf("gcloud could not reach Google Cloud: %s", text)
	case containsAny(lower, "invalid_grant", "invalid jwt signature", "unauthenticated", "invalid_client", "account not found"):
		return "", rejectedError("Google Cloud did not accept this key; it may have been deleted or disabled: " + text)
	case containsAny(lower, "has not been used in project", "service_disabled", "it is disabled"):
		return fmt.Sprintf("The key signs in, but the Compute Engine API is off in project %s. Turn it on in the Google Cloud console before planning machines.", project), nil
	case containsAny(lower, "was not found", "notfound"):
		return fmt.Sprintf("The key signs in, but there is no project %s: check the project ID.", project), nil
	case containsAny(lower, "permission", "forbidden", "403"):
		return fmt.Sprintf("The key signs in, but it cannot use Compute Engine in project %s: check the project ID, and give the service account Compute Admin (for machines) and Storage Admin (for buckets) there.", project), nil
	}
	return "", fmt.Errorf("gcloud could not check the key: %s", text)
}
