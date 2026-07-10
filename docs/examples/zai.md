# Z.AI (GLM)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Z.AI / GLM models |

Z.AI has three built-in `known_auth` profiles. Pick the one that matches your
API key type.

## Prerequisites

| Profile | Env var | Use case |
|---------|---------|----------|
| `zai-main-api` | `ZAI_MAIN_API_KEY` | Normal Z.AI API keys |
| `zai-coding-api` | `ZAI_CODING_API_KEY` | GLM Coding Plan keys |
| `zai` | `ZAI_API_KEY` | Legacy alias for main API (compatibility) |

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

### Main API (recommended)

```yaml
models:
  - alias: glm-5.1
    display_name: "GLM 5.1 (Z.AI)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: zai-main-api
    upstream_model: glm-5.1
    max_output_tokens: 131072
```

### GLM Coding Plan

```yaml
models:
  - alias: glm-5.2
    display_name: "GLM 5.2 (Z.AI GLM Coding Plan)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: zai-coding-api
    upstream_model: glm-5.2
    max_output_tokens: 131072
    max_context_tokens: 200000
    extra_args:
      thinking:
        type: enabled
```

### Legacy alias

```yaml
models:
  - alias: glm-5.1
    display_name: "GLM 5.1 (Z.AI legacy profile)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: zai
    upstream_model: glm-5.1
```

| known_auth | base URL |
|---|---|
| `zai`, `zai-main-api` | `https://api.z.ai/api/paas/v4` |
| `zai-coding-api` | `https://api.z.ai/api/coding/paas/v4` |

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "glm-5.2",
      "displayName": "GLM 5.2 (Z.AI GLM Coding Plan)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 131072
    }
  ]
}
```

## Run

Main API example:

```bash
export ZAI_MAIN_API_KEY=...
./droid-proxy start --config config.yaml
./droid-proxy status
```

Coding Plan:

```bash
export ZAI_CODING_API_KEY=...
./droid-proxy start --config config.yaml
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Prefer `zai-main-api` or `zai-coding-api` for new configs; `zai` remains for
  backward compatibility.
- Verify current model IDs in Z.AI's documentation.
