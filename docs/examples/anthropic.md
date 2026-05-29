# Anthropic

Anthropic's `/v1/messages` is supported with native streaming and count_tokens.
The proxy automatically decompresses gzipped responses (Anthropic's LB sometimes
strips the `Content-Encoding` header).

## config.yaml

```yaml
models:
  - alias: claude-sonnet-4-5-20250929
    display_name: "Claude Sonnet 4.5 (Anthropic)"
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    known_auth: anthropic
    upstream_model: claude-sonnet-4-5-20250929
    max_context_tokens: 200000
```

`known_auth: anthropic` injects the required `anthropic-version: 2023-06-01`
header and uses `x-api-key` instead of `Authorization`.

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "claude-sonnet-4-5-20250929",
      "displayName": "Claude Sonnet 4.5 (Anthropic)",
      "provider": "anthropic",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 8192
    }
  ]
}
```

## Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
droid-proxy --config config.yaml
```

## Pass-through of custom Anthropic headers

The proxy forwards `anthropic-version` and `anthropic-beta` headers from the
client when set, so opt-in features that Droid sends arrive at Anthropic intact.
