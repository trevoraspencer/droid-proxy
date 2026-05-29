# DeepSeek

DeepSeek's chat API speaks OpenAI Chat Completions and returns `reasoning_content`
deltas when the model thinks. droid-proxy captures these automatically so
follow-up turns with tool results carry the prior reasoning forward — required
by DeepSeek to keep tool-using conversations coherent.

The examples below use the current 2026 `deepseek-v4-flash` naming. Older
aliases such as `deepseek-chat`, `deepseek-reasoner`, and example proxy aliases
like `droid-deepseek-v3` may still work for existing configs, but treat them as
legacy compatibility names rather than new defaults.

## config.yaml

```yaml
models:
  - alias: droid-deepseek-v4-flash
    display_name: "DeepSeek V4 Flash"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    upstream_model: deepseek-v4-flash
    max_output_tokens: 8192
    max_context_tokens: 64000
    capabilities:
      reasoning: deepseek
```

`known_auth: deepseek` fills in:

- `base_url: https://api.deepseek.com/v1`
- `api_key_env: DEEPSEEK_API_KEY`

## ~/.factory/settings.json

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

## Run

```bash
export DEEPSEEK_API_KEY=sk-...
droid-proxy --config config.yaml
```

## Curl smoke test

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "droid-deepseek-v4-flash",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq .
```
