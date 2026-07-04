# Marketplace And MCP Lifecycle Spec

Status: draft implementation plan  
Branch: `codex-marketplace-mcp-lifecycle`

## Goal

Build a first-class NullBot marketplace that can discover MCP servers and skills from GitHub-hosted release repos, download selected binaries into the user's app data directory, update a local market manifest, and enable/disable/remove installed MCP tools without restarting the whole app.

The same capabilities should be available through:

- `/market` modal for clickable/manual installs;
- `/mcp` modal for enabled server lifecycle management;
- built-in agent tools for discovery, install, enable, disable, remove, and refresh;
- skills that teach the agent how to create and publish new MCP servers.

This work must keep the default NullBot safe: no coding/file/shell powers are enabled until the user or agent explicitly installs and enables MCP servers.

## Repos And Artifacts

Initial market sources:

| Package | Repo | Release Asset |
| --- | --- | --- |
| Coding tools | `Bradthebrad/nullbot-code-mcp` | `nullbot-code-mcp.exe`, `nullbot-code-mcp-small.exe` |
| Parser tools | `Bradthebrad/nullbot-parsers-mcp` | `nullbot-parsers-mcp.exe`, `nullbot-parsers-mcp-small.exe` |
| Skills | `Bradthebrad/nullbot-skills` | `SKILL.md` folders or release/archive assets |

Future sources:

- `nullbot-web-mcp`
- `nullbot-officetools-mcp`
- `nullbot-imagetools-mcp`
- `nullbot-ragtools-mcp`
- `nullbot-remote-mcp`
- `nullbot-remote-interfaces`

## App Data Layout

NullBot app data remains rooted at `~/.nullbot` unless overridden.

```text
~/.nullbot/
  market/
    sources.json
    manifest.json
    cache/
      github/
        Bradthebrad/
          nullbot-code-mcp.json
          nullbot-parsers-mcp.json
  mcp/
    nullbot-code-mcp/
      nullbot-code-mcp.exe
      package.json
      server.json
    nullbot-parsers-mcp/
      nullbot-parsers-mcp.exe
      package.json
      server.json
  skills/
    mcp-skill/
      SKILL.md
```

## Market Manifest

`market/manifest.json` is the local canonical state. It should be rewritten after every refresh/install/remove/enable/disable operation.

```json
{
  "schema_version": 1,
  "updated_at": "2026-06-07T00:00:00Z",
  "sources": [
    {
      "name": "nullbot-official",
      "type": "github_releases",
      "owner": "Bradthebrad",
      "repos": [
        "nullbot-code-mcp",
        "nullbot-parsers-mcp",
        "nullbot-skills"
      ]
    }
  ],
  "packages": [
    {
      "id": "nullbot-code-mcp",
      "kind": "mcp_server",
      "name": "NullBot Code MCP",
      "description": "Workspace-bounded coding tools: files, search, exact edits, async commands.",
      "repo": "Bradthebrad/nullbot-code-mcp",
      "release_tag": "v0.1.0",
      "assets": [
        {
          "name": "nullbot-code-mcp.exe",
          "platform": "windows",
          "arch": "amd64",
          "url": "https://github.com/Bradthebrad/nullbot-code-mcp/releases/download/v0.1.0/nullbot-code-mcp.exe",
          "sha256": "",
          "compressed": false
        },
        {
          "name": "nullbot-code-mcp-small.exe",
          "platform": "windows",
          "arch": "amd64",
          "url": "https://github.com/Bradthebrad/nullbot-code-mcp/releases/download/v0.1.0/nullbot-code-mcp-small.exe",
          "sha256": "",
          "compressed": true,
          "warning": "UPX compressed binaries can trigger antivirus or SmartScreen heuristics."
        }
      ],
      "default_transport": "stdio",
      "default_args": ["--workspace", "{{workspace}}"],
      "permissions": ["workspace_read", "workspace_write", "command_execute"],
      "installed": false,
      "enabled": false,
      "install_dir": "",
      "installed_asset": "",
      "installed_at": "",
      "status": "available"
    }
  ]
}
```

### Package Kinds

- `mcp_server`: downloadable executable managed under `~/.nullbot/mcp/<id>/`.
- `skill_pack`: downloadable or cloneable skills managed under `~/.nullbot/skills/<id>/`.
- `bundle`: future grouping that installs multiple packages together.

## Market Sources

`market/sources.json` contains user-configurable sources:

