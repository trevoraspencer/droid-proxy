# droid-proxy documentation

Guides for running `droid-proxy` with [Factory Droid](https://factory.ai).

## Getting started

**Interactive (preferred):**

1. Read the [README](../README.md) for install.
2. Run `./droid-proxy config` to onboard providers, store API keys, write
   `config.yaml`, and sync Factory settings — see
   [CLI reference](CLI.md#interactive-config-dashboard).
3. Start the proxy — see [CLI reference](CLI.md).

**Manual alternative:**

1. Read the [README](../README.md) for install and a DeepSeek quickstart.
2. Copy [`config.example.yaml`](../config.example.yaml) to `config.yaml`.
3. Copy [`.env.local.example`](../.env.local.example) to `.env.local` and add API keys.
4. Start the proxy — see [CLI reference](CLI.md).

## Reference

| Document | Description |
|----------|-------------|
| [CLI.md](CLI.md) | `config` dashboard, `start`, `stop`, `status`, `logs`, launchd `service`, `auth` (login + `status`/`enable`/`disable`/`logout`), env files |
| [CONFIG.md](CONFIG.md) | Full YAML schema |
| [FACTORY.md](FACTORY.md) | `~/.factory/settings.json` integration |
| [PROVIDERS.md](PROVIDERS.md) | Provider matrix, tiers, OAuth summary |
| [OAUTH.md](OAUTH.md) | Codex/ChatGPT and xAI OAuth walkthrough |
| [SMOKE.md](SMOKE.md) | Verify your setup with curl |
| [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) | Direct dependency SPDX licenses |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | How to build, test, and send PRs |
| [../CHANGELOG.md](../CHANGELOG.md) | Release history |

## Provider examples

Each guide includes `config.yaml`, Factory settings, run commands, and a curl check.

| Provider | Guide | Tier |
|----------|-------|------|
| DeepSeek | [examples/deepseek.md](examples/deepseek.md) | T1 |
| OpenAI | [examples/openai.md](examples/openai.md) | T1 |
| Anthropic | [examples/anthropic.md](examples/anthropic.md) | T1 |
| xAI (API key) | [examples/xai.md](examples/xai.md) | T2 |
| Kimi (Moonshot) | [examples/kimi.md](examples/kimi.md) | T2 |
| Groq | [examples/groq.md](examples/groq.md) | T2 |
| Fireworks | [examples/fireworks.md](examples/fireworks.md) | T2 |
| Z.AI | [examples/zai.md](examples/zai.md) | T2 |
| Xiaomi MiMo | [examples/mimo.md](examples/mimo.md) | T2 |
| Ollama (local) | [examples/local-ollama.md](examples/local-ollama.md) | T2 |
| vLLM (local) | [examples/local-vllm.md](examples/local-vllm.md) | T2 |
| Codex/ChatGPT OAuth | [examples/codex-oauth.md](examples/codex-oauth.md) | T1 OAuth |
| xAI OAuth | [examples/xai-oauth.md](examples/xai-oauth.md) | T1 OAuth |

Factory settings snippets: [`factory-settings/`](factory-settings/).

## Contributors only

Maintainer docs (not required for normal use):

- [PUBLIC_RELEASE.md](PUBLIC_RELEASE.md) — public release strategy and orphan-branch procedure
- [../SECURITY.md](../SECURITY.md) — security vulnerability reporting
- [../CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) — community standards
- [../scripts/live-e2e/README.md](../scripts/live-e2e/README.md) — optional live E2E validation against real credentials
