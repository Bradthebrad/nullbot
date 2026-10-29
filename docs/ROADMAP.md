# Roadmap

What is planned next. None of this is built yet; it is here so the direction
is clear and so the pieces that exist today are designed with it in mind.

The premise stays the same: a small core you can read, agents you can steer,
and every model or tool a choice you make. New powers arrive as providers or
as separate tool servers, under the same per-project permissions.

## Remote nodes

Drive a headless NullBot on another machine (one NullBot made in the cloud,
or any server) from the desktop app: send it requests, watch its activity,
steer and stop its runs, over an SSH connection rather than an open port.
Today a node runs `nullbot serve` and is reached with `ssh` and
`nullbot infra connect` for its model.

## OpenTofu for larger setups

Plans cover machines, firewall rules, key pairs and buckets through the
official CLIs. For setups with many parts (networks, databases, load
balancers), an OpenTofu configuration could be one step of a plan: shown
with `tofu plan`'s output, approved like any other plan, applied, and torn
down with the rest, its state kept in NullBot's infrastructure folder.

## More of the cloud

Managed databases, DNS records and scheduled start/stop for machines, in the
same plan-approve-ledger form.
