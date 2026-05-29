# Local Ollama

Ollama exposes an OpenAI-compatible endpoint at `http://127.0.0.1:11434/v1`.
Any model you've pulled with `ollama pull <name>` can be wired up.

## config.yaml

```yaml
models:
  - alias: droid-llama3
    display_name: "Llama3 8B (local)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: ollama
    upstream_model: llama3:8b
    capabilities:
      # Ollama's tool-calling for non-instruct models is unreliable.
      # Mark off if you hit issues.
      tools: true
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "droid-llama3",
      "modelDisplayName": "Llama3 (local)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxTokens": 4096
    }
  ]
}
```

## Run

Ollama doesn't require an API key, and `known_auth: ollama` tells the proxy not
to send an upstream `Authorization` header by default:

```bash
ollama serve &  # if not already running
droid-proxy --config config.yaml
```
