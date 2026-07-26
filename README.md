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
| Clear history | `/reset` |
| Quit | `/exit` or `Ctrl+C` |
| Scroll up | `↑` / `PgUp` / mouse wheel |
| Scroll down | `↓` / `PgDn` / mouse wheel |
| Previous message | `↑` (in empty input) |
| Next message | `↓` (in empty input) |

## Requirements

- Go 1.25+
- API key for at least one provider
