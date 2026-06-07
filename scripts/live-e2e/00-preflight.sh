#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

info "Run directory: $LIVE_E2E_RUN_DIR"

for cmd in go jq curl lsof pgrep pkill tar find rg make shasum sed ps env stat; do
  require_cmd "$cmd"
done

go version | tee "$LIVE_E2E_RUN_DIR/go-version.txt"
git status --short | tee "$LIVE_E2E_RUN_DIR/git-status.before.txt"

if ! test -z "$(gofmt -l .)"; then
  gofmt -l . | tee "$LIVE_E2E_RUN_DIR/gofmt.before.txt"
  warn "repository has files that need gofmt"
fi

pgrep -fl 'droid-proxy|cursor-proxy' \
  2>"$LIVE_E2E_RUN_DIR/processes.before.err" \
  | tee "$LIVE_E2E_RUN_DIR/processes.before.txt" || true

lsof -nP -iTCP -sTCP:LISTEN \
  | rg 'droid-proxy|cursor-proxy|:8787|:1455|:56121|:8000|:11434' \
  | tee "$LIVE_E2E_RUN_DIR/listeners.before.txt" || true

for port in 8787 1455 56121; do
  if assert_port_free "$port"; then
    info "Port $port is free"
  else
    warn "Port $port is currently in use"
  fi
done

if [[ -f "$HOME/.factory/settings.json" ]]; then
  jq '.customModels[]? | {model, displayName, provider, baseUrl}' \
    "$HOME/.factory/settings.json" \
    | tee "$LIVE_E2E_RUN_DIR/factory-models.before.json" || true
else
  warn "$HOME/.factory/settings.json does not exist yet"
fi

load_local_env

require_env_names DEEPSEEK_API_KEY ZAI_CODING_API_KEY FIREWORKS_API_KEY FIREWORKS_MODEL || true
require_one_of_env MIMO_API_KEY MIMO_TOKEN_PLAN_CN_API_KEY MIMO_TOKEN_PLAN_SGP_API_KEY MIMO_TOKEN_PLAN_AMS_API_KEY || true

info "Preflight complete. Add secrets/OAuth using scripts/live-e2e/README.md before the final live run."
