# OpenAI

OpenAI's `/v1/responses` is supported natively. Droid sends Responses-style
calls when configured in `openai` mode.

## config.yaml

```yaml
models:
  - alias: gpt-4o
    display_name: "GPT-4o (OpenAI)"
    factory_provider: openai
    upstream_protocol: openai-responses
    known_auth: openai
    upstream_model: gpt-4o
    max_context_tokens: 128000
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "gpt-4o",
      "displayName": "GPT-4o (OpenAI)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 16000
    }
  ]
}
```

## Run

```bash
export OPENAI_API_KEY=sk-...
droid-proxy --config config.yaml
```
