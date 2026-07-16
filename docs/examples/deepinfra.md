# DeepInfra

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Models hosted on [DeepInfra](https://deepinfra.com/) |

The native `deepinfra` profile uses the OpenAI-compatible inference base URL
`https://api.deepinfra.com/v1/openai` with OpenAI Chat Completions. Model IDs
are opaque Hugging Face-style identifiers (e.g.
`meta-llama/Llama-3.3-70B-Instruct`), version-suffixed names, and private
deployment identifiers (`deploy_id:...`). They are preserved byte-for-byte
without provider-prefix or catalog normalization.

DeepInfra inference and public catalog discovery use different paths and
authentication policies. Inference requires a Bearer token; catalog discovery
is unauthenticated and lives at a separate origin.

## Prerequisites

- Token from DeepInfra
- Env var: `DEEPINFRA_TOKEN`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## Model discovery and manual entry

Interactive onboarding uses unauthenticated
`GET https://api.deepinfra.com/models/list` with `Accept: application/json`
and **no** `Authorization` or other credential header. The proxy does not
fall back to `/v1/models`, `/v1/openai/models`, or any undocumented alias.

The official-contract response is a bare top-level JSON array. The proxy reads
opaque IDs from `model_name`, retains only records whose exact
`reported_type` is `text-generation`, and excludes non-LLM rows (image
generation, embeddings, audio, etc.). Results are then sorted and
de-duplicated.

**Manual model entry is the guaranteed native onboarding path.** You can
type any valid model ID directly, including private deployment identifiers
(`deploy_id:...`) and IDs absent from the public catalog. When discovery
fails, returns no models, or you cancel it, the manual form remains usable.
Surrounding interactive whitespace is trimmed; model ID punctuation, case,
path components, and suffixes remain byte-exact without normalization.

Discovery is mock-validated in this build. Mock success does not establish
live provider availability or model catalog membership, which is mutable and
model-dependent.

## Serving paths and tiers

DeepInfra serving paths use `service_tier` on supported models. The proxy
does not impose a restrictive local tier enum — it forwards configured
`service_tier` values unchanged and the effective tier is determined and
echoed by DeepInfra in the response.

| Path | Request `service_tier` | Effective response literal |
|------|------------------------|---------------------------|
| **Standard** | _(omitted)_ | `"default"` (or absent) |
| **Priority** | `"priority"` | `"priority"` (success) or `"default"` (fallback) |
| **Flex** | `"flex"` | `"flex"` (success) |

The default TUI/onboarding flow creates a Standard model entry with no
`service_tier`. Priority and Flex are configured through documented config
entries — no TUI tier picker is required. The response `service_tier` is
authoritative: a successful Priority response retains `"priority"`, a
successful Flex response retains `"flex"`, and an unsupported-Priority
fallback retains provider literal `"default"`, meaning effective Standard.
The proxy relays all of these verbatim without rewriting, retry, or tier
synthesis.

### Known official-page contradiction (Flex enum)

As of 2026-07-15, the DeepInfra Chat overview documents `service_tier`
`"flex"` alongside `"priority"` and `"default"`, while the generated OpenAPI
tier enum omits `"flex"`. This mission treats the discrepancy as a reason
for generic pass-through rather than a restrictive local enum. The proxy
forwards configured `service_tier` values unchanged and does not validate
against a local enum. Tier eligibility is mutable and model-dependent.

## Recipe

### Standard (no service_tier)

The default TUI flow creates this entry:

```yaml
models:
  - alias: deepinfra-llama-33-70b
    display_name: "Llama 3.3 70B (DeepInfra)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepinfra
    upstream_model: meta-llama/Llama-3.3-70B-Instruct
    max_output_tokens: 128000
```

### Priority (exact "priority")

```yaml
models:
  - alias: deepinfra-llama-33-70b-priority
    display_name: "Llama 3.3 70B Priority (DeepInfra)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepinfra
    upstream_model: meta-llama/Llama-3.3-70B-Instruct
    max_output_tokens: 128000
    extra_args:
      service_tier: priority
```

### Flex (exact "flex")

```yaml
models:
  - alias: deepinfra-llama-33-70b-flex
    display_name: "Llama 3.3 70B Flex (DeepInfra)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepinfra
    upstream_model: meta-llama/Llama-3.3-70B-Instruct
    max_output_tokens: 128000
    extra_args:
      service_tier: flex
```

### Private deployment (opaque deploy_id)

```yaml
models:
  - alias: deepinfra-private-deploy
    display_name: "Private Deployment (DeepInfra)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepinfra
    upstream_model: org/custom-model:deploy_id:abc123
    max_output_tokens: 128000
```

The native profile inherits `base_url` and `api_key_env` from the registry.
No `base_url` or `api_key_env` field is needed in the model entry — the proxy
fills those from `known_auth: deepinfra` at load time. Explicit `base_url`,
`api_key_env`, and `upstream_protocol` overrides are preserved if set.

## Pass-through request fields

`extra_args` is a top-level pass-through escape hatch. Fields are merged into
every outgoing request body at the top level (not deep-merged). The proxy does
not maintain a restrictive allowlist and does not promise that unknown fields
are accepted upstream — it forwards them verbatim.

The proxy does not invent a static session-affinity value, a
`prompt_cache_key`, or any other synthetic field. Explicit caller or
configured values pass through unchanged subject to security header filtering.

No provider-wide reasoning mode, capability, sampling, tier, or output-limit
default is applied to DeepInfra. Capabilities, reasoning options, tier
eligibility, and limits are model-dependent and mutable.

Documented DeepInfra-relevant pass-through fields include:

| Field | Type | Notes |
|-------|------|-------|
| `service_tier` | string | `"priority"` or `"flex"`; omitted for Standard |
| `reasoning_effort` | string | e.g. `low`, `high` (model-dependent) |
| `reasoning` | object | e.g. `{"effort":"high","exclude":false}` (model-dependent) |
| `reasoning_content` | string | reasoning output relayed unchanged (model-dependent) |
| `chat_template_kwargs` | object | e.g. `{"enable_thinking":true}` (model-dependent) |
| `prompt_cache_key` | string | explicit cache key; never synthesized |
| `cache_control` | object | message-level cache control; model-dependent |
| `response_format` | object | JSON schema or JSON object mode |
| `temperature` | number | sampling temperature |
| `top_p` | number | nucleus sampling |
| `max_tokens` | integer | max completion tokens |
| `seed` | integer | deterministic sampling seed |
| `stop` | array | stop sequences |
| `stream` | boolean | enable SSE streaming |
| `stream_options` | object | e.g. `{"include_usage":true}` |
| `tools` | array | tool/function definitions |
| `tool_choice` | string/object | tool selection strategy |

## Factory sync

Factory sync writes only the managed local projection to
`~/.factory/settings.json`. A synced entry contains:

- local alias (`model`)
- display name (`displayName`)
- Factory provider (`generic-chat-completion-api`)
- local proxy `baseUrl` (`http://127.0.0.1:9787`)
- proxy placeholder/client-auth key (`apiKey`)
- `maxOutputTokens`

Factory sync **never** includes the DeepInfra upstream URL, `known_auth`, the
upstream model, any `extra_args` (including `service_tier`), the env-var name,
or the upstream credential. Skipping sync leaves Factory settings and backups
unchanged.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "deepinfra-llama-33-70b",
      "displayName": "Llama 3.3 70B (DeepInfra)",
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
export DEEPINFRA_TOKEN=...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepinfra-llama-33-70b",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Inference base is `https://api.deepinfra.com/v1/openai`. Discovery uses the
  unauthenticated `GET https://api.deepinfra.com/models/list` endpoint at a
  separate origin.
- The proxy does not fall back to `/v1/models`, `/v1/openai/models`, or any
  undocumented alias for discovery.
- Model IDs, version suffixes, and `deploy_id:...` values are opaque and
  preserved byte-for-byte without normalization.
- Standard requests omit `service_tier`. Priority sends exact `"priority"` and
  Flex sends exact `"flex"`. The effective tier is authoritative and echoed by
  DeepInfra: successful Priority/Flex retain `"priority"`/`"flex"`, while
  unsupported-Priority fallback retains `"default"` (effective Standard).
- As of 2026-07-15, the Chat overview documents Flex while the generated
  OpenAPI tier enum omits it. The proxy resolves this in favor of pass-through
  rather than a restrictive local enum.
- Tier eligibility, reasoning capabilities, cache support, context windows,
  and output limits are model-dependent and mutable. No requested tier is
  guaranteed; the effective tier depends on the model and DeepInfra's
  backend.
- `max_output_tokens` is explicitly configured model metadata or the
  documented local sync fallback, never a DeepInfra guarantee.
- `agent_ready` reflects configured or resolved generic capability metadata,
  not live DeepInfra discovery or model-specific feature support.
- This mission validates generic OpenAI Chat transport through local fakes,
  not live credentialed calls.
