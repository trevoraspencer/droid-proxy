# Droid Proxy Deep Audit Findings

Audit date: 2026-06-16  
Baseline: `799cdb88274d2085343ea03ab8c178ea49627322`  
Scope: current working tree at Phase 1, no source changes.

## Summary

Severity histogram:

| Severity | Count |
|---|---:|
| P0 | 0 |
| P1 | 6 |
| P2 | 6 |
| P3 | 1 |

Area counts:

| Area | Count |
|---|---:|
| correctness | 4 |
| concurrency | 1 |
| security | 3 |
| simplification | 3 |
| tests | 2 |
| perf | 1 |
| docs | 1 |

The roadmap said 23 packages; the authoritative current command `go list ./...` reports 22 Go packages. The checklist below has 23 rows: the 22 current packages plus the package-count discrepancy row, so the mismatch is explicit rather than hidden.

## Packages Triaged

- [x] `cmd/droid-proxy`
- [x] `internal/config`
- [x] `internal/configedit`
- [x] `internal/daemon`
- [x] `internal/factory`
- [x] `internal/handlers`
- [x] `internal/livee2e`
- [x] `internal/logging`
- [x] `internal/oauth`
- [x] `internal/providerapi`
- [x] `internal/reasoning`
- [x] `internal/secrets`
- [x] `internal/security`
- [x] `internal/server`
- [x] `internal/stream`
- [x] `internal/testutil`
- [x] `internal/tokens`
- [x] `internal/translate`
- [x] `internal/tui`
- [x] `internal/update`
- [x] `internal/upstream`
- [x] `internal/version`
- [x] `roadmap package-count discrepancy` - `go list ./...` shows 22 packages, not 23.

## Depth Evidence

High-risk packages and functions/line ranges examined:

- `internal/oauth`: `AccountPool` state and mutex surface (`pool.go:86-97`), reload preservation (`pool.go:237-291`), lease accounting (`pool.go:383-410`), eligibility/select/sticky paths (`pool.go:490-622`), snapshot exposure (`pool.go:659-760`), quota parsing/merge/reset (`quota.go:12-303`), refresh lock (`refresh_lock.go:52-93`), token save/load (`store.go:84-202`), token exchange/refresh/JWT parsing (`provider.go:77-203`, `provider.go:243-427`), watcher add/remove/reload callbacks (`watcher.go:98`, `watcher.go:180`, `watcher.go:229`, `watcher.go:285`), OAuth login/device flows (`login.go:68-140`, `device.go:105-143`).
- `internal/handlers`: OAuth responses single-token path (`oauth_responses.go:18-126`), Codex failover (`oauth_responses.go:163-434`), force-refresh replay (`oauth_responses.go:444-532`), stream lease release (`oauth_responses.go:534-578`), SSE repair and completed-output patching (`oauth_sse.go:21-418`), payload/header shaping (`oauth_payload.go:19-377`, `oauth_headers.go:25-200`), native chat/messages/responses/count/models handlers (`chat.go:1-72`, `messages.go:1-250`, `responses.go:1-179`, `count_tokens.go`, `models.go`, `pool_health.go`), raw forwarding helpers (`base.go:45-143`, `forwarding.go:69-120`).
- `internal/translate`: Anthropic request/response mapping (`anthropic_chat.go:13-447`), Responses request/response mapping (`responses_chat.go:13-372`), incremental Chat stream translation state machines (`chat_stream.go:54-520`), Responses error shape (`responses_error.go:13-92`), common scalar helpers (`common.go`).
- `internal/server`: request IDs/access/trace/recovery/auth/body-limit middleware (`middleware.go:24-237`), engine/start/shutdown/watcher wiring (`server.go:1-168`), workflow/runtime tests as release guards (`workflow_validation_test.go`, `workflow_runtime_smoke_test.go`).
- `internal/config`: parse/default/hydrate/validate pipeline (`load.go:24-49`, `load.go:110-231`), model matrix and URL validation (`config.go:313-427`), known-auth registry (`known_auth.go:33-173`), docs drift tests (`docs_test.go`).
- `internal/security`: release/legal/public-security tests (`public_release_test.go`, `legal_release_test.go`, `docs_release_test.go`). This package is test-only in current tree.

