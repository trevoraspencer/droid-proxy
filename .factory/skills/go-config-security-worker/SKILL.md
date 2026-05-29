---
name: go-config-security-worker
description: Implements droid-proxy config compatibility, docs/example alignment, and security hardening in Go.
---

# Go Config and Security Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for config loading/defaults, known-auth hydration, docs/examples compatibility, startup safety, body/response limits, timeouts, redacted logging, header filtering, credential isolation, reasoning cache isolation, and `agent_ready` metadata.

## Work Procedure

1. Read mission artifacts and `.factory/library/environment.md`, `security.md`, and `protocols.md`.
2. Write failing tests first for config/runtime behavior. Prefer table-driven tests covering omitted vs explicit values, invalid inputs, and fake upstream capture.
3. For security features, prove negative cases: upstream hit count zero, sentinel secrets absent, invalid configs fail before binding, and downstream credentials are not forwarded.
4. Keep config docs and examples synchronized with runtime behavior when the feature explicitly includes docs/examples.
5. Use bounded test payloads with tiny configured limits instead of huge payloads.
6. Run targeted tests, then `GOMAXPROCS=2 go test -p=1 ./...`; run race serially before handoff when feasible.

## Example Handoff

```json
{
  "salientSummary": "Added non-loopback auth-disabled startup failure and auth-before-body tests; full serial tests and lint passed.",
  "whatWasImplemented": "Config validation now rejects wildcard binds without client auth, validates non-empty auth keys after env expansion, and middleware tests prove unauthenticated oversized bodies are rejected before body reads or upstream calls.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "GOMAXPROCS=2 go test -p=1 ./internal/config ./internal/server ./internal/handlers -run 'Auth|NonLoopback|Body'", "exitCode": 0, "observation": "Targeted security tests passed."},
      {"command": "GOMAXPROCS=2 go test -p=1 ./...", "exitCode": 0, "observation": "Full serial suite passed."}
    ],
    "interactiveChecks": [
      {"action": "Started invalid wildcard config in subprocess test", "observed": "Exited non-zero before listener opened; stderr mentioned client_auth requirement."}
    ]
  },
  "tests": {"added": [{"file": "internal/config/security_test.go", "cases": [{"name": "wildcard bind requires client auth", "verifies": "VAL-SEC-001"}]}]},
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- A required security policy needs a product decision not captured in the contract.
- A docs requirement conflicts with current Factory/DeepSeek research.
- Resource constraints prevent required verification from running.
