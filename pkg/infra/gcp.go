package infra

import (
	"fmt"
	"regexp"
	"strings"
)

// Google Cloud through the gcloud command. Machines and buckets carry
// labels (nullbot-managed, nullbot-id, nullbot-plan); firewall rules, which
// cannot, are named after their NullBot id and say so in their description.

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func gcpLabels(r Resource) string {
	return "nullbot-managed=true,nullbot-id=" + r.ID + ",nullbot-plan=" + r.PlanID
}

// gcpArmType reports an Arm machine type (Tau T2A, Axion C4A).
func gcpArmType(t string) bool {
	return strings.HasPrefix(t, "t2a-") || strings.HasPrefix(t, "c4a-")
}

// gcpGPUType reports a machine type with GPUs attached, which must stop
// rather than move during host maintenance.
func gcpGPUType(t string) bool {
	for _, prefix := range []string{"g2-", "a2-", "a3-", "a4-"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func gcpMachineSteps(spec Spec, m MachineSpec, add func(Resource) Resource, cost float64) []Step {
	project := []string{"--project", spec.Project}
	// A firewall rule has no labels, so its name is its NullBot id.
	ruleID := NewResourceID()
	rule := add(Resource{ID: ruleID, Name: ruleID, CloudID: ruleID, Kind: KindFirewall, Project: spec.Project, Region: spec.Region,
		Details: map[string]string{"ports": joinInts(m.Ports), "allow_from": strings.Join(m.AllowFrom, ",")}})
	var allow []string
	for _, p := range m.Ports {
		allow = append(allow, fmt.Sprintf("tcp:%d", p))
	}
	steps := []Step{{
		Summary: fmt.Sprintf("Create firewall rule %s: TCP %s from %s to %s", rule.Name, joinInts(m.Ports), strings.Join(m.AllowFrom, ", "), m.Name),
		Argv: append([]string{"gcloud", "compute", "firewall-rules", "create", rule.Name, "--network", "default", "--direction", "INGRESS",
			"--allow", strings.Join(allow, ","), "--source-ranges", strings.Join(m.AllowFrom, ","), "--target-tags", rule.ID,
			"--description", "NullBot " + rule.PlanID + " for " + m.Name, "--format", "json", "--quiet"}, project...),
		Resource: rule.ID, Action: "create", Parse: "gcp_firewall",
	}}
	inst := add(Resource{Kind: KindInstance, Name: m.Name, CloudID: m.Name, Project: spec.Project, Region: spec.Region, Zone: spec.Zone,
		CostHourly: cost, DependsOn: []string{rule.ID},
		Details: map[string]string{"type": m.Type, "disk_gb": fmt.Sprint(m.DiskGB), "ssh_user": m.SSHUser, "ports": joinInts(m.Ports)}})
	family := "ubuntu-2404-lts-amd64"
	if gcpArmType(m.Type) {
		family = "ubuntu-2404-lts-arm64"
	}
	argv := []string{"gcloud", "compute", "instances", "create", m.Name, "--zone", spec.Zone, "--machine-type", m.Type,
		"--image-family", family, "--image-project", "ubuntu-os-cloud", "--boot-disk-size", fmt.Sprintf("%dGB", m.DiskGB),
		"--shielded-secure-boot", "--labels", gcpLabels(inst), "--tags", rule.ID, "--format", "json", "--quiet"}
	if gcpGPUType(m.Type) {
		argv = append(argv, "--maintenance-policy", "TERMINATE")
	}
	if m.Spot {
		argv = append(argv, "--provisioning-model", "SPOT", "--instance-termination-action", "STOP")
	}
	var metadata []string
	if m.SSHKey != "" {
		metadata = append(metadata, "ssh-keys="+m.SSHUser+":"+m.SSHKey)
	}
	step := Step{Summary: fmt.Sprintf("Start machine %s (%s, Ubuntu 24.04, %d GB disk) in %s", m.Name, m.Type, m.DiskGB, spec.Zone), Resource: inst.ID, Action: "create", Parse: "gcp_instance"}
	if len(metadata) > 0 {
		argv = append(argv, "--metadata", strings.Join(metadata, ","))
	}
	if m.UserData != "" {
		argv = append(argv, "--metadata-from-file", "user-data="+fileArg)
		step.Input = m.UserData
		step.Summary += ", with a first-boot script"
	}
	step.Argv = append(argv, project...)
	return append(steps, step)
}

func gcpBucketSteps(spec Spec, b BucketSpec, add func(Resource) Resource) []Step {
	bucket := add(Resource{Kind: KindBucket, Name: b.Name, CloudID: b.Name, Project: spec.Project, Region: spec.Region})
	return []Step{
		{Summary: fmt.Sprintf("Create bucket gs://%s in %s, private", b.Name, spec.Region), Argv: []string{"gcloud", "storage", "buckets", "create", "gs://" + b.Name,
			"--location", spec.Region, "--uniform-bucket-level-access", "--public-access-prevention", "--format", "json", "--project", spec.Project},
			Resource: bucket.ID, Action: "create", Parse: "bucket"},
		{Summary: fmt.Sprintf("Label gs://%s as NullBot's", b.Name), Argv: []string{"gcloud", "storage", "buckets", "update", "gs://" + b.Name, "--update-labels", gcpLabels(bucket), "--project", spec.Project},
			Resource: bucket.ID, Action: "change"},
	}
}

func gcpDeleteSteps(r Resource, deleteContents bool) []Step {
	project := []string{"--project", r.Project}
	switch r.Kind {
	case KindInstance:
		return []Step{{Summary: fmt.Sprintf("Delete machine %s and its disk", r.Name), Argv: append([]string{"gcloud", "compute", "instances", "delete", r.CloudID, "--zone", r.Zone, "--delete-disks", "all", "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true}}
	case KindFirewall:
		return []Step{{Summary: fmt.Sprintf("Delete firewall rule %s", r.Name), Argv: append([]string{"gcloud", "compute", "firewall-rules", "delete", r.CloudID, "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true}}
	case KindBucket:
		var steps []Step
		if deleteContents {
			steps = append(steps, Step{Summary: fmt.Sprintf("Delete everything in gs://%s", r.Name), Argv: append([]string{"gcloud", "storage", "rm", "--recursive", "gs://" + r.Name + "/**", "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true})
		}
		summary := fmt.Sprintf("Delete bucket gs://%s, if it is empty", r.Name)
		if deleteContents {
			summary = fmt.Sprintf("Delete bucket gs://%s", r.Name)
		}
		return append(steps, Step{Summary: summary, Argv: append([]string{"gcloud", "storage", "buckets", "delete", "gs://" + r.Name, "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true})
	}
	return nil
}
