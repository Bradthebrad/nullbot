package infra

import (
	"fmt"
	"strings"
)

// AWS through the aws command (AWS CLI v2). Every resource gets these tags,
// so it can be found again: nullbot-managed=true, nullbot-id, nullbot-plan.

func awsTags(resourceType string, r Resource, name string) string {
	tags := []string{
		"{Key=nullbot-managed,Value=true}",
		"{Key=nullbot-id,Value=" + r.ID + "}",
		"{Key=nullbot-plan,Value=" + r.PlanID + "}",
	}
	if name != "" {
		tags = append(tags, "{Key=Name,Value="+name+"}")
	}
	return "ResourceType=" + resourceType + ",Tags=[" + strings.Join(tags, ",") + "]"
}

// armType reports a Graviton (arm64) instance type: its family ends in g
// (t4g, c7g, m7g, r8g, g5g) or gd/gn/ge.
func armType(t string) bool {
	family, _, _ := strings.Cut(t, ".")
	if len(family) < 3 {
		return false
	}
	rest := strings.TrimLeft(family[1:], "0123456789")
	return strings.HasPrefix(rest, "g")
}

func awsUbuntuImage(t string) string {
	arch := "amd64"
	if armType(t) {
		arch = "arm64"
	}
	// EC2 resolves this public SSM parameter to the current Ubuntu 24.04 AMI
	// in the region, so no image id has to be looked up first.
	return "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/" + arch + "/hvm/ebs-gp3/ami-id"
}

func awsMachineSteps(spec Spec, m MachineSpec, add func(Resource) Resource, cost float64) []Step {
	region := []string{"--region", spec.Region}
	var steps []Step
	var keyName string
	var deps []string
	if m.SSHKey != "" {
		key := add(Resource{Kind: KindKeyPair, Name: "nb-" + m.Name, Region: spec.Region})
		keyName = key.Name
		deps = append(deps, key.ID)
		steps = append(steps, Step{
			Summary:  fmt.Sprintf("Upload the SSH public key for %s (%s) as key pair %s", m.Name, SSHKeyFingerprint(m.SSHKey), key.Name),
			Argv:     append([]string{"aws", "ec2", "import-key-pair", "--key-name", key.Name, "--public-key-material", encodeKey(m.SSHKey), "--tag-specifications", awsTags("key-pair", key, ""), "--output", "json"}, region...),
			Resource: key.ID, Action: "create", Parse: "aws_key_pair",
		})
	}
	sg := add(Resource{Kind: KindSecurityGroup, Name: "nb-" + m.Name, Region: spec.Region, Details: map[string]string{"ports": joinInts(m.Ports), "allow_from": strings.Join(m.AllowFrom, ",")}})
	deps = append(deps, sg.ID)
	steps = append(steps, Step{
		Summary:  fmt.Sprintf("Create security group %s in the default network", sg.Name),
		Argv:     append([]string{"aws", "ec2", "create-security-group", "--group-name", sg.Name, "--description", "NullBot " + sg.PlanID + " for " + m.Name, "--tag-specifications", awsTags("security-group", sg, sg.Name), "--output", "json"}, region...),
		Resource: sg.ID, Action: "create", Parse: "aws_security_group",
	})
	var perms []string
	for _, port := range m.Ports {
		var ranges []string
		for _, cidr := range m.AllowFrom {
			ranges = append(ranges, "{CidrIp="+cidr+"}")
		}
		perms = append(perms, fmt.Sprintf("IpProtocol=tcp,FromPort=%d,ToPort=%d,IpRanges=[%s]", port, port, strings.Join(ranges, ",")))
	}
	steps = append(steps, Step{
		Summary:  fmt.Sprintf("Open TCP %s to %s", joinInts(m.Ports), strings.Join(m.AllowFrom, ", ")),
		Argv:     append(append([]string{"aws", "ec2", "authorize-security-group-ingress", "--group-id", placeholder(sg.ID), "--ip-permissions"}, perms...), append([]string{"--output", "json"}, region...)...),
		Resource: sg.ID, Action: "change",
	})
	inst := add(Resource{Kind: KindInstance, Name: m.Name, Region: spec.Region, CostHourly: cost, DependsOn: deps,
		Details: machineDetails(m)})
	argv := []string{"aws", "ec2", "run-instances", "--image-id", awsUbuntuImage(m.Type), "--instance-type", m.Type, "--count", "1",
		"--security-group-ids", placeholder(sg.ID),
		"--block-device-mappings", fmt.Sprintf("DeviceName=/dev/sda1,Ebs={VolumeSize=%d,VolumeType=gp3,DeleteOnTermination=true,Encrypted=true}", m.DiskGB),
		"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
		"--tag-specifications", awsTags("instance", inst, m.Name), awsTags("volume", inst, m.Name),
		"--output", "json"}
	if keyName != "" {
		argv = append(argv, "--key-name", keyName)
	}
	if m.Spot {
		argv = append(argv, "--instance-market-options", "MarketType=spot")
	}
	step := Step{
		Summary:  fmt.Sprintf("Start machine %s (%s, Ubuntu 24.04, %d GB disk)", m.Name, m.Type, m.DiskGB),
		Resource: inst.ID, Action: "create", Parse: "aws_instance",
	}
	if m.UserData != "" {
		argv = append(argv, "--user-data", "file://"+fileArg)
		step.Input = m.UserData
		step.Summary += ", with a first-boot script"
	}
	step.Argv = append(argv, region...)
	steps = append(steps, step,
		Step{Summary: fmt.Sprintf("Wait for %s to be running", m.Name), Argv: append([]string{"aws", "ec2", "wait", "instance-running", "--instance-ids", placeholder(inst.ID)}, region...), Resource: inst.ID, Action: "wait"},
		Step{Summary: fmt.Sprintf("Read %s's address", m.Name), Argv: append([]string{"aws", "ec2", "describe-instances", "--instance-ids", placeholder(inst.ID),
			"--query", "Reservations[0].Instances[0].{public_ip:PublicIpAddress,private_ip:PrivateIpAddress,state:State.Name}", "--output", "json"}, region...),
			Resource: inst.ID, Action: "read", Parse: "aws_instance_details"},
	)
	return steps
}

