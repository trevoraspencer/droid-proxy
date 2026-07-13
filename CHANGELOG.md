# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `droid-bench` performance evaluation suite (`cmd/droid-bench`): a
  deterministic mock provider with request capture and simulated prompt
  caching, a scenario harness that benchmarks droid-proxy against direct
  provider connections and alternative proxies (TTFB/TTFT percentiles,
  inter-chunk pacing, throughput, cache-hit %), and prompt-cache fidelity
  checks (byte determinism, prefix stability, `cache_control`/usage
  passthrough, stream integrity). New `make bench` and `make bench-compare`
  targets, CI-covered fidelity tests, and [docs/BENCHMARKS.md](docs/BENCHMARKS.md).
- Micro-benchmarks for the translation, SSE pump, and payload-override hot
  paths.

- `capabilities.factory_reasoning_effort` metadata and Factory settings sync
  support so GPT-5.6 and Grok 4.5 custom models expose reasoning controls in
  Droid while Grok Build and Composer remove stale reasoning metadata.
- `doctor` probes `/health` on the configured listen address and on
  `[::1]:<port>`: a foreign responder on the configured address is a hard
  issue, and an IPv6 listener shadowing `localhost` URLs (for example
  Cursor's MCP OAuth loopback or `wrangler dev` on port 8787) is called out
  with a warning that points checks at `http://127.0.0.1:<port>`.
- Stale-config detection: the model-not-found 404, `doctor`, and the config
  TUI now say when `config.yaml` changed after the running proxy loaded it
  and that a restart applies it.
- `status` and `doctor` query launchctl/systemctl for the live service state,
  so a proxy running under the managed service is reported correctly even
  when the local pidfile is stale.
- `docs/TROUBLESHOOTING.md` covering localhost/IPv6 port squatters, stale
  config, and managed-service semantics.

### Changed

- `extra_args` are applied to upstream payloads in sorted key order, so
  identical client requests always produce byte-identical upstream bodies
  (previously map iteration order could reorder appended keys per request).
- The Anthropic→OpenAI-chat translator drops `cache_control` hints instead of
  rejecting the request: Droid sends `cache_control` whenever Anthropic
  prompt caching is enabled, and OpenAI-style upstreams cache prompt prefixes
  implicitly, so dropping the hint is the safe mapping. Translated Anthropic
  aliases now work with Droid prompt caching turned on.
- With the per-user service installed, `droid-proxy stop` now stops through
  the service manager (`launchctl bootout` / `systemctl --user stop`) so
  KeepAlive cannot immediately resurrect the process; previously it sent
  SIGTERM and the service restarted within seconds.

### Fixed

- The config TUI restart action restarts the managed service when one is
  installed instead of spawning a competing background daemon.

## [0.2.1] - 2026-07-12

### Fixed

- Generate and validate one SPDX SBOM from each shipped binary so all embedded Go dependencies are inventoried, then attach each SBOM only to its corresponding archive.

## [0.2.0] - 2026-07-12

### Added

- Z.AI GLM Coding Plan support for `glm-5.2`, including the sample
  configuration, Factory settings, and credential-gated live-E2E coverage.
- First-class GPT-5.6 Codex OAuth presets for the recommended `gpt-5.6` Sol
  alias plus Terra and Luna, with standard/priority-request variants, 1.05M
  context, 128K output metadata, capability metadata, credential-validated
  explicit `gpt-5.6-sol` routing for the local Sol aliases, and
  caller-preserving Codex CLI `0.144.0` Version/User-Agent fallbacks that
  provide current client metadata for Luna. Includes focused
  forwarding/failover tests and credential-gated live-E2E coverage.
- Release asset audit for packaged archives, checksums, installer upload artifacts, and release build identity.
- Release installer `--restart` / `--no-restart` flags so upgrades can restart a running proxy after the binary is replaced.

### Fixed

- Linux daemon PID checks now recognize the procfs ` (deleted)` executable suffix left behind after replacing a running binary.

### Security

- Hardened the release installer to reject archives with unsafe paths or link entries before extraction.
- Release workflows now pin every third-party Action to an immutable commit,
  isolate write permissions to the publishing job, generate and checksum an SPDX
  SBOM, and publish GitHub build-provenance and SBOM attestations before uploading
  release assets.

## [0.1.0] - 2026-07-04

### Added

- One-command GitHub release installer (`install.sh`) with checksum verification, idempotent upgrades, noninteractive mode, and per-user install defaults.
- `droid-proxy setup` for config seeding and per-user service setup on macOS launchd and Linux systemd.
- `droid-proxy doctor` diagnostics for release installs, source installs, stale service files, missing config paths, and service repair guidance.
- Interactive `droid-proxy config` dashboard for provider onboarding, managed env storage, model discovery, and Factory settings sync.
- Curated provider docs and examples for Anthropic, OpenAI, DeepSeek, Xiaomi MiMo, xAI, Kimi, Z.AI, Groq, Fireworks, Ollama, vLLM, Codex/ChatGPT OAuth, and xAI OAuth.
- Codex OAuth multi-account load balancing with sticky, round-robin, fill-first, least-connections, and random selection strategies.
- Release asset packaging and GitHub Actions workflows for CI and tagged releases.
- Public security, legal, license, and contributor documentation.
- Install, upgrade, and repair guide in `docs/UPGRADE.md`.

### Changed

- Release installs no longer depend on a source checkout for config seeding, service setup, or healthy `doctor` output.
- Default config resolution checks the per-user runtime config before development files.
- Service commands route to the host OS backend instead of being macOS-only.
- Build identity reports deterministic version and commit data from release ldflags or Go VCS build metadata.
- Provider model discovery reports read and size-limit failures explicitly.
- Chat translation preserves assistant text when a response also contains tool calls.

### Fixed

- macOS service installation validates config paths before writing LaunchAgent files.
- OAuth force-refresh cancellation no longer marks the selected Codex account unhealthy.
- SSE forwarding completes pending event boundaries at EOF.
- Generic JSON credential fields are redacted in logs.
- OAuth token, auth directory, installation ID, and refresh-lock permission hardening errors fail visibly.
- Malformed OAuth ID-token identity handling is documented and tested.

[Unreleased]: https://github.com/trevoraspencer/droid-proxy/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/trevoraspencer/droid-proxy/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/trevoraspencer/droid-proxy/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/trevoraspencer/droid-proxy/releases/tag/v0.1.0
