# Local Ollama

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Local models via [Ollama](https://ollama.com/) |

Ollama exposes an OpenAI-compatible endpoint at `http://127.0.0.1:11434/v1`.
Any model you've pulled with `ollama pull <name>` can be wired up.

## Prerequisites

- Ollama installed and running (`ollama serve`)
- Model pulled (e.g. `ollama pull llama3:8b`)
- No API key required

> Alternatively, run `./droid-proxy config` and pick Ollama from the provider
> list — see [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: llama3
    display_name: "Llama 3 8B (Ollama local)"
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
      "model": "llama3",
      "displayName": "Llama 3 8B (Ollama local)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 4096
    }
  ]
}
```

## Run

```bash
ollama serve &   # if not already running
./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "llama3",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- `known_auth: ollama` sends no upstream `Authorization` header.
- Tool calling quality varies by model; set `capabilities.tools: false` if agents
  misbehave.
- Override `base_url` if Ollama listens on a non-default host or port.
