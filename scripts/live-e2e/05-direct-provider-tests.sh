#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env
ensure_proxy_health

API_BASE="http://127.0.0.1:8787"
MODELS_OUT="$LIVE_E2E_RUN_DIR/models.before-direct-tests.json"

if ! curl -fsS \
    --connect-timeout "$LIVE_E2E_CURL_CONNECT_TIMEOUT" \
    --max-time "$LIVE_E2E_CURL_MAX_TIME" \
    "$API_BASE/v1/models" > "$MODELS_OUT"; then
  fail "could not inspect configured model mappings before direct tests"
fi

assert_model_mapping() {
  local alias="$1"
  local expected_upstream="$2"
  local provider="$3"
  local configured_upstream

  if ! configured_upstream="$(jq -r --arg alias "$alias" '[.data[]? | select(.id == $alias) | .upstream_model] | first // empty' "$MODELS_OUT")"; then
    append_result "$provider" "$alias" "configured upstream model" "FAIL" "could not parse /v1/models"
    fail "could not parse configured model mappings before live checks"
  fi
  if [[ "$configured_upstream" == "$expected_upstream" ]]; then
    append_result "$provider" "$alias" "configured upstream model" "PASS" "$expected_upstream"
    return
  fi

  append_result "$provider" "$alias" "configured upstream model" "FAIL" "expected $expected_upstream; got ${configured_upstream:-missing}"
  fail "$alias must map to $expected_upstream before live checks (got ${configured_upstream:-missing})"
}

assert_model_mapping "gpt-5.6" "gpt-5.6-sol" "ChatGPT/Codex OAuth (GPT-5.6 Sol)"
assert_model_mapping "gpt-5.6-fast" "gpt-5.6-sol" "ChatGPT/Codex OAuth (GPT-5.6 Sol Fast)"
assert_model_mapping "grok-4.5" "grok-4.5" "xAI OAuth (Grok 4.5)"
assert_model_mapping "grok-build-0.1" "grok-build-0.1" "xAI OAuth (Grok Build)"
assert_model_mapping "grok-composer-2.5-fast" "grok-composer-2.5-fast" "xAI OAuth (Composer 2.5 Fast)"
assert_model_mapping "grok-4.3" "grok-4.3" "xAI Grok 4.3 OAuth"

require_env_names DEEPSEEK_API_KEY ZAI_CODING_API_KEY FIREWORKS_API_KEY FIREWORKS_MODEL
check_mimo_env_matches_profile

run_chat_model() {
  local model="$1"
  local provider="$2"
  local artifact_id body out http_status call_id

  artifact_id="$(model_artifact_id "$model")"
  info "Testing chat provider $provider ($model)"

  body="$LIVE_E2E_RUN_DIR/$artifact_id.nonstream.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.nonstream.json"
  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    messages:[{role:"user",content:"Reply exactly: droid-proxy-ok"}]
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/chat/completions" "$body" "$out")" && http_ok "$http_status" \
      && jq -e '(.choices[0].message.content // "") | length > 0' "$out" >/dev/null; then
    append_result "$provider" "$model" "direct non-stream" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "direct non-stream" "FAIL" "HTTP $http_status"
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.stream.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.stream.sse"
  jq -n --arg model "$model" '{
    model:$model,
    stream:true,
    messages:[{role:"user",content:"Count from 1 to 5, one number per line."}]
  }' > "$body"

  if http_status="$(post_stream "$API_BASE/v1/chat/completions" "$body" "$out")" && http_ok "$http_status" \
      && rg '^data: ' "$out" >/dev/null && rg 'data: \[DONE\]' "$out" >/dev/null; then
    append_result "$provider" "$model" "direct stream" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "direct stream" "FAIL" "HTTP $http_status"
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.tool-call.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.tool-call.json"
  jq -n --arg model "$model" '{
    model:$model,
    messages:[{role:"user",content:"Do not answer directly. Use the get_weather tool for Indianapolis."}],
    tools:[{
      type:"function",
      function:{
        name:"get_weather",
        description:"Get weather for a city.",
        parameters:{type:"object",properties:{city:{type:"string"}},required:["city"]}
      }
    }],
    tool_choice:"auto"
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/chat/completions" "$body" "$out")" && http_ok "$http_status" \
      && jq -e '(.choices[0].message.tool_calls // []) | length > 0' "$out" >/dev/null; then
    append_result "$provider" "$model" "tool call" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "tool call" "FAIL" "HTTP $http_status"
  fi

  call_id="$(jq -r '.choices[0].message.tool_calls[0].id // empty' "$out" 2>/dev/null || true)"
  if [[ -z "$call_id" ]]; then
    append_result "$provider" "$model" "tool result" "SKIP" "no tool call id"
    return
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.tool-result.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.tool-result.json"
  jq -n --arg model "$model" --slurpfile first "$LIVE_E2E_RUN_DIR/$artifact_id.tool-call.json" '{
    model:$model,
    messages:[
      {role:"user",content:"Do not answer directly. Use the get_weather tool for Indianapolis."},
      {role:"assistant",content:"",tool_calls:$first[0].choices[0].message.tool_calls},
      {role:"tool",tool_call_id:$first[0].choices[0].message.tool_calls[0].id,content:"72F and clear"}
    ]
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/chat/completions" "$body" "$out")" && http_ok "$http_status"; then
    append_result "$provider" "$model" "tool result" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "tool result" "FAIL" "HTTP $http_status"
  fi
}

