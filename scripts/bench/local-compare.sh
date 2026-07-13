#!/usr/bin/env bash
# Local proxy-overhead benchmark: droid-proxy vs a direct connection, against
# the deterministic droid-bench mock upstream. Produces latency/throughput
# comparison tables and runs the prompt-cache fidelity checks.
#
# Usage: bash scripts/bench/local-compare.sh [--quick]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

MOCK_ADDR="127.0.0.1:18100"
PROXY_ADDR="127.0.0.1:18787"
OUT_DIR="${BENCH_OUT_DIR:-bench-results}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/droid-bench.XXXXXX")"
QUICK=0
[[ "${1:-}" == "--quick" ]] && QUICK=1

info() { printf '[local-compare] %s\n' "$*"; }

cleanup() {
  [[ -n "${PROXY_PID:-}" ]] && kill "$PROXY_PID" 2>/dev/null || true
  [[ -n "${MOCK_PID:-}" ]] && kill "$MOCK_PID" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

info "building droid-proxy and droid-bench"
go build -o "$WORK_DIR/droid-proxy" ./cmd/droid-proxy
go build -o "$WORK_DIR/droid-bench" ./cmd/droid-bench

mkdir -p "$OUT_DIR"

# --- mock upstream -----------------------------------------------------------
# TTFT/inter-chunk delays simulate a fast provider; keep them small so the
# proxy's own overhead is visible in the deltas rather than drowned out.
"$WORK_DIR/droid-bench" mock \
  --listen "$MOCK_ADDR" --ttft 5ms --inter-chunk 2ms --chunks 40 \
  >"$OUT_DIR/mock.log" 2>&1 &
MOCK_PID=$!

# --- droid-proxy pointed at the mock ----------------------------------------
cat >"$WORK_DIR/config.yaml" <<EOF
listen:
  host: 127.0.0.1
  port: 18787
logging:
  level: warn
oauth:
  auth_dir: $WORK_DIR/auth
models:
  - alias: bench-chat
    display_name: "Bench Chat (mock)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: mock-model
    base_url: http://$MOCK_ADDR
    extra_args:
      reasoning_effort: high
      thinking:
        type: enabled
  - alias: bench-anthropic
    display_name: "Bench Anthropic native (mock)"
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    upstream_model: mock-claude
    base_url: http://$MOCK_ADDR
  - alias: bench-anthropic-xlat
    display_name: "Bench Anthropic translated (mock)"
    factory_provider: anthropic
    upstream_protocol: openai-chat
    upstream_model: mock-model
    base_url: http://$MOCK_ADDR
EOF

"$WORK_DIR/droid-proxy" --config "$WORK_DIR/config.yaml" >"$OUT_DIR/proxy.log" 2>&1 &
PROXY_PID=$!

wait_healthy() {
  local url=$1 name=$2
  for _ in $(seq 1 50); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      info "$name is up"
      return 0
    fi
    sleep 0.2
  done
  info "FATAL: $name did not become healthy ($url)"
  exit 1
}
wait_healthy "http://$MOCK_ADDR/health" "mock upstream"
wait_healthy "http://$PROXY_ADDR/health" "droid-proxy"

# --- benchmark config --------------------------------------------------------
REQS=30; CREQS=32; STREAM_REQS=20
if [[ $QUICK -eq 1 ]]; then REQS=8; CREQS=8; STREAM_REQS=6; fi

cat >"$WORK_DIR/bench.yaml" <<EOF
targets:
  - name: direct-mock
    base_url: http://$MOCK_ADDR
    model: mock-model
    baseline: true
    model_by_protocol:
      anthropic-messages: mock-claude
  - name: droid-proxy
    base_url: http://$PROXY_ADDR
    model: bench-chat
    model_by_protocol:
      anthropic-messages: bench-anthropic

scenarios:
  - name: chat-small-nonstream
    protocol: openai-chat
    requests: $REQS
    warmup: 3
    system_prompt_bytes: 2048
    user_message_bytes: 256
    unique_prompts: true

  - name: chat-agentic-stream
    protocol: openai-chat
    stream: true
    requests: $STREAM_REQS
    warmup: 2
    system_prompt_bytes: 16384
    user_message_bytes: 1024
    history_turns: 8
    include_tools: true

  - name: chat-large-context-nonstream
    protocol: openai-chat
    requests: $STREAM_REQS
    warmup: 2
    system_prompt_bytes: 65536
    user_message_bytes: 2048
    history_turns: 16
    include_tools: true

  - name: anthropic-agentic-stream
    protocol: anthropic-messages
    stream: true
    requests: $STREAM_REQS
    warmup: 2
    system_prompt_bytes: 16384
    user_message_bytes: 1024
    history_turns: 8
    include_tools: true
    cache_control: true

  - name: chat-cache-growth
    protocol: openai-chat
    requests: 16
    system_prompt_bytes: 16384
    user_message_bytes: 512
    history_turns: 16
    growing_conversation: true

  - name: chat-concurrent-stream
    protocol: openai-chat
    stream: true
    requests: $CREQS
    warmup: 4
    concurrency: 8
    system_prompt_bytes: 8192
    user_message_bytes: 512
    history_turns: 4
EOF

info "running benchmark scenarios (results in $OUT_DIR/)"
"$WORK_DIR/droid-bench" run \
  --config "$WORK_DIR/bench.yaml" \
  --json "$OUT_DIR/local-compare.json" \
  --md "$OUT_DIR/local-compare.md" \
  | tee "$OUT_DIR/local-compare.txt"

# Translated path measured separately: same anthropic client workload, but the
# proxy translates to openai-chat upstream. Compare against droid-proxy's
# native anthropic row above to see the translation tax.
cat >"$WORK_DIR/bench-xlat.yaml" <<EOF
targets:
  - name: droid-proxy-xlat
    base_url: http://$PROXY_ADDR
    model: bench-anthropic-xlat
scenarios:
  - name: anthropic-translated-stream
    protocol: anthropic-messages
    stream: true
    requests: $STREAM_REQS
    warmup: 2
    system_prompt_bytes: 16384
    user_message_bytes: 1024
    history_turns: 8
    include_tools: true
    cache_control: true
EOF
"$WORK_DIR/droid-bench" run \
  --config "$WORK_DIR/bench-xlat.yaml" \
  --json "$OUT_DIR/local-compare-xlat.json" \
  --md "$OUT_DIR/local-compare-xlat.md" \
  | tee -a "$OUT_DIR/local-compare.txt"

info "running prompt-cache fidelity checks"
"$WORK_DIR/droid-bench" cache-check \
  --proxy "http://$PROXY_ADDR" \
  --mock "http://$MOCK_ADDR" \
  --chat-model bench-chat \
  --anthropic-model bench-anthropic \
  --anthropic-translated-model bench-anthropic-xlat \
  | tee "$OUT_DIR/fidelity.txt"

info "done — reports: $OUT_DIR/local-compare.{txt,md,json}, $OUT_DIR/fidelity.txt"
