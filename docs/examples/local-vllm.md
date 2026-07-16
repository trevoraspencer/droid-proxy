# Local vLLM

## Overview

| | |
|---|---|
| **Tier** | T2 — OpenAI-compatible chat |
| **Factory mode** | `generic-chat-completion-api` |
| **Upstream protocol** | `openai-chat` |
| **When to use** | Self-hosted models via [vLLM](https://docs.vllm.ai/) |

vLLM's OpenAI-compatible API server defaults to port 8000.

## Prerequisites

- vLLM running with `--api-server` (or equivalent)
- No API key by default; optional if you start vLLM with `--api-key`

> Alternatively, run `./droid-proxy config` and pick vLLM from the provider
> list — see [CLI.md](../CLI.md#interactive-config-dashboard).

## config.yaml

```yaml
models:
  - alias: meta-llama/Llama-3.1-8B-Instruct
    display_name: "Llama 3.1 8B (vLLM)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: vllm
    upstream_model: meta-llama/Llama-3.1-8B-Instruct
```

If vLLM requires an API key:

```yaml
    known_auth: vllm
    api_key_env: VLLM_API_KEY
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "meta-llama/Llama-3.1-8B-Instruct",
      "displayName": "Llama 3.1 8B (vLLM)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:9787",
      "apiKey": "x",
      "maxOutputTokens": 4096
    }
  ]
}
```

## Run

```bash
python -m vllm.entrypoints.openai.api_server \
  --model meta-llama/Llama-3.1-8B-Instruct

./droid-proxy start --config config.yaml
./droid-proxy status
```

## Verify

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }' | jq -r '.choices[0].message.content'
```

## Notes

- `known_auth: vllm` assumes `http://127.0.0.1:8000/v1` with no auth.
- Override `base_url` for remote or custom-port vLLM deployments.
