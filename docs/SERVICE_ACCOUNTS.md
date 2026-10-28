# Local service accounts and connector integration

## Initial implementation scope

Service connectors are separate, optional MCP programs, not built-in model tools.

| Package | Supported initial adapters | Deliberate limits |
| --- | --- | --- |
| `nullbot-emails-mcp` | Gmail REST and Microsoft Graph mail (Outlook/Hotmail/Live/Microsoft 365, subject to provider policy); Gmail app-password IMAP/SMTP over fixed TLS endpoints | Manual tokens/app passwords; no OAuth login/refresh, arbitrary mail servers, Yahoo or iCloud Mail implementation |
| `nullbot-drives-mcp` | Google Drive, OneDrive, local synced iCloud Drive folder | Read-only; bounded results; redirect-based cloud downloads blocked; local reparse points/placeholders rejected |
| `nullbot-messages-mcp` | Twilio SMS, outbound WhatsApp Business Cloud API text, Discord bot channel read/send, Telegram bot pending updates/send, outbound Signal via an unofficial local bridge | No personal messaging account login, Telegram history/acknowledgments, Signal inbox, Skype, native Windows iMessage, or dedicated carrier email-to-SMS integration |

No provider request occurs when opening Accounts, refreshing local status, saving, or deleting configuration. `configured_unverified` is local configuration metadata, not proof of connectivity, scopes, token validity, delivery, or executable trust. See each connector README for tool-specific details.

## Local UI contract

`pkg/app/service_accounts.go` exports:

- `ServiceAccountCatalog() []ServiceDefinition`
- `(*App).ServiceAccountStatus() (ServiceAccountState, error)`
- `(*App).SaveServiceAccount(ServiceAccountInput) error`
- `(*App).DeleteServiceAccount(id string) error`

The shared Accounts panel in `nullbot_ui` exposes these through the same-user Wails bridge, not through chat/model tools. Inputs are `id`, `service`, `label`, `fields`, `allow_write`. Empty IDs generate a random identifier. Existing IDs replace *all* fields; blank secrets do not preserve old credentials. The service associated with an existing ID cannot change. Labels are non-secret metadata: never put credentials in labels.

Status returns only account IDs, service IDs, labels, local status, and write permission. Credential fields and local folder roots are never returned by the status API. The form starts blank for credential replacement and does not read saved secrets back into the webview. Supported fields are:

| Service ID | Fields |
| --- | --- |
| `gmail`, `outlook`, `google_drive`, `onedrive` | `access_token` |
| `gmail_imap` | `email` (required, nonsecret bare ASCII email address, at most 254 characters), `app_password` (required, secret; ASCII spaces stripped before checking for 1–256 printable ASCII characters) |
| `discord` | `bot_token` (required, secret; 20–256 characters, three nonempty dot-separated URL-safe segments, no `mfa.` prefix; bot only, not a user token) |
| `telegram` | `bot_token` (required, secret; 1–20 digit bot ID starting 1–9, colon, then 30–128 URL-safe letters/digits/underscore/hyphen) |
| `signal` | `bridge_url` (required, nonsecret literal loopback URL with explicit port and no trailing slash), `number` (required, nonsecret E.164 sender: `+` and 7–15 digits starting 1–9), `api_token` (optional, secret; at most 4096 characters matching `^[A-Za-z0-9._~+/-]+=*$`, or empty) |
| `icloud_drive` | `root` (absolute local synced directory) |
| `twilio` | `account_sid`, `auth_token`, `from` |
| `whatsapp` | `access_token`, `phone_number_id`, `api_version` (e.g. `v23.0`; choose a currently supported provider version) |

Writes are disabled by default. Email/message sends additionally require a per-call confirmation flag. That flag is a client/agent contract, **not a cryptographic proof of human approval**; the client must obtain approval of the exact account, recipient and content before setting it. Do not enable write access for clients you do not trust. Drive tools remain read-only regardless of the flag.

## Provider-specific setup boundaries

