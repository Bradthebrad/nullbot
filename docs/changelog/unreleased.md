# Unreleased

## Local models

- **Model servers you host** are providers of their own (`local:<id>`):
  Ollama, llama.cpp, LM Studio, vLLM or any OpenAI-compatible server, on
  this computer, another machine or an internal domain. A server's key comes
  from a vault secret.
- **Real context sizes**: NullBot asks each server what context a model runs
  with (Ollama's `num_ctx` or default, llama.cpp's `-c`, LM Studio's loaded
  context, vLLM's `max_model_len`), and plans for 8k until it knows.
- **A run checks its server answers** before it starts.
- **Ollama**: pull models (including `hf.co/...` GGUF builds), delete them,
  and make a copy with a larger context.
- **Install from Hugging Face**: search GGUF models, see each build and its
  size, and install one into Ollama, LM Studio's folder or NullBot's own,
  with resumable, checksum-verified downloads. A token for gated models
  comes from the vault and is sent only to huggingface.co.
- `nullbot models`, `nullbot models local find|add|remove`, `nullbot models
  pull|context|remove|search|builds|install`.
- See [docs/LOCAL_MODELS.md](../LOCAL_MODELS.md).

## Claude CLI provider

- **Use a Claude subscription** by picking a model from the Claude CLI group
  (default, Opus, Sonnet, Haiku). NullBot runs your installed `claude`
  command and never reads its credentials; `/accounts` shows whether it is
  signed in.
- **Claude's text, thinking, commentary and tool steps stream into the
  chat**, redacted as they arrive, and its tool steps show in the activity
  view.
- **The project's permission picks Claude's tools**: read-only gets reading
  and search, read & write adds edits, full access adds Bash. No MCP
  servers or project hooks are loaded, and the CLI gets an environment
  without credentials.
- **Conversations continue** the same CLI session, sending only what is
  new.
- See [docs/CLAUDE_CLI.md](../CLAUDE_CLI.md).

## Chats

- **Several chats at once.** A `Hub` runs chats that share the configuration,
  projects, tool servers, schedule and vault; each has its own history file,
  queue, run controls and subagents. Activity records carry their chat.
- **The main chat steers the others** with `chats_list`, `chat_read`,
  `chat_send` (send, queue, steer, or steer one subagent), `chat_control` and
  `chat_open`.
- **Open chats come back** after a restart (`chats.json`), each continuing
  its own history. History file names stay unique when chats start in the
  same second.
- See [docs/CHATS.md](../CHATS.md).

## Secrets vault

- **One encrypted vault** at `~/.nullbot/vault/vault.json` (AES-256-GCM)
  holds API keys, the ChatGPT sign-in, GitHub tokens, SSH keys, AWS and
  Google Cloud credentials and any other secret. Its key is protected by
  Windows DPAPI, the macOS keychain, the Linux Secret Service or, on servers,
  a passphrase from `NULLBOT_VAULT_PASSPHRASE` or
  `NULLBOT_VAULT_PASSPHRASE_FILE`, which NullBot removes from its environment
  at start.
- **Plain-text keys move in.** `api/keys.json` and `api/codex_auth.json` are
  moved into the vault, overwritten and deleted the first time the vault can
  be written. Systems with no keychain and no passphrase keep using them, and
  `nullbot vault` says so.
- **Agents use secrets without seeing them.** `secrets_list` shows names,
  kinds and what each provides; `secret_run` runs a command with chosen
  secrets in its environment (GitHub tokens sign git in over HTTPS, SSH keys
  become `GIT_SSH_COMMAND`, cloud keys become their usual variables), never
  in its arguments, stops anything the command leaves running, and removes
  the values from what it prints. `save_output_as` keeps a credential
  a command creates as a new secret instead of showing it. A secret is usable
  by agents only once you allow it.
- **Every tool result is filtered** for saved secret values (as written,
  JSON-escaped, Base64 and line by line for private keys) before it reaches a
  model, and so are Codex answers and tool servers' error output. Agents
  cannot read the vault or `api/` with NullBot's file tools.
- **Tool servers** can be given secrets by ID; they get them in their
  environment when they start and restart when they change.
- **GitHub:** check a token (login and scopes), generate an Ed25519 SSH key or
  import one, and add its public key to your GitHub account.
- **Command line:** `nullbot vault` and `nullbot secrets` (`set`, `remove`,
  `ssh-keygen`, `github`, `github-add-key`, `kinds`); `/secrets` in the
  terminal UI. A passphrase vault asks for its passphrase before the terminal
  UI opens.
- **Masked keys** in Settings and the terminal UI show nothing of a key of
  12 characters or fewer, and a mask sent back never replaces a key.

See [docs/SECRETS.md](../SECRETS.md).
