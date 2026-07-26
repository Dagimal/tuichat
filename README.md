# TUI Chat

Terminal AI Chat — a Go TUI app for chatting with LLM providers (OpenAI-compatible APIs) via a Bubble Tea interface with real-time Markdown rendering.

## Quick Start

```bash
# 1. Build
go build -o tuichat .

# 2. Set your API key
export DEEPSEEK_API_KEY="sk-..."

# 3. Run
./tuichat
```

## Configuration

Config path priority:

1. `~/.config/tuichat/config.yaml`
2. `config.yaml` (project root)

### Example: `~/.config/tuichat/config.yaml`

```yaml
default_model: "deepseek-v4-flash"

providers:
  - name: "deepseek"
    api_key: "$DEEPSEEK_API_KEY"
    base_url: "https://api.deepseek.com/v1"
    models:
      - id: "deepseek-v4-flash"
        name: "DeepSeek V4 Flash"
      - id: "deepseek-v4-pro"
        name: "DeepSeek V4 Pro"
```

API keys prefixed with `$` are read from environment variables.

## Usage

| Action | Key / Command |
|--------|--------------|
| Send message | `Enter` |
| Switch model | `/model` or `/switch` |
| Manage sessions | `/sessions` |
| Clear history | `/reset` |
| New session | `/new <name>` |
| Rename session | `/rename <name>` or `Ctrl+R` in session list |
| Delete session | `Ctrl+D` in session list |
| Filter sessions | type directly in session list |
| Quit | `/exit` or `Ctrl+C` |
| Scroll up | `↑` / `PgUp` / mouse wheel |
| Scroll down | `↓` / `PgDn` / mouse wheel |
| Previous message | `↑` |
| Next message | `↓` |
| Command completion | `/` + `↑`/`↓` + `Enter`

> **Copy text:** hold `Shift` + click-drag to select. This is standard terminal
> behavior when mouse tracking is active (required for wheel scrolling).

## Sessions

Sessions are auto-saved to `~/.config/tuichat/sessions/` after each response.
Start fresh every time you open the app (no auto-resume). Saved sessions
are browsable via `/sessions`.

- Session name defaults to the first message snippet
- `/new <name>` — start a new session with a custom name
- `/rename <name>` or `Ctrl+R` in session list — rename current/selected session
- `Ctrl+D` in session list — delete a session
- **Filter sessions** — type any text directly in the session list to search by name
- Rename inline — `Ctrl+R` opens a rename bar below the list

## Token Counter

Each message header shows estimated tokens `[N tok]`. The status bar shows
input/output token counts and total context tokens (`ctx`) sent per request.

## Requirements

- Go 1.25+
- API key for at least one provider

## Features

- **Real-time Markdown** — streaming response rendered with Glamour, adjusts to terminal width
- **Multi-provider** — OpenAI-compatible APIs, switch models mid-session
- **Session management** — auto-save, browse, rename, delete, filter
- **Command auto-complete** — type `/` to see available commands
- **Token counter** — per-message and session totals, including context window
- **Mouse support** — scroll wheel, `Shift`+drag to copy text
- **Rendered message cache** — glamour output cached per message, no re-render on stream updates
