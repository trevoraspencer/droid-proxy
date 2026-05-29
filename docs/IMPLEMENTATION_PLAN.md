# droid-proxy Implementation Plan

> **Historical/non-authoritative:** This was the initial implementation plan.
> Runtime behavior, provider status, Factory settings schema, and model examples
> in `README.md`, `docs/CONFIG.md`, `docs/PROVIDERS.md`,
> `docs/examples/`, `docs/factory-settings/`, and `config.example.yaml` are the
> authoritative user-facing references.

> **For agentic workers:** Use `superpowers:executing-plans` to execute task-by-task. Steps use `- [ ]` for tracking.

**Goal:** A Factory Droid-only downstream proxy in Go that lets Droid use BYOK / custom models from any upstream provider (Anthropic, OpenAI, DeepSeek, Kimi, xAI, ZAI, iFlow, Copilot-style, local Ollama/vLLM, etc.) via plain localhost endpoints.

**Architecture:** Single binary HTTP server (gin) that accepts the three Factory Droid endpoint protocols (`anthropic` / `openai` / `generic-chat-completion-api`) and forwards each request to a configured upstream, translating between protocols where needed. Models are declared in `config.yaml` with explicit Factory provider mode + upstream protocol so the proxy knows which translation path applies. DeepSeek reasoning replay is ported narrowly from the reference codebase.

**Tech Stack:** Go 1.26.3+, gin (router), logrus (logger), gopkg.in/yaml.v3 (config), tidwall/gjson + sjson (JSON shaping), tiktoken-go/tokenizer (token counting fallback), `net/http/httptest` (tests).

**Module path:** `droid-proxy` (local module name; no github reference). Renaming to a hosted path is a one-liner if/when the repo is published.

---

## Reference policy

`/Users/trevor/code/CLIProxyAPIPlus` is a **source donor only**. Rules:

- New repo: fresh git history, fresh module name, fresh package names, fresh docs/README, no LICENSE inheritance.
- **No public references** in code/docs/comments to the donor repo or its module path (`router-for-me/CLIProxyAPI`), branding, or ancestry.
- We do not import the donor module; we copy code we need and adapt it under our own package paths.
- We only adapt these narrow areas (explicit user constraint):
  - `internal/runtime/executor/deepseek_reasoning.go` → `internal/reasoning/cache.go`
  - `internal/runtime/executor/deepseek_stream.go` → `internal/reasoning/stream.go`
  - `internal/runtime/executor/openai_compat_executor.go` (request shape, header injection only) → `internal/upstream/client.go`
  - `sdk/api/handlers/openai_responses_stream_error.go` → `internal/translate/responses_error.go`
  - `sdk/api/handlers/header_filter.go` → `internal/upstream/headers.go`
  - `sdk/api/handlers/stream_forwarder.go` → `internal/stream/forward.go`
  - `sdk/api/handlers/openai/openai_responses_handlers.go` (SSE framing helpers only) → `internal/translate/responses_sse.go`
  - `sdk/api/handlers/claude/code_handlers.go` (handler shape only, no client-rotation/quota logic) → `internal/handlers/messages.go`
- Explicitly NOT ported: TUI, OAuth scheduler, registry, Amp/management UI, wsrelay, stores, full translator engine, usage dashboard, multi-credential pool, plug-in registry.

---

## Factory Droid contract (what we must serve)

Droid reads `~/.factory/settings.json` `customModels[]`. Each model entry declares a `provider` mode that determines which endpoint Droid will hit on the configured `base_url`:

| Factory `provider` | Droid hits | We expose | Translation matrix |
|---|---|---|---|
| `generic-chat-completion-api` | `POST /v1/chat/completions` | Chat Completions endpoint | Passthrough to OpenAI-compatible upstream; reject if upstream is Anthropic-native (unsupported combo). |
| `openai` | `POST /v1/responses` and `POST /v1/chat/completions` | Both | Passthrough Responses to OpenAI upstream; translate Chat ↔ Responses when serving Responses off a Chat-only upstream; passthrough Chat otherwise. |
| `anthropic` | `POST /v1/messages`, `POST /v1/messages/count_tokens` | Both | Passthrough to Anthropic upstream; translate Anthropic→Chat→Anthropic when serving from an OpenAI-compatible upstream. |

**Endpoint surface (final):**

| Method | Path | Purpose |
|---|---|---|
| GET | `/health`, `/healthz` | Liveness (no auth) |
| GET | `/v1/models`, `/models` | Model list (auth-gated; same content) |
| POST | `/v1/chat/completions`, `/chat/completions` | Chat Completions in/out |
| POST | `/v1/responses`, `/responses` | OpenAI Responses in/out |
| POST | `/v1/messages`, `/messages` | Anthropic messages in/out |
| POST | `/v1/messages/count_tokens`, `/messages/count_tokens` | Anthropic count_tokens |

`/v1` prefix is optional on every non-health route (Droid sometimes elides it).

---

## Capability tiers

