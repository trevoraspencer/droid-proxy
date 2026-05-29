#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

CONFIG_TEMPLATE="$LIVE_E2E_REPO_ROOT/docs/live-e2e/config.local.yaml.template"
ENV_TEMPLATE="$LIVE_E2E_REPO_ROOT/docs/live-e2e/env.live-e2e.example"

[[ -f "$CONFIG_TEMPLATE" ]] || fail "missing template: $CONFIG_TEMPLATE"
[[ -f "$ENV_TEMPLATE" ]] || fail "missing template: $ENV_TEMPLATE"

backup_file "$LIVE_E2E_CONFIG"
cp "$CONFIG_TEMPLATE" "$LIVE_E2E_CONFIG"
info "Wrote $LIVE_E2E_CONFIG from $CONFIG_TEMPLATE"

if [[ ! -f "$LIVE_E2E_ENV_FILE" ]]; then
  cp "$ENV_TEMPLATE" "$LIVE_E2E_ENV_FILE"
  info "Created $LIVE_E2E_ENV_FILE from $ENV_TEMPLATE"
else
  info "$LIVE_E2E_ENV_FILE already exists; leaving it unchanged"
fi

if rg -n 'sk-[A-Za-z0-9_-]{12,}|Bearer [A-Za-z0-9._~+/=-]{12,}|refresh_token|access_token|id_token' "$LIVE_E2E_CONFIG" >/dev/null 2>&1; then
  fail "$LIVE_E2E_CONFIG appears to contain a literal secret"
fi

info "Config scaffold is ready. Fill $LIVE_E2E_ENV_FILE, then run OAuth commands in docs/live-e2e/DONE.md."
