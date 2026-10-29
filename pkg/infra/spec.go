package infra

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spec is what to create, as an agent or a person asks for it.
type Spec struct {
	Cloud Cloud `json:"cloud"`
	// Credential names the vault secret to use (an aws or gcp secret).
	Credential string `json:"credential"`
	// Region for AWS (us-east-1); zone for Google Cloud machines
	// (us-central1-a) and location for its buckets (US, us-central1).
	Region   string        `json:"region,omitempty"`
	Zone     string        `json:"zone,omitempty"`
	Project  string        `json:"project,omitempty"`
	Machines []MachineSpec `json:"machines,omitempty"`
	Buckets  []BucketSpec  `json:"buckets,omitempty"`
	Reason   string        `json:"reason,omitempty"`
}

// MachineSpec is a virtual machine.
type MachineSpec struct {
	Name   string `json:"name"`
	Type   string `json:"type"`              // t3.medium, g6.xlarge, e2-standard-4, g2-standard-4
	DiskGB int    `json:"disk_gb,omitempty"` // boot disk; 30 when unset
	// SSHKey is the OpenSSH public key to log in with, and SSHUser the
	// login (ubuntu on AWS's Ubuntu images; nullbot on Google Cloud).
	SSHKey  string `json:"ssh_public_key,omitempty"`
	SSHUser string `json:"ssh_user,omitempty"`
	// Ports to open, and from where. SSH (22) is always opened, only to
	// AllowFrom, which must be given: there is no "from anywhere" default.
	Ports     []int    `json:"ports,omitempty"`
	AllowFrom []string `json:"allow_from"`
	// UserData is a cloud-init script run on first boot (see Bootstrap).
	UserData string `json:"user_data,omitempty"`
	// Spot asks for spare capacity at a lower price, which the provider
	// may take back at any time.
	Spot bool `json:"spot,omitempty"`
}

// BucketSpec is a storage bucket.
type BucketSpec struct {
	Name string `json:"name"`
}

// Defaults for a spec without them.
const (
	DefaultAWSRegion = "us-east-1"
	DefaultGCPZone   = "us-central1-a"
	DefaultDiskGB    = 30
)

