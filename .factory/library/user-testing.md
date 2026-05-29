# User Testing

Testing surface, resource cost classification, and validation execution guidance.

---

## Validation Surface

Surface: local HTTP/API only. Use Go tests, `httptest.Server`, loopback fake upstreams, and occasional curl-style runtime smoke. No browser, Docker, real provider, Ollama, vLLM, or local model server is required.

Required endpoints:
- `GET /health`, `GET /healthz`, `HEAD /healthz`
- `GET /v1/models`, `GET /models`
- `POST /v1/chat/completions`, `POST /chat/completions`
- `POST /v1/responses`, `POST /responses`
- `POST /v1/messages`, `POST /messages`
- `POST /v1/messages/count_tokens`, `POST /messages/count_tokens`

## Validation Concurrency

Resource profile: 8 CPU cores / 16 GiB RAM MacBook Air.

Max concurrent full-stack/API validators: **1**. Run workflow validation serially. Do not run race validation concurrently with runtime smoke.

Recommended commands:
- `GOMAXPROCS=2 go test -p=1 ./...`
- `GOMAXPROCS=2 go test -race -p=1 ./...`

## Dry Run Findings

Planning dry run built a temporary binary, started the proxy with `config.example.yaml`, and verified `/health`, `/healthz`, `/v1/models`, and `/models` returned HTTP 200. The proxy was stopped and no listener remained on port `8787`. No real provider keys were used.

## Flow Validator Guidance: local HTTP/API

- Use only the local HTTP/API surface exercised through Go handler/translator tests, `httptest.Server`, or loopback fake upstreams. Do not call real provider hosts or require real API keys.
- Run validators serially for this surface (`max concurrency: 1`) with conservative settings such as `GOMAXPROCS=2 go test -p=1 ...`.
- Prefer targeted protocol-translation tests in `internal/handlers`, `internal/translate`, `internal/stream`, and `internal/tokens`; capture command output and map each tested validation assertion to concrete test evidence.
- Stay within the repository working tree and mission evidence/report paths. Do not start persistent services unless a runtime smoke specifically requires it, and clean up any listener/process before returning.
- SSE assertions must parse events frame-by-frame and verify terminal markers; substring-only stream checks are not sufficient for protocol-translation validation evidence.
