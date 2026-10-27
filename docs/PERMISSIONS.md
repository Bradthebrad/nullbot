# Project Permissions

NullBot works inside **projects**: directories you add, each with a
permission. One project is the **primary** project. The bot "sits" there:
relative paths resolve against it, local MCP servers start with it as their
working directory, and tools that take no path are judged by its permission.

## Permissions

| Permission | Allows |
| --- | --- |
| Read only | read |
| Read & write | read, write, delete |
| Full | read, write, delete, execute (run programs and commands) |
| Custom | read, plus any of write, delete, execute (`"allow": ["write", "execute"]`) |

```json
"projects": [
  {"id": "ops", "name": "acme-ops", "path": "C:\\work\\acme", "permission": "read-write"},
  {"id": "ref", "name": "reference", "path": "D:\\docs", "permission": "read-only"},
  {"id": "lab", "name": "lab", "path": "C:\\lab", "permission": "custom", "allow": ["execute"]}
],
"primary_project_id": "ops"
```

## How a tool call is checked

This applies to every tool: NullBot's built-ins, local (stdio) MCP servers
and remote (HTTP) MCP servers.

1. **Classify the tool.** The capability it needs comes from, in order:
   - your per-tool setting (`enabled_mcp_servers.<id>.tool_policies`, set from
     the Tools page: auto, read, write, delete, execute or blocked);
   - NullBot's own knowledge of its built-in tools;
   - the higher of the server's annotations (`readOnlyHint`,
     `destructiveHint`) and the tool's name (`search`/`get`/`list` are read,
     `write`/`send`/`create` are write, `delete`/`remove` are delete,
     `run`/`exec`/`shell`/`eval` are execute). A server cannot make
     `delete_files` read-only by annotating it so;
   - otherwise **write**, so an unrecognized tool never runs in a read-only
     project.
2. **Find paths in the arguments.** Keys such as `path`, `file`, `dir`,
   `cwd`, `paths`, `output_path`, `input_path` are paths. Other values count
   only when they are unambiguous absolute local paths (`C:\…`, `\\server\…`,
   `file://…`, `~/…`). Output keys need write; source keys need read.
3. **Authorize.** Each path must be inside a project that allows the needed
   capability. Overlapping projects are combined restrictively: a read-only
   project nested in a read-write one stays read-only, and deleting or
   running commands in a parent directory is refused if it contains a project
   that does not allow it. A project's root cannot be deleted. A call with no
   paths is authorized against the primary project.

The decision is recorded on the tool's activity record, so the UI can show
why a call was allowed or blocked. Denied calls never reach the tool.

## What MCP servers are told

Local servers start in the primary project and receive:

- MCP **roots**: every project as a `file://` URI. Servers that honor roots
  stay inside them, and they are notified when roots change.
- `NULLBOT_PRIMARY_PROJECT`: the primary project's path.
- `NULLBOT_PROJECTS`: JSON list of `{id, name, path, permission,
  capabilities, primary}`.

Changing projects restarts running servers so they see the new values.

## Limits

This is a policy check on tool **arguments**. MCP servers are separate
programs and NullBot cannot sandbox what their own code does (for example a
server that writes files it was never asked about). Install servers you
trust, and keep projects read-only unless the bot needs more.
