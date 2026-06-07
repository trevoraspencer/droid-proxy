#!/usr/bin/env zsh

set -euo pipefail

_live_e2e_lib_path="${(%):-%N}"
typeset -g LIVE_E2E_SCRIPT_DIR="${LIVE_E2E_SCRIPT_DIR:-${_live_e2e_lib_path:A:h}}"
typeset -g LIVE_E2E_REPO_ROOT="${LIVE_E2E_REPO_ROOT:-$(cd "$LIVE_E2E_SCRIPT_DIR/../.." && pwd)}"
typeset -g LIVE_E2E_BASE_DIR="${LIVE_E2E_BASE_DIR:-$LIVE_E2E_REPO_ROOT/.factory/validation/live-e2e}"
typeset -g LIVE_E2E_RUN_ID="${LIVE_E2E_RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
typeset -g LIVE_E2E_RUN_DIR="${LIVE_E2E_RUN_DIR:-$LIVE_E2E_BASE_DIR/$LIVE_E2E_RUN_ID}"
typeset -g LIVE_E2E_RESULTS_NDJSON="${LIVE_E2E_RESULTS_NDJSON:-$LIVE_E2E_RUN_DIR/results.ndjson}"
typeset -g LIVE_E2E_CONFIG="${LIVE_E2E_CONFIG:-$LIVE_E2E_REPO_ROOT/config.local.yaml}"
if [[ -z "${LIVE_E2E_ENV_FILE:-}" ]]; then
  print -u2 -r -- "[live-e2e] ERROR: LIVE_E2E_ENV_FILE is required."
  print -u2 -r -- '[live-e2e] Example: export LIVE_E2E_ENV_FILE="$HOME/.droid-proxy/live-e2e/secrets.env"'
  exit 1
fi
typeset -g LIVE_E2E_ENV_FILE

