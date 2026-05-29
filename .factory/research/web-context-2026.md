# 2026 Web Context Research

Raw planning research summary for the droid-proxy improvement mission.

## Sources Consulted

- Factory BYOK overview: https://docs.factory.ai/cli/byok/overview.md
- Factory OpenAI & Anthropic BYOK: https://docs.factory.ai/cli/byok/openai-anthropic.md
- OpenAI streaming Responses guide: https://developers.openai.com/api/docs/guides/streaming-responses.md
- OpenAI function calling guide: https://developers.openai.com/api/docs/guides/function-calling.md
- OpenAI migrate to Responses guide: https://developers.openai.com/api/docs/guides/migrate-to-responses.md
- Anthropic Messages API: https://docs.anthropic.com/en/api/messages.md
- Anthropic Messages streaming: https://docs.anthropic.com/en/api/messages-streaming.md
- Anthropic count_tokens: https://docs.anthropic.com/en/api/messages-count-tokens.md
- Anthropic tool use: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/handle-tool-calls.md
- Anthropic errors: https://docs.anthropic.com/en/api/errors.md
- DeepSeek API docs and thinking mode: https://api-docs.deepseek.com/
- Ollama OpenAI compatibility: https://docs.ollama.com/api/openai-compatibility.md
- vLLM OpenAI-compatible server: https://docs.vllm.ai/en/stable/serving/openai_compatible_server/
- Go `net/http` and `httputil.ReverseProxy` docs: https://pkg.go.dev/net/http and https://pkg.go.dev/net/http/httputil#ReverseProxy
- Go security best practices: https://go.dev/doc/security/best-practices

## Key Findings

- Factory custom models use camelCase settings fields and support `anthropic`, `openai`, and `generic-chat-completion-api` providers.
- OpenAI `provider: openai` should be treated as Responses API; Chat and Responses tool/stream shapes differ enough to require real translation.
- Anthropic Messages streaming has strict event sequencing and tool-result ordering rules.
- DeepSeek reasoning/tool workflows require replaying reasoning content on follow-up tool turns; newer 2026 model names should be documented with legacy alias context.
- Local Ollama/vLLM OpenAI-compatible providers should be fake-upstream validated locally and must not require Docker or real model servers for this mission.
- Go HTTP hardening should use bounded reads, server timeouts, careful header filtering, cancellation propagation, and redacted logs.
