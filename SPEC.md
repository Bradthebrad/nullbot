# NullBot Product Spec

Default product name: `NullBot`

Tagline: `It's just a client - no magic here.`

The user can change the `Null` prefix in settings. For example, setting
`brand_prefix = Brad` makes the app display `BradBot` while keeping the same
runtime and config layout.

## Goals

Build a compact Go-based bot application with a Bubble Tea TUI, slash commands,
MCP tool management, skills, history, context compaction, and configurable agent
behavior. It should be useful as a real local assistant without shipping coding
tools by default.

The app must be modular enough that users can import or reuse pieces in other Go
codebases.

## Architecture

- `cmd/nullbot`: binary entrypoint.
- `internal/app`: config, command routing, history, built-in tools, and runtime
  state.
- `internal/tui`: Bubble Tea TUI, layout, modal views, and key handling.
- `tinychain/client`: provider request/response models and small clients.
- `tinychain/agent`: model/tool loop, skills, subagents, compaction hooks.
- `tinychain/mcp`: local and remote MCP tool transport.

The first UI is an embedded local web UI served from the binary. This keeps the
artifact single-binary friendly with no frontend build step. A native desktop
shell can be added later with Wails if desired.

## App Data

Default app directory:

- Windows: `%USERPROFILE%\.nullbot`
- macOS/Linux: `$HOME/.nullbot`

If the user changes the prefix, the directory changes to
`$HOME/.<lower-prefix>bot`, unless `app_dir` is explicitly configured.

Directory layout:

- `config.json`: app, model, MCP, editor, compaction, and UI settings.
- `SKILL.md`: default skill describing API interaction and skill authoring.
- `skills/`: user-installed skills.
- `mcp/`: installed MCP server binaries and per-server configs.
- `history/`: durable chat/session summaries and artifacts.
- `market/`: cached market index metadata.
- `artifacts/`: agent and subagent outputs.

## Built-In Tools

Built-in tools must stay safe and context conservative.

- `config_dir_list`: list files in the bot config directory.
- `config_dir_read`: read a small text file from the bot config directory.
- `skills_list`: list installed skills and their metadata.
- `history_recent`: summarize recent sessions/artifacts without loading all
  content into context.
- `market_list`: list known MCP packages from cached market metadata.
- `mcp_list`: list installed MCP servers and their status.

Coding tools are not bundled. No codebase read/write/search/edit/run command
tools are available until the user installs a coding MCP package from `/market`.

## Default SKILL.md

The default skill in the config directory teaches the bot to:

- interact carefully with APIs;
- discover API capabilities from docs and web pages when web tools are present;
- write new `SKILL.md` files when the user asks;
- search available MCP tools and suggest installable ones;
- avoid pretending unavailable tools exist.

## Slash Commands

Slash commands are explicit. The word `plan` in ordinary prose must not switch
the app into plan mode.

- `/help`: show command list.
- `/plan [focus]`: create or edit a plan. With no focus, open the plan menu.
- `/also <message>`: inject additional user guidance into the active workflow.
- `/pause`: pause active work without discarding tool results.
- `/mcp [list|add|remove|config|status]`: manage MCP servers.
- `/market`: open MCP package marketplace/search menu.
- `/skills [list|add|remove|open|reload]`: manage skills.
- `/analyze [focus]`: analyze the conversation, artifacts, or a focused topic.
- `/compact [focus]`: compact message/tool history, optionally with a focus.
- `/init`: initialize config directory and default skill.
- `/files [workspace <path>|open|view|recent]`: view file/workspace info in app or external editor, and set the active workspace.
- `/clear`: clear visible chat while preserving history.
- `/reset`: reset current session state.
- `/config [key=value]`: open settings or update a config key.
- `/ls`, `/dir`: list files in the configured workspace with a small built-in command, even without coding MCP tools.
- `/rm`, `/rmdir`: remove workspace files/directories with explicit paths; recursive directory removal must be explicit.

## Skill Triggering

Users can trigger a skill mid-sentence with `/skill-name`, such as:

`check /email for anything new`

The command parser should detect inline skill tokens and add a lightweight skill
hint to the next model call. It should not execute arbitrary slash commands
mid-sentence.

## History And Artifacts

The UI shows history, but the agent does not automatically reference it. The
agent can call `history_recent` to retrieve a compact summary of recent sessions
or artifacts.

Subagents write named artifacts under `artifacts/subagents/<task-name>/`. The
main agent can inspect summaries without loading raw full content.

## Context Management

Automatic compaction should be configurable:

