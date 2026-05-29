# Smoke test guide

Once `go test ./...` passes, exercise the running binary end-to-end against a
real upstream. This document is the recipe.

## 1. Health

```bash
curl -s http://127.0.0.1:8787/health
# {"service":"droid-proxy","status":"ok","version":"0.0.0-dev"}
```

## 2. Models list

```bash
curl -s http://127.0.0.1:8787/v1/models | jq '.data[] | {id, factory_provider, upstream_protocol, agent_ready}'
```

Every configured alias should appear, with `factory_provider` and
`upstream_protocol` matching what's in `config.yaml`.

## 3. Chat completions (DeepSeek example)

Non-streaming:

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "stream": false,
    "messages": [{"role":"user","content":"say hi in one word"}]
  }' | jq -r '.choices[0].message.content'
```

Streaming:

```bash
curl -sS -N http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-v4-flash",
    "stream": true,
    "messages": [{"role":"user","content":"count to three"}]
  }'
```

You should see `data: {...}` chunks ending with `data: [DONE]`.

## 4. Tool calls (DeepSeek)

```bash
curl -sS http://127.0.0.1:8787/v1/chat/completions \
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

The response should include a `tool_calls` array with a function call request.

## 5. Anthropic messages

```bash
curl -sS http://127.0.0.1:8787/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "max_tokens": 256,
    "messages": [{"role":"user","content":"hi"}]
  }' | jq -r '.content[0].text'
```

## 6. count_tokens

```bash
curl -sS http://127.0.0.1:8787/v1/messages/count_tokens \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "messages": [{"role":"user","content":"hello world"}]
  }' | jq '.input_tokens'
```

## 7. OpenAI Responses

```bash
curl -sS http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o",
    "input": "hello"
  }' | jq '.output[].content'
```

## 8. Drive Droid

1. Start the proxy.
2. Add a model entry to `~/.factory/settings.json` (see `docs/factory-settings/`).
3. Select the model in Droid.
4. Send a message; confirm a response.
5. Ask the agent to do something tool-using ("read the README"); confirm tool
   calls flow.
6. Watch the proxy's log to confirm each request id is visible and the trace
   shape matches what you expect.

## 9. Verify no secrets leak in logs

With `trace_requests: true` and an Authorization header set on a request:

```bash
DROID_PROXY_TEST_KEY=sk-secret-do-not-log-1234 ./droid-proxy --config config.yaml 2> /tmp/proxy.log &
sleep 1
curl -sS http://127.0.0.1:8787/v1/chat/completions -d '...' >/dev/null
grep -q 'sk-secret-do-not-log-1234' /tmp/proxy.log && echo "LEAK!" || echo "OK"
```

`OK` means the redaction is in place.
