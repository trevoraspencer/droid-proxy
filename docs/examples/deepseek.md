# DeepSeek

## Overview

| | |
|---|---|
| **Tier** | T1 — native OpenAI Chat passthrough |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | DeepSeek models with tool-using agent workflows and reasoning replay |

DeepSeek's chat API speaks OpenAI Chat Completions and returns `reasoning_content`
deltas when the model thinks. droid-proxy captures these automatically so
follow-up turns with tool results carry the prior reasoning forward — required
by DeepSeek to keep tool-using conversations coherent.

The examples below use the current 2026 `deepseek-v4-flash` naming. Older
aliases such as `deepseek-chat`, `deepseek-reasoner`, and legacy proxy aliases
like `droid-deepseek-v3` may still work for existing configs, but treat them as
legacy compatibility names rather than new defaults.

## Prerequisites

- API key from [DeepSeek](https://platform.deepseek.com/)
- Env var: `DEEPSEEK_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: deepseek-v4-flash
    display_name: "DeepSeek V4 Flash (DeepSeek)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    upstream_model: deepseek-v4-flash
    max_output_tokens: 8192
    max_context_tokens: 64000
    capabilities:
      reasoning: deepseek
```

`known_auth: deepseek` fills in `base_url`, `api_key_env`, and reasoning defaults.
See [PROVIDERS.md](../PROVIDERS.md) for tier details.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "deepseek-v4-flash",
      "displayName": "DeepSeek V4 Flash (DeepSeek)",
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
export DEEPSEEK_API_KEY=sk-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

Or load from `.env.local`:

```bash
set -a && source .env.local && set +a
./droid-proxy start --config config.yaml
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Enable `capabilities.reasoning: deepseek` (or use `known_auth: deepseek`) for
  reasoning replay on multi-turn tool conversations.
- Ready-to-paste Factory snippet: [generic.json](../factory-settings/generic.json).
