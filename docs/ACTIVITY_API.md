# Activity API

`App.SubscribeActivity(func(ActivityRecord))` delivers every activity record
live. `App.State().Activity` and replies carry the most recent stored records.
This document is the contract UIs build against.

## Record

| Field | Type | Notes |
| --- | --- | --- |
| `seq` | uint64 | Increases by one per published record. Use it to deduplicate and order. |
| `time` | RFC 3339 | When the event happened. |
| `kind` | string | Source event, e.g. `on_tool_start`, `on_llm_new_token`, `submission`, `runtime`. |
| `status` | string | Normalized meaning (table below). |
| `name` | string | For subagent records this is prefixed `agent/…`; prefer `tool`. |
| `detail` | string | Human/TUI text. For tools it is a bounded summary; prefer the structured fields. |
| `submission_id`, `lane` | string | The chat submission and lane (`primary`, `also`). |
| `agent_id`, `task_id` | string | The agent or subagent that produced it. `main` is the manager. |
| `run_id`, `parent_run_id` | string | Model run id; for tool records `run_id` is the tool call id. |

### Status values

| Status | Meaning | Stored? |
| --- | --- | --- |
| `model start` | A model round began (`detail` = `messages=N`). | yes |
| `response delta` | One streamed answer fragment (`detail` = text). | **no**, live only |
| `reasoning delta` | One streamed visible-reasoning fragment. | **no**, live only |
| `commentary` | Assistant commentary between tool rounds. | yes |
| `reasoning` | A complete visible reasoning summary. | yes |
| `tool start` / `tool complete` / `tool error` | Tool lifecycle (structured fields below). | yes |
| `agent complete` / `model error` | A model run finished or failed. | yes |
| `submission running` / `submission done` / `submission error` / `submission canceled` | Chat submission lifecycle. | yes |
| `instruction applied` / `instruction rejected` | A steering instruction was delivered. | yes |
| `context compact start` / `context compact complete` / `context compact error` | Automatic compaction. | yes |
| `runtime dirty`, `resume requested`, `session loaded` | Runtime housekeeping. | yes |

Stored records live in a 400-record ring. Stream deltas are never stored, so
a long answer cannot evict tool history.

### Tool fields

Present on `tool start`, `tool complete` and `tool error`.

| Field | Type | Notes |
| --- | --- | --- |
| `call_id` | string | Provider tool call id. Start and end share it. |
| `tool` | string | Tool name as the model called it. |
| `title` | string | Human title from the MCP server (`title` or `annotations.title`), if any. |
| `server`, `server_title` | string | MCP server id and display name. Empty for built-in tools. |
| `source` | string | `builtin` or `mcp`. |
| `read_only`, `destructive` | bool | Server-provided hints, when present. |
| `args` | object | The call's arguments. Each string is capped at 1000 characters and each list or object at 50 entries, so the value is always valid JSON. |
| `result` | string | `tool complete`: the first 2000 characters of the text returned to the model. |
| `result_meta` | object | `tool complete`/`tool error`: shape summary, e.g. `items`, `bytes`, `content_types`, `links`, `structured`, `is_error`. |
| `error` | string | `tool error`: the error text. |
| `duration_ms` | int | `tool complete`/`tool error`: wall time of the call. |
| `permission` | object | The authorization decision (below). |

### Permission decision

Every tool call is authorized against the project permissions in force when
it runs (see `docs/PERMISSIONS.md`). The decision is reported on the tool's
end or error record:

```json
{
  "allowed": false,
  "capability": "write",
  "source": "name",
  "rule": "name contains \"send\"",
  "project": "acme-ops",
  "permission": "read-only",
  "reason": "email_send needs write access, but primary project acme-ops is read-only",
  "paths": [
    {"arg": "path", "path": "C:\\work\\acme\\out.txt", "capability": "write", "project": "acme-ops", "allowed": false, "reason": "project acme-ops is read-only and does not allow write"}
  ]
}
```

A denied call never reaches the tool; the model receives `permission denied:
<reason>`.

## Change notifications

`App.SubscribeChanges(func(topic string))` says a snapshot changed; fetch the
snapshot you need. Topics: `submissions`, `tasks`, `mcp`, `config`,
`history`, `schedules`. Notifications can be frequent during a run (`mcp`
fires after every MCP call, `tasks` on tool and model lifecycle events), so
debounce before fetching.
