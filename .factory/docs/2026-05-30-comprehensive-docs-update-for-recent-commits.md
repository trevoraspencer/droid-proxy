Update user docs to cover the features added in the last few commits (`a864c76`, `7bf5b03`, `d039bb3`) plus the just-completed remediation. The config TUI and basic OAuth flow are already documented; the gaps are the OAuth account-management commands, the `/v1/models` `oauth_auth` field, xAI request handling, disabled-account behavior, and the rolling Factory backup.

## 1. `docs/CLI.md`
- Synopsis: add the new auth forms:
  - `droid-proxy auth status [codex|xai] [--config PATH]`
  - `droid-proxy auth enable|disable|logout <provider> <account> [--config PATH]`
- Under "OAuth login": add an **Account management** subsection documenting `status` (lists accounts with email/sub/expiry/disabled/path), `enable`/`disable` (toggles a token; disabled accounts are skipped at request time), and `logout` (deletes a token file). Note `<account>` is the same selector as `oauth_account` (email, sub, account_id, or filename).

## 2. `docs/OAUTH.md`
- New **Managing accounts** section: `auth status` (with sample output), `auth enable`/`auth disable` (note disabled tokens are skipped during request-time selection), `auth logout`.
- New **Checking OAuth health** subsection: document the `/v1/models` `oauth_auth` object (`provider`, `pinned_account`, `matching_account_count`, `active_count`, `disabled_count`, `expired_or_expiring_count`, `missing_auth`) with a `jq` example.
- New **xAI request handling** section (counterpart to the existing "Codex request metadata"): explains, user-facing, that for xAI OAuth the proxy drops `service_tier`, derives `prompt_cache_key` from the session header, normalizes/sanitizes tools for agent compatibility (flattens namespace tools, drops unsupported tools like `tool_search`/`image_generation`/`apply_patch`, converts `custom`→`function`, strips `pattern`/`format` and slash-bearing enum values, strips unsupported web-search fields), adds the `reasoning.encrypted_content` include when reasoning is present, and repairs split/empty `response.completed` output in streamed responses.
- Update **Token storage** to mention the `disabled` flag field; cross-link **Multi-account selection** to the new account-management commands.

## 3. `docs/FACTORY.md`
- In the `/v1/models` / agent-readiness area: document the `oauth_auth` object returned for OAuth models, with a `jq` example.
- Note that `droid-proxy config` sync writes a single rolling `~/.factory/settings.json.bak` backup before overwriting (reflects the remediation's single-backup behavior).

## 4. `docs/PROVIDERS.md`
- In "OAuth providers": add a short paragraph on the account-management commands (`auth status/enable/disable/logout`), noting disabled accounts are skipped; cross-link OAUTH.md.

## 5. `docs/SMOKE.md`
- Add an OAuth-health check: `curl -s .../v1/models | jq '.data[] | select(.oauth_auth) | {id, oauth_auth}'`.

## 6. `README.md` (root) and `docs/README.md`
- Root: expand the "Focused OAuth" bullet to mention account status/enable/disable/logout controls.
- `docs/README.md`: update the CLI.md row description to include account management.

## 7. `docs/examples/codex-oauth.md` and `xai-oauth.md`
- Add a brief "Manage accounts" note linking to OAUTH.md's account-management + health sections. In `xai-oauth.md`, add one line noting the proxy auto-sanitizes the request for Grok agent compatibility (link to the new OAUTH.md section).

## Verification
- Re-grep the docs to confirm no remaining references to the old timestamped `settings.json.bak-<ts>` and that the new commands/fields are consistently named.
- Confirm all intra-doc relative links resolve.
- No code changes; docs only.