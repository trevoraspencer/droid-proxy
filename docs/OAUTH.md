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

- **Codex:** ChatGPT account with Codex access (typically ChatGPT Plus or Pro).
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
Saved codex OAuth credentials to /Users/you/.droid-proxy/auth/codex-user@example.com.json
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
  - alias: gpt-5.2-codex
    display_name: "GPT-5.2 Codex (ChatGPT OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.2-codex
```

### xAI

```yaml
models:
  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
    max_context_tokens: 256000
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

`grok-build-0.1` is the Grok Build coding model. `grok-4.3` is broader xAI
OAuth model support and is not described as Grok Build CLI parity. See xAI's
docs for [Grok Build 0.1](https://docs.x.ai/developers/models/grok-build-0.1),
[Grok 4.3](https://docs.x.ai/developers/models/grok-4.3), and
[reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning).

## Step 3: Configure Factory Droid

Use `provider: "openai"` and point `baseUrl` at the proxy:

```json
{
  "customModels": [
    {
      "model": "gpt-5.2-codex",
      "displayName": "GPT-5.2 Codex (ChatGPT OAuth)",
      "provider": "openai",
      "baseUrl": "http://127.0.0.1:8787",
      "apiKey": "x",
      "maxOutputTokens": 128000
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
When multiple Codex accounts are available, the proxy selects among them
according to the configured strategy and can fail over on retryable errors
(429, 5xx, transport timeout) within the configured `max_failovers` budget.
Pinned models (`oauth_account` set) restrict selection to the matching subset.

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
OAuth auth directory: /Users/you/.droid-proxy/auth
codex:
  - provider: codex
    account: user@example.com
    email: user@example.com
    expires: 2026-05-29T21:00:00Z
    last_refresh: 2026-05-29T20:00:00Z
    disabled: false
    path: /Users/you/.droid-proxy/auth/codex-user@example.com.json
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

The proxy also applies small Codex compatibility fixes automatically:

- Rewrites the Factory-facing model alias to `upstream_model`.
- Forces `store: false` and adds default Codex instructions only when the
  caller did not provide instructions.
- Preserves Factory's `reasoning` object, so the reasoning level selected in
  Droid can flow through on the same custom model.
- Drops `max_output_tokens`, which Factory may send from custom-model settings
  but the Codex OAuth endpoint rejects.

## xAI request handling

For xAI OAuth requests, the proxy adjusts the outbound `/v1/responses` payload
so it stays compatible with xAI's Responses endpoint. These changes are applied
automatically:

- Drops `service_tier` (not accepted on the OAuth endpoint).
- Drops Factory's top-level `reasoning` object when
  `capabilities.factory_reasoning: drop` is set or implied.
- Preserves Factory's top-level `reasoning` object when
  `capabilities.factory_reasoning: passthrough` is set, as with `grok-4.3`.
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
OAuth accepts the top-level `reasoning` object, so one custom model can cover
multiple reasoning levels. xAI OAuth is model-specific: `grok-build-0.1`
currently rejects that top-level effort parameter, so configure
`capabilities.factory_reasoning: drop`; `grok-4.3` supports configurable
reasoning, so configure `capabilities.factory_reasoning: passthrough`.
Encrypted reasoning round-trip fields are still preserved where needed.

Use a separate alias for provider fast/speed modes once the provider-specific
request field is verified. For example, keep `gpt-5.5-chatgpt` and
`gpt-5.5-chatgpt-fast` as separate custom models, with the fast alias carrying
only the additional `extra_args` needed by that upstream.

## Verify

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.2-codex","input":"hello"}' | jq '.output'
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
