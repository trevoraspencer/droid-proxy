# xAI OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth - native xAI Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `xai-responses` |
| **When to use** | xAI subscription-backed models such as Grok Build and Grok 4.3 |

For pay-per-use API-key access, see [xai.md](xai.md).

Uses browser PKCE login. See [OAUTH.md](../OAUTH.md) for the full walkthrough.

## Prerequisites

- xAI subscription access for the model you want to use
- OAuth login completed: `droid-proxy auth xai --config config.yaml`

> Alternatively, run `droid-proxy config` to add OAuth models and manage
> accounts interactively - see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

Grok Build is the Grok Build coding model. Factory's top-level reasoning effort
is dropped because Grok Build currently rejects that parameter.

```yaml
models:
  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
    max_output_tokens: 128000
    max_context_tokens: 256000
    capabilities:
      factory_reasoning: drop
```

Grok 4.3 is broader xAI OAuth model support, not strict Grok Build CLI parity.
Factory reasoning levels are passed through to xAI for this model.

```yaml
models:
  - alias: grok-4.3
    display_name: "Grok 4.3 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-4.3
    max_output_tokens: 128000
    max_context_tokens: 1000000
    capabilities:
      factory_reasoning: passthrough
```

Optional: pin either model to a specific logged-in account:

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
      "maxOutputTokens": 128000
    },
    {
      "model": "grok-4.3",
      "displayName": "Grok 4.3 (xAI OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    }
  ]
}
```

## Run

```bash
droid-proxy auth xai --config config.yaml
droid-proxy start --config config.yaml
droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-build-0.1",
    "input": "hello",
    "reasoning": {"effort": "high"}
  }' | jq '.output'
```

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.3",
    "input": "hello",
    "reasoning": {"effort": "low"}
  }' | jq '.output'
```

## Manage accounts

```bash
droid-proxy auth status xai
droid-proxy auth disable xai user@example.com
droid-proxy auth logout  xai user@example.com
```

Check the model is logged in: `curl -s http://127.0.0.1:8787/v1/models | jq
'.data[] | select(.oauth_auth) | {id, capabilities, oauth_auth}'`. See
[OAUTH.md](../OAUTH.md#managing-accounts) for the full reference.

## Notes

- `grok-build-0.1` is documented by xAI as the API model that powers Grok Build.
- `grok-4.3` is configured as xAI OAuth model support, not as a Grok Build CLI
  model.
- `capabilities.factory_reasoning: drop` removes Factory's top-level
  `reasoning` object before xAI.
- `capabilities.factory_reasoning: passthrough` preserves Factory's top-level
  `reasoning` object for models that support configurable reasoning.
- Callback defaults: `127.0.0.1:56121/callback` (configurable under `oauth:`).
- The proxy automatically sanitizes the outbound request for xAI Responses
  compatibility (tool normalization, encrypted reasoning, completed-output
  repair) - see [xAI request handling](../OAUTH.md#xai-request-handling).
- Ready-to-paste Factory snippet: [xai-oauth.json](../factory-settings/xai-oauth.json).
- xAI references: [Grok Build 0.1](https://docs.x.ai/developers/models/grok-build-0.1),
  [Grok 4.3](https://docs.x.ai/developers/models/grok-4.3), and
  [reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning).
