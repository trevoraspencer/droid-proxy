# VISION.md — Single Source of Truth

*Canonical source of truth for droid-proxy. Last revised 2026-06-22. This document
supersedes ad-hoc intent expressed in the README or commit history until it is
intentionally revised. **AI agents and contributors: read §14 before making any
change.** Project status: Beta, pre-`v0.1.0`.*

## 1. One-Sentence Vision

droid-proxy is a localhost, single-binary bridge that lets Factory Droid talk to
any BYOK or OAuth LLM provider — deliberately small, secure-by-default, honestly
scoped, and maintained quality-first by a single developer rather than grown for
adoption.

## 2. Purpose and Problem Statement

**The problem.** Factory Droid is excellent, but it steers you toward its own
model choices. Developers who want to drive Droid with *their own* providers — a
cheaper model, a Codex/ChatGPT or xAI OAuth subscription they already pay for, a
local Ollama/vLLM model, or a specific provider like DeepSeek, MiMo, Kimi, Groq,
Fireworks, or Z.AI — have no first-class path. Worse, every provider speaks a
slightly different dialect (OpenAI Chat vs. Responses vs. Anthropic Messages),
handles reasoning/thinking differently, and OAuth providers need login, token
refresh, and multi-account management.

**The solution.** droid-proxy runs on `127.0.0.1`, exposes Droid's three provider
modes (`anthropic`, `openai`, `generic-chat-completion-api`), and translates or
forwards each request to whatever upstream you configure — handling protocol
translation (T1–T3), OAuth login plus multi-account pooling with failover,
reasoning replay across tool turns, and secret redaction, all from one Go binary
with credentials kept in local files.

**Why a repo** (not a script, gist, SaaS, or Droid fork): there are enough real
moving parts — tiered protocol translation, an OAuth pool with
affinity/cooldown/refresh, config plus TUI onboarding, daemon lifecycle, and a
substantial test suite — that it needs to be a tested, documented, maintained
codebase. It is deliberately **not** a SaaS (the entire point is local credentials
and no hosted middleman) and **not** a fork of Droid (it is an external,
decoupled bridge that survives Droid updates).

## 3. Target Users and Jobs To Be Done

**Primary user:** a developer who already uses Factory Droid and wants to drive it
with their own model providers and accounts — for cost, for models Droid does not
offer natively, for OAuth subscriptions they already hold, or for local/offline
models. In practice this is *the maintainer and people like them*.

**Secondary users:** AI coding agents maintaining this repo (this document is
written partly for them); occasional contributors adding a provider example or a
fix.

**Explicitly not for:** people who do not use Factory Droid; teams wanting a
shared or hosted gateway; anyone seeking a managed service or API reseller;
non-developer end users.

**Jobs to be done:**
- Point Droid at my chosen model and have tool-using agent workflows actually work.
- Use my Codex/ChatGPT or xAI OAuth subscription — with multiple accounts pooled —
  inside Droid.
- Keep my API keys and OAuth tokens on my own machine.
- Set it all up without hand-editing three separate config files.

## 4. Value Proposition

**Pains removed:** provider lock-in within Droid; protocol-mismatch breakage;
manual token juggling; credentials leaving the machine; fiddly multi-file setup.

**Gains created:** BYOK/OAuth freedom inside Droid; honest signal (`agent_ready`,
T1–T4 tiers) about what actually works; local-first security; one-binary
simplicity.

**In one line:** *Use any model you bring inside Factory Droid, on your own
machine, with honest signal about what actually works — from one small,
secure-by-default binary.*

## 5. Final Desired End State

**"Done" is a reliability state, not a feature count.** The repo is
finished-enough when:

- `go build ./cmd/droid-proxy` yields a working static binary, and
  `droid-proxy config` onboards a provider end to end (the TUI writes
  `config.yaml`, stores the key in `~/.droid-proxy/env` at mode 0600, and syncs
  `~/.factory/settings.json`).
- Every **curated** provider the maintainer uses is validated end to end, honestly
  tier-classified, with an accurate `agent_ready` flag in `/v1/models`.
- **All gates are green:** `make build`, `make test`, `make test-race`,
  `make lint`, the legal/docs/CI audits, and gitleaks.
- **Docs match code** (enforced by `docs-audit`), and security defaults are intact
  (localhost bind, 0600 perms, redaction on, `client_auth` off).
- **`v0.1.0` is tagged** as the first honest public checkpoint with a GitHub
  release; further tags follow as it improves. Self-update from `origin/main`
  works.

After that, the project is allowed to enter **maintenance mode — and that is
success, not abandonment.** Ongoing work is reactive: keeping pace with Factory
Droid and provider-API drift, security and dependency upkeep, and validated
provider additions.

**Trajectory:**
- **~6 months:** `v0.1.0` plus a few point releases shipped; the maintainer uses it
  daily without friction; curated providers work; CI stays green; any external
  user can onboard from the docs alone.
