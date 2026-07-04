# droid-proxy

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`droid-proxy` is a local HTTP proxy for [Factory Droid](https://factory.ai). It lets Droid use BYOK providers, local models, OpenAI-compatible endpoints, and supported OAuth accounts through one Go binary running on your machine.

The first public release is `v0.1.0`. The supported install and upgrade path is the GitHub release installer.

## What This Is

- A localhost bridge between Factory Droid and upstream model APIs you configure.
- A single Go binary with no hosted service component.
- A per-user install: binary in `~/.local/bin`, config in the OS user config directory, and runtime state under `~/.droid-proxy`.
- A BYOK/OAuth tool: provider API keys and OAuth tokens stay on your machine.

## What This Is Not

- Not a hosted proxy, API reseller, or model provider.
- Not affiliated with Factory AI or any upstream provider.
- Not designed for public-internet exposure without your own firewall, TLS, and authentication controls.

## Security Model

- The proxy listens on `127.0.0.1` by default.
- Upstream credentials load from your environment, `.env.local`, or `~/.droid-proxy/env`.
- `droid-proxy config` writes managed secrets with restrictive permissions.
- OAuth tokens are stored under `~/.droid-proxy/auth`.
- Logs redact credential-shaped fields by default.
- Optional `client_auth` can require Droid to send a proxy API key.

Report vulnerabilities privately through [SECURITY.md](SECURITY.md).

## Features

- Factory Droid provider modes: `anthropic`, `openai`, and `generic-chat-completion-api`.
- Health, model listing, Chat Completions, Responses, Anthropic Messages, and token-count endpoints.
- Curated provider profiles for Anthropic, OpenAI, DeepSeek, Xiaomi MiMo, xAI, Kimi, Z.AI, Groq, Fireworks, Ollama, and vLLM.
- OAuth login for Codex/ChatGPT and xAI accounts.
- Codex OAuth multi-account load balancing with sticky, round-robin, fill-first, least-connections, and random strategies.
- `agent_ready` model metadata so tool-using workflows are marked only when the proxy path is validated.
- DeepSeek-style reasoning replay across tool turns.
- Interactive `droid-proxy config` onboarding that writes config, stores keys, and syncs Factory settings.

## Endpoints

| Method | Path | Notes |
|---|---|---|
| `GET` | `/health`, `/healthz` | Health checks |
| `GET` | `/v1/models` | Configured models and readiness metadata |
| `POST` | `/v1/chat/completions` | OpenAI Chat-compatible requests |
| `POST` | `/v1/responses` | OpenAI Responses-compatible requests |
| `POST` | `/v1/messages` | Anthropic Messages-compatible requests |
| `POST` | `/v1/messages/count_tokens` | Anthropic token counting |
| `GET` | `/v1/oauth/pool-health`, `/oauth/pool-health` | Codex account pool status |

The `/v1` prefix is optional on non-health routes.

## Install

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
```

The installer downloads the latest macOS or Linux release archive, verifies `checksums.txt`, installs `droid-proxy` to `~/.local/bin/droid-proxy`, and creates a per-user config if one does not already exist. Re-running the command upgrades the binary and preserves existing config, OAuth tokens, logs, and managed secrets.

Inspect the script first if preferred:

```bash
curl -fsSLO https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh
sh install.sh
```

Add the per-user binary directory to your shell `PATH` if needed:

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Per-user runtime layout:

| Item | macOS | Linux |
|---|---|---|
| Binary | `~/.local/bin/droid-proxy` | `~/.local/bin/droid-proxy` |
| Config | `~/Library/Application Support/droid-proxy/config.yaml` | `${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml` |
| User service | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| State, logs, managed env | `~/.droid-proxy/` | `~/.droid-proxy/` |

After install:

```bash
droid-proxy config
droid-proxy setup --service
droid-proxy doctor
```

Source builds are for contributors:

```bash
git clone https://github.com/trevoraspencer/droid-proxy.git
cd droid-proxy
make install-user
```

Source builds require Go 1.26.4 or newer. See [docs/UPGRADE.md](docs/UPGRADE.md) for release upgrades, source installs, and service repair.

## Quickstart

Run the interactive setup:

```bash
droid-proxy config
```

The dashboard lets you choose a provider, enter or reuse credentials, discover models when the provider supports it, write `config.yaml`, and sync the selected model into Factory's `~/.factory/settings.json`.

Install the user service after at least one model is configured:

```bash
droid-proxy setup --service
droid-proxy doctor
```

Manual DeepSeek example:

```yaml
models:
  - alias: deepseek-v4-flash
    display_name: "DeepSeek V4 Flash (DeepSeek)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    upstream_model: deepseek-v4-flash
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
      reasoning_effort: high
```

Factory custom model entry:

```json
{
  "customModels": [
    {
      "model": "deepseek-v4-flash",
      "displayName": "DeepSeek V4 Flash (DeepSeek)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
    }
  ]
}
```

Verify with curl before opening Droid:

```bash
curl -s http://127.0.0.1:8787/health
curl -s http://127.0.0.1:8787/v1/models | jq '.data[] | {id, agent_ready}'
```

## Run The Proxy

Start, stop, restart, and inspect logs from the installed binary:

```bash
droid-proxy start
droid-proxy status
droid-proxy logs
droid-proxy restart
droid-proxy stop
```

Install or repair the per-user service:

```bash
droid-proxy setup --service
```

This writes a launchd user agent on macOS or a systemd user unit on Linux.

## Documentation

Complete docs start at [docs/README.md](docs/README.md).

| Topic | Document |
|---|---|
| Project scope | [VISION.md](VISION.md) |
| CLI and services | [docs/CLI.md](docs/CLI.md) |
| Install, upgrade, repair | [docs/UPGRADE.md](docs/UPGRADE.md) |
| Config schema | [docs/CONFIG.md](docs/CONFIG.md) |
| Factory settings | [docs/FACTORY.md](docs/FACTORY.md) |
| Provider matrix | [docs/PROVIDERS.md](docs/PROVIDERS.md) |
| OAuth login | [docs/OAUTH.md](docs/OAUTH.md) |
| Smoke tests | [docs/SMOKE.md](docs/SMOKE.md) |
| Examples | [docs/examples/](docs/examples/) |

Provider walkthroughs: [DeepSeek](docs/examples/deepseek.md), [OpenAI](docs/examples/openai.md), [Anthropic](docs/examples/anthropic.md), [MiMo](docs/examples/mimo.md), [Ollama](docs/examples/local-ollama.md), [vLLM](docs/examples/local-vllm.md), [xAI](docs/examples/xai.md), [Kimi](docs/examples/kimi.md), [Groq](docs/examples/groq.md), [Fireworks](docs/examples/fireworks.md), [Z.AI](docs/examples/zai.md), [Codex OAuth](docs/examples/codex-oauth.md), and [xAI OAuth](docs/examples/xai-oauth.md).

## Configuration Notes

- `${VAR}` and `${VAR:-default}` expansion is supported in string fields.
- `client_auth` protects the proxy from other local processes when enabled.
- `oauth.auth_dir` controls OAuth token storage.
- `oauth.load_balancing` controls Codex account selection: `sticky`, `round-robin`, `fill-first`, `least-connections`, or `random`.
- Per-model `capabilities` drive the `agent_ready` flag.

For `codex-responses`, the proxy passes Factory's `reasoning` object through to the upstream. For DeepSeek-style OpenAI Chat providers, `capabilities.reasoning: deepseek` enables reasoning replay across tool turns.

## Troubleshooting

**`config error: model "X": env var Y is empty`**

Run `droid-proxy config`, export the variable in your shell, or add it to `.env.local`. Runtime env files are layered from `~/.droid-proxy/env` and then `.env.local` when present.

**Factory shows a model, but the proxy says it is not configured**

Restart the proxy so it reloads `config.yaml`:

```bash
droid-proxy restart
```

**Droid says the model is offline**

```bash
droid-proxy status
curl -s http://127.0.0.1:8787/health
curl -s http://127.0.0.1:8787/v1/models | jq '.data[].id'
```

Confirm the Factory `baseUrl` matches the proxy listen address.

**A translated request returns a local `400`**

T3 translation rejects request shapes that cannot be mapped safely to the target upstream. Use the error message to remove the unsupported field or select a native upstream protocol.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for contributor setup. Release history is in [CHANGELOG.md](CHANGELOG.md).

```bash
make build
make test
make test-race
make lint
make docs-audit
make security-audit
```

## Disclaimer

droid-proxy is an independent open-source project. It is not affiliated with, endorsed by, or officially supported by [Factory AI](https://factory.ai), OpenAI, Anthropic, DeepSeek, xAI, Xiaomi, Moonshot AI, Z.AI, Groq, Fireworks, Ollama, vLLM, or any other provider named in the documentation. Factory Droid and provider names are trademarks of their respective owners.

## License

MIT - see [LICENSE](LICENSE). Third-party component licenses are listed in [docs/THIRD_PARTY_LICENSES.md](docs/THIRD_PARTY_LICENSES.md).
