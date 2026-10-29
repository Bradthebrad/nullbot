# Secrets

NullBot keeps every credential in one encrypted vault: model API keys, the
ChatGPT sign-in, GitHub tokens, SSH keys, AWS and Google Cloud credentials,
and anything else you add. Agents can use a secret without ever seeing it.

## Where secrets are kept

```text
~/.nullbot/vault/vault.json
```

The file holds a small header and your secrets encrypted with AES-256-GCM
under a random 256-bit key. That key is never written to disk in the clear.
It is protected by:

| System | Protection |
| --- | --- |
| Windows | DPAPI for your Windows account; only you, on this computer, can open it |
| macOS | your login keychain (through the `security` tool) |
| Linux desktop | the Secret Service: GNOME Keyring or KWallet (through `secret-tool`) |
| Servers and anything else | a passphrase, stretched with PBKDF2-SHA256 (600,000 rounds) |

The header names the protection and binds it to the encrypted data, so
editing the file makes it unreadable instead of quietly changing it. Writes go
through a temporary file and a rename, so a crash leaves the old vault or the
new one, never half of one. A vault file or folder that is a symbolic link is
refused.

Older versions kept API keys in `api/keys.json` and the ChatGPT sign-in in
`api/codex_auth.json` in plain text. NullBot moves both into the vault the
first time it can, overwrites the old files and deletes them. On a system
with no keychain and no passphrase configured, those two files stay where they
are and keep working, and NullBot says so (`nullbot vault`, Settings >
Secrets). Every other kind of secret needs the vault.

### Servers: a passphrase

On a machine without a keychain, give NullBot the passphrase in one of:

- `NULLBOT_VAULT_PASSPHRASE`: the passphrase itself;
- `NULLBOT_VAULT_PASSPHRASE_FILE`: a file holding it, such as a systemd
  credential (`LoadCredential=` puts it under `$CREDENTIALS_DIRECTORY`).

NullBot reads the variable at start and removes it from its own environment,
so no command or tool server it starts inherits it. In the terminal UI, a
locked vault asks for its passphrase before the UI opens; the desktop app
asks in Settings > Secrets.

```sh
nullbot vault init --passphrase     # make a passphrase vault (asks twice)
nullbot vault passphrase            # change the passphrase
nullbot vault keychain              # move to this system's keychain
nullbot vault                       # how it is protected, and what is in plain text
```

## Kinds of secret

| Kind | Fields | A command gets |
| --- | --- | --- |
| `api_key` | provider, key | `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, `GOOGLE_API_KEY` and `GEMINI_API_KEY`, or `<PROVIDER>_API_KEY` |
| `github_token` | token, host | `GH_TOKEN`, `GITHUB_TOKEN`, and git over HTTPS signed in for that host only |
| `ssh_key` | private key (no passphrase), host | `GIT_SSH_COMMAND` and `NULLBOT_SSH_KEY_FILE`, a temporary key file |
| `aws` | access key ID, secret access key, session token, region | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION`, `AWS_DEFAULT_REGION` |
| `gcp` | service account key (JSON), project | `GOOGLE_APPLICATION_CREDENTIALS` and `CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` (a temporary file), `GOOGLE_CLOUD_PROJECT`, `CLOUDSDK_CORE_PROJECT` |
| `generic` | value, variable name, as a file or not | the variable you name |

An API key named after its provider (`openai`, `anthropic`, `brave`) is the
one NullBot itself uses for that provider; these are what Settings > Accounts
saves. Other API keys (`openai-work`) are only used where you choose.

Variables that change how programs run (`PATH`, `LD_PRELOAD`, `NODE_OPTIONS`,
`GIT_SSH_COMMAND` and similar) and names starting with `NULLBOT_` cannot be
set by a secret.

## How agents use secrets

Agents have two tools:

- `secrets_list` shows each secret's name, kind, the variables it provides
  and public details such as a GitHub login, an SSH public key or a region.
  It never shows a value.
- `secret_run` runs a command with the secrets it names in its environment,
  in a project whose permission allows running commands.

What `secret_run` does:

1. Checks that every named secret exists and that you allowed agents to use
   it (off by default; turn it on per secret).
2. Builds the command's environment from NullBot's, minus anything that looks
   like a credential (variables with `TOKEN`, `SECRET`, `PASSWORD`, `_API_KEY`
   and so on, every `AWS_` variable and the vault passphrase), plus the
   secrets' variables.
