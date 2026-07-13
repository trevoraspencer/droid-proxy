# xAI OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth - native xAI Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `xai-responses` |
| **When to use** | xAI subscription-backed models such as Grok 4.5, Composer 2.5 Fast, Grok Build 0.1, and Grok 4.3 |

For pay-per-use API-key access, see [xai.md](xai.md).

Uses browser PKCE login. See [OAUTH.md](../OAUTH.md) for the full walkthrough.

## Prerequisites

- xAI subscription access for the model you want to use
- OAuth login completed: `droid-proxy auth xai --config config.yaml`

> Alternatively, run `droid-proxy config` to add OAuth models and manage
> accounts interactively - see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

Grok 4.5 is the recommended xAI OAuth preset and the current Grok Build
default. The private Grok Build route is a separate contract from the public
xAI API, so this preset explicitly uses the Grok CLI proxy and model-override
headers. It preserves `prompt_cache_key` and passes through Factory's supported
low, medium, and high reasoning levels. The 500,000-token context window is
publicly documented; the example intentionally makes no upstream maximum-output
claim. Factory's JSON entry below uses droid-proxy's standard local 128,000-token
client cap.

```yaml
models:
  - alias: grok-4.5
    display_name: "Grok 4.5 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: https://cli-chat-proxy.grok.com/v1
    upstream_model: grok-4.5
    max_context_tokens: 500000
    capabilities:
      factory_reasoning: passthrough
      factory_reasoning_effort: high
      prompt_caching: true
```

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

Composer 2.5 Fast is available through Grok Build / Grok CLI OAuth. It uses the
Grok CLI proxy endpoint rather than the public xAI API-key endpoint, and it does
not support Factory's top-level reasoning effort.

```yaml
models:
  - alias: grok-composer-2.5-fast
    display_name: "Composer 2.5 Fast (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: https://cli-chat-proxy.grok.com/v1
    upstream_model: grok-composer-2.5-fast
    max_output_tokens: 128000
    max_context_tokens: 200000
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

Optional: pin any xAI OAuth model to a specific logged-in account:

```yaml
    oauth_account: user@example.com
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "grok-4.5",
      "displayName": "Grok 4.5 (xAI OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000,
      "reasoningEffort": "high"
    },
    {
      "model": "grok-build-0.1",
      "displayName": "Grok Build 0.1 (xAI OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    },
    {
      "model": "grok-composer-2.5-fast",
      "displayName": "Composer 2.5 Fast (xAI OAuth)",
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
    "model": "grok-4.5",
    "input": "hello",
    "prompt_cache_key": "example-conversation",
    "reasoning": {"effort": "high"}
  }' | jq '.output'
```

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
    "model": "grok-composer-2.5-fast",
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

- `grok-4.5` is the recommended xAI OAuth alias and uses the private Grok CLI
  proxy because Grok 4.5 is Grok Build's current default. Public API success is
  not evidence that an account can use this private OAuth route.
- Grok 4.5 access depends on account plan and region. At launch, xAI documents
  Grok 4.5 as unavailable in the EU, with EU availability expected in mid-July
  2026; verify the current xAI documentation for later changes.
- `grok-build-0.1` is documented by xAI as the API model that powers Grok Build.
- `grok-composer-2.5-fast` is the Grok Build / Grok CLI OAuth model key for
  Composer 2.5 Fast and uses `https://cli-chat-proxy.grok.com/v1`.
- `grok-4.3` is configured as xAI OAuth model support, not as a Grok Build CLI
  model.
- `capabilities.factory_reasoning: drop` removes Factory's top-level
  `reasoning` object before xAI.
- `capabilities.factory_reasoning: passthrough` preserves Factory's top-level
  `reasoning` object for models that support configurable reasoning.
- Callback defaults: `127.0.0.1:56121/callback` (configurable under `oauth:`).
- The proxy automatically sanitizes the outbound request for xAI Responses
  compatibility (private CLI auth/model headers, forced upstream streaming,
  tool normalization, encrypted reasoning, completed-output repair) - see
  [xAI request handling](../OAUTH.md#xai-request-handling).
- Ready-to-paste Factory snippet: [xai-oauth.json](../factory-settings/xai-oauth.json).
- xAI references: [Composer 2.5](https://x.ai/news/composer-2-5),
  [Grok Build 0.1](https://docs.x.ai/developers/models/grok-build-0.1),
  [Grok 4.5](https://docs.x.ai/developers/grok-4-5),
  [Grok 4.3](https://docs.x.ai/developers/models/grok-4.3), and
  [reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning).