assert_live_e2e_env_file_safe() {
  local env_dir env_real repo_real rel

  env_dir="${LIVE_E2E_ENV_FILE:h}"
  [[ -n "$env_dir" ]] || fail "LIVE_E2E_ENV_FILE has no directory component: $LIVE_E2E_ENV_FILE"
  env_real="$(cd "$env_dir" && pwd -P)/$(basename "$LIVE_E2E_ENV_FILE")"
  repo_real="$(cd "$LIVE_E2E_REPO_ROOT" && pwd -P)"

  case "$env_real" in
    "$repo_real"/*)
      case "$env_real" in
        *.example) ;;
        *)
          fail "LIVE_E2E_ENV_FILE must stay outside the repo (use e.g. \$HOME/.droid-proxy/live-e2e/secrets.env). Got: $LIVE_E2E_ENV_FILE"
          ;;
      esac
      rel="${env_real#"$repo_real"/}"
      if git -C "$LIVE_E2E_REPO_ROOT" ls-files --error-unmatch "$rel" >/dev/null 2>&1; then
        fail "LIVE_E2E_ENV_FILE is tracked by git: $rel"
      fi
      ;;
  esac
}

assert_live_e2e_env_file_safe
typeset -g LIVE_E2E_PROXY_PID_FILE="${LIVE_E2E_PROXY_PID_FILE:-$LIVE_E2E_RUN_DIR/proxy.pid}"
typeset -g LIVE_E2E_PROXY_LOG="${LIVE_E2E_PROXY_LOG:-$LIVE_E2E_RUN_DIR/proxy.log}"
typeset -g LIVE_E2E_CURRENT_RUN_ENV="${LIVE_E2E_CURRENT_RUN_ENV:-$LIVE_E2E_RUN_DIR/current-run.env}"
typeset -g LIVE_E2E_CURL_CONNECT_TIMEOUT="${LIVE_E2E_CURL_CONNECT_TIMEOUT:-10}"
typeset -g LIVE_E2E_CURL_MAX_TIME="${LIVE_E2E_CURL_MAX_TIME:-300}"
typeset -g LIVE_E2E_STREAM_MAX_TIME="${LIVE_E2E_STREAM_MAX_TIME:-600}"

export LIVE_E2E_SCRIPT_DIR LIVE_E2E_REPO_ROOT LIVE_E2E_BASE_DIR LIVE_E2E_RUN_ID
export LIVE_E2E_RUN_DIR LIVE_E2E_RESULTS_NDJSON LIVE_E2E_CONFIG LIVE_E2E_ENV_FILE
export LIVE_E2E_PROXY_PID_FILE LIVE_E2E_PROXY_LOG LIVE_E2E_CURRENT_RUN_ENV
export LIVE_E2E_CURL_CONNECT_TIMEOUT LIVE_E2E_CURL_MAX_TIME LIVE_E2E_STREAM_MAX_TIME

mkdir -p "$LIVE_E2E_RUN_DIR"
mkdir -p "$LIVE_E2E_BASE_DIR"
print -r -- "$LIVE_E2E_RUN_DIR" > "$LIVE_E2E_BASE_DIR/latest-run-dir"

write_current_run_env() {
  {
    print -r -- "export LIVE_E2E_SCRIPT_DIR=${(q)LIVE_E2E_SCRIPT_DIR}"
    print -r -- "export LIVE_E2E_REPO_ROOT=${(q)LIVE_E2E_REPO_ROOT}"
    print -r -- "export LIVE_E2E_BASE_DIR=${(q)LIVE_E2E_BASE_DIR}"
    print -r -- "export LIVE_E2E_RUN_ID=${(q)LIVE_E2E_RUN_ID}"
    print -r -- "export LIVE_E2E_RUN_DIR=${(q)LIVE_E2E_RUN_DIR}"
    print -r -- "export LIVE_E2E_RESULTS_NDJSON=${(q)LIVE_E2E_RESULTS_NDJSON}"
    print -r -- "export LIVE_E2E_CONFIG=${(q)LIVE_E2E_CONFIG}"
    print -r -- "export LIVE_E2E_ENV_FILE=${(q)LIVE_E2E_ENV_FILE}"
    print -r -- "export LIVE_E2E_PROXY_PID_FILE=${(q)LIVE_E2E_PROXY_PID_FILE}"
    print -r -- "export LIVE_E2E_PROXY_LOG=${(q)LIVE_E2E_PROXY_LOG}"
    print -r -- "export LIVE_E2E_CURRENT_RUN_ENV=${(q)LIVE_E2E_CURRENT_RUN_ENV}"
    print -r -- "export LIVE_E2E_CURL_CONNECT_TIMEOUT=${(q)LIVE_E2E_CURL_CONNECT_TIMEOUT}"
    print -r -- "export LIVE_E2E_CURL_MAX_TIME=${(q)LIVE_E2E_CURL_MAX_TIME}"
    print -r -- "export LIVE_E2E_STREAM_MAX_TIME=${(q)LIVE_E2E_STREAM_MAX_TIME}"
  } > "$LIVE_E2E_CURRENT_RUN_ENV"
}

write_current_run_env

info() {
  print -u2 -r -- "[live-e2e] $*"
}

warn() {
  print -u2 -r -- "[live-e2e] WARN: $*"
}

fail() {
  print -u2 -r -- "[live-e2e] ERROR: $*"
  exit 1
}

# model_artifact_id turns a provider model ID into a safe filename stem.
# Fireworks IDs like accounts/fireworks/models/kimi-k2p6 must not be used raw in paths.
model_artifact_id() {
  print -r -- "${1//\//__}"
}

require_cmd() {
  local name="$1"
  command -v "$name" >/dev/null 2>&1 || fail "required command not found: $name"
}

load_local_env() {
  if [[ -f "$LIVE_E2E_ENV_FILE" ]]; then
    info "Loading environment names from $LIVE_E2E_ENV_FILE"
    setopt allexport
    source "$LIVE_E2E_ENV_FILE"
    unsetopt allexport
  else
    warn "$LIVE_E2E_ENV_FILE does not exist yet"
  fi
  infer_mimo_known_auth
}

infer_mimo_known_auth() {
  if [[ -n "${MIMO_KNOWN_AUTH:-}" ]]; then
    export MIMO_KNOWN_AUTH
    return
  fi

  if [[ -n "${MIMO_API_KEY:-}" ]]; then
    export MIMO_KNOWN_AUTH="mimo"
  elif [[ -n "${MIMO_TOKEN_PLAN_CN_API_KEY:-}" ]]; then
    export MIMO_KNOWN_AUTH="mimo-token-plan-cn"
  elif [[ -n "${MIMO_TOKEN_PLAN_SGP_API_KEY:-}" ]]; then
    export MIMO_KNOWN_AUTH="mimo-token-plan-sgp"
  elif [[ -n "${MIMO_TOKEN_PLAN_AMS_API_KEY:-}" ]]; then
    export MIMO_KNOWN_AUTH="mimo-token-plan-ams"
  fi
}

require_env_names() {
  local -a missing
  local name value

  for name in "$@"; do
    value="${(P)name-}"
    if [[ -z "$value" ]]; then
      missing+=("$name")
    else
      info "$name is set"
    fi
  done

  if (( ${#missing[@]} > 0 )); then
    print -u2 -r -- "[live-e2e] Missing required environment names:"
    print -u2 -r -- "  ${(j: :)missing}"
    return 1
  fi
}

require_one_of_env() {
  local -a names
  names=("$@")
  local name value

  for name in "${names[@]}"; do
    value="${(P)name-}"
    if [[ -n "$value" ]]; then
      info "$name is set"
      return 0
    fi
  done

  print -u2 -r -- "[live-e2e] Missing one of:"
  print -u2 -r -- "  ${(j: :)names}"
  return 1
}

check_mimo_env_matches_profile() {
  infer_mimo_known_auth
  case "${MIMO_KNOWN_AUTH:-mimo}" in
    mimo)
      require_env_names MIMO_API_KEY
      ;;
    mimo-token-plan-cn)
      require_env_names MIMO_TOKEN_PLAN_CN_API_KEY
      ;;
    mimo-token-plan-sgp)
      require_env_names MIMO_TOKEN_PLAN_SGP_API_KEY
      ;;
    mimo-token-plan-ams)
      require_env_names MIMO_TOKEN_PLAN_AMS_API_KEY
      ;;
    *)
      fail "unsupported MIMO_KNOWN_AUTH=${MIMO_KNOWN_AUTH:-}"
      ;;
  esac
}

backup_file() {
  local target="$1"
  if [[ -e "$target" ]]; then
    local backup="$target.live-e2e-backup.$(date +%Y%m%d-%H%M%S)"
    cp -p "$target" "$backup"
    info "Backed up $target to $backup"
  fi
}

port_listeners() {
  local port="$1"
  lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true
}

assert_port_free() {
  local port="$1"
  if port_listeners "$port" | rg . >/dev/null 2>&1; then
    port_listeners "$port" >&2
    return 1
  fi
}

append_result() {
  local provider="$1"
  local alias="$2"
  local check="$3"
  local result_status="$4"
  local notes="${5:-}"

  jq -n \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg provider "$provider" \
    --arg alias "$alias" \
    --arg check "$check" \
    --arg status "$result_status" \
    --arg notes "$notes" \
    '{timestamp:$ts, provider:$provider, alias:$alias, check:$check, status:$status, notes:$notes}' \
    >> "$LIVE_E2E_RESULTS_NDJSON"
}

http_ok() {
  local http_status="$1"
  [[ "$http_status" == 2* ]]
}

post_json() {
  local url="$1"
  local body_file="$2"
  local out_file="$3"
  local http_status

  if ! http_status="$(curl -sS \
      --connect-timeout "$LIVE_E2E_CURL_CONNECT_TIMEOUT" \
      --max-time "$LIVE_E2E_CURL_MAX_TIME" \
      -w "%{http_code}" -o "$out_file" \
      -H "Content-Type: application/json" \
      --data-binary @"$body_file" \
      "$url")"; then
    print -r -- "000"
    return 1
  fi
  print -r -- "$http_status"
}

post_stream() {
  local url="$1"
  local body_file="$2"
  local out_file="$3"
  local http_status

  if ! http_status="$(curl -sS -N \
      --connect-timeout "$LIVE_E2E_CURL_CONNECT_TIMEOUT" \
      --max-time "$LIVE_E2E_STREAM_MAX_TIME" \
      -w "%{http_code}" -o "$out_file" \
      -H "Content-Type: application/json" \
      --data-binary @"$body_file" \
      "$url")"; then
    print -r -- "000"
    return 1
  fi
  print -r -- "$http_status"
}

ensure_proxy_health() {
  curl -sS \
    --connect-timeout "$LIVE_E2E_CURL_CONNECT_TIMEOUT" \
    --max-time "$LIVE_E2E_CURL_MAX_TIME" \
    "http://127.0.0.1:8787/health" > "$LIVE_E2E_RUN_DIR/health.json" \
    || fail "proxy is not healthy on http://127.0.0.1:8787"
}

token_auth_dir() {
  print -r -- "$HOME/.droid-proxy/auth"
}

token_files_for_provider() {
  local provider="$1"
  local auth_dir
  auth_dir="$(token_auth_dir)"
  [[ -d "$auth_dir" ]] || return 0
  find "$auth_dir" -maxdepth 1 -type f -name '*.json' -print 2>/dev/null \
    | while IFS= read -r file; do
        if jq -e --arg provider "$provider" '(.type // "" | ascii_downcase) == $provider' "$file" >/dev/null 2>&1; then
          print -r -- "$file"
        fi
      done
}

stop_proxy_from_pid_file() {
  if [[ ! -f "$LIVE_E2E_PROXY_PID_FILE" ]]; then
    return 0
  fi

  local pid
  pid="$(<"$LIVE_E2E_PROXY_PID_FILE")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    info "Stopping previous droid-proxy pid $pid"
    kill "$pid" 2>/dev/null || true
    for _ in {1..20}; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.25
    done
    if kill -0 "$pid" 2>/dev/null; then
      warn "droid-proxy pid $pid did not stop within 5s"
    fi
  fi
  rm -f "$LIVE_E2E_PROXY_PID_FILE"
}

stop_matching_live_proxies() {
  local stopped_file="$LIVE_E2E_RUN_DIR/stopped-live-proxies.txt"
  local repo_real config_dir config_base config_real pid command

  : > "$stopped_file"
  repo_real="$(cd "$LIVE_E2E_REPO_ROOT" && pwd -P)"
  config_dir="${LIVE_E2E_CONFIG:h}"
  config_base="${LIVE_E2E_CONFIG:t}"
  config_real="$(cd "$config_dir" 2>/dev/null && pwd -P)/$config_base"

  ps -axo pid=,command= | while read -r pid command; do
    [[ -n "$pid" && "$pid" != "$$" ]] || continue
    [[ "$command" == *"droid-proxy"* ]] || continue
    [[ "$command" == *"$repo_real/droid-proxy"* ]] || continue
    [[ "$command" == *"--config $LIVE_E2E_CONFIG"* ||
       "$command" == *"--config=$LIVE_E2E_CONFIG"* ||
       "$command" == *"--config $config_real"* ||
       "$command" == *"--config=$config_real"* ||
       "$command" == *"--config config.local.yaml"* ]] || continue

    print -r -- "$pid $command" >> "$stopped_file"
    info "Stopping previous live droid-proxy pid $pid"
    kill "$pid" 2>/dev/null || true
  done

  if [[ -s "$stopped_file" ]]; then
    sleep 1
  fi
}
