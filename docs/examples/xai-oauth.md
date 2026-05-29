# xAI Grok Build OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth — native xAI Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `xai-responses` |
| **When to use** | Grok Build via xAI subscription (not API key) |

For API-key access, see [xai.md](xai.md).

Uses browser PKCE login. See [OAUTH.md](../OAUTH.md) for the full walkthrough.

## Prerequisites

- xAI Grok Build subscription
- OAuth login completed: `./droid-proxy auth xai --config config.yaml`

## config.yaml

```yaml
models:
  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
    max_output_tokens: 8192
```

Optional: pin a specific logged-in account:

```yaml
    oauth_account: user@example.com
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "grok-build-0.1",
      "displayName": "Grok Build 0.1 (xAI OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 8192
    }
  ]
}
```

## Run

```bash
./droid-proxy auth xai --config config.yaml
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-build-0.1",
    "input": "hello"
  }' | jq '.output'
```

## Notes

- Replace `upstream_model` with the Grok Build model ID your account supports.
- Callback defaults: `127.0.0.1:56121/callback` (configurable under `oauth:`).
- Ready-to-paste Factory snippet: [xai-oauth.json](../factory-settings/xai-oauth.json).
