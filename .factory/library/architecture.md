# Architecture

Architectural decisions, package boundaries, and patterns discovered during planning.

---

- `cmd/droid-proxy/main.go` loads YAML config, initializes logging, builds `server.Server`, and handles graceful shutdown.
- `internal/config` owns config loading, env expansion, defaults, provider/protocol validation, and known-auth hydration.
- `internal/server` wires Gin routes, request IDs, recovery, access logs, client auth, and HTTP server lifecycle.
- `internal/handlers` owns public API surfaces: health, models, chat completions, responses, messages, and count_tokens.
- `internal/upstream` owns model routing, upstream request construction, auth headers, and response header filtering.
- `internal/stream` owns SSE line forwarding/keepalive behavior; mission work must add protocol terminal/error awareness without regressing passthrough.
- `internal/reasoning` owns DeepSeek-style `reasoning_content` capture/replay; session scoping and abnormal-stream commits are mission-critical.
- `internal/translate` currently contains Responses error helpers and is the natural location for protocol translation helpers.
