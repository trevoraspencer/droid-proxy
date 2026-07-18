# Baseten

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Models hosted on [Baseten](https://baseten.co/) shared Model API |

The native `baseten` profile uses the shared Model API base URL
`https://inference.baseten.co/v1` with OpenAI Chat Completions. Model slugs
are opaque and may contain organization prefixes, dots, hyphens, underscores,
colons, and slashes.

Dedicated or custom OpenAI-compatible Baseten deployments are **not** handled
by this native profile. Use the existing
[Custom OpenAI-compatible endpoint](../PROVIDERS.md#adding-a-new-provider) flow
with an explicit `base_url` and `api_key_env` for those.

## Prerequisites

- API key from Baseten
- Env var: `BASETEN_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## Model discovery and manual entry

Interactive onboarding uses authenticated `GET /v1/models` discovery to
present sorted, de-duplicated model IDs. Model slugs are opaque and are
preserved byte-for-byte through the picker, save, and reload cycle. Surrounding
interactive whitespace is trimmed; no provider-prefix or catalog normalization
mutates the opaque slug.

**Manual model entry is always available.** You can type any valid Baseten
model slug directly, including IDs absent from the discovery result or private
deployments that are not in the shared catalog. When discovery fails, returns
no models, or you cancel it, the manual form remains usable.

Discovery is mock-validated in this build. Mock success does not establish live
provider availability or model catalog membership, which is mutable and
model-dependent.

## Recipe

```yaml
models:
  - alias: baseten-deepseek-v4
    display_name: "DeepSeek V4 (Baseten)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: baseten
    upstream_model: org/deepseek-v4
    max_output_tokens: 128000
```

The native profile inherits `base_url` and `api_key_env` from the registry.
No `base_url` or `api_key_env` field is needed in the model entry — the proxy
fills those from `known_auth: baseten` at load time.

## Custom deployment recipe

For a dedicated Baseten deployment with its own OpenAI-compatible endpoint,
use the custom endpoint flow instead:

```yaml
models:
  - alias: baseten-custom-deploy
    display_name: "Custom Baseten Deployment"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: "https://my-deployment.baseten.co/v1"
    api_key_env: CUSTOM_BASETEN_DEPLOY_KEY
    upstream_model: my-custom-model
    max_output_tokens: 128000
```

A custom deployment model does **not** carry `known_auth: baseten`. It uses its
own `base_url` and `api_key_env` explicitly. No model slug derives or selects
an origin from the native profile.

## Pass-through request fields

`extra_args` is a top-level pass-through escape hatch. Fields are merged into
every outgoing request body at the top level (not deep-merged). The proxy does
not maintain a restrictive allowlist and does not promise that unknown fields
are accepted upstream — it forwards them verbatim.

No provider-wide reasoning mode, capability, sampling, tier, or output-limit
default is applied to Baseten. Capabilities, reasoning options, and limits are
model-dependent.

## Factory sync

Factory sync writes only the managed local projection to
`~/.factory/settings.json`. A synced entry contains:

- local alias (`model`)
- display name (`displayName`)
- Factory provider (`generic-chat-completion-api`)
- local proxy `baseUrl` (`http://127.0.0.1:9787`)
- proxy placeholder/client-auth key (`apiKey`)
- `maxOutputTokens`

Factory sync **never** includes the Baseten upstream URL, `known_auth`, the
upstream model slug, any `extra_args`, the env-var name, or the upstream
credential. Skipping sync leaves Factory settings and backups unchanged.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "baseten-deepseek-v4",
      "displayName": "DeepSeek V4 (Baseten)",
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
export BASETEN_API_KEY=...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "baseten-deepseek-v4",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- The native profile is scoped to shared Model APIs. Dedicated/custom
  OpenAI-compatible deployments use the existing custom endpoint flow with an
  explicit `base_url`.
- `agent_ready` reflects configured or resolved generic capability metadata,
  not live Baseten discovery or model-specific feature support.
- Capabilities, reasoning options, and catalog membership are model-dependent
  and mutable.
- This mission validates generic OpenAI Chat transport through local fakes,
  not live credentialed calls.
