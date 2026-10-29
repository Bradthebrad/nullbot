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

// ValidProject reports a Google Cloud project id NullBot will pass to gcloud.
func ValidProject(p string) bool { return ValidName(p) || projectPattern.MatchString(p) }

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
	// Firewall rules have no labels, so each is named after its NullBot id.
	// The default network lets SSH and RDP in from anywhere; a rule that
	// refuses everything else (priority 1000) sits under the one that lets
	// allow_from in (priority 900) and over the default ones (65534).
	ruleID := NewResourceID()
	rule := add(Resource{ID: ruleID, Name: ruleID, Kind: KindFirewall, Project: spec.Project, Region: spec.Region,
		Details: map[string]string{"ports": joinInts(m.Ports), "allow_from": strings.Join(m.AllowFrom, ",")}})
	denyID := NewResourceID()
	deny := add(Resource{ID: denyID, Name: denyID, Kind: KindFirewall, Project: spec.Project, Region: spec.Region,
		Details: map[string]string{"denies": "everything else"}})
	var allow []string
	for _, p := range m.Ports {
		allow = append(allow, fmt.Sprintf("tcp:%d", p))
	}
	steps := []Step{
		{
			Summary: fmt.Sprintf("Create firewall rule %s: refuse every other connection to %s, including the default network's open SSH", deny.Name, m.Name),
			Argv: append([]string{"gcloud", "compute", "firewall-rules", "create", deny.Name, "--network", "default", "--direction", "INGRESS",
				"--action", "DENY", "--rules", "all", "--source-ranges", "0.0.0.0/0", "--priority", "1000", "--target-tags", rule.ID,
				"--description", "NullBot " + deny.PlanID + " for " + m.Name, "--format", "json", "--quiet"}, project...),
			Resource: deny.ID, Action: "create", Parse: "gcp_firewall",
		},
		{
			Summary: fmt.Sprintf("Create firewall rule %s: TCP %s from %s to %s", rule.Name, joinInts(m.Ports), strings.Join(m.AllowFrom, ", "), m.Name),
			Argv: append([]string{"gcloud", "compute", "firewall-rules", "create", rule.Name, "--network", "default", "--direction", "INGRESS",
				"--allow", strings.Join(allow, ","), "--source-ranges", strings.Join(m.AllowFrom, ","), "--priority", "900", "--target-tags", rule.ID,
				"--description", "NullBot " + rule.PlanID + " for " + m.Name, "--format", "json", "--quiet"}, project...),
			Resource: rule.ID, Action: "create", Parse: "gcp_firewall",
		},
	}
	inst := add(Resource{Kind: KindInstance, Name: m.Name, Project: spec.Project, Region: spec.Region, Zone: spec.Zone,
		CostHourly: cost, DependsOn: []string{rule.ID, deny.ID},
		Details: machineDetails(m)})
	family := "ubuntu-2404-lts-amd64"
	if gcpArmType(m.Type) {
		family = "ubuntu-2404-lts-arm64"
	}
	// No service account: nothing on the machine can use the project's
	// other resources.
	argv := []string{"gcloud", "compute", "instances", "create", m.Name, "--zone", spec.Zone, "--machine-type", m.Type,
		"--image-family", family, "--image-project", "ubuntu-os-cloud", "--boot-disk-size", fmt.Sprintf("%dGB", m.DiskGB),
		"--shielded-secure-boot", "--no-service-account", "--no-scopes", "--labels", gcpLabels(inst), "--tags", rule.ID, "--format", "json", "--quiet"}
	if gcpGPUType(m.Type) {
		argv = append(argv, "--maintenance-policy", "TERMINATE")
	}
	if m.Spot {
		argv = append(argv, "--provisioning-model", "SPOT", "--instance-termination-action", "STOP")
	}
	// Only this machine's key logs in, not keys added to the whole project.
	metadata := []string{"block-project-ssh-keys=TRUE"}
	step := Step{Summary: fmt.Sprintf("Start machine %s (%s, Ubuntu 24.04, %d GB disk) in %s", m.Name, m.Type, m.DiskGB, spec.Zone), Resource: inst.ID, Action: "create", Parse: "gcp_instance"}
	if m.SSHKey != "" {
		metadata = append(metadata, "ssh-keys="+m.SSHUser+":"+m.SSHKey)
		step.Summary += fmt.Sprintf(", letting in SSH key %s", SSHKeyFingerprint(m.SSHKey))
	}
	argv = append(argv, "--metadata", strings.Join(metadata, ","))
	if m.UserData != "" {
		argv = append(argv, "--metadata-from-file", "user-data="+fileArg)
		step.Input = m.UserData
		step.Summary += ", with a first-boot script"
	}
	step.Argv = append(argv, project...)
	return append(steps, step)
}

func gcpBucketSteps(spec Spec, b BucketSpec, add func(Resource) Resource) []Step {
	// Its cloud id is set only when the create step succeeds; Google Cloud
	// refuses to create a bucket that exists, so one is never taken over.
	bucket := add(Resource{Kind: KindBucket, Name: b.Name, Project: spec.Project, Region: spec.Region})
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
	name := r.CloudID
	var steps []Step
	if name == "" {
		switch r.Kind {
		case KindInstance:
			steps = append(steps, Step{Summary: fmt.Sprintf("Look for %s by NullBot's label %s", r.Name, r.ID),
				Argv: append([]string{"gcloud", "compute", "instances", "list", "--filter", "labels.nullbot-id=" + r.ID, "--format", "json"}, project...), Resource: r.ID, Action: "find", Parse: "gcp_find"})
		case KindFirewall:
			steps = append(steps, Step{Summary: fmt.Sprintf("Look for firewall rule %s", r.Name),
				Argv: append([]string{"gcloud", "compute", "firewall-rules", "list", "--filter", "name=" + r.Name, "--format", "json"}, project...), Resource: r.ID, Action: "find", Parse: "gcp_find"})
		default:
			return []Step{{Summary: fmt.Sprintf("Forget %s, which NullBot never created", r.Name), Resource: r.ID, Action: "delete"}}
		}
		name = placeholder(r.ID)
	} else if r.Kind == KindInstance {
		steps = append(steps, Step{Summary: fmt.Sprintf("Check that %s still carries NullBot's label %s", r.Name, r.ID),
			Argv: append([]string{"gcloud", "compute", "instances", "describe", name, "--zone", r.Zone, "--format", "json"}, project...), Resource: r.ID, Action: "verify"})
	}
	switch r.Kind {
	case KindInstance:
		return append(steps, Step{Summary: fmt.Sprintf("Delete machine %s and its disk", r.Name), Argv: append([]string{"gcloud", "compute", "instances", "delete", name, "--zone", r.Zone, "--delete-disks", "all", "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true})
	case KindFirewall:
		return append(steps, Step{Summary: fmt.Sprintf("Delete firewall rule %s", r.Name), Argv: append([]string{"gcloud", "compute", "firewall-rules", "delete", name, "--quiet"}, project...), Resource: r.ID, Action: "delete", IgnoreMissing: true})
	case KindBucket:
		steps = append(steps, Step{Summary: fmt.Sprintf("Check that gs://%s still carries NullBot's label %s", r.Name, r.ID),
			Argv: append([]string{"gcloud", "storage", "buckets", "describe", "gs://" + r.Name, "--format", "json"}, project...), Resource: r.ID, Action: "verify"})
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
