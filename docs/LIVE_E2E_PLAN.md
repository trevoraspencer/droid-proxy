# Live E2E provider test plan

This plan verifies `droid-proxy` against the live providers that matter for this
repo and removes local legacy-proxy copies from the test machine so they cannot
mask failures, bind the same ports, or leave stale Factory Droid settings
behind.

Target date: 2026-05-29

## Goals

- Prove live end-to-end behavior for:
  - ChatGPT/Codex OAuth through `codex-responses`
  - xAI OAuth through `xai-responses` (`grok-build-0.1` and `grok-4.3`)
  - Z.AI GLM coding plan through OpenAI-compatible chat
  - Fireworks through OpenAI-compatible chat
  - DeepSeek through OpenAI-compatible chat with reasoning replay
  - Xiaomi MiMo V2.5 Pro through OpenAI-compatible chat with `api-key` auth
    and reasoning replay
- Exercise direct HTTP proxy calls and real Factory Droid coding-agent flows.
- Confirm old local legacy-proxy installs are removed or
  quarantined before testing begins.
- Capture reproducible pass/fail evidence without committing secrets, token
  files, or private logs.

## Non-goals

- No image, video, websocket, quota dashboard, multi-account balancing, or UI
  parity testing.
- No benchmarking beyond basic latency notes.
- No attempt to make droid-proxy match every behavior of previous proxy tools.

## References to confirm before spending tokens

Provider model names and account entitlements drift. Before the live run, verify
the exact model IDs in the provider dashboard or docs and adjust `config.local.yaml`.

