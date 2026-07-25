# Groq

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Fast inference via [Groq Cloud](https://console.groq.com/) |

## Prerequisites

- API key from Groq Console
- Env var: `GROQ_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: llama-3.3-70b
    display_name: "Llama 3.3 70B (Groq)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: groq
    upstream_model: llama-3.3-70b-versatile
    max_output_tokens: 128000
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "llama-3.3-70b",
      "displayName": "Llama 3.3 70B (Groq)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    }
  ]
}
```

## Run

```bash
export GROQ_API_KEY=gsk_...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "llama-3.3-70b",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- `known_auth: groq` sets `base_url: https://api.groq.com/openai/v1`.
- Groq model availability changes frequently — verify IDs in Groq's model list.
