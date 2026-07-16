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
  Codex/ChatGPT OAuth and `openai` over `xai-responses` for xAI OAuth.
- ✅ **DeepSeek reasoning replay** (T1) for upstreams identified as DeepSeek or
  any model with `capabilities.reasoning: deepseek`, including Xiaomi MiMo.
- ✅ **T3** translation paths: `openai` over `openai-chat` and `anthropic`
  over `openai-chat` translate text, streaming, tool calls, and tool results
  against OpenAI-compatible Chat Completions upstreams.

## Provider matrix

`known_auth` is a short string you can set on a model to inherit defaults
(base URL, env var, auth header, version headers, and model-discovery metadata).
Anything you set explicitly in `config.yaml` always wins.

| known_auth | Default base URL | Env var | Default upstream | Tier | Example |
|-----|-----|-----|-----|-----|-----|
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `openai-responses` | T1 (openai mode) | [openai.md](examples/openai.md) |
| `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `anthropic-messages` | T1 (anthropic mode) | [anthropic.md](examples/anthropic.md) |
| `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | `openai-chat` | T1 (generic mode, reasoning replay) | [deepseek.md](examples/deepseek.md) |
| `xai` | `https://api.x.ai/v1` | `XAI_API_KEY` | `openai-chat` | T2 | [xai.md](examples/xai.md) |
| `kimi` | `https://api.moonshot.cn/v1` | `MOONSHOT_API_KEY` | `openai-chat` | T2 | [kimi.md](examples/kimi.md) |
| `groq` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `openai-chat` | T2 | [groq.md](examples/groq.md) |
| `fireworks` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `openai-chat` | T2 | [fireworks.md](examples/fireworks.md) |
| `fireworks-fire-pass` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_FIRE_PASS_API_KEY` | `openai-chat` | T2 | [fireworks-fire-pass.md](examples/fireworks-fire-pass.md) |
| `zai` | `https://api.z.ai/api/paas/v4` | `ZAI_API_KEY` | `openai-chat` | T2 (legacy main API alias) | [zai.md](examples/zai.md) |
| `zai-main-api` | `https://api.z.ai/api/paas/v4` | `ZAI_MAIN_API_KEY` | `openai-chat` | T2 | [zai.md](examples/zai.md) |
| `zai-coding-api` | `https://api.z.ai/api/coding/paas/v4` | `ZAI_CODING_API_KEY` | `openai-chat` | T2 (GLM Coding Plan) | [zai.md](examples/zai.md) |
| `mimo` | `https://api.xiaomimimo.com/v1` | `MIMO_API_KEY` | `openai-chat` | T2 (reasoning replay) | [mimo.md](examples/mimo.md) |
| `mimo-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_CN_API_KEY` | `openai-chat` | T2 (reasoning replay) | [mimo.md](examples/mimo.md) |
| `mimo-token-plan-sgp` | `https://token-plan-sgp.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_SGP_API_KEY` | `openai-chat` | T2 (reasoning replay) | [mimo.md](examples/mimo.md) |
| `mimo-token-plan-ams` | `https://token-plan-ams.xiaomimimo.com/v1` | `MIMO_TOKEN_PLAN_AMS_API_KEY` | `openai-chat` | T2 (reasoning replay) | [mimo.md](examples/mimo.md) |
| `ollama` | `http://127.0.0.1:11434/v1` | _(none; local no-auth)_ | `openai-chat` | T2 | [local-ollama.md](examples/local-ollama.md) |
| `vllm` | `http://127.0.0.1:8000/v1` | _(none; local no-auth)_ | `openai-chat` | T2 | [local-vllm.md](examples/local-vllm.md) |

Custom OpenAI-compatible upstreams (LiteLLM, custom self-hosted gateways, etc.)
work the same as the above — set `base_url`, `api_key_env`, and
`upstream_protocol: openai-chat`. No `known_auth` needed.

For DeepSeek, `known_auth: deepseek` enables `capabilities.reasoning: deepseek`
and sends `extra_args.thinking.type: enabled` plus `reasoning_effort: high`.
The replay capability keeps tool-call turns coherent; the `extra_args` make
upstream thinking explicit.

For Z.AI, use `zai-coding-api` with GLM Coding Plan keys. Use `zai-main-api`
with normal Z.AI API keys. The older `zai` profile remains as a compatibility
alias for the main API and still reads `ZAI_API_KEY`.

## Xiaomi MiMo

MiMo is OpenAI Chat-compatible and should be exposed to Droid as
`generic-chat-completion-api`. The built-in profiles use Xiaomi's documented
`api-key` header, send `extra_args.thinking.type: enabled`, and enable
DeepSeek-style reasoning replay by default because MiMo thinking mode returns
`reasoning_content` with tool calls and requires it to be sent back in later
turns.

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

| oauth_provider | upstream_protocol | Default upstream | Callback default | Example |
|---|---|---|---|---|
| `codex` | `codex-responses` | `https://chatgpt.com/backend-api/codex` | `localhost:1455/auth/callback` | [codex-oauth.md](examples/codex-oauth.md) |
| `xai` | `xai-responses` | `https://api.x.ai/v1` | `127.0.0.1:56121/callback` | [xai-oauth.md](examples/xai-oauth.md) |

See [OAUTH.md](OAUTH.md) for the full OAuth walkthrough.

For Codex OAuth, the dashboard's primary family is GPT-5.6: the recommended
`gpt-5.6` Sol alias plus `gpt-5.6-terra` and `gpt-5.6-luna`, each with a local
standard/fast pair. The public API aliases `gpt-5.6` to Sol, while the
credential-validated private OAuth path requires explicit `gpt-5.6-sol`; both
local Sol aliases therefore map to that explicit ID. Fast aliases keep their
standard entry's upstream model and request `service_tier: priority`; the
effective response tier is account/backend dependent and may remain `default`.
The public API represents Pro as `reasoning.mode: pro`, not a model slug, but
credentialed tests returned upstream 400 for that mode on the tested accounts.
`effort: max` succeeds, reasoning is preserved unchanged, and all such 4xx
responses are surfaced without model fallback. Mode and model availability
remain account/plan dependent.

For xAI OAuth, use the recommended `grok-4.5` alias for Grok Build's current
default through the private Grok CLI OAuth endpoint, `grok-build-0.1` for the
older Grok Build coding behavior,
`grok-composer-2.5-fast` for Composer 2.5 Fast via the Grok CLI OAuth endpoint,
and `grok-4.3` for the broader xAI reasoning model. Configure
`capabilities.factory_reasoning: drop` for Grok Build and Composer, and
`capabilities.factory_reasoning: passthrough` for Grok 4.5 and Grok 4.3.

OAuth token files live under `oauth.auth_dir` (default
`~/.droid-proxy/auth`) with `0700` directory and `0600` file permissions. If a
model sets `oauth_account`, the proxy selects that stored account; otherwise for
Codex models the account pool selects from eligible accounts using the configured
`oauth.load_balancing` strategy (see [CONFIG.md](CONFIG.md#oauthload_balancing)),
and for xAI models the first valid account is used after refresh.

Manage stored accounts with `droid-proxy auth status` (list), `auth disable` /
`auth enable` (a disabled account is skipped during request-time selection), and
`auth logout` (delete a token file). Per-model OAuth health is also surfaced in
the `oauth_auth` object on `/v1/models`, and Codex pool readiness is available
from `/v1/oauth/pool-health` / `droid-proxy auth pool` with per-account
eligibility reason codes. See [OAUTH.md](OAUTH.md) for details.

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
