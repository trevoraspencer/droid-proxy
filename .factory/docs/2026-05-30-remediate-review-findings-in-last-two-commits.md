Remediate the issues found in `d039bb3` (config TUI) and `7bf5b03` (xAI OAuth). Full scope: M-items + L-items + T1 tests.

## M1 — Refactor xAI request rewrite to sjson (`internal/handlers/oauth_responses.go`)
Replace `prepareXAIResponsesPayload`'s full `map[string]any` round-trip (which coerces all numbers to float64 and reorders keys) with surgical edits, matching the Codex path:
- `sjson.DeleteBytes(out, "service_tier")`
- set `prompt_cache_key` via `sjson.SetBytes` only when currently empty, using the session id from headers
- decode **only** the `tools` array with `json.Decoder` + `UseNumber()` (preserves numeric precision in tool schemas), run the existing `normalizeXAITools`, then write back with `sjson.SetRawBytes(out, "tools", ...)`
- update `include` via gjson read + `sjson.SetBytes`

Adapt helpers: `xaiSessionID(h http.Header)` — drop the dead `prompt_cache_key` fallback and the redundant `session_id`/`Session_id` duplicate lookup (**fixes L1**); add `xaiReasoningPresentBytes(body)` and `setXAIInclude(body)`. Keep `normalizeXAITools`, `sanitizeXAIToolSchema`, `includeXAIEncryptedReasoning`, `xaiValueContainsReasoning` unchanged. Existing tests (`...XAISanitizesPayloadForAgentCompatibility`, `...XAIStreaming...`) validate behavior.

## M2 — Propagate Upsert errors (`internal/tui/backend.go`)
In `syncFactory`, check and return the error from `settings.Upsert(...)` instead of discarding it.

## M3 — Single rolling Factory backup (`internal/factory/settings.go`)
Change `Save(backup)` to write/overwrite a single `settings.json.bak` instead of a new timestamped `.bak-<ts>` per call (prevents unbounded backup accumulation). Add a unit test asserting one stable backup file.

## M4 — Symmetric env value quoting (`internal/daemon/envfile.go` + `internal/secrets/secrets.go`)
Add `daemon.ParseEnvValue` that uses `strconv.Unquote` for double-quoted values with a safe fallback to the current trim behavior (backward compatible with hand-written `.env.local`). Use it in both `daemon.LoadEnvFile` and `secrets.readFile` so values written with `%q` round-trip correctly. Add a secrets round-trip test for a value containing a quote.

## L2 — `removeModel` surfaces Factory errors (`internal/tui/backend.go`)
Return `factory.Load`/`Save` errors instead of silently returning `nil`.

## L3 — Remove dead code (`internal/configedit/configedit.go`)
Drop the redundant `caps := m.Capabilities; clone.Capabilities = caps` in `validateModel` (already copied by `clone := *m`).

## L4 — `secretsPathHint` uses real path (`internal/tui/tui.go`)
Return `secrets.Path()` instead of the hardcoded `~/.droid-proxy/env` string.

## T1 — Add tui unit tests (`internal/tui/tui_test.go`)
Cover the pure helpers: `defaultAlias`, `defaultDisplay`, `isLoopbackBaseURL`, `factoryProviderFor`, `upstreamForOAuth`, `proxyBaseURL` (temp config), and `buildModelFromForm` (custom / oauth / known-auth paths plus validation: remote-without-key-env rejected, loopback allowed, bad max_output_tokens rejected).

## Not changing
- **N1** (`oauthResponsesTerminal` now also applies `data.type` terminal detection to Codex): left as an intentional generalization; documented only.

## Verification
Run `go build ./...`, `go vet ./...`, and `go test ./...`. The `server` runtime-smoke test needs port `8787` free — I'll confirm green with the daemon stopped (it currently fails only because your daemon holds the port).