#!/usr/bin/env bash
# Verify one Gitleaks release archive against the repository's pinned manifest.
set -euo pipefail

if [[ "$#" -ne 3 ]]; then
  printf 'usage: %s CHECKSUMS ASSET ARCHIVE\n' "$0" >&2
  exit 2
fi

checksums="$1"
asset="$2"
archive="$3"

if [[ ! -f "$checksums" ]]; then
  printf '[verify-gitleaks] checksum manifest not found: %s\n' "$checksums" >&2
  exit 1
fi
if [[ ! -f "$archive" ]]; then
  printf '[verify-gitleaks] archive not found: %s\n' "$archive" >&2
  exit 1
fi

entry_count="$(awk -v asset="$asset" '$2 == asset {count++} END {print count + 0}' "$checksums")"
if [[ "$entry_count" != "1" ]]; then
  printf '[verify-gitleaks] expected exactly one checksum for %s, found %s\n' "$asset" "$entry_count" >&2
  exit 1
fi
expected="$(awk -v asset="$asset" '$2 == asset {print $1}' "$checksums")"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$archive" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
else
  printf '[verify-gitleaks] required command not found: sha256sum or shasum\n' >&2
  exit 1
fi

if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  printf '[verify-gitleaks] malformed checksum for %s\n' "$asset" >&2
  exit 1
fi
if [[ "$actual" != "$expected" ]]; then
  printf '[verify-gitleaks] checksum mismatch for %s\n' "$asset" >&2
  exit 1
fi

printf '[verify-gitleaks] checksum verified: %s\n' "$asset"
