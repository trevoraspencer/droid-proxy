#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env

LEAK_REPORT="$LIVE_E2E_RUN_DIR/secret-scan.txt"
RESULTS_JSON="$LIVE_E2E_RUN_DIR/results.json"
RESULTS_MD="$LIVE_E2E_RUN_DIR/RESULTS.md"

: > "$LEAK_REPORT"

scan_env_value() {
  local name="$1"
  local value="${(P)name-}"
  [[ -n "$value" ]] || return 0

  if rg -n -F "$value" "$LIVE_E2E_RUN_DIR" >/dev/null 2>&1; then
    print -r -- "literal value for $name was found in $LIVE_E2E_RUN_DIR" >> "$LEAK_REPORT"
  fi
}

for name in \
  DEEPSEEK_API_KEY ZAI_CODING_API_KEY ZAI_MAIN_API_KEY ZAI_API_KEY MIMO_API_KEY MIMO_TOKEN_PLAN_CN_API_KEY \
  MIMO_TOKEN_PLAN_SGP_API_KEY MIMO_TOKEN_PLAN_AMS_API_KEY FIREWORKS_API_KEY; do
  scan_env_value "$name"
done

rg -n 'Bearer [A-Za-z0-9._~+/=-]{20,}|sk-[A-Za-z0-9_-]{12,}|xai-[A-Za-z0-9_-]{12,}|"(access_token|refresh_token|id_token)"[[:space:]]*:[[:space:]]*"[^"]{8,}"' \
  "$LIVE_E2E_RUN_DIR" >> "$LEAK_REPORT" 2>/dev/null || true

if [[ -s "$LEAK_REPORT" ]]; then
  append_result "all" "all" "secret scan" "FAIL" "see secret-scan.txt"
else
  append_result "all" "all" "secret scan" "PASS" "no literal configured secrets found"
fi

if [[ -f "$LIVE_E2E_RESULTS_NDJSON" ]]; then
  jq -s '.' "$LIVE_E2E_RESULTS_NDJSON" > "$RESULTS_JSON"
else
  print -r -- "[]" > "$RESULTS_JSON"
fi

matrix_cell() {
  local alias="$1"
  local check="$2"
  local default="${3:-}"
  jq -r --arg alias "$alias" --arg check "$check" --arg default "$default" '
    [.[] | select(.alias == $alias and .check == $check) | .status]
    | if length == 0 then $default else .[-1] end
  ' "$RESULTS_JSON"
}

matrix_status() {
  local alias="$1"
  jq -r --arg alias "$alias" '
    [.[] | select(.alias == $alias) | .status] as $statuses
    | if any($statuses[]; . == "FAIL") then "FAIL"
      elif any($statuses[]; . == "MANUAL") then "MANUAL"
      elif ($statuses | length) == 0 then ""
      elif any($statuses[]; . == "SKIP") then "SKIP"
      else "PASS"
      end
  ' "$RESULTS_JSON"
}

matrix_notes() {
  local alias="$1"
  jq -r --arg alias "$alias" '
    [.[] | select(.alias == $alias and .status != "PASS")
      | "\(.check)=\(.status)\(if (.notes // "") != "" then " (\(.notes))" else "" end)"]
    | join("; ")
    | gsub("\\|"; "/")
  ' "$RESULTS_JSON"
}

matrix_row() {
  local provider="$1"
  local alias="$2"
  local oauth_default="$3"
  print -r -- "| $provider | \`$alias\` | $(matrix_cell "$alias" "direct non-stream") | $(matrix_cell "$alias" "direct stream") | $(matrix_cell "$alias" "tool call") | $(matrix_cell "$alias" "tool result") | $(matrix_cell "$alias" "oauth refresh" "$oauth_default") | $(matrix_cell "$alias" "factory text") | $(matrix_cell "$alias" "factory file task") | $(matrix_status "$alias") | $(matrix_notes "$alias") |"
}

{
  print -r -- "# Live E2E Results"
  print -r -- ""
  print -r -- "- Run id: $LIVE_E2E_RUN_ID"
  print -r -- "- Run dir: $LIVE_E2E_RUN_DIR"
  print -r -- "- Proxy log: $LIVE_E2E_PROXY_LOG"
  print -r -- ""
  print -r -- "## Check Results"
  print -r -- ""
  print -r -- "| Provider | Alias | Check | Status | Notes |"
  print -r -- "| --- | --- | --- | --- | --- |"
  jq -r '.[] | "| \(.provider) | `\(.alias)` | \(.check) | \(.status) | \(.notes) |"' "$RESULTS_JSON"
  print -r -- ""
  print -r -- "## Provider Matrix"
  print -r -- ""
  print -r -- "| Provider | Alias | Direct non-stream | Direct stream | Tool call | Tool result | OAuth refresh | Factory text | Factory file task | Status | Notes |"
  print -r -- "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"
  matrix_row "ChatGPT/Codex OAuth (GPT-5.6 Sol)" "gpt-5.6" ""
  matrix_row "ChatGPT/Codex OAuth (GPT-5.6 Sol Fast)" "gpt-5.6-fast" "N/A"
  matrix_row "xAI OAuth (Grok Build)" "grok-build-0.1" ""
  matrix_row "xAI OAuth (Composer 2.5 Fast)" "grok-composer-2.5-fast" ""
  matrix_row "xAI OAuth (Grok 4.5)" "grok-4.5" ""
  matrix_row "xAI Grok 4.3 OAuth" "grok-4.3" ""
  matrix_row "Z.AI GLM coding" "glm-5.1" "N/A"
  matrix_row "Xiaomi MiMo" "mimo-v2.5-pro" "N/A"
  matrix_row "Fireworks" "${FIREWORKS_MODEL}" "N/A"
  matrix_row "DeepSeek" "deepseek-v4-flash" "N/A"
} > "$RESULTS_MD"

if jq -e '.[] | select(.status == "FAIL")' "$RESULTS_JSON" >/dev/null; then
  warn "One or more checks failed. See $RESULTS_MD"
  exit 1
fi

info "Results written to $RESULTS_MD"
