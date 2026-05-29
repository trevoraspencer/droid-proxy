#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env
ensure_proxy_health
require_cmd jq
require_cmd shasum

API_BASE="http://127.0.0.1:8787"

token_hash() {
  local file="$1"
  local hash_line
  hash_line="$(shasum -a 256 "$file")"
  print -r -- "${hash_line%% *}"
}

run_refresh_check() {
  local provider="$1"
  local model="$2"
  local label="$3"
  local artifact_id token backup tmp body out http_status forced_hash after_hash

  artifact_id="$(model_artifact_id "$model")"

  token="$(token_files_for_provider "$provider" | sed -n '1p')"
  if [[ -z "$token" ]]; then
    append_result "$label" "$model" "oauth refresh" "FAIL" "no $provider token file found"
    return 0
  fi

  backup="$token.live-e2e-backup.$LIVE_E2E_RUN_ID"
  tmp="$token.live-e2e-edit.$$"
  body="$LIVE_E2E_RUN_DIR/$artifact_id.oauth-refresh.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.oauth-refresh.json"

  if [[ -e "$backup" ]]; then
    append_result "$label" "$model" "oauth refresh" "FAIL" "backup already exists"
    return 0
  fi

  if ! cp -p "$token" "$backup"; then
    append_result "$label" "$model" "oauth refresh" "FAIL" "could not back up token"
    return 0
  fi
  chmod 600 "$backup" 2>/dev/null || true

  if ! jq '.expired = "2000-01-01T00:00:00Z"' "$token" > "$tmp"; then
    rm -f "$tmp"
    mv "$backup" "$token" 2>/dev/null || true
    append_result "$label" "$model" "oauth refresh" "FAIL" "could not edit token expiry"
    return 0
  fi
  chmod 600 "$tmp" 2>/dev/null || true
  mv "$tmp" "$token"
  forced_hash="$(token_hash "$token")"

  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    input:"Reply exactly: droid-proxy-ok"
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status"; then
    after_hash="$(token_hash "$token")"
    if [[ "$after_hash" != "$forced_hash" ]]; then
      rm -f "$backup"
      append_result "$label" "$model" "oauth refresh" "PASS" "HTTP $http_status; token file updated"
      return 0
    fi
    mv "$backup" "$token" 2>/dev/null || true
    chmod 600 "$token" 2>/dev/null || true
    append_result "$label" "$model" "oauth refresh" "FAIL" "HTTP $http_status; token file was not updated"
    return 0
  fi

  mv "$backup" "$token" 2>/dev/null || true
  chmod 600 "$token" 2>/dev/null || true
  append_result "$label" "$model" "oauth refresh" "FAIL" "HTTP ${http_status:-000}; restored token backup"
}

run_refresh_check "codex" "gpt-5.2-codex" "ChatGPT/Codex OAuth"
run_refresh_check "xai" "grok-build-0.1" "xAI Grok Build OAuth"

info "OAuth refresh checks complete. Results: $LIVE_E2E_RESULTS_NDJSON"
