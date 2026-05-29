# droid-proxy

A localhost HTTP proxy that lets [Factory Droid](https://factory.ai) use any
BYOK / custom model — Anthropic, OpenAI, DeepSeek, Xiaomi MiMo, xAI, Kimi, ZAI,
Groq, Fireworks, local Ollama or vLLM, custom OpenAI-compatible endpoints,
plus Codex/ChatGPT and xAI Grok Build OAuth — from a single Go binary.

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
  Grok Build accounts, with tokens stored locally under `~/.droid-proxy/auth`.

## Endpoints

| Method | Path | Used by Droid `provider` mode |
|---|---|---|
| `GET` | `/health`, `/healthz` | (any) |
| `GET` | `/v1/models` | (informational) |
| `POST` | `/v1/chat/completions` | `generic-chat-completion-api`, `openai` |
| `POST` | `/v1/responses` | `openai` |
| `POST` | `/v1/messages` | `anthropic` |
| `POST` | `/v1/messages/count_tokens` | `anthropic` |

The `/v1` prefix is optional on every non-health route.

## Install

```bash
git clone <this-repo> droid-proxy && cd droid-proxy
go build -o droid-proxy ./cmd/droid-proxy
```

Requires Go 1.26.3 or newer in the Go 1.26 line. The build produces a single static binary.

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
         "maxOutputTokens": 8192
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
- **OAuth Responses** for Codex/ChatGPT and xAI Grok Build
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

## Configuration

Full schema: [`docs/CONFIG.md`](docs/CONFIG.md). Highlights:

- `${VAR}` and `${VAR:-default}` env expansion in all string values.
- `client_auth` to require Droid to authenticate to the proxy (off by default).
- `reasoning_cache` for DeepSeek-style reasoning replay.
- `oauth.auth_dir` for Codex/xAI token storage.
- Per-model `capabilities` drive the `agent_ready` flag.

## Troubleshooting

**`config error: model "X": env var Y is empty`**

Export the model's API key before starting, or use `${Y:-fallback}` in config.
Load keys from `.env.local` — see [docs/CLI.md](docs/CLI.md).

**Translated Responses or Messages calls fail with a local `400`**

T3 translation rejects stateful or multimodal inputs that cannot be mapped
safely to Chat Completions. Check `error.message`, remove the unsupported
field, or use a native upstream protocol.

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
| Upstream API keys | Your env / `.env.local` | Never written to disk by the proxy |

## Development

```bash
make build      # build the binary
make test       # run unit + integration tests
make test-race  # tests with the race detector
make lint       # gofmt + go vet
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
