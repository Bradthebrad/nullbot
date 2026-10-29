# Cloud infrastructure

NullBot can create and remove machines and storage on AWS and Google Cloud:
for example a GPU machine that runs a model for a few hours, a box for a
headless NullBot, or a bucket for a job's files. It is built to stay under
your control:

- **Every change is a plan you approve.** A plan lists the exact `aws` or
  `gcloud` commands it will run and roughly what it costs per hour. Nothing
  runs until you approve it, in the app or on the command line. Agents can
  propose plans and run the ones you approved; they cannot approve them.
- **Everything is recorded.** Each proposal, decision and command, with what
  it returned, goes into an append-only ledger. Everything NullBot creates
  is in an inventory, and tagged (labelled on Google Cloud) as NullBot's, so
  it can be found and removed later, even after a plan failed halfway.
- **The official tools, nothing else.** Plans run the AWS CLI v2 and the
  Google Cloud CLI. NullBot adds no SDKs or other dependencies; install the
  CLIs you need.
- **Credentials stay in the vault.** A plan names an `aws` or `gcp` secret
  ([SECRETS.md](SECRETS.md)); its values go into the CLI's environment for
  each command and never into a plan, the ledger or an agent's context. The
  commands run with a private CLI configuration, so your own AWS profiles,
  SSO sessions and gcloud login are never used by mistake.

## Setting up

1. Install the CLI for your cloud: [AWS CLI v2](https://aws.amazon.com/cli/)
   or the [Google Cloud CLI](https://cloud.google.com/sdk/docs/install).
   NullBot finds `aws` and `gcloud` on your `PATH`, or at
   `NULLBOT_AWS_BIN` / `NULLBOT_GCLOUD_BIN`.
2. Add credentials in **Settings > Secrets**: AWS credentials (an access key
   for an IAM user limited to what NullBot should do, with its region), or a
   Google Cloud service account key file (its project is used).
3. Optionally, an SSH key secret for logging in to machines:
   `nullbot secrets ssh-keygen cloud`.
4. To let agents propose plans: **Settings > Infrastructure**, or
   `nullbot infra agents on`. It is off by default.

## A plan

A person writes a spec as JSON; an agent fills in the same fields through
`infra_plan`:

```json
{
  "cloud": "aws",
  "credential": "aws-lab",
  "region": "us-east-1",
  "ssh_key_secret": "cloud",
  "reason": "A GPU box to run Qwen for the eval this afternoon",
  "machines": [{
    "name": "gpu-box",
    "type": "g6.xlarge",
    "disk_gb": 100,
    "allow_from": ["my-ip"],
    "bootstrap": {"gpu_drivers": true, "ollama": true, "ollama_models": ["qwen2.5:32b"]}
  }],
  "buckets": [{"name": "acme-eval-scratch"}]
}
```

```sh
nullbot infra plan --file gpu-box.json      # propose; prints every command
nullbot infra show plan-1a2b3c4d            # look again
nullbot infra approve plan-1a2b3c4d --apply # approve and run
nullbot infra reject plan-1a2b3c4d          # or not
```

What a plan does:

| | AWS | Google Cloud |
| --- | --- | --- |
| Machine | Ubuntu 24.04 (the current image, arm64 for Graviton types), encrypted gp3 disk, IMDSv2 only | Ubuntu 24.04, balanced disk, Shielded VM secure boot |
| Network | a security group of its own in the default VPC | a firewall rule of its own, by network tag |
| Login | your SSH key from the vault, imported as a key pair, user `ubuntu` | your SSH key in the machine's metadata, user `nullbot` |
| Buckets | private (public access blocked), tagged | uniform access, public access prevention, labelled |
| Spare capacity | `"spot": true` | `"spot": true` |

Ports: SSH (22) is always open, and any `ports` you list, **only to the
addresses in `allow_from`**. There is no "open to everyone" default;
`my-ip` is this computer's public address, looked up when the plan is made.

Costs are approximate on-demand list prices in us-east-1 / us-central1,
including the disk, for common machine types. Other regions, data
transfer and storage cost more; check the provider's calculator for
anything that matters.

## First-boot setup

A machine's `bootstrap` writes its first-boot script for you (or give your
own as `user_data`):

| Field | What it does |
| --- | --- |
| `gpu_drivers` | Installs the NVIDIA driver Ubuntu recommends and restarts once |
| `ollama` | Installs Ollama, listening on the machine itself only, with a 16k context (`context` to change it) |
| `ollama_models` | Pulls these models |
| `nullbot` | Installs a headless NullBot (a release from GitHub, checked against its SHA256SUMS), using the machine's Ollama, running `nullbot serve` as a service |
| `nullbot_version` | That release instead of the latest |
| `packages` | Extra Ubuntu packages |

Its progress is in `/var/log/nullbot-bootstrap.log` on the machine;
`nullbot infra inspect` shows it.

## Using a model on a cloud machine

Ollama on a machine NullBot made listens only on the machine. A tunnel
brings it here over SSH:

```sh
nullbot infra connect gpu-box     # until Ctrl+C
nullbot -p "…" --model local:gpu-box/qwen2.5:32b
```

`connect` opens the tunnel and adds the machine as a local model server
(see [LOCAL_MODELS.md](LOCAL_MODELS.md)), so the manager or subagents can
use it. `nullbot infra tunnel MACHINE --port N` forwards any other port.

## What runs where

```sh
nullbot infra                   # everything NullBot made, its addresses and cost per hour
nullbot infra inspect gpu-box   # ports, services, nginx sites, containers, setup, GPU
nullbot infra here              # what listens on this computer
```

`inspect` logs in with the machine's SSH key and runs only read-only
commands (`ss`, `systemctl list-units`, `nginx -T`, `docker ps`, `df`,
`free`, `nvidia-smi`). Each port says whether other machines can reach it.
Host keys are remembered in NullBot's own known_hosts file
(`~/.nullbot/infra/known_hosts`) the first time.

## Removing things

```sh
nullbot infra destroy gpu-box              # a machine and what it used
nullbot infra destroy plan-1a2b3c4d        # everything a plan made
nullbot infra destroy acme-eval-scratch --contents   # a bucket and its files
```

A removal is a plan too. Machines go first, then their security groups,
firewall rules and key pairs. A bucket is removed only if it is empty,
unless you say `--contents`. Something already gone counts as removed.

## Agents

With agent tools on, agents get:

| Tool | |
| --- | --- |
| `infra_status` | what NullBot made, its state and cost, and recent plans |
| `infra_plan` | propose machines or buckets |
| `infra_apply` | wait for your decision, then run an approved plan |
| `infra_destroy` | propose removing resources |
| `infra_inspect` | what a machine, or this computer, runs |

An agent waiting on `infra_apply` continues as soon as you approve, and is
told when you reject. To reach a machine it made, an agent uses
`secret_run` with the SSH key secret.

## Files

```text
~/.nullbot/infra/plans/<id>.json   each plan, with every step as it ran
~/.nullbot/infra/resources.json    what NullBot made, and its state
~/.nullbot/infra/ledger.jsonl      every proposal, decision and command, appended
~/.nullbot/infra/known_hosts       machines' SSH host keys
```

## Limits

- Machines use the default VPC (AWS) or default network (Google Cloud).
- NullBot only removes what it made. It does not track changes you make to
  those resources yourself; `nullbot infra inspect` shows a machine's
  current state.
- The cost estimate covers machines and disks, not traffic, snapshots or
  bucket storage.
- Approving is the control: read the commands before you approve them.
