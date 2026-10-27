# Running NullBot headless

The `nullbot` binary runs the terminal UI by default. It also runs without
any UI: one request from the command line, or a long-running process that
runs scheduled tasks and answers requests read from standard input.

Headless runs use the same NullBot folder as the desktop app and the terminal
UI (`~/.nullbot`, or `NULLBOT_APP_DIR`): the same configuration, models,
projects and permissions, skills, MCP servers, scheduled tasks and history.
Anything a headless run does shows up in the apps' history afterwards.

## One request

```sh
nullbot -p "Summarize the open incidents in reports/ and list what is still failing"
```

The answer goes to standard output. Read a longer request from a file or a
pipe with `-p -`:

```sh
nullbot -p - < request.txt
git diff | nullbot -p - --project acme-web
```

A bare argument works too: `nullbot "what changed in export/ this week?"`.

## Options

| Option | What it does |
| --- | --- |
| `--json` | Print the reply as one JSON object: `answer`, `status`, the `tools` called (name, status, error, duration) and `error` when the request failed. |
| `--activity` | Stream progress to standard error: each tool call as it starts and finishes, which agent made it, and context compaction. |
| `--project NAME` | Work in this project for this run (its id or name). The project's permission applies as usual. |
| `--model PROVIDER/MODEL` | Use this model for the manager for this run, such as `openai/gpt-5.1` or `anthropic/claude-sonnet-5-5`. |
| `--app-dir DIR` | Use another NullBot folder. |

Overrides apply to the run only; nothing is saved. `nullbot version` prints
the version and `nullbot help` the usage.

```sh
nullbot -p "Which tests are flaky?" --json --project lab | jq -r .answer
```

```json
{"answer": "Two tests failed on 3 of the last 10 runs…", "status": "done", "tools": [{"tool": "project_read_file", "status": "tool complete", "duration_ms": 12}]}
```

## Exit status

| Code | Meaning |
| --- | --- |
| 0 | The request finished. |
| 1 | The request failed (for example, the provider isn't set up or the model returned an error), or `--project` or `--model` named something that doesn't exist. The reason goes to standard error. |
| 2 | Usage error: an unknown command or option, or an empty request. |
| 130 | Interrupted (Ctrl+C or SIGTERM). The request is cancelled, subagents included. |

## Serve: scheduled tasks and a request stream

```sh
nullbot serve
```

`serve` starts the scheduler, so scheduled tasks (created with `/schedule` in
the terminal UI, or under Activity > Schedules in the desktop app) run on
time. Each line on standard input is a request; its answer is printed to
standard output, one reply per request, in order. When standard input closes,
NullBot keeps running scheduled tasks until it is interrupted. An interrupt
cancels the request under way, if any, and exits.

With `--json`, each reply is one line of JSON, which makes `serve` easy to
drive from another program:

```sh
printf '%s\n' "Check the nightly export log" "Draft the status update" | nullbot serve --json
```

To keep it running on a server, run it under your service manager. A systemd
unit, for example:

```ini
[Service]
ExecStart=/usr/local/bin/nullbot serve --app-dir /srv/nullbot
StandardInput=null
Restart=on-failure
```

On Windows, a Task Scheduler task that starts `nullbot.exe serve` at logon
does the same.

## What is different from the apps

- There is no one to answer a visual, so `visualize` is not offered: it
  exists only while the desktop app is running.
- Permissions are the ones saved for each project; headless runs never ask.
  A call the project doesn't allow fails and the agent is told why.
- Run control is the process: interrupt to cancel. To pause or steer a long
  request, run it in one of the apps.
