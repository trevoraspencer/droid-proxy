#!/usr/bin/env bash
# Documentation consistency audit for droid-proxy.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0

info() { printf '[docs-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }

info "Starting documentation audit in ${ROOT}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_cmd go
require_cmd rg

for doc in README.md docs/README.md docs/UPGRADE.md CONTRIBUTING.md CHANGELOG.md VISION.md; do
  if [[ -f "$doc" ]]; then
    pass "doc present: $doc"
  else
    fail "missing doc: $doc"
  fi
done

if rg -qi 'not affiliated' README.md && rg -qi 'independent open-source' README.md; then
  pass "README disclaimer present"
else
  fail "README missing public disclaimer"
fi

if rg -q '## Security Model' README.md && rg -q '127\.0\.0\.1' README.md; then
  pass "README documents localhost security model"
else
  fail "README missing localhost security model guidance"
fi

if rg -q 'CONTRIBUTING.md' README.md && rg -q 'CHANGELOG.md' README.md; then
  pass "README links contributor and changelog docs"
else
  fail "README should reference CONTRIBUTING.md and CHANGELOG.md"
fi

info "Running documentation consistency tests"
if go test ./internal/config/ -run '^TestDocs' -count=1; then
  pass "config docs tests"
else
  fail "config docs tests failed"
fi

if go test ./internal/security/ -run '^Test.*Docs|TestREADME' -count=1; then
  pass "security docs tests"
else
  fail "security docs tests failed"
fi

if (( failures > 0 )); then
  info "Docs audit finished with ${failures} failure(s)"
  exit 1
fi

info "Docs audit finished clean"
exit 0
