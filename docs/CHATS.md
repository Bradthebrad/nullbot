# Chats

NullBot can run several conversations at once. Each chat has its own
conversation and history file, its own queue of requests, its own run
controls (pause, stop after this step, stop everything) and its own
subagents. All chats share one configuration, one set of projects and
permissions, one set of tool servers, the schedule and the secrets vault.

The desktop app shows chats as tabs in groups that can be split side by side
or stacked. The terminal UI and headless runs use a single chat for now.

## The main chat

The first chat, Main, is always open. It is the only chat with tools to see
and steer the others:

| Tool | What it does |
| --- | --- |
| `chats_list` | Every open chat: id, title, state (idle, running, paused, stopping, held), the request it is working on, queued requests, subagents at work and its last answer |
| `chat_read` | A chat's recent messages and the tool calls of its current or last request |
| `chat_send` | Sends a message to a chat: `send` starts a request when it is idle (and queues it otherwise), `queue` adds it after its current work, `steer` guides its running request; with `subagent`, the guidance goes to one of its subagents |
| `chat_control` | Pauses, resumes or stops a chat (`stop` wraps up after the current step; `stop_everything` cancels now and holds its queue) |
| `chat_open` | Opens a new chat, optionally starting it on a request, so work runs in parallel |

A message the main chat sends appears in the other chat marked "(from the
main chat)", so the record shows who asked. Everything that chat then does is
checked against the same project permissions as anything typed into it. The
main chat cannot message itself, and slash commands stay with the person.

## What is shared and what is not

| Shared by all chats | Each chat's own |
| --- | --- |
| Configuration, models, projects and permissions | Conversation and history file (`history/<id>.jsonl`) |
| Tool servers (started once, used by every chat) | Queue, pause and stop |
| The schedule (it runs in the main chat, so each task fires once) | Subagents and their steering |
| The secrets vault | Visuals it showed, and the answers to them |
| Skills, plans and usage records | Side questions |

Changing settings in one chat changes them for all. Closing a chat stops its
work; its history stays and can be continued from History, which opens it in
a chat of its own. Up to twelve chats can be open.

## Coming back after a restart

NullBot records which chats are open in `chats.json` in your NullBot folder.
When the desktop app starts, those chats open again, each continuing its own
history file.

## For front ends

`app.NewHub(config)` (or `app.HubFor(app)`) runs the chats. `Hub.Open`,
`Hub.Continue`, `Hub.Close`, `Hub.Rename` and `Hub.Chats` manage them;
`Hub.Chat(id)` returns a chat's `*app.App`, which has the same methods as a
single-chat NullBot. `Hub.SubscribeActivity` and `Hub.SubscribeChanges`
report every chat's activity and changes with its chat id, and activity
records carry it in `chat`. `Hub.Tasks` and `Hub.LiveVisualizations` list
every chat's subagents and open visuals; `Hub.VisualOwner` finds the chat an
answer to a visual belongs to.