3. For a GitHub token, adds a git credential helper for that host through
   `GIT_CONFIG_COUNT`: the helpers configured elsewhere are cleared for that
   host, and the helper reads the token from the environment, so the token is
   never on a command line. `GIT_TERMINAL_PROMPT=0` stops git waiting for a
   password.
4. Writes SSH keys and key files to a private temporary folder (mode 0700,
   files 0600), and overwrites and deletes them when the command ends.
5. Expands `${VAR}` in the arguments for the secrets' variables only, so a
   request can say `curl -H "Authorization: Bearer ${GH_TOKEN}"` without the
   value appearing in the conversation.
6. Runs the command with a time limit (300 seconds by default, at most an
   hour) and stops its whole process tree at the limit.
7. Removes every secret value from what the command printed, including the
   Base64 form, before the agent sees it, and records the use in the activity
   log (secret names, project and program, never values).

`save_output_as` stores what a command prints as a new secret instead of
returning it, for credentials a command creates (an access key, a generated
password). An agent can only overwrite secrets agents made.

### Every tool result is checked

Every tool's result, built in or from a tool server, passes through the same
filter before it reaches a model: saved secret values, the ChatGPT sign-in,
provider keys from the environment and the vault passphrase are replaced with
`[secret:name]`. So reading a file that happens to contain a saved key shows
the placeholder, not the key. The tool servers' error output shown in Tools is
filtered the same way.

## Tool servers

A secret can be given to tool servers by ID (Settings > Secrets, or
`--server ID`). The server gets the secret's variables in its environment each
time it starts, and restarts when the secret changes. A server you have not
named never gets it. SSH keys, Google Cloud key files and other file secrets
cannot be given to servers; use them with `secret_run`.

## GitHub

- **Token**: save a fine-grained or classic personal access token. Settings >
  Secrets > Check (or `nullbot secrets github NAME`) asks GitHub who it
  belongs to and records the login and, for classic tokens, its scopes.
  Changing the token forgets the old login until it is checked again.
- **SSH key**: generate an Ed25519 key in NullBot (`nullbot secrets ssh-keygen
  NAME`), or import one without a passphrase (OpenSSH, PKCS#1, PKCS#8 or EC).
  NullBot shows the public key and fingerprint. With a token that may write
  public keys (`admin:public_key`, or "Git SSH keys" for fine-grained tokens),
  Settings > Secrets > Add to GitHub (`nullbot secrets github-add-key KEY
  TOKEN`) uploads the public key for you.

git over SSH uses `StrictHostKeyChecking=accept-new`: the first connection to
a host records its key in your `known_hosts`, and a changed key afterwards is
refused.

## The command line

```sh
nullbot secrets                                     # list
nullbot secrets kinds                               # kinds and fields
nullbot secrets set github --kind github_token --agent
nullbot secrets set deploy --kind ssh_key --file private_key=$HOME/.ssh/nullbot --agent
nullbot secrets set aws-prod --kind aws --set region=us-east-1 --agent
nullbot secrets set gcp --kind gcp --file credentials_json=key.json
nullbot secrets set registry --kind generic --set env=REGISTRY_TOKEN --server my-mcp
nullbot secrets remove aws-prod
```

At a terminal, values are asked for without echo. From a pipe, a kind with one
value reads it from standard input; a kind with several reads a JSON object
(`{"access_key_id": "...", "secret_access_key": "..."}`).

## Limits

- A command given a secret can send it anywhere it can reach. Allow agents to
  use a secret only where you would let them act with it, and prefer narrowly
  scoped tokens (fine-grained GitHub tokens, IAM users limited to what the
  task needs).
- The output filter replaces exact values and their Base64 forms. A command
  that prints a secret transformed some other way (hex, reversed, split)
  defeats it.
- Short values (under eight characters) are not filtered vault-wide, only in
  the output of the command they were given to.
- The overwrite before deleting temporary and legacy files does not reach
  every disk: SSDs and copy-on-write file systems may keep old blocks.
- Anyone who can run programs as you on an unlocked session can ask the
  keychain for the vault key, as they can for your browser's passwords. The
  vault protects secrets at rest and from the agent, not from malware running
  as you.
