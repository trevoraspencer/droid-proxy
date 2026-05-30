# Codex / ChatGPT OAuth

## Overview

| | |
|---|---|
| **Tier** | T1 OAuth — native Codex Responses passthrough |
| **Factory mode** | `openai` |
| **Upstream protocol** | `codex-responses` |
| **When to use** | Codex models via your ChatGPT/Codex subscription (no API key) |

Uses browser PKCE login instead of an API key. See [OAUTH.md](../OAUTH.md) for
the full OAuth walkthrough.

## Prerequisites

- ChatGPT account with Codex access
- OAuth login completed: `./droid-proxy auth codex --config config.yaml`
  - On headless machines, use `./droid-proxy auth codex --config config.yaml --device`.

> Alternatively, run `./droid-proxy config` to add OAuth models and manage
> accounts interactively — see
> [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: gpt-5.2-codex
    display_name: "GPT-5.2 Codex (ChatGPT OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.2-codex
    max_output_tokens: 128000
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
      "model": "gpt-5.2-codex",
      "displayName": "GPT-5.2 Codex (ChatGPT OAuth)",
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
./droid-proxy auth codex --config config.yaml
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.2-codex",
    "input": "hello"
  }' | jq '.output'
```

## Manage accounts

```bash
./droid-proxy auth status codex                  # list accounts + expiry
./droid-proxy auth disable codex user@example.com
./droid-proxy auth logout  codex user@example.com
```

Check the model is logged in: `curl -s http://127.0.0.1:8787/v1/models | jq
'.data[] | select(.oauth_auth) | {id, oauth_auth}'`. See
[OAUTH.md](../OAUTH.md#managing-accounts) for the full reference.

## Notes

- Replace `upstream_model` with the Codex model ID your account supports.
- Tokens refresh automatically five minutes before expiry, with per-account locking to avoid concurrent refresh-token reuse.
- Codex requests include stable installation/session metadata, and token files may record passive quota hints from upstream.
- Ready-to-paste Factory snippet: [codex-oauth.json](../factory-settings/codex-oauth.json).