func (s *Spec) normalize() error {
	s.Cloud = Cloud(strings.ToLower(strings.TrimSpace(string(s.Cloud))))
	if s.Cloud != AWS && s.Cloud != GCP {
		return errors.New("choose a cloud: aws or gcp")
	}
	if strings.TrimSpace(s.Credential) == "" {
		return fmt.Errorf("name the %s credentials to use (a secret in the vault)", s.Cloud)
	}
	if len(s.Machines) == 0 && len(s.Buckets) == 0 {
		return errors.New("nothing to create: give machines or buckets")
	}
	if len(s.Machines) > 10 || len(s.Buckets) > 10 {
		return errors.New("a plan creates at most 10 machines and 10 buckets")
	}
	switch s.Cloud {
	case AWS:
		if s.Region == "" {
			s.Region = DefaultAWSRegion
		}
		if !ValidRegion(s.Region) {
			return fmt.Errorf("%q is not an AWS region", s.Region)
		}
	case GCP:
		if s.Zone == "" {
			s.Zone = DefaultGCPZone
		}
		if !ValidRegion(s.Zone) {
			return fmt.Errorf("%q is not a Google Cloud zone", s.Zone)
		}
		if s.Region == "" {
			s.Region = zoneRegion(s.Zone)
		}
		if s.Project == "" {
			return errors.New("the Google Cloud project is unknown; give project, or use a key file that names one")
		}
		if !ValidName(s.Project) && !projectPattern.MatchString(s.Project) {
			return fmt.Errorf("%q is not a Google Cloud project id", s.Project)
		}
	}
	names := map[string]bool{}
	for i := range s.Machines {
		m := &s.Machines[i]
		m.Name = strings.ToLower(strings.TrimSpace(m.Name))
		m.Type = strings.TrimSpace(m.Type)
		if !ValidName(m.Name) {
			return fmt.Errorf("machine name %q: use lowercase letters, digits and dashes, starting with a letter", m.Name)
		}
		if names[m.Name] {
			return fmt.Errorf("two machines are named %s", m.Name)
		}
		names[m.Name] = true
		if !ValidMachineType(m.Type) {
			return fmt.Errorf("machine %s: %q is not a machine type", m.Name, m.Type)
		}
		if m.DiskGB == 0 {
			m.DiskGB = DefaultDiskGB
		}
		if m.DiskGB < 10 || m.DiskGB > 4096 {
			return fmt.Errorf("machine %s: choose a disk between 10 and 4096 GB", m.Name)
		}
		if len(m.AllowFrom) == 0 {
			return fmt.Errorf("machine %s: say who may connect (allow_from, such as your address/32)", m.Name)
		}
		for _, cidr := range m.AllowFrom {
			if !ValidCIDR(cidr) {
				return fmt.Errorf("machine %s: %q is not an IPv4 range such as 203.0.113.4/32", m.Name, cidr)
			}
		}
		ports := map[int]bool{22: true}
		for _, p := range m.Ports {
			if p < 1 || p > 65535 {
				return fmt.Errorf("machine %s: %d is not a port", m.Name, p)
			}
			ports[p] = true
		}
		m.Ports = m.Ports[:0]
		for p := range ports {
			m.Ports = append(m.Ports, p)
		}
		sort.Ints(m.Ports)
		m.SSHKey = strings.TrimSpace(m.SSHKey)
		if m.SSHKey != "" && !strings.HasPrefix(m.SSHKey, "ssh-") && !strings.HasPrefix(m.SSHKey, "ecdsa-") {
			return fmt.Errorf("machine %s: the SSH key must be an OpenSSH public key", m.Name)
		}
		if strings.ContainsAny(m.SSHKey, "\n\r,") {
			return fmt.Errorf("machine %s: the SSH key must be one line", m.Name)
		}
		if m.SSHUser == "" {
			m.SSHUser = map[Cloud]string{AWS: "ubuntu", GCP: "nullbot"}[s.Cloud]
		}
		if !ValidName(m.SSHUser) {
			return fmt.Errorf("machine %s: %q is not a user name", m.Name, m.SSHUser)
		}
		if len(m.UserData) > 16<<10 {
			return fmt.Errorf("machine %s: the first-boot script is larger than 16 KB", m.Name)
		}
	}
	for i := range s.Buckets {
		b := &s.Buckets[i]
		b.Name = strings.ToLower(strings.TrimSpace(b.Name))
		if !ValidBucketName(b.Name) {
			return fmt.Errorf("bucket name %q: use 3 to 63 lowercase letters, digits, dots and dashes", b.Name)
		}
		if names["bucket:"+b.Name] {
			return fmt.Errorf("two buckets are named %s", b.Name)
		}
		names["bucket:"+b.Name] = true
	}
	return nil
}

