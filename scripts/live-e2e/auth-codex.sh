#!/usr/bin/env zsh

set -euo pipefail
unsetopt bgnice

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env

[[ -x "$LIVE_E2E_REPO_ROOT/droid-proxy" ]] || fail "$LIVE_E2E_REPO_ROOT/droid-proxy does not exist; run scripts/live-e2e/03-build-and-start.sh first"
[[ -f "$LIVE_E2E_CONFIG" ]] || fail "$LIVE_E2E_CONFIG does not exist; run scripts/live-e2e/02-generate-config.sh first"

require_env_names DEEPSEEK_API_KEY ZAI_CODING_API_KEY FIREWORKS_API_KEY FIREWORKS_MODEL
check_mimo_env_matches_profile

info "Starting Codex/ChatGPT OAuth login"
exec "$LIVE_E2E_REPO_ROOT/droid-proxy" auth codex --config "$LIVE_E2E_CONFIG" "$@"
