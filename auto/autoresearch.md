# droid-proxy Autoresearch Instructions

This file is the operating contract for autonomous improvement loops in this
repo. It is subordinate to `VISION.md`, which is the canonical source of truth.
If this file and `VISION.md` ever disagree, follow `VISION.md`.

## Model And Reasoning Mandate

- Run the loop entirely with Codex / ChatGPT 5.5 using xhigh reasoning.
- Do not invoke Grok, Grok Build, Grok composer, or Grok MCP/plugin tooling.
- For heavy reasoning, hypothesis selection, or code-generation moments, prefix
  the cycle log note with `Using Codex/ChatGPT 5.5 xhigh:`.
- If helper threads or subagents are created, request model `gpt-5.5` and xhigh
  reasoning. If the runtime cannot set that explicitly, log the limitation and
  continue with the strongest available Codex/ChatGPT model.

## Source Of Truth

Read, in this order, before changing behavior:

1. `VISION.md`
2. `README.md`
3. `docs/CONFIG.md`
4. `docs/PROVIDERS.md`
5. `CONTRIBUTING.md`
6. `SECURITY.md`
7. `internal/security/*_release_test.go`
8. `Makefile`

`VISION.md` is immutable during autoresearch unless the maintainer explicitly
approves editing it. The loop should optimize the project described there:
a local, Droid-only, single-binary bridge that is small, secure-by-default,
honestly scoped, quality-first, and solo-maintainable.

## Composite Metric

Default score weights:

- **Tests and gates: 40%** - `npm test`, lint, build, command smokes, race/full
  gates when available.
- **VISION alignment: 30%** - in-scope work only, no non-goals, no dependency or
  abstraction creep, `VISION.md` remains untouched, docs expose the canonical
  source of truth, CHANGELOG changes are additive.
- **Robustness, docs, and performance: 30%** - docs/legal/CI audits, release
  readiness checks, benchmark signal where benchmarks exist, secret-safety gates.

The score is a ratchet, not a vanity metric. A change is accepted only when it is
in scope, improves or preserves required gates, and increases the composite score
or fixes a required red gate without weakening another category.

## What Better Means

Per `VISION.md`, better means:

- More correctness.
- Stronger security-default preservation.
- Better maintainability and smaller surface area.
- More accurate docs and less drift.
- Fewer onboarding footguns.

Better does **not** mean more providers, more clients, a bigger framework, a web
dashboard, telemetry, hosted/multi-user operation, or adoption-oriented features.

## Hard Rules

- Stay Droid-only. Do not add first-class paths for other agents, IDEs, generic
  apps, or hosted clients.
- Do not weaken security defaults: localhost bind, `0600` secrets, redaction-on,
  and `client_auth` off by default.
- Do not change `~/.droid-proxy/` paths, CLI command names, public route shapes,
  `droid-proxy/` User-Agent product strings, or config compatibility without
  explicit approval.
- Do not add dependencies, plugin systems, generic middleware layers, speculative
  abstractions, or provider-side intelligence without explicit approval.
- Do not mark a provider `agent_ready` without end-to-end validation evidence.
- Do not rewrite or revert CHANGELOG history. CHANGELOG changes must be additive.
- Do not commit secrets, local Factory artifacts, `.supergoal/`, `notes/`,
  `scratch/`, `tmp/`, or generated eval artifacts.
- Keep commits small, conventional, and focused.

## Stop Conditions

The loop says "NEVER STOP" only inside the approved, safe scope. Stop and ask the
maintainer when:

- A required gate remains red and cannot be fixed with an in-scope change.
- The next step would touch a `VISION.md` non-goal.
- The next step would weaken a security default.
- The next step would add a dependency or abstraction layer.
- The next step would break a compatibility surface.
- The change would make the project less solo-maintainable.
- There is genuine ambiguity about scope.

When unsure, choose the smaller reversible action or do nothing.

## Cycle Protocol

Each cycle follows this sequence:

1. Record the clean base commit, current branch, and current score.
2. Read the relevant `VISION.md` sections again.
3. Log `Using Codex/ChatGPT 5.5 xhigh:` with the hypothesis, expected score
   impact, and risk.
4. Select exactly one small improvement.
5. Implement the smallest complete change using existing patterns.
6. Add or update tests for every behavior change.
7. Update docs and CHANGELOG additively when user-visible behavior changes.
8. Run `npm run eval`.
9. Accept only if required gates pass and score improves, or if a required red
   gate is fixed without category regression.
10. Commit accepted changes with the cycle log entry.
11. If rejected, revert only files touched in that cycle and keep a rejection log
    entry explaining why.

## Git Keep/Revert Logic

- Work only on an autoresearch branch.
- Before each cycle, capture `HEAD` and a file list of intentional edits.
- Never revert files changed by the user outside the cycle.
- If a cycle fails, restore only the files touched by that cycle to `HEAD`.
- Remove untracked files only if the cycle created them and they are listed in
  the cycle log.
- Preserve `auto/autoresearch.jsonl` entries even for rejected cycles.

## Research Mandate

Research starts locally: `VISION.md`, tests, code, docs, and release scripts.
Use external research only for current provider/API/tooling questions that cannot
be answered from the repo. Prefer official upstream documentation for unstable
APIs. Do not use research as a reason to widen scope beyond `VISION.md`.

## Preferred Improvement Queue

1. Red or flaky required gates.
2. Security-default or redaction-safety gaps.
3. Docs/code drift against `VISION.md`, README, CONFIG, PROVIDERS, or release
   tests.
4. Missing regression tests for existing behavior.
5. Onboarding footgun reductions that do not expand scope.
6. Performance improvements only after correctness/security/docs are green.

Reject feature-count, provider-count, framework, and adoption-driven work.