func zoneRegion(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

// BuildCreatePlan turns a spec into a plan, with the commands it will run.
func BuildCreatePlan(spec Spec, requester string) (Plan, error) {
	if err := spec.normalize(); err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: NewPlanID(), Cloud: spec.Cloud, Credential: spec.Credential, Region: spec.Region, Project: spec.Project,
		Kind: "create", State: PlanProposed, Reason: strings.TrimSpace(spec.Reason), Requester: requester, Created: now,
	}
	var titles []string
	add := func(r Resource) Resource {
		r.Cloud, r.PlanID, r.Credential, r.State, r.Created, r.Updated = spec.Cloud, plan.ID, spec.Credential, StatePlanned, now, now
		if r.ID == "" {
			r.ID = NewResourceID()
		}
		plan.Resources = append(plan.Resources, r)
		return r
	}
	for _, m := range spec.Machines {
		cost, known := MachinePrice(spec.Cloud, m.Type)
		if !known {
			plan.Notes = append(plan.Notes, fmt.Sprintf("No price is known for %s; check the provider's pricing page.", m.Type))
		}
		cost += DiskPrice(spec.Cloud, m.DiskGB)
		if m.Spot {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s uses spare (spot) capacity: cheaper, but the provider may stop it at any time. The estimate is the regular price.", m.Name))
		}
		plan.CostHourly += cost
		titles = append(titles, fmt.Sprintf("%s (%s)", m.Name, m.Type))
		var steps []Step
		switch spec.Cloud {
		case AWS:
			steps = awsMachineSteps(spec, m, add, cost)
		case GCP:
			steps = gcpMachineSteps(spec, m, add, cost)
		}
		plan.Steps = append(plan.Steps, steps...)
	}
	for _, b := range spec.Buckets {
		titles = append(titles, "bucket "+b.Name)
		switch spec.Cloud {
		case AWS:
			plan.Steps = append(plan.Steps, awsBucketSteps(spec, b, add)...)
		case GCP:
			plan.Steps = append(plan.Steps, gcpBucketSteps(spec, b, add)...)
		}
	}
	plan.Title = fmt.Sprintf("Create %s on %s", strings.Join(titles, ", "), cloudLabel(spec.Cloud))
	plan.CostNote = "Approximate on-demand list price in us-east-1 (AWS) or us-central1 (Google Cloud), including the disk. Other regions, data transfer, and storage use cost more."
	for i := range plan.Steps {
		plan.Steps[i].ID = strconv.Itoa(i + 1)
		plan.Steps[i].State = "pending"
	}
	return plan, nil
}

// BuildDestroyPlan removes resources NullBot made, dependents first.
// deleteContents also empties buckets that are not empty.
func BuildDestroyPlan(resources []Resource, deleteContents bool, requester, reason string) (Plan, error) {
	var live []Resource
	for _, r := range resources {
		if r.Live() && r.State != StateDeleting {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return Plan{}, errors.New("nothing to remove: those resources are already gone")
	}
	first := live[0]
	for _, r := range live[1:] {
		if r.Cloud != first.Cloud || r.Credential != first.Credential {
			return Plan{}, errors.New("remove resources from one cloud and one set of credentials at a time")
		}
	}
	plan := Plan{ID: NewPlanID(), Cloud: first.Cloud, Credential: first.Credential, Region: first.Region, Project: first.Project,
		Kind: "destroy", State: PlanProposed, Requester: requester, Reason: strings.TrimSpace(reason), Created: time.Now().UTC(), Resources: live}
	// Machines first, then what they used: rules, keys; buckets any time.
	order := map[string]int{KindInstance: 0, KindBucket: 1, KindFirewall: 2, KindSecurityGroup: 2, KindKeyPair: 3}
	sort.SliceStable(live, func(i, j int) bool { return order[live[i].Kind] < order[live[j].Kind] })
	var names []string
	for _, r := range live {
		names = append(names, r.Name)
		plan.CostHourly -= r.CostHourly
		switch r.Cloud {
		case AWS:
			plan.Steps = append(plan.Steps, awsDeleteSteps(r, deleteContents)...)
		case GCP:
			plan.Steps = append(plan.Steps, gcpDeleteSteps(r, deleteContents)...)
		}
	}
	plan.Title = fmt.Sprintf("Remove %s from %s", strings.Join(names, ", "), cloudLabel(first.Cloud))
	plan.CostNote = "What removing them saves per hour, approximately."
	if !deleteContents {
		for _, r := range live {
			if r.Kind == KindBucket {
				plan.Notes = append(plan.Notes, fmt.Sprintf("Bucket %s is removed only if it is empty.", r.Name))
			}
		}
	}
	for i := range plan.Steps {
		plan.Steps[i].ID = strconv.Itoa(i + 1)
		plan.Steps[i].State = "pending"
	}
	return plan, nil
}

func cloudLabel(c Cloud) string {
	if c == GCP {
		return "Google Cloud"
	}
	return "AWS"
}

// placeholder refers to a resource's cloud id, filled in when the step runs.
func placeholder(id string) string { return "{{cloud_id:" + id + "}}" }

// fileArg refers to a step's input, written to a private file when it runs.
const fileArg = "{{input_file}}"

func encodeKey(pub string) string { return base64.StdEncoding.EncodeToString([]byte(pub)) }
