# Local models

Run the manager or any subagent on a model you host: on this computer, on
another machine on your network, or behind an internal domain. NullBot works
with:

| Server | Usual address | Notes |
| --- | --- | --- |
| [Ollama](https://ollama.com) | `http://localhost:11434` | NullBot can pull, copy and delete its models |
| llama.cpp's `llama-server` | `http://localhost:8080` | start it with `--jinja` so tool calls work |
| [LM Studio](https://lmstudio.ai) | `http://localhost:1234` | turn on its local server |
| [vLLM](https://docs.vllm.ai) | `http://localhost:8000` | start it with tool calling enabled for your model |
| any OpenAI-compatible server | its address | listed through `/v1/models` |

## Adding a server

In the desktop app: **Settings > Models > Model servers**. **Find servers on
this computer** looks at the usual addresses. **Add a server** takes any
address, such as `http://gpu-box.lan:8000` or
`https://llm.internal.example.com`.

From the command line:

```sh
nullbot models local find                       # what is running here
nullbot models local add ollama                 # at its usual address
nullbot models local add vllm --id gpu --url http://gpu-box.lan:8000 --key-secret gpu-key
nullbot models local                            # each server, whether it answers, its models
nullbot models local remove gpu
```

Each server becomes a provider named `local:<id>`, and its models join the
model lists. Use one for a single headless run with
`--model local:ollama/qwen2.5:7b`.

A server started with an API key reads it from a vault secret (`--key-secret`
names an API key or generic secret; see [SECRETS.md](SECRETS.md)). The key is
never written to the configuration. When a key would go to another machine
over plain `http`, NullBot warns you, since others on the network could read
it.

## Context windows

A local server often runs a model with a much smaller context than the model
supports, and NullBot's instructions and tool list alone take several
thousand tokens. NullBot asks each server what it actually uses:

- **Ollama**: the model's `num_ctx` if it has one, otherwise Ollama's default
  (4,096 unless `OLLAMA_CONTEXT_LENGTH` is set). **Make a 32k copy** in the
  app, or `nullbot models context MODEL --size 32768`, creates an Ollama copy
  of the model with a larger context.
- **llama.cpp**: the context the server was started with (`-c`).
- **LM Studio**: the context the model is loaded with, or its maximum when
  it is not loaded yet.
- **vLLM**: `max_model_len`.

The size is checked when the model lists are refreshed and before a run
starts. A model NullBot has not been able to ask about is planned with an
8,192-token context. As with any model, you can override the size in
Settings > Models.

## Before a run

A run on a local model starts only if its server answers (checked at most
every 30 seconds). If it does not, the run fails at once with the address and
the error, instead of waiting on a timeout.

## Pulling Ollama models

```sh
nullbot models pull qwen2.5:7b
nullbot models pull hf.co/bartowski/Qwen2.5-7B-Instruct-GGUF:Q4_K_M
nullbot models remove qwen2.5:7b
```

In the app, each Ollama server has a **Pull** field with live progress and a
Cancel button. Ollama pulls GGUF builds from Hugging Face directly with the
`hf.co/owner/repo:QUANT` form.

## Tool calls

NullBot's agents work through tool calls, so pick a model that supports
them. Ollama lists `tools` among a model's capabilities, and NullBot shows it
next to the model. Small models follow tool instructions less reliably; a
local model often works best as a subagent for reading and summarizing while
a larger model plans.