func awsBucketSteps(spec Spec, b BucketSpec, add func(Resource) Resource) []Step {
	// Its cloud id is set only when the create step succeeds, so a bucket
	// that already existed is never taken over or removed.
	bucket := add(Resource{Kind: KindBucket, Name: b.Name, Region: spec.Region})
	create := []string{"aws", "s3api", "create-bucket", "--bucket", b.Name, "--region", spec.Region, "--output", "json"}
	if spec.Region != "us-east-1" {
		create = append(create, "--create-bucket-configuration", "LocationConstraint="+spec.Region)
	}
	tags := fmt.Sprintf("TagSet=[{Key=nullbot-managed,Value=true},{Key=nullbot-id,Value=%s},{Key=nullbot-plan,Value=%s}]", bucket.ID, bucket.PlanID)
	return []Step{
		// In us-east-1, creating a bucket you already own succeeds quietly;
		// checking first keeps an existing bucket from being adopted.
		{Summary: fmt.Sprintf("Check that there is no bucket named %s yet", b.Name), Argv: []string{"aws", "s3api", "head-bucket", "--bucket", b.Name, "--region", spec.Region},
			Resource: bucket.ID, Action: "absent"},
		{Summary: fmt.Sprintf("Create bucket %s in %s", b.Name, spec.Region), Argv: create, Resource: bucket.ID, Action: "create", Parse: "bucket"},
		{Summary: fmt.Sprintf("Keep %s private", b.Name), Argv: []string{"aws", "s3api", "put-public-access-block", "--bucket", b.Name, "--region", spec.Region,
			"--public-access-block-configuration", "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"}, Resource: bucket.ID, Action: "change"},
		{Summary: fmt.Sprintf("Tag %s as NullBot's", b.Name), Argv: []string{"aws", "s3api", "put-bucket-tagging", "--bucket", b.Name, "--region", spec.Region, "--tagging", tags}, Resource: bucket.ID, Action: "change"},
	}
}

func awsDeleteSteps(r Resource, deleteContents bool) []Step {
	region := []string{"--region", r.Region}
	id := r.CloudID
	if id == "" {
		find := awsFindStep(r)
		if find == nil {
			// A bucket's id is its name, recorded only when NullBot created
			// it; without one, it was never created by NullBot.
			return []Step{{Summary: fmt.Sprintf("Forget %s, which NullBot never created", r.Name), Resource: r.ID, Action: "delete"}}
		}
		// Its id was never recorded (the plan stopped while it ran): look
		// for it by NullBot's tag, and remove it only if it is there.
		id = placeholder(r.ID)
		return append([]Step{*find}, awsDeleteByID(r, id, region, deleteContents)...)
	}
	return awsDeleteByID(r, id, region, deleteContents)
}