We classify every configured model into one of four tiers. Tier appears in `/v1/models` metadata and in `docs/PROVIDERS.md`.

- **T1 — Direct reuse**: Upstream is the canonical provider and Droid's protocol matches it natively. Examples: OpenAI key → openai mode; Anthropic key → anthropic mode; DeepSeek key → generic-chat-completion-api. Streaming, tools, structured output, multimodal all work as-is.
- **T2 — OpenAI-compatible config**: Upstream exposes OpenAI Chat Completions; Droid uses generic-chat-completion-api or openai mode (we translate Chat→Responses for the Responses endpoint). Streaming + tools tested. Examples: local Ollama, vLLM, LiteLLM, Together, Fireworks, Groq.
- **T3 — Protocol translation**: We translate between protocols on the fly. Streaming + tools work but may have minor delta-event timing differences. Examples: Chat→Anthropic, Anthropic→Chat, Chat→Responses.
- **T4 — Best effort**: Chat-only support; tool calls / structured output / multimodal may not survive translation reliably. Examples: any provider exposed via Chat that does not implement OpenAI tool-calls correctly. We mark these `agent_ready: false`.

A model is `agent_ready: true` iff: streaming works AND tools survive a round-trip AND tool_result handling is correct AND we have a covering test.

---

## File structure

```
droid-proxy/
├── cmd/droid-proxy/
│   └── main.go                       # CLI: parse flags, load config, init logger, run server, signal handling
├── internal/
│   ├── config/
│   │   ├── config.go                 # Config + Model + ProviderMode + CapabilityOverrides types
│   │   ├── load.go                   # YAML + env expansion (${VAR}, ${VAR:-default})
│   │   ├── defaults.go               # default port, timeouts, capability inference
│   │   └── load_test.go
│   ├── version/version.go            # populated at link time
│   ├── logging/
│   │   ├── logger.go                 # logrus init from config
│   │   └── redact.go                 # secret-pattern redaction for logs
│   ├── server/
│   │   ├── server.go                 # gin engine, route registration, signal-aware Shutdown
│   │   ├── middleware.go             # auth, request-id, panic recovery
│   │   └── server_test.go
│   ├── handlers/
│   │   ├── base.go                   # shared response helpers, error JSON, alias resolution
│   │   ├── health.go                 # /health, /healthz
│   │   ├── models.go                 # /v1/models
│   │   ├── chat.go                   # /v1/chat/completions
│   │   ├── responses.go              # /v1/responses
│   │   ├── messages.go               # /v1/messages
│   │   ├── count_tokens.go           # /v1/messages/count_tokens
│   │   ├── chat_test.go
│   │   ├── responses_test.go
│   │   ├── messages_test.go
│   │   └── count_tokens_test.go
│   ├── upstream/
│   │   ├── client.go                 # HTTP client factory; per-request timeouts
│   │   ├── headers.go                # FilterUpstreamHeaders (ported from header_filter.go)
│   │   ├── route.go                  # alias→Model resolution
│   │   ├── auth.go                   # resolve API key from env / known auth
│   │   ├── send.go                   # do(req) with logging hooks + redacted trace
│   │   └── headers_test.go
│   ├── stream/
│   │   ├── forward.go                # SSE pump with keep-alive (ported from stream_forwarder.go)
│   │   ├── reader.go                 # SSE line scanner
│   │   └── forward_test.go
│   ├── translate/
│   │   ├── chat_to_responses.go      # Chat chunk → Responses typed SSE events
│   │   ├── chat_to_responses_test.go
│   │   ├── responses_to_chat.go      # Responses → Chat (request shaping for upstream)
│   │   ├── responses_to_chat_test.go
│   │   ├── chat_to_anthropic.go      # Chat ↔ Anthropic messages, with thinking when supported
│   │   ├── chat_to_anthropic_test.go
│   │   ├── anthropic_to_chat.go      # Anthropic /v1/messages → Chat (request shaping)
│   │   ├── anthropic_to_chat_test.go
│   │   ├── responses_error.go        # BuildOpenAIResponsesStreamErrorChunk (ported)
│   │   ├── responses_error_test.go
│   │   ├── responses_sse.go          # SSE framing helpers (ported)
│   │   └── responses_sse_test.go
│   ├── reasoning/
│   │   ├── cache.go                  # TTL+max-entries cache, key by provider/auth/model/session
│   │   ├── stream.go                 # SSE delta accumulator → Commit() persists reasoning
│   │   ├── patch.go                  # patch outgoing request with cached reasoning_content
│   │   ├── id.go                     # apiKeyHash helper, session derivation
│   │   ├── cache_test.go
│   │   ├── stream_test.go
│   │   └── patch_test.go
│   ├── tokens/
│   │   ├── count.go                  # local tokenizer fallback for count_tokens
│   │   └── count_test.go
│   └── ids/
│       └── ids.go                    # request-id, chunk-id helpers
├── docs/
│   ├── IMPLEMENTATION_PLAN.md        # this file
│   ├── PROVIDERS.md                  # provider matrix, tiers, knobs
│   ├── CONFIG.md                     # full config schema
│   ├── factory-settings/
│   │   ├── anthropic.json            # Droid settings.json snippet, anthropic mode
│   │   ├── openai.json               # Droid settings.json snippet, openai mode
│   │   └── generic.json              # generic-chat-completion-api snippet
│   └── examples/
│       ├── deepseek.md
│       ├── local-ollama.md
│       ├── local-vllm.md
│       ├── openai.md
│       └── anthropic.md
├── config.example.yaml
├── README.md
├── LICENSE                           # MIT, fresh, no inheritance
├── Makefile                          # build, test, lint, run targets
├── go.mod
└── go.sum
```

