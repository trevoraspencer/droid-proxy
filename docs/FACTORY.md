# Factory Droid integration

Factory Droid talks to `droid-proxy` over localhost HTTP. You configure two
things: the proxy's `config.yaml` (which upstream models exist) and Droid's
`~/.factory/settings.json` (which models Droid shows in the UI).

## How provider modes map to endpoints

| Droid `provider` | Proxy endpoint | When to use |
|------------------|----------------|-------------|
| `generic-chat-completion-api` | `POST /v1/chat/completions` | OpenAI-compatible chat APIs (DeepSeek, MiMo, Groq, local Ollama, etc.) |
| `openai` | `POST /v1/responses` | OpenAI Responses API, Codex OAuth, xAI Grok OAuth |
| `anthropic` | `POST /v1/messages`, `POST /v1/messages/count_tokens` | Anthropic Messages API |

The model alias in Factory settings must match the `alias` in `config.yaml`.
Droid sends that string as the `model` field on each request.

> **Tip:** `droid-proxy config` writes these `customModels` entries for you.
> Add a model in the dashboard, then press `s` (selected) or `S` (all) to sync
> into `~/.factory/settings.json` — no hand-editing required. The manual schema
> below still applies if you prefer to edit the file yourself.

## Required `settings.json` fields

Each entry in `customModels` needs these fields (current Factory schema):

| Field | Description |
|-------|-------------|
| `model` | Alias from `config.yaml` (e.g. `deepseek-v4-flash`) |
| `displayName` | Label shown in Droid's model picker |
| `provider` | One of `generic-chat-completion-api`, `openai`, or `anthropic` |
| `baseUrl` | Proxy URL, typically `http://127.0.0.1:8787` |
| `apiKey` | Placeholder when proxy `client_auth` is off (see below) |
| `maxOutputTokens` | Max tokens Droid may request |

Example:

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

Ready-to-paste snippets for common providers:

- [Generic chat (DeepSeek)](factory-settings/generic.json)
- [OpenAI mode](factory-settings/openai.json)
- [Anthropic mode](factory-settings/anthropic.json)
- [Codex OAuth](factory-settings/codex-oauth.json)
- [xAI Grok OAuth](factory-settings/xai-oauth.json)

Per-provider walkthroughs with full config blocks are in
[examples/](examples/).

## The `apiKey` placeholder

By default `client_auth.enabled` is `false`, so the proxy does not validate
Droid's `apiKey`. Use any non-empty placeholder such as `"x"`.

If you enable client auth on the proxy:

```yaml
client_auth:
  enabled: true
  api_keys:
    - "${DROID_PROXY_API_KEY}"
```

Set the same value in Factory's `apiKey` field (or send it via Droid's
configured auth mechanism).

## Choosing the right `provider`

Match Factory's `provider` to the model's `factory_provider` in
`config.yaml`:

```yaml
models:
  - alias: deepseek-v4-flash
    factory_provider: generic-chat-completion-api   # → provider: generic-chat-completion-api
  - alias: gpt-4o
    factory_provider: openai                        # → provider: openai
  - alias: claude-sonnet-4-5-20250929
    factory_provider: anthropic                     # → provider: anthropic
```

A mismatch causes Droid to hit the wrong endpoint and requests will fail.

## Checking agent readiness

Before relying on a model for tool-using agent workflows, confirm it is
agent-ready:

```bash
curl -s http://127.0.0.1:8787/v1/models | jq '.data[] | {id, agent_ready, capabilities}'
```

`agent_ready: true` means streaming, tools, and tool results are validated for
that model's tier. See [PROVIDERS.md](PROVIDERS.md) for tier definitions.

## Typical setup flow

1. Copy and edit `config.yaml` — add model entries for providers you use.
2. Export API keys (or use `.env.local` — see [CLI.md](CLI.md)).
3. Start the proxy: `./droid-proxy start --config config.yaml`
4. Merge entries into `~/.factory/settings.json`.
5. Restart Droid or refresh settings; pick the model in the UI.
6. Run [SMOKE.md](SMOKE.md) checks if anything fails.

## See also

- [Configuration reference](CONFIG.md)
- [Supported providers](PROVIDERS.md)
- [CLI reference](CLI.md)
