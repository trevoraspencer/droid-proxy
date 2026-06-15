#!/usr/bin/env bash
# Phase 4 documentation audit for droid-proxy public release.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0

info() { printf '[docs-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }

info "Starting Phase 4 documentation audit in ${ROOT}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_cmd go
require_cmd rg

for doc in CONTRIBUTING.md CHANGELOG.md README.md docs/README.md; do
  if [[ -f "$doc" ]]; then
    pass "doc present: $doc"
  else
    fail "missing doc: $doc"
  fi
done

if rg -q 'not affiliated' README.md && rg -q 'independent open-source' README.md; then
  pass "README disclaimer present"
else
  fail "README missing public disclaimer"
fi

if rg -q '## Security model' README.md || rg -q 'localhost' README.md; then
  pass "README mentions security/localhost expectations"
else
  fail "README missing security model guidance"
fi

if rg -q 'CONTRIBUTING' README.md; then
  pass "README links to contributing guide"
else
  fail "README should reference CONTRIBUTING.md"
fi

info "Running documentation consistency tests"
if go test ./internal/config/ -run '^TestDocs' -count=1; then
  pass "config docs tests"
else
  fail "config docs tests failed"
fi

if go test ./internal/security/ -run '^TestDocs' -count=1; then
  pass "security docs tests"
else
  fail "security docs tests failed"
fi

info "MANUAL: read README quickstart on a fresh clone before publishing"

if (( failures > 0 )); then
  info "Docs audit finished with ${failures} failure(s)"
  exit 1
fi

info "Docs audit finished clean"
exit 0