---

## Config schema (final)

```yaml
listen:
  host: 127.0.0.1                       # default localhost
  port: 8787                            # default

client_auth:
  enabled: false                        # when true, require auth header from Droid
  api_keys:                             # optional; if list non-empty, any one works
    - "${DROID_PROXY_API_KEY}"
  header: "Authorization"               # default
  scheme: "Bearer"                      # default; "" allows raw value

logging:
  level: info                           # trace/debug/info/warn/error
  format: text                          # text|json
  redact: true                          # redact secrets in logs/traces
  trace_requests: false                 # log request+response bodies (redacted) for debugging

reasoning_cache:
  enabled: true
  max_entries: 1024
  ttl: 30m

upstream:
  http_timeout: 600s                    # per-request, large for long completions
  stream_keep_alive: 15s                # SSE comment heartbeat interval

models:
  - alias: droid-deepseek-v3            # name Droid sends as `model`
    display_name: "DeepSeek V3"
    factory_provider: generic-chat-completion-api   # which Droid endpoint protocol
    upstream_protocol: openai-chat                  # how we talk to upstream
    base_url: "https://api.deepseek.com/v1"
    api_key_env: "DEEPSEEK_API_KEY"
    # OR: known_auth: deepseek
    max_output_tokens: 8192
    max_context_tokens: 64000
    extra_headers:
      "x-foo": "bar"
    extra_args:
      stream_options:
        include_usage: true
    capabilities:                       # all optional; sensible defaults inferred
      streaming: true
      tools: true
      tool_result_safe: true
      images: false
      json_mode: true
      structured_output: true
      reasoning: deepseek               # none|deepseek|anthropic-thinking
      prompt_caching: false
```

**Provider mode × upstream protocol matrix (validated at config load):**

| factory_provider | upstream_protocol values allowed |
|---|---|
| `generic-chat-completion-api` | `openai-chat` |
| `openai` | `openai-responses`, `openai-chat` (translated) |
| `anthropic` | `anthropic-messages`, `openai-chat` (translated) |

Invalid combinations fail loud at startup.

**Env-var expansion**: `${VAR}` and `${VAR:-default}` everywhere in string values.

---

## Phases & Tasks

### Phase 0: Plan + repo bootstrap

#### Task 0.1: Create plan, scaffold layout

**Files:**
- Create: `docs/IMPLEMENTATION_PLAN.md` (this file — already done)
- Create: `LICENSE`, `README.md` (stub), `.gitignore`, `Makefile`, `config.example.yaml` (stubs)

- [ ] Write this plan and commit it.
- [ ] Initial commit on `main`.

---

### Phase 1: Module + minimal server

#### Task 1.1: go.mod + main entrypoint

**Files:**
- Create: `go.mod`, `cmd/droid-proxy/main.go`, `internal/version/version.go`

- [ ] Run `go mod init droid-proxy` (or hand-write go.mod).
- [ ] Add deps: gin v1.10+, logrus, yaml.v3, gjson, sjson, tiktoken-go/tokenizer.
- [ ] Write `cmd/droid-proxy/main.go`:
  - parses `--config` flag (default `./config.yaml`).
  - loads config, initializes logger, builds server, runs with signal-aware graceful shutdown.
  - prints version + listen addr at startup.
- [ ] `go build ./...` succeeds.

#### Task 1.2: Config types + loader

**Files:**
- Create: `internal/config/config.go`, `internal/config/load.go`, `internal/config/defaults.go`, `internal/config/load_test.go`
- Create: `config.example.yaml` (real working example with one DeepSeek model)

- [ ] Define types: `Config`, `Listen`, `ClientAuth`, `Logging`, `ReasoningCache`, `Upstream`, `Model`, `Capabilities`, `FactoryProvider`, `UpstreamProtocol`.
- [ ] Constants for FactoryProvider/UpstreamProtocol with `String()` and `IsValid()`.
- [ ] `Load(path string) (*Config, error)` reads YAML, expands `${VAR}` and `${VAR:-default}`, validates the FactoryProvider×UpstreamProtocol matrix above, fills defaults, returns errors collated.
- [ ] Tests:
  - valid full config parses.
  - missing required fields error with clear messages.
  - invalid provider×protocol combinations are rejected at load.
  - `${ENV}` and `${ENV:-default}` expansion works on string fields.
  - duplicate aliases rejected.
