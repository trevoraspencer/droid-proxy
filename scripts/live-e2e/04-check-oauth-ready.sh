#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

AUTH_DIR="$(token_auth_dir)"

for port in 1455 56121; do
  if assert_port_free "$port"; then
    info "OAuth callback port $port is free"
  else
    fail "OAuth callback port $port is in use"
  fi
done

[[ -d "$AUTH_DIR" ]] || fail "$AUTH_DIR does not exist; run OAuth login commands from docs/live-e2e/DONE.md"

stat -f '%A %N' "$AUTH_DIR" | tee "$LIVE_E2E_RUN_DIR/oauth-auth-dir.stat"
auth_mode="$(stat -f '%A' "$AUTH_DIR")"
[[ "$auth_mode" == "700" ]] || fail "$AUTH_DIR must have mode 700, got $auth_mode"

for file in "$AUTH_DIR"/*.json(N); do
  stat -f '%A %N' "$file" | tee -a "$LIVE_E2E_RUN_DIR/oauth-token-files.stat"
  mode="$(stat -f '%A' "$file")"
  [[ "$mode" == "600" ]] || fail "$file must have mode 600, got $mode"
done

codex_count="$(token_files_for_provider codex | wc -l | tr -d ' ')"
xai_count="$(token_files_for_provider xai | wc -l | tr -d ' ')"

[[ "$codex_count" -gt 0 ]] || fail "no codex OAuth token found in $AUTH_DIR"
[[ "$xai_count" -gt 0 ]] || fail "no xai OAuth token found in $AUTH_DIR"

info "OAuth token storage is ready"
