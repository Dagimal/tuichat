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
| Manage sessions | `/sessions` (r=rename, d=delete) |
| Clear history | `/reset` |
| Rename session | `/rename <name>` |
| Quit | `/exit` or `Ctrl+C` |
| Scroll up | `↑` / `PgUp` / mouse wheel |
| Scroll down | `↓` / `PgDn` / mouse wheel |
| Previous message | `↑` |
| Next message | `↓` |
| Command completion | `/` + `Tab` or `↑`/`↓` |

> **Copy text:** hold `Shift` + click-drag to select. This is standard terminal
> behavior when mouse tracking is active (required for wheel scrolling).

## Sessions

Sessions are auto-saved to `~/.config/tuichat/sessions/` after each response.
On restart, the last session resumes automatically.

- Session name defaults to the first message snippet
- Rename with `/rename <name>` or press `r` in the sessions list
- Delete by pressing `d` in the sessions list

## Token Counter

Each message shows estimated token count in the header. The status bar shows
total input/output tokens for the current session.

## Requirements

- Go 1.25+
- API key for at least one provider
