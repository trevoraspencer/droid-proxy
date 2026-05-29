# Kimi (Moonshot)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Moonshot Kimi models |

## Prerequisites

- API key from [Moonshot AI](https://platform.moonshot.cn/)
- Env var: `MOONSHOT_API_KEY`

## config.yaml

```yaml
models:
  - alias: kimi-k2
    display_name: "Kimi K2 (Moonshot)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: kimi
    upstream_model: kimi-k2-0711-preview
    max_output_tokens: 8192
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "kimi-k2",
      "displayName": "Kimi K2 (Moonshot)",
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
export MOONSHOT_API_KEY=sk-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "kimi-k2",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- `known_auth: kimi` sets `base_url: https://api.moonshot.cn/v1`.
- Check Moonshot docs for current model IDs and update `upstream_model` accordingly.
