# Smoke test guide

Verify your proxy setup after configuration. Run these checks with the proxy
started in the background:

```bash
./droid-proxy start --config config.yaml
./droid-proxy status
```

See [CLI.md](CLI.md) for start/stop/logs. Per-provider setup:
[examples/](examples/).

## 0. Confirm the proxy is running

```bash
./droid-proxy status
curl -s http://127.0.0.1:9787/health
# {"service":"droid-proxy","status":"ok","version":"..."}
```

If health fails, check `./droid-proxy logs` or `~/.droid-proxy/stderr.log`.

## 1. Models list

```bash
curl -s http://127.0.0.1:9787/v1/models | jq '.data[] | {id, factory_provider, upstream_protocol, agent_ready}'
```

Every configured alias should appear with fields matching `config.yaml`.

For OAuth models, confirm an account is logged in via the `oauth_auth` object
(`missing_auth: false`, `active_count > 0`):

```bash
curl -s http://127.0.0.1:9787/v1/models | jq '.data[] | select(.oauth_auth) | {id, oauth_auth}'
```

## 2. Chat completions (DeepSeek example)

Requires `deepseek-v4-flash` in config and `DEEPSEEK_API_KEY` set.

Non-streaming:

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "stream": false,
    "messages": [{"role":"user","content":"say hi in one word"}]
  }' | jq -r '.choices[0].message.content'
```

Streaming:

```bash
curl -sS -N http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "stream": true,
    "messages": [{"role":"user","content":"count to three"}]
  }'
```

You should see `data: {...}` chunks ending with `data: [DONE]`.

## 3. Tool calls (DeepSeek)

```bash
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "messages": [{"role":"user","content":"What is the weather in SF?"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "parameters": {"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}
      }
    }],
    "tool_choice": "auto"
  }' | jq '.choices[0].message.tool_calls'
```

The response should include a `tool_calls` array.

## 4. Anthropic messages

Requires `claude-sonnet-4-5-20250929` (or your Anthropic alias) in config:

```bash
curl -sS http://127.0.0.1:9787/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "max_tokens": 256,
    "messages": [{"role":"user","content":"hi"}]
  }' | jq -r '.content[0].text'
```

## 5. count_tokens

```bash
curl -sS http://127.0.0.1:9787/v1/messages/count_tokens \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "messages": [{"role":"user","content":"hello world"}]
  }' | jq '.input_tokens'
```

## 6. OpenAI Responses

Requires `gpt-4o` (or your OpenAI Responses alias) in config:

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o",
    "input": "hello"
  }' | jq '.output'
```

Streaming aliases that translate `/v1/responses` to an OpenAI Chat-compatible
upstream should emit incremental delta events and close each item before
`response.completed`:

```bash
curl -sS -N http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "<openai-chat-backed-responses-alias>",
    "stream": true,
    "input": "count to three"
  }'
```

You should see `response.output_item.done` before `response.completed`, and the
completed response should include a non-empty `response.output` array.

## 7. OAuth Responses (optional)

Requires prior login (`droid-proxy auth codex` or `auth xai`) and a matching
model in config. Skip if you only use API-key providers.

Codex example:

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.6",
    "input": "hello"
  }' | jq '.output'
```

`gpt-5.6` is the recommended local Sol alias. The public API documents that
unsuffixed ID as a Sol alias, but the credential-validated private OAuth path
requires the proxy config to map it to explicit `gpt-5.6-sol`. The dashboard
also provides local standard/fast pairs for Sol, Terra, and Luna; each fast
alias keeps the standard entry's upstream model and requests
`service_tier: priority`. The effective tier is account/backend dependent and
is visible in the response. Availability depends on the logged-in account,
plan, and workspace policy. An
unavailable-model 4xx is a real validation failure and is surfaced without
model downgrade.

Credentialed maintainers can additionally set
`LIVE_E2E_CODEX_GPT56_ADVANCED=1` for the max-reasoning and cache-options
sanitization gate described in `scripts/live-e2e/README.md`. The probe omits
`mode: pro`: the credentialed test accounts returned upstream 400 for that
public API mode, and the proxy intentionally does not downgrade it. Mode
availability remains account/plan dependent.

xAI examples:

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.5",
    "input": "hello",
    "prompt_cache_key": "smoke-conversation",
    "reasoning": {"effort": "high"}
  }' | jq '.output'
```

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-build-0.1",
    "input": "hello"
  }' | jq '.output'
```

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-composer-2.5-fast",
    "input": "hello"
  }' | jq '.output'
```

```bash
curl -sS http://127.0.0.1:9787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.3",
    "input": "hello",
    "reasoning": {"effort": "low"}
  }' | jq '.output'
```

## 8. Drive Droid

1. Start the proxy: `./droid-proxy start --config config.yaml`
2. Add models via `./droid-proxy config` and sync to Factory (`s`/`S`), or
   hand-edit `~/.factory/settings.json` — see [FACTORY.md](FACTORY.md).
3. Select the model in Droid.
4. Send a message; confirm a response.
5. Ask the agent to use a tool ("read the README"); confirm tool calls flow.
6. Watch `./droid-proxy logs` for request IDs.

## 9. Verify no secrets leak in logs

With `trace_requests: true` in config and a test API key:

```bash
set -a && source .env.local && set +a
./droid-proxy --config config.yaml 2> /tmp/proxy.log &
sleep 1
curl -sS http://127.0.0.1:9787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}' >/dev/null
grep -q 'sk-' /tmp/proxy.log && echo "LEAK!" || echo "OK"
kill %1 2>/dev/null
```

`OK` means redaction is working. Use foreground mode here only for this debug
check — normal operation should use `start`.

## Developers

After code changes, also run:

```bash
go test ./...
```

Workflow validation uses local fake upstreams and does not require provider API
keys. The runtime smoke test
(`TestWorkflowValidation_RuntimeSmokeBinaryStartReadinessShutdownCleanup` in
`internal/server/workflow_runtime_smoke_test.go`) builds and exercises a real
binary against fake upstreams; it requires port **9787** to be free (stop a
running daemon first).
