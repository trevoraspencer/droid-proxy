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

checksum_entry_count() {
  local checksums="$1"
  local name="$2"
  awk -v f="$name" '$2 == f {count++} END {print count + 0}' "$checksums"
}

audit_action_pins() {
  local workflow="$1"
  local line ref
  local action_count=0

  while IFS= read -r line; do
    [[ "$line" =~ uses:[[:space:]]*([^[:space:]#]+) ]] || continue
    ref="${BASH_REMATCH[1]}"
    [[ "$ref" == ./* || "$ref" == docker://* ]] && continue
    action_count=$((action_count + 1))
    if [[ "$ref" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$ ]] &&
      [[ "$line" =~ \#[[:space:]]*v[0-9]+([.][0-9]+)* ]]; then
      pass "$workflow pins $ref with a readable version comment"
    else
      fail "$workflow has an unpinned or uncommented action: ${line#"${line%%[![:space:]]*}"}"
    fi
  done < "$workflow"

  if (( action_count == 0 )); then
    fail "$workflow contains no auditable GitHub Action references"
  fi
}

require_workflow_text() {
  local workflow="$1"
  local needle="$2"
  local description="$3"
  if grep -Fq -- "$needle" "$workflow"; then
    pass "$description"
  else
    fail "$description"
  fi
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
require_cmd python3
has_checksum_tool=0
if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
  has_checksum_tool=1
  pass "sha256 checksum tool present"
else
  fail "required command not found: sha256sum or shasum"
fi

ci_workflow=".github/workflows/ci.yml"
release_workflow=".github/workflows/release.yml"

for workflow in "$ci_workflow" "$release_workflow"; do
  if [[ -f "$workflow" ]]; then
    pass "$workflow present"
    audit_action_pins "$workflow"
  else
    fail "$workflow missing"
  fi
done

if [[ -f "$ci_workflow" ]]; then
  require_workflow_text "$ci_workflow" "make release-audit" "CI runs the release hardening audit"
  require_workflow_text "$ci_workflow" "fa0500f6b7e41d28791ebc680f5dd9899cd42b58629218a5f041efa899151a8e" "CI checksum-pins the gitleaks artifact"
  if grep -Eq '(curl|wget)[^|]*\|' "$ci_workflow"; then
    fail "CI must not pipe network responses into another process"
  else
    pass "CI does not pipe network responses into another process"
  fi
fi

if [[ -f "$release_workflow" ]]; then
  for spec in \
    "SYFT_VERSION: \"1.46.0\"|release workflow pins the Syft version" \
    "SYFT_SHA256: \"d654f678b709eb53c393d38519d5ed7d2e57205529404018614cfefa0fb2b5ca\"|release workflow pins the Syft artifact checksum" \
    "bash scripts/validate-release-version.sh|release workflow validates SemVer" \
    "tag_commit=|release workflow resolves the tag commit" \
    "overwrite_files: false|release workflow refuses asset replacement" \
    "--output spdx-json=dist/droid-proxy.spdx.json|release workflow selects SPDX JSON" \
    "sha256sum droid-proxy.spdx.json >> checksums.txt|release workflow checksums the SBOM" \
    "name: Attest build provenance|release workflow generates build provenance attestation" \
    "name: Attest release SBOM|release workflow generates SBOM attestation" \
    "sbom-path: dist/droid-proxy.spdx.json|release workflow supplies the SBOM predicate" \
    "dist/droid-proxy.spdx.json|release workflow publishes the SBOM" \
    "COMMIT: \${{ github.sha }}|release build embeds the full triggering commit"; do
    needle="${spec%%|*}"
    description="${spec#*|}"
    require_workflow_text "$release_workflow" "$needle" "$description"
  done

  if grep -Fq "workflow_dispatch" "$release_workflow"; then
    fail "release workflow must publish only from an existing pushed tag"
  else
    pass "release workflow publishes only from an existing pushed tag"
  fi

  if python3 scripts/audit-workflow-permissions.py; then
    pass "workflow permissions are parsed structurally and exactly constrained"
  else
    fail "workflow permission maps are unsafe or malformed"
  fi

  for valid in v0.2.0 v1.2.3-alpha.1 v1.2.3+build.1 v1.2.3-rc.1+build.7; do
    if ! bash scripts/validate-release-version.sh "$valid" >/dev/null; then
      fail "valid SemVer tag rejected: $valid"
    fi
  done
  for invalid in 0.2.0 v01.2.3 v1.02.3 v1.2.03 v1.2.3.4 v1.2.3..x v1.2; do
    if bash scripts/validate-release-version.sh "$invalid" >/dev/null 2>&1; then
      fail "invalid SemVer tag accepted: $invalid"
    fi
  done
  pass "release tag validator enforces SemVer syntax"

  attest_count="$(grep -Ec 'uses:[[:space:]]+actions/attest@[0-9a-f]{40}' "$release_workflow" || true)"
  if [[ "$attest_count" == "2" ]]; then
    pass "release workflow has separate provenance and SBOM attestations"
  else
    fail "release workflow must invoke actions/attest exactly twice (found $attest_count)"
  fi

  verify_line="$(grep -nF "name: Verify release bundle checksums" "$release_workflow" | head -n 1 | cut -d: -f1 || true)"
  attest_line="$(grep -nF "name: Attest build provenance" "$release_workflow" | head -n 1 | cut -d: -f1 || true)"
  upload_line="$(grep -nF "name: Upload release assets" "$release_workflow" | head -n 1 | cut -d: -f1 || true)"
  if [[ -n "$verify_line" && -n "$attest_line" && -n "$upload_line" ]] &&
    (( verify_line < attest_line && attest_line < upload_line )); then
    pass "release bundle is verified and attested before publication"
  else
    fail "release workflow must verify, then attest, then publish assets"
  fi
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
  if LC_ALL=C sort -c "$checksums" >/dev/null 2>&1; then
    pass "checksums.txt is deterministically sorted"
  else
    fail "checksums.txt is not sorted"
  fi
  if awk 'NF != 2 || $1 !~ /^[0-9a-f]{64}$/ || $2 ~ /\// {exit 1}' "$checksums"; then
    pass "checksums.txt uses SHA-256 values and release basenames"
  else
    fail "checksums.txt contains malformed digests or paths"
  fi
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
  count="$(checksum_entry_count "$checksums" install.sh)"
  if [[ -n "$expected" && "$expected" == "$actual" && "$count" == "1" ]]; then
    pass "install.sh checksum matches"
  else
    fail "install.sh checksum missing, duplicated, or mismatched"
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
    count="$(checksum_entry_count "$checksums" "$asset")"
    if [[ -n "$expected" && "$expected" == "$actual" && "$count" == "1" ]]; then
      pass "checksum matches: $asset"
    else
      fail "checksum missing, duplicated, or mismatched: $asset"
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
