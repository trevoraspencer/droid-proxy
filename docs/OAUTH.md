# OAuth walkthrough

`droid-proxy` supports browser PKCE OAuth login for two subscription-based
providers:

| Provider | Command | Factory mode | Upstream protocol |
|----------|---------|--------------|-------------------|
| Codex / ChatGPT | `auth codex` | `openai` | `codex-responses` |
| xAI | `auth xai` | `openai` | `xai-responses` |

OAuth models do not use API keys in your environment. Tokens are stored locally
and refreshed automatically.

Full example pages:

- [Codex / ChatGPT OAuth](examples/codex-oauth.md)
- [xAI OAuth](examples/xai-oauth.md)

## Prerequisites

- **Codex:** ChatGPT account with Codex access to the configured model. Current
  published availability includes GPT-5.6 Terra for Free/Go and Sol, Terra,
  and Luna for eligible Plus, Pro, Business, and Enterprise accounts; managed
  workspace policy and usage limits also apply. See OpenAI's
  [availability article](https://help.openai.com/en/articles/20001354).
- **xAI:** subscription access for the xAI OAuth model you configure.
- A working `config.yaml` with OAuth callback settings (defaults in
  `config.example.yaml` are usually fine).
- Port available for the local callback server:
  - Codex: `localhost:1455`
  - xAI: `127.0.0.1:56121`

## Step 1: Log in

```bash
./droid-proxy auth codex --config config.yaml
./droid-proxy auth xai --config config.yaml
```

The command opens your default browser (macOS `open`, Linux `xdg-open`, Windows
`rundll32`). Complete the provider login flow. On success:

```text
Saved codex OAuth credentials to ~/.droid-proxy/auth/codex-user@example.com.json
```

**Headless / remote machines:** pass `--no-browser` to print the authorization
URL instead of opening a browser:

```bash
./droid-proxy auth codex --config config.yaml --no-browser
```

Codex also supports device-code login, which does not require a localhost
callback:

```bash
./droid-proxy auth codex --config config.yaml --device
```

The command prints `https://auth.openai.com/codex/device` and a short code,
then polls until the browser approval completes.

## Step 2: Add a model to config.yaml

### Codex

```yaml
models:
  - alias: gpt-5.6
    display_name: "GPT-5.6 Sol (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-sol
    max_output_tokens: 128000
    max_context_tokens: 1050000
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true

  - alias: gpt-5.6-fast # local alias; private OAuth uses explicit Sol ID
    display_name: "GPT-5.6 Sol Fast (Codex OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.6-sol
    max_output_tokens: 128000
    max_context_tokens: 1050000
    extra_args:
      service_tier: priority
    capabilities:
      streaming: true
      tools: true
      tool_result_safe: true
      images: true
      json_mode: true
      structured_output: true
      factory_reasoning: passthrough
      factory_reasoning_effort: max
      prompt_caching: true
```

The dashboard also provides standard and fast presets for `gpt-5.6-terra`
and `gpt-5.6-luna`. The public API documents `gpt-5.6` as the recommended Sol
alias, but credentialed validation shows that the private Codex OAuth backend
requires the explicit `gpt-5.6-sol` ID. The dashboard therefore keeps the
user-facing local aliases `gpt-5.6` and `gpt-5.6-fast` while mapping both to
`gpt-5.6-sol`; only the fast entry requests
`extra_args.service_tier: priority`.

### xAI

```yaml
models:
  - alias: grok-4.5
    display_name: "Grok 4.5 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: https://cli-chat-proxy.grok.com/v1
    upstream_model: grok-4.5
    max_context_tokens: 500000
    capabilities:
      factory_reasoning: passthrough
      factory_reasoning_effort: high
      prompt_caching: true

  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
    max_context_tokens: 256000
    capabilities:
      factory_reasoning: drop

  - alias: grok-composer-2.5-fast
    display_name: "Composer 2.5 Fast (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: https://cli-chat-proxy.grok.com/v1
    upstream_model: grok-composer-2.5-fast
    max_output_tokens: 128000
    max_context_tokens: 200000
    capabilities:
      factory_reasoning: drop

  - alias: grok-4.3
    display_name: "Grok 4.3 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-4.3
    max_context_tokens: 1000000
    capabilities:
      factory_reasoning: passthrough
```

`grok-4.5` is the recommended xAI OAuth alias and Grok Build's current default;
it uses the private Grok CLI proxy and passes through low, medium, and high
reasoning. Public API and private OAuth access are separate contracts.
`grok-build-0.1` is the Grok Build coding API model.
`grok-composer-2.5-fast` is Composer 2.5 Fast via the Grok Build / Grok CLI
OAuth endpoint. `grok-4.3` is broader xAI OAuth model support and is not
described as Grok Build CLI parity. See xAI's docs for
[Composer 2.5](https://x.ai/news/composer-2-5),
[Grok Build 0.1](https://docs.x.ai/developers/models/grok-build-0.1),
[Grok 4.5](https://docs.x.ai/developers/grok-4-5),
[Grok 4.3](https://docs.x.ai/developers/models/grok-4.3), and
[reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning).

## Step 3: Configure Factory Droid

Use `provider: "openai"` and point `baseUrl` at the proxy:

```json
{
  "customModels": [
    {
      "model": "gpt-5.6",
      "displayName": "GPT-5.6 Sol (Codex OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000,
      "reasoningEffort": "max"
    }
  ]
}
```

Ready-to-paste snippets: [codex-oauth.json](factory-settings/codex-oauth.json),
[xai-oauth.json](factory-settings/xai-oauth.json).

## Step 4: Start the proxy

```bash
./droid-proxy start --config config.yaml
./droid-proxy status
```

OAuth tokens are read from disk on each request — no env vars needed.

## Token storage

| Setting | Default |
|---------|---------|
| `oauth.auth_dir` | `~/.droid-proxy/auth` |
| Directory permissions | `0700` |
| File permissions | `0600` |

Token files are named from the provider and account, for example:

- `codex-user@example.com.json`
- `xai-user@example.com.json`

Each file contains access and refresh tokens plus metadata (email, expiry).
A `disabled` flag (toggled by `auth enable`/`auth disable`) marks accounts the
proxy should skip during request-time selection. Codex token files may also
include passive health fields such as `codex_quota`, `rate_limit_reset_at`, and
`last_seen_at`; these are telemetry from upstream response headers/events and
may be used by the Codex account pool for load-balancing eligibility decisions.

## Multi-account selection

Log in multiple times with different accounts. Each saves a separate file.

For **Codex OAuth**, the proxy uses an in-memory account pool with configurable
load balancing (see `oauth.load_balancing` in [CONFIG.md](CONFIG.md#oauthload_balancing)).
The default **`sticky`** strategy binds each Droid conversation (from
`session_id` / `prompt_cache_key`) to one Codex account until that account is
rate-limited or unhealthy, then fails over and re-binds to the next eligible
account. Bindings persist in `~/.droid-proxy/conversation_affinity.json`.
When multiple Codex accounts are available, the proxy can fail over on retryable
errors (429, 5xx, transport timeout) within the configured `max_failovers`
budget. Pinned models (`oauth_account` set) restrict selection to the matching
subset.

Check pool status:

```bash
curl -s http://127.0.0.1:8787/v1/oauth/pool-health | jq .
droid-proxy auth pool --config config.yaml
```

If a Codex token file is deleted while a request using it is still in flight,
the live pool-health snapshot may keep that account visible with
`token_file_present: false` until the request finishes. Such removed entries
are not eligible for new requests, are shown as `removed` by
`droid-proxy auth pool`, and any sticky conversation bindings to the deleted
token path are pruned on reload.

Each pool-health account includes secret-safe readiness fields:

| Field | Meaning |
|-------|---------|
| `eligible` | `true` when the account can be selected right now. |
| `eligibility_status` | `eligible`, or the primary reason selection skips the account. |
| `eligibility_reasons` | All current skip reasons, such as `disabled`, `cooldown`, `rate_limited`, `unhealthy`, or `expired_no_refresh`. |

`droid-proxy auth pool` shows these reason codes in its `STATUS` column so an
offline release binary can explain why `eligible_count` is zero without
exposing token secrets or raw token file paths.

For **xAI OAuth**, the proxy uses the existing single-account path; xAI accounts
are not pooled or load-balanced.

To pin a Codex model to one account, set `oauth_account` on the model entry:

```yaml
    oauth_account: user@example.com
```

The proxy matches against email, subject, account ID, or filename. If
`oauth_account` is unset for a Codex model, the account pool selects from all
eligible (non-disabled, non-cooled-down) accounts using the configured strategy.
If `oauth_account` is unset for an xAI model, the first valid account is used.

## Managing accounts

Inspect and manage stored accounts without re-running a login. These commands
work from the CLI; the same actions are available in the `droid-proxy config`
dashboard (press `o`).

```bash
./droid-proxy auth status                        # both providers
./droid-proxy auth status codex                  # one provider
./droid-proxy auth disable xai user@example.com  # stop using an account
./droid-proxy auth enable  xai user@example.com  # re-enable it
./droid-proxy auth logout  codex user@example.com
```

`auth status` prints each stored account:

```text
OAuth auth directory: ~/.droid-proxy/auth
codex:
  - provider: codex
    account: user@example.com
    email: user@example.com
    expires: 2026-05-29T21:00:00Z
    last_refresh: 2026-05-29T20:00:00Z
    disabled: false
    path: ~/.droid-proxy/auth/codex-user@example.com.json
xai:
  (no accounts)
```

- **`disable`** sets the `disabled` flag; the proxy then skips that account when
  picking a token for requests. Use it to park a rate-limited or secondary
  account without deleting its tokens.
- **`enable`** clears the flag.
- **`logout`** deletes the token file entirely (re-run `auth <provider>` to log
  back in).

`<account>` is the same selector accepted by `oauth_account`: email, subject
(`sub`), account ID, or token filename.

## Checking OAuth health

`/v1/models` includes an `oauth_auth` object for every model that uses an OAuth
provider, summarizing the accounts that match the model's `oauth_account`:

```bash
curl -s http://127.0.0.1:8787/v1/models \
  | jq '.data[] | select(.oauth_auth) | {id, oauth_auth}'
```

| Field | Meaning |
|-------|---------|
| `provider` | The model's `oauth_provider` (`codex` or `xai`). |
| `pinned_account` | The model's `oauth_account`, or empty for "any account". |
| `matching_account_count` | Stored accounts matching the pin. |
| `active_count` | Matching accounts that are enabled and not expiring. |
| `disabled_count` | Matching accounts marked disabled. |
| `expired_or_expiring_count` | Matching accounts whose access token is expired or within the 5-minute refresh window. |
| `missing_auth` | `true` when no stored account matches — log in or fix `oauth_account`. |

## Auto-refresh

Before each request, the proxy checks token expiry. If the access token expires
within **five minutes**, it refreshes using the stored refresh token and writes
the updated file back to disk.

Refresh is coordinated per account inside the process and with a small lock
file under `oauth.auth_dir/.locks/`, so concurrent requests do not spend the
same rotating refresh token multiple times. Token files are replaced atomically.

If refresh fails (revoked session, expired refresh token), re-run:

```bash
./droid-proxy auth codex --config config.yaml
```

## Callback configuration

Override defaults in `config.yaml` if ports conflict:

```yaml
oauth:
  auth_dir: "~/.droid-proxy/auth"
  codex_callback_host: localhost
  codex_callback_port: 1455
  xai_callback_host: 127.0.0.1
  xai_callback_port: 56121
```

| Provider | Default callback |
|----------|------------------|
| Codex | `http://localhost:1455/auth/callback` |
| xAI | `http://127.0.0.1:56121/callback` |

Codex device login bypasses the local callback and exchanges through
`https://auth.openai.com/deviceauth/callback`.

## Codex request metadata

For Codex requests, the proxy adds stable request identity headers used by
Codex clients, including `x-codex-installation-id`, `x-client-request-id`,
`session_id`, `x-codex-window-id`, and `OpenAI-Beta`. The same installation and
window identifiers are merged into `client_metadata` without overwriting caller
provided metadata keys.

OpenAI's
[GPT-5.6 availability article](https://help.openai.com/en/articles/20001354)
lists Codex CLI `0.144.0` as the minimum version for GPT-5.6. When the
downstream request omits them, the proxy therefore supplies `Version: 0.144.0`
and a `codex_cli_rs/0.144.0` User-Agent fallback. Caller-supplied `Version` and
`User-Agent` values are forwarded unchanged.

The proxy also applies small Codex compatibility fixes automatically:

- Rewrites the Factory-facing model alias to `upstream_model`. For the local
  `gpt-5.6` Sol aliases, that means forwarding `gpt-5.6-sol`: credentialed
  private-OAuth validation rejects the public API's unsuffixed alias.
- Accepts `extra_args.service_tier: fast` in config and normalizes the outgoing
  request to `priority`. The effective tier is account/backend dependent and
  appears in the response.
- Forces `store: false` and adds default Codex instructions only when the
  caller did not provide instructions.
- Preserves Factory's `reasoning` object exactly. Credentialed validation
  confirms GPT-5.6 `effort: max` succeeds, while the tested accounts returned
  an upstream 400 for `mode: pro` on Sol, Terra, and Luna. The proxy does not
  strip or downgrade Pro; it surfaces that response. Mode availability remains
  account/plan dependent.
- Strips public `prompt_cache_options`, which the private endpoint rejects,
  while preserving `prompt_cache_key` for private prompt caching. The legacy
  `prompt_cache_retention` field remains stripped.
- Drops `max_output_tokens`, which Factory may send from custom-model settings
  but the Codex OAuth endpoint rejects.
- Continues to strip `previous_response_id`, `safety_identifier`, and
  `stream_options`. Public API guidance for those fields does not establish
  support on the private ChatGPT/Codex OAuth backend.

The public OpenAI API documents GPT-5.6 IDs and capabilities, but it does not
document this private OAuth backend contract. Credentialed validation
establishes the explicit Sol mapping above and confirms standard Luna with
current Codex client-version metadata. The fallback uses OpenAI's documented
minimum client version; it is not a claim that the private header contract is
public. Model and mode availability beyond those observations remains account-
and plan-dependent. Run the credentialed live-E2E gate before relying on a path
in production. A non-retryable 4xx is returned as-is; Codex failover switches
accounts only and never changes `upstream_model`.

## xAI request handling

For xAI OAuth requests, the proxy adjusts the outbound `/v1/responses` payload
so it stays compatible with xAI's Responses endpoint. These changes are applied
automatically:

- On the private Grok CLI proxy, sends the CLI token-auth, client identity,
  client surface/version, and exact `x-grok-model-override` headers.
- Forces the private Grok CLI upstream request to stream because its inference
  clusters are stream-only; non-streaming callers still receive reconstructed
  JSON after the upstream SSE response completes.
- Drops `service_tier` (not accepted on the OAuth endpoint).
- Drops Factory's top-level `reasoning` object when
  `capabilities.factory_reasoning: drop` is set or implied.
- Preserves Factory's top-level `reasoning` object when
  `capabilities.factory_reasoning: passthrough` is set, as with `grok-4.5` and
  `grok-4.3`.
- Sets `prompt_cache_key` from the downstream session header (`X-Session-ID`,
  `Session_id`, or `X-Client-Request-Id`) when the caller did not provide one.
- Normalizes `tools` for agent compatibility: flattens namespace/grouped tools,
  drops unsupported tools (`tool_search`, `image_generation`, `apply_patch`),
  converts `custom` tools to `function`, strips JSON-schema `pattern`/`format`
  and enum values containing `/`, and removes unsupported web-search fields
  (`search_context_size`, `user_location`, domain filters, etc.).
- Adds `reasoning.encrypted_content` to `include` when the request carries
  reasoning, so encrypted reasoning round-trips across turns.

For streamed responses the proxy also repairs `response.completed` events whose
`output` arrives empty or split, reconstructing it from the preceding
`response.output_item.done` events so Droid receives a complete final message.

## Reasoning and fast mode

Use Factory Droid's reasoning selector when the upstream supports it. Codex
OAuth preserves the top-level `reasoning` object exactly; credentialed
validation confirms `effort: max` works. The public API represents Pro as
`reasoning.mode: pro`, not a `*-pro` model ID, but the private Codex OAuth
tests returned HTTP 400 for that mode on Sol, Terra, and Luna with the tested
accounts. The proxy forwards it unchanged and surfaces the error instead of
silently downgrading; mode availability remains account/plan dependent. xAI
OAuth is model-specific: `grok-build-0.1` currently rejects
that top-level effort parameter, and
`grok-composer-2.5-fast` does not support reasoning effort on the Grok CLI OAuth
endpoint, so configure both with `capabilities.factory_reasoning: drop`;
`grok-4.5` and `grok-4.3` support configurable reasoning, so configure
`capabilities.factory_reasoning: passthrough`.
Encrypted reasoning round-trip fields are still preserved where needed.

Use the GPT-5.6 preset pairs for fast mode. For example, `gpt-5.6` and the
local alias `gpt-5.6-fast` both forward `model: gpt-5.6-sol`; only the fast
entry requests `service_tier: priority`. Terra and Luna use their explicit
family IDs with the same standard/fast pattern. The response reports the
effective tier, which may remain `default` depending on the account/backend.

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6","input":"hello"}' | jq '.output'
```

See [SMOKE.md](SMOKE.md) for more checks.

## Troubleshooting

**`no codex OAuth accounts found; run droid-proxy auth codex`**

Run the auth command before starting requests for OAuth models.

**Browser opens but callback fails**

Ensure nothing else listens on the callback port. Check firewall rules for
localhost.

**Wrong account used**

Set `oauth_account` explicitly or remove unused token files from
`~/.droid-proxy/auth/`.

## See also

- [CLI reference](CLI.md) — `auth` command flags
- [Configuration reference](CONFIG.md) — `oauth:` section
- [Supported providers](PROVIDERS.md) — OAuth matrix