```json
{
  "schema_version": 1,
  "sources": [
    {
      "name": "nullbot-official",
      "type": "github_releases",
      "owner": "Bradthebrad",
      "repos": [
        "nullbot-code-mcp",
        "nullbot-parsers-mcp",
        "nullbot-skills"
      ]
    }
  ]
}
```

Initial implementation may seed this file if missing.

## GitHub Discovery

Refresh flow:

1. Read `sources.json`.
2. For each GitHub release repo:
   - call GitHub releases API or download public release metadata;
   - prefer latest non-draft release;
   - collect assets, notes, and checksum assets;
   - parse `SHA256SUMS.txt` when present;
   - fetch README text for package description when needed.
3. Merge package metadata with local installed/enabled state.
4. Write `manifest.json`.

No GitHub token is required for public repos, but a token can be used if available to avoid rate limits.

## Download And Install

Install flow:

1. Resolve package id from manifest.
2. Select asset:
   - default normal `.exe`;
   - allow `small=true` to choose UPX `-small.exe`;
   - platform selection starts with Windows amd64.
3. Download to temp path under `~/.nullbot/market/tmp/`.
4. Verify SHA256 when available.
5. Move to `~/.nullbot/mcp/<package-id>/<asset-name>`.
6. Write `mcp/<package-id>/package.json` with original package metadata.
7. Write `mcp/<package-id>/server.json` with runtime config.
8. Mark package installed in manifest.
9. Do not enable automatically unless the user/agent requested `enable=true`.

## MCP Server Config

`mcp/<id>/server.json`:

```json
{
  "id": "nullbot-code-mcp",
  "name": "NullBot Code MCP",
  "transport": "stdio",
  "command": "C:\\Users\\brada\\.nullbot\\mcp\\nullbot-code-mcp\\nullbot-code-mcp.exe",
  "args": ["--workspace", "{{workspace}}"],
  "enabled": false,
  "permissions": ["workspace_read", "workspace_write", "command_execute"],
  "env": {},
  "installed_at": "2026-06-07T00:00:00Z",
  "updated_at": "2026-06-07T00:00:00Z"
}
```

When enabling an MCP server, NullBot copies or merges this into `config.EnabledMCPServers`.

## Agent Reinitialization

Current runtime builds an agent bundle with built-in tools plus enabled MCP tools in `pkg/app/runtime.go`.

Required behavior:

- Any install/enable/disable/remove operation marks the runtime bundle stale.
- The next chat submission rebuilds the agent bundle.
- If an operation happens while a run is active, it should not mutate the active run's tool list.
- UI shows activity: `mcp lifecycle changed; tools will refresh next run`.
- Optional later enhancement: immediate rebuild when idle.

Implementation approach:

- Add `App.MarkRuntimeDirty(reason string)`.
- Store `runtimeVersion` and `runtimeDirtyReason` on `App`.
- In `ensureRuntime`, rebuild when dirty even if config/model did not change.
- Call `Close()` on old MCP clients when replacing the bundle.

## Built-In Agent Tools

Add these built-in tools:

| Tool | Purpose |
| --- | --- |
| `market_refresh` | Refresh market sources and rewrite manifest. |
| `market_list_available` | Return available packages with descriptions, permissions, installed/enabled state. |
| `market_read_package` | Return README/release notes/package details for one package. |
| `market_install_package` | Download and install a package into app data. Optional `enable` and `small` flags. |
| `mcp_list_servers` | List installed and enabled MCP server configs. |
| `mcp_enable_server` | Enable installed MCP server and mark runtime dirty. |
| `mcp_disable_server` | Disable enabled MCP server and mark runtime dirty. |
| `mcp_remove_server` | Disable and remove an installed MCP server. Mark runtime dirty. |
| `skills_market_list` | List available skill packs. |
| `skills_market_install` | Install skill pack into `~/.nullbot/skills/<id>/`. |

The agent should not silently enable dangerous tools. For coding tools, require an explicit user instruction in the conversation or a permission confirmation flow.

## `/market` Modal

The `/market` UI should:

- refresh or load `manifest.json`;
- show packages grouped by `MCP Servers`, `Skills`, and `Bundles`;
- show status badges: `available`, `installed`, `enabled`, `update available`, `error`;
- show permissions clearly;
- allow selection with checkboxes;
- actions:
  - `r`: refresh;
  - `i`: install selected;
  - `e`: install and enable selected;
  - `u`: update installed selected;
  - `d`: details/README;
  - `enter`: open details;
  - `esc`: close.