- OpenAI Codex/ChatGPT sign-in: https://help.openai.com/en/articles/11381614-api-codex-cli-and-sign-in-with-chatgpt
- OpenAI Codex CLI overview: https://help.openai.com/en/articles/11096431
- xAI Grok Build model docs: https://docs.x.ai/developers/models/grok-build-0.1
- xAI Grok 4.3 model docs: https://docs.x.ai/developers/models/grok-4.3
- xAI reasoning docs: https://docs.x.ai/developers/model-capabilities/text/reasoning
- Z.AI GLM-4.6 docs: https://docs.z.ai/guides/llm/glm-4.6
- Z.AI chat completions API: https://docs.z.ai/api-reference/llm/chat-completion
- Z.AI GLM Coding Plan quick start: https://docs.z.ai/devpack/quick-start
- Fireworks chat completions API: https://docs.fireworks.ai/api-reference/post-chatcompletions
- DeepSeek chat completions API: https://api-docs.deepseek.com/api/create-chat-completion
- DeepSeek model/pricing page: https://api-docs.deepseek.com/quick_start/pricing
- Xiaomi MiMo repo example: [`docs/examples/mimo.md`](examples/mimo.md)
- Xiaomi MiMo provider matrix: [`docs/PROVIDERS.md`](PROVIDERS.md#xiaomi-mimo)

## Scaffolded run

The commands in this document are also available as secret-free local scaffolding
under [`scripts/live-e2e/`](../scripts/live-e2e/). Use this path when preparing
the full live run:

```bash
export LIVE_E2E_ENV_FILE="$PWD/.factory/validation/live-e2e/secrets.env"
scripts/live-e2e/00-preflight.sh
scripts/live-e2e/01-clean-old-proxies.sh
scripts/live-e2e/02-generate-config.sh
```

Then fill the env file at `$LIVE_E2E_ENV_FILE` and complete both OAuth logins using
[`docs/live-e2e/DONE.md`](live-e2e/DONE.md). The OAuth helpers load that env
file before invoking `./droid-proxy auth`, so manual shell exports are not
required. After that, run:

```bash
export LIVE_E2E_ENV_FILE="$PWD/.factory/validation/live-e2e/secrets.env"
scripts/live-e2e/run-all-after-secrets.sh
```

The final runner keeps one exported run id across child scripts, restarts any
previous live `droid-proxy` process from this repo/config, runs direct provider
checks plus OAuth refresh and redaction checks, writes Factory settings, and
generates a Factory Droid manual evidence checklist.

Generated local files are intentionally gitignored:

- `config.local.yaml`
- `.factory/validation/live-e2e/secrets.env` (or your chosen `LIVE_E2E_ENV_FILE`)
- `.factory/validation/live-e2e/<run-id>/`
- OAuth tokens under `~/.droid-proxy/auth`

## Phase 0: Decommission local legacy proxies

Do this before any live test. The point is to make `droid-proxy` the only proxy
that Factory Droid can reach. The commands below search for common process names
and directory patterns used by previous proxy tools. Adjust the patterns if your
machine used different names.

### 0.1 Record current state

```bash
mkdir -p .factory/validation/live-e2e/$(date +%Y%m%d-%H%M%S)

pgrep -af 'legacy-proxy|droid-proxy|cursor-proxy' \
  | tee .factory/validation/live-e2e/processes.before.txt || true

lsof -nP -iTCP -sTCP:LISTEN \
  | rg 'legacy-proxy|droid-proxy|cursor-proxy|:8787|:1455|:56121|:8000|:11434' \
  | tee .factory/validation/live-e2e/listeners.before.txt || true

cp ~/.factory/settings.json \
  ~/.factory/settings.json.pre-droid-proxy-live-e2e.$(date +%Y%m%d-%H%M%S) 2>/dev/null || true

jq '.customModels[]? | {model, displayName, provider, baseUrl}' \
  ~/.factory/settings.json 2>/dev/null \
  | tee .factory/validation/live-e2e/factory-models.before.json || true
```

### 0.2 Stop old proxy processes

```bash
pkill -f 'legacy-proxy' || true
```

Do not kill `cursor-proxy` unless it is binding a port needed by this run. If it
is active on `127.0.0.1:8787`, stop it for the duration of the test or move
`droid-proxy` to a different port and update Factory settings accordingly.

### 0.3 Locate, archive, then remove local legacy proxy repos

Review the printed directories before removal.

```bash
for root in "$HOME/code" "$HOME/Developer" "$HOME/Documents/GitHub"; do
  [ -d "$root" ] || continue
  find "$root" \
    \( -name .git -o -name node_modules -o -name vendor \) -prune -o \
    -type d \
    \( -iname '*proxy*' \) \
    -print 2>/dev/null | grep -vi droid-proxy | grep -vi cursor-proxy
done
```

For each matching legacy proxy repo that is not `droid-proxy` or `cursor-proxy`:

```bash
archive_dir="$HOME/.local/share/droid-proxy-archives/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$archive_dir"

# Replace /path/to/donor with the reviewed path.
donor=/path/to/donor
tar -C "$(dirname "$donor")" -czf "$archive_dir/$(basename "$donor").tgz" "$(basename "$donor")"
rm -rf "$donor"
```

Also remove stale shell aliases, launch agents, npm globals, or symlinks that
start legacy proxies:

```bash
type -a legacy-proxy 2>/dev/null || true
launchctl list | rg -i 'proxy' || true
npm ls -g --depth=0 2>/dev/null | rg -i 'proxy' || true
pipx list 2>/dev/null | rg -i 'proxy' || true
```

### 0.4 Confirm clean slate

```bash
pgrep -af 'legacy-proxy' && exit 1 || true

lsof -nP -iTCP:8787 -sTCP:LISTEN
lsof -nP -iTCP:1455 -sTCP:LISTEN
lsof -nP -iTCP:56121 -sTCP:LISTEN
```

Expected result:

- No legacy proxy process is running.
- Port `8787` is free before starting `droid-proxy`.
- Ports `1455` and `56121` are free before OAuth login.
- Factory custom models no longer point to old proxy base URLs.

## Phase 1: Build and create live config

Run static verification first:

```bash
test -z "$(gofmt -l .)"
go test ./...
go vet ./...
make build
```

Create `config.local.yaml` from `config.example.yaml`. This file is gitignored.
Use real API keys from the environment; never paste secrets into the YAML.

```bash
cp config.example.yaml config.local.yaml
```

Minimum live model block:

```yaml
listen:
  host: 127.0.0.1
  port: 8787

logging:
  level: info
  format: text
  redact: true
  trace_requests: true

oauth:
  auth_dir: "~/.droid-proxy/auth"
  codex_callback_host: localhost
  codex_callback_port: 1455
  xai_callback_host: 127.0.0.1
  xai_callback_port: 56121

models:
  - alias: gpt-5.2-codex
    display_name: "GPT-5.2 Codex (ChatGPT OAuth)"
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: "${CODEX_UPSTREAM_MODEL:-gpt-5.2-codex}"
    max_output_tokens: 128000
    max_context_tokens: 400000

  - alias: grok-build-0.1
    display_name: "Grok Build 0.1 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: "${XAI_GROK_BUILD_MODEL:-grok-build-0.1}"
    max_output_tokens: 128000
    max_context_tokens: 256000
    capabilities:
      factory_reasoning: drop

  - alias: grok-4.3
    display_name: "Grok 4.3 (xAI OAuth)"
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: "${XAI_GROK_MODEL:-grok-4.3}"
    max_output_tokens: 128000
    max_context_tokens: 1000000
    capabilities:
      factory_reasoning: passthrough

  - alias: glm-5.1
    display_name: "GLM 5.1 (Z.AI GLM Coding Plan)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: zai-coding-api
    upstream_model: "${ZAI_GLM_MODEL:-glm-5.1}"
    max_output_tokens: 131072
    max_context_tokens: 200000
    extra_args:
      thinking:
        type: enabled

  - alias: mimo-v2.5-pro
    display_name: "MiMo V2.5 Pro (Xiaomi MiMo)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: "${MIMO_KNOWN_AUTH:-mimo}"
    upstream_model: "${MIMO_MODEL:-mimo-v2.5-pro}"
    max_output_tokens: 131072
    max_context_tokens: 1048576
    extra_args:
      thinking:
        type: enabled

  - alias: "${FIREWORKS_MODEL}"
    display_name: "DeepSeek V4 Pro (Fireworks)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: "${FIREWORKS_MODEL}"
    max_output_tokens: 128000
    max_context_tokens: 131072

  - alias: deepseek-v4-flash
    display_name: "DeepSeek V4 Flash (DeepSeek)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    upstream_model: "${DEEPSEEK_MODEL:-deepseek-v4-flash}"
    max_output_tokens: 128000
    max_context_tokens: 64000
    capabilities:
      reasoning: deepseek
    extra_args:
      thinking:
        type: enabled
      reasoning_effort: high
```

Required environment:

```bash
export DEEPSEEK_API_KEY=...
export ZAI_CODING_API_KEY=...
export MIMO_API_KEY=...
export FIREWORKS_API_KEY=...
export FIREWORKS_MODEL=accounts/fireworks/models/deepseek-v4-pro

# Optional overrides after checking current account/model availability.
export CODEX_UPSTREAM_MODEL=gpt-5.2-codex
export XAI_GROK_BUILD_MODEL=grok-build-0.1
export XAI_GROK_MODEL=grok-4.3
export ZAI_GLM_MODEL=glm-5.1
export MIMO_KNOWN_AUTH=mimo
export MIMO_MODEL=mimo-v2.5-pro
export DEEPSEEK_MODEL=deepseek-v4-flash
```

For MiMo Token Plan, set `MIMO_KNOWN_AUTH` to one of
`mimo-token-plan-cn`, `mimo-token-plan-sgp`, or `mimo-token-plan-ams`, then
export the matching env var from [`docs/examples/mimo.md`](examples/mimo.md):
`MIMO_TOKEN_PLAN_CN_API_KEY`, `MIMO_TOKEN_PLAN_SGP_API_KEY`, or
`MIMO_TOKEN_PLAN_AMS_API_KEY`.

Config acceptance gate:

```bash
go test ./internal/config
```

Then start the proxy:

```bash
./droid-proxy --config config.local.yaml 2>&1 \
  | tee .factory/validation/live-e2e/proxy.log
```

In another terminal:

```bash
curl -sS http://127.0.0.1:8787/health | jq .
curl -sS http://127.0.0.1:8787/v1/models \
  | jq '.data[] | {id, factory_provider, upstream_protocol, agent_ready}'
```

## Phase 2: OAuth login

Run OAuth login after Phase 0 confirms callback ports are free.

```bash
scripts/live-e2e/auth-codex.sh
scripts/live-e2e/auth-xai.sh
```

If invoking `./droid-proxy auth ...` directly, source `$LIVE_E2E_ENV_FILE`
first in that terminal so config validation can see the provider API key
environment variables.

Token storage checks:

```bash
stat -f '%A %N' ~/.droid-proxy/auth
for f in ~/.droid-proxy/auth/*; do
  [ -f "$f" ] && stat -f '%A %N' "$f"
done
```

Expected result:

- Auth dir is `700`.
- Token JSON files are `600`.
- Command output never contains access tokens, refresh tokens, ID tokens, or
  Authorization header values.

## Phase 3: Direct proxy HTTP tests

Create an output directory per run:

```bash
run_dir=".factory/validation/live-e2e/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$run_dir"
```

### 3.1 Chat providers: DeepSeek, Z.AI, MiMo, Fireworks

Run each alias through the same contract:

```bash
for model in deepseek-v4-flash glm-5.1 mimo-v2.5-pro "$FIREWORKS_MODEL"; do
  curl -sS http://127.0.0.1:8787/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"$model\",
      \"stream\": false,
      \"messages\": [{\"role\":\"user\",\"content\":\"Reply exactly: droid-proxy-ok\"}]
    }" | tee "$run_dir/$model.nonstream.json" | jq .

  curl -sS -N http://127.0.0.1:8787/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"$model\",
      \"stream\": true,
      \"messages\": [{\"role\":\"user\",\"content\":\"Count from 1 to 5, one number per line.\"}]
    }" | tee "$run_dir/$model.stream.sse"

  curl -sS http://127.0.0.1:8787/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"$model\",
      \"messages\": [{\"role\":\"user\",\"content\":\"Use the get_weather tool for Indianapolis.\"}],
      \"tools\": [{
        \"type\": \"function\",
        \"function\": {
          \"name\": \"get_weather\",
          \"description\": \"Get weather for a city.\",
          \"parameters\": {
            \"type\": \"object\",
            \"properties\": {\"city\": {\"type\":\"string\"}},
            \"required\": [\"city\"]
          }
        }
      }],
      \"tool_choice\": \"auto\"
    }" | tee "$run_dir/$model.tool-call.json" | jq .
done
```

Pass criteria:

- Non-stream response contains the exact phrase or a clearly valid model answer.
- Stream output contains multiple `data:` chunks and terminates cleanly.
- Tool-call response includes a valid OpenAI-compatible `tool_calls` array when
  the provider supports tools.
- No API key or bearer token appears in `$run_dir` or
  `.factory/validation/live-e2e/proxy.log`.

DeepSeek-specific checks:

- Confirm the configured upstream model is accepted (`deepseek-v4-flash` or the
  current model ID from DeepSeek docs/account).
- Confirm reasoning output does not break the final `message.content`.
- Confirm a second turn with a tool result does not lose tool-call state.

Z.AI-specific checks:

- Confirm `glm-5.1` is available on the GLM coding plan account, or replace it
  with the current account-specific coding model.
- Confirm the live model uses `known_auth: zai-coding-api`, which maps Coding
  Plan keys to `https://api.z.ai/api/coding/paas/v4`.
- Confirm `extra_args.thinking.type: enabled` is accepted. If the account rejects
  it, remove the extra arg and retest before changing code.

MiMo-specific checks:

- Confirm the account uses the correct profile: `mimo` for normal API access, or
  the correct regional `mimo-token-plan-*` profile for Token Plan access.
- Confirm the upstream request uses Xiaomi's `api-key` header and not
  `Authorization: Bearer`.
- Confirm `mimo-v2.5-pro` is accepted for coding and long reasoning. If the
  account only exposes a different V2.5 model, update `MIMO_MODEL` and retest.
- Confirm `extra_args.thinking.type: enabled` is accepted and that
  `reasoning_content` replay works across a tool-call turn, as with DeepSeek.
- Do not enable Xiaomi web search or prompt-cache controls during this run unless
  their API docs explicitly expose those controls for Chat Completions.

Fireworks-specific checks:

- Use a Fireworks model that explicitly supports function calling/tool use.
- If the first selected model fails tools, retest with a known tool-capable
  Fireworks model before marking the provider broken.

### 3.2 OAuth Responses providers: Codex and xAI

Run each OAuth alias through the Responses contract:

```bash
for model in gpt-5.2-codex grok-build-0.1 grok-4.3; do
  curl -sS http://127.0.0.1:8787/v1/responses \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"$model\",
      \"stream\": false,
      \"input\": \"Reply exactly: droid-proxy-ok\"
    }" | tee "$run_dir/$model.responses.nonstream.json" | jq .

  curl -sS -N http://127.0.0.1:8787/v1/responses \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"$model\",
      \"stream\": true,
      \"input\": \"Count from 1 to 5, one number per line.\"
    }" | tee "$run_dir/$model.responses.stream.sse"

  curl -sS http://127.0.0.1:8787/v1/responses \
    -H 'Content-Type: application/json' \
    -H "X-Client-Request-Id: live-e2e-$model-tool" \
    -d "{
      \"model\": \"$model\",
      \"stream\": false,
      \"input\": [{\"role\":\"user\",\"content\":\"Use the get_weather tool for Indianapolis.\"}],
      \"tools\": [{
        \"type\": \"function\",
        \"name\": \"get_weather\",
        \"description\": \"Get weather for a city.\",
        \"parameters\": {
          \"type\": \"object\",
          \"properties\": {\"city\": {\"type\":\"string\"}},
          \"required\": [\"city\"]
        }
      }],
      \"tool_choice\": \"auto\"
    }" | tee "$run_dir/$model.responses.tool-call.json" | jq .
done
```

Pass criteria:

- Non-stream request succeeds even though the proxy asks the OAuth upstream for
  SSE internally.
- Stream request emits valid SSE and terminates with the provider's terminal
  Responses event.
- Tool-call response includes `function_call` output items or provider-equivalent
  tool-call events.
- xAI requests use a stable conversation/session ID when one is supplied.
- Token refresh happens without exposing token values in logs.

OAuth refresh test:

1. Back up the token file in `~/.droid-proxy/auth`.
2. Temporarily set the token expiry in the JSON to a near-past value.
3. Run a single `/v1/responses` request.
4. Confirm the request succeeds and the token file timestamp changes.
5. Restore the backup if the provider rejects refresh.

## Phase 4: Error and redaction tests

Run one intentional failure per provider:

- Temporarily set an invalid model ID and confirm the upstream error maps through
  with the original HTTP status or a clear proxy error.
- Temporarily unset each BYOK env var and confirm the proxy returns an auth error
  without leaking env var values.
- Temporarily move one OAuth token file aside and confirm the proxy returns a
  redacted authentication error.

Secret scan:

```bash
rg -n 'sk-|xai-|Bearer |refresh_token|access_token|id_token|DEEPSEEK_API_KEY|ZAI_CODING_API_KEY|ZAI_MAIN_API_KEY|ZAI_API_KEY|MIMO_API_KEY|MIMO_TOKEN_PLAN|FIREWORKS_API_KEY' \
  .factory/validation/live-e2e 2>/dev/null
```

Expected result: no literal secret values. Field names may appear in JSON only if
the value is redacted or absent.

## Phase 5: Factory Droid E2E

Update `~/.factory/settings.json` so every tested model points at
`http://127.0.0.1:8787` and uses the matching Factory provider mode:

| droid-proxy alias | Factory provider | Factory baseUrl |
| --- | --- | --- |
| `gpt-5.2-codex` | `openai` | `http://127.0.0.1:8787` |
| `grok-build-0.1` | `openai` | `http://127.0.0.1:8787` |
| `grok-4.3` | `openai` | `http://127.0.0.1:8787` |
| `glm-5.1` | `generic-chat-completion-api` | `http://127.0.0.1:8787` |
| `mimo-v2.5-pro` | `generic-chat-completion-api` | `http://127.0.0.1:8787` |
| `${FIREWORKS_MODEL}` | `generic-chat-completion-api` | `http://127.0.0.1:8787` |
| `deepseek-v4-flash` | `generic-chat-completion-api` | `http://127.0.0.1:8787` |

For each model in Factory Droid:

1. Select the model.
2. Ask: `Reply exactly: droid-proxy-ok`.
3. Ask it to inspect this repo: `Read README.md and summarize the proxy purpose in one sentence.`
4. Ask it to perform a harmless tool-using file task:

   ```text
   Create .factory/validation/live-e2e/<model-alias>/result.txt with the exact
   text "<model-alias> ok", then read it back and report the file contents.
   ```

5. Verify the file exists and contains the expected text.
6. Confirm proxy logs show requests for the selected alias and no calls to old
   proxy ports or old base URLs.

Pass criteria:

- Factory Droid can complete the text and file-tool workflow for each model.
- Streaming does not stall.
- Tool calls and tool results complete without malformed-message errors.
- `~/.factory/settings.json` contains no active custom model pointing at old
  legacy proxy URLs.

## Phase 6: Result matrix

Fill this table during the live run.

| Provider | Alias | Direct non-stream | Direct stream | Tool call | Tool result | OAuth refresh | Factory text | Factory file task | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ChatGPT/Codex OAuth | `gpt-5.2-codex` |  |  |  |  |  |  |  |  |  |
| xAI OAuth (Grok Build) | `grok-build-0.1` |  |  |  |  |  |  |  |  |  |
| xAI Grok 4.3 OAuth | `grok-4.3` |  |  |  |  |  |  |  |  |  |
| Z.AI GLM coding | `glm-5.1` |  |  |  |  | N/A |  |  |  |  |
| Xiaomi MiMo | `mimo-v2.5-pro` |  |  |  |  | N/A |  |  |  |  |
| Fireworks | `${FIREWORKS_MODEL}` |  |  |  |  | N/A |  |  |  |  |
| DeepSeek | `deepseek-v4-flash` |  |  |  |  | N/A |  |  |  |  |

Use these status values:

- `PASS`: all required checks passed.
- `CONFIG`: provider failed because of account entitlement, model ID, base URL,
  or plan mismatch.
- `PROXY-BUG`: request shape, streaming, translation, refresh, or header handling
  needs a code fix.
- `PROVIDER-LIMIT`: provider lacks a required feature for Factory Droid agent use.

## Phase 7: Fix loop

For each `PROXY-BUG`:

1. Save the smallest redacted request/response fixture under a test package
   `testdata/` directory.
2. Add or update a unit/integration test that fails on the captured behavior.
3. Patch the proxy.
4. Run:

   ```bash
   go test ./...
   go vet ./...
   ```

5. Rerun the single failed provider case.
6. Rerun the corresponding Factory Droid workflow.

For each `CONFIG` issue:

1. Update `config.local.yaml` with the provider-approved model ID or base URL.
2. Document the working value in this plan's result notes if it is safe to share.
3. Do not change code unless the provider requires a request shape the current
   config model cannot express.

## Final acceptance criteria

- Local legacy proxy repos are archived/removed from the Mac.
- No legacy proxy process is running.
- `droid-proxy` owns the configured Factory Droid base URL.
- `go test ./...` and `go vet ./...` pass after any fixes.
- All target providers are either `PASS` or have a clear `CONFIG` /
  `PROVIDER-LIMIT` reason.
- Factory Droid successfully completes at least one text task and one file/tool
  task for every provider marked `PASS`.
- Logs and saved evidence contain no literal API keys, bearer tokens, OAuth
  refresh tokens, or ID tokens.
