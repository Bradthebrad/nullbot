# History

Everything NullBot says and does is kept on disk, per session, under the
app folder:

| File | What it holds |
| --- | --- |
| `history/<session>.jsonl` | Every message, one per line: requests, answers, public reasoning summaries, commentary, steering and compaction summaries (role `compaction`). Never trimmed. |
| `history/activity/<session>.jsonl` | Every finished tool call: tool, server, arguments, a preview of the result (about 2,000 characters) or the error, how long it took, which agent ran it, and the permission decision. Model errors too. |
| `artifacts/<session>-assistant-<time>.md` | Each answer as its own Markdown file. |

The app keeps only recent activity in memory (400 records, and 40 requests in
the activity panel). Those limits never apply to what is on disk.

## Agent tools

| Tool | Use |
| --- | --- |
| `history_search` | Search all sessions, newest first. `query` (all words must appear; `"quoted phrases"` exactly), `role` (user, assistant, reasoning, commentary, tool), `tool` (tool or server name), `session_id`, `since` / `until` (a date, an RFC 3339 time, or `90m`, `36h`, `7d`, `2w`), `limit`, `offset`. Each result names its session and its message or tool-call number. |
| `history_session_read` | Read any range of a session: `start` (message number; negative counts from the end), `limit`, `full` for whole messages instead of previews, `include_tools` to interleave the tool calls made in that range. |
| `history_sessions` | List recent sessions. |
| `history_recent` | The last messages of the current conversation. |

All four are read-only and work in any project.

The desktop app's History view uses the same search, with filters for time
and for messages or tool calls.

How much of the conversation each new request carries, and how a long request
is kept inside the model's window, is described in [CONTEXT.md](CONTEXT.md).
