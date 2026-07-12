#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if [[ ! "$version" =~ $semver ]]; then
  echo "invalid SemVer release tag: $version" >&2
  exit 1
fi
