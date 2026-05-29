# xAI (API key)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Grok models via xAI's API key (not Grok Build OAuth) |

For subscription-based Grok Build access, see [xai-oauth.md](xai-oauth.md).

## Prerequisites

- API key from [xAI Console](https://console.x.ai/)
- Env var: `XAI_API_KEY`

## config.yaml

```yaml
models:
  - alias: grok-3
    display_name: "Grok 3 (xAI)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: xai
    upstream_model: grok-3
    max_output_tokens: 8192
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "grok-3",
      "displayName": "Grok 3 (xAI)",
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
export XAI_API_KEY=xai-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-3",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Replace `upstream_model` with the model ID your xAI account supports.
- `known_auth: xai` sets `base_url: https://api.x.ai/v1`.
