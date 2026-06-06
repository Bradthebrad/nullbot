# NullBot

`NullBot` is a local Go bot client with a Bubble Tea TUI.

Default name: `NullBot`

Tagline: `It's just a client - no magic here.`

## Run

```powershell
go run ./cmd/nullbot
```

The app opens directly in the terminal.

For development with local app data:

```powershell
$env:NULLBOT_APP_DIR='.appdata'; go run ./cmd/nullbot
```

## Build

Console build:

```powershell
go build -o nullbot.exe ./cmd/nullbot
```

## TUI Shortcuts

- `Ctrl+Q`: quit.
- `Ctrl+C`: pause active work.
- `Ctrl+J`: insert newline.
- `Ctrl+V`: paste from clipboard.
- `Ctrl+A`: select all input; the next typed/pasted text replaces it.
- `Home`: jump to start of input.
- `End`: jump to end of input.
- `Ctrl+Z`: clear current input.
- `Ctrl+K`: clear output area.
- `Ctrl+L`: clear activity panel.
- `Ctrl+O`: open full activity log.
- `PageUp` / `PageDown`: scroll output panel.
- `Shift+Up` / `Shift+Down`: scroll activity panel.
- Mouse wheel: scroll the output, activity, or modal panel under the pointer.

## Interactive Modals

- `/config`: opens editable config and API keys. Use `Up/Down`, `Enter` or `e` to edit, `Enter` to accept the field, and `s` to save. Some terminals capture `Ctrl+S`. Settings go to `.nullbot/config.json`; API keys go to `.nullbot/api/keys.json`.
- `/models`: opens model selection grouped by provider. Use `Up/Down`, then `Enter` or `s` to save the selected provider/model. OpenAI and OpenRouter model lists refresh from live APIs when local keys are set.

## Logs

NullBot writes logs to `.nullbot/logs/nullbot.log`. Use `/logs` in the TUI to inspect recent log lines.
