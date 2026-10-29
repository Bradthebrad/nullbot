// Package infra creates and tears down cloud machines and storage on AWS and
// Google Cloud through their official command-line tools (aws, gcloud), in a
// way NullBot can account for:
//
//   - every change is a Plan of steps, shown as the exact commands it will
//     run, and nothing runs until a person approves it;
//   - everything created is tagged (labels on Google Cloud) as NullBot's,
//     with the resource's own id, so it can be found and removed later;
//   - every command run, and what it returned, goes into an append-only
//     ledger, and the resources NullBot made are kept in an inventory.
//
// Credentials are never part of a plan or the ledger: the caller supplies
// them as environment variables when a plan is applied.
package infra

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Cloud is a provider.
type Cloud string

const (
	AWS Cloud = "aws"
	GCP Cloud = "gcp"
)

// Kinds of resource NullBot manages.
const (
	KindInstance      = "instance"
	KindBucket        = "bucket"
	KindSecurityGroup = "security_group" // AWS
	KindKeyPair       = "key_pair"       // AWS
	KindFirewall      = "firewall"       // Google Cloud
)

// Resource is something NullBot created, as the inventory knows it.
type Resource struct {
	ID      string `json:"id"` // nb-xxxxxxxx: NullBot's own id, also its tag
	Cloud   Cloud  `json:"cloud"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Region  string `json:"region,omitempty"`
	Zone    string `json:"zone,omitempty"`
	Project string `json:"project,omitempty"` // Google Cloud project
	// CloudID is the provider's id: i-0abc for an AWS instance, sg-… for a
	// security group; on Google Cloud the name is the id.
	CloudID string `json:"cloud_id,omitempty"`
	// State: planned, creating, ready, failed, deleting, deleted, missing.
	State      string            `json:"state"`
	PlanID     string            `json:"plan_id"`
	Credential string            `json:"credential"` // the vault secret it was made with
	Created    time.Time         `json:"created"`
	Updated    time.Time         `json:"updated"`
	Details    map[string]string `json:"details,omitempty"` // public_ip, type, disk_gb, ports…
	CostHourly float64           `json:"cost_hourly,omitempty"`
	DependsOn  []string          `json:"depends_on,omitempty"`
}

// Resource states.
const (
	StatePlanned  = "planned"
	StateCreating = "creating"
	StateReady    = "ready"
	StateFailed   = "failed"
	StateDeleting = "deleting"
	StateDeleted  = "deleted"
	StateMissing  = "missing" // gone from the cloud without NullBot removing it
)

// Live reports a resource that exists, or may, in the cloud.
func (r Resource) Live() bool {
	switch r.State {
	case StateDeleted, StatePlanned:
		return false
	}
	return true
}

// Step is one command of a plan.
type Step struct {
	ID string `json:"id"`
	// Summary says what the step does, in words.
	Summary string `json:"summary"`
	// Argv is the command, exactly as it will run, after {{placeholders}}
	// are filled in from earlier steps. It never holds a credential.
	Argv []string `json:"argv"`
	// Resource is the NullBot id the step creates, changes or deletes.
	Resource string `json:"resource,omitempty"`
	Action   string `json:"action"` // create, delete, wait, read, change
	// Input is written to the command's standard input (user data, a
	// public key), shown in the plan, and not kept in the ledger.
	Input string `json:"input,omitempty"`
	// Parse names how the output updates the inventory.
	Parse string `json:"parse,omitempty"`
	// IgnoreMissing makes "not found" a success, for deletes.
	IgnoreMissing bool `json:"ignore_missing,omitempty"`
	// State and Error are filled in as the plan runs.
	State  string    `json:"state,omitempty"` // pending, running, done, failed, skipped
	Error  string    `json:"error,omitempty"`
	Output string    `json:"output,omitempty"` // a short excerpt
	Ran    time.Time `json:"ran,omitempty"`
}

// Plan states.
const (
	PlanProposed = "proposed"
	PlanApproved = "approved"
	PlanApplying = "applying"
	PlanApplied  = "applied"
	PlanFailed   = "failed"
	PlanRejected = "rejected"
)

// Plan is a set of changes waiting for approval, being applied, or done.
type Plan struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Cloud      Cloud      `json:"cloud"`
	Credential string     `json:"credential"`
	Region     string     `json:"region,omitempty"`
	Project    string     `json:"project,omitempty"`
	Kind       string     `json:"kind"` // create or destroy
	Steps      []Step     `json:"steps"`
	Resources  []Resource `json:"resources"` // what it creates or removes
	// CostHourly is the approximate cost of what it creates, per hour.
	CostHourly float64   `json:"cost_hourly,omitempty"`
	CostNote   string    `json:"cost_note,omitempty"`
	Notes      []string  `json:"notes,omitempty"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"` // why the agent asked
	Requester  string    `json:"requester,omitempty"`
	Created    time.Time `json:"created"`
	Decided    time.Time `json:"decided,omitempty"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	Finished   time.Time `json:"finished,omitempty"`
	Error      string    `json:"error,omitempty"`
	// ApprovedHash is ContentHash when the plan was approved: a plan whose
	// commands changed after approval does not run.
	ApprovedHash string `json:"approved_hash,omitempty"`
	// RunnerPID and RunnerHost are the process applying it, so a run whose
	// process ended without finishing can be recognized.
	RunnerPID  int    `json:"runner_pid,omitempty"`
	RunnerHost string `json:"runner_host,omitempty"`
	// Hash is ContentHash as the plan was read, for approving exactly what
	// was shown. It is not stored.
	Hash string `json:"hash,omitempty"`
}

// ContentHash covers everything a plan does: its cloud, credentials, each
// step's command, input and handling, and the resources it touches.
// Progress (states, output, times) is left out.
func (p Plan) ContentHash() string {
	type step struct {
		Argv          []string `json:"argv"`
		Input         string   `json:"input"`
		Resource      string   `json:"resource"`
		Action        string   `json:"action"`
		Parse         string   `json:"parse"`
		IgnoreMissing bool     `json:"ignore_missing"`
	}
	type resource struct {
		ID, Kind, Name, Region, Zone, Project, CloudID string
	}
	content := struct {
		ID, Cloud, Credential, Region, Project, Kind string
		Steps                                        []step
		Resources                                    []resource
	}{ID: p.ID, Cloud: string(p.Cloud), Credential: p.Credential, Region: p.Region, Project: p.Project, Kind: p.Kind}
	for _, s := range p.Steps {
		content.Steps = append(content.Steps, step{s.Argv, s.Input, s.Resource, s.Action, s.Parse, s.IgnoreMissing})
	}
	for _, r := range p.Resources {
		content.Resources = append(content.Resources, resource{r.ID, r.Kind, r.Name, r.Region, r.Zone, r.Project, r.CloudID})
	}
	data, _ := json.Marshal(content)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CleanText drops control characters (keeping new lines and tabs), so text
// from an agent or a remote machine cannot rewrite a terminal: hide the
// lines after it, move the cursor, or change the title.
func CleanText(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	return r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069))
}

// Pending reports a plan waiting for a decision.
func (p Plan) Pending() bool { return p.State == PlanProposed }

func newID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// NewResourceID is a fresh NullBot resource id.
func NewResourceID() string { return newID("nb-") }

// NewPlanID is a fresh plan id.
func NewPlanID() string { return newID("plan-") }

var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,40}[a-z0-9])?$`)

