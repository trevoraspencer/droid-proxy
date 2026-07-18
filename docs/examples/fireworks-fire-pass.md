# Fireworks AI (Fire Pass)

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Fire Pass eligible routers on [Fireworks AI](https://fireworks.ai/) |

Fire Pass is an **experimental** Fireworks product that uses a separate
credential (`FIREWORKS_FIRE_PASS_API_KEY`) from Standard Fireworks
(`FIREWORKS_API_KEY`). Both use the same inference base URL and OpenAI Chat
protocol but are distinct profiles with independent keys and access scopes.

## Important caveats

- **Experimental product.** Fire Pass is experimental and its behavior,
  eligibility, pricing, and availability may change at any time.
- **Personal, non-production scope.** Fire Pass is documented as intended for
  personal, non-production agentic coding use. Check current official Fireworks
  documentation for the latest scope and terms.
- **Zero-token-cost claims are limited.** Any zero-token-cost benefit applies
  only to currently eligible routers used with an active Fire Pass. It does
  not apply to arbitrary Fireworks models, Standard/Priority models, or
  routers not marked Fire Pass eligible.
- **Arbitrary models are not free.** Fire Pass does not make arbitrary
  Fireworks models free. Only the curated Fire Pass-eligible routers in the
  static picker qualify, and only with an active pass.
- **Mutable availability and pricing.** Router eligibility, availability, and
  pricing are model-dependent and may change without notice. Mock validation
  during local testing does not establish live provider availability.

## Prerequisites

- Fire Pass key from Fireworks (experimental, personal/non-production agentic
  coding scope)
- Env var: `FIREWORKS_FIRE_PASS_API_KEY`

> Alternatively, run `./droid-proxy config` to set keys in `~/.droid-proxy/env`,
> write `config.yaml`, and sync Factory — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## Model discovery

Fire Pass uses a **static curated router catalog** — no remote discovery
request is made. The curated routers are reviewed against official Fire Pass
documentation (source:
[Fireworks Fire Pass docs](https://docs.fireworks.ai/firepass), as of
2026-07-15). The canonical router `accounts/fireworks/routers/glm-5p2-fast`
must be represented.

**Manual model entry is always available** alongside the curated list, so
experimental provider changes do not strand users. You can type any valid
Fireworks router ID directly.

Fire Pass and Standard Fireworks Fast catalogs use independent official-source
membership and may overlap. The Fire Pass catalog excludes routers not marked
Fire Pass eligible.

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

## Factory sync

Factory sync writes only the managed local projection. A synced entry contains
the local alias, display name, `generic-chat-completion-api`, local proxy
`baseUrl` (`http://127.0.0.1:9787`), proxy placeholder/client-auth key,
`maxOutputTokens`, and supported local reasoning metadata.

Factory sync **never** includes the Fireworks upstream URL, `known_auth`, the
upstream router ID, `service_tier`, any `extra_args`, the env-var name, or the
upstream credential. Skipping sync leaves Factory settings and backups
unchanged.

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
- Fire Pass and Standard Fireworks are distinct profiles with independent keys
  and independent catalog source membership.
- Fire Pass is not Fireworks Fast with `service_tier: fast`. Fast is a router
  model ID, not a `service_tier` value.
