# Benchmarks: measuring droid-proxy against native and alternative proxies

This suite answers three questions with numbers instead of vibes:

1. **General performance** — how much latency does droid-proxy add over
   connecting Factory Droid directly to a provider, and how does it compare to
   alternative local bridge proxies (ProxyPilot, vibeproxy, droidproxy, and
   similar projects)?
2. **Caching** — does traffic through the proxy still engage provider prompt
   caches (Anthropic explicit `cache_control`, OpenAI/DeepSeek implicit prefix
   caching), and do cache-hit counters survive the round trip so Droid can see
   them?
3. **Overall optimization** — where does the proxy spend CPU per request, and
   which known proxy optimizations does it implement or lack?

The suite has three layers, all driven by the `droid-bench` binary and Go
benchmarks:

| Layer | What it measures | How to run |
|---|---|---|
| Fidelity checks | Cache-critical proxy behavior (byte determinism, prefix stability, `cache_control`/usage passthrough, stream integrity) | `droid-bench cache-check`, `make bench-compare`, or CI (`go test ./internal/bench/...`) |
| End-to-end harness | TTFB / TTFT / total latency percentiles, inter-chunk gaps, throughput, cache-hit % per target | `droid-bench run`, `make bench-compare` |
| Micro-benchmarks | CPU/allocations of translation and stream hot paths | `make bench` |

## Quick start: local overhead + fidelity

```bash
make bench-compare          # full run (~2 min)
bash scripts/bench/local-compare.sh --quick   # fast smoke (~30 s)
```

This builds `droid-proxy` and `droid-bench`, starts a deterministic mock
provider (`droid-bench mock`) with simulated model latency, points a
temporary droid-proxy config at it, and measures the same workloads through
both paths. Reports land in `bench-results/` (text, markdown, JSON). The run
finishes with the fidelity checks; any `FAIL` line is a real defect.

Because both targets talk to the same mock on loopback, network noise is
eliminated and every millisecond of difference is proxy overhead. The mock's
`--ttft 5ms --inter-chunk 2ms` defaults are deliberately fast: against a real
provider (hundreds of ms), overhead this small disappears into noise — which
is itself a result worth knowing.

## Benchmarking against native providers and other proxies

`droid-bench run` drives any OpenAI/Anthropic-compatible endpoint, so the
same scenarios compare Droid's three connection options:

- **native** — Droid → provider directly (the `baseline: true` target),
- **droid-proxy** — Droid → droid-proxy → provider,
- **alternatives** — Droid → ProxyPilot / vibeproxy / droidproxy → provider
  (they all expose OpenAI/Anthropic-compatible local endpoints; vibeproxy and
  droidproxy bundle the same third-party proxy engine that ProxyPilot forks,
  so benchmarking one instance of that engine largely covers all three).

```bash
droid-bench example-config > bench.yaml   # edit targets/scenarios
droid-bench run --config bench.yaml --json out.json --md out.md
```

Guidance for live-provider runs:

- Mark the direct-provider target `baseline: true`; deltas are computed
  against it per scenario.
- Use `unique_prompts: true` for latency scenarios so provider caches don't
  flatter later requests, and `growing_conversation: true` (with warm
  requests) for cache scenarios.
- Use ≥30 requests per scenario and compare p50/p95, not means; wide-area
  network variance dominates single samples.
- Use `--repeat N` (≥4) for any comparison you intend to act on: it
  interleaves full passes over the targets (A/B/A/B) and reports paired
  per-rep deltas with mean±sd, which cancels host/provider drift and puts
  error bars on the difference.
- The `cache hit %` column is computed from provider usage counters
  (`cached_tokens` / `cache_read_input_tokens`), so it works on live
  providers exactly as on the mock. On a shared mock run, compare a target's
  cache % only with itself across scenarios (targets prime the mock cache in
  sequence).
- Run cells are sequential by design: targets never compete for bandwidth or
  provider rate limits mid-measurement.

Scenario knobs model Droid's actual traffic: multi-kilobyte system prompts,
tool-call/tool-result history turns, streaming with `stream_options.include_usage`,
Anthropic `cache_control` breakpoints, and growing conversations that replay
the previous turns byte-for-byte (`growing_conversation`).

## Fidelity checks: what "caching works" actually requires

`droid-bench cache-check` verifies the properties that silently make or break
prompt caching through any proxy. It sends canonical requests through the
proxy to a capture-enabled mock and asserts on what the proxy forwarded and
returned:

If the proxy has `client_auth` enabled, provide the credential through the
environment rather than the process argument list:

```bash
DROID_PROXY_CLIENT_KEY=... droid-bench cache-check [model flags]
```

The mock intentionally binds only to loopback because its capture endpoint
returns full request bodies. Each captured body is limited to 16 MiB.

