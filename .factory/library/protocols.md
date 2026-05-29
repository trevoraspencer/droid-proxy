# Protocols

Distilled protocol compatibility notes for workers.

---

## Factory BYOK

Current Factory custom model settings use camelCase fields such as `model`, `displayName`, `provider`, `baseUrl`, `apiKey`, and `maxOutputTokens`. Supported providers are `anthropic`, `openai`, and `generic-chat-completion-api`.

## OpenAI

- `provider: openai` expects the Responses API on `/v1/responses`.
- Responses streaming uses typed SSE events such as `response.created`, output delta events, `response.completed`, and `error`.
- Chat Completions tool calls use `tools`, `tool_choice`, and `choices[].message.tool_calls` / streamed `choices[].delta.tool_calls`.
- Translators must not silently drop unsupported stateful/multimodal fields.

## Anthropic

- Messages API uses `/v1/messages` and `/v1/messages/count_tokens` with `x-api-key` and `anthropic-version` headers.
- Streaming event order includes `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, and `message_stop`.
- Tool use is represented as `tool_use` blocks; tool results are user `tool_result` blocks that must map carefully to Chat `role: tool` messages when translating.
- Anthropic-facing local errors should use Anthropic-shaped error envelopes.

## DeepSeek

- DeepSeek-style Chat streams can include `reasoning_content`; tool workflows require replaying prior assistant reasoning content on follow-up turns.
- Do not cache or replay partial reasoning from truncated, cancelled, errored, or timed-out streams.
- 2026 docs emphasize newer names such as `deepseek-v4-flash` and `deepseek-v4-pro`; legacy aliases must be documented as compatibility/legacy when referenced.
