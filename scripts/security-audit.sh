#!/usr/bin/env bash
# Security audit for tracked files, local ignore rules, and secret redaction tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0
warnings=0

info() { printf '[security-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }
warn() { info "WARN: $*"; warnings=$((warnings + 1)); }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

ensure_gitleaks() {
  if command -v gitleaks >/dev/null 2>&1; then
    return 0
  fi
  local ver="8.24.2"
  local os arch
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) fail "gitleaks auto-install: unsupported OS $(uname -s)"; return 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch="x64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) fail "gitleaks auto-install: unsupported arch $(uname -m)"; return 1 ;;
  esac
  local asset="gitleaks_${ver}_${os}_${arch}.tar.gz"
  local bindir="/tmp/gitleaks-${ver}-${os}-${arch}"
  local dest="${bindir}/gitleaks"
  info "Installing gitleaks ${ver} (${os}/${arch}) to ${dest}"
  mkdir -p "$bindir"
  curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v${ver}/${asset}" \
    | tar -xz -C "$bindir" gitleaks
  if [[ ! -x "$dest" ]]; then
    fail "gitleaks install failed"
    return 1
  fi
  export PATH="${bindir}:${PATH}"
}

info "Starting security audit in ${ROOT}"

require_cmd git
require_cmd rg
require_cmd go

ensure_gitleaks
if gitleaks detect --source . --config .gitleaks.toml --verbose --no-banner; then
  pass "gitleaks scan clean"
else
  fail "gitleaks reported potential secrets"
fi

history_hits="$(
  git log --all -p --no-color 2>/dev/null \
    | rg -i 'ghp_[a-zA-Z0-9]{20,}|github_pat_[a-zA-Z0-9_]{20,}|AKIA[0-9A-Z]{16}|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY' \
    || true
)"
if [[ -z "$history_hits" ]]; then
  pass "git history grep found no high-confidence credential patterns"
else
  fail "git history grep found high-confidence credential patterns"
  printf '%s\n' "$history_hits" | head -20
fi

tracked_sensitive="$(
  git ls-files \
    | rg -i '(^|/)(\.env$|\.env\.local$|secrets\.env$|config\.yaml$|config\.local\.yaml$|.*\.pem$|.*\.p12$|.*\.pfx$|id_rsa$|\.key$)' \
    | rg -v '\.env\.local\.example$' \
    || true
)"
if [[ -z "$tracked_sensitive" ]]; then
  pass "no tracked files match sensitive filename patterns"
else
  fail "tracked files match sensitive filename patterns:"
  printf '  %s\n' $tracked_sensitive
fi

must_ignore=(
  config.yaml
  config.local.yaml
  .env
  .env.local
  .factory/validation/
  secrets.env
)
for path in "${must_ignore[@]}"; do
  if git check-ignore -q "$path" 2>/dev/null; then
    pass "gitignore covers ${path}"
  else
    fail "gitignore does not cover ${path}"
  fi
done

if rg -n '=.{8,}' .env.local.example config.example.yaml 2>/dev/null \
  | rg -v '""|example|your-|changeme|127\.0\.0\.1|localhost|/v1|generic-chat|openai|anthropic|deepseek|mimo|groq|fireworks|ollama|vllm|codex|xai|moonshot|kimi|zai|droid-|8787|9787|model|alias|base_url|known_auth|provider|http' \
  >/dev/null; then
  warn "example config files may contain non-empty secret-like values; review manually"
else
  pass "example config files look placeholder-only"
fi

info "Running secret-redaction and safety tests"
if go test ./internal/logging/ ./internal/handlers/ -run 'Redact|Secret|Sentinel|Redaction|TraceLogging|DefaultLogging' -count=1; then
  pass "logging and handler secret-safety tests"
else
  fail "logging and handler secret-safety tests failed"
fi

if go test ./internal/security/ -count=1; then
  pass "tracked-file and documentation guards"
else
  fail "tracked-file and documentation guards failed"
fi

if [[ -f "${HOME}/.droid-proxy/live-e2e/secrets.env" ]]; then
  pass "live provider secrets are outside the repository"
elif [[ -f "${ROOT}/.factory/validation/live-e2e/secrets.env" ]]; then
  if git check-ignore -q ".factory/validation/live-e2e/secrets.env" 2>/dev/null; then
    pass "local live-e2e secrets.env is gitignored"
  else
    fail "local live-e2e secrets.env exists but is not gitignored"
  fi
else
  pass "no live-e2e secrets.env present in working tree"
fi

info "MANUAL: rotate any provider credentials used during local testing before publishing a release"

if (( failures > 0 )); then
  info "Audit finished with ${failures} failure(s) and ${warnings} warning(s)"
  exit 1
fi

info "Audit finished clean with ${warnings} warning(s)"
exit 0
