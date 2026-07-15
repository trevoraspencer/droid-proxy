# Codex / ChatGPT OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth — native Codex Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `codex-responses` |
| **When to use** | GPT-5.6 models available to your ChatGPT/Codex account (no API key) |

Uses browser PKCE login instead of an API key. See [OAUTH.md](../OAUTH.md) for
the full OAuth walkthrough.

## Prerequisites

- ChatGPT account with Codex access to the selected model. Published availability
  currently includes Terra for Free/Go and Sol, Terra, and Luna for eligible
  Plus, Pro, Business, and Enterprise accounts; workspace policy and usage
  limits can narrow that list.
- OpenAI lists Codex CLI `0.144.0` as the minimum version for GPT-5.6. The proxy
  supplies equivalent `Version` and User-Agent fallbacks when callers omit them.
- OAuth login completed: `./droid-proxy auth codex --config config.yaml`
  - On headless machines, use `./droid-proxy auth codex --config config.yaml --device`.

> Alternatively, run `./droid-proxy config` to add OAuth models and manage
> accounts interactively — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: gpt-5.6
    display_name: "GPT-5.6 Sol (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-sol
    max_output_tokens: 128000
    max_context_tokens: 1050000
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

  - alias: gpt-5.6-fast # local Factory alias, not an OpenAI model ID
    display_name: "GPT-5.6 Sol Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-sol # fast keeps the same explicit upstream model
    max_output_tokens: 128000
    max_context_tokens: 1050000
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

  - alias: gpt-5.6-terra
    display_name: "GPT-5.6 Terra (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-terra
    max_output_tokens: 128000
    max_context_tokens: 1050000
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

  - alias: gpt-5.6-terra-fast
    display_name: "GPT-5.6 Terra Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-terra
    max_output_tokens: 128000
    max_context_tokens: 1050000
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

  - alias: gpt-5.6-luna
    display_name: "GPT-5.6 Luna (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-luna
    max_output_tokens: 128000
    max_context_tokens: 1050000
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

  - alias: gpt-5.6-luna-fast
    display_name: "GPT-5.6 Luna Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-luna
    max_output_tokens: 128000
    max_context_tokens: 1050000
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
| `gpt-5.6` | `gpt-5.6-sol` | standard, recommended local Sol alias |
| `gpt-5.6-fast` | `gpt-5.6-sol` | requests `service_tier: priority` |
| `gpt-5.6-terra` | `gpt-5.6-terra` | standard |
| `gpt-5.6-terra-fast` | `gpt-5.6-terra` | requests `service_tier: priority` |
| `gpt-5.6-luna` | `gpt-5.6-luna` | standard |
| `gpt-5.6-luna-fast` | `gpt-5.6-luna` | requests `service_tier: priority` |

The public OpenAI API documents `gpt-5.6` as an alias of `gpt-5.6-sol`.
Credentialed validation against the private Codex OAuth backend found that the
unsuffixed ID is rejected there, while `gpt-5.6-sol` succeeds. The local
`gpt-5.6` and `gpt-5.6-fast` aliases therefore both forward the explicit Sol
ID, and a duplicate explicit-Sol preset is intentionally omitted. On the
public API, Pro is `reasoning.mode: pro`, not a separate model ID. The proxy
preserves that value, but the credentialed test accounts returned upstream 400
for Pro on Sol, Terra, and Luna; there is no silent downgrade. Credentialed
`effort: max` without Pro succeeds, while mode availability remains
account/plan dependent.
See OpenAI's [model catalog](https://developers.openai.com/api/docs/models),
[GPT-5.6 guide](https://developers.openai.com/api/docs/guides/latest-model),
[reasoning guide](https://developers.openai.com/api/docs/guides/reasoning), and
[ChatGPT/Codex availability article](https://help.openai.com/en/articles/20001354)
for the public contract summarized here.

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
      "model": "gpt-5.6",
      "displayName": "GPT-5.6 Sol (Codex OAuth)",
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
    "model": "gpt-5.6",
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

- The GPT-5.6 IDs, limits, and capabilities above come from the public OpenAI
  API documentation. The explicit `gpt-5.6-sol` requirement for the private
  Codex OAuth path and Luna's need for current Codex client-version metadata
  come from credentialed validation. The proxy defaults missing `Version` and
  User-Agent signals to the officially documented Codex CLI `0.144.0` minimum
  while preserving explicit caller values. The broader private backend
  contract is not public, so actual model and mode availability must still be
  validated with your account. Upstream 4xx errors are surfaced; the proxy
  never silently downgrades GPT-5.6 to another model.
- Codex failover changes only the selected OAuth account. Every retry keeps the
  exact configured `upstream_model`.
- The proxy preserves `reasoning` exactly. Credentialed `effort: max` succeeds;
  `mode: pro` remains forwarded, but the tested accounts received an upstream
  400 on the private OAuth path, which the proxy surfaces without downgrade.
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
- Use `scripts/live-e2e/` for credentialed validation. The advanced GPT-5.6
  max-reasoning/cache-sanitization check is opt-in because it can consume more
  plan quota; it deliberately omits unsupported private-OAuth Pro mode.
- Tokens refresh automatically five minutes before expiry, with per-account locking to avoid concurrent refresh-token reuse.
- Codex requests include stable installation/session metadata, and token files may record quota telemetry from upstream that informs load-balancing eligibility.
- Ready-to-paste Factory snippet: [codex-oauth.json](../factory-settings/codex-oauth.json).
