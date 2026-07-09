# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Hardened the release installer to reject archives with unsafe paths or link entries before extraction.

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

[Unreleased]: https://github.com/trevoraspencer/droid-proxy/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/trevoraspencer/droid-proxy/releases/tag/v0.1.0
