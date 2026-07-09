#!/usr/bin/env bash
# Release asset contract audit for droid-proxy.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

failures=0

info() { printf '[release-audit] %s\n' "$*"; }
pass() { info "PASS: $*"; }
fail() { info "FAIL: $*"; failures=$((failures + 1)); }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

sha256_value() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

checksum_entry() {
  local checksums="$1"
  local name="$2"
  awk -v f="$name" '$2 == f {print $1}' "$checksums" | head -n 1
}

tar_contains() {
  local archive="$1"
  local entry="$2"
  local listing
  if ! listing="$(tar -tzf "$archive")"; then
    return 1
  fi
  printf '%s\n' "$listing" | grep -Fxq "$entry"
}

tar_excludes_sensitive_paths() {
  local archive="$1"
  local listing
  if ! listing="$(tar -tzf "$archive")"; then
    return 1
  fi
  if printf '%s\n' "$listing" | grep -Ei '(^|/)(\.env$|\.env\.local$|secrets\.env$|config\.yaml$|config\.local\.yaml$|\.factory/|.*\.pem$|.*\.p12$|.*\.pfx$|id_rsa$|.*\.key$)' >/dev/null; then
    return 1
  fi
  return 0
}

info "Starting release asset audit in ${ROOT}"

require_cmd go
require_cmd tar
has_checksum_tool=0
if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
  has_checksum_tool=1
  pass "sha256 checksum tool present"
else
  fail "required command not found: sha256sum or shasum"
fi

tmp_base="${TMPDIR:-/tmp}"
mkdir -p "$tmp_base"
TMP="$(mktemp -d "$tmp_base/droid-proxy-release-audit.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

dist="$TMP/dist"
audit_go_cache="${GOCACHE:-$TMP/go-build}"
audit_version="v0.0.0-audit"
audit_commit="releaseaudit1"
platforms="${RELEASE_AUDIT_PLATFORMS:-linux/amd64}"
mkdir -p "$audit_go_cache"

if GOCACHE="$audit_go_cache" DIST_DIR="$dist" VERSION="$audit_version" COMMIT="$audit_commit" PLATFORMS="$platforms" bash scripts/release-assets.sh; then
  pass "release-assets.sh built audit assets for ${platforms}"
else
  fail "release-assets.sh failed for ${platforms}"
fi

checksums="$dist/checksums.txt"
if [[ -s "$checksums" ]]; then
  pass "checksums.txt present"
else
  fail "checksums.txt missing or empty"
fi

if [[ -f "$dist/install.sh" ]]; then
  pass "install.sh copied to release dist"
else
  fail "install.sh missing from release dist"
fi

if [[ -s "$checksums" && -f "$dist/install.sh" && "$has_checksum_tool" == "1" ]]; then
  expected="$(checksum_entry "$checksums" install.sh)"
  actual="$(sha256_value "$dist/install.sh")"
  if [[ -n "$expected" && "$expected" == "$actual" ]]; then
    pass "install.sh checksum matches"
  else
    fail "install.sh checksum missing or mismatched"
  fi
fi

host_os="unknown"
host_arch="unknown"
if command -v go >/dev/null 2>&1; then
  host_os="$(go env GOOS)"
  host_arch="$(go env GOARCH)"
fi

for platform in $platforms; do
  os="${platform%/*}"
  arch="${platform#*/}"
  asset="droid-proxy_${os}_${arch}.tar.gz"
  archive="$dist/$asset"

  if [[ -f "$archive" ]]; then
    pass "asset present: $asset"
  else
    fail "missing asset: $asset"
    continue
  fi

  if [[ -s "$checksums" && "$has_checksum_tool" == "1" ]]; then
    expected="$(checksum_entry "$checksums" "$asset")"
    actual="$(sha256_value "$archive")"
    if [[ -n "$expected" && "$expected" == "$actual" ]]; then
      pass "checksum matches: $asset"
    else
      fail "checksum missing or mismatched: $asset"
    fi
  fi

  for entry in droid-proxy LICENSE README.md install_config.yaml; do
    if tar_contains "$archive" "$entry"; then
      pass "$asset contains $entry"
    else
      fail "$asset missing $entry"
    fi
  done

  if tar_excludes_sensitive_paths "$archive"; then
    pass "$asset excludes local config, secrets, and runtime paths"
  else
    fail "$asset contains sensitive or runtime-like paths"
  fi

  extract="$TMP/extract-${os}-${arch}"
  mkdir -p "$extract"
  if ! tar -xzf "$archive" -C "$extract"; then
    fail "$asset could not be extracted"
    continue
  fi
  if [[ -x "$extract/droid-proxy" ]]; then
    pass "$asset droid-proxy is executable"
  else
    fail "$asset droid-proxy is not executable"
  fi

  if [[ "$os" == "$host_os" && "$arch" == "$host_arch" && -x "$extract/droid-proxy" ]]; then
    version_out=""
    if version_out="$("$extract/droid-proxy" --version)" &&
      [[ "$version_out" == *"$audit_version"* && "$version_out" == *"$audit_commit"* ]]; then
      pass "$asset reports release ldflags"
    else
      fail "$asset version output missing release ldflags: $version_out"
    fi
  fi
done

if (( failures > 0 )); then
  info "Release audit finished with ${failures} failure(s)"
  exit 1
fi

info "Release audit finished clean"
exit 0
