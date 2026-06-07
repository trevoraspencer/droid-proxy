#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

PROXY_PROCESS_PATTERN='droid-proxy|cursor-proxy'

info "Recording current proxy state"
pgrep -fl "$PROXY_PROCESS_PATTERN" \
  2>"$LIVE_E2E_RUN_DIR/processes.clean.before.err" \
  | tee "$LIVE_E2E_RUN_DIR/processes.clean.before.txt" || true
lsof -nP -iTCP -sTCP:LISTEN \
  | rg 'droid-proxy|cursor-proxy|:8787|:1455|:56121|:8000|:11434' \
  | tee "$LIVE_E2E_RUN_DIR/listeners.clean.before.txt" || true

if [[ -f "$HOME/.factory/settings.json" ]]; then
  cp -p "$HOME/.factory/settings.json" \
    "$HOME/.factory/settings.json.pre-droid-proxy-live-e2e.$LIVE_E2E_RUN_ID"
fi

info "Stopping existing droid-proxy processes"
pkill -f "$PROXY_PROCESS_PATTERN" || true
sleep 1

if pgrep -fl "$PROXY_PROCESS_PATTERN" > "$LIVE_E2E_RUN_DIR/processes.clean.after.txt" 2>"$LIVE_E2E_RUN_DIR/processes.clean.after.err"; then
  cat "$LIVE_E2E_RUN_DIR/processes.clean.after.txt"
  fail "proxy process still running"
fi

for port in 8787 1455 56121; do
  port_listeners "$port" | tee "$LIVE_E2E_RUN_DIR/listeners.port-$port.after.txt" || true
done

info "Proxy cleanup complete"
