# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Public-release preparation tooling: secret audit, legal audit, docs audit,
  orphan-branch history script, and contributor documentation.
- `SECURITY.md`, `CODE_OF_CONDUCT.md`, `NOTICE`, and third-party license docs.
- Codex OAuth multi-account pooling with sticky affinity, failover, and pool
  health endpoint.
- Interactive `droid-proxy config` dashboard for provider onboarding.
- Provider examples and Factory settings snippets under `docs/`.

### Changed

- Documentation reorganized for public release (removed internal planning
  artifacts, added contributor guides).
- Non-streaming Chat translation now preserves assistant text when the same
  response also contains tool calls for both Anthropic Messages and OpenAI
  Responses targets.
- Provider model discovery now reports response read failures and oversized
  model-list bodies explicitly instead of surfacing misleading parse errors.

### Fixed

- Codex OAuth force-refresh cancellation no longer marks the selected account
  unhealthy or fails over to another account.
- `droid-proxy auth --help` and `droid-proxy service --help` now print usage
  and exit successfully instead of being treated as invalid subcommands.
- SSE forwarding now completes a pending final event boundary at EOF before
  terminal/truncation handling, avoiding dropped terminal frames.
- Malformed OAuth ID-token identity handling is documented and tested so it
  cannot misattribute an account.

### Security

- Log redaction now covers generic JSON credential fields such as
  `access_token`, `refresh_token`, `id_token`, `token`, `secret`, and
  `authorization` for non-`sk-` OAuth-shaped values.
- OAuth auth directory, token file, installation ID, and refresh-lock permission
  hardening errors now fail loudly instead of being silently ignored.

### Migration Notes

- Environments on filesystems that reject `chmod` for OAuth auth files may now
  see login/token-save errors; fix the filesystem or directory permissions
  rather than relying on best-effort mode hardening.

## [0.1.0] - TBD

First public release. Tag this version after the orphan-branch publish step
described in [docs/PUBLIC_RELEASE.md](docs/PUBLIC_RELEASE.md).

[Unreleased]: https://github.com/trevoraspencer/droid-proxy/compare/main...HEAD
[0.1.0]: https://github.com/trevoraspencer/droid-proxy/releases/tag/v0.1.0
