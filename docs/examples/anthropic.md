# Anthropic

## Overview

| | |
|---|---|
| **Tier** | T1 — native Anthropic Messages passthrough |
| **Factory mode** | `anthropic` |
| **Upstream protocol** | `anthropic-messages` |
| **When to use** | Claude models via Anthropic's Messages API |

The proxy automatically decompresses gzipped responses (Anthropic's load balancer
sometimes strips the `Content-Encoding` header).

## Prerequisites

- API key from [Anthropic](https://console.anthropic.com/)
- Env var: `ANTHROPIC_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: claude-sonnet-4-5-20250929
    display_name: "Claude Sonnet 4.5 (Anthropic)"
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    known_auth: anthropic
    upstream_model: claude-sonnet-4-5-20250929
    max_context_tokens: 200000
```

`known_auth: anthropic` injects `anthropic-version: 2023-06-01`, uses
`x-api-key` instead of `Authorization`, and lets `droid-proxy config` discover
models from Anthropic's `/v1/models` endpoint.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "claude-sonnet-4-5-20250929",
      "displayName": "Claude Sonnet 4.5 (Anthropic)",
      "provider": "anthropic",
      "baseUrl": "http://127.0.0.1:9787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    }
  ]
}
```

## Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "max_tokens": 256,
    "messages": [{"role":"user","content":"hi"}]
  }' | jq -r '.content[0].text'
```

## Notes

- The proxy forwards `anthropic-version` and `anthropic-beta` headers from Droid
  when set, so opt-in features arrive at Anthropic intact.
- Ready-to-paste Factory snippet: [anthropic.json](../factory-settings/anthropic.json).
