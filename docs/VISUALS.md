# Visuals

`visualize` lets the agent show the person something in a window of the
desktop app and collect how they respond: a poll, a web page or prototype, a
picture to label, a chart, a draw.io diagram, or any project file. Everything
the person does in it is recorded. When they press **Send to NullBot**, the
agent gets their answer, their note and the interactions.

The tools exist only when a host that can show visuals is running (the
desktop app calls `EnableVisuals`). The terminal UI and headless runs don't
offer them.

![A poll in the NullBot desktop app: three headline options, one chosen, with a note for NullBot](images/visual-poll.png)

## Tools

`visualize` takes a `title` and one of:

| kind | what it shows | what comes back |
|---|---|---|
| `poll` | `options` (labels, or objects with `label`, `description`, `image`), optional `multiple` and `allow_other` | `{"selected": [ids], "other": "..."}` |
| `html` | inline `content`, or a project `.html` file at `path` served with its folder (or `root`) so its scripts, styles and images load | whatever the page sets with `nullbot.setResponse` / `nullbot.submit` |
| `svg` | inline markup or a project file; scripts run | as for html |
| `image` | a project picture (or a `data:image/…` URL); with `labels`, the person pins labeled points | `{"image": {width, height}, "points": [{n, label, x, y}]}` in the picture's pixels |
| `chart` | a Chart.js 4 config as JSON (or `{type, labels, series: [{name, values}]}`) | the bars/points they clicked |
| `drawio` | draw.io XML or a `.drawio` file, rendered to SVG | the shapes and connectors they selected |
| `file` | any other project file, shown as Files shows it | their note |

The kind is inferred from `path` or `content` when omitted. `prompt` is shown
above the visual: what to look at, choose, label or try.

`wait=true` (the default for polls) blocks until the person sends or skips,
the request is stopped or cancelled, or `timeout_minutes` passes (30 by
default, at most 240). Otherwise the tool returns at once; when the person
sends later, their response reaches the agent as a message: added to the
request that made the visual if it is still running, otherwise as a new
request. That message says the response is data from the visual, not
instructions.

`visualize_results` reads a visual's status, answer, note, script errors and
interactions (the last 300 by default). Without an id it lists recent visuals.

## What is recorded

The bridge, loaded first in every page, records clicks (what was clicked, by
id, name, label or text, plus `data-*` attributes), form changes (passwords
as `(hidden)`), form submissions (forms never leave the page), keys pressed
outside text fields, page loads and moves between pages, script errors,
failed loads, console errors and warnings, and dialogs. Pages can add their
own with `nullbot.record(name, data)`.

`nullbot.setResponse(data)` sets what is sent; `nullbot.submit(data)` sets it
and asks the person to send. A page can't send by itself: only the window's
Send button does.

## Where visuals are saved

Each visual is a folder named by its id (`vis-YYYYMMDD-HHMMSS-xxxxxx`):
`visual.json` (what it is, and its answer), the content it shows (inline
content, or a copy of the project file it shows; a project web page is served
from the project instead), and `events.jsonl` (the interactions, at most
20,000 or 8 MB).

The folder is in the project, under `.nullbot/visualizations`, when the
project allows writing and `.nullbot` is a plain folder (not a link).
Otherwise it is in the app folder under `visualizations/<project id>`. The
Visuals page in the app lists them by project.

`.nullbot` gets a `.gitignore` that ignores everything in it, since records
hold what the person typed and sent; delete that file to commit them.

Each `visual.json` is signed with a key kept in the app folder
(`visualizations/.key`). A record that doesn't verify (written by hand, copied
from another computer, or shipped in a repository) is ignored: it isn't
listed, opened or read back to the agent. A visual in a project that no longer
allows writing stays as it was: it can be viewed, not recorded into, answered
or deleted.

## Permissions

`visualize` needs read access to the files it shows (checked like any tool's
paths). `allow_network`, which lets an html or svg visual load from and talk
to the internet, needs the Full permission everywhere the page can read: its
site folder and every project inside it, or its project when it has no site
folder. That is checked again each time the visual opens, so taking Full away
takes the internet away. A page from a project is served only from its site
folder, after links are followed, and never NullBot's records (`.nullbot`).

## How visuals are kept apart from the app

A visual's page runs its own scripts, so the app hands it to a sandboxed frame
as `srcdoc`: the document has no address of its own and an opaque origin. It
can't read or script the app's page, and the desktop app refuses calls into Go
from anything but its own address. Its files load from `/__vis/<token>/`
through the app's asset handler; everything served there carries a policy
that stops it running as a document, so a frame that navigates itself to one
of those addresses gets an inert page (and the app then shows that page the
safe way). Without `allow_network` the page's policy also limits it to its own
files and NullBot's bundled libraries (Chart.js at `/__nullbot/lib/chart.js`).

The app also refuses to start inside a frame, so a visual that loads the
app's own address gets a blank page with nothing that can reach NullBot.

What a page policy can't stop, even with the network off:

- navigating its own frame to a web address, which carries whatever the page
  puts in the URL (the bridge blocks links to the web, not a script setting
  `location`);
- channels the policy doesn't cover, such as WebRTC connection setup and DNS
  prefetching.

Pages shown from a project can read that page's folder. Treat a visual like
any web page you open.

The Visuals page previews pages and drawings only when you press Show
preview, since they run their own scripts.
