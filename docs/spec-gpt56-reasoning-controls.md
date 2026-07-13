# GPT-5.6 reasoning controls patch spec

## Baseline and problem

This patch targets post-PR #25 `main` at `d8e89f7`. Proxy request forwarding
already preserves Codex `reasoning` objects and normalizes `service_tier: fast`
to `priority`; those paths are not being redesigned here.

Droid 0.144.1's built-in registry ends at GPT-5.5. Its custom-model schema has
an optional `reasoningEffort` field. Unknown aliases such as the six local
GPT-5.6 aliases do not expose reasoning controls when that field is absent,
even though the proxy can forward the selected effort.

Read-only inspection of the installed Droid 0.144.1 binary validated that
`customModels[].reasoningEffort` accepts these exact values:

`none`, `dynamic`, `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`

For an unknown model ID, an omitted value resolves to no reasoning control.
Providing a value opts the custom model into Droid's reasoning-effort selector.

## Scoped design

1. Add a validated Factory reasoning-effort capability to proxy model config,
   separate from `capabilities.factory_reasoning`:
   - `factory_reasoning` continues to control whether incoming reasoning is
     forwarded to or dropped for the upstream.
   - `factory_reasoning_effort` carries the exact Droid custom-model schema
     value to advertise in Factory settings and model metadata.
2. Factory settings sync writes `reasoningEffort` when the model advertises a
   valid effort and removes a previously managed `reasoningEffort` when the
   model does not. Existing unknown settings fields remain preserved.
3. The six GPT-5.6 Codex OAuth presets advertise `max`, matching the
   credential-validated Codex forwarding behavior. Their standard/fast alias
   and upstream mappings remain unchanged; fast presets still use
   `service_tier: priority`.
4. Grok 4.5 advertises `high`. Composer 2.5 Fast and Grok Build continue to use
   `factory_reasoning: drop` and advertise no reasoning effort, so sync removes
   stale `reasoningEffort` metadata for those aliases.
5. `/v1/models` returns the resolved Factory reasoning-effort capability so
   settings/runtime parity can be inspected without reading live settings.
6. Document the current Factory field and add the H7 retirement note for the
   legacy cursor proxy projects.

## Required verification

- Factory settings unit tests cover write, idempotent update, preservation of
  unrelated fields, and removal of stale `reasoningEffort`.
- Config validation and config-editor round-trip tests cover every accepted
  installed-Droid schema value plus invalid and drop/passthrough combinations.
- TUI tests cover all six GPT-5.6 aliases, Grok 4.5, Composer, and Grok Build.
- Models endpoint tests cover capability serialization.
- Response/settings integration tests prove advertised passthrough models keep
  reasoning while non-advertised drop models remove it.
- Documentation/example tests require the intended `reasoningEffort` values.
- Run targeted tests, the full suite, race tests, lint/vet, and repository
  audits, then perform an adversarial diff review before publishing.

## Port and migration boundaries

The repository default remains `8787`. Port `9787` is reserved for the
operator's current local service and Factory URLs and is not introduced into
source defaults or public examples.

This patch must not edit live Factory settings or control the running service.
After tests and review, the migration plan is:

1. Confirm the installed proxy version/config and read the six GPT-5.6, Grok
   4.5, Grok Build, and Composer entries from Factory settings without editing.
2. Review a proposed before/after diff limited to `reasoningEffort`: add `max`
   to the six GPT-5.6 aliases, add `high` to Grok 4.5, and remove the field from
   Grok Build and Composer. Preserve every unrelated setting and model entry.
3. Use the normal config dashboard sync so its rolling backup is created; do
   not hand-rewrite the settings file.
4. Re-read the settings file and verify only the reviewed fields changed. If
   verification fails, restore the rolling backup before reopening Droid.

Executing that migration is explicitly outside this PR.
