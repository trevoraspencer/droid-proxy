#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
tmp_base="${TMPDIR:-/tmp}"
mkdir -p "$tmp_base"
TMP="$(mktemp -d "$tmp_base/droid-proxy-install-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

asset_dir="$TMP/assets"
mkdir -p "$asset_dir/src"

make_asset() {
  local version="$1"
  local message="$2"
  rm -rf "$asset_dir/src"
  mkdir -p "$asset_dir/src"
  cat > "$asset_dir/src/droid-proxy" <<EOF
#!/usr/bin/env sh
set -eu
case "\${1:-}" in
  --version)
    echo "droid-proxy $version"
    ;;
  setup)
    echo "setup \$*" >> "$TMP/setup.log"
    ;;
  doctor)
    echo "doctor ok"
    ;;
  config)
    echo "config should not run in noninteractive test" >> "$TMP/config.log"
    ;;
  *)
    echo "$message"
    ;;
esac
EOF
  chmod 0755 "$asset_dir/src/droid-proxy"
  tar -C "$asset_dir/src" -czf "$asset_dir/droid-proxy_linux_amd64.tar.gz" droid-proxy
  (
    cd "$asset_dir"
    shasum -a 256 "droid-proxy_linux_amd64.tar.gz" | awk '{print $1 "  " $2}' > checksums.txt
  )
}

assert_file() {
  [[ -e "$1" ]] || {
    echo "missing expected file: $1" >&2
    exit 1
  }
}

make_asset "9.9.9" "first"
prefix="$TMP/prefix"
DROID_PROXY_INSTALL_BASE_URL="file://$asset_dir" \
DROID_PROXY_INSTALL_OS=linux \
DROID_PROXY_INSTALL_ARCH=amd64 \
DROID_PROXY_INSTALL_INTERACTIVE=0 \
DROID_PROXY_INSTALL_SKIP_DOCTOR=1 \
  sh "$ROOT/scripts/install.sh" --version 9.9.9 --prefix "$prefix"

assert_file "$prefix/bin/droid-proxy"
assert_file "$TMP/setup.log"
if [[ -e "$TMP/config.log" ]]; then
  echo "noninteractive install should not launch config" >&2
  exit 1
fi
if ! "$prefix/bin/droid-proxy" --version | grep -q "9.9.9"; then
  echo "installed binary did not report 9.9.9" >&2
  exit 1
fi

make_asset "9.9.10" "second"
DROID_PROXY_INSTALL_BASE_URL="file://$asset_dir" \
DROID_PROXY_INSTALL_OS=linux \
DROID_PROXY_INSTALL_ARCH=amd64 \
DROID_PROXY_INSTALL_INTERACTIVE=0 \
DROID_PROXY_INSTALL_SKIP_DOCTOR=1 \
  sh "$ROOT/scripts/install.sh" --version 9.9.10 --prefix "$prefix"
if ! "$prefix/bin/droid-proxy" --version | grep -q "9.9.10"; then
  echo "rerun did not upgrade installed binary" >&2
  exit 1
fi

make_asset "9.9.11" "bad"
printf '0000000000000000000000000000000000000000000000000000000000000000  droid-proxy_linux_amd64.tar.gz\n' > "$asset_dir/checksums.txt"
bad_out="$TMP/bad.out"
if DROID_PROXY_INSTALL_BASE_URL="file://$asset_dir" \
  DROID_PROXY_INSTALL_OS=linux \
  DROID_PROXY_INSTALL_ARCH=amd64 \
  DROID_PROXY_INSTALL_INTERACTIVE=0 \
  DROID_PROXY_INSTALL_SKIP_DOCTOR=1 \
    sh "$ROOT/scripts/install.sh" --version 9.9.11 --prefix "$prefix" >"$bad_out" 2>&1; then
  echo "checksum mismatch install unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -q "checksum mismatch" "$bad_out"; then
  echo "checksum mismatch output missing expected message" >&2
  cat "$bad_out" >&2
  exit 1
fi

echo "install_test: ok"
