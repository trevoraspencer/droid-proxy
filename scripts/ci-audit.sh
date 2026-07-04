#!/usr/bin/env bash
# CI and module-path audit for droid-proxy.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0

info() { printf '[ci-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }

info "Starting CI audit in ${ROOT}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_cmd go

if [[ -f .github/workflows/ci.yml ]]; then
  pass "GitHub Actions CI workflow present"
else
  fail "missing .github/workflows/ci.yml"
fi

if [[ -f .github/dependabot.yml ]]; then
  pass "Dependabot config present"
else
  fail "missing .github/dependabot.yml"
fi

module_path="$(head -1 go.mod | awk '{print $2}')"
if [[ "$module_path" == "github.com/trevoraspencer/droid-proxy" ]]; then
  pass "go.mod module path is public: $module_path"
else
  fail "go.mod module path must be github.com/trevoraspencer/droid-proxy (got $module_path)"
fi

if go mod verify >/dev/null 2>&1; then
  pass "go mod verify"
else
  fail "go mod verify failed"
fi

info "Running CI-equivalent checks"
if test -z "$(gofmt -l .)"; then
  pass "gofmt clean"
else
  fail "gofmt found unformatted files"
  gofmt -l .
fi

if go vet ./...; then
  pass "go vet"
else
  fail "go vet failed"
fi

if go build -o /tmp/droid-proxy-ci-check ./cmd/droid-proxy; then
  pass "go build ./cmd/droid-proxy"
  rm -f /tmp/droid-proxy-ci-check
else
  fail "go build failed"
fi

info "Running module-path release tests"
if go test ./internal/security/ -run 'GoMod|GoSources|CIWorkflow|CIAudit' -count=1; then
  pass "module/CI release tests"
else
  fail "module/CI release tests failed"
fi

info "NOTE: full suite runs in GitHub Actions (.github/workflows/ci.yml)"

if (( failures > 0 )); then
  info "CI audit finished with ${failures} failure(s)"
  exit 1
fi

info "CI audit finished clean"
exit 0
