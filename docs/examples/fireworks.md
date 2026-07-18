# Fireworks AI

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Models hosted on [Fireworks AI](https://fireworks.ai/) |

Fireworks offers four distinct serving paths on the same OpenAI Chat
Completions inference base:

| Path | Model ID shape | `service_tier` | Credential |
|------|---------------|-----------------|------------|
| **Standard** | `accounts/fireworks/models/...` | _(omitted)_ | `FIREWORKS_API_KEY` |
| **Priority** | `accounts/fireworks/models/...` | `priority` | `FIREWORKS_API_KEY` |
| **Fast** | `accounts/fireworks/routers/...` | _(omitted)_ | `FIREWORKS_API_KEY` |
| **Fire Pass** | `accounts/fireworks/routers/...` | _(omitted)_ | `FIREWORKS_FIRE_PASS_API_KEY` |

Fire Pass is a separate experimental product. See
[fireworks-fire-pass.md](fireworks-fire-pass.md) for Fire Pass recipes and
caveats.

## Prerequisites

- API key from Fireworks
- Env var: `FIREWORKS_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## Serving-path recipes

All four paths use the same inference base URL, protocol, and Factory provider.
The difference is the upstream model/router ID and the optional `service_tier`
in `extra_args`. No combination picker is required: each path is a distinct
model entry.

### Standard

Ordinary model ID, no `service_tier`:

```yaml
models:
  - alias: deepseek-v4-pro
    display_name: "DeepSeek V4 Pro (Fireworks Standard)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/deepseek-v4-pro
    max_output_tokens: 128000
    capabilities:
      reasoning: deepseek
```

### Priority

Same ordinary model ID plus `extra_args.service_tier: priority`:

```yaml
models:
  - alias: deepseek-v4-pro-priority
    display_name: "DeepSeek V4 Pro (Fireworks Priority)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/deepseek-v4-pro
    max_output_tokens: 128000
    capabilities:
      reasoning: deepseek
    extra_args:
      service_tier: priority
```

### Fast

Router model ID, no `service_tier`. Baseline Fast onboarding is always
tier-absent:

```yaml
models:
  - alias: glm-5p2-fast
    display_name: "GLM-5.2 Fast (Fireworks)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/routers/glm-5p2-fast
    max_output_tokens: 128000
```

### Explicit Fast + Priority (snapshot-supported only)

A Fast router combined with `service_tier: priority` is preserved unchanged
only when the committed official snapshot marks that exact router/tier pair
supported. The proxy neither infers nor synthesizes this combination — you
must configure it explicitly. Availability and price are model-dependent and
mutable. As of the committed source snapshot (2026-07-15),
`accounts/fireworks/routers/glm-5p2-fast` is the supported Fast router:

```yaml
models:
  - alias: glm-5p2-fast-priority
    display_name: "GLM-5.2 Fast Priority (Fireworks)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/routers/glm-5p2-fast
    max_output_tokens: 128000
    extra_args:
      service_tier: priority
```

## Model discovery and manual entry

Standard model discovery uses the authenticated `GET /inference/v1/models`
endpoint as a **best-effort compatibility** attempt. It is not the official
account-scoped Fireworks List Models API, and mock success during local
validation does not establish live provider availability.

**Manual model entry is the guaranteed native onboarding path.** You can
always type any valid Fireworks model or router ID directly, including IDs
absent from the discovery result or from the curated Fast catalog. Surrounding
interactive whitespace is trimmed; model/router punctuation, case, path
components, and suffixes remain byte-exact without provider-prefix or catalog
normalization.

## Pass-through request fields (`extra_args`)

`extra_args` is a top-level pass-through escape hatch. Fields are merged into
every outgoing request body at the top level (not deep-merged). The proxy does
not maintain a restrictive allowlist and does not promise that unknown fields
are accepted upstream — it forwards them verbatim.

The proxy does **not** invent a static session-affinity value, a
`prompt_cache_key`, or any other synthetic field. Unrelated conversations are
never pinned together. Explicit caller or configured values pass through
unchanged subject to security header filtering.

Documented Fireworks-relevant fields include:

| Field | Type | Notes |
|-------|------|-------|
| `service_tier` | string | `priority` for Priority serving path; omitted for Standard/baseline Fast |
| `reasoning_effort` | string | e.g. `low`, `high` |
| `reasoning_history` | string | e.g. `interleaved` |
| `thinking` | object | e.g. `{"type":"enabled"}` |
| `prompt_cache_key` | string | explicit cache key; never synthesized |
| `prompt_cache_isolation_key` | string | explicit isolation key; never synthesized |
| `perf_metrics_in_response` | boolean | request provider performance metrics |
| `context_length_exceeded_behavior` | string | e.g. `truncate` |
| `response_format` | object | JSON schema or JSON object mode |
| `min_p` | number | min-p sampling |
| `top_k` | integer | top-k sampling |
| `repetition_penalty` | number | repetition penalty |
| `tools` | array | tool/function definitions |
| `tool_choice` | string/object | tool selection strategy |
| `parallel_tool_calls` | boolean | allow parallel tool calls |
| `stream` | boolean | enable SSE streaming |
| `stream_options` | object | e.g. `{"include_usage":true}` |

## Factory sync

Factory sync writes only the managed local projection to
`~/.factory/settings.json`. A synced entry contains:

- local alias (`model`)
- display name (`displayName`)
- Factory provider (`generic-chat-completion-api`)
- local proxy `baseUrl` (`http://127.0.0.1:9787`)
- proxy placeholder/client-auth key (`apiKey`)
- `maxOutputTokens`
- supported local reasoning metadata

Factory sync **never** includes the Fireworks upstream URL, `known_auth`, the
upstream model/router ID, `service_tier`, any `extra_args`, the env-var name,
or the upstream credential. Skipping sync leaves Factory settings and backups
unchanged.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "deepseek-v4-pro",
      "displayName": "DeepSeek V4 Pro (Fireworks Standard)",
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
export FIREWORKS_API_KEY=fw_...
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-pro",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- Fireworks Standard model IDs use the `accounts/fireworks/models/...` prefix.
- Fireworks Fast router IDs use the `accounts/fireworks/routers/...` prefix.
- Enable `capabilities.reasoning: deepseek` for DeepSeek models that return
  `reasoning_content`.
- Priority, Fast, and snapshot-supported combinations are model-dependent and
  mutable. Check current official Fireworks documentation for eligibility.
- `extra_args.service_tier: fast` is **not** a valid Fireworks configuration;
  Fast is represented by a router model ID, not by `service_tier`.
