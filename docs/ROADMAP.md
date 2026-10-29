# Roadmap

What is planned next. None of this is built yet; it is here so the direction
is clear and so the pieces that exist today are designed with it in mind.

The premise stays the same: a small core you can read, agents you can steer,
and every model or tool a choice you make. New powers arrive as providers or
as separate tool servers, under the same per-project permissions.

## Cloud infrastructure

Tool servers that create and tear down infrastructure on AWS and Google Cloud:
start and stop machines, create storage, and run jobs, so an agent can set up
an environment for a task and remove it afterwards. Planned rules:

- every create, change and delete is shown and confirmed before it runs, with
  the expected cost where the provider reports one;
- everything NullBot creates is tagged, so it can be listed and removed;
- credentials come from the vault ([SECRETS.md](SECRETS.md)): an `aws` or
  `gcp` secret is handed to the provider's tools for the call and never shown
  to the agent.