Lower-traffic packages were swept for correctness, resource handling, and simplification: `internal/tui/backend.go:34-263`, `internal/daemon/*.go`, `internal/update/update.go:72-288`, `internal/configedit/configedit.go:1-339`, `internal/factory/settings.go:1-249`, `internal/providerapi/list.go:21-139`, `internal/reasoning/cache.go:46-328`, `internal/reasoning/session.go:18-42`, `internal/tokens/count.go:20-55`, `internal/upstream/send.go:39-161`, `internal/upstream/headers.go:8-137`, `internal/version/version.go`, and `internal/testutil/oauthhealth.go`.

## Findings

| ID | Severity | Area | Owning phase | File:line | Evidence | Proposed fix | Behavioral change |
|---|---|---|---:|---|---|---|---|
| F-001 | P1 | concurrency | 3 | `internal/handlers/oauth_responses.go:321-328` | In the Codex 401/403 force-refresh path, a `ForceRefresh` error immediately marks the account unhealthy. The earlier `RefreshIfNeeded` path checks `c.Request.Context().Err()` before marking unhealthy (`oauth_responses.go:214-220`), but the force-refresh path does not. A downstream cancellation during forced refresh can therefore poison account health/cooldown even though upstream auth was not proven bad. | Add the same context-cancellation guard before `MarkUnhealthy`, and pin it with a regression test that cancels during forced refresh. | Yes - runtime pool-health behavior only. |
| F-002 | P1 | correctness | 3 | `internal/stream/forward.go:187-201`, `internal/stream/forward.go:222-228` | If upstream EOF arrives with a pending SSE event that was not terminated by a blank line, `Forward` updates only the internal `Event` state (`flushEvent`) and returns; it does not write the missing blank line. Downstream clients may never see a complete final event even when the pending event contains a terminal marker. Existing tests cover normal blank-line terminal frames and truncation, but not terminal EOF without the final blank line. | On EOF with a pending event that is terminal, emit/flush a frame boundary before returning; add unit tests for pending terminal and pending non-terminal EOF. | Yes - stream framing becomes more tolerant. |
| F-003 | P1 | correctness | 4 | `internal/translate/anthropic_chat.go:354-367`, `internal/translate/responses_chat.go:268-288` | Non-streaming Chat responses with both assistant text and `tool_calls` drop the text during translation. Both translators branch on `tool_calls` and skip `message["content"]`. Streaming translators already support text followed by tool calls (`chat_stream.go:110-145`, `chat_stream.go:231-266`), so non-streaming behavior is inconsistent and loses assistant-visible text. | Preserve assistant text as a text block/output item before function/tool blocks; add golden tests for content+tool_calls in Chat -> Anthropic and Chat -> Responses. | Yes - fixes previously dropped output. |
| F-004 | P1 | security | 5 | `internal/logging/redact.go:10-19` | Redaction covers bearer headers, `api_key`/`apiKey` JSON fields, query credential parameters, and `sk-...` token shapes. It does not generally redact JSON fields such as `access_token`, `refresh_token`, `id_token`, `token`, `secret`, or `authorization` unless the value happens to match the loose `sk-` pattern. Current sentinel tests use `sk-`-prefixed OAuth values, so generic OAuth token shapes are not pinned. | Extend JSON-field redaction for common OAuth credential names and add non-`sk-` test vectors. | No intended user-visible behavior; logs become safer. |
| F-005 | P1 | security | 5 | `internal/oauth/store.go:146`, `internal/oauth/store.go:160`, `internal/oauth/installation.go:19`, `internal/oauth/installation.go:35`, `internal/oauth/refresh_lock.go:61` | OAuth auth dirs, token files, installation IDs, and refresh lock dirs call `os.Chmod` as best-effort and ignore errors. Atomic writes set temp-file mode, but if a platform/filesystem rejects chmod or an existing directory remains over-permissive, the security boundary silently degrades. | Return chmod errors for security-sensitive dirs/files where supported; add tests asserting modes and failure propagation with injected filesystem hooks or helper functions. | Yes - setup/save can now fail loudly on permission hardening failure. |
| F-006 | P1 | security | 2 | `internal/oauth/provider.go:280`, `internal/oauth/provider.go:316`, `internal/oauth/provider.go:367-402` | The recon seed is present: `parseJWTIdentity` parsing failures are swallowed by design. Malformed ID tokens are indistinguishable from absent identity and can fall through to legacy selectors/filenames. Existing auth-isolation tests cover many selector paths, but not an explicit malformed-ID-token decision. | Make parse errors explicit enough to test: either propagate for exchange/refresh, or keep best-effort behavior with a documented reason and tests proving malformed ID tokens cannot mis-attribute accounts. | Possibly - depends on chosen strictness. |
| F-007 | P2 | simplification | 6 | `internal/handlers/oauth_responses.go:72-77`, `internal/handlers/oauth_responses.go:242-249`, `internal/handlers/oauth_responses.go:468-475` | The recon seed is present: three equivalent `if downstreamStream { a.Client.Do(req) } else { a.Client.HTTP.Do(req) }` blocks plus repeated build/apply/execute status handling. This is error-prone in the highest-risk handler path. | Extract a small helper for OAuth Responses request execution, then use it in single-token, failover, and auth replay paths. | No. |
| F-008 | P2 | simplification | 6 | `internal/server/middleware.go:236-237` | The recon seed is present: `var _ = logging.Redact` exists solely as an import keeper even though the package currently uses `logging.Redact` throughout the file. | Remove the import-keeper line and rely on normal compiler unused-import checks. | No. |
| F-009 | P2 | tests | 4 | `internal/translate/responses_chat_test.go:118-181`, `internal/translate/responses_chat_test.go:315-327` | Translation tests cover text-only, tool-only, finish reason mapping, and streaming mixed text+tool flows. They do not pin non-streaming mixed text+tool output, which is the gap behind F-003. | Add golden tests for Chat responses that include both `message.content` and `message.tool_calls` for each target format. | No. |
| F-010 | P2 | tests | 7 | Multiple; see F-001 through F-006 | Critical-path coverage is strong, but the audit found missing edge tests for force-refresh cancellation, pending terminal SSE without final blank line, non-`sk-` JSON credential redaction, chmod failure propagation, malformed ID-token handling, and provider discovery body-read errors. | Add focused tests after fixes land; measure before/after `go test -cover ./...` for `oauth`, `handlers`, and `translate`. | No. |
| F-011 | P2 | perf | 6 | `internal/providerapi/list.go:55-60` | `ListModels` ignores the result of `io.ReadAll(io.LimitReader(resp.Body, 4<<20))`. A truncated or read-failing provider response can become a misleading parse/empty-model error, and the fixed 4 MiB `LimitReader` does not detect bodies larger than the cap. | Check read errors and use a limit+1 pattern so oversized model lists return a clear bounded-body error. | Yes - error messages become more accurate. |
| F-012 | P2 | simplification | 6 | See ignored-error appendix | The current production source has many explicit blank-identifier error ignores. Most are legitimate deferred `Close`, best-effort cleanup, or committed-response writes, but they are not locally explained and make future audits noisy. A few security-sensitive chmod/JWT sites are real findings (F-005/F-006). | Resolve real sites, add small helper comments for intentional categories where useful, and avoid adding new unclassified blank-error ignores. | No. |
| F-013 | P3 | docs | 8 | `.supergoal/.../ROADMAP.md`, current `go list ./...` | The roadmap claims 23 packages, but current source has 22 Go packages. This does not affect runtime, but release/audit docs should not repeat stale package counts. | Mention the authoritative package count in final audit/docs if package totals are documented. | No. |