func awsFindStep(r Resource) *Step {
	region := []string{"--region", r.Region}
	tag := "Name=tag:nullbot-id,Values=" + r.ID
	var argv []string
	switch r.Kind {
	case KindInstance:
		argv = []string{"aws", "ec2", "describe-instances", "--filters", tag, "Name=instance-state-name,Values=pending,running,stopping,stopped", "--query", "Reservations[].Instances[].InstanceId", "--output", "json"}
	case KindSecurityGroup:
		argv = []string{"aws", "ec2", "describe-security-groups", "--filters", tag, "--query", "SecurityGroups[].GroupId", "--output", "json"}
	case KindKeyPair:
		argv = []string{"aws", "ec2", "describe-key-pairs", "--filters", tag, "--query", "KeyPairs[].KeyPairId", "--output", "json"}
	default:
		return nil
	}
	return &Step{Summary: fmt.Sprintf("Look for %s by NullBot's tag %s", r.Name, r.ID), Argv: append(argv, region...), Resource: r.ID, Action: "find", Parse: "aws_find"}
}

func awsDeleteByID(r Resource, id string, region []string, deleteContents bool) []Step {
	switch r.Kind {
	case KindInstance:
		return []Step{
			{Summary: fmt.Sprintf("Terminate machine %s; its disk is deleted with it", r.Name), Argv: append([]string{"aws", "ec2", "terminate-instances", "--instance-ids", id, "--output", "json"}, region...), Resource: r.ID, Action: "delete", IgnoreMissing: true},
			{Summary: fmt.Sprintf("Wait for %s to be gone", r.Name), Argv: append([]string{"aws", "ec2", "wait", "instance-terminated", "--instance-ids", id}, region...), Resource: r.ID, Action: "wait", IgnoreMissing: true},
		}
	case KindSecurityGroup:
		return []Step{{Summary: fmt.Sprintf("Delete security group %s", r.Name), Argv: append([]string{"aws", "ec2", "delete-security-group", "--group-id", id}, region...), Resource: r.ID, Action: "delete", IgnoreMissing: true}}
	case KindKeyPair:
		return []Step{{Summary: fmt.Sprintf("Delete key pair %s", r.Name), Argv: append([]string{"aws", "ec2", "delete-key-pair", "--key-pair-id", id}, region...), Resource: r.ID, Action: "delete", IgnoreMissing: true}}
	case KindBucket:
		argv := []string{"aws", "s3", "rb", "s3://" + r.Name, "--region", r.Region}
		summary := fmt.Sprintf("Delete bucket %s, if it is empty", r.Name)
		if deleteContents {
			argv = append(argv, "--force")
			summary = fmt.Sprintf("Delete bucket %s and everything in it", r.Name)
		}
		return []Step{
			{Summary: fmt.Sprintf("Check that %s still carries NullBot's tag %s", r.Name, r.ID), Argv: []string{"aws", "s3api", "get-bucket-tagging", "--bucket", r.Name, "--region", r.Region, "--output", "json"},
				Resource: r.ID, Action: "verify"},
			{Summary: summary, Argv: argv, Resource: r.ID, Action: "delete", IgnoreMissing: true},
		}
	}
	return nil
}

// machineDetails are what the inventory shows about a machine: its size,
// login, open ports and what its first boot sets up.
func machineDetails(m MachineSpec) map[string]string {
	d := map[string]string{"type": m.Type, "disk_gb": fmt.Sprint(m.DiskGB), "ssh_user": m.SSHUser, "ports": joinInts(m.Ports), "allow_from": strings.Join(m.AllowFrom, ",")}
	if m.Spot {
		d["spot"] = "yes"
	}
	if b := m.Bootstrap; b != nil {
		var setup []string
		if b.GPUDrivers {
			setup = append(setup, "gpu_drivers")
		}
		if b.Ollama {
			setup = append(setup, "ollama")
		}
		if b.NullBot {
			setup = append(setup, "nullbot")
		}
		if len(setup) > 0 {
			d["setup"] = strings.Join(setup, ",")
		}
		if len(b.OllamaModels) > 0 {
			d["models"] = strings.Join(b.OllamaModels, ",")
		}
	} else if m.UserData != "" {
		d["setup"] = "user_data"
	}
	return d
}

func joinInts(ints []int) string {
	parts := make([]string, len(ints))
	for i, n := range ints {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ",")
}