- **~12 months:** kept pace with Droid and provider changes at low effort;
  config/CLI surfaces stable enough that breaking changes are rare; perhaps a few
  community-contributed provider examples; still solo-maintainable in a few hours
  per month.
- **~24 months:** either still quietly doing its job in maintenance mode (a win),
  **or** consciously sunset and archived with a clear note if Droid evolves such
  that the bridge is obsolete — an honest, acceptable outcome, explicitly **not** a
  failure.

## 6. Scope

### 6.1 In Scope
- **Factory Droid as the sole client**, across its three provider modes
  (`anthropic`, `openai`, `generic-chat-completion-api`).
- The existing endpoint surface: `/health`, `/healthz`, `/v1/models`,
  `/v1/chat/completions`, `/v1/responses`, `/v1/messages`,
  `/v1/messages/count_tokens`, `/v1/oauth/pool-health`.
- **Curated providers** via `known_auth` profiles and `docs/examples/`; T1–T3
  translation; T4 as best-effort.
- **OAuth** (Codex/ChatGPT, xAI) — PKCE browser login; Codex **multi-account
  pooling** with affinity, failover, cooldown, and refresh-replay.
- **Reasoning** replay (DeepSeek-style) and model-specific passthrough.
- **Local config** (YAML plus env layering), **TUI onboarding**, Factory settings
  sync, **daemon** lifecycle (start/stop/restart/logs, macOS launchd), and
  **self-update**.
- **Security:** localhost-first, 0600 secrets, log redaction, optional
  `client_auth`.
- The **test suite, CI, and release/audit tooling** themselves.

### 6.2 Out of Scope (Non-Goals)
- **Any non-Droid client as a first-class target** (other coding agents, IDEs,
  generic apps). The generic OpenAI/Anthropic protocol code is an internal
  mechanism, never a feature to market or extend toward other clients.
- **Hosted, multi-user, or shared deployment;** being a SaaS, a gateway-for-others,
  or an API reseller.
- **A web UI or dashboard** — the TUI is the only UI.
- **Supported public-internet exposure.** You *may* bind a non-localhost host, but
  firewalls, TLS, and auth then become your responsibility; hardening that path is
  not a goal.
- **Comprehensive "support every provider" coverage** or generic provider
  auto-discovery.
- **Provider-side intelligence** beyond proxying: no billing, analytics, model
  routing/optimization, or caching layers beyond the existing reasoning replay and
  OAuth pool.
- **Plugin systems or speculative abstraction frameworks.**
- **Telemetry that phones home.**
- **Compatibility guarantees before the maintainer chooses to make them** (pre-1.0
  may break).

**Tempting ideas future agents should reject:** "Make it work with *[other
agent/IDE]* too" → no, Droid-only · "Add a hosted/Docker multi-user mode" → no ·
"Auto-support every provider / scrape model lists generically" → no, curated ·
"Add a web dashboard" → no, TUI only · "Add a plugin/middleware framework" → no,
YAGNI · "Add caching/routing/load-balancing intelligence" → no, beyond the
existing reasoning and pool · "Pull in a big framework to simplify X" → **stop and
ask** (dependencies are a tripwire).

## 7. Key Capabilities and Priorities

### 7.1 MVP / v1 (`v0.1.0` — essentially present today)
Working proxy across Droid's three modes for curated providers (T1–T3) · OAuth
login plus Codex multi-account pool · reasoning replay · TUI onboarding plus config
plus Factory sync · daemon lifecycle plus self-update · security defaults plus
redaction · green gates plus accurate docs plus a tagged release.

### 7.2 Later (reactive, optional, unscheduled)
Additional *validated* providers as the maintainer adopts them · drift fixes for
Droid/provider API changes · gradual stabilization toward an eventual, undated
`v1.0.0` compatibility promise · minor DX polish (diagnostics, smoke tooling).

### 7.3 Explicitly Not Planned
Non-Droid clients · hosted/multi-user · web UI · comprehensive provider coverage ·
plugin/middleware frameworks · telemetry · provider-side intelligence
(billing/analytics/routing). *(Mirrors §6.2.)*

## 8. Guiding Principles
1. **Small over featureful.** YAGNI ruthlessly. The binary, the dependency list,
   and the abstraction count stay small. New abstractions are forbidden until
   proven necessary.
2. **Secure-by-default, local-first.** Localhost bind, 0600 secrets, redaction on,
   `client_auth` available but off. Credentials never leave the machine. Security
   defaults never *silently* regress.
3. **Honest over impressive.** Tiers and `agent_ready` tell the truth about what
   works; docs match code; never claim coverage you have not validated.
4. **Curated, not comprehensive.** First-class equals what the maintainer
   validates; the long tail is best-effort or community. Adding a provider is cheap
   *by pattern* (`known_auth` plus examples), never by framework.