## Resolution Log

- F-006: RESOLVED in Phase 2 working diff. `internal/oauth/provider.go` now documents the best-effort JWT identity decision, `internal/oauth/oauth_test.go` adds `TestMalformedJWTIdentityDoesNotMisattributeAccount`, and `TestParseCodexIdentityFallsBackToAccessToken` now covers malformed ID-token fallback to access-token identity.
- F-001: RESOLVED in Phase 3 working diff. `internal/handlers/oauth_responses.go` now releases the selected account and returns without marking it unhealthy when `ForceRefresh` fails after request-context cancellation; `TestResponsesCodex401RefreshCancellationDoesNotMarkUnhealthy` pins no failover, no unhealthy mark, and zero in-flight leases.
- F-002: RESOLVED in Phase 3 working diff. `internal/stream/forward.go` now emits and flushes the missing SSE blank-line boundary before evaluating a pending EOF event; `TestForward_PendingTerminalEOFCompletesFrame` and `TestForward_PendingNonTerminalEOFFlushesBeforeTruncation` pin terminal and truncation behavior.
- F-003: RESOLVED in Phase 4 working diff. `ChatToAnthropicResponse` and `ChatToResponsesResponse` now preserve non-empty assistant text before tool-use/function-call blocks when a Chat response contains both `message.content` and `tool_calls`; `TestChatToAnthropicResponse_TextAndToolCallsPreserved` and `TestChatToResponsesResponse_TextAndToolCallsPreserved` pin ordering and finish/status behavior.
- F-009: RESOLVED in Phase 4 working diff. The mixed non-streaming content+tool-call golden assertions now cover both Chat -> Anthropic and Chat -> Responses output shapes.
- F-004: RESOLVED in Phase 5 working diff. `internal/logging.Redact` now masks generic JSON credential fields such as `access_token`, `refresh_token`, `id_token`, `token`, `secret`, `authorization`, and `credential` without redacting benign fields such as `model` and `trace_id`; `TestRedact_JSONCredentialFields` pins non-`sk-` OAuth-shaped values.
- F-005: RESOLVED in Phase 5 working diff. OAuth auth-dir, token-file, installation-id, and refresh-lock chmod failures now return explicit errors; existing mode tests remain in place, and `TestSaveTokenReturnsAuthDirChmodError`, `TestSaveTokenReturnsTokenFileChmodError`, `TestInstallationIDReturnsChmodError`, and `TestAcquireRefreshFileLockReturnsChmodError` pin failure propagation.
- F-007: RESOLVED in Phase 6 working diff. `responsesViaOAuth`, `responsesViaCodexFailover`, and `codexAuthReplay` now share `doPreparedUpstream` for stream/non-stream request execution; existing OAuth/failover tests cover unchanged behavior.
- F-008: RESOLVED in Phase 6 working diff. The `var _ = logging.Redact` import keeper was removed from `internal/server/middleware.go`; `go build ./...` is the compiler guard.
- F-011: RESOLVED in Phase 6 working diff. Provider model discovery now returns explicit read and oversize errors with a limit+1 reader; `TestListModelsReadError` and `TestListModelsOversizedBody` pin the behavior.
- F-012: RESOLVED in Phase 6 working diff. The real ignored-error sites from F-005/F-008/F-011 are resolved, and the remaining intentional categories stay documented in this inventory; no new production import keepers were added.
- F-010: RESOLVED in Phase 7 working diff. The named edge-test gaps are now covered by focused tests from Phases 2-7: force-refresh cancellation, pending EOF SSE frames, non-`sk-` JSON redaction, chmod failure propagation, malformed JWT fallback, provider discovery read/oversize errors, and `safeErrorMessage` redaction/default/bounding. Targeted coverage improved vs baseline: `internal/oauth` 69.8% -> 69.9%, `internal/handlers` 77.3% -> 77.7%, `internal/translate` 52.8% -> 56.1%; whole-tree race and changed-package `-count=2` runs pass.
- F-013: RESOLVED in Phase 8 working diff. Final audit uses the authoritative current `go list ./...` package count of 22 and avoids repeating the stale roadmap count except to document the discrepancy.

