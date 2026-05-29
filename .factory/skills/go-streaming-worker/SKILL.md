---
name: go-streaming-worker
description: Implements droid-proxy streaming reliability, cancellation, timeout, and reasoning-stream behavior.
---

# Go Streaming Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for SSE terminal marker detection, protocol-shaped stream errors, downstream cancellation/write failures, stream idle timeout behavior, keepalive semantics, DeepSeek reasoning stream commit safety, Responses stream repair/docs alignment, and leak checks.

## Work Procedure

1. Read mission artifacts plus `.factory/library/protocols.md`, `security.md`, and `user-testing.md`.
2. Write failing streaming tests first with fake upstreams. Use deadlines and small intervals to avoid hangs.
3. Parse complete SSE events in tests, including split writes, CRLF, comments, blank lines, and terminal markers. Do not use substring-only assertions.
4. Prove cancellation and cleanup through fake upstream context cancellation and bounded goroutine/process/listener checks, including handler/httptest listener evidence for VAL-STREAM-010 work.
5. For resource-cleanup assertions, include explicit process/listener cleanup evidence in addition to goroutine counts, with deadlines, settle windows, and tolerances.
6. Do not let keepalive comments count as upstream activity for idle timeout.
7. Run targeted tests, full serial tests, and race serially when feasible.

## Example Handoff

```json
{
  "salientSummary": "Implemented EOF-before-terminal detection for Chat, Responses, and Anthropic streams with protocol-shaped terminal errors and cancellation tests.",
  "whatWasImplemented": "Stream forwarding now tracks terminal markers per surface, emits bounded terminal error SSE frames on truncation, and stops upstream reads on downstream cancellation/write failure.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "GOMAXPROCS=2 go test -p=1 ./internal/stream ./internal/handlers -run 'Stream|Trunc|Cancel'", "exitCode": 0, "observation": "Streaming edge tests passed."},
      {"command": "GOMAXPROCS=2 go test -p=1 ./...", "exitCode": 0, "observation": "Full serial suite passed."}
    ],
    "interactiveChecks": [
      {"action": "Fake upstream closed before terminal marker", "observed": "Downstream received one protocol-shaped error event and handler returned before deadline."}
    ]
  },
  "tests": {"added": [{"file": "internal/handlers/stream_reliability_test.go", "cases": [{"name": "responses stream truncation emits error", "verifies": "VAL-STREAM-003"}]}]},
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- A required streaming error shape conflicts with downstream client compatibility.
- A test is flaky under serial low-resource constraints and needs a contract adjustment.
- Implementing stream idle timeout requires config fields not yet available.
