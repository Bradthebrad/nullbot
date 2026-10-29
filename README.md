<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/nullbot-hero-dark.svg">
  <img alt="NullBot: a small, auditable agent you can talk to in text or in interactive visuals" src="assets/nullbot-hero-light.svg">
</picture>

NullBot is a small, auditable agent written in Go. You talk to it in text, in
the desktop app, in your terminal or headless from a script, or through
interactive visuals it puts in front of you: polls, web pages and prototypes,
charts, pictures to label and diagrams. A manager agent plans the work and
hands pieces to subagents, each on whatever provider and model you choose.
Every tool call is shown as it happens and checked against the permission you
gave the project it touches.

This repository holds the agent runtime (`pkg/app`), the terminal UI and the
headless CLI. The desktop app, which is the main way to use NullBot, lives in
[nullbot_ui](https://github.com/Bradthebrad/nullbot_ui). All of them share one
NullBot folder, so a conversation, a project or a schedule set up in one is
there in the others.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/chat-dark.png">
  <img alt="The NullBot desktop app: a conversation on the left, and on the right every step of the run, including a write the project's permission blocked" src="docs/images/chat-light.png">
</picture>

## Design

**Base tooling first.** The built-in tools are few and local: project files
within their permissions, subagents, plans, history, skills, the Marketplace
and visuals. Anything heavier, such as running commands, browsing the web,
reading mail or generating images, is a separate tool server you choose to
install, can read the source of, and can turn off. The one command runner in
the core is `secret_run`, which exists so saved secrets never leave NullBot:
it runs only with secrets you allowed, in projects that allow running
programs. The core stays small enough to audit.

**Two ways to talk.** Most work is text. Some questions are easier to answer by
pointing: which of three headlines, where the scratches are on a photo, which
bar in a chart looks wrong, whether the checkout button works. For those the
agent opens a visual, and what you click, type and choose comes back to it as
data, along with your note.

**You stay in charge of the run.** Each project has a permission (read only,
read & write, full, or custom), and every call, built-in or from a tool server
on this machine or another, is checked against it. While a request runs you can
steer it, ask something on the side, pause it, have it wrap up, or stop it
outright, and the trace shows each step as it happens. Several chats can run
at once, and the main chat can see and steer the others
([docs/CHATS.md](docs/CHATS.md)).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/how-it-works-dark.svg">
  <img alt="How NullBot works: you talk in text or visuals; a manager agent and subagents on any model use built-in tools and add-on MCP servers, each call checked against the project it touches" src="assets/how-it-works-light.svg">
</picture>

## Three ways to run it

| | What it is | Start it |
| --- | --- | --- |
| Desktop app | The full experience: chat with a live trace, visuals, Files, Activity, Tools, History and Settings. Windows. | Download from [nullbot_ui releases](https://github.com/Bradthebrad/nullbot_ui/releases). |
| Terminal UI | Keyboard-first: output and activity panels, slash commands, modals for models, skills, tools and usage. | `nullbot` |
| Headless | One request from the command line, or a long-running process that runs scheduled tasks and answers requests on standard input. Plain or JSON output. | `nullbot -p "…"`, `nullbot serve` |

```sh
nullbot                                   # terminal UI
nullbot -p "What changed in export/ this week?"
git diff | nullbot -p - --json --project acme-web
nullbot serve --app-dir /srv/nullbot      # scheduled tasks + requests on stdin
```

Headless runs use the same configuration, projects, permissions, skills, tool
servers and history as the apps; overrides such as `--project` and `--model`
apply to one run. Exit status is 0 when the request finished, 1 when it failed
and 130 when it was interrupted. See [docs/HEADLESS.md](docs/HEADLESS.md).

<img alt="The NullBot terminal UI: the conversation on the left, every tool call and subagent on the right" src="docs/images/tui.png">

## Interactive visuals

In the desktop app, the `visualize` tool opens a window beside the
conversation and records what you do in it. When you press **Send to
NullBot**, the agent gets your answer, your note and the interactions.

| Kind | What you see | What comes back |
| --- | --- | --- |
| Poll | Options with descriptions or pictures; one or several choices; your own answer | The choices and your text |
| Page | Inline HTML or a project's web page with its scripts, styles and images: prototypes, forms, small games | Clicks, form changes, page moves, script errors and whatever the page reports |
| Picture | A project image; with labels, you pin labeled points | Each point's label and position in the picture's pixels |
| Chart | A Chart.js chart from data | The bars and points you clicked |
| Diagram | A draw.io diagram | The shapes and connectors you selected |
| File | Any project file, shown as the Files view shows it | Your note |

The agent can wait for your answer or carry on and read it when you send it.
Visuals are saved with the project, under `.nullbot/visualizations`, and the
Visuals page lists them by project with every recorded interaction. Pages run
in a sandboxed frame, cut off from the internet unless the agent asks for it
and the project has the Full permission. See
[docs/VISUALS.md](docs/VISUALS.md).

<img alt="A checkout prototype in a NullBot visual window, with nine interactions recorded and a note for NullBot" src="docs/images/visual-prototype.png">

## Agents, models and providers

The manager talks with you, plans and hands out work; subagents take a piece
each, in parallel, with their own context window. Each can use any model from
any provider you have set up:

- a ChatGPT subscription, by signing in (Codex)
- a Claude subscription, through your installed `claude` command: Claude
  works with its own tools within the project's permission, and its steps
  stream into the chat ([docs/CLAUDE_CLI.md](docs/CLAUDE_CLI.md))
- OpenAI, Anthropic and OpenRouter, with an API key
- models you host: Ollama, llama.cpp, LM Studio, vLLM or any
  OpenAI-compatible server, here or on another machine. NullBot ranks which
  models run well on your hardware (a Go port of
  [llmfit](https://github.com/AlexsJones/llmfit)) and installs them from
  Hugging Face ([docs/LOCAL_MODELS.md](docs/LOCAL_MODELS.md))

Every request is measured against the model's context window. Tool results the
model has already read are shortened from 60% full, older messages are
summarized from 85%, and a tool result too large to fit is cut to a preview
with its size; the full history stays searchable. See
[docs/CONTEXT.md](docs/CONTEXT.md) and, for steering and stopping a run,
[docs/RUNS.md](docs/RUNS.md).

## Tools

### Built in

| Area | Tools |
| --- | --- |
| Project files | `project_read_file`, `project_write_file`, `list_dir`, `workspace_info` |
| Visuals | `visualize`, `visualize_results` (desktop app only) |
| Subagents | `spawn_subagent`, `subagent_status`, `subagent_wait` |
| Plans | `plans_list`, `plan_read`, `plan_update_step` |
| History | `history_search`, `history_session_read`, `history_sessions`, `history_recent` |
| Skills | `skills_list`, `skill_read`, `skill_references`, `create_skill` |
| Marketplace and servers | `market_list_available`, `market_read_package`, `market_install_package`, `market_refresh`, `mcp_list_servers`, `mcp_enable_server`, `mcp_disable_server`, `mcp_remove_server` |
| Secrets | `secrets_list` (names only), `secret_run` (a command with secrets in its environment, values removed from its output) |
| Chats (main chat only) | `chats_list`, `chat_read`, `chat_send`, `chat_control`, `chat_open` |
| NullBot itself | `config_dir_list`, `config_dir_read`, `logs_recent` |

### Tool servers

Separate MCP servers, installed from the Marketplace (Tools > Marketplace in
the desktop app, `/market` in the terminal). Any other MCP server, local or
remote, can be added too, and the same permissions apply to it.

| Server | What it adds |
| --- | --- |
| [nullbot-code-mcp](https://github.com/Bradthebrad/nullbot-code-mcp) | Coding: read, search and exactly edit files, and run commands, inside the project |
| [nullbot-web-mcp](https://github.com/Bradthebrad/nullbot-web-mcp) | Web search, readable page fetches, and browser automation and testing through Chromium |
| [nullbot-parsers-mcp](https://github.com/Bradthebrad/nullbot-parsers-mcp) | Document parsing: PDF, Office, EPUB, archives, email files, notebooks, tables, metadata |
| [nullbot-imagetools-mcp](https://github.com/Bradthebrad/nullbot-imagetools-mcp) | Image generation and editing through OpenAI and OpenRouter, plus local resize, crop, thumbnails, contact sheets and PDFs |
| [nullbot-emails-mcp](https://github.com/Bradthebrad/nullbot-emails-mcp) | Gmail and Outlook: list, read and, once you confirm, send |
| [nullbot-drives-mcp](https://github.com/Bradthebrad/nullbot-drives-mcp) | Google Drive, OneDrive and synced folders such as iCloud Drive, read only |
| [nullbot-messages-mcp](https://github.com/Bradthebrad/nullbot-messages-mcp) | Discord and Telegram bots, Twilio SMS, WhatsApp Business and a local Signal bridge |

Accounts for the email, drives and messages servers are added in the
desktop app (Settings > Accounts), kept with Windows DPAPI, and passed only to
the matching server installed from the Marketplace. See
[docs/SERVICE_ACCOUNTS.md](docs/SERVICE_ACCOUNTS.md).

### Secrets

API keys, the ChatGPT sign-in, GitHub tokens, SSH keys and AWS or Google Cloud
credentials live in one encrypted vault, protected by Windows DPAPI, the macOS
keychain, the Linux Secret Service, or a passphrase on servers. Agents never
see a value: `secrets_list` shows names and what each provides, and
`secret_run` runs a command with the secrets it names in its environment (a
GitHub token signs git in over HTTPS; an SSH key becomes `GIT_SSH_COMMAND`),
then removes their values from the output. Every other tool's result is
filtered the same way. A secret reaches agents only once you allow it, and a
tool server only if you name it.

```sh
nullbot secrets set github --kind github_token --agent   # asks for the token without echo
nullbot secrets ssh-keygen deploy --host github.com --agent
nullbot secrets github github                            # check who the token signs in as
```

See [docs/SECRETS.md](docs/SECRETS.md).

### Skills

Skills are `SKILL.md` instructions the agent reads when a request calls for
them. They add no powers of their own; they teach the agent to use the tools it
has. Packs for web search, browser automation and research, API probing and
building MCP servers are in
[nullbot-skills](https://github.com/Bradthebrad/nullbot-skills) and in the
Marketplace.

## Projects and permissions

| Permission | Allows |
| --- | --- |
| Read only | Reading |
| Read & write | Reading, writing and deleting |
| Full | All of that, and running programs |
| Custom | Reading, plus any of writing, deleting and running programs |

One project is primary: relative paths resolve there and local tool servers
start there. A call that names no folder is judged by the primary project;
where projects overlap, the stricter permission wins. What each tool needs is
worked out from the server's hints and the tool's name, and you can change it
per tool on the Tools page. See [docs/PERMISSIONS.md](docs/PERMISSIONS.md).

## Terminal UI

### Commands

| Command | What it does |
| --- | --- |
| `/help` | Show commands |
| `/models`, `/effort` | Choose provider and model; choose reasoning effort |
| `/accounts` | API key and subscription status; `/accounts codex login` signs in to a ChatGPT subscription |
| `/secrets` | Saved secrets (never their values) and how the vault is protected |
| `/agents`, `/tasks`, `/thoughts` | Live manager and subagent dashboard; inspect or cancel tasks; reasoning and progress |
| `/plan` | Open, focus or execute a plan |
| `/schedule` | Create, list, run and cancel scheduled tasks (`in 2h`, `at 09:00`, `every 24h`) |
| `/also <message>` | Add guidance to the request that is running |
| `/pause` | Pause active work without losing state |
| `/compact`, `/analyze` | Compact the conversation, or analyze it, with an optional focus |
| `/mcp`, `/market`, `/skills` | Manage tool servers, install from the Marketplace, manage skills |
| `/history`, `/logs`, `/usage` | Saved sessions, runtime log, token and cost tracking |
| `/files`, `/ls`, `/rm`, `/rmdir` | Workspace files |
| `/config`, `/themes`, `/name` | Settings, color theme, the bot's displayed name |
| `/copy`, `/paste`, `/clear`, `/reset` | Copy the last answer, attach from the clipboard, clear the output, reset the session |

### Keys

| Key | Action |
| --- | --- |
| `F1` | Help |
| `Enter`, `Ctrl+J` | Send; new line |
| `Tab`, `Up`/`Down` | Complete a command or path; previous and next input |
| `Ctrl+C` | Pause active work |
| `Ctrl+V`, `Alt+V` | Paste or attach from the clipboard |
| `Ctrl+O` | Full activity log |
| `Ctrl+H`, `Ctrl+P` | History; plan |
| `Ctrl+K`, `Ctrl+L`, `Ctrl+Z` | Clear output; clear activity; clear input |
| `PageUp`/`PageDown`, `Shift+Up`/`Shift+Down` | Scroll output; scroll activity |
| `Ctrl+Q` | Quit |

## NullBot folder

```text
~/.nullbot/                 (or NULLBOT_APP_DIR)
  config.json               settings, models, projects and their permissions
  vault/vault.json          encrypted secrets: API keys, sign-ins, tokens, keys
  history/                  every message and tool call, by session
  logs/nullbot.log          runtime log
  market/                   Marketplace catalog
  mcp/<id>/                 installed tool servers
  skills/<name>/SKILL.md    installed skills
  plans/, artifacts/        saved plans and saved answers
  visualizations/           visuals for projects that can't hold them
```

Each project also gets a `.nullbot` folder for its visuals, with a
`.gitignore` that keeps them out of commits.

## Build from source

NullBot needs Go 1.26.4 or later. Dependencies are tagged public modules,
including [tinychain](https://github.com/Bradthebrad/tinychain).

```sh
go test ./...
go build -o nullbot ./cmd/nullbot
go build -ldflags "-X main.version=v0.5.0" -o nullbot.exe ./cmd/nullbot   # with a version
```

Use a separate NullBot folder while developing: set `NULLBOT_APP_DIR` or pass
`--app-dir`.

## Roadmap

Planned, not built yet:

- **Cloud infrastructure tools** that create and tear down AWS and Google Cloud
  resources, with every change confirmed first.

See [docs/ROADMAP.md](docs/ROADMAP.md).

## Documentation

| Topic | |
| --- | --- |
| Headless runs | [docs/HEADLESS.md](docs/HEADLESS.md) |
| Visuals | [docs/VISUALS.md](docs/VISUALS.md) |
| Permissions | [docs/PERMISSIONS.md](docs/PERMISSIONS.md) |
| Steering and stopping runs | [docs/RUNS.md](docs/RUNS.md) |
| Context and compaction | [docs/CONTEXT.md](docs/CONTEXT.md) |
| History and search | [docs/HISTORY.md](docs/HISTORY.md) |
| Secrets and the vault | [docs/SECRETS.md](docs/SECRETS.md) |
| Claude through the claude command | [docs/CLAUDE_CLI.md](docs/CLAUDE_CLI.md) |
| Local models | [docs/LOCAL_MODELS.md](docs/LOCAL_MODELS.md) |
| Several chats at once | [docs/CHATS.md](docs/CHATS.md) |
| Service accounts | [docs/SERVICE_ACCOUNTS.md](docs/SERVICE_ACCOUNTS.md) |
| Activity events API | [docs/ACTIVITY_API.md](docs/ACTIVITY_API.md) |
| Marketplace server lifecycle | [docs/MARKETPLACE_MCP_LIFECYCLE_SPEC.md](docs/MARKETPLACE_MCP_LIFECYCLE_SPEC.md) |
| Roadmap | [docs/ROADMAP.md](docs/ROADMAP.md) |

No API keys, account tokens, configuration or session data belong in this
repository; NullBot reads them from your NullBot folder at run time.