## Ignored-Error Inventory

Recon seeded "22 ignored-error sites"; the current production tree has more explicit blank-identifier sites once CLI flag parsing, stdout writes, and repeated deferred closes are counted. The real fix/keep verdicts are below. Test files are excluded.

Verdict key: `keep` = intentional and safe; `fix` = tracked by a finding; `resolved` = fixed in this working diff; `review` = safe-looking but worth tightening during simplification.

| Site(s) | Verdict | Rationale |
|---|---|---|
| `cmd/droid-proxy/server.go:24`, `cmd/droid-proxy/update_cmd.go:26`, `cmd/droid-proxy/config_cmd.go:14`, `cmd/droid-proxy/daemon_cmd.go:23`, `cmd/droid-proxy/daemon_cmd.go:103`, `cmd/droid-proxy/daemon_cmd.go:157`, `cmd/droid-proxy/daemon_cmd.go:182` | keep | CLI parses permissively and then validates/uses flag values; existing command tests cover behavior. |
| `cmd/droid-proxy/auth_pool.go:68` | keep | Deferred response close after status endpoint request. |
| `cmd/droid-proxy/auth_pool.go:119`, `cmd/droid-proxy/auth_pool.go:140`, `cmd/droid-proxy/auth_pool.go:143` | keep | CLI table output to the caller; write/flush errors are not actionable beyond process stderr behavior. |
| `cmd/droid-proxy/paths.go:133` | review | Best-effort env load in path resolution; acceptable, but a short comment would make intent clear. |
| `internal/upstream/send.go:138`, `internal/upstream/send.go:148` | keep | Deferred body closes in helper functions that return read errors. |
| `internal/update/update.go:223` | keep | Best-effort cleanup after temp-file close error. |
| `internal/tui/backend.go:208` | keep | Stop-before-restart is best effort; subsequent start/wait path determines outcome. |
| `internal/tui/backend.go:241`, `internal/tui/backend.go:260` | keep | Best-effort partial config parsing for UI defaults when full config load is unavailable. |
| `internal/daemon/runtime.go:76`, `internal/daemon/daemon.go:58`, `internal/daemon/launchd.go:91`, `internal/daemon/launchd.go:121`, `internal/daemon/launchd.go:152`, `internal/daemon/launchd.go:195` | keep | Cleanup/fallback paths where primary operation result is already handled or a fallback is attempted. |
| `internal/daemon/launchd.go:125`, `internal/daemon/launchd.go:129` | keep | Close after earlier write/chmod/template errors; the original error is the useful one. |
| `internal/configedit/configedit.go:158` | review | YAML encoder close error is ignored after encode path; low-risk but can be returned in Phase 6. |
| `internal/configedit/configedit.go:322` | keep | Best-effort hydration while loading model snippets for editing, not strict config validation. |
| `internal/factory/settings.go:228` | keep | Close after write error; original write error is returned. |
| `internal/handlers/chat.go:72`, `internal/handlers/messages.go:72`, `internal/handlers/messages.go:184`, `internal/handlers/messages.go:237`, `internal/handlers/responses.go:70`, `internal/handlers/responses.go:135`, `internal/handlers/oauth_responses.go:82`, `internal/handlers/oauth_responses.go:289`, `internal/handlers/oauth_responses.go:311`, `internal/handlers/oauth_responses.go:384`, `internal/handlers/oauth_responses.go:504`, `internal/handlers/oauth_responses.go:524`, `internal/handlers/oauth_responses.go:538` | keep | Response-body close after read/forward paths. Real lease/cancellation issues are tracked separately in F-001/F-002. |
| `internal/handlers/messages.go:99`, `internal/handlers/messages.go:137`, `internal/handlers/messages.go:218`, `internal/handlers/responses.go:97`, `internal/handlers/responses.go:166`, `internal/handlers/responses.go:179` | keep | SSE/error writes after headers may already be committed; tests cover stream/error behavior. |
| `internal/stream/forward.go:86`, `internal/translate/chat_stream.go:356` | keep | Closing readers to unblock scanner goroutines. |
| `internal/server/server.go:161` | keep | Best-effort close after shutdown error path. |
| `internal/server/middleware.go:237` | resolved | F-008 resolved in Phase 6; import keeper removed. |
| `internal/oauth/pool.go:307`, `internal/oauth/pool.go:315`, `internal/oauth/pool.go:323` | review | Affinity persistence errors are intentionally non-fatal today; add a comment/logging decision in Phase 2/6. |
| `internal/oauth/provider.go:280`, `internal/oauth/provider.go:316`, `internal/oauth/provider.go:368` | resolved | F-006 resolved in Phase 2; best-effort JWT identity behavior is documented and tested. |
| `internal/oauth/installation.go:19`, `internal/oauth/installation.go:35`, `internal/oauth/store.go:146`, `internal/oauth/store.go:160`, `internal/oauth/refresh_lock.go:61` | resolved | F-005 resolved in Phase 5; chmod errors now return explicit failures. |
| `internal/oauth/store.go:175`, `internal/oauth/store.go:179`, `internal/oauth/store.go:183`, `internal/oauth/store.go:187`, `internal/oauth/store.go:198`, `internal/oauth/store.go:199` | keep | Temp-file cleanup/close and directory fsync cleanup after primary errors; mode-setting issue tracked separately. |
| `internal/oauth/refresh_lock.go:68`, `internal/oauth/refresh_lock.go:69`, `internal/oauth/refresh_lock.go:73`, `internal/oauth/refresh_lock.go:76`, `internal/oauth/refresh_lock.go:82` | keep | Lock cleanup/unlock paths are best effort; lock creation/write/close errors are returned. |
| `internal/oauth/watcher.go:98`, `internal/oauth/watcher.go:180`, `internal/oauth/watcher.go:229`, `internal/oauth/watcher.go:285` | review | Watcher close/add/remove errors are best effort in hot-reload paths; consider debug logging or comments. |
| `internal/oauth/login.go:68`, `internal/oauth/login.go:115`, `internal/oauth/login.go:124`, `internal/oauth/login.go:129`, `internal/oauth/login.go:140` | keep | Local callback shutdown and browser HTML writes; failure is not actionable after response attempt. |
| `internal/oauth/device.go:143` | keep | Explicit close before draining/retrying a token polling response. |
| `internal/providerapi/list.go:56` | resolved | F-011 resolved in Phase 6; read and oversize errors are returned explicitly. |