- [ ] `go test ./internal/config/...` passes.

#### Task 1.3: Logger init + redaction

**Files:**
- Create: `internal/logging/logger.go`, `internal/logging/redact.go`, `internal/logging/redact_test.go`

- [ ] `New(cfg config.Logging) *logrus.Logger` builds a logger with level/format.
- [ ] `Redact(s string) string` masks: `Authorization: Bearer X`, `x-api-key: X`, `api_key=X`, `sk-...`, `${VAR}=X`. Test each pattern.
- [ ] `RedactBytes([]byte) []byte` for JSON bodies (best effort: regex over canonical strings).
- [ ] Tests: each pattern stays redacted; unrelated text unchanged.

#### Task 1.4: Gin server + health endpoints + middleware

**Files:**
- Create: `internal/server/server.go`, `internal/server/middleware.go`, `internal/server/server_test.go`
- Create: `internal/handlers/health.go`, `internal/handlers/base.go`

- [ ] `Server` struct holds config + logger + gin.Engine.
- [ ] `New(cfg, logger)` builds engine without gin defaults; registers logger middleware (request id, latency, status, redacted).
- [ ] Middleware: request-id (header `X-Request-ID` or generated), client_auth (if enabled), panic recovery → JSON 500.
- [ ] Routes: `GET /health`, `GET /healthz` → `{"status":"ok"}`. No auth on health.
- [ ] `Run(ctx) error` runs the HTTP server, returns when ctx is cancelled.
- [ ] Tests via httptest:
  - GET /health 200 ok.
  - With client_auth enabled, GET /v1/models without header → 401.
  - With client_auth enabled, GET /v1/models with valid key → 200 (route returns empty 200 for now).

---

### Phase 2: Models endpoint + alias resolution

#### Task 2.1: Alias resolver

**Files:**
- Create: `internal/upstream/route.go`, `internal/upstream/route_test.go`

- [ ] `type Router struct{ models map[string]*config.Model }`
- [ ] `NewRouter(models []*config.Model) *Router` indexes by alias; returns error on duplicates (already validated, but defense-in-depth).
- [ ] `(r *Router) Resolve(alias string) (*config.Model, error)` returns model or NotFound error.
- [ ] `(r *Router) List() []*config.Model` returns models in config order.
- [ ] Tests: lookup hit; lookup miss; case-sensitivity; list preserves order.

#### Task 2.2: /v1/models handler

**Files:**
- Create: `internal/handlers/models.go`, `internal/handlers/models_test.go`

- [ ] Returns OpenAI-style models list: `{"object":"list","data":[{"id":alias,"object":"model","owned_by":"droid-proxy","created":epoch}]}`.
- [ ] Each entry also includes `display_name`, `factory_provider`, `upstream_protocol`, `agent_ready` (bool) and `capabilities`.
- [ ] Registered on both `/v1/models` and `/models`.
- [ ] Tests:
  - Multiple models listed in config order.
  - Empty config returns empty array (not nil).
  - agent_ready=true requires streaming+tools+tool_result+capability override consistency.

---

### Phase 3: Header filter + upstream HTTP client + stream forwarder

#### Task 3.1: Header filter

**Files:**
- Create: `internal/upstream/headers.go`, `internal/upstream/headers_test.go`

- [ ] Port `FilterUpstreamHeaders` and `WriteUpstreamHeaders` semantics. Strip RFC 7230 hop-by-hop + Set-Cookie + Content-Length/Encoding + gateway-detector prefixes.
- [ ] Tests: each blocked header is removed; Connection: foo strips foo; canonical case unaffected.

#### Task 3.2: Upstream HTTP client

**Files:**
- Create: `internal/upstream/client.go`, `internal/upstream/auth.go`, `internal/upstream/send.go`

- [ ] `NewClient(timeout time.Duration) *http.Client` — single shared transport with sensible defaults (idle conn pool, no proxy unless env-set, follow redirects off for streaming).
- [ ] `ResolveAPIKey(m *config.Model) (string, error)` returns key from `api_key_env` or `known_auth` (env var lookup table for canonical providers — see PROVIDERS.md table).
- [ ] `Send(ctx, client, req)` wraps Do(); logs request line (redacted) at trace level.
- [ ] Inject `Authorization: Bearer <key>` (or `x-api-key` for Anthropic) and `extra_headers` from model config.

#### Task 3.3: SSE stream forwarder

**Files:**
- Create: `internal/stream/forward.go`, `internal/stream/reader.go`, `internal/stream/forward_test.go`

