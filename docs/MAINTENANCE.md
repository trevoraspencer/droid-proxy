# Maintenance review — 2026-10-08

This review covers the HTTP boundary, native and translated protocols,
stream termination, OAuth persistence and failover, provider profiles,
configuration, daemon/update/migration paths, dependencies, installer,
documentation, and release controls. The project retains its Factory Droid
and local-proxy scope from [VISION.md](../VISION.md).

## Remediated findings

- Streaming requests had no response-header deadline and could hang before
  receiving an event. Dialing and headers now have deadlines; error bodies
  are bounded in time while healthy streams retain their idle-based lifetime.
- Terminal SSE events previously waited for EOF, allowing an open connection
  to hang or append a spurious error. Native and translated pumps now finish
  at terminal frames, including data-only Responses and Anthropic errors.
- Responses `incomplete` events now retain partial output and incomplete
  status. Multiline/CRLF OAuth events are parsed as frames. Failed Responses
  expose the nested error safely, and separate quota events merge their windows.
- Translated Responses keep a stable ID across their lifecycle. Text and tool
  argument accumulation uses builders to avoid repeated copying on long streams.
- Cached reasoning text has a 64 MiB budget alongside TTL and entry limits.
- Trace logging preserves connection deadline control. Native Anthropic
  errors retain Retry-After and request IDs. Connection headers are filtered.
- Malformed JSON and invalid model/stream types are rejected before routing.
  Public Responses providers accept native Chat requests as documented.
- Duplicate top-level JSON fields are rejected to prevent disagreement between
  proxy and backend parsers. Browser-origin inference requests are rejected
  before routing, protecting unauthenticated localhost credentials from web pages.
- Refresh, disable, logout, and quota writes share cancellable process/file
  locking. Concurrent forced refreshes reuse refreshed credentials, and late
  telemetry cannot recreate logged-out credentials. Disabled accounts stay disabled.
- Codex quota errors honor scalar `resets_at` / `resets_in_seconds` fields.
- OAuth `extra_headers` now supports client-profile overrides while protecting
  credentials and account identity. `api-key` is reserved alongside other
  provider authentication headers. Injected metadata has deterministic order.
- Current picker profiles are GPT-6.1 Sol, GPT-6 Astra, GPT-6 Luna, Grok 4.7,
  and Grok 4.7 Fast. Existing configuration and older xAI presets remain usable.
  Codex version metadata is 0.162.0; Grok is 1.0.50, replacing a rejected 0.2.x
  profile. Examples, Factory snippets, and live test mappings match the picker.
- Go 1.27.2 and compatible dependencies/tools are updated. YAML uses the
  YAML organization's stable v3.0.5 release with compatible Node/encoding
  behavior; v4 remains a release candidate and was deferred. Its license record
  now covers both MIT and Apache-2.0 files. Obsolete development
  bootstrap fixtures and a guard prohibiting upstream attribution were removed.
- Secret audits scan both history and the current working tree, including new
  files, and redact any reported credential values.

## Upstream code review

The main comparison is
[CLIProxyAPI at 54946fa](https://github.com/router-for-me/CLIProxyAPI/tree/54946fa3dfa29c6ca7312ac141a92cdd5e413771).
The review inspected Codex and xAI executors, client-version handling, response
repair, stream completion, model catalogs, and auth selection. It informed the
profile updates, explicit terminal handling, and configuration overrides.
No upstream implementation was copied or added as a dependency.

The original router-for-me/CLIProxyAPIPlus URL was unavailable during the review.
A related public fork,
[cliproxyapi-plusplus at 5cf4773](https://github.com/kooshapari/cliproxyapi-plusplus/tree/5cf47738fe21f30f4e6bf1086a16d36503272487),
was inspected for executor and retry behavior; its Codex reset-field handling
informed the scalar quota-reset fix. This is a comparison with that fork, not
verification of the unavailable original repository.

Useful simplifications were adopted in shared stream parsing and token locking.
Broader upstream features such as WebSocket transports, a management server,
background npm polling, and many additional auth providers would increase this
project's operational scope. Client versions remain explicit and overridable;
model configuration remains independent of the compiled presets.

## Current provider sources

- [OpenAI model catalog](https://developers.openai.com/api/docs/models) and
  [Codex model guide](https://learn.chatgpt.com/docs/models) identify the current
  model family. The reviewed Codex client catalog uses 272,000 context tokens;
  public API limits differ, so presets use that conservative private-client value.
- [OpenAI Codex 0.162.0](https://github.com/openai/codex/releases/tag/rust-v0.162.0)
  supplies the current stable client-version reference.
- [Responses streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)
  define completed, incomplete, and failed terminal results.
- [Grok 4.7 guide](https://docs.x.ai/developers/grok-4-7) documents context and
  reasoning. The private Grok profiles follow the reviewed upstream catalog;
  public API access and OAuth model access are separate.
- [Official Grok CLI package](https://www.npmjs.com/package/@xai-official/grok)
  reported version 1.0.50. CLIProxyAPI's reviewed client-version code records
  the private proxy's minimum 1.0.13 and HTTP 426 for older clients.

## Validation and limits

Final checks passed on Linux with Go 1.27.2:

- Full Go test suite with the race detector, plus targeted regressions for the
  final Origin check and token-lock changes; vet, Staticcheck, and gofmt.
- Module checksum verification and govulncheck. No affected symbols or imported
  packages were reported. One module-only advisory remains:
  [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), for unused
  `golang.org/x/crypto/openpgp`; that package is not imported by this project.
- Documentation, secret/history/working-tree, license, CI, and release audits.
- Installer tests and release cross-builds for Linux and Darwin, amd64 and arm64.
- ShellCheck for 11 Bash/POSIX scripts and syntax checks for all 26 shell scripts.
- The local quick benchmark had zero request errors. All 12 prompt-prefix,
  tool/usage, native/translated streaming, and cache-fidelity checks passed.
  Small-sample timings are a smoke test, not a throughput guarantee.

All provider protocol tests use local fake upstreams and synthetic credentials.
No credentialed requests to live providers were made during this review.
Private OAuth endpoints can change without a published contract; account,
region, workspace policy, and rollout determine model availability.
Use [the live-E2E harness](../scripts/live-e2e/README.md) with your own account
to establish live availability for the routes you use.

The installer and daemon logic can be tested locally and cross-compiled;
this Linux review does not establish native macOS launchd behavior.

## Keeping the repo current

Before releases, run `make lint test test-race vulncheck docs-audit security-audit
legal-audit ci-audit release-audit test-installer release-dry-run`. Recheck the
official model/client sources above when a private endpoint returns a version
or model error. Override client metadata with model `extra_headers` when
needed, and validate that change with the local tests and live harness.
Dependency updates should retain module integrity, pinned CI actions,
checksum-verified tooling, and release provenance controls.
