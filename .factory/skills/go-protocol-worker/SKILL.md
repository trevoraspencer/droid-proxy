---
name: go-protocol-worker
description: Implements droid-proxy provider protocol translation features in Go.
---

# Go Protocol Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for OpenAI Responses-over-Chat and Anthropic Messages-over-Chat translation, native passthrough regression protection, provider error shapes, route alias behavior, and protocol-specific tests.

## Work Procedure

1. Read `mission.md`, `validation-contract.md`, `features.json`, `AGENTS.md`, `.factory/services.yaml`, and `.factory/library/protocols.md`.
2. Identify the exact assertions in the assigned feature `fulfills` list and write failing tests first. Use fake `httptest.Server` upstreams only.
3. For translation features, test upstream request capture and downstream response shape before implementation. Cover text, tools, tool results, streaming, errors, prefixless aliases, and malformed inputs as required by the feature.
4. Implement minimal, maintainable translator helpers. Prefer isolated functions under `internal/translate` plus thin handler integration.
5. Preserve native passthrough behavior. Run native passthrough tests after translator changes.
6. Parse SSE event-by-event in tests; do not rely only on substring checks.
7. Run targeted tests, then `GOMAXPROCS=2 go test -p=1 ./...`. Run `GOMAXPROCS=2 go test -race -p=1 ./...` before handoff unless impossible; report any skip as incomplete.

## Example Handoff

```json
{
  "salientSummary": "Implemented Responses-over-Chat non-streaming text/tool translation with fake upstream request capture and downstream Responses shape tests; full serial Go tests passed.",
  "whatWasImplemented": "Added red-first tests and translator helpers for Responses input/messages, tool definitions, Chat tool_calls to Responses function_call output, and function_call_output follow-up to Chat tool messages.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "GOMAXPROCS=2 go test -p=1 ./internal/handlers ./internal/translate -run 'Responses|Tool'", "exitCode": 0, "observation": "Targeted translator tests passed."},
      {"command": "GOMAXPROCS=2 go test -p=1 ./...", "exitCode": 0, "observation": "Full serial test suite passed."}
    ],
    "interactiveChecks": [
      {"action": "Exercised httptest fake Chat upstream via /v1/responses", "observed": "Upstream received /chat/completions with rewritten model and tool schema; downstream returned Responses output items."}
    ]
  },
  "tests": {
    "added": [
      {"file": "internal/handlers/responses_translate_test.go", "cases": [{"name": "responses string input translates to chat", "verifies": "VAL-PROTO-003"}]}
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- Translation requirement conflicts with an upstream protocol limitation not covered by the contract.
- Implementing a required mapping would require real provider calls.
- Native passthrough regression cannot be avoided without changing accepted scope.