| Check | Property | Why it matters |
|---|---|---|
| `chat-field-passthrough` | `messages`, sampling params, `prompt_cache_key`, unknown fields reach upstream byte-identical | Dropped fields disable provider-side caching/routing features |
| `chat-upstream-determinism` / `translated-upstream-determinism` | identical client requests → byte-identical upstream bodies | Nondeterministic serialization defeats body-keyed dedup and makes diffing impossible |
| `*-prefix-stability` (chat, anthropic-native, anthropic-translated) | earlier conversation turns keep exactly the same bytes as turns are appended | Prefix drift zeroes implicit prompt-cache hits on **every** agent turn |
| `anthropic-cache-control-passthrough` | `cache_control` blocks survive native passthrough untouched | This is how Droid engages Anthropic's explicit prompt cache |
| `*-usage-passthrough` | `cached_tokens` / `cache_read_input_tokens` round-trip to the client | Without them, cache behavior is invisible to Droid and to you |
| `*-stream-integrity` | chunk order, usage frames, and terminal markers survive streaming | Re-framing bugs corrupt agent runs in ways plain latency tests miss |

The same checks run in CI against an in-process proxy + mock
(`internal/bench/fidelity`), so regressions in any of these properties fail
`make test`.

## Micro-benchmarks

```bash
make bench
```

Covers `AnthropicToChatRequest` (small/agentic/large), `ChatToAnthropicResponse`,
both chat-stream translators, the raw SSE pump (`stream.Forward`), and
`applyUpstreamPayloadOverrides`. Reference numbers from a shared CI-class
container (Linux, 4 vCPU):

| Path | Cost per request | Allocations |
|---|---|---|
| Payload override (sjson splice, 80 KB body) | ~0.13 ms | 11 allocs |
| Anthropic→Chat translation, 12-turn agentic body | ~0.6 ms | ~1.9 k allocs |
| Anthropic→Chat translation, 24-turn 8 KB-text body | ~5 ms | ~3.6 k allocs |
| Chat-stream→Anthropic/Responses re-framing (120 chunks) | ~0.7 ms | ~5 k allocs |
| Raw SSE pump (200 lines) | ~0.12 ms | ~800 allocs |

Interpretation: native passthrough routes are effectively free; the translated
routes cost single-digit milliseconds of CPU per request at agentic payload
sizes — visible on loopback, negligible against real provider latency, and
worth optimizing only if profiling shows it in your workload.

## Current results and known gaps

Measured with this suite (mock upstream, loopback, quick mode):

- droid-proxy adds **~0.5–0.7 ms TTFB** per request over direct; ~1 % of
  total time on a 40-chunk stream, ~6 % on total latency under 8-way
  concurrent streaming. Inter-chunk pacing is preserved (flush-per-event).
- All fidelity checks pass: byte-deterministic upstream bodies, stable
  conversation prefixes on all three paths, `cache_control` and usage
  passthrough, intact stream framing.

### Three-way head-to-head (in-container, mock upstream)

The shared proxy engine used by the reference projects (built from its public
Go module, v7.2.73) was configured with the same mock as an OpenAI-compatible
provider and measured with identical scenarios using `droid-bench run
--repeat 6`: six interleaved passes over the target matrix, each target's
per-rep p50 paired against the same-rep baseline so shared-host drift cancels
out of the deltas. The engine ran in its lowest-overhead shipped
configuration (`commercial-mode` enabled, which removes its request-capture
middleware; stdout discarded — its per-request console log cannot be disabled
below Info level). Added latency vs the direct baseline, paired p50 deltas
(mean±sd over 6 reps):

| Scenario | droid-proxy | shared engine |
|---|---|---|
| chat-small-nonstream (ttfb) | +9.3%±0.8 | +11.6%±2.1 |
| chat-agentic-stream (ttft) | +8.6%±1.9 | +14.8%±3.9 |
| chat-large-context-nonstream (ttfb) | +22.3%±5.4 | +51.9%±8.5 |
| chat-concurrent-stream ×8 (ttft) | +9.6%±3.9 | +14.8%±5.5 |

droid-proxy is lighter on every scenario; the separation is decisive on
large-context payloads (where the engine's overhead grows superlinearly with
body size) and inside the error bars on the small-request and concurrent
scenarios. An earlier single-pass run had reported droid-proxy at +0.7% ttft
under concurrency — the repeated, paired measurement shows that was
single-sample flattery; ~9–10% (≈0.85 ms absolute) is the honest number.
Equalizing logging did not close the engine's gap, so the difference is not a
logging artifact.