- **Gmail IMAP/SMTP:** `gmail_imap` is separate from the unchanged `gmail` REST OAuth adapter. Use a Google app password, not a normal Google password, with the full Gmail or Google Workspace email address. Google 2-Step Verification and account/admin policy determine app-password availability. The connector uses fixed TLS Gmail IMAP/SMTP endpoints; no host, port, TLS override or arbitrary mail server is accepted in account fields.
- **Discord:** create and authorize a bot outside NullBot. Bot channel reads/sends depend on channel permissions and Discord message-content policy. No selfbot, user token, personal inbox or personal-account login is provided.
- **Telegram:** create a bot with BotFather outside NullBot. Reads are pending bot updates only, **not chat history**. No offset is advanced and no updates are acknowledged, so reads may repeat; existing webhooks can prevent polling. NullBot does not configure/remove webhooks. Sending accepts numeric chat IDs, not usernames, for chats the bot is permitted to access. No personal-account session/login is provided.
- **Signal:** unofficial **outbound-only** integration with a **separately installed/configured `signal-cli-rest-api` bridge**. NullBot performs no installation, registration, device linking or inbox retrieval. Only `http://127.0.0.1:<port>` or `http://[::1]:<port>` with a decimal port from 1 to 65535 is accepted. No trailing slash is allowed; credentials, hostnames, remote addresses, other schemes, paths, queries and fragments are rejected. This is local HTTP, not TLS: keep the bridge bound to loopback and secure it against other local processes. The optional bridge API token is removed when blank or omitted on full replacement.

All setup and status operations remain local and unverified. Account setup never installs or enables a connector, links an account, or sends a test message.

## Storage and runtime boundary

On Windows, the vault uses current-user DPAPI with UI disabled. It is stored under the OS-known LocalAppData directory:

`NullBot/ServiceAccounts/<digest-of-app-config-root>/accounts.dpapi`

The vault is outside the project and normal NullBot config directory. It uses bounded parsing, validated records, a process-local mutex, encrypted temporary files and rename replacement. Links/junctions in the storage path are rejected. Non-Windows integrated secure storage fails closed; standalone connectors can still use explicitly supplied process environments on their supported platforms.

Do not copy the vault into a repo or treat it as a portable backup. DPAPI is not a sandbox against software running as the same Windows user. The vault lock is process-local: avoid editing the same installation's accounts from multiple app processes concurrently.

At subprocess launch, NullBot:

1. Blanks inherited reserved service variables (including `GMAIL_`, `DISCORD_`, `TELEGRAM_`, `SIGNAL_` and `NULLBOT_SERVICE_`, case-insensitively) and common credential-looking environment names. Explicit reserved overrides are rejected too, including nonsecret bridge URLs, sender numbers and chat IDs. Explicit non-service `MCPEntry.Env` remains available for other MCPs; this is a heuristic hygiene measure, not a complete secret scanner.
2. Requires the exact official connector ID, repository, installed path, enabled state, stdio transport and default arguments before considering injection.
3. Hashes the installed executable and compares it to the install-time SHA256 pin. Missing/mismatched pins fail closed; credentials are not passed.
4. Supplies only accounts assigned to that connector as JSON in `NULLBOT_SERVICE_ACCOUNTS`. It never writes credentials into command-line arguments or globally changes NullBot's environment.

Windows env keys are canonicalized to one spelling to avoid randomized case-insensitive alias conflicts. Large combined credentials remain subject to OS environment-size limits.

Only trusted local stdio launches receive vault credentials. Standalone connector HTTP modes, where supported, require separate explicit configuration. A matching filename alone is insufficient. The pin survives marketplace metadata refresh; new release checksums must not silently authorize modified installed bytes.

These checks do not protect against an administrator, same-user malware, simultaneous executable replacement after verification, or arbitrary user-approved command execution. A powerful code/shell MCP runs with your user privileges and is not isolated by DPAPI. Saving/deleting accounts affects future runtimes; terminate/restart existing runs to apply changes. Removing a local account does not revoke the provider token: revoke it at the provider when needed.

## Marketplace lifecycle

The three servers are listed in the Marketplace. Install fetches the newest GitHub release and refuses one without a SHA-256 checksum for the binary; no release URLs are guessed, and account setup never installs or enables anything.

A published connector release must provide its executable and `SHA256SUMS.txt`. Installation rejects service connector assets without a valid SHA256 checksum, verifies downloaded bytes, and persists an independent `installed_sha256`. Enable remains a separate explicit action. Existing installations without this pin require a verified reinstall before receiving credentials. This implementation does not publish releases, modify the user's live manifest, or install connectors itself.

## Validation

Run using the existing cached toolchain/dependencies:

```powershell
$env:GOPROXY='off'
$env:GOSUMDB='off'
$env:GOTOOLCHAIN='local'
go test ./... -count=1 -timeout=120s
go build ./...
```

Service tests use synthetic credentials, temporary app/market directories, and an injected temporary vault root. DPAPI tests exercise the local Windows crypto API, not a provider. Coverage includes vault roundtrip/corruption, full replacement/deletion, secret-free status, executable hash changes, transport/argument rejection, connector-scoped injection, reserved environment suppression, catalog freshness and release-pin preservation. Link tests may skip when Windows symlink privileges are unavailable. No live OAuth consent, provider connectivity or real delivery is validated by offline tests.
