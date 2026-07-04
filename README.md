# droid-proxy

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A localhost HTTP proxy that lets [Factory Droid](https://factory.ai) use any
BYOK / custom model — Anthropic, OpenAI, DeepSeek, Xiaomi MiMo, xAI, Kimi, ZAI,
Groq, Fireworks, local Ollama or vLLM, custom OpenAI-compatible endpoints,
plus Codex/ChatGPT and xAI OAuth — from a single Go binary.

**Status:** Beta — actively developed pre-`v0.1.0` public release. Expect config
and provider behavior to evolve; pin a release tag once published.

Project scope and contributor priorities are defined in
[`VISION.md`](VISION.md), the canonical source of truth for what this repo is
and is not trying to become.

## What this is

- A **local bridge** between Factory Droid and upstream model APIs you configure.
- A **single Go binary** with no hosted service component — you run it on your machine.
- A **BYOK/OAuth tool** — your API keys and OAuth tokens stay in local files under
  `~/.droid-proxy/`, not on a shared server.

## What this is not

- **Not** a hosted proxy, API reseller, or model provider.
- **Not** affiliated with Factory AI or any upstream provider (see Disclaimer).
- **Not** designed for exposure to the public internet without extra access controls.

## Security model

- **Listen address defaults to `127.0.0.1`** — only local processes can reach the proxy.
- **Upstream credentials** live in your environment, `.env.local`, or
  `~/.droid-proxy/env` (written by `droid-proxy config`, mode `0600`).
- **OAuth tokens** are stored as JSON under `~/.droid-proxy/auth/` (mode `600`).
- **Logs redact secrets by default** (`logging.redact: true` in config).
- Optional `client_auth` can require Droid to present a proxy API key — off by default.

Report vulnerabilities privately: [SECURITY.md](SECURITY.md).

- **Localhost-first.** Examples use `http://127.0.0.1:8787`. No tunneling
  required unless you specifically want remote access.
- **All three Droid provider modes.** `anthropic`, `openai`, and
  `generic-chat-completion-api` are first-class.
- **Honest about what works.** Each model is tier-classified and an
  `agent_ready` flag in `/v1/models` tells you whether tool-using workflows
  are validated end to end.
- **Reasoning replay.** DeepSeek-style `reasoning_content` is captured and
  re-supplied on subsequent turns so multi-step tool conversations stay
  coherent.
- **Focused OAuth.** Browser PKCE login is available for Codex/ChatGPT and xAI
  accounts, with tokens stored locally under `~/.droid-proxy/auth`.
  Manage accounts with `auth status`, `auth enable`/`auth disable`, and
  `auth logout`, and check per-model OAuth health via `oauth_auth` in
  `/v1/models`.
- **Codex OAuth multi-account pooling.** Multiple Codex/ChatGPT OAuth accounts
  are load-balanced across requests. Selection strategies include `sticky`
  (default, per-conversation affinity), `round-robin`,
  `fill-first`, `least-connections`, `random`, bounded failover on 429/5xx/
  transport errors with cooldown, and 401/403 force-refresh replay before
  failover. Auth-dir watcher hot-reloads token files. xAI remains
  single-account. Single-account mode (no failover, no refresh+replay) is
  preserved when only one account exists.

## Endpoints

| Method | Path | Used by Droid `provider` mode |
|---|---|---|
| `GET` | `/health`, `/healthz` | (any) |
| `GET` | `/v1/models` | (informational) |
| `POST` | `/v1/chat/completions` | `generic-chat-completion-api`, `openai` |
| `POST` | `/v1/responses` | `openai` |
| `POST` | `/v1/messages` | `anthropic` |
| `POST` | `/v1/messages/count_tokens` | `anthropic` |
| `GET` | `/v1/oauth/pool-health`, `/oauth/pool-health` | Codex multi-account pool status (read-only) |

The `/v1` prefix is optional on every non-health route.

## Install

```bash
curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
```

The installer downloads the latest GitHub release for macOS or Linux, verifies
the checksum, installs the binary to `~/.local/bin/droid-proxy`, and seeds a
per-user config if one does not already exist. Existing configs and secrets are
preserved on re-run, so the same command is the release upgrade path.

Inspect-first install:

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
| Installed binary | `~/.local/bin/droid-proxy` | `~/.local/bin/droid-proxy` |
| Runtime config | `~/Library/Application Support/droid-proxy/config.yaml` | `${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml` |
| Service | `~/Library/LaunchAgents/com.droid-proxy.agent.plist` | `${XDG_CONFIG_HOME:-~/.config}/systemd/user/droid-proxy.service` |
| Runtime state, logs, managed env | `~/.droid-proxy/` | `~/.droid-proxy/` |

After install:

```bash
droid-proxy config
droid-proxy setup --service
droid-proxy doctor
```

For source builds and contributor workflows:

```bash
git clone https://github.com/trevoraspencer/droid-proxy.git
cd droid-proxy
make install-user
```

Requires Go 1.26.4 or newer in the Go 1.26 line. See
[docs/UPGRADE.md](docs/UPGRADE.md) before mixing release installs and source
checkout updates.

## Quickstart: interactive setup

The easiest way to onboard a provider and model is the interactive dashboard:

```bash
droid-proxy config
```

It picks a provider from the built-in registry (or a custom endpoint / OAuth),
prompts for the API key (stored in `~/.droid-proxy/env`, chmod 600), discovers
available models from the provider, writes your `config.yaml`, and syncs the
entry into Factory's `~/.factory/settings.json` — all without hand-editing
three files. Press `r` to restart the proxy when done. See
[`docs/CLI.md`](docs/CLI.md#interactive-config-dashboard).

The manual, file-based flow is below.

## Quickstart: DeepSeek via Droid

1. **Configure droid-proxy.**

   ```bash
   cp config.example.yaml config.yaml
   cp .env.local.example .env.local   # add DEEPSEEK_API_KEY=sk-...
   ```

   The default DeepSeek block in `config.yaml` looks like:

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

2. **Start the proxy.**

   ```bash
   set -a && source .env.local && set +a
   ./droid-proxy start --config config.yaml
   ./droid-proxy status
   ```

3. **Tell Droid about it.** Edit `~/.factory/settings.json`:

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

4. **Use it.** Pick the model in Droid and start a chat. See
   [docs/SMOKE.md](docs/SMOKE.md) to verify with curl first.

## Run the proxy

Use the background daemon so you do not need a terminal open while Droid runs:

```bash
droid-proxy start
droid-proxy stop
droid-proxy logs
```

For auto-start on login, install the per-user service:

```bash
droid-proxy setup --service
```

This writes a launchd user agent on macOS or a systemd user unit on Linux. Full
command reference: [`docs/CLI.md`](docs/CLI.md).

## Documentation

Complete guides: **[docs/README.md](docs/README.md)**

| Topic | Document |
|-------|----------|
| Project vision and scope | [VISION.md](VISION.md) |
| CLI & daemon | [docs/CLI.md](docs/CLI.md) |
| Install, upgrade, repair | [docs/UPGRADE.md](docs/UPGRADE.md) |
| YAML schema | [docs/CONFIG.md](docs/CONFIG.md) |
| Factory Droid settings | [docs/FACTORY.md](docs/FACTORY.md) |
| Provider matrix | [docs/PROVIDERS.md](docs/PROVIDERS.md) |
| OAuth login | [docs/OAUTH.md](docs/OAUTH.md) |
| Verify setup | [docs/SMOKE.md](docs/SMOKE.md) |
| Per-provider examples | [docs/examples/](docs/examples/) |

## Provider coverage

Every supported provider with tier classification is in
[`docs/PROVIDERS.md`](docs/PROVIDERS.md). Highlights:

- **T1/T2 passthrough** for DeepSeek, MiMo, OpenAI Responses, Anthropic
  Messages, and OpenAI-compatible chat providers.
- **OAuth Responses** for Codex/ChatGPT and xAI
  (`droid-proxy auth codex` / `auth xai`).
- **T3 protocol translation** for OpenAI Responses-over-Chat and Anthropic
  Messages-over-Chat on OpenAI-compatible upstreams.

Example walkthroughs: [DeepSeek](docs/examples/deepseek.md),
[OpenAI](docs/examples/openai.md), [Anthropic](docs/examples/anthropic.md),
[MiMo](docs/examples/mimo.md), [Ollama](docs/examples/local-ollama.md),
[vLLM](docs/examples/local-vllm.md), [xAI](docs/examples/xai.md),
[Kimi](docs/examples/kimi.md), [Groq](docs/examples/groq.md),
[Fireworks](docs/examples/fireworks.md), [Z.AI](docs/examples/zai.md),
[Codex OAuth](docs/examples/codex-oauth.md), [xAI OAuth](docs/examples/xai-oauth.md).

## Reasoning and fast-mode models

Factory Droid should control per-request reasoning levels when the selected
upstream supports that request field. For `codex-responses`, `droid-proxy`
passes the request's `reasoning` object through to the Codex/ChatGPT upstream.
For `xai-responses`, reasoning passthrough is model-specific:
`grok-build-0.1` uses `capabilities.factory_reasoning: drop` because Grok Build
currently rejects Factory's top-level effort, `grok-composer-2.5-fast` also
uses `drop` through the Grok CLI OAuth endpoint, while `grok-4.3` uses
`capabilities.factory_reasoning: passthrough`. For DeepSeek-style OpenAI Chat
providers, `capabilities.reasoning: deepseek` enables reasoning replay across
tool turns; it is separate from Factory's UI reasoning selector. For DeepSeek
and MiMo `known_auth` profiles, droid-proxy also sends provider-specific
thinking-on request fields so these models do not depend on Factory's generic
custom-model reasoning badge.
Provider-specific details are documented in [`docs/OAUTH.md`](docs/OAUTH.md).

Use separate aliases when a provider exposes a distinct fast/speed mode, so the
Factory model picker feels native:

```yaml
models:
  - alias: gpt-5.5-chatgpt
    display_name: "GPT 5.5 (ChatGPT)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.5

  - alias: gpt-5.5-chatgpt-fast
    display_name: "GPT 5.5 Fast (ChatGPT)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.5
    # "Fast" is the display label; the verified Codex upstream value is
    # service_tier: priority. The loader also accepts service_tier: fast and
    # normalizes it to priority. The completed response may still echo
    # service_tier: "default", so do not use the response echo as proof that
    # the request value was ignored.
    extra_args:
      service_tier: priority
```

After adding or syncing a model, restart the proxy (`r` in the TUI or
`droid-proxy restart`) so the running daemon reloads `config.yaml`.

## Configuration

Full schema: [`docs/CONFIG.md`](docs/CONFIG.md). Highlights:

- `${VAR}` and `${VAR:-default}` env expansion in all string values.
- `client_auth` to require Droid to authenticate to the proxy (off by default).
- `reasoning_cache` for DeepSeek-style reasoning replay.
- `oauth.auth_dir` for Codex/xAI token storage.
- `oauth.load_balancing` for Codex multi-account pooling: `strategy`
  (`sticky` (default), `round-robin`, `fill-first`, `least-connections`, `random`),
  `max_failovers`, `rate_limit_cooldown`, `error_cooldown`. A 429 cooldown uses
  `Retry-After`, then exhausted-window reset evidence, then this fallback.
- Per-model `capabilities` drive the `agent_ready` flag.

## Troubleshooting

**`config error: model "X": env var Y is empty`**

Set the key with `./droid-proxy config` (stored in `~/.droid-proxy/env`), export
it manually, or use `${Y:-fallback}` in config. Keys load in layers: managed
`~/.droid-proxy/env` first, then `.env.local` if present — see
[docs/CLI.md](docs/CLI.md#config-and-env-file-resolution).

**Translated Responses or Messages calls fail with a local `400`**

T3 translation rejects stateful or multimodal inputs that cannot be mapped
safely to Chat Completions. Check `error.message`, remove the unsupported
field, or use a native upstream protocol.

**Factory shows a model, but the proxy says it is not configured**

Factory settings were synced, but the running proxy has not reloaded the config
yet. Press `r` in `droid-proxy config` or run `droid-proxy restart`, then retry.

**Droid says model is offline**

```bash
./droid-proxy status
curl -s http://127.0.0.1:8787/health
curl -s http://127.0.0.1:8787/v1/models | jq '.data[].id'
```

Confirm `baseUrl` in Factory settings matches the proxy listen address.

## FAQ

**Can I run this on a remote server?**

The defaults assume localhost. You can bind another host in `config.yaml`, but
you are responsible for firewalls, TLS, and `client_auth` if the port is reachable
beyond your machine.

**Do I need to edit three files for every provider?**

No. Run `./droid-proxy config` — it writes `config.yaml`, stores keys in
`~/.droid-proxy/env`, and syncs Factory settings in one flow.

**Which provider should I start with?**

DeepSeek via the manual quickstart below, or any guide in
[docs/examples/](docs/examples/). Use [docs/SMOKE.md](docs/SMOKE.md) to verify
with curl before opening Droid.

**How do I add a second Codex/ChatGPT account?**

Run `droid-proxy auth codex` again. Multiple accounts pool automatically — see
[docs/OAUTH.md](docs/OAUTH.md) and `oauth.load_balancing` in
[docs/CONFIG.md](docs/CONFIG.md).

**Where is my data?**

| Data | Location | Notes |
|------|----------|-------|
| Reasoning cache | In-memory | Lost on restart |
| Daemon / service logs | `~/.droid-proxy/stdout.log`, `stderr.log` | Written by `start` and user services |
| OAuth tokens | `~/.droid-proxy/auth/*.json` | From `auth codex` / `auth xai` |
| Foreground logs | stderr | Unless you redirect manually |
| Upstream API keys | Your env / `.env.local`, or `~/.droid-proxy/env` | Only written to disk (chmod 600) when you save them via `droid-proxy config` |

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor guide. Recent
changes are listed in [CHANGELOG.md](CHANGELOG.md).

```bash
make build      # build the binary
make test       # run unit + integration tests
make test-race  # tests with the race detector
make lint       # gofmt + go vet
make docs-audit # documentation consistency checks
```

OAuth pool and handler tests:

```bash
go test -race ./internal/oauth/... ./internal/handlers/...
```

On low-resource laptops:

```bash
GOMAXPROCS=2 go test -p=1 ./...
GOMAXPROCS=2 go test -race -p=1 ./...
GOMAXPROCS=2 go vet ./... && test -z "$(gofmt -l .)"
```

Workflow validation uses local fake upstreams and does not require provider API keys.

**Contributors:** optional live validation harness in [scripts/live-e2e/README.md](scripts/live-e2e/README.md).

## Disclaimer

droid-proxy is an independent open-source project. It is **not affiliated with,
endorsed by, or officially supported by** [Factory AI](https://factory.ai) or any
model provider named in the documentation. Factory Droid and provider names are
trademarks of their respective owners.

## License

MIT — see [`LICENSE`](LICENSE). Third-party component licenses are listed in
[`docs/THIRD_PARTY_LICENSES.md`](docs/THIRD_PARTY_LICENSES.md).
