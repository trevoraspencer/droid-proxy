# OpenAI

## Overview

| | |
|---|---|
| **Tier** | T1 — native OpenAI Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `openai-responses` |
| **When to use** | GPT models via OpenAI's Responses API |

Droid sends Responses-style calls when configured in `openai` mode. The proxy
also accepts `/v1/chat/completions` for models in `openai` mode when Droid sends
chat-completions-shaped requests.

## Prerequisites

- API key from [OpenAI](https://platform.openai.com/)
- Env var: `OPENAI_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: gpt-4o
    display_name: "GPT-4o (OpenAI)"
    factory_provider: openai
    upstream_protocol: openai-responses
    known_auth: openai
    upstream_model: gpt-4o
    max_context_tokens: 128000
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "gpt-4o",
      "displayName": "GPT-4o (OpenAI)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 16000
    }
  ]
}
```

## Run

```bash
export OPENAI_API_KEY=sk-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o",
    "input": "hello"
  }' | jq '.output'
```

## Notes

- Use `factory_provider: openai` with `upstream_protocol: openai-responses` for
  native Responses passthrough — not T3 translation.
- Ready-to-paste Factory snippet: [openai.json](../factory-settings/openai.json).
