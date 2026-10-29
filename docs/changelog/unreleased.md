# Unreleased

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

See [docs/SECRETS.md](../SECRETS.md).