5. **Quality is the gate, not adoption.** Correctness > security > maintainability >
   simplicity > performance > features. Green gates are non-negotiable.
6. **Boring where it counts.** Proxy core, security model, config schema, and CLI
   surface stay stable and predictable.
7. **Decoupled from Droid's internals.** An external bridge that tracks Droid's
   *public provider contract*, so it survives Droid updates.
8. **Solo-maintainable.** Every addition is weighed against "can one person keep
   this green in a few hours a month?" If not, it is probably out of scope.

*Welcome abstractions:* the existing translate-tier seam, the OAuth provider
interface, the `known_auth` registry. *Forbidden until proven:* plugin systems,
generic middleware frameworks, premature interfaces. *Dependencies:* the current
small set is the budget; new ones are a tripwire.

## 9. Architecture and Technical Direction
- **Module:** one Go module `github.com/trevoraspencer/droid-proxy`;
  `cmd/droid-proxy` for CLI dispatch plus `internal/*` packages, each with one
  clear responsibility.
- **Request path:** Gin server plus middleware (request-id, recovery, trace/access
  log, optional `client_auth`, body limit) → handlers
  (`chat`/`messages`/`responses`/`models`/`count_tokens`) → upstream router
  (alias → model) → translate seam (T1/T2 passthrough or T3 translation) →
  upstream client → response (stream-preferred or buffered) → reasoning-cache
  capture/replay.
- **OAuth subsystem:** provider interface (Codex, xAI), PKCE login, token store
  under `~/.droid-proxy/auth`, account pool (selector strategies plus affinity plus
  failover/cooldown plus refresh-replay plus fsnotify hot-reload).
- **Config:** YAML plus layered env (`~/.droid-proxy/env`, `.env.local`), `${VAR}`
  expansion, `known_auth` registry, per-model `capabilities` driving `agent_ready`.
- **Stays simple:** the request path, the single-binary build, the config schema.
  **Extensible by pattern (not framework):** providers (`known_auth` plus
  `docs/examples/`), protocols (translate tiers). **Forbidden until proven:**
  plugin systems, premature interfaces, heavy dependencies.
- **Runtime:** Go 1.26 line; macOS and Linux; macOS launchd for service mode;
  `127.0.0.1:8787` default; no external runtime services.
- **Version stamping:** the build's version ldflags path (`-X …/internal/version.*`)
  must track the module path; if they drift, self-update version stamping silently
  breaks.

## 10. Non-Functional Requirements
- **Security:** local-first bind, 0600 secret files, redaction on by default,
  optional `client_auth`, zero telemetry.
- **Reliability:** graceful shutdown, OAuth failover and cooldown, token
  hot-reload, race-clean tests.
- **Performance:** stream passthrough preferred; translate only when necessary;
  timeouts sized for long agent turns (e.g. read 60s / write 600s / idle 120s).
- **Portability:** single static binary; macOS/Linux; modern Go line.
- **Observability:** structured logs (text or JSON) plus request IDs plus
  redaction; a `pool-health` endpoint; nothing phoned home.
- **Maintainability:** small surface, well-tested, holdable in one person's head.
- **Compatibility:** pre-1.0 may break; compat surfaces (paths, User-Agent, CLI,
  schema) change only deliberately and documented.

## 11. Quality Bar
Unit and integration tests run against fake upstreams (CI needs no real keys) and
are race-clean. Every behavior change ships with a regression test — the repo's
established habit. "Professional" here means *honest, tested, redaction-safe,
reproducible build, accurate docs* — not heavyweight process. Before any change is
accepted, the full gate set must pass: `gofmt`, `go vet`, `build`, `test`,
`test-race`, `legal-audit`, `docs-audit`, `ci-audit`, and `gitleaks`.

## 12. Success Metrics and Definition of Done

**Gate metrics that must never regress:**
- All CI gates green on `main`.
- Security defaults intact (localhost · 0600 · redaction-on · `client_auth`-off) —
  kept honest by `internal/security/*_release_test.go` and the secret-safety tests.
- No secrets in history; no internal artifacts committed.
- Curated providers' `agent_ready` stays *validated, not aspirational*.
- Reproducible single static binary on the supported Go line; docs match code.

**What "better" means:** fewer setup steps, fewer footguns, cleaner onboarding,
less drift breakage — **not** more providers or more stars. **An autoresearch loop
optimizes** correctness, security, maintainability, and docs-accuracy *within
scope* — never feature, provider, or adoption count.

**Definition of Done (per change):** in scope · tests added and green · all gates
green · docs updated in the same change · no security-default regression · no new
dependency or abstraction without approval · CHANGELOG updated additively.