- enabled/disabled;
- approximate token threshold;
- message count threshold;
- tool result truncation threshold;
- compaction focus;
- keep last N messages verbatim.

Manual compaction via `/compact [focus]` should show the resulting summary in
the UI and allow the user to accept, reject, or edit it.

## Planning And Subagents

Planning is explicit via `/plan`, UI button, or model/tool decision inside an
active workflow.

If the agent fans out work, it can name subagents by task, such as:

- `api-discovery`
- `market-scan`
- `document-parser`
- `email-checker`

Each subagent has isolated context and returns one final message plus optional
artifacts.

## MCP Market

`/market` opens a UI that scans configured Git sources for Go-based MCP servers.

Market package metadata should include:

- name;
- description;
- category;
- git URL;
- release/binary URL if available;
- checksum if available;
- supported platforms;
- transport;
- required args/env;
- permissions;
- installed version.

Initial implementation may read cached JSON metadata. Later implementation can
use `go-git` to clone or fetch indexes and can download release binaries into
`app_dir/mcp/<server-name>/`.

Installed MCP servers are disabled until explicitly enabled.

## Permissions

Dangerous tool categories require explicit user action:

- codebase file access;
- file mutation;
- shell command execution;
- network search/browser automation;
- email access;
- document parsing outside the app config directory.

Permission decisions should support:

- yes once;
- yes for session;
- always for this tool/server/action;
- deny.

## Editor Integration

`/files` and file menu actions can open files with the configured editor.

Built-in editor choices:

- Windows: Notepad, VS Code, Visual Studio, Sublime Text, Notepad++.
- Linux/macOS: vim, vi, neovim/nvim, VS Code, Sublime Text, custom command.

If the configured executable is missing, return a clear message such as:

`could not find vscode`

## Config

Config must expose:

- brand prefix and tagline;
- app directory override;
- provider/model settings;
- reasoning effort;
- agent max iterations;
- compaction thresholds;
- enabled MCP servers;
- skill directories;
- history limits;
- editor command;
- UI theme;
- permission defaults.

## UI

Core TUI regions:

- centered title showing `NullBot` or the user's chosen name;
- left output panel for chat transcript and agent output;
- right activity panel for tool calls, subagents, logs, and status events;
- status bar for provider/model, progress, active sessions, and quick hints;
- configurable multiline input box, defaulting to 3 visible lines;
- popup modals for config, confirmations, market, plan, editor, analysis,
  compaction, history, logs, MCP, skills, and files.

The UI must support both slash commands and clickable controls.

Initial implementation uses keyboard controls only. Mouse support can be added
with Bubble Tea mouse messages later.

## TUI Modals

- Config modal: view and edit config values.
- Confirm modal: yes/no/always/session choices for risky operations.
- Market modal: browse MCP tool packages.
- Plan modal: view plan, set focus, execute plan, and manually edit plan.
- Editor modal: built-in text editor for plan, compact summary, analysis, and
  small config/skill files.
- Analysis modal: view analysis results, optionally focused.
- Compact modal: view compaction result and accept/reject/edit it.
- History modal: inspect recent sessions in a context-conservative way.
- Logs modal: inspect runtime logs without writing to stdout.

## Composer Keyboard Behavior

The command composer should feel closer to a terminal than a plain text box.

- Tab completion should complete slash commands, subcommands, skill names, MCP
  server names, file paths when file tools are installed, config keys, and
  common command arguments.
- The UI should show a subtle inline ghost suggestion for the best command or
  history match.
- Pressing right arrow at the end of the current input should accept the visible
  ghost suggestion and fill the composer.
- Repeated tasks should be easy to replay by matching recent commands and
  messages as the user types.
- Up/down arrows should navigate through current and previous messages.
- For multiline input, up/down should first move between lines inside the
  current message.
- If the cursor is at the beginning of a multiline message and the user presses
  up, the composer should navigate to the previous message/history entry.
- If the cursor is at the end of a multiline message and the user presses down,
  the composer should navigate to the next message/history entry.
- Escape should dismiss completion menus without clearing the current input.
- Ctrl+C should pause active work when work is running; when no work is running,
  it should behave as a normal input cancel/clear shortcut.

## Sources

- Go `embed` supports compiling static files into a binary.
- Wails can embed frontend assets and bind Go methods to frontend code, useful
  for a later native wrapper.
- MCP uses JSON-RPC over stdio and Streamable HTTP. Stdio is the default local
  transport; Streamable HTTP is the modern remote transport. Legacy SSE can be
  supported for compatibility.
