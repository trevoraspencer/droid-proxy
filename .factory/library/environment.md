# Environment

Environment variables, external dependencies, and setup notes.

**What belongs here:** Required env vars, external API key placeholders, local fake upstream notes, platform constraints.
**What does NOT belong here:** Service commands/ports (use `.factory/services.yaml`).

---

- Automated validation must not require real provider API keys.
- Use sentinel fake keys in tests, for example `sk-test-redact-me` or `anthropic-test-redact-me`.
- Docker is not installed and must not be required.
- Resource profile: 8 CPU cores / 16 GiB RAM MacBook Air; full-stack/API validators run serially with max 1 active workflow.
- Preferred test command shape: `GOMAXPROCS=2 go test -p=1 ./...`.
