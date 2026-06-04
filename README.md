# droid-proxy

A localhost HTTP proxy that lets [Factory Droid](https://factory.ai) use any
BYOK / custom model — Anthropic, OpenAI, DeepSeek, Xiaomi MiMo, xAI, Kimi, ZAI,
Groq, Fireworks, local Ollama or vLLM, custom OpenAI-compatible endpoints,
plus Codex/ChatGPT and xAI OAuth — from a single Go binary.

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
  are load-balanced across requests. Four selection strategies (`round-robin`,
  `fill-first`, `least-connections`, `random`), bounded failover on 429/5xx/
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
git clone <this-repo> droid-proxy && cd droid-proxy
go build -o droid-proxy ./cmd/droid-proxy
```

Requires Go 1.26.4 or newer in the Go 1.26 line. The build produces a single static binary.

To run `droid-proxy` commands from any directory, put the built binary on your
shell `PATH`. On macOS or Linux, a symlink in `~/.local/bin` keeps the source
checkout in place while making the command globally available:

```bash
cd /path/to/droid-proxy
mkdir -p ~/.local/bin
ln -sf "$PWD/droid-proxy" ~/.local/bin/droid-proxy
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
droid-proxy status
```

Replace `/path/to/droid-proxy` with the source checkout where you ran
`go build`; for example, `/Users/trevor/code/droid-proxy`. After this, use
`droid-proxy start`, `droid-proxy status`, `droid-proxy restart`,
`droid-proxy config`, and
`droid-proxy update` from any working directory. The `~/.droid-proxy/` directory
is only for runtime state, logs, saved auth tokens, and managed env files; it
does not contain the executable. When there is no config file in the current
directory, commands such as `droid-proxy config` fall back to the config path
recorded by the running proxy.

To update a source install later:

```bash
./droid-proxy update --dry-run
./droid-proxy update
```

The updater fetches `origin/main` from GitHub, refuses to touch dirty or locally
ahead checkouts, rebuilds the binary, and restarts a running proxy.

## Quickstart: interactive setup

The easiest way to onboard a provider and model is the interactive dashboard:

```bash
./droid-proxy config
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
./droid-proxy start --config config.yaml
./droid-proxy stop
./droid-proxy logs
```

For auto-start on login (macOS), install the launchd user agent. Full command
reference: [`docs/CLI.md`](docs/CLI.md).

## Documentation

Complete guides: **[docs/README.md](docs/README.md)**

| Topic | Document |
|-------|----------|
| CLI & daemon | [docs/CLI.md](docs/CLI.md) |
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
currently rejects Factory's top-level effort, while `grok-4.3` uses
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
    display_name: "GPT 5.5 (ChatGPT Pro)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.5

  - alias: gpt-5.5-chatgpt-fast
    display_name: "GPT 5.5 Fast Mode (ChatGPT Pro)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.5
    # Add provider-specific extra_args here only after verifying the exact
    # fast-mode field for this upstream.
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
  (`round-robin` (default), `fill-first`, `least-connections`, `random`),
  `max_failovers`, `rate_limit_cooldown`, `error_cooldown`.
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

**Where is my data?**

| Data | Location | Notes |
|------|----------|-------|
| Reasoning cache | In-memory | Lost on restart |
| Daemon / launchd logs | `~/.droid-proxy/stdout.log`, `stderr.log` | Written by `start` and `service install` |
| OAuth tokens | `~/.droid-proxy/auth/*.json` | From `auth codex` / `auth xai` |
| Foreground logs | stderr | Unless you redirect manually |
| Upstream API keys | Your env / `.env.local`, or `~/.droid-proxy/env` | Only written to disk (chmod 600) when you save them via `droid-proxy config` |

## Development

```bash
make build      # build the binary
make test       # run unit + integration tests
make test-race  # tests with the race detector
make lint       # gofmt + go vet
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

**Contributors:** live validation harness in [docs/LIVE_E2E_PLAN.md](docs/LIVE_E2E_PLAN.md).

## License

MIT — see [`LICENSE`](LICENSE).
