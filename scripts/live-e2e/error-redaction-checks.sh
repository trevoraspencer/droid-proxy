#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env
ensure_proxy_health

API_BASE="$LIVE_E2E_PROXY_URL"

result_has_env_value() {
  local file="$1"
  local name value
  for name in \
    DEEPSEEK_API_KEY ZAI_CODING_API_KEY ZAI_MAIN_API_KEY ZAI_API_KEY MIMO_API_KEY MIMO_TOKEN_PLAN_CN_API_KEY \
    MIMO_TOKEN_PLAN_SGP_API_KEY MIMO_TOKEN_PLAN_AMS_API_KEY FIREWORKS_API_KEY; do
    value="${(P)name-}"
    [[ -n "$value" ]] || continue
    if rg -F "$value" "$file" >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}

run_invalid_alias_check() {
  local body out http_status
  body="$LIVE_E2E_RUN_DIR/invalid-model.request.json"
  out="$LIVE_E2E_RUN_DIR/invalid-model.response.json"

  jq -n '{
    model:"droid-live-e2e-missing",
    stream:false,
    messages:[{role:"user",content:"This request should fail before reaching an upstream provider."}]
  }' > "$body"

  http_status="$(post_json "$API_BASE/v1/chat/completions" "$body" "$out" || true)"
  if [[ "$http_status" == 4* ]] && ! result_has_env_value "$out"; then
    append_result "all" "all" "invalid model redaction" "PASS" "HTTP $http_status"
  else
    append_result "all" "all" "invalid model redaction" "FAIL" "HTTP $http_status"
  fi
}

run_missing_env_check() {
  local out err exit_code
  out="$LIVE_E2E_RUN_DIR/missing-deepseek-env.stdout"
  err="$LIVE_E2E_RUN_DIR/missing-deepseek-env.stderr"

  set +e
  env -u DEEPSEEK_API_KEY "$LIVE_E2E_REPO_ROOT/droid-proxy" --config "$LIVE_E2E_CONFIG" > "$out" 2> "$err"
  exit_code="$?"
  set -e

  if [[ "$exit_code" != "0" ]] && rg 'DEEPSEEK_API_KEY' "$err" >/dev/null 2>&1 && ! result_has_env_value "$err"; then
    append_result "DeepSeek" "deepseek-v4-flash" "missing env redaction" "PASS" "exit $exit_code"
  else
    append_result "DeepSeek" "deepseek-v4-flash" "missing env redaction" "FAIL" "exit $exit_code"
  fi
}

run_missing_oauth_token_check() {
  local provider="$1"
  local model="$2"
  local label="$3"
  local artifact_id auth_dir move_dir token body out http_status
  local -a tokens

  artifact_id="$(model_artifact_id "$model")"

  tokens=("${(@f)$(token_files_for_provider "$provider")}")
  if (( ${#tokens[@]} == 0 )); then
    append_result "$label" "$model" "missing oauth token redaction" "FAIL" "no $provider token file found"
    return 0
  fi

  auth_dir="$(token_auth_dir)"
  move_dir="$auth_dir/.live-e2e-moved-$provider-$LIVE_E2E_RUN_ID"
  if [[ -e "$move_dir" ]]; then
    append_result "$label" "$model" "missing oauth token redaction" "FAIL" "temporary token dir already exists"
    return 0
  fi
  mkdir -m 700 "$move_dir"

  body="$LIVE_E2E_RUN_DIR/$artifact_id.missing-token.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.missing-token.json"
  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    input:"This request should fail because the token file is temporarily absent."
  }' > "$body"

  for token in "${tokens[@]}"; do
    mv "$token" "$move_dir/$(basename "$token")"
  done
  http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out" || true)"
  for token in "$move_dir"/*.json(N); do
    mv "$token" "$auth_dir/$(basename "$token")"
    chmod 600 "$auth_dir/$(basename "$token")" 2>/dev/null || true
  done
  rmdir "$move_dir" 2>/dev/null || true

  if [[ "$http_status" == "401" || "$http_status" == "403" ]] && ! result_has_env_value "$out"; then
    append_result "$label" "$model" "missing oauth token redaction" "PASS" "HTTP $http_status"
  else
    append_result "$label" "$model" "missing oauth token redaction" "FAIL" "HTTP $http_status"
  fi
}

run_invalid_alias_check
run_missing_env_check
run_missing_oauth_token_check "codex" "gpt-5.6" "ChatGPT/Codex OAuth (GPT-5.6 Sol)"
run_missing_oauth_token_check "xai" "grok-build-0.1" "xAI OAuth"
run_missing_oauth_token_check "xai" "grok-composer-2.5-fast" "xAI OAuth (Composer 2.5 Fast)"
run_missing_oauth_token_check "xai" "grok-4.5" "xAI OAuth (Grok 4.5)"

info "Error and redaction checks complete. Results: $LIVE_E2E_RESULTS_NDJSON"
