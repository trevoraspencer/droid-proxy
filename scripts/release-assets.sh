#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
VERSION_PKG="github.com/trevoraspencer/droid-proxy/internal/version"
PLATFORMS="${PLATFORMS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64}"
DRY_RUN=0

if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
fi

echo "release-assets: version=$VERSION commit=$COMMIT dist=$DIST_DIR"
if [[ "$DRY_RUN" != "1" ]]; then
  mkdir -p "$DIST_DIR"
  rm -f "$DIST_DIR"/checksums.txt
fi
for platform in $PLATFORMS; do
  os="${platform%/*}"
  arch="${platform#*/}"
  asset="droid-proxy_${os}_${arch}.tar.gz"
  echo "release-assets: $asset"
  if [[ "$DRY_RUN" == "1" ]]; then
    continue
  fi
  work="$DIST_DIR/.work-${os}-${arch}"
  rm -rf "$work"
  mkdir -p "$work"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -ldflags "-X ${VERSION_PKG}.Version=${VERSION} -X ${VERSION_PKG}.Commit=${COMMIT}" \
    -o "$work/droid-proxy" ./cmd/droid-proxy
  cp "$ROOT/LICENSE" "$work/LICENSE"
  cp "$ROOT/README.md" "$work/README.md"
  cp "$ROOT/internal/setup/install_config.yaml" "$work/install_config.yaml"
  tar -C "$work" -czf "$DIST_DIR/$asset" droid-proxy LICENSE README.md install_config.yaml
  rm -rf "$work"
  (
    cd "$DIST_DIR"
    shasum -a 256 "$asset" | awk '{print $1 "  " $2}' >> checksums.txt
  )
done

if [[ "$DRY_RUN" == "1" ]]; then
  echo "release-assets: dry run complete"
  exit 0
fi

cp "$ROOT/scripts/install.sh" "$DIST_DIR/install.sh"
(
  cd "$DIST_DIR"
  shasum -a 256 install.sh | awk '{print $1 "  " $2}' >> checksums.txt
)
echo "release-assets: wrote assets and checksums.txt"
