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
	// since it changed.
	Checked  string `json:"checked,omitempty"`
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
		a := CloudAccount{Name: s.Name, AgentUse: s.AgentUse, Checked: s.Public["verified"]}
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
	if err == nil {
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
		plan = infra.Plan{Cloud: infra.AWS, Region: id.Region}
	case SecretGCP:
		id.Project, id.Identity = gcpAccountProject(s), s.Public["client_email"]
		if id.Project == "" {
			return id, fmt.Errorf("%s names no project and its key file has none; set its project", s.Name)
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
		out, stderr, err := runner.Run(ctx, []string{"aws", "sts", "get-caller-identity", "--output", "json"})
		if ctx.Err() != nil {
			return id, errors.New("AWS did not answer in time; check this computer's connection")
		}
		if err != nil {
			return id, forgetRejected(config, s, awsCheckError(config, stderr, err))
		}
		var who struct{ Account, Arn string }
		if json.Unmarshal([]byte(out), &who) != nil || who.Account == "" {
			return id, errors.New("aws answered in a way NullBot did not understand; is it the AWS CLI v2?")
		}
		id.Account, id.Identity = who.Account, who.Arn
	case infra.GCP:
		_, stderr, err := runner.Run(ctx, []string{"gcloud", "compute", "project-info", "describe", "--project", id.Project, "--format", "json(name)"})
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

func awsCheckError(config Config, stderr string, err error) error {
	text := cliError(config, stderr, err)
	switch {
	case strings.Contains(text, "InvalidClientTokenId"):
		return rejectedError("AWS does not recognize this access key ID: check it, and that the key is active in IAM")
	case strings.Contains(text, "SignatureDoesNotMatch"):
		return rejectedError("the secret access key does not belong to this access key ID: paste it again")
	case strings.Contains(text, "ExpiredToken"):
		return rejectedError("these temporary credentials have expired: paste fresh ones, session token included")
	}
	return fmt.Errorf("aws could not sign in: %s", text)
}

// gcpCheckError tells a key that does not sign in (an error) from one that
// signs in but cannot use Compute Engine in the project yet (a note).
func gcpCheckError(config Config, project, stderr string, err error) (string, error) {
	text := cliError(config, stderr, err)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "failed to establish") || strings.Contains(lower, "name resolution") || strings.Contains(lower, "timed out") || strings.Contains(lower, "connection refused"):
		return "", fmt.Errorf("gcloud could not reach Google Cloud: %s", text)
	case strings.Contains(lower, "auth token") || strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "invalid jwt") || strings.Contains(lower, "unauthenticated"):
		return "", rejectedError("Google Cloud did not accept this key; it may have been deleted or disabled: " + text)
	case strings.Contains(lower, "has not been used in project") || strings.Contains(lower, "service_disabled") || strings.Contains(lower, "it is disabled"):
		return fmt.Sprintf("The key signs in, but the Compute Engine API is off in project %s. Turn it on in the Google Cloud console before planning machines.", project), nil
	case strings.Contains(lower, "was not found") || strings.Contains(lower, "notfound"):
		return fmt.Sprintf("The key signs in, but there is no project %s: check the project ID.", project), nil
	case strings.Contains(lower, "permission") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "403"):
		return fmt.Sprintf("The key signs in, but it cannot use Compute Engine in project %s: check the project ID, and give the service account Compute Admin (for machines) and Storage Admin (for buckets) there.", project), nil
	}
	return "", fmt.Errorf("gcloud could not sign in: %s", text)
}
