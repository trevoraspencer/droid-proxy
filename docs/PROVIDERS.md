# Supported providers

`droid-proxy` is designed to put any model in front of Factory Droid. The exact
behavior depends on three things:

1. The **Factory provider mode** Droid uses to call the proxy
   (`anthropic`, `openai`, or `generic-chat-completion-api`).
2. The **upstream protocol** the actual provider speaks
   (`anthropic-messages`, `openai-responses`, `openai-chat`,
   `codex-responses`, or `xai-responses`).
3. The **tier**: how much translation we do between (1) and (2).

This page documents the matrix and the honest tier classification of each
supported provider.

## Tier definitions

| Tier | Meaning |
|------|---------|
| **T1** | Direct passthrough. Droid's protocol matches the upstream's protocol natively. Streaming, tools, structured output, multimodal all work as-is. |
| **T2** | OpenAI-compatible endpoint. Upstream speaks `/v1/chat/completions` correctly. Streaming + tools tested. |
| **T3** | Protocol translation. We re-shape request and response between protocols. Streaming + tools are supported for the implemented OpenAI Chat-backed translations, with minor delta-event timing differences from native upstreams. |
| **T4** | Best effort. Chat-only support; tool calls / structured output / multimodal may not survive translation reliably. `agent_ready: false`. |

## Current status

This release ships:

- ✅ **T1 / T2** paths: `generic-chat-completion-api` over `openai-chat`,
  `openai` over `openai-responses`, `anthropic` over `anthropic-messages`.
- ✅ **OAuth Responses paths**: `openai` over `codex-responses` for
  Codex/ChatGPT OAuth and `openai` over `xai-responses` for xAI Grok Build
  OAuth.
- ✅ **DeepSeek reasoning replay** (T1) for upstreams identified as DeepSeek or
  any model with `capabilities.reasoning: deepseek`, including Xiaomi MiMo.
- ✅ **T3** translation paths: `openai` over `openai-chat` and `anthropic`
  over `openai-chat` translate text, streaming, tool calls, and tool results
  against OpenAI-compatible Chat Completions upstreams.

## Provider matrix

`known_auth` is a short string you can set on a model to inherit defaults
(base URL, env var, auth header, version headers). Anything you set explicitly
in `config.yaml` always wins.

| known_auth | Default base URL | Env var | Default upstream | Tier |
|-----|-----|-----|-----|-----|
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `openai-responses` | T1 (openai mode) |
| `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `anthropic-messages` | T1 (anthropic mode) |
| `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | `openai-chat` | T1 (generic mode, reasoning replay) |
| `xai` | `https://api.x.ai/v1` | `XAI_API_KEY` | `openai-chat` | T2 |
| `kimi` | `https://api.moonshot.cn/v1` | `MOONSHOT_API_KEY` | `openai-chat` | T2 |
| `groq` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `openai-chat` | T2 |
| `fireworks` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `openai-chat` | T2 |
| `zai` | `https://api.z.ai/api/paas/v4` | `ZAI_API_KEY` | `openai-chat` | T2 (legacy main API alias) |
| `zai-main-api` | `https://api.z.ai/api/paas/v4` | `ZAI_MAIN_API_KEY` | `openai-chat` | T2 |
| `zai-coding-api` | `https://api.z.ai/api/coding/paas/v4` | `ZAI_CODING_API_KEY` | `openai-chat` | T2 (GLM Coding Plan) |
| `mimo` | `https://api.xiaomimimo.com/v1` | `MIMO_API_KEY` | `openai-chat` | T2 (reasoning replay) |
| `mimo-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_CN_API_KEY` | `openai-chat` | T2 (reasoning replay) |
| `mimo-token-plan-sgp` | `https://token-plan-sgp.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_SGP_API_KEY` | `openai-chat` | T2 (reasoning replay) |
| `mimo-token-plan-ams` | `https://token-plan-ams.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_AMS_API_KEY` | `openai-chat` | T2 (reasoning replay) |
| `ollama` | `http://127.0.0.1:11434/v1` | _(none; local no-auth)_ | `openai-chat` | T2 |
| `vllm` | `http://127.0.0.1:8000/v1` | _(none; local no-auth)_ | `openai-chat` | T2 |

