# Contributing to droid-proxy

Thanks for helping improve `droid-proxy`. This project is a localhost HTTP proxy for Factory Droid BYOK, local-model, and OAuth workflows.

## Before You Start

- Read [README.md](README.md), [docs/README.md](docs/README.md), and [VISION.md](VISION.md) to understand the project scope.
- Report vulnerabilities privately through [SECURITY.md](SECURITY.md).
- Follow [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
- Keep credentials, local runtime files, and personal paths out of commits.

## Development Setup

Requirements: Go 1.26.4 or newer.

```bash
git clone https://github.com/trevoraspencer/droid-proxy.git
cd droid-proxy
go build -o droid-proxy ./cmd/droid-proxy
cp config.example.yaml config.yaml
cp .env.local.example .env.local
```

Add only test credentials to `.env.local`; the file is ignored by git.

## Build And Test

```bash
make build
make test
make lint
make test-race
make docs-audit
make security-audit
make legal-audit
make ci-audit
```

Most OAuth and handler tests use local fakes and do not require real provider credentials:

```bash
go test -race ./internal/oauth/... ./internal/handlers/...
```

## Making Changes

1. Keep the change focused and reviewable.
2. Match the style of the surrounding code and run `gofmt` for Go changes.
3. Update docs and examples when behavior changes CLI flags, config schema, provider routing, OAuth, services, or Factory settings.
4. Add or update tests for bug fixes and non-trivial behavior.
5. Keep example configs placeholder-only.

## Documentation Conventions

| Audience | Location |
|---|---|
| Users | `README.md`, `docs/README.md`, `docs/CONFIG.md`, `docs/PROVIDERS.md`, `docs/examples/` |
| Contributors | `CONTRIBUTING.md`, `VISION.md`, `docs/CLI.md` |
| Release and security checks | `scripts/*-audit.sh`, `.github/workflows/` |

When adding a provider:

1. Add or update a row in `docs/PROVIDERS.md`.
2. Add or update a guide under `docs/examples/`.
3. Add a `known_auth` entry when the provider has a reusable auth profile.
4. Run `make docs-audit`.

## Pull Requests

- Describe what changed and why.
- List the commands used to test the change.
- Link related issues when applicable.
- Keep real API responses, OAuth token files, screenshots with account data, and local paths out of the PR.

## Live End-To-End Checks

The optional harness in [scripts/live-e2e/README.md](scripts/live-e2e/README.md) validates real provider credentials. It is not required for typical contributions.

## License

By contributing, you agree that your contributions are licensed under the project's [MIT License](LICENSE).
