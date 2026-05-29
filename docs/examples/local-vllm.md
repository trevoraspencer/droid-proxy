# Local vLLM

vLLM's `--api-server` provides OpenAI-compatible endpoints. Default port 8000.

## config.yaml

```yaml
models:
  - alias: droid-mistral-vllm
    display_name: "Mistral 7B (vLLM)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: vllm
    upstream_model: mistralai/Mistral-7B-Instruct-v0.3
```

## ~/.factory/settings.json

```json
{
  "customModels": [
    {
      "model": "droid-mistral-vllm",
      "modelDisplayName": "Mistral (vLLM)",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxTokens": 4096
    }
  ]
}
```

## Run

```bash
python -m vllm.entrypoints.openai.api_server \
  --model mistralai/Mistral-7B-Instruct-v0.3

droid-proxy --config config.yaml
```

`known_auth: vllm` assumes the local vLLM server does not require an API key and
therefore sends no upstream auth header. If you start vLLM with `--api-key`,
add `api_key_env: VLLM_API_KEY` to the model and export that value before
starting the proxy.
