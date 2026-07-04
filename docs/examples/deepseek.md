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
    max_output_tokens: 128000
    max_context_tokens: 64000
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
      reasoning_effort: high
```

`known_auth: deepseek` fills in `base_url`, `api_key_env`, thinking-on request
defaults, and reasoning replay defaults.
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
      "maxOutputTokens": 128000
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
- DeepSeek thinking defaults to enabled upstream, but droid-proxy sets
  `extra_args.thinking.type: enabled` and `reasoning_effort: high` for
  `known_auth: deepseek` so the request is explicit.
- Ready-to-paste Factory snippet: [generic.json](../factory-settings/generic.json).
