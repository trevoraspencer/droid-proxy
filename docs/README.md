# droid-proxy documentation

Guides for installing, configuring, and running `droid-proxy` with [Factory Droid](https://factory.ai).

## Getting Started

1. Install from the GitHub release using the command in [README.md](../README.md#install).
2. Run `droid-proxy config` to create or update your config, store credentials, and sync Factory settings.
3. Run `droid-proxy setup --service` to install the per-user service, or use `droid-proxy start` for a manual background process.
4. Run `droid-proxy doctor` and the checks in [SMOKE.md](SMOKE.md) before using the model in Droid.

Manual config is also supported: copy [config.example.yaml](../config.example.yaml), add credentials through your environment or `.env.local`, and start the proxy with `droid-proxy start --config config.yaml`.

## Reference

| Document | Description |
|---|---|
| [../VISION.md](../VISION.md) | Project scope and compatibility priorities |
| [CLI.md](CLI.md) | Commands, flags, services, diagnostics, updater, and auth |
| [UPGRADE.md](UPGRADE.md) | Release upgrades, source installs, and service repair |
| [CONFIG.md](CONFIG.md) | YAML schema and env loading |
| [FACTORY.md](FACTORY.md) | Factory `settings.json` integration |
| [PROVIDERS.md](PROVIDERS.md) | Provider matrix, tiers, and OAuth summary |
| [OAUTH.md](OAUTH.md) | Codex/ChatGPT and xAI OAuth walkthrough |
| [SMOKE.md](SMOKE.md) | Curl checks and runtime validation |
| [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) | Direct dependency SPDX licenses |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | Development and contribution guide |
| [../CHANGELOG.md](../CHANGELOG.md) | Release history |

## Provider Examples

Each guide includes config, Factory settings, run commands, and a curl check.

| Provider | Guide | Tier |
|---|---|---|
| DeepSeek | [examples/deepseek.md](examples/deepseek.md) | T1 |
| OpenAI | [examples/openai.md](examples/openai.md) | T1 |
| Anthropic | [examples/anthropic.md](examples/anthropic.md) | T1 |
| xAI API key | [examples/xai.md](examples/xai.md) | T2 |
| Kimi | [examples/kimi.md](examples/kimi.md) | T2 |
| Groq | [examples/groq.md](examples/groq.md) | T2 |
| Fireworks | [examples/fireworks.md](examples/fireworks.md) | T2 |
| Z.AI | [examples/zai.md](examples/zai.md) | T2 |
| Xiaomi MiMo | [examples/mimo.md](examples/mimo.md) | T2 |
| Ollama | [examples/local-ollama.md](examples/local-ollama.md) | T2 |
| vLLM | [examples/local-vllm.md](examples/local-vllm.md) | T2 |
| Codex/ChatGPT OAuth | [examples/codex-oauth.md](examples/codex-oauth.md) | T1 OAuth |
| xAI OAuth | [examples/xai-oauth.md](examples/xai-oauth.md) | T1 OAuth |

Factory settings snippets are in [factory-settings/](factory-settings/).

## Contributor References

- [../SECURITY.md](../SECURITY.md) - vulnerability reporting
- [../CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) - community standards
- [../CONTRIBUTING.md](../CONTRIBUTING.md) - build, test, and PR workflow
- [../scripts/live-e2e/README.md](../scripts/live-e2e/README.md) - optional live provider validation
