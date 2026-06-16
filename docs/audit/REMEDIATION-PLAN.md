# Droid Proxy Remediation Plan

Baseline: `799cdb88274d2085343ea03ab8c178ea49627322`  
Source of truth: `docs/audit/FINDINGS.md`

This plan groups findings by owning Supergoal phase. All P0/P1 findings have exactly one owner below.

## Phase 2 - OAuth pool & quota correctness

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-006 | P1 | M | 1 | RESOLVED in Phase 2 working diff: documented best-effort JWT identity behavior in `internal/oauth/provider.go`; updated `TestParseCodexIdentityFallsBackToAccessToken`; added `TestMalformedJWTIdentityDoesNotMisattributeAccount`. |

Mandatory focus:

- Add or update tests in `internal/oauth` for malformed ID token handling.
- Re-check pool lock invariants while touching OAuth; document `AccountPool.mu` at the struct boundary if the Phase 2 acceptance gate requires it.
- Run Phase 2 commands including `go test -race ./internal/oauth/...`.

## Phase 3 - Request-path & streaming correctness

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-001 | P1 | S | 1 | RESOLVED in Phase 3 working diff: added a request-context cancellation guard around Codex force-refresh replay failure before `MarkUnhealthy`; added `TestResponsesCodex401RefreshCancellationDoesNotMarkUnhealthy`. |
| F-002 | P1 | M | 2 | RESOLVED in Phase 3 working diff: `stream.Forward` now flushes a pending EOF frame boundary before terminal/truncation evaluation; added `TestForward_PendingTerminalEOFCompletesFrame` and `TestForward_PendingNonTerminalEOFFlushesBeforeTruncation`. |

Mandatory focus:

- Add handler regression coverage for cancellation during forced refresh.
- Add stream unit tests and, if needed, one handler-level test proving downstream receives a complete terminal event.
- Re-run `go test ./internal/handlers/... ./internal/stream/... ./internal/upstream/...` and full `go test ./...`.

## Phase 4 - Translation fidelity & reasoning replay

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-003 | P1 | M | 1 | RESOLVED in Phase 4 working diff: preserve non-empty assistant text before tool-use/function-call output when non-streaming Chat responses also contain `tool_calls`, for both Chat -> Anthropic and Chat -> Responses. |
| F-009 | P2 | S | 2 | RESOLVED in Phase 4 working diff: added mixed content+tool-call golden assertions for both target formats. |

Mandatory focus:

- Preserve existing finish-reason behavior: tool calls still map to `tool_use`/function-call completion.
- Add exact JSON assertions for content block/item ordering.
- Re-run `go test ./internal/translate/... ./internal/reasoning/... ./internal/tokens/...` and full `go test ./...`.

## Phase 5 - Security, secrets & config correctness

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-004 | P1 | S | 1 | RESOLVED in Phase 5 working diff: redaction now covers generic JSON credential names (`access_token`, `refresh_token`, `id_token`, `token`, `secret`, `authorization`, etc.) with non-`sk-` OAuth test vectors. |
| F-005 | P1 | M | 2 | RESOLVED in Phase 5 working diff: security-sensitive chmod errors are now visible for OAuth auth dirs, token files, installation IDs, and refresh locks; mode and failure behavior are tested. |

Mandatory focus:

- Avoid over-redacting benign JSON fields such as model names or trace IDs.
- Keep token-file and managed secret-file modes at `0600`; auth/lock dirs at `0700`.
- Run security/config/server/logging/secrets package tests and full `go test ./...`.

## Phase 6 - Simplification, dedup & dead code

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-007 | P2 | M | 1 | RESOLVED in Phase 6 working diff: extracted `doPreparedUpstream` and removed the triplicated stream/non-stream Do blocks from OAuth Responses paths. |
| F-008 | P2 | XS | 2 | RESOLVED in Phase 6 working diff: removed the `var _ = logging.Redact` import keeper. |
| F-011 | P2 | S | 3 | RESOLVED in Phase 6 working diff: provider discovery now returns explicit body read/oversize errors. |
| F-012 | P2 | S | 4 | RESOLVED in Phase 6 working diff: real ignored-error sites from F-005/F-008/F-011 are fixed; remaining intentional categories are documented in `FINDINGS.md`. |

Mandatory focus:

- Keep behavior unchanged for F-007; existing OAuth failover tests are the safety net.
- No new blank-identifier import keepers or unexplained ignored errors.
- Run `go test -race ./internal/oauth/... ./internal/handlers/...` after handler refactoring.

## Phase 7 - Tests, coverage & performance

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-010 | P2 | M | 1 | RESOLVED in Phase 7 working diff: critical edge-test gaps are covered, targeted package coverage improved vs baseline, whole-tree race passed, and changed packages passed `-count=2`. |

Mandatory focus:

- Measure `go test -cover ./...` before/after for `oauth`, `handlers`, and `translate`.
- Run `go test -race ./...`.
- Run `go test -count=2` for changed packages.

## Phase 8 - Release-readiness, docs/CHANGELOG sync & Polish/Harden

| Finding | Severity | Effort | Order | Plan |
|---|---|---:|---:|---|
| F-013 | P3 | XS | 1 | RESOLVED in Phase 8 working diff: final audit uses the authoritative current `go list ./...` package count of 22 and does not repeat stale roadmap package totals except as a discrepancy note. |

Mandatory focus:

- Add CHANGELOG entries and migration notes for any behavioral changes from Phases 2-7.
- Reconcile README, `config.example.yaml`, help output, docs, and audit artifacts.
- Run full release gates: build, vet, gofmt, full tests, race, `make lint`, `make docs-audit`.

## P0/P1 Owner Check

| Finding | Severity | Owner |
|---|---|---:|
| F-001 | P1 | Phase 3 |
| F-002 | P1 | Phase 3 |
| F-003 | P1 | Phase 4 |
| F-004 | P1 | Phase 5 |
| F-005 | P1 | Phase 5 |
| F-006 | P1 | Phase 2 |

No P0 findings were found in Phase 1.
