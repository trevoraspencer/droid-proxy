# Configuration reference

`droid-proxy` reads a YAML file (default `./config.yaml`, override with
`--config`). Every string value supports `${VAR}` and `${VAR:-default}` env
expansion — secrets stay in the environment, not in the file.

A working example lives at [`config.example.yaml`](../config.example.yaml).

## Process management

The YAML file describes the HTTP server only. Starting, stopping, and installing
as a background service are CLI commands — see [CLI.md](CLI.md).

Typical flow (interactive — preferred):

```bash
./droid-proxy config    # onboard providers/models, store keys, sync Factory
./droid-proxy start --config config.yaml
```

Manual file-based alternative:

```bash
cp config.example.yaml config.yaml
cp .env.local.example .env.local
set -a && source .env.local && set +a
./droid-proxy start --config config.yaml
```

## Environment files

Load API keys from shell-style env files instead of exporting them manually.
Keys are loaded in **layers**, with later layers overriding earlier ones. See
[CLI.md](CLI.md#config-and-env-file-resolution) for the full resolution order.

| Layer | Source | Behavior |
|-------|--------|----------|
| Base | `~/.droid-proxy/env` | Managed secrets file written by `droid-proxy config` (chmod 600). Always loaded. |
| Override | `--env-file PATH` | Explicit path for foreground mode, `start`, and user services. |
| Override (default) | Repo env file | When `--env-file` is omitted: `.env.local` in the config directory, if present. |

This means keys onboarded via `droid-proxy config` are available even when a
repo `.env.local` also exists, while `.env.local` can override matching names.

Env files support `KEY=value` or `export KEY=value` lines. Comments (`#`) and
blank lines are ignored when loading. Missing files are skipped without error.
Values written by `droid-proxy config` use `export KEY="..."` quoting; the
loader unescapes double-quoted values so special characters round-trip
correctly.

See [`.env.local.example`](../.env.local.example) for a template of all supported
API key env vars. OAuth tokens are **not** loaded from env files — use
`droid-proxy auth codex` / `auth xai` ([OAUTH.md](OAUTH.md)).

## Top-level structure

```yaml
listen: {...}            # bind address
server: {...}            # inbound body cap and HTTP server timeouts
client_auth: {...}       # require an api key from Droid (optional)
logging: {...}           # log level, format, secret redaction
reasoning_cache: {...}   # DeepSeek-style reasoning replay
upstream: {...}          # http client timeouts and response caps
oauth: {...}             # Codex/ChatGPT and xAI OAuth token storage/callbacks
models:                  # required, non-empty
  - alias: ...
  - alias: ...
```

## `listen`

| key | type | default | description |
|---|---|---|---|
| `host` | string | `127.0.0.1` | Bind host. Use `0.0.0.0` to expose beyond localhost. |
| `port` | int | `9787` | TCP port. Use `droid-proxy migrate-port` to migrate an explicit `8787` config. |

## `server`

Inbound request limits and HTTP server timeout knobs. Defaults are finite when
fields are omitted. For the cap and timeout fields below, an explicit `0` or
`0s` opts out and is preserved by the loader; negative values are rejected.
Runtime enforcement of request-body limiting is implemented by the security
middleware features that consume this schema.

| key | type | default | description |
|---|---|---|---|
| `request_body_max_bytes` | int | `10485760` | Maximum downstream request body size in bytes. `0` opts out. |
| `read_header_timeout` | duration | `30s` | Maximum time to read request headers. `0s` opts out. |
| `read_timeout` | duration | `60s` | Maximum time to read an entire request, including body. `0s` opts out. |
| `write_timeout` | duration | `600s` | Maximum time before response writes time out. `0s` opts out. |
| `idle_timeout` | duration | `120s` | Maximum keep-alive idle time between requests. `0s` opts out. |
| `shutdown_timeout` | duration | `5s` | Graceful shutdown deadline after cancellation. `0s` opts out of adding a shutdown deadline. |

## `client_auth`

| key | type | default | description |
|---|---|---|---|
| `enabled` | bool | `false` | When true, every non-health request must present a valid api key. |
| `api_keys` | []string | `[]` | Accepted keys. At least one required when `enabled: true`. |
| `header` | string | `Authorization` | Header to read. |
| `scheme` | string | `Bearer` | Prefix scheme expected before the key. Empty string means raw value. |

## `logging`

| key | type | default | description |
|---|---|---|---|
| `level` | string | `info` | `trace`, `debug`, `info`, `warn`, `error`. |
| `format` | string | `text` | `text` or `json`. |
| `redact` | bool | `true` | Apply secret redaction to logs. |
| `trace_requests` | bool | `false` | Log request + response bodies at trace level (redacted). Useful for debugging. |

## `reasoning_cache`

DeepSeek (and DeepSeek-compatible) APIs return a `reasoning_content` field
alongside tool calls. Anthropic-style "extended thinking" tools require this
field to be re-supplied on subsequent turns. droid-proxy captures and replays
it automatically for models with `capabilities.reasoning: deepseek`. Provider
profiles such as `deepseek` and `mimo` also set provider-specific
`extra_args.thinking.type: enabled`; replay and upstream thinking are related
but separate settings.

| key | type | default | description |
|---|---|---|---|
| `enabled` | bool | `true` | Disable to opt out entirely. |
| `max_entries` | int | `1024` | LRU + TTL eviction; cache grows no larger. |
| `ttl` | duration | `30m` | How long a captured reasoning blob is kept. |

The cache is in-memory only by design. Cold starts lose history.

## `upstream`

Upstream HTTP client controls. Defaults are finite when fields are omitted.
`stream_keep_alive: 0s`, `response_body_max_bytes: 0`, and
`error_body_max_bytes: 0` opt out and are preserved; negative values are
rejected. Runtime enforcement of response/error body caps is implemented by
the security-hardening features that consume this schema.

| key | type | default | description |
|---|---|---|---|
| `http_timeout` | duration | `600s` | Per-request upstream timeout. Generous because completions can be long. |
| `stream_keep_alive` | duration | `15s` | SSE comment-frame heartbeat interval. `0s` opts out. |
| `response_body_max_bytes` | int | `104857600` | Maximum upstream non-stream success body size in bytes. `0` opts out. |
| `error_body_max_bytes` | int | `1048576` | Maximum upstream error body size in bytes. `0` opts out. |

## `oauth`

OAuth is optional and only used by models whose `upstream_protocol` is
`codex-responses` or `xai-responses`. Token files are written with restrictive
permissions and token values are never logged by the auth commands.

| key | type | default | description |
|---|---|---|---|
| `auth_dir` | string | `~/.droid-proxy/auth` | Directory for saved OAuth token JSON files. |
| `codex_callback_host` | string | `localhost` | Loopback host for Codex/ChatGPT OAuth login. |
| `codex_callback_port` | int | `1455` | Loopback port for Codex/ChatGPT OAuth login. |
| `xai_callback_host` | string | `127.0.0.1` | Loopback host for xAI OAuth login. |
| `xai_callback_port` | int | `56121` | Loopback port for xAI OAuth login. |

### `oauth.load_balancing`

Codex OAuth multi-account pooling configuration. This block applies **only** to
Codex OAuth (`codex-responses`) accounts. xAI OAuth remains single-account and
is unaffected by these settings. All four fields have defaults; omit the block
or any individual field to use defaults.

| key | type | default | description |
|---|---|---|---|
| `strategy` | enum | `sticky` | Account selection: `sticky` (per-conversation affinity, default), `round-robin`, `fill-first`, `least-connections`, or `random`. |
| `max_failovers` | int | `2` | Maximum additional alternate-account attempts on retryable errors. `max_failovers=0` disables failover (single attempt only). |
| `rate_limit_cooldown` | duration | `60s` | Cooldown after a `429` when no `Retry-After` header or exhausted-window quota reset is available. `0s` means no cooldown. |
| `error_cooldown` | duration | `30s` | Cooldown after `5xx` or transport timeout. `0s` means no cooldown. |
| `quota_soft_cap_percent` | float | `80` | Prefer accounts below this `used_percent` on primary/secondary windows. `0` disables proactive avoidance. |
| `affinity_path` | string | `~/.droid-proxy/conversation_affinity.json` | Persisted conversation→account map for `sticky`. |
| `affinity_max_entries` | int | `10000` | Maximum affinity bindings stored on disk. |
| `affinity_ttl` | duration | `720h` | Drop affinity entries older than this TTL. |

Authenticate before starting the proxy:

```bash
droid-proxy auth codex --config config.yaml
droid-proxy auth xai --config config.yaml
```

## `models[]` (required)

Each model entry maps a public alias (what Droid sends as `model`) to a
specific upstream configuration.

| key | type | required | description |
|---|---|---|---|
| `alias` | string | ✅ | Provider-native model ID that Droid sends as `model` (e.g. `deepseek-v4-flash`, `glm-5.2`). |
| `display_name` | string |  | Human-readable name surfaced in `/v1/models`. Use `{Model name} ({Provider label})`. |
| `factory_provider` | enum | ✅ | One of `anthropic`, `openai`, `generic-chat-completion-api`. Picks which Droid endpoint protocol the proxy serves. |
| `upstream_protocol` | enum | ✅ | One of `anthropic-messages`, `openai-responses`, `openai-chat`, `codex-responses`, `xai-responses`. Picks how the proxy talks to the real provider. See the matrix in [PROVIDERS.md](PROVIDERS.md). |
| `oauth_provider` | enum | for OAuth upstreams | `codex` for `codex-responses`, or `xai` for `xai-responses`. |
| `oauth_account` | string |  | Optional stored OAuth account selector. Matches saved email, subject, account id, or token filename stem. |
| `base_url` | string | one of `base_url`, `known_auth`, or OAuth upstream required | Upstream root URL. e.g. `https://api.deepseek.com/v1`. OAuth upstreams use their provider default when omitted. |
| `known_auth` | string |  | Shortcut: looks up base_url, env var, auth header, version headers, and model-discovery metadata from a built-in registry. See PROVIDERS.md. Use `zai-coding-api` for Z.AI GLM Coding Plan keys and `zai-main-api` for normal Z.AI API keys. |
| `upstream_model` | string |  | Forwarded `model` field on the upstream call. If unset, the alias itself is sent. |
| `api_key_env` | string |  | Env var holding the API key. If unset, the env var declared by `known_auth` is used. Not required for OAuth upstreams. |
| `max_output_tokens` | int |  | Factory-facing output-token setting; surfaced in `/v1/models`. When omitted, Factory sync writes `128000`. Set an explicit lower value for upstreams with lower hard caps. |
| `max_context_tokens` | int |  | Informational; surfaced in `/v1/models`. |
| `extra_headers` | map[string]string |  | Headers appended to every upstream request for this model. |
| `extra_args` | map[string]any |  | Top-level fields merged into every outgoing request body (e.g. `temperature`, `stream_options`, or `service_tier: priority` for a local GPT-5.6 fast alias). |
| `capabilities` | object |  | Capability overrides. See below. |

`extra_headers` ignores proxy-managed or security-sensitive names, including
auth headers, `Host`, cookies, forwarded-client headers, hop-by-hop headers, and
`Accept-Encoding`. The proxy manages response compression itself so body limits
and downstream response headers stay consistent.

### `capabilities`

All optional. Defaults are reasonable for most OpenAI-compatible providers.

| key | type | default | description |
|---|---|---|---|
| `streaming` | bool | `true` | Whether the model supports SSE streaming. |
| `tools` | bool | `true` | Whether the model supports tool/function calling. |
| `tool_result_safe` | bool | `true` | Whether the model handles `role: "tool"` (or Anthropic `tool_result`) replies correctly. |
| `images` | bool | `false` | Whether the model accepts image inputs. |
| `json_mode` | bool | `true` | Whether `response_format: {"type":"json_object"}` works. |
| `structured_output` | bool | `false` | Whether JSON-Schema-constrained output (`response_format: {"type":"json_schema"}`) works. |
| `reasoning` | enum | `none` | `none`, `deepseek`, or `anthropic-thinking`. `deepseek` enables reasoning replay. |
| `factory_reasoning` | enum | protocol default | `drop` removes Factory's top-level `reasoning` object before upstream; `passthrough` preserves it. Defaults to `drop` for `xai-responses` and `passthrough` elsewhere. |
| `factory_reasoning_effort` | enum | omitted | Optional Factory custom-model reasoning selector value: `none`, `dynamic`, `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Requires `factory_reasoning: passthrough`. |
| `prompt_caching` | bool | `false` | Whether the model supports provider prompt-caching controls, such as `cache_control` or `prompt_cache_options`. |

A model is reported as `agent_ready: true` in `/v1/models` iff
`streaming && tools && tool_result_safe` are all true.

### Model slug and display name convention

Use the **exact model ID the upstream provider expects** as the slug (`alias` /
Factory `customModels[].model`) unless a documented local alias deliberately
maps to a different `upstream_model`. Put provider context in the display name
only:

- **Slug**: provider-native ID — `glm-5.2`, `deepseek-v4-flash`, `gpt-5.6`,
  Fireworks paths like `accounts/fireworks/models/deepseek-v4-pro`
- **Display name**: `{Readable model name} ({Provider label})` — e.g.
  `GLM 5.2 (Z.AI GLM Coding Plan)`, `DeepSeek V4 Flash (DeepSeek)`

Do not use `droid-` prefixes or `(via droid-proxy)` suffixes in documented
defaults. Set `upstream_model` to the same value as `alias` (or omit it).
The deliberate Codex OAuth exceptions are local mode/family aliases. In
particular, `gpt-5.6` and `gpt-5.6-fast` both map to `gpt-5.6-sol` because the
credential-validated private backend requires the explicit Sol ID; the fast
entry differs only by requesting `extra_args.service_tier: priority`. The
effective tier remains account/backend dependent and is reported in the
response.

#### Multi-provider example

The same logical model can be available through different providers. Slugs
usually differ because providers use different model ID strings:

```yaml
models:
  - alias: deepseek-v4-pro
    display_name: "DeepSeek V4 Pro (DeepSeek)"
    known_auth: deepseek
    upstream_model: deepseek-v4-pro

  - alias: accounts/fireworks/models/deepseek-v4-pro
    display_name: "DeepSeek V4 Pro (Fireworks)"
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/deepseek-v4-pro
```

Both entries can coexist on one proxy. Droid sends each slug unchanged; the
proxy routes by alias and forwards the matching `upstream_model`.

**Collision note:** If two providers used the identical model ID string, only
one `alias` could exist per config. In practice provider IDs usually differ
(especially Fireworks path-style IDs).

#### Migration from `droid-*` slugs

Older examples used proxy-local aliases such as `droid-deepseek-v4-flash`.
Update **both** `config.yaml` `alias` values and `~/.factory/settings.json`
`customModels[].model` to the provider-native IDs, then re-select the model in
Droid. Factory settings must use `displayName` and `maxOutputTokens` (not the
legacy `modelDisplayName` / `maxTokens` fields).

### OAuth model examples

```yaml
oauth:
  auth_dir: "~/.droid-proxy/auth"

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

  - alias: gpt-5.6-fast # local Factory alias, not an upstream model ID
    display_name: "GPT-5.6 Sol Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-sol
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

  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
    max_context_tokens: 256000
    capabilities:
      factory_reasoning: drop

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

  - alias: grok-4.3
    display_name: "Grok 4.3 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-4.3
    max_context_tokens: 1000000
    capabilities:
      factory_reasoning: passthrough
```

The Codex preset picker also offers standard/fast pairs for
`gpt-5.6-terra` and `gpt-5.6-luna`, all with 1,050,000 context and 128,000
output metadata. The public API documents `gpt-5.6` as the recommended alias
for `gpt-5.6-sol`, but the credential-validated private OAuth path requires the
explicit Sol ID. The preset therefore exposes local alias `gpt-5.6` while
setting `upstream_model: gpt-5.6-sol`; a duplicate explicit-Sol preset would be
misleading. On the public API, Pro is a reasoning mode
(`reasoning.mode: pro`), not a separate model ID. Credentialed private-OAuth
tests returned upstream 400 for that mode on the tested accounts; the proxy
preserves it and surfaces the error without downgrade. Credentialed
`effort: max` succeeds, while mode availability remains account/plan dependent.

For these Codex presets, `prompt_caching: true` reflects preserved
`prompt_cache_key` support. Public `prompt_cache_options` is stripped because
the private OAuth endpoint rejects it.

These IDs and capabilities are public
[OpenAI API model metadata](https://developers.openai.com/api/docs/models).
The explicit Sol mapping is credential-validated private-OAuth behavior;
availability on that backend still depends on the logged-in account, plan, and
workspace policy and should be validated with the credentialed live-E2E gate.
The proxy surfaces unavailable-model 4xx responses and never downgrades the
configured model.

## Environment variables

Anything in YAML can reference an env var with `${NAME}` or `${NAME:-default}`.
Common patterns:

```yaml
# Always pull the key from env
api_key_env: DEEPSEEK_API_KEY

# Inline default for a base URL with env override
base_url: "${MY_UPSTREAM_URL:-https://api.deepseek.com/v1}"

# Auth keys
client_auth:
  enabled: true
  api_keys:
    - "${DROID_PROXY_API_KEY}"
```

Empty env vars expand to empty strings. The config validator will then surface
a clear error at startup for fields that need a value.

## Validation

At startup, droid-proxy validates:

- Every model has a unique `alias`.
- Every model has `factory_provider` and `upstream_protocol` set and the combo
  is one of the allowed pairs above.
- Every non-OAuth model has either `base_url` or `known_auth`.
- OAuth models use `factory_provider: openai`, set the matching
  `oauth_provider`, and do not require `api_key_env`.
- When `client_auth.enabled: true`, at least one `api_keys` entry exists.
- `oauth.auth_dir` is not blank and OAuth callback ports are valid TCP ports.
- Duration and byte cap fields reject negative values; documented `0` / `0s`
  opt-outs are preserved instead of being replaced by defaults.

Invalid config produces a single error message listing every problem; nothing
is started until the file is healthy.
