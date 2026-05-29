#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"

DONOR_PROCESS_PATTERN='CLIProxyAPIPlus|CLIProxyAPI|cliproxy|VibeProxy|vibeproxy'
ARCHIVE_DIR="$HOME/.local/share/droid-proxy-archives/$LIVE_E2E_RUN_ID"
MANIFEST="$ARCHIVE_DIR/manifest.tsv"
mkdir -p "$ARCHIVE_DIR"
: > "$MANIFEST"
require_cmd shasum

info "Recording current proxy state"
pgrep -fl "$DONOR_PROCESS_PATTERN|droid-proxy|cursor-proxy" \
  2>"$LIVE_E2E_RUN_DIR/processes.clean.before.err" \
  | tee "$LIVE_E2E_RUN_DIR/processes.clean.before.txt" || true
lsof -nP -iTCP -sTCP:LISTEN \
  | rg 'CLIProxy|cliproxy|VibeProxy|vibeproxy|droid-proxy|cursor-proxy|:8787|:1455|:56121|:8000|:11434' \
  | tee "$LIVE_E2E_RUN_DIR/listeners.clean.before.txt" || true

if [[ -f "$HOME/.factory/settings.json" ]]; then
  cp -p "$HOME/.factory/settings.json" \
    "$HOME/.factory/settings.json.pre-droid-proxy-live-e2e.$LIVE_E2E_RUN_ID"
fi

info "Stopping CLIProxyAPIPlus/VibeProxy processes"
pkill -f "$DONOR_PROCESS_PATTERN" || true
sleep 1

info "Archiving and removing donor proxy repos"
typeset -a donor_roots
donor_roots=("$HOME/code" "$HOME/Developer" "$HOME/Documents/GitHub")

for root in "${donor_roots[@]}"; do
  [[ -d "$root" ]] || continue
  find "$root" \
    \( -name .git -o -name node_modules -o -name vendor \) -prune -o \
    -type d \
    \( -iname 'CLIProxyAPIPlus' -o -iname 'CLIProxyAPI' -o -iname 'cliproxyapi' -o -iname 'VibeProxy' -o -iname 'vibeproxy' \) \
    -print 2>/dev/null
done | while IFS= read -r donor; do
  [[ -n "$donor" ]] || continue
  local_real="$(cd "$donor" && pwd -P)"
  repo_real="$(cd "$LIVE_E2E_REPO_ROOT" && pwd -P)"

  if [[ "$local_real" == "$repo_real" || "$local_real" == *"/cursor-proxy"* ]]; then
    info "Skipping $donor"
    continue
  fi

  base="$(basename "$donor")"
  parent="$(dirname "$donor")"
  hash_line="$(print -nr -- "$local_real" | shasum -a 256)"
  hash="${hash_line%% *}"
  archive="$ARCHIVE_DIR/$base-$hash.tgz"
  if [[ -e "$archive" ]]; then
    fail "archive already exists for $donor: $archive"
  fi
  info "Archiving $donor to $archive"
  if ! tar -C "$parent" -czf "$archive" "$base"; then
    fail "archive failed for $donor"
  fi
  [[ -s "$archive" ]] || fail "archive is empty for $donor: $archive"
  print -r -- "$local_real	$archive" >> "$MANIFEST"
  info "Removing $donor"
  rm -rf "$donor"
done

{
  type -a cliproxy cliproxyapi CLIProxyAPIPlus vibeproxy VibeProxy 2>/dev/null || true
  launchctl list 2>/dev/null | rg -i 'cliproxy|vibeproxy' || true
  npm ls -g --depth=0 2>/dev/null | rg -i 'cliproxy|vibeproxy' || true
  pipx list 2>/dev/null | rg -i 'cliproxy|vibeproxy' || true
} | tee "$LIVE_E2E_RUN_DIR/stale-launchers.txt" || true

if pgrep -fl "$DONOR_PROCESS_PATTERN" > "$LIVE_E2E_RUN_DIR/processes.clean.after.txt" 2>"$LIVE_E2E_RUN_DIR/processes.clean.after.err"; then
  cat "$LIVE_E2E_RUN_DIR/processes.clean.after.txt"
  fail "old proxy process still running"
fi

for port in 8787 1455 56121; do
  port_listeners "$port" | tee "$LIVE_E2E_RUN_DIR/listeners.port-$port.after.txt" || true
done

info "Old donor proxy cleanup complete"
