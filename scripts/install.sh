#!/usr/bin/env sh
set -eu

REPO="trevoraspencer/droid-proxy"
PREFIX="${PREFIX:-$HOME/.local}"
VERSION=""
YES=0
NO_CONFIG=0
NO_SERVICE=0
RESTART=0
NO_RESTART=0

usage() {
  cat <<'USAGE'
droid-proxy installer

Usage:
  install.sh [--version VERSION] [--prefix DIR] [--yes] [--no-config] [--no-service] [--restart] [--no-restart]

Installs droid-proxy as a per-user binary under ~/.local/bin by default.
Re-run the same command to upgrade. Existing config and secrets are preserved.
Use --restart to restart a running proxy after the new binary is installed.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || { echo "install.sh: --version needs a value" >&2; exit 2; }
      VERSION="$2"
      shift 2
      ;;
    --prefix)
      [ "$#" -ge 2 ] || { echo "install.sh: --prefix needs a value" >&2; exit 2; }
      PREFIX="$2"
      shift 2
      ;;
    --yes|-y)
      YES=1
      shift
      ;;
    --no-config)
      NO_CONFIG=1
      shift
      ;;
    --no-service)
      NO_SERVICE=1
      shift
      ;;
    --restart)
      RESTART=1
      shift
      ;;
    --no-restart)
      NO_RESTART=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "install.sh: unknown argument $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "install.sh: required command not found: $1" >&2
    exit 1
  }
}

detect_os() {
  if [ -n "${DROID_PROXY_INSTALL_OS:-}" ]; then
    printf '%s\n' "$DROID_PROXY_INSTALL_OS"
    return
  fi
  case "$(uname -s)" in
    Darwin) printf 'darwin\n' ;;
    Linux) printf 'linux\n' ;;
    *) echo "install.sh: unsupported OS $(uname -s)" >&2; exit 1 ;;
  esac
}

detect_arch() {
  if [ -n "${DROID_PROXY_INSTALL_ARCH:-}" ]; then
    printf '%s\n' "$DROID_PROXY_INSTALL_ARCH"
    return
  fi
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    *) echo "install.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
  esac
}

sha256_verify() {
  file="$1"
  sums="$2"
  base="$(basename "$file")"
  expected="$(awk -v f="$base" '$2 == f {print $1}' "$sums" | head -n 1)"
  if [ -z "$expected" ]; then
    echo "install.sh: no checksum entry for $base" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  else
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  fi
  if [ "$actual" != "$expected" ]; then
    echo "install.sh: checksum mismatch for $base" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    exit 1
  fi
}

validate_archive_manifest() {
  file="$1"
  entries="$WORKDIR/archive-entries.txt"
  verbose="$WORKDIR/archive-entries.verbose.txt"
  found_binary=0

  if ! tar -tzf "$file" > "$entries"; then
    echo "install.sh: could not list archive entries for $(basename "$file")" >&2
    exit 1
  fi

  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    normalized="$entry"
    while [ "${normalized#./}" != "$normalized" ]; do
      normalized="${normalized#./}"
    done
    if [ -z "$normalized" ]; then
      echo "install.sh: unsafe empty archive entry in $(basename "$file")" >&2
      exit 1
    fi
    case "$normalized" in
      /*|../*|*/../*|*/..|..|*//*)
        echo "install.sh: unsafe archive entry: $entry" >&2
        exit 1
        ;;
    esac
    if [ "$normalized" = "droid-proxy" ]; then
      found_binary=1
    fi
  done < "$entries"

  if [ "$found_binary" != "1" ]; then
    echo "install.sh: archive did not contain droid-proxy" >&2
    exit 1
  fi

  if ! tar -tvzf "$file" > "$verbose"; then
    echo "install.sh: could not inspect archive entry types for $(basename "$file")" >&2
    exit 1
  fi
  if ! awk 'substr($1, 1, 1) == "l" || substr($1, 1, 1) == "h" { exit 1 }' "$verbose"; then
    echo "install.sh: archive contains link entries" >&2
    exit 1
  fi
}

download() {
  url="$1"
  out="$2"
  curl -fsSL "$url" -o "$out"
}

is_interactive() {
  [ "${DROID_PROXY_INSTALL_INTERACTIVE:-}" = "1" ] && return 0
  [ "${DROID_PROXY_INSTALL_INTERACTIVE:-}" = "0" ] && return 1
  [ -t 0 ] && [ -t 1 ]
}

