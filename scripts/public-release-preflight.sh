#!/usr/bin/env bash
# Phase 0 preflight: verify repo is ready to build a public-history orphan branch.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0

info() { printf '[public-release-preflight] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }
warn() { info "WARN: $*"; }

info "Phase 0 public release preflight"

if [[ ! -f "${ROOT}/docs/PUBLIC_RELEASE.md" ]]; then
  fail "docs/PUBLIC_RELEASE.md missing — record the release strategy first"
else
  pass "release strategy document present"
fi

if ! git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  fail "not a git repository"
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  fail "working tree is not clean — commit or stash changes before creating public history"
else
  pass "working tree clean"
fi

current_branch="$(git branch --show-current)"
info "Current branch: ${current_branch:-detached}"

commit_count="$(git rev-list --count HEAD 2>/dev/null || echo 0)"
info "Commits on current branch: ${commit_count}"
if (( commit_count <= 1 )); then
  warn "only one commit on HEAD — orphan squash may be unnecessary"
else
  pass "multi-commit history will be collapsed (${commit_count} commits)"
fi

if [[ -f "${ROOT}/scripts/pre-public-audit.sh" ]]; then
  info "Running Phase 1 pre-public-audit (required)"
  if bash "${ROOT}/scripts/pre-public-audit.sh"; then
    pass "Phase 1 pre-public-audit"
  else
    fail "Phase 1 pre-public-audit failed"
  fi
else
  fail "scripts/pre-public-audit.sh not found"
fi

info "Checking for common pre-public blockers in the working tree"
blockers=()
[[ -d "${ROOT}/.factory/docs" ]] && blockers+=(".factory/docs/ (internal agent notes)")
[[ -d "${ROOT}/.factory/research" ]] && blockers+=(".factory/research/ (internal research)")
[[ -f "${ROOT}/docs/IMPLEMENTATION_PLAN.md" ]] && blockers+=("docs/IMPLEMENTATION_PLAN.md (contains local donor paths)")
[[ -f "${ROOT}/docs/live-e2e/DONE.md" ]] && blockers+=("docs/live-e2e/DONE.md (internal validation log)")

if ((${#blockers[@]} > 0)); then
  for b in "${blockers[@]}"; do
    warn "still present: ${b} — remove in a later cleanup phase before publishing"
  done
else
  pass "no common internal-artifact blockers detected"
fi

if git ls-files --error-unmatch docs/PUBLIC_RELEASE.md >/dev/null 2>&1; then
  pass "docs/PUBLIC_RELEASE.md is tracked"
else
  fail "docs/PUBLIC_RELEASE.md is not tracked by git"
fi

info "MANUAL: confirm API keys and OAuth credentials have been rotated (Phase 1.2)"
info "MANUAL: confirm GitHub repo is still private until after you review public-main"

if (( failures > 0 )); then
  info "Preflight finished with ${failures} failure(s)"
  exit 1
fi

info "Preflight finished — ready for create-public-history (dry run or APPLY=1)"
exit 0