- [ ] Port `ForwardStream` from `stream_forwarder.go` minus BaseAPIHandler coupling. Signature: `Forward(ctx, writer, flusher, data <-chan []byte, errs <-chan error, opts Options)`.
- [ ] Options: KeepAliveInterval, WriteChunk, WriteTerminalError, WriteDone, WriteKeepAlive.
- [ ] `Reader` is a wrapper around bufio.Scanner with 50MB buffer + line callback.
- [ ] Tests:
  - data flows through; keep-alive ticks fire on idle.
  - context cancel mid-stream returns cleanly.
  - errs channel surfaces terminal error with WriteTerminalError called once.

---

### Phase 4: Chat Completions endpoint (the core path)

#### Task 4.1: Non-streaming chat completions

**Files:**
- Create: `internal/handlers/chat.go`, `internal/handlers/chat_test.go`

- [ ] Handler `ChatCompletions(c *gin.Context)`:
  - read raw body.
  - parse `model` field via gjson.
  - resolve via router.
  - validate factory_provider matches: must be `generic-chat-completion-api` or `openai`.
  - validate upstream_protocol: must be `openai-chat` (Phase 4 supports this only; Anthropic-via-Chat-translation is Phase 7 work).
  - rewrite `model` in payload to model's upstream model name if `upstream_model` is set, else keep alias.
  - apply `extra_args` JSON-deep-merge into payload via sjson.
  - check `stream` field; if false, forward non-stream; if true, forward stream.
  - call upstream `{base_url}/chat/completions`.
  - on success, copy filtered headers + body verbatim.
  - on error status, surface body as-is with same status.
- [ ] Tests with `httptest.NewServer`:
  - upstream returns OpenAI-format response → proxy returns identical body.
  - upstream returns 429 → proxy returns 429 with body.
  - missing model → 400 with OpenAI-format error.

#### Task 4.2: Streaming chat completions

**Files:**
- Modify: `internal/handlers/chat.go`
- Modify: `internal/handlers/chat_test.go`

- [ ] When `stream: true`:
  - Set Content-Type: text/event-stream, Cache-Control: no-cache, Connection: keep-alive.
  - Launch goroutine that reads from upstream SSE line-by-line via stream.Reader; forwards each `data:` chunk verbatim; emits keep-alive on idle.
  - Surface upstream errors as a `data: {"error":{...}}` chunk + close.
  - Handle client disconnect (ctx done) → cancel upstream req.
- [ ] Tests:
  - upstream emits N chunks ending with `data: [DONE]`; proxy forwards all chunks + DONE.
  - upstream closes mid-stream → proxy emits an error chunk.
  - client cancels mid-stream → upstream request is cancelled (verify via test hook).

#### Task 4.3: Tool calls + tool_result safety

**Files:**
- Modify: `internal/handlers/chat_test.go`

