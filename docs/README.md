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

Maintainer validation harness (not required for normal use):

- [PUBLIC_RELEASE.md](PUBLIC_RELEASE.md) — Phase 0 strategy and orphan-branch procedure for going public
- [LIVE_E2E_PLAN.md](LIVE_E2E_PLAN.md) — live end-to-end test plan
- [live-e2e/DONE.md](live-e2e/DONE.md) — manual steps after scaffold

## Internal / historical

[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) is an archived build plan for
agents — not authoritative user documentation.
