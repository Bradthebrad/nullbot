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
	"encoding/hex"
	"fmt"
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

var cidrPattern = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/\d{1,2}$`)

// ValidCIDR reports an IPv4 range such as 203.0.113.4/32.
func ValidCIDR(cidr string) bool {
	if !cidrPattern.MatchString(cidr) {
		return false
	}
	var a, b, c, d, bits int
	if _, err := fmt.Sscanf(cidr, "%d.%d.%d.%d/%d", &a, &b, &c, &d, &bits); err != nil {
		return false
	}
	return a <= 255 && b <= 255 && c <= 255 && d <= 255 && bits >= 0 && bits <= 32
}

var regionPattern = regexp.MustCompile(`^[a-z]{2,}(-[a-z0-9]+){1,3}$`)

// ValidRegion reports a region or zone name such as us-east-1 or
// us-central1-a.
func ValidRegion(region string) bool { return regionPattern.MatchString(region) }

var typePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,40}$`)

// ValidMachineType reports a machine type such as t3.medium or e2-standard-4.
func ValidMachineType(t string) bool { return typePattern.MatchString(t) }
