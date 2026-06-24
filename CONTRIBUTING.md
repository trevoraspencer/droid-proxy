# Contributing to droid-proxy

Thanks for helping improve droid-proxy. This project is a localhost HTTP proxy for
[Factory Droid](https://factory.ai) BYOK and custom models.

## Before you start

- Read [VISION.md](VISION.md) first. It is the canonical source of truth for
  project scope, priorities, non-goals, and AI-agent instructions.
- Read the [README](README.md) and [docs/README.md](docs/README.md) to understand
  how the proxy fits together.
- Report security issues privately — see [SECURITY.md](SECURITY.md). Do not open
  public issues for vulnerabilities.
- Be respectful — see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Development setup

**Requirements:** Go 1.26.4 or newer (see `go.mod`).

```bash
git clone https://github.com/trevoraspencer/droid-proxy.git
cd droid-proxy
go build -o droid-proxy ./cmd/droid-proxy
cp config.example.yaml config.yaml
cp .env.local.example .env.local   # add keys you plan to test with
```

## Build and test

```bash
make build
make test
make lint          # gofmt + go vet
make test-race     # optional; slower
make ci-audit      # module path + CI-equivalent checks
```

Documentation and config consistency tests:

```bash
make docs-audit
```

Public-release guard tests (if you touch security or release tooling):

```bash
make pre-public-audit
make legal-audit
```

OAuth and handler integration tests (no real API keys required for most cases):

```bash
go test -race ./internal/oauth/... ./internal/handlers/...
```

## Making changes

1. **Keep scope focused.** Prefer small, reviewable PRs.
2. **Match existing style.** Run `gofmt`; follow patterns in the file you edit.
3. **Update docs with behavior changes.** If you change CLI flags, config schema,
   provider behavior, or Factory integration, update the relevant file under
   `docs/` and any example in `config.example.yaml`.
4. **Add or extend tests** when you fix a bug or add non-trivial behavior.
5. **Do not commit secrets.** Never add real API keys, OAuth tokens, or personal
   paths. Example files must stay placeholder-only.

## Documentation conventions

| Audience | Location |
|----------|----------|
| Users | `README.md`, `docs/CONFIG.md`, `docs/PROVIDERS.md`, `docs/examples/` |
| Contributors | `VISION.md`, `CONTRIBUTING.md` (this file), `docs/CLI.md` |
| Maintainers | `docs/PUBLIC_RELEASE.md`, `scripts/live-e2e/README.md` |

When adding a provider:

1. Add or update a row in `docs/PROVIDERS.md`.
2. Add `docs/examples/<provider>.md` with config, Factory settings, and curl check.
3. Add a `known_auth` entry in code if applicable.
4. Run `make docs-audit`.

## Pull requests

- Describe **what** changed and **why**.
- Note how you tested (commands run, providers exercised).
- Link related issues if any.
- Ensure CI-relevant commands pass locally:

```bash
make lint
make test
make docs-audit
make ci-audit
```

## Live end-to-end tests (maintainers only)

Optional validation against real provider credentials lives in
[scripts/live-e2e/README.md](scripts/live-e2e/README.md). Not required for
typical contributions.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
