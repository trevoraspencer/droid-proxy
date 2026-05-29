# Fireworks AI

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Models hosted on [Fireworks AI](https://fireworks.ai/) |

## Prerequisites

- API key from Fireworks
- Env var: `FIREWORKS_API_KEY`

## config.yaml

```yaml
models:
  - alias: deepseek-v4-pro
    display_name: "DeepSeek V4 Pro (Fireworks)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/deepseek-v4-pro
    max_output_tokens: 8192
    capabilities:
      reasoning: deepseek
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "deepseek-v4-pro",
      "displayName": "DeepSeek V4 Pro (Fireworks)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 8192
    }
  ]
}
```

## Run

```bash
export FIREWORKS_API_KEY=fw_...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-pro",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Fireworks model IDs use the `accounts/fireworks/models/...` prefix.
- Enable `capabilities.reasoning: deepseek` for DeepSeek models that return
  `reasoning_content`.
