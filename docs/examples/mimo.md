# Xiaomi MiMo

Xiaomi MiMo exposes an OpenAI-compatible Chat Completions endpoint at
`https://api.xiaomimimo.com/v1`. It also supports a separate Token Plan with
regional base URLs. droid-proxy ships `known_auth` shortcuts for both.

MiMo thinking mode returns `reasoning_content` alongside tool calls. Xiaomi's
docs require that field to be preserved in later turns that include those tool
calls, otherwise the API can return `400`. droid-proxy uses the same reasoning
replay path as DeepSeek for MiMo, so use Factory's `generic-chat-completion-api`
mode for agent workflows.

## config.yaml

```yaml
models:
  - alias: mimo-v2.5-pro
    display_name: "MiMo V2.5 Pro (Xiaomi MiMo)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: mimo
    upstream_model: mimo-v2.5-pro
    max_output_tokens: 131072
    max_context_tokens: 1048576
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
```

`known_auth: mimo` fills in:

- `base_url: https://api.xiaomimimo.com/v1`
- `api_key_env: MIMO_API_KEY`
- `api-key: $MIMO_API_KEY` as the upstream auth header
- `upstream_protocol: openai-chat`
- `capabilities.reasoning: deepseek`

Token Plan profiles:

| known_auth | base URL | env var |
|---|---|---|
| `mimo-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_CN_API_KEY` |
| `mimo-token-plan-sgp` | `https://token-plan-sgp.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_SGP_API_KEY` |
| `mimo-token-plan-ams` | `https://token-plan-ams.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_AMS_API_KEY` |

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "mimo-v2.5-pro",
      "displayName": "MiMo V2.5 Pro (Xiaomi MiMo)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 131072
    }
  ]
}
```

## Run

```bash
export MIMO_API_KEY=sk-...
droid-proxy --config config.yaml
```

For Token Plan, use the matching Token Plan env var instead, for example:

```bash
export MIMO_TOKEN_PLAN_SGP_API_KEY=tp-...
```

## Notes

- Xiaomi's current V2.5 defaults are `temperature: 1.0` and `top_p: 0.95`.
  In thinking mode, `mimo-v2.5-pro` and `mimo-v2.5` force temperature to the
  provider default even if a different value is sent.
- `mimo-v2.5-pro` and `mimo-v2.5` have a 1M context window and 128K maximum
  output. `mimo-v2-flash` has a 256K context window and 64K maximum output.
- Xiaomi's OpenAI examples use `max_completion_tokens`. Droid's generic Chat
  path normally manages output length itself; add a fixed
  `extra_args.max_completion_tokens` only if you intentionally want to override
  every request for this model.
- Xiaomi bills prompt cache hits differently from cache misses, but does not
  document a `cache_control` request field for this Chat path. Leave
  `capabilities.prompt_caching` unset unless Xiaomi documents an explicit API.
- Web search is a provider-side tool with separate activation and billing.
  Do not enable it by default for Droid agent runs.
