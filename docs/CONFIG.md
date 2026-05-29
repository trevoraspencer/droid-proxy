# Configuration reference

`droid-proxy` reads a YAML file (default `./config.yaml`, override with
`--config`). Every string value supports `${VAR}` and `${VAR:-default}` env
expansion — secrets stay in the environment, not in the file.

A working example lives at [`config.example.yaml`](../config.example.yaml).

## Top-level structure

```yaml
listen: {...}            # bind address
server: {...}            # inbound body cap and HTTP server timeouts
client_auth: {...}       # require an api key from Droid (optional)
logging: {...}           # log level, format, secret redaction
reasoning_cache: {...}   # DeepSeek-style reasoning replay
upstream: {...}          # http client timeouts and response caps
models:                  # required, non-empty
  - alias: ...
  - alias: ...
```

## `listen`

| key | type | default | description |
|---|---|---|---|
| `host` | string | `127.0.0.1` | Bind host. Use `0.0.0.0` to expose beyond localhost. |
| `port` | int | `8787` | TCP port. |

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
it automatically for models with `capabilities.reasoning: deepseek`.

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

## `models[]` (required)

Each model entry maps a public alias (what Droid sends as `model`) to a
specific upstream configuration.

| key | type | required | description |
|---|---|---|---|
| `alias` | string | ✅ | Identifier Droid sends as `model`. |
| `display_name` | string |  | Human-readable name surfaced in `/v1/models`. |
| `factory_provider` | enum | ✅ | One of `anthropic`, `openai`, `generic-chat-completion-api`. Picks which Droid endpoint protocol the proxy serves. |
| `upstream_protocol` | enum | ✅ | One of `anthropic-messages`, `openai-responses`, `openai-chat`. Picks how the proxy talks to the real provider. See the matrix in [PROVIDERS.md](PROVIDERS.md). |
| `base_url` | string | one of `base_url` or `known_auth` required | Upstream root URL. e.g. `https://api.deepseek.com/v1`. |
| `known_auth` | string |  | Shortcut: looks up base_url, env var, auth header, version headers from a built-in registry. See PROVIDERS.md. |
| `upstream_model` | string |  | Forwarded `model` field on the upstream call. If unset, the alias itself is sent. |
| `api_key_env` | string |  | Env var holding the API key. If unset, the env var declared by `known_auth` is used. |
| `max_output_tokens` | int |  | Informational; surfaced in `/v1/models`. |
| `max_context_tokens` | int |  | Informational; surfaced in `/v1/models`. |
| `extra_headers` | map[string]string |  | Headers appended to every upstream request for this model. |
| `extra_args` | map[string]any |  | Top-level fields merged into every outgoing request body (e.g. `temperature`, `stream_options`). |
| `capabilities` | object |  | Capability overrides. See below. |

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
| `prompt_caching` | bool | `false` | Whether the model supports cache_control breakpoints. |

A model is reported as `agent_ready: true` in `/v1/models` iff
`streaming && tools && tool_result_safe` are all true.

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
- Every model has either `base_url` or `known_auth`.
- When `client_auth.enabled: true`, at least one `api_keys` entry exists.
- Duration and byte cap fields reject negative values; documented `0` / `0s`
  opt-outs are preserved instead of being replaced by defaults.

Invalid config produces a single error message listing every problem; nothing
is started until the file is healthy.
