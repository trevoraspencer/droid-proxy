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

1. **Configure droid-proxy.** Copy `config.example.yaml` to `config.yaml`.
   The default DeepSeek block is already active and looks like:

   ```yaml
   listen:
     host: 127.0.0.1
     port: 8787

   models:
     - alias: droid-deepseek-v4-flash
       display_name: "DeepSeek V4 Flash"
       factory_provider: generic-chat-completion-api
       upstream_protocol: openai-chat
       known_auth: deepseek
       upstream_model: deepseek-v4-flash
       capabilities:
         reasoning: deepseek
   ```

2. **Run the proxy.**

   ```bash
   export DEEPSEEK_API_KEY=sk-...
   ./droid-proxy --config config.yaml
   ```

3. **Tell Droid about it.** Edit `~/.factory/settings.json`:

   ```json
   {
     "customModels": [
       {
         "model": "droid-deepseek-v4-flash",
         "modelDisplayName": "DeepSeek V4 Flash (via droid-proxy)",
         "provider": "generic-chat-completion-api",
         "baseUrl": "http://127.0.0.1:8787",
         "apiKey": "x",
         "maxTokens": 8192
       }
     ]
   }
   ```

4. **Use it.** Pick the model in Droid and start a chat. The proxy logs each
   request id so you can trace problems.

## More examples

- [DeepSeek](docs/examples/deepseek.md)
- [Xiaomi MiMo](docs/examples/mimo.md)
- [Local Ollama](docs/examples/local-ollama.md)
- [Local vLLM](docs/examples/local-vllm.md)
- [OpenAI](docs/examples/openai.md)
- [Anthropic](docs/examples/anthropic.md)

Ready-to-paste `settings.json` snippets:

- [Anthropic mode](docs/factory-settings/anthropic.json)
- [OpenAI mode](docs/factory-settings/openai.json)
- [Generic chat completions mode](docs/factory-settings/generic.json)

## Provider coverage

Every supported provider with tier classification is in
[`docs/PROVIDERS.md`](docs/PROVIDERS.md). The short version:

- **T1/T2 passthrough** for DeepSeek, Xiaomi MiMo, OpenAI (`/v1/responses`),
  Anthropic (`/v1/messages` + `count_tokens`), and OpenAI-compatible providers.
- **OAuth Responses support** for Codex/ChatGPT and xAI Grok Build through
  `droid-proxy auth codex --config config.yaml` or
  `droid-proxy auth xai --config config.yaml`.
- **T3 protocol translation** for OpenAI Responses-over-Chat and Anthropic
  Messages-over-Chat is implemented for text, streaming, tools, and tool
  results over OpenAI-compatible Chat upstreams.

## Configuration

Full schema is in [`docs/CONFIG.md`](docs/CONFIG.md). Highlights:

- All string values support `${VAR}` and `${VAR:-default}` env expansion.
- `client_auth: { enabled: true, api_keys: [...] }` to require Droid to
  authenticate to the proxy. Off by default since localhost is trusted.
- `reasoning_cache: { enabled: true, max_entries, ttl }` controls DeepSeek
  reasoning replay.
- `oauth.auth_dir` controls where Codex/ChatGPT and xAI OAuth tokens are
  stored.
- Per-model `capabilities` overrides (streaming, tools, tool_result_safe,
  json_mode, structured_output, reasoning, prompt_caching) drive the
  `agent_ready` flag.

## Troubleshooting

**`config error: model "X": env var Y is empty`**

The model's `api_key_env` is unset. `export Y=...` before running, or use
`${Y:-fallback}` syntax in your config.

**Translated Responses or Messages calls fail with a local `400`**

OpenAI Responses-over-Chat and Anthropic Messages-over-Chat are implemented,
but intentionally reject stateful or multimodal inputs that cannot be mapped
safely to Chat Completions. Check the response's `error.message`, remove the
unsupported field, or use a native upstream protocol if your provider supports
that endpoint directly.

**Droid says model is offline**

Confirm the proxy is healthy and Droid is pointed at it:

```bash
curl -s http://127.0.0.1:8787/health
curl -s http://127.0.0.1:8787/v1/models | jq '.data[].id'
```

**Where is my data?**

`droid-proxy` keeps no persistent state. The reasoning cache is in-memory and
disappears at restart. No logs are written to disk unless you redirect stderr
yourself.

## Development

```bash
make build      # build the binary
make test       # run unit + integration tests
make test-race  # tests with the race detector
make lint       # gofmt + go vet
```

On low-resource laptops, run the Go checks serially:

```bash
GOMAXPROCS=2 go test -p=1 ./...
GOMAXPROCS=2 go test -race -p=1 ./...
GOMAXPROCS=2 go vet ./... && test -z "$(gofmt -l .)"
```

Workflow validation uses local fake upstreams and does not require provider API
keys.

## License

MIT — see [`LICENSE`](LICENSE).