confirm() {
  prompt="$1"
  if [ "$YES" = "1" ]; then
    return 0
  fi
  if ! is_interactive; then
    return 1
  fi
  printf '%s [y/N] ' "$prompt"
  read ans || return 1
  case "$ans" in
    y|Y|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

proxy_running() {
  "$BINDIR/droid-proxy" status 2>/dev/null | grep -q "is running"
}

need_cmd curl
need_cmd tar
OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ -n "${DROID_PROXY_INSTALL_BASE_URL:-}" ]; then
  BASE_URL="$DROID_PROXY_INSTALL_BASE_URL"
elif [ -n "$VERSION" ]; then
  BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
else
  BASE_URL="https://github.com/${REPO}/releases/latest/download"
fi

ASSET="droid-proxy_${OS}_${ARCH}.tar.gz"
WORKDIR="${DROID_PROXY_INSTALL_TMPDIR:-}"
if [ -z "$WORKDIR" ]; then
  tmp_base="${TMPDIR:-/tmp}"
  mkdir -p "$tmp_base"
  WORKDIR="$(mktemp -d "$tmp_base/droid-proxy-install.XXXXXX")"
  CLEAN_TMP=1
else
  mkdir -p "$WORKDIR"
  CLEAN_TMP=0
fi
cleanup() {
  if [ "${CLEAN_TMP:-0}" = "1" ]; then
    rm -rf "$WORKDIR"
  fi
}
trap cleanup EXIT INT TERM

archive="$WORKDIR/$ASSET"
checksums="$WORKDIR/checksums.txt"
download "${BASE_URL}/${ASSET}" "$archive"
download "${BASE_URL}/checksums.txt" "$checksums"
sha256_verify "$archive" "$checksums"
validate_archive_manifest "$archive"

extract="$WORKDIR/extract"
mkdir -p "$extract"
tar -xzf "$archive" -C "$extract"
if [ -L "$extract/droid-proxy" ] || [ ! -f "$extract/droid-proxy" ] || [ ! -x "$extract/droid-proxy" ]; then
  echo "install.sh: archive did not contain a regular executable droid-proxy" >&2
  exit 1
fi

BINDIR="${BINDIR:-$PREFIX/bin}"
mkdir -p "$BINDIR"
tmpbin="$BINDIR/.droid-proxy.install.$$"
cp "$extract/droid-proxy" "$tmpbin"
chmod 0755 "$tmpbin"
mv "$tmpbin" "$BINDIR/droid-proxy"

echo "installed: $BINDIR/droid-proxy"
"$BINDIR/droid-proxy" --version || true

if [ "$NO_CONFIG" = "0" ]; then
  "$BINDIR/droid-proxy" setup
fi

if is_interactive && [ "$NO_CONFIG" = "0" ]; then
  if confirm "Open the interactive droid-proxy config dashboard now?"; then
    "$BINDIR/droid-proxy" config
  fi
fi

if [ "$NO_SERVICE" = "0" ]; then
  if confirm "Install and start the per-user service now?"; then
    if "$BINDIR/droid-proxy" setup --service; then
      SERVICE_STARTED=1
    else
      echo "install.sh: service setup did not complete; run droid-proxy config, then droid-proxy setup --service" >&2
    fi
  fi
fi

if [ "$NO_RESTART" = "0" ] && [ "${SERVICE_STARTED:-0}" != "1" ] && proxy_running; then
  if [ "$RESTART" = "1" ] || confirm "Restart the running droid-proxy process now?"; then
    if "$BINDIR/droid-proxy" restart; then
      echo "restarted running droid-proxy"
    else
      echo "install.sh: restart did not complete; run droid-proxy restart, then droid-proxy doctor" >&2
      if [ "$RESTART" = "1" ]; then
        exit 1
      fi
    fi
  else
    echo "running droid-proxy was not restarted; run droid-proxy restart to use the new binary"
  fi
fi

if [ "${DROID_PROXY_INSTALL_SKIP_DOCTOR:-0}" != "1" ]; then
  "$BINDIR/droid-proxy" doctor || true
fi

cat <<EOF

Next steps:
  export PATH="$BINDIR:\$PATH"
  droid-proxy config
  droid-proxy setup --service
  droid-proxy doctor

Upgrade tip:
  rerun the installer with --restart when droid-proxy is already running
EOF
