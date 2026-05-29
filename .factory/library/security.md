# Security and Reliability

Security, logging, timeout, body-limit, and stream-hardening guidance.

---

- Non-loopback or wildcard bind with `client_auth.enabled: false` must hard-fail before binding.
- Client auth rejection should happen before body reading, parsing, translation, or upstream forwarding.
- Request body reads, upstream non-stream reads, upstream error reads, gzip decompression, SSE line size, trace log payloads, and runtime smoke all need finite bounds.
- Trace logging must be disabled by default. When enabled with redaction, no literal sentinel secrets may appear in URLs/query parameters, headers, bodies, errors, panic logs, tool schemas, tool arguments, or tool results. Credential-bearing query names include common forms such as `token`, `access_token`, `refresh_token`, `id_token`, `auth`, `authorization`, `key`, `api_key`, `apiKey`, `credential`, `secret`, and `password`.
- Never forward downstream client credentials upstream. Upstream receives only configured provider credentials and allowed protocol headers.
- Provider credentials must use provider-correct outbound headers: OpenAI-compatible known-auth providers use `Authorization: Bearer <key>`, Anthropic uses raw `x-api-key: <key>` with required Anthropic headers, and local no-auth providers such as Ollama/vLLM omit auth only for loopback/local upstream configurations.
- Reserved/hop-by-hop/security-sensitive headers must not be overridden by model `extra_headers` or propagated request headers.

## Config vs Runtime Enforcement Boundary

Config-compatibility owns schema, documented defaults, validation, and presence-aware opt-out semantics for caps and timeouts. Security-hardening owns runtime enforcement of request body limits, upstream response caps, timeout behavior, bounded errors, and bounded/redacted logging. Security features should consume the config knobs introduced by config-compatibility and avoid creating a parallel incompatible config surface.