// ValidName reports a name both clouds accept for machines and rules:
// lowercase letters, digits and dashes, starting with a letter.
func ValidName(name string) bool { return namePattern.MatchString(name) }

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// ValidBucketName reports a bucket name both clouds accept.
func ValidBucketName(name string) bool {
	return bucketPattern.MatchString(name) && !strings.Contains(name, "..") && !strings.HasPrefix(name, "goog") && !strings.HasPrefix(name, "xn--")
}

// ValidCIDR reports an IPv4 range written as its network, such as
// 203.0.113.4/32 or 10.0.0.0/16. A range whose address has bits past its
// length (203.0.113.4/0) is refused: it reads like one address but opens
// many.
func ValidCIDR(cidr string) bool {
	prefix, err := netip.ParsePrefix(cidr)
	return err == nil && prefix.Addr().Is4() && prefix.Masked() == prefix && prefix.String() == cidr
}

// CIDRSize is how many addresses a range covers.
func CIDRSize(cidr string) uint64 {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return 0
	}
	return 1 << (32 - prefix.Bits())
}

// CIDRHint suggests the range someone probably meant, for an error.
func CIDRHint(cidr string) string {
	if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Addr().Is4() {
		if prefix.Masked() != prefix {
			return fmt.Sprintf("; did you mean %s/32 (that address) or %s (the whole range)?", prefix.Addr(), prefix.Masked())
		}
	}
	return ""
}

var regionPattern = regexp.MustCompile(`^[a-z]{2,}(-[a-z0-9]+){1,3}$`)

// ValidRegion reports a region or zone name such as us-east-1 or
// us-central1-a.
func ValidRegion(region string) bool { return regionPattern.MatchString(region) }

var locationPattern = regexp.MustCompile(`^(?i)[a-z][a-z0-9]{1,15}(-[a-z0-9]+){0,3}$`)

// ValidLocation reports a Google Cloud Storage location: a region
// (us-central1), a dual region (nam4) or a multi-region (US, EU, ASIA).
func ValidLocation(location string) bool {
	return len(location) <= 40 && locationPattern.MatchString(location)
}

var typePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,40}$`)

// ValidMachineType reports a machine type such as t3.medium or e2-standard-4.
func ValidMachineType(t string) bool { return typePattern.MatchString(t) }