For `*-small.exe` UPX assets, show:

`Small build is UPX-compressed and may trigger Windows AV or SmartScreen.`

## `/mcp` Modal

The `/mcp` UI should:

- list installed MCP servers from `~/.nullbot/mcp`;
- show enabled/disabled state from config;
- show command, args, transport, permissions, last install/update time;
- actions:
  - `e`: enable selected;
  - `d`: disable selected;
  - `r`: remove selected;
  - `t`: test/list tools for selected;
  - `c`: edit config args/env;
  - `enter`: details.

Testing a selected server should start it, call `initialize`, call `tools/list`, display discovered tools, and close it.

## Skills Marketplace

Create `nullbot-skills` repo for reusable skills.

Initial skill:

```text
skills/
  mcp-skill/
    SKILL.md
```

The `mcp-skill` should teach the agent:

- how NullBot MCP market packages are structured;
- how to use `github.com/Bradthebrad/tinychain/mcp` to build a Go stdio MCP server;
- how to expose `streamable-http` and `sse` flags;
- how to write README/release notes;
- how to build stripped Windows binaries;
- how to optionally UPX-compress small binaries;
- how to create GitHub releases with normal and small assets;
- how to update a market package source/manifest.

This skill should not grant tools by itself. It only gives instructions. Actual code/file/shell/GitHub actions still require installed MCP tooling and user permission.

## Permissions

Package metadata must list permissions. UI and agent tools must surface permissions before enabling.

Initial permission strings:

- `workspace_read`
- `workspace_write`
- `command_execute`
- `document_parse`
- `network_fetch`
- `github_release_download`
- `skill_install`

Dangerous permissions:

- `workspace_write`
- `command_execute`
- `network_fetch`

Dangerous permissions should require explicit user confirmation in UI. Agent tool calls should return an error unless the requested package id and enable/install intent are explicit in recent user text.

## Failure Modes

Handle clearly:

- GitHub repo missing or private;
- rate limited;
- no releases;
- no matching Windows asset;
- checksum missing;
- checksum mismatch;
- download interrupted;
- executable blocked by AV;
- test launch fails;
- server lists no tools;
- runtime rebuild fails.

All errors should be logged to `~/.nullbot/logs/nullbot.log` and reflected in `manifest.json` package status.

## Implementation Phases

### Phase 1: Data Model And Manifest

- Add `pkg/app/market.go`.
- Define `MarketManifest`, `MarketSource`, `MarketPackage`, `MarketAsset`, `InstalledPackage`.
- Seed default `sources.json`.
- Implement load/save/merge state.
- Replace current `readMarketCache` with manifest-aware reader.

### Phase 2: GitHub Discovery

- Add GitHub release discovery client using stdlib `net/http`.
- Fetch release metadata and README/release notes.
- Parse `SHA256SUMS.txt`.
- Update `manifest.json`.
- Add `market_refresh` and richer `market_list_available`.

### Phase 3: Install/Remove/Enable/Disable

- Implement download temp file, checksum verification, atomic move.
- Write `package.json` and `server.json`.
- Add MCP lifecycle helpers.
- Add built-in tools.
- Mark runtime dirty when enabled server set changes.

### Phase 4: UI Modals

- Replace placeholder `/market` modal data with interactive package list.
- Replace placeholder `/mcp` modal data with interactive installed-server list.
- Add details views and confirmation modal integration.

### Phase 5: Agent Rebuild

- Add runtime dirty flag.
- Ensure enabled MCP changes reload tools on next run.
- Add activity records for market/MCP lifecycle events.

### Phase 6: Skills Repo

- Initialize `nullbot-skills`.
- Add `skills/mcp-skill/SKILL.md`.
- Publish repo/release if desired.
- Add skill-pack entries to default market sources.

### Phase 7: Tests

- Unit-test manifest merge.
- Unit-test checksum parser.
- Unit-test download using local `httptest`.
- Unit-test enable/disable config mutation.
- TUI tests for market/mcp modal rendering.

## Open Decisions

- Whether `market_install_package` can enable dangerous MCP servers in one call or must require separate `mcp_enable_server`.
- Whether public GitHub API rate limits are acceptable without token support.
- Whether small UPX binaries should be default or opt-in.
- Whether skills are installed from Git tree paths or GitHub release archives.
- How much permission confirmation should live in App logic versus TUI logic.