Custom OpenAI-compatible upstreams (LiteLLM, custom self-hosted gateways, etc.)
work the same as the above — set `base_url`, `api_key_env`, and
`upstream_protocol: openai-chat`. No `known_auth` needed.

For Z.AI, use `zai-coding-api` with GLM Coding Plan keys. Use `zai-main-api`
with normal Z.AI API keys. The older `zai` profile remains as a compatibility
alias for the main API and still reads `ZAI_API_KEY`.

## Xiaomi MiMo

MiMo is OpenAI Chat-compatible and should be exposed to Droid as
`generic-chat-completion-api`. The built-in profiles use Xiaomi's documented
`api-key` header and enable DeepSeek-style reasoning replay by default because
MiMo thinking mode returns `reasoning_content` with tool calls and requires it
to be sent back in later turns.

Use `mimo-v2.5-pro` for coding and long reasoning. Use `mimo-v2.5` when
multimodal input is needed. Avoid new configs with legacy `mimo-v2-pro` or
`mimo-v2-omni`; Xiaomi says those legacy names start auto-routing to V2.5 on
June 1, 2026 and are fully deprecated on June 30, 2026.

Provider-side prompt cache hits are billed separately from cache misses, but
Xiaomi does not document a Chat `cache_control` field. Keep
`capabilities.prompt_caching` unset unless their API adds an explicit control.

OpenRouter is intentionally not supported and not on the roadmap.

## OAuth providers

OAuth providers are configured per model rather than through `known_auth`.
Run `droid-proxy auth codex --config config.yaml` or
`droid-proxy auth xai --config config.yaml` first, then configure a model with
`factory_provider: openai`, the matching OAuth upstream protocol, and
`oauth_provider`.

| oauth_provider | upstream_protocol | Default upstream | Callback default | Notes |
|---|---|---|---|---|
| `codex` | `codex-responses` | `https://chatgpt.com/backend-api/codex` | `localhost:1455/auth/callback` | Codex/ChatGPT OAuth for text, streaming, tools, and tool outputs. |
| `xai` | `xai-responses` | `https://api.x.ai/v1` | `127.0.0.1:56121/callback` | xAI Grok Build OAuth through xAI's Responses API. |

OAuth token files live under `oauth.auth_dir` (default
`~/.droid-proxy/auth`) with `0700` directory and `0600` file permissions. If a
model sets `oauth_account`, the proxy selects that stored account; otherwise it
uses the first valid account for the provider after refresh.

## Factory provider × upstream protocol matrix

| factory_provider | upstream_protocol | Droid hits | Tier | Status in this build |
|---|---|---|---|---|
| `generic-chat-completion-api` | `openai-chat` | `/v1/chat/completions` | T1/T2 | ✅ supported |
| `openai` | `openai-responses` | `/v1/responses` and `/v1/chat/completions` | T1 | ✅ supported (Responses native; Chat returns native chat-completions response when Droid sends one) |
| `openai` | `openai-chat` | `/v1/responses` (translated) and `/v1/chat/completions` (native) | T3 | ✅ supported |
| `openai` | `codex-responses` | `/v1/responses` | T1 OAuth | ✅ supported for Responses text, streaming, tools, and tool outputs |
| `openai` | `xai-responses` | `/v1/responses` | T1 OAuth | ✅ supported for Responses text, streaming, tools, and tool outputs |
| `anthropic` | `anthropic-messages` | `/v1/messages`, `/v1/messages/count_tokens` | T1 | ✅ supported (gzip auto-decompress, native streaming) |
| `anthropic` | `openai-chat` | `/v1/messages` (translated), `/v1/messages/count_tokens` (local fallback) | T3 | ✅ supported |

A model marked `agent_ready: true` in `/v1/models` means:

- Streaming works.
- Tool calls survive a round trip.
- `tool_result` messages are forwarded without reshaping.

To opt a model out of agent-ready, set `capabilities.streaming: false` (or
`tools: false`, etc.) explicitly.

## Adding a new provider

`droid-proxy` does not require code changes to add a new OpenAI-compatible
provider. Just add a model entry to `config.yaml`:

```yaml
models:
  - alias: my-custom-model
    display_name: "Custom"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: "https://my-provider.example.com/v1"
    api_key_env: MY_PROVIDER_KEY
```

Only when a provider needs a non-default auth header (like Anthropic's
`x-api-key`) or default version headers, would a code change be needed.
File an issue with the provider's docs link.