- [ ] Test: send a chat completion with `tools: [...]` and `tool_choice: "auto"`; httptest upstream returns a chunk with `tool_calls`; proxy forwards untouched.
- [ ] Test: subsequent request with `messages` including `{role:"tool", tool_call_id:..., content:...}` is forwarded as-is (no field reshaping that breaks OpenAI's tool result contract).
- [ ] Test: validate that the response chunks containing tool_call deltas survive a round-trip without re-ordering or field changes.

---

### Phase 5: OpenAI Responses endpoint

#### Task 5.1: Responses passthrough (openai-responses upstream)

**Files:**
- Create: `internal/handlers/responses.go`, `internal/handlers/responses_test.go`
- Create: `internal/translate/responses_error.go`, `internal/translate/responses_error_test.go`
- Create: `internal/translate/responses_sse.go`, `internal/translate/responses_sse_test.go`

- [ ] Port `BuildOpenAIResponsesStreamErrorChunk` → `responses_error.go`. Build with tests for each status code mapping.
- [ ] Port SSE framing helpers (`responsesSSEFrameLen`, `responsesSSEDataPayload`, etc.) → `responses_sse.go`. Tests confirm framing behavior on partial chunks.
- [ ] Handler `Responses(c)`:
  - factory_provider must be `openai`.
  - if upstream_protocol = `openai-responses`: stream forwards `data:` events; on each event, parse `data` payload to detect `response.output_item.done` and `response.completed` so we can repair empty `response.output` arrays (per the reference framer).
  - on error: emit `event: error\ndata: <BuildOpenAIResponsesStreamErrorChunk>\n\n` and close.
- [ ] Tests: passthrough of typed SSE events; final `response.completed` repaired when upstream omits `response.output`; error path emits typed error chunk.

#### Task 5.2: Chat→Responses translation (when upstream is chat-only)

**Files:**
- Create: `internal/translate/chat_to_responses.go`, `internal/translate/chat_to_responses_test.go`
- Create: `internal/translate/responses_to_chat.go`, `internal/translate/responses_to_chat_test.go`
- Modify: `internal/handlers/responses.go`

- [ ] `ResponsesRequestToChat([]byte) ([]byte, error)`:
  - extract `input` (array or string), `instructions`, `tools`, `tool_choice`, `temperature`, `max_output_tokens`, `metadata`.
  - flatten into OpenAI Chat `messages[]` (instructions → system message; input items → user/assistant/tool messages).
  - map `tools` (Responses format) → Chat `tools`.
  - return chat request body.
- [ ] `ChatStreamChunkToResponses(state *State, chatChunk []byte) ([][]byte, error)`:
  - parse OpenAI chat SSE chunk.
  - on first delta: emit `response.created`, `response.output_item.added` for message.
  - on text deltas: emit `response.output_text.delta`.
  - on tool_call deltas: emit `response.function_call.delta` events.
  - on `finish_reason`: emit `response.output_item.done` + `response.completed`.
  - state tracks per-stream output indexes and sequence numbers.
- [ ] `ChatNonStreamToResponses(chatBody []byte) []byte`: rebuild a complete Responses-style response with `id`, `output[]`, `usage`, `status: "completed"`.
- [ ] Handler glue: when upstream_protocol is `openai-chat`, use these translators.
- [ ] Tests:
  - text-only stream produces correct sequence of typed events.
  - tool-call stream produces function_call events.
  - non-stream chat → non-stream responses payload has output items + usage.

---

### Phase 6: Anthropic /v1/messages endpoint

#### Task 6.1: Anthropic-native passthrough

**Files:**
- Create: `internal/handlers/messages.go`, `internal/handlers/messages_test.go`

- [ ] Handler `Messages(c)`:
  - factory_provider must be `anthropic`.
  - if upstream_protocol = `anthropic-messages`: forward POST `{base_url}/v1/messages` with `x-api-key`, `anthropic-version`, `anthropic-beta` headers from request + model.extra_headers.
  - Streaming: SSE forward with typed events (`message_start`, `content_block_start/delta/stop`, `message_delta`, `message_stop`).
  - Non-streaming: read full body, gzip-decompress if response is gzipped without Content-Encoding, return.
- [ ] Tests:
  - native passthrough non-stream: byte-equivalent body returned.
  - streaming: each event copied verbatim.
  - gzipped body without Content-Encoding: decompressed correctly.

#### Task 6.2: count_tokens

**Files:**
- Create: `internal/handlers/count_tokens.go`, `internal/handlers/count_tokens_test.go`
- Create: `internal/tokens/count.go`, `internal/tokens/count_test.go`

- [ ] If upstream is `anthropic-messages`: forward to `{base_url}/v1/messages/count_tokens`.
- [ ] If upstream is OpenAI-compatible: locally count via tiktoken-go (cl100k_base fallback), return `{"input_tokens": N}`.
- [ ] Tests:
  - anthropic upstream: count is upstream-reported.
  - openai upstream: local count is non-zero and stable across runs.

#### Task 6.3: Anthropic↔Chat translation (T3)

**Files:**
- Create: `internal/translate/anthropic_to_chat.go`, `internal/translate/anthropic_to_chat_test.go`
- Create: `internal/translate/chat_to_anthropic.go`, `internal/translate/chat_to_anthropic_test.go`
- Modify: `internal/handlers/messages.go`

- [ ] `AnthropicRequestToChat([]byte) ([]byte, error)`:
  - flatten Anthropic `messages[]` (with content blocks) to Chat `messages[]`.
  - map `system` field to system message.
  - map Anthropic `tools[]` → OpenAI `tools[]` (schema reshape).
  - map `tool_use` blocks → assistant `tool_calls`; `tool_result` blocks → tool messages.
- [ ] `ChatStreamChunkToAnthropic(state, chatChunk) ([][]byte, error)`:
  - emit `message_start` on first chunk.
  - emit `content_block_start` + `content_block_delta` for text/tool_use.
  - emit `message_delta` + `message_stop` at end.
- [ ] `ChatNonStreamToAnthropic(chatBody) []byte`: assemble Anthropic message response.
- [ ] Handler glue: when upstream_protocol is `openai-chat`, use translators.
- [ ] Tests covering text, tool_use→tool_result round-trip, system message preservation.

---

### Phase 7: DeepSeek reasoning replay

#### Task 7.1: Cache + key shape

**Files:**
- Create: `internal/reasoning/cache.go`, `internal/reasoning/cache_test.go`, `internal/reasoning/id.go`

- [ ] Port `deepSeekReasoningCache` semantics: `Store(key, reasoning)`, `Lookup(key) (string, bool)`, `Len()`, eviction by TTL + max-entries.
- [ ] Key fields: `Provider, AuthHash, Model, BaseURL, Session, ThinkingSettings, ToolCallIDs, TurnHash`.
- [ ] `APIKeyHash(key string) string` = first 16 hex chars of SHA256.
- [ ] Tests: store+lookup; TTL expiry; max-entries eviction picks oldest; empty fields make key invalid.

#### Task 7.2: Stream capture

**Files:**
- Create: `internal/reasoning/stream.go`, `internal/reasoning/stream_test.go`

- [ ] Port `deepSeekStreamCapture` semantics: ObserveLine; Commit; per-choice state with reasoning/content/tool_calls accumulators.
- [ ] Tests:
  - reasoning + tool_call stream → on Commit, cache holds reasoning under correct key.
  - missing tool_call id → not stored.
  - error chunk → failed; nothing stored.

#### Task 7.3: Patch outgoing request

**Files:**
- Create: `internal/reasoning/patch.go`, `internal/reasoning/patch_test.go`

- [ ] `PatchRequest(payload []byte, scope Scope, cache *Cache) []byte`:
  - parse JSON; iterate assistant messages with tool_calls but no reasoning_content; lookup; insert from cache.
- [ ] `CaptureNonStream(body []byte, scope, cache)` for non-streaming responses.
- [ ] Tests:
  - tool_calls without reasoning_content gets backfilled.
  - already-present reasoning_content is preserved.
  - capture from non-stream body matches stream-captured shape.

#### Task 7.4: Integration into chat handler

**Files:**
- Modify: `internal/handlers/chat.go`
- Modify: `internal/handlers/chat_test.go`

- [ ] When model has `capabilities.reasoning: deepseek`, instantiate reasoning cache scope from request, patch outgoing payload, install stream capture on the SSE pump.
- [ ] Tests:
  - first request returns reasoning + tool_calls → captured.
  - second request with tool_results back → outgoing payload has `reasoning_content` injected on the prior assistant message.

---

### Phase 8: Provider-specific known-auth + capability inference

#### Task 8.1: Known-auth table

**Files:**
- Modify: `internal/upstream/auth.go`
- Create: `docs/PROVIDERS.md`

- [ ] `KnownAuth` table maps a short string (e.g. `deepseek`, `anthropic`, `openai`, `xai`, `kimi`, `zai`, `iflow`, `groq`, `fireworks`, `together`, `ollama`, `openrouter-ignored`) to:
  - default base_url (when not overridden in config).
  - env var name for API key (`DEEPSEEK_API_KEY`, `ANTHROPIC_API_KEY`, etc.).
  - default capabilities (tools/streaming/reasoning).
  - default upstream_protocol.
- [ ] Excluded by goal directive: openrouter (do not register).
- [ ] At config load, if `known_auth: xxx` is set, hydrate empty fields from this table. Explicit fields always win.
- [ ] Tests: hydration leaves user-set fields alone; unknown known_auth value fails loud.

#### Task 8.2: PROVIDERS.md

- [ ] One section per supported provider. Columns: tier, default base URL, env var, factory_provider modes, capabilities, known limitations.
- [ ] Honest tier classification:
  - **T1**: openai, anthropic, deepseek, xai, kimi.
  - **T2**: ollama, vllm, groq, fireworks, together, iflow, zai, copilot-like OpenAI-compat.
  - **T3**: any model serving anthropic mode off openai-chat upstream; openai responses off openai-chat upstream.
  - **T4**: providers with broken tool-call semantics in chat completions (call out by name with reproducible test case).

---

### Phase 9: Tests at the seams (cross-cutting)

#### Task 9.1: End-to-end factory examples

**Files:**
- Create: `internal/handlers/factory_examples_test.go`

- [ ] Build httptest upstreams for each: OpenAI Chat (deepseek/ollama style), OpenAI Responses, Anthropic.
- [ ] Construct three config setups corresponding to the three Droid provider modes.
- [ ] Drive each through every endpoint that mode supports; assert wire format matches what Droid expects:
  - generic-chat-completion-api: chat/completions stream + nonstream with tool_calls.
  - openai: /v1/responses typed events + chat/completions.
  - anthropic: /v1/messages stream events + count_tokens.

#### Task 9.2: Disconnect + redaction tests

- [ ] Client disconnect mid-stream: upstream req cancellation propagates within 1s.
- [ ] Trace logs with `trace_requests: true` show `Authorization: Bearer ***` not the actual key. Search log output for the literal key — must be absent.

---

### Phase 10: Docs + examples

#### Task 10.1: README

**Files:**
- Create: `README.md` (final)

- [ ] Sections: what it is, install, quickstart (single command with DeepSeek), Factory settings.json snippets per mode, supported providers table linking to PROVIDERS.md, troubleshooting (common errors), license.
- [ ] All examples use `http://127.0.0.1:8787` or `http://localhost:8787`. No mention of tunneling/cloudflare/ngrok — leave that to user discretion if remote access desired.

#### Task 10.2: docs/factory-settings + docs/examples

- [ ] `docs/factory-settings/anthropic.json`: BYOK snippet using anthropic mode pointing at proxy.
- [ ] `docs/factory-settings/openai.json`: BYOK snippet using openai mode.
- [ ] `docs/factory-settings/generic.json`: BYOK snippet using generic-chat-completion-api mode.
- [ ] `docs/examples/deepseek.md`, `local-ollama.md`, `local-vllm.md`, `openai.md`, `anthropic.md` — each has: config.yaml stanza, settings.json stanza, sample curl.

#### Task 10.3: docs/CONFIG.md

- [ ] Full schema reference. Every key documented with type, default, description, examples.

---

### Phase 11: Release verification

#### Task 11.1: Build + test + vet

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports no files.

#### Task 11.2: Manual smoke test instructions

**Files:**
- Create: `docs/SMOKE.md`

- [ ] Document the exact steps a user runs to confirm Droid+proxy works with one provider end to end. Include `curl` calls against each of the 3 endpoint surfaces against a running proxy with a deepseek model configured.

#### Task 11.3: License + scrub

- [ ] Write fresh MIT `LICENSE`. Author: Trevor Spencer.
- [ ] Grep entire repo for `router-for-me`, `CLIProxyAPI`, `cli-proxy-api`, `cliproxy`, and any donor-specific package names. **Zero matches required** outside `docs/IMPLEMENTATION_PLAN.md` (which references the donor by path for traceability) and outside ad-hoc local notes.
- [ ] Grep for personal info / tokens / credentials in committed files: none allowed.

---

## Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Responses ↔ Chat translation drift over time as OpenAI changes Responses spec | High | Medium | Encapsulate translation in `internal/translate/` with thorough tests; pin to spec snapshot in a comment with date. |
| Anthropic streaming events have many edge cases (thinking, tool_use, ping events, message_delta with stop_reason) | High | Medium | Native passthrough is byte-for-byte; only the Chat→Anthropic translator path is at risk and only when user opts into T3. Cover top 10 event sequences with table-driven tests. |
| DeepSeek reasoning cache key shape needs to survive across server restarts | Low | Low | In-memory only by design (matches reference); user accepts cold start. Documented in CONFIG.md. |
| Droid sends `model` field shape we don't expect (e.g., `models/foo`, `anthropic/bar`) | Medium | High | Resolver strips known prefixes; failing aliases produce explicit error listing valid aliases. |
| gin's default behaviour conflicts (auto JSON-encoding when we want raw streaming) | Medium | Medium | Use `c.Writer.Write(...)` directly for SSE; do not call `c.JSON` for streaming paths. |
| Donor-code lineage shows in copied files (license header, package names, function names) | High | High | Each ported file gets rewritten under our package with new function names; remove any reference comments; run grep at release. |
| Tool-call passthrough silently corrupts arguments | Medium | High | Tests assert byte-for-byte arguments on both stream and non-stream paths. |
| Concurrent requests share cache state incorrectly | Low | High | All caches use sync.RWMutex; tests run with `-race`. |
| 5xx upstream during stream → client hangs | Medium | Medium | Stream forwarder always writes a terminal error chunk + closes; tested. |
| Slow upstream → client perceives proxy as dead | Medium | Low | Keep-alive SSE comments at configurable interval; default 15s. |

---

## Test strategy

- **Unit tests** for: config load/validate, header filter, redaction, SSE reader, cache, stream capture, reasoning patch, each translator function, error chunk builder, alias resolver.
- **Integration tests** with `httptest.NewServer` for: each handler against a fake upstream that emits the right protocol; full request → upstream → response cycle.
- **Race detector**: `go test -race ./...` in CI script / Makefile target.
- **No mocks of stdlib**: real `http.Client`, `httptest.Server`. We mock the upstream only.
- **Test data fixtures** under `testdata/` per package: real DeepSeek SSE captures (sanitized), Anthropic event sequences, Responses typed events.

---

## Release criteria

- [ ] `go build ./...` and `go test ./...` and `go vet ./...` all pass.
- [ ] `go test -race ./...` passes.
- [ ] README has copy-paste working examples for at least: DeepSeek, local Ollama, local vLLM, OpenAI, Anthropic, plus a reference-supported provider (e.g. xAI or Kimi).
- [ ] PROVIDERS.md has the tier table populated and honest.
- [ ] Each model marked `agent_ready: true` has a passing test for: streaming, tool_call round-trip, tool_result handling.
- [ ] No literal references to donor repo in committed source files.
- [ ] `trace_requests: true` does not log any literal API key value — verified by test.
- [ ] Droid (manual): with proxy running and `~/.factory/settings.json` configured for at least one model in each provider mode, basic chat and tool-using chat both work end to end. (Documented in `docs/SMOKE.md`; user runs it.)

---

## Out of scope (will not implement)

- OpenRouter integration (per goal).
- Provider invention (only providers proven in the reference donor + explicitly named).
- Management UI / dashboard / TUI / OAuth scheduler.
- Persistent state stores (Redis/Postgres/S3).
- Multi-credential rotation / quota management.
- Cloudflare/ngrok tunneling — left to user.
- Cross-provider model fan-out / fallback chains.
- Image generation, video generation (not part of Droid's BYOK contract).

---

## Open questions (none blocking)

These can be deferred and decided during implementation without blocking progress:
- Default port: 8787 — happy to change if user has preference.
- Module path: `droid-proxy` (local) — can be renamed to a hosted path later.
- License: MIT — user can override before first push.

If any of these become blockers (e.g., conflicting port), I will ask. Otherwise the chosen defaults stand.
