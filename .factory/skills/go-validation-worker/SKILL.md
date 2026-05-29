---
name: go-validation-worker
description: Builds fake-upstream workflow validation, runtime smoke, endpoint matrix, and evidence capture.
---

# Go Validation Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for validation harnesses, endpoint truth tables, runtime smoke tests, Factory settings example validation, fake upstream wire capture, end-to-end Droid workflows, redaction/error/truncation workflow coverage, and low-resource validation tooling.

## Work Procedure

1. Read mission artifacts, `.factory/services.yaml`, and `.factory/library/user-testing.md`.
2. Keep full-stack/API validation serial: max one active workflow at a time. Avoid `t.Parallel` in heavy runtime tests.
3. Build fake upstreams that capture request method/path/headers/body, script non-stream and SSE responses, and expose evidence to assertions.
4. Add loopback-only/config guardrails so real provider URLs cannot be used accidentally.
5. Runtime smoke must capture PID, readiness deadline, shutdown signal, bounded wait, and post-run listener/process cleanup.
6. Validate docs/settings examples mechanically where assigned; do not require real credentials.
7. Before handoff, map every fulfilled validation assertion to explicit test cases, truth-table rows, or runtime-smoke evidence; call out any assertion portions that remain untested as incomplete work.
8. Run targeted validation tests, then full serial tests; run race serially if feasible and not concurrent with runtime smoke.

## Example Handoff

```json
{
  "salientSummary": "Added serial fake-upstream workflow harness and endpoint truth-table tests covering prefixed/prefixless routes without external provider calls.",
  "whatWasImplemented": "Validation helpers now create loopback-only fake providers, capture upstream/downstream transcripts, guard against real provider URLs, and verify runtime smoke cleanup after SIGTERM.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "GOMAXPROCS=2 go test -p=1 ./internal/... -run 'Workflow|Smoke|Matrix'", "exitCode": 0, "observation": "Workflow validation tests passed serially."},
      {"command": "GOMAXPROCS=2 go test -p=1 ./...", "exitCode": 0, "observation": "Full serial suite passed."}
    ],
    "interactiveChecks": [
      {"action": "Runtime smoke started built binary on 127.0.0.1:8787", "observed": "Health/models returned 200, SIGTERM stopped process, and port was free afterward."}
    ]
  },
  "tests": {"added": [{"file": "internal/handlers/workflow_validation_test.go", "cases": [{"name": "endpoint truth table uses fake upstreams only", "verifies": "VAL-VALID-014"}]}]},
  "discoveredIssues": [],
  "assertionTraceability": [
    {"assertion": "VAL-VALID-014", "evidence": "endpoint truth table test rows for every public route/provider/protocol combination"}
  ]
}
```

## When to Return to Orchestrator

- Runtime smoke cannot run without changing mission port/resource boundaries.
- A validation path requires real provider credentials or non-loopback egress.
- Resource use exceeds the MacBook Air constraints despite serial execution.
