#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

load_local_env

cd "$LIVE_E2E_REPO_ROOT"

SETTINGS="$HOME/.factory/settings.json"
TEMPLATE="$LIVE_E2E_REPO_ROOT/docs/live-e2e/factory-settings.live.json.template"
TMP="$LIVE_E2E_RUN_DIR/settings.json.tmp"

[[ -f "$TEMPLATE" ]] || fail "missing template: $TEMPLATE"
[[ -f "$LIVE_E2E_CONFIG" ]] || fail "missing config: $LIVE_E2E_CONFIG"
mkdir -p "$HOME/.factory"
backup_file "$SETTINGS"

[[ -n "${FIREWORKS_MODEL:-}" ]] || fail "FIREWORKS_MODEL must be set in $LIVE_E2E_ENV_FILE"

fireworks_display_name=""
fireworks_max_tokens="8192"
block_display=""
block_max="8192"
while IFS= read -r line; do
  if [[ "$line" == "  - alias:"* ]]; then
    block_display=""
    block_max="8192"
  fi
  if [[ "$line" =~ 'display_name:[[:space:]]*"([^"]+)"' ]]; then
    block_display="${match[1]}"
  fi
  if [[ "$line" =~ 'max_output_tokens:[[:space:]]*([0-9]+)' ]]; then
    block_max="${match[1]}"
  fi
  if [[ "$line" == *"known_auth: fireworks"* ]]; then
    fireworks_display_name="$block_display"
    fireworks_max_tokens="$block_max"
  fi
done < "$LIVE_E2E_CONFIG"

[[ -n "$fireworks_display_name" ]] || fail "could not read Fireworks display_name from $LIVE_E2E_CONFIG"

fireworks_entry="$(jq -n \
  --arg model "$FIREWORKS_MODEL" \
  --arg displayName "$fireworks_display_name" \
  --argjson maxOutputTokens "$fireworks_max_tokens" \
  '{
    model: $model,
    displayName: $displayName,
    provider: "generic-chat-completion-api",
    baseUrl: "http://127.0.0.1:8787",
    apiKey: "not-required-when-client-auth-disabled",
    maxOutputTokens: $maxOutputTokens
  }')"

if [[ -f "$SETTINGS" ]] && jq empty "$SETTINGS" >/dev/null 2>&1; then
  jq --slurpfile live "$TEMPLATE" \
     --argjson fireworks "$fireworks_entry" \
     '.customModels = ($live[0].customModels + [$fireworks])' "$SETTINGS" > "$TMP"
else
  jq --argjson fireworks "$fireworks_entry" \
     '.customModels += [$fireworks]' "$TEMPLATE" > "$TMP"
fi

mv "$TMP" "$SETTINGS"
chmod 600 "$SETTINGS" 2>/dev/null || true

jq '.customModels[]? | {model, displayName, provider, baseUrl, maxOutputTokens}' "$SETTINGS" \
  | tee "$LIVE_E2E_RUN_DIR/factory-models.after.json"

if jq -e '.customModels[]? | select((.baseUrl // "") | test("127\\.0\\.0\\.1|localhost"; "i")) | select((.baseUrl // "") | test(":8787"; "i") | not)' "$SETTINGS" >/dev/null; then
  fail "$SETTINGS contains localhost custom model baseUrl not pointing at droid-proxy (:8787)"
fi

info "Factory Droid settings updated at $SETTINGS"
