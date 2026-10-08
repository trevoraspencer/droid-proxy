# Codex / ChatGPT OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth — native Codex Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `codex-responses` |
| **When to use** | current Codex models available to your ChatGPT/Codex account (no API key) |

Uses browser PKCE login instead of an API key. See [OAUTH.md](../OAUTH.md) for
the full OAuth walkthrough.

## Prerequisites

- ChatGPT account with Codex access to the selected model. Availability depends
  on your plan, workspace policy, rollout, and usage limits. Check the current
  [Codex model guide](https://learn.chatgpt.com/docs/models).
- The proxy supplies Codex client version `0.162.0` metadata when omitted by
  callers; `extra_headers` can override it as the private backend evolves.
- OAuth login completed: `./droid-proxy auth codex --config config.yaml`
  - On headless machines, use `./droid-proxy auth codex --config config.yaml --device`.

> Alternatively, run `./droid-proxy config` to add OAuth models and manage
> accounts interactively — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: gpt-6.1-sol
    display_name: "GPT-6.1 Sol (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6.1-sol
    max_output_tokens: 128000
    max_context_tokens: 272000
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-6.1-sol-fast # local Factory alias, not an OpenAI model ID
    display_name: "GPT-6.1 Sol Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6.1-sol # fast keeps the same explicit upstream model
    max_output_tokens: 128000
    max_context_tokens: 272000
    extra_args:
      service_tier: priority
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-6-astra
    display_name: "GPT-6 Astra (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6-astra
    max_output_tokens: 128000
    max_context_tokens: 272000
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-6-astra-fast
    display_name: "GPT-6 Astra Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6-astra
    max_output_tokens: 128000
    max_context_tokens: 272000
    extra_args:
      service_tier: priority
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-6-luna
    display_name: "GPT-6 Luna (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6-luna
    max_output_tokens: 128000
    max_context_tokens: 272000
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-6-luna-fast
    display_name: "GPT-6 Luna Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-6-luna
    max_output_tokens: 128000
    max_context_tokens: 272000
    extra_args:
      service_tier: priority
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true
```

The interactive dashboard offers the following presets. Fast names are local
Factory aliases; they never change the upstream model ID.

| Local alias | Upstream model | Mode |
|---|---|---|
| `gpt-6.1-sol` | `gpt-6.1-sol` | standard, recommended local Sol alias |
| `gpt-6.1-sol-fast` | `gpt-6.1-sol` | requests `service_tier: priority` |
| `gpt-6-astra` | `gpt-6-astra` | standard |
| `gpt-6-astra-fast` | `gpt-6-astra` | requests `service_tier: priority` |
| `gpt-6-luna` | `gpt-6-luna` | standard |
| `gpt-6-luna-fast` | `gpt-6-luna` | requests `service_tier: priority` |

The picker uses explicit upstream IDs from the current
[Codex model guide](https://learn.chatgpt.com/docs/models). Standard and fast
aliases share a model; fast requests `service_tier: priority`. The effective
service tier and reasoning modes depend on the account and backend. The
272,000-token context metadata follows the reviewed Codex client catalog;
public API limits can differ. These profiles were checked against source and
local protocol fixtures, not a credentialed session. See
[MAINTENANCE.md](../MAINTENANCE.md) for the dated sources and validation scope.

Optional: pin a specific logged-in account:

```yaml
    oauth_account: user@example.com
```

## Multi-account load balancing (Codex only)

When multiple Codex accounts are logged in, the proxy selects among them using
a configurable strategy. Configure under `oauth.load_balancing` in `config.yaml`
(see [CONFIG.md](../CONFIG.md#oauthload_balancing)):

```yaml
oauth:
  load_balancing:
    strategy: sticky           # sticky (default), round-robin, fill-first, least-connections, random
    quota_soft_cap_percent: 80 # prefer accounts below this usage (0 = off)
    max_failovers: 2         # additional alternate-account attempts
    rate_limit_cooldown: 60s # cooldown after 429 without Retry-After or exhausted-window reset
    error_cooldown: 30s      # cooldown after 5xx or transport timeout
```

All fields have defaults; omit the block or individual fields to use defaults.
This only applies to Codex OAuth — xAI OAuth is always single-account.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "gpt-6.1-sol",
      "displayName": "GPT-6.1 Sol (Codex OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:9787",
      "apiKey": "x",
      "maxOutputTokens": 128000,
      "reasoningEffort": "max"
    }
  ]
}
```

## Run

```bash
./droid-proxy auth codex --config config.yaml
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-6.1-sol",
    "input": "hello"
  }' | jq '.output'
```

## Manage accounts

```bash
./droid-proxy auth status codex                  # list accounts + expiry
./droid-proxy auth disable codex user@example.com
./droid-proxy auth logout  codex user@example.com
```

Check the model is logged in: `curl -s http://127.0.0.1:9787/v1/models | jq
'.data[] | select(.oauth_auth) | {id, oauth_auth}'`. See
[OAUTH.md](../OAUTH.md#managing-accounts) for the full reference.
For pool-level readiness, run `./droid-proxy auth pool --config config.yaml`;
the `STATUS` column explains skips such as `disabled`, `rate_limited`, or
`expired_no_refresh`.

## Notes

- Presets follow the October 2026 model catalog. Existing user aliases remain
  configurable, including older models still available to your account.
- Codex failover changes only the selected OAuth account. Every retry keeps the
  exact configured `upstream_model`.
- The proxy preserves `reasoning` and surfaces unavailable-model or mode errors
  without silently downgrading the configured model.
- Public `prompt_cache_options` is stripped because the private OAuth endpoint
  rejects it. Private prompt caching remains keyed by the preserved
  `prompt_cache_key`.
- The proxy normalizes requested `service_tier: fast` to `priority`, but the
  effective tier is account/backend dependent and reported in the response.
- The proxy removes `max_output_tokens` for private-endpoint compatibility.
- The OAuth path also strips `previous_response_id`, `safety_identifier`,
  legacy `prompt_cache_retention`, and `stream_options`. That means public API
  features depending on those fields are not available through this path until
  credentialed evidence establishes private-endpoint support.
- Use `scripts/live-e2e/` for credentialed validation. The advanced current Codex
  max-reasoning/cache-sanitization check is opt-in because it can consume more
  plan quota; it deliberately omits unsupported private-OAuth Pro mode.
- Tokens refresh automatically five minutes before expiry, with per-account locking to avoid concurrent refresh-token reuse.
- Codex requests include stable installation/session metadata, and token files may record quota telemetry from upstream that informs load-balancing eligibility.
- Ready-to-paste Factory snippet: [codex-oauth.json](../factory-settings/codex-oauth.json).
