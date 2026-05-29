# OAuth walkthrough

`droid-proxy` supports browser PKCE OAuth login for two subscription-based
providers:

| Provider | Command | Factory mode | Upstream protocol |
|----------|---------|--------------|-------------------|
| Codex / ChatGPT | `auth codex` | `openai` | `codex-responses` |
| xAI Grok Build | `auth xai` | `openai` | `xai-responses` |

OAuth models do not use API keys in your environment. Tokens are stored locally
and refreshed automatically.

Full example pages:

- [Codex / ChatGPT OAuth](examples/codex-oauth.md)
- [xAI Grok Build OAuth](examples/xai-oauth.md)

## Prerequisites

- **Codex:** ChatGPT account with Codex access (typically ChatGPT Plus or Pro).
- **xAI:** xAI Grok Build subscription.
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

### xAI Grok Build

```yaml
models:
  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
```

Replace `upstream_model` with the model ID your account supports.

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
      "maxOutputTokens": 8192
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

## Multi-account selection

Log in multiple times with different accounts. Each saves a separate file.

To pin a model to one account, set `oauth_account` on the model entry:

```yaml
    oauth_account: user@example.com
```

The proxy matches against email, subject, account ID, or filename. If
`oauth_account` is unset, the first valid account for that provider is used
after refresh.

## Auto-refresh

Before each request, the proxy checks token expiry. If the access token expires
within **five minutes**, it refreshes using the stored refresh token and writes
the updated file back to disk.

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
