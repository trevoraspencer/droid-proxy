#!/usr/bin/env zsh

set -euo pipefail
unsetopt bgnice

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env

[[ -f "$LIVE_E2E_CONFIG" ]] || fail "$LIVE_E2E_CONFIG does not exist; run scripts/live-e2e/02-generate-config.sh first"

require_env_names DEEPSEEK_API_KEY ZAI_CODING_API_KEY FIREWORKS_API_KEY FIREWORKS_MODEL
check_mimo_env_matches_profile

if ! test -z "$(gofmt -l .)"; then
  gofmt -l .
  fail "gofmt found unformatted files"
fi

go test ./... | tee "$LIVE_E2E_RUN_DIR/go-test.txt"
go vet ./... | tee "$LIVE_E2E_RUN_DIR/go-vet.txt"
make build | tee "$LIVE_E2E_RUN_DIR/make-build.txt"

stop_proxy_from_pid_file
stop_matching_live_proxies

if ! assert_port_free 8787; then
  fail "port 8787 is already in use"
fi

info "Starting droid-proxy with $LIVE_E2E_CONFIG and $LIVE_E2E_ENV_FILE"
nohup "$LIVE_E2E_REPO_ROOT/droid-proxy" --config "$LIVE_E2E_CONFIG" --env-file "$LIVE_E2E_ENV_FILE" \
  > "$LIVE_E2E_PROXY_LOG" 2>&1 < /dev/null &
proxy_pid="$!"
disown "$proxy_pid" 2>/dev/null || true
print -r -- "$proxy_pid" > "$LIVE_E2E_PROXY_PID_FILE"

sleep 2
if ! kill -0 "$proxy_pid" 2>/dev/null; then
  tail -80 "$LIVE_E2E_PROXY_LOG" >&2 || true
  fail "droid-proxy exited during startup"
fi

ensure_proxy_health
curl -sS \
  --connect-timeout "$LIVE_E2E_CURL_CONNECT_TIMEOUT" \
  --max-time "$LIVE_E2E_CURL_MAX_TIME" \
  "http://127.0.0.1:8787/v1/models" \
  | tee "$LIVE_E2E_RUN_DIR/models.json" \
  | jq '.data[] | {id, factory_provider, upstream_protocol, agent_ready}'

info "droid-proxy is running as pid $proxy_pid"
info "Proxy log: $LIVE_E2E_PROXY_LOG"
