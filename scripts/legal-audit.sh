#!/usr/bin/env bash
# Phase 2 legal and licensing audit for droid-proxy public release.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0
warnings=0

info() { printf '[legal-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }
warn() { info "WARN: $*"; warnings=$((warnings + 1)); }

info "Starting Phase 2 legal audit in ${ROOT}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_cmd go
require_cmd rg

# --- 2.1 LICENSE file ---
if [[ -f LICENSE ]]; then
  if rg -q 'MIT License' LICENSE && rg -q 'Copyright \(c\)' LICENSE; then
    pass "LICENSE present and appears to be MIT"
  else
    fail "LICENSE missing MIT or copyright notice"
  fi
else
  fail "LICENSE file missing"
fi

# --- 2.2 Required legal documents ---
for doc in NOTICE SECURITY.md CODE_OF_CONDUCT.md docs/THIRD_PARTY_LICENSES.md; do
  if [[ -f "$doc" ]]; then
    pass "legal document present: $doc"
  else
    fail "missing legal document: $doc"
  fi
done

# --- 2.2 Module integrity ---
if go mod verify >/dev/null 2>&1; then
  pass "go mod verify"
else
  fail "go mod verify failed"
fi

# --- 2.4 Public disclaimer in README ---
if rg -qi 'not affiliated' README.md && rg -qi 'independent open-source' README.md; then
  pass "README public disclaimer present"
else
  fail "README missing public disclaimer (not affiliated / independent open-source)"
fi

# --- 2.2 Dependency license allowlist (direct deps only) ---
info "Running legal release tests"
if go test ./internal/security/ -run 'Legal|License|Notice|SecurityPolicy|READMEContains|DependencyLicenses' -count=1; then
  pass "legal release tests"
else
  fail "legal release tests failed"
fi

info "MANUAL (2.2): confirm copyright holder name and year in LICENSE are correct"
info "MANUAL (2.4): confirm project name does not infringe third-party trademarks"
info "MANUAL (2.4): confirm adapted/reference code (if any) is documented"

if (( failures > 0 )); then
  info "Legal audit finished with ${failures} failure(s) and ${warnings} warning(s)"
  exit 1
fi

info "Legal audit finished clean with ${warnings} warning(s)"
exit 0
