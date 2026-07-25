#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

info "Using run directory: $LIVE_E2E_RUN_DIR"

if [[ ! -f "$LIVE_E2E_CONFIG" ]]; then
  "$SCRIPT_DIR/02-generate-config.sh"
fi

"$SCRIPT_DIR/03-build-and-start.sh"
"$SCRIPT_DIR/04-check-oauth-ready.sh"
"$SCRIPT_DIR/05-direct-provider-tests.sh"
"$SCRIPT_DIR/oauth-refresh-check.sh"
"$SCRIPT_DIR/error-redaction-checks.sh"
"$SCRIPT_DIR/06-write-factory-settings.sh"
"$SCRIPT_DIR/factory-manual-evidence.sh"
"$SCRIPT_DIR/07-redaction-and-results.sh"

info "Post-secret live E2E scaffold run complete"
info "Factory Droid manual evidence checklist is in $LIVE_E2E_RUN_DIR/factory-manual-evidence.md"
