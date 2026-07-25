# Xiaomi MiMo

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat with reasoning replay |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Xiaomi MiMo models with thinking mode and tool workflows |

Xiaomi MiMo exposes an OpenAI-compatible Chat Completions endpoint. MiMo
thinking mode returns `reasoning_content` alongside tool calls. Xiaomi's docs
require that field in later turns that include those tool calls, otherwise the
API can return `400`. droid-proxy uses the same reasoning replay path as
DeepSeek.

## Prerequisites

- API key from [Xiaomi MiMo](https://www.xiaomimimo.com/) or MiMo Token Plan
- Env var: `MIMO_API_KEY` (standard) or a Token Plan regional key (see below)

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

Standard MiMo API:

```yaml
models:
  - alias: mimo-v2.5-pro
    display_name: "MiMo V2.5 Pro (Xiaomi MiMo)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: mimo
    upstream_model: mimo-v2.5-pro
    max_output_tokens: 131072
    max_context_tokens: 1048576
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
```

`known_auth: mimo` fills in:

- `base_url: https://api.xiaomimimo.com/v1`
- `api_key_env: MIMO_API_KEY`
- `api-key` auth header (not Bearer)
- `capabilities.reasoning: deepseek`
- `extra_args.thinking.type: enabled`

### Token Plan variants

Use the regional profile that matches your Token Plan subscription:

| known_auth | base URL | env var |
|---|---|---|
| `mimo-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_CN_API_KEY` |
| `mimo-token-plan-sgp` | `https://token-plan-sgp.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_SGP_API_KEY` |
| `mimo-token-plan-ams` | `https://token-plan-ams.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_AMS_API_KEY` |

Example Token Plan entry (Singapore):

```yaml
models:
  - alias: mimo-v2.5-pro
    display_name: "MiMo V2.5 Pro (Token Plan SGP)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: mimo-token-plan-sgp
    upstream_model: mimo-v2.5-pro
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "mimo-v2.5-pro",
      "displayName": "MiMo V2.5 Pro (Xiaomi MiMo)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 131072
    }
  ]
}
```

## Run

Standard API:

```bash
export MIMO_API_KEY=sk-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

Token Plan (example — Singapore):

```bash
export MIMO_TOKEN_PLAN_SGP_API_KEY=tp-...
./droid-proxy start --config config.yaml
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "mimo-v2.5-pro",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Use `mimo-v2.5-pro` for coding and long reasoning; `mimo-v2.5` for multimodal.
  Avoid new configs with legacy `mimo-v2-pro` or `mimo-v2-omni` — Xiaomi
  deprecates those names in 2026.
- Thinking mode forces provider-default temperature even if you send another value.
- Xiaomi bills prompt cache hits separately but does not document a Chat
  `cache_control` field — leave `capabilities.prompt_caching` unset.
- See [PROVIDERS.md](../PROVIDERS.md) for MiMo migration dates.
