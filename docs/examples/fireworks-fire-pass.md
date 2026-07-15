# Fireworks AI (Fire Pass)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Fire Pass eligible routers on [Fireworks AI](https://fireworks.ai/) |

Fire Pass is an experimental Fireworks product that uses a separate credential
(`FIREWORKS_FIRE_PASS_API_KEY`) from Standard Fireworks (`FIREWORKS_API_KEY`).
Both use the same inference base URL and OpenAI Chat protocol.

## Prerequisites

- Fire Pass key from Fireworks (experimental, personal/non-production agentic coding scope)
- Env var: `FIREWORKS_FIRE_PASS_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: glm-5p2-fast-firepass
    display_name: "GLM-5.2 Fast (Fire Pass)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks-fire-pass
    upstream_model: accounts/fireworks/routers/glm-5p2-fast
    max_output_tokens: 128000
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "glm-5p2-fast-firepass",
      "displayName": "GLM-5.2 Fast (Fire Pass)",
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
export FIREWORKS_FIRE_PASS_API_KEY=fpk_...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Notes

- Fire Pass uses a static curated router catalog with manual entry fallback.
- Router eligibility, availability, and pricing are mutable and experimental.
- The canonical documented router is `accounts/fireworks/routers/glm-5p2-fast`.
- Fire Pass and Standard Fireworks are distinct profiles with independent keys.
