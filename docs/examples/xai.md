# xAI (API key)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Grok models via xAI's API key (not xAI OAuth subscription access) |

For subscription-based xAI OAuth access, see [xai-oauth.md](xai-oauth.md).

## Prerequisites

- API key from [xAI Console](https://console.x.ai/)
- Env var: `XAI_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: grok-3
    display_name: "Grok 3 (xAI)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: xai
    upstream_model: grok-3
    max_output_tokens: 128000
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "grok-3",
      "displayName": "Grok 3 (xAI)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:9787",
      "apiKey": "x",
      "maxOutputTokens": 128000
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
curl -sS http://127.0.0.1:9787/v1/chat/completions \
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
