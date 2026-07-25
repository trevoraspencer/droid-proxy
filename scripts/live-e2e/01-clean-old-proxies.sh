#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

PROXY_PROCESS_PATTERN='droid-proxy|cursor-proxy'
PROXY_PORTS=(8787 1455 56121)
SELECT_KILLS="$SCRIPT_DIR/select-proxy-kills.zsh"
[[ -f "$SELECT_KILLS" ]] || fail "missing selector: $SELECT_KILLS"

# Build a tab-separated process table: "<pid>\t<comm>\t<args>". comm is the
# executable (the selector uses its basename); args is recorded for the audit
# log but is NEVER used for selection, so an argv substring can't cause a
# collateral kill.
proxy_process_table() {
  local pid comm args
  ps -axo pid= 2>/dev/null | while IFS= read -r pid; do
    pid="${pid//[[:space:]]/}"
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    comm="$(ps -o comm= -p "$pid" 2>/dev/null)" || continue
    [[ -n "$comm" ]] || continue
    args="$(ps -o args= -p "$pid" 2>/dev/null)" || true
    print -r -- "${pid}\t${comm}\t${args}"
  done
}

# PIDs that own the proxy ports — the most precise signal.
proxy_port_owner_pids() {
  local port
  for port in $PROXY_PORTS; do
    lsof -ti tcp:"$port" 2>/dev/null || true
  done | sort -un
}

# The current shell and all of its ancestors — never terminate ourselves.
self_and_ancestor_pids() {
  local pid="$$" guard=0
  while [[ "$pid" =~ ^[0-9]+$ ]] && (( pid > 1 )) && (( guard < 64 )); do
    print -r -- "$pid"
    pid="$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
    (( guard += 1 ))
  done
}

# Scoped kill-candidate set: pipe the real process table + port owners +
# self/ancestor exclusions through the pure selector.
select_proxy_kills() {
  local owners excludes
  owners="$(proxy_port_owner_pids | tr '\n' ' ')"
  excludes="$(self_and_ancestor_pids | tr '\n' ' ')"
  proxy_process_table \
    | PROXY_PORT_OWNER_PIDS="$owners" \
      PROXY_EXCLUDE_PIDS="$excludes" \
      PROXY_BINARIES="droid-proxy cursor-proxy" \
      zsh "$SELECT_KILLS"
}

kill_pids() {
  local sig="$1" pids="$2" pid
  while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    kill "$sig" "$pid" 2>/dev/null || true
  done <<< "$pids"
}

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

info "Stopping droid-proxy processes (scoped: proxy ports + exact binary basename, excluding self/ancestors)"
candidates_str="$(select_proxy_kills)"
if [[ -n "$candidates_str" ]]; then
  print -r -- "$candidates_str" > "$LIVE_E2E_RUN_DIR/processes.clean.kill-candidates.txt"
  info "Scoped kill candidates: ${candidates_str//$'\n'/ }"
  kill_pids "-TERM" "$candidates_str"
  sleep 1
  survivors_str="$(select_proxy_kills)"
  [[ -n "$survivors_str" ]] && kill_pids "-KILL" "$survivors_str"
else
  : > "$LIVE_E2E_RUN_DIR/processes.clean.kill-candidates.txt"
  info "No scoped proxy processes found"
fi

# Opt-in broad fallback. The default path above never matches by argv substring,
# so this legacy behavior — which can terminate any process with the pattern
# anywhere in its command line — is gated behind an explicit env flag.
if [[ "${LIVE_E2E_FORCE_KILL:-}" == "1" ]]; then
  info "LIVE_E2E_FORCE_KILL=1 — applying broad pkill -f '$PROXY_PROCESS_PATTERN' fallback"
  pkill -f "$PROXY_PROCESS_PATTERN" || true
  sleep 1
fi

# Recheck using the scoped criteria so we never fail on an unrelated repo-path process.
remaining_str="$(select_proxy_kills)"
if [[ -n "$remaining_str" ]]; then
  print -r -- "$remaining_str" | tee "$LIVE_E2E_RUN_DIR/processes.clean.after.txt"
  fail "proxy process still running (scoped): ${remaining_str//$'\n'/ }"
fi
: > "$LIVE_E2E_RUN_DIR/processes.clean.after.txt"

for port in $PROXY_PORTS; do
  port_listeners "$port" | tee "$LIVE_E2E_RUN_DIR/listeners.port-$port.after.txt" || true
done

info "Proxy cleanup complete"