Both proxies pass every applicable fidelity check (byte-identical chat
passthrough, deterministic prefix-stable translation, usage/`prompt_cache_key`
passthrough, stream integrity) — caching correctness does not differentiate
them on these paths; overhead and footprint do. Resident memory after the run:
droid-proxy ~21 MB vs ~48 MB; binary ~35 MB vs ~56 MB. Behavioral differences
observed: the engine re-serializes response JSON (alphabetized keys) and
reports the upstream model name instead of the client-facing alias unless its
force-mapping option is enabled, and it fetches remote model catalogs at
startup. Percentages are loopback-relative: against a real provider, both
proxies' absolute overhead (≈0.5–3.6 ms here) is noise; the fidelity
properties are what carry over. Not covered by this comparison: OAuth paths
(need real accounts), TLS/HTTP2 connection behavior (plaintext loopback), and
the macOS wrapper apps' own relay layers.

Two fixes shipped with the suite (both were found by writing it):

- `extra_args` are now applied in sorted key order, making upstream bodies
  byte-deterministic across identical requests (previously Go map iteration
  order could reorder appended keys per request).
- The Anthropic→OpenAI-chat translator now **drops** `cache_control` hints
  instead of rejecting the request with a 400. Droid sends `cache_control`
  whenever Anthropic prompt caching is on, so translated aliases were
  unusable in that mode; OpenAI-style upstreams cache prefixes implicitly, so
  dropping the hint is the correct mapping.

Optimization gaps relative to the alternatives (from a source-level survey of
ProxyPilot, vibeproxy, droidproxy, and the shared proxy engine the latter two
bundle — ProxyPilot is a fork of that same engine, so most engine features
apply to all three), roughly ordered by expected value for Droid workloads:

1. **Session-affinity beyond Codex OAuth.** droid-proxy has sticky affinity
   for the Codex account pool; the shared engine applies session-sticky
   routing universally (keyed from headers/`prompt_cache_key`/metadata) so
   provider-side prompt caches keep hitting under multi-account rotation.
   Relevant if API-key pools are ever load-balanced.
2. **Stable `prompt_cache_key` minting.** The shared engine derives a
   persistent per-session `prompt_cache_key` (surviving proxy restarts) and
   injects it for Responses-API upstreams; droid-proxy preserves the client's
   key but never mints one when the client sends none.
3. **Pre-first-byte stream retries.** The shared engine retries/rotates
   credentials on streaming requests until the first downstream byte is
   written — failover the client never sees. droid-proxy surfaces the
   upstream error instead.
4. **Retry-After-aware cooldowns for API-key upstreams.** The OAuth pool has
   cooldowns; plain API-key models fail through immediately with no parsed
   `Retry-After` backoff.
5. **Anthropic 4-breakpoint clamping.** The shared engine deterministically
   drops excess `cache_control` breakpoints (Anthropic's limit is 4) instead
   of letting the upstream 400; droid-proxy forwards bodies as-is.
6. **Metrics endpoint.** ProxyPilot exposes Prometheus counters (latency,
   per-model tokens, cache hit/miss). droid-proxy has structured logs only;
   `droid-bench` fills the gap externally but a `/metrics` endpoint would
   make regressions visible in normal operation.
7. **Optional response cache.** ProxyPilot caches identical non-streaming
   responses (TTL + LRU). Marginal for agent traffic (bodies rarely repeat
   verbatim), and it risks staleness — measure with `chat-cache-growth`
   before considering it.
8. **Token-budget trimming.** ProxyPilot trims oversized payloads
   append-only against the model's context window before the upstream 400s.

Deliberate non-gaps: droid-proxy already flushes per SSE event, injects
keep-alive frames on idle streams, pools upstream connections with HTTP/2,
uses byte-splicing (`sjson`) instead of re-serialization on native paths (the
same trick droidproxy's ThinkingProxy advertises for cache-key stability),
deduplicates OAuth refreshes, and serves `count_tokens` locally.

## Reading the reports

- `ttfb p50` — request start → response headers. The purest proxy-overhead
  number for non-streaming requests.
- `ttft p50/p95` — request start → first content token (streaming). What
  Droid users feel as "the model started answering".
- `total p50/p95` — request start → terminal event. Streaming totals are
  dominated by generation time; look at deltas, not absolutes.
- `gap mean` / `max gap` — inter-chunk pacing. A proxy that buffers streams
  shows up here even when totals look fine.
- `req/s`, `chunks/s` — throughput of the measured phase (per-cell wall
  time), meaningful when `concurrency > 1`.
- `cache %` — `cached_tokens ÷ prompt_tokens` from provider usage counters.
  On growing-conversation scenarios against live providers this is the
  headline caching number; expect it to climb toward the provider's maximum
  cacheable share after the first few turns.

## Caveats

- Loopback results measure proxy overhead, not user experience; against real
  providers the same overhead is usually noise. Run both.
- Live-provider numbers include provider-side variance (load, region,
  cache warmth). Interleave repeat runs and compare percentiles.
- The mock's simulated prompt cache models prefix matching at message
  granularity; providers cache at token-block granularity, so live cache-hit
  ratios will differ in magnitude (not in direction).
- `droid-bench` measures the proxy layer, not Droid itself; Droid's own
  request shaping (system prompt, tool schemas) is approximated by the
  scenario knobs.