## 13. Workflow Expectations
- **Plan/spec-first for non-trivial work;** trivial fixes can go direct.
- **Tests required for every behavior change** (a regression test). The rule is
  "tests green and new behavior covered," not strict red-green ceremony.
- **Small, focused commits; CI-green PRs**, in the conventional-commit-ish style
  already in the history (`ci:`, `fix:`, etc.).
- **CHANGELOG is additive;** never rewrite or revert entries.
- **This `VISION.md` is edited only with explicit maintainer approval.**
- **No heavyweight ADRs or decision logs** — the CHANGELOG, commit history, and
  this document are the record.

## 14. Mandatory Instructions for AI Agents and Contributors

### 14.1 Read This First
This `VISION.md` (canonical SSoT) → `README.md` (the user contract) →
`docs/CONFIG.md` and `docs/PROVIDERS.md` → `CONTRIBUTING.md` and `SECURITY.md` →
`internal/security/*_release_test.go` and the `Makefile` (the gates).

### 14.2 Optimize For
Correctness, security, maintainability, and docs-accuracy. The smallest change that
fully solves the problem. Matching existing patterns. A regression test for every
behavior change.

### 14.3 Do Not Do (without explicit approval)
- Weaken a security default.
- Add a non-Droid client path, or any §6.2 non-goal.
- Rename or move `~/.droid-proxy/` paths, or change the `droid-proxy/` User-Agent
  product string.
- Decouple the version ldflags path (`-X …/internal/version.*`) from the module
  path — they must stay in sync or self-update version stamping silently breaks.
- Commit internal artifacts (e.g. `.supergoal/`, `notes/`, `scratch/`, `tmp/` —
  already gitignored) or secrets.
- Rewrite or revert CHANGELOG history.
- Add dependencies or speculative abstractions.
- Edit this `VISION.md`.

### 14.4 Ask Before (the four tripwires)
- Changing any **security default**.
- **Widening scope** — a non-Droid client, hosted/multi-user, or promoting a
  provider to first-class/validated.
- **Breaking a compat surface** — paths, User-Agent, CLI command names, or
  backward-incompatible config schema.
- **Adding a dependency or a new abstraction layer.**

### 14.5 Stop Conditions (stop and ask — do not guess)
- A gate would stay red and you cannot green it within scope.
- Proceeding requires touching a §6.2 non-goal.
- There is genuine ambiguity about whether something is in scope — default to *not*
  doing it.
- You are tempted to mark a provider `agent_ready` without validating it end to end.
- You would need to bind beyond localhost or relax redaction to "make it work."
- The change would make the project no longer solo-maintainable.

**Handling ambiguity and scope creep:** default to the smaller, in-scope,
reversible action; reject scope creep by citing §6.2; never widen scope to resolve
ambiguity. **Deciding between options:** follow the §8 ordering (correctness →
security → maintainability → simplicity → performance → features) and prefer
whatever keeps the project small and solo-maintainable.

## 15. Risks and Anti-Patterns
*A pre-mortem — the ways this repo most plausibly fails, and the guard against each.*
- **Scope creep** ("just one more client, provider, or mode") erodes
  solo-maintainability until the project is abandoned. *Guard:* §6.2 plus the
  tripwires.
- **Quality erosion via breadth** — marking providers `agent_ready` without
  validation leads to broken tool-loops and lost trust. *Guard:* honest tiers;
  validation-gated first-class.
- **Security-default drift** — a refactor quietly binds `0.0.0.0`, logs a token, or
  loosens perms. *Guard:* security release tests as gates; tripwire #1.
- **Droid/provider API drift** silently breaks the bridge. *Guard:* integration
  tests against fakes; reactive maintenance is *expected*, not failure.
- **Dependency bloat or supply-chain risk.** *Guard:* tripwire #4, gitleaks,
  Dependabot, a lean dependency budget.
- **Pretending it is not solo** (roadmaps, SLAs) leads to burnout. *Guard:*
  maintenance mode and graceful sunset are blessed outcomes.
- **Over-abstraction** makes the codebase un-holdable in one head. *Guard:* YAGNI;
  forbidden-until-proven.
- **Agent damage** — a future coding agent "helpfully" widens scope, rewrites
  history, or weakens a default. *Guard:* §14.
- **Dangerous assumptions to keep checking:** "Droid's contract is stable" · "more
  providers means better" · "localhost means redaction is optional." All false.

## 16. Open Questions and Future Evolution
- When does `v1.0.0`'s compatibility promise feel *earned*? (Maintainer's call; no
  deadline.)
- If Factory Droid ships native BYOK/OAuth that covers the maintainer's needs, is
  that the sunset trigger? (Likely yes — and that is fine.)
- Windows support? (Currently macOS/Linux; out of scope unless nearly free.)
- Ever publish prebuilt release binaries, versus build-from-source only? (Open; the
  tooling exists.)

*Tracked here as questions, not scope commitments.*
