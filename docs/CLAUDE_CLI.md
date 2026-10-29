# Claude through the claude command

The Claude CLI provider uses your Claude subscription by running the `claude`
command-line tool (Claude Code) as the model. NullBot sends it your request,
and Claude answers using its own tools: reading and searching files, editing
them, running commands. What Claude writes, thinks and does streams into the
chat as it happens, the same way NullBot's own tool calls do.

## Setting it up

1. Install Claude Code: <https://claude.com/claude-code>.
2. Sign in with your subscription: run `claude` in a terminal and use
   `/login`. On a server with no browser, run `claude setup-token` and give
   NullBot the token as `CLAUDE_CODE_OAUTH_TOKEN`.
3. In NullBot, pick a model from the **Claude CLI** group: the CLI's default,
   Opus, Sonnet or Haiku. The Accounts page (`/accounts`) shows whether the
   command was found and is signed in.

NullBot looks for `claude` on your `PATH`; set `NULLBOT_CLAUDE_BIN` to use a
different one.

NullBot never reads the CLI's credentials. It runs the command and reads what
it prints. `claude auth status` is how it learns whether you are signed in.

## What Claude may do

Claude works in the primary project's folder. The project's permission
decides which of Claude's tools exist for the run. Anything that would need
someone to approve it is refused, because nobody is there to approve it in a
background run.

| Project permission | Claude's tools | Approvals |
| --- | --- | --- |
| Read-only | Read, Grep, Glob, WebFetch, WebSearch, TodoWrite | reads inside the project folders; nothing else |
| Read & write | the above, plus Edit, Write, NotebookEdit | edits inside the project folders |
| Full access | the above, plus Bash | any command |
| Custom | write adds the edit tools; execute adds Bash | as above |

Another project is added to the run (`--add-dir`) only when it allows
everything the primary project does, since the CLI applies one set of tools
to every folder.

Each run also starts with:
- no MCP servers (`--strict-mcp-config`), so only the tools above exist;
- only your user settings (`--setting-sources user`), so hooks and settings
  in a project's `.claude` folder do not run;
- an environment without credentials. Variables that look like a key,
  token, password or secret, and every `AWS_` variable, are removed, the same
  as for `secret_run`. Only `CLAUDE_CODE_OAUTH_TOKEN` is kept.
  `ANTHROPIC_API_KEY` is removed so the CLI uses your subscription rather than
  API billing.

Claude's tool steps show in the activity view under "Claude CLI". Their
arguments and output go through the same secret filter as every other tool
result, as do Claude's text and thinking while they stream. A secret value
split across two pieces of the stream is still caught.

## Conversations

The first request in a chat starts a CLI session. NullBot's instructions are
passed as an addition to Claude's system prompt, and earlier messages are
passed as context. Later requests continue the same session (`--resume`) and
send only what is new, so Claude keeps what it read and did. If the
conversation has changed since (it was compacted, or you continued an old
chat after a restart), NullBot starts a new session with the whole
conversation instead.

Stopping a run interrupts the CLI, which ends its turn and stops its
commands. Anything still running afterwards is stopped with it.

## Limits

- Claude uses its own tools, not NullBot's. Tool servers, `secret_run`,
  visuals and subagents are not available to it in this mode.
- Usage is what the CLI reports for each run. Cost is an estimate from the
  CLI and can differ from your bill.
- Older versions of the CLI may not know every option NullBot passes. If a
  run fails with "unknown option", update the CLI with `claude update`.
- With Full access, Bash can do anything you can do, as it can with
  NullBot's own command tools.
