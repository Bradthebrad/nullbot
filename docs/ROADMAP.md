# Roadmap

What is planned next. None of this is built yet; it is here so the direction
is clear and so the pieces that exist today are designed with it in mind.

The premise stays the same: a small core you can read, agents you can steer,
and every model or tool a choice you make. New powers arrive as providers or
as separate tool servers, under the same per-project permissions.

## Local models

Run the manager or any subagent on a model you host:

- on this computer (`localhost`): Ollama, llama.cpp's server, LM Studio, vLLM
  and other OpenAI-compatible servers;
- on another machine on your network, by address;
- on an internal domain, such as a GPU box behind your company's DNS.

tinychain's OpenAI client already takes any base URL, so the work is in
NullBot: a provider entry with a base URL and an optional key, listing the
models the server offers, finding each model's context window, and checking
the endpoint is reachable before a run starts. Local and remote endpoints
will be set per agent, so a small local model can do the reading while a
hosted one plans.

## Model installs: llmfit and Hugging Face

Pick and install a local model from inside NullBot:

- [llmfit](https://github.com/AlexsJones/llmfit) looks at the machine's CPU,
  memory and GPU and ranks which models will actually run well on it. NullBot
  would show that ranking and install the choice through the runtime you use
  (Ollama or llama.cpp first).
- Hugging Face downloads for models that aren't in a runtime's catalog, such
  as a specific GGUF build, with the file checked before it is used.

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

## Also queued

- A Claude Code provider that runs your own installed `claude` command with
  your own sign-in and streams what it does into NullBot.
