#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
tmp_base="${TMPDIR:-/tmp}"
mkdir -p "$tmp_base"
TMP="$(mktemp -d "$tmp_base/droid-proxy-install-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

asset_dir="$TMP/assets"
mkdir -p "$asset_dir/src"

write_checksums() {
  (
    cd "$asset_dir"
    shasum -a 256 "droid-proxy_linux_amd64.tar.gz" | awk '{print $1 "  " $2}' > checksums.txt
  )
}

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
  write_checksums
}

make_traversal_asset() {
  rm -rf "$asset_dir/src" "$asset_dir/evil"
  mkdir -p "$asset_dir/src" "$asset_dir/evil"
  cat > "$asset_dir/src/droid-proxy" <<'EOF'
#!/usr/bin/env sh
exit 0
EOF
  chmod 0755 "$asset_dir/src/droid-proxy"
  printf 'should not be extracted\n' > "$asset_dir/evil/owned"
  (
    cd "$asset_dir/src"
    tar -czf "$asset_dir/droid-proxy_linux_amd64.tar.gz" droid-proxy ../evil/owned
  )
  write_checksums
}

make_symlink_asset() {
  rm -rf "$asset_dir/src"
  mkdir -p "$asset_dir/src"
  ln -s /bin/sh "$asset_dir/src/droid-proxy"
  tar -C "$asset_dir/src" -czf "$asset_dir/droid-proxy_linux_amd64.tar.gz" droid-proxy
  write_checksums
}

assert_file() {
  [[ -e "$1" ]] || {
    echo "missing expected file: $1" >&2
    exit 1
  }
}

assert_installed_version() {
  local want="$1"
  if ! "$prefix/bin/droid-proxy" --version | grep -q "$want"; then
    echo "installed binary did not report $want" >&2
    exit 1
  fi
}

expect_install_failure() {
  local label="$1"
  local want="$2"
  local out="$TMP/$label.out"
  local work="$TMP/install-work-$label"
  rm -rf "$work"
  mkdir -p "$work"
  if DROID_PROXY_INSTALL_BASE_URL="file://$asset_dir" \
    DROID_PROXY_INSTALL_OS=linux \
    DROID_PROXY_INSTALL_ARCH=amd64 \
    DROID_PROXY_INSTALL_INTERACTIVE=0 \
    DROID_PROXY_INSTALL_SKIP_DOCTOR=1 \
    DROID_PROXY_INSTALL_TMPDIR="$work" \
      sh "$ROOT/scripts/install.sh" --version 9.9.11 --prefix "$prefix" >"$out" 2>&1; then
    echo "$label install unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -q "$want" "$out"; then
    echo "$label output missing expected message: $want" >&2
    cat "$out" >&2
    exit 1
  fi
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
assert_installed_version "9.9.9"

make_asset "9.9.10" "second"
DROID_PROXY_INSTALL_BASE_URL="file://$asset_dir" \
DROID_PROXY_INSTALL_OS=linux \
DROID_PROXY_INSTALL_ARCH=amd64 \
DROID_PROXY_INSTALL_INTERACTIVE=0 \
DROID_PROXY_INSTALL_SKIP_DOCTOR=1 \
  sh "$ROOT/scripts/install.sh" --version 9.9.10 --prefix "$prefix"
assert_installed_version "9.9.10"

make_asset "9.9.11" "bad"
printf '0000000000000000000000000000000000000000000000000000000000000000  droid-proxy_linux_amd64.tar.gz\n' > "$asset_dir/checksums.txt"
expect_install_failure "checksum-mismatch" "checksum mismatch"
assert_installed_version "9.9.10"

make_traversal_asset
expect_install_failure "archive-traversal" "unsafe archive entry"
if [[ -e "$TMP/install-work-archive-traversal/evil/owned" ]]; then
  echo "path traversal archive wrote outside extraction directory" >&2
  exit 1
fi
assert_installed_version "9.9.10"

make_symlink_asset
expect_install_failure "archive-symlink" "archive contains link entries"
assert_installed_version "9.9.10"

echo "install_test: ok"
