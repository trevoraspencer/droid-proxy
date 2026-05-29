# Local vLLM

vLLM's `--api-server` provides OpenAI-compatible endpoints. Default port 8000.

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

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "meta-llama/Llama-3.1-8B-Instruct",
      "displayName": "Llama 3.1 8B (vLLM)",
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
python -m vllm.entrypoints.openai.api_server \
  --model meta-llama/Llama-3.1-8B-Instruct

droid-proxy --config config.yaml
```

`known_auth: vllm` assumes the local vLLM server does not require an API key and
therefore sends no upstream auth header. If you start vLLM with `--api-key`,
add `api_key_env: VLLM_API_KEY` to the model and export that value before
starting the proxy.