run_responses_model() {
  local model="$1"
  local provider="$2"
  local artifact_id body out http_status call_id

  artifact_id="$(model_artifact_id "$model")"
  info "Testing Responses provider $provider ($model)"

  body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.nonstream.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.nonstream.json"
  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    input:"Reply exactly: droid-proxy-ok"
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status" \
      && jq -e '(.id // "") | length > 0' "$out" >/dev/null; then
    append_result "$provider" "$model" "direct non-stream" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "direct non-stream" "FAIL" "HTTP $http_status"
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.stream.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.stream.sse"
  jq -n --arg model "$model" '{
    model:$model,
    stream:true,
    input:"Count from 1 to 5, one number per line."
  }' > "$body"

  if http_status="$(post_stream "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status" \
      && rg '^data: ' "$out" >/dev/null && rg 'response.completed|data: \[DONE\]' "$out" >/dev/null; then
    append_result "$provider" "$model" "direct stream" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "direct stream" "FAIL" "HTTP $http_status"
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.tool-call.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.tool-call.json"
  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    input:[{role:"user",content:"Do not answer directly. Use the get_weather tool for Indianapolis."}],
    tools:[{
      type:"function",
      name:"get_weather",
      description:"Get weather for a city.",
      parameters:{type:"object",properties:{city:{type:"string"}},required:["city"]}
    }],
    tool_choice:"auto"
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status" \
      && jq -e '[.output[]? | select(.type == "function_call")] | length > 0' "$out" >/dev/null; then
    append_result "$provider" "$model" "tool call" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "tool call" "FAIL" "HTTP $http_status"
  fi

  call_id="$(jq -r '[.output[]? | select(.type == "function_call") | (.call_id // .id)] | first // empty' "$out" 2>/dev/null || true)"
  if [[ -z "$call_id" ]]; then
    append_result "$provider" "$model" "tool result" "SKIP" "missing call id"
    return
  fi

  body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.tool-result.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.tool-result.json"
  jq -n --arg model "$model" --slurpfile first "$LIVE_E2E_RUN_DIR/$artifact_id.responses.tool-call.json" '{
    model:$model,
    input: (
      [{role:"user",content:"Do not answer directly. Use the get_weather tool for Indianapolis."}]
      + [$first[0].output[]? | select(.type == "function_call")]
      + [{type:"function_call_output",call_id:([$first[0].output[]? | select(.type == "function_call") | (.call_id // .id)] | first),output:"72F and clear"}]
    )
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status"; then
    append_result "$provider" "$model" "tool result" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "tool result" "FAIL" "HTTP $http_status"
  fi
}

run_codex_gpt56_advanced() {
  local model="gpt-5.6"
  local provider="ChatGPT/Codex OAuth (GPT-5.6 Sol)"
  local check="max reasoning and cache sanitization"
  local artifact_id body out http_status

  if [[ "${LIVE_E2E_CODEX_GPT56_ADVANCED:-0}" != "1" ]]; then
    append_result "$provider" "$model" "$check" "SKIP" "set LIVE_E2E_CODEX_GPT56_ADVANCED=1 to run credentialed gate"
    return
  fi

  artifact_id="$(model_artifact_id "$model")"
  body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.max-cache-sanitization.request.json"
  out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.max-cache-sanitization.json"
  jq -n --arg model "$model" '{
    model:$model,
    stream:false,
    input:"Reply exactly: droid-proxy-ok",
    reasoning:{effort:"max"},
    prompt_cache_options:{mode:"explicit"}
  }' > "$body"

  if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status" \
      && jq -e '(.id // "") | length > 0' "$out" >/dev/null; then
    append_result "$provider" "$model" "$check" "PASS" "HTTP $http_status"
  else
    append_result "$provider" "$model" "$check" "FAIL" "HTTP ${http_status:-000}; private OAuth compatibility gate did not validate"
  fi
}

run_xai_reasoning_levels() {
  local model="grok-4.5"
  local provider="xAI OAuth (Grok 4.5)"
  local effort artifact_id body out http_status

  for effort in low medium high; do
    artifact_id="$(model_artifact_id "$model")"
    body="$LIVE_E2E_RUN_DIR/$artifact_id.responses.reasoning-$effort.request.json"
    out="$LIVE_E2E_RUN_DIR/$artifact_id.responses.reasoning-$effort.json"
    jq -n --arg model "$model" --arg effort "$effort" '{
      model:$model,
      stream:false,
      input:"Reply exactly: droid-proxy-ok",
      reasoning:{effort:$effort},
      prompt_cache_key:"droid-proxy-grok-4-5-live-e2e"
    }' > "$body"

    if http_status="$(post_json "$API_BASE/v1/responses" "$body" "$out")" && http_ok "$http_status" \
        && jq -e '(.id // "") | length > 0' "$out" >/dev/null; then
      append_result "$provider" "$model" "reasoning $effort" "PASS" "HTTP $http_status"
    else
      append_result "$provider" "$model" "reasoning $effort" "FAIL" "HTTP ${http_status:-000}"
    fi
  done
}

run_chat_model "deepseek-v4-flash" "DeepSeek"
run_chat_model "glm-5.1" "Z.AI GLM coding"
run_chat_model "mimo-v2.5-pro" "Xiaomi MiMo"
run_chat_model "${FIREWORKS_MODEL}" "Fireworks"

run_responses_model "gpt-5.6" "ChatGPT/Codex OAuth (GPT-5.6 Sol)"
run_responses_model "gpt-5.6-fast" "ChatGPT/Codex OAuth (GPT-5.6 Sol Fast)"
run_codex_gpt56_advanced
run_responses_model "grok-4.5" "xAI OAuth (Grok 4.5)"
run_xai_reasoning_levels
run_responses_model "grok-build-0.1" "xAI OAuth (Grok Build)"
run_responses_model "grok-composer-2.5-fast" "xAI OAuth (Composer 2.5 Fast)"
run_responses_model "grok-4.3" "xAI Grok 4.3 OAuth"

info "Direct provider tests complete. Results: $LIVE_E2E_RESULTS_NDJSON"
