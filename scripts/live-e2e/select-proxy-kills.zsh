#!/usr/bin/env zsh
#
# select-proxy-kills.zsh — pure kill-candidate selection for live-e2e cleanup.
#
# Reads a process table on stdin and prints, one PID per line, the processes
# that should be terminated:
#   * a process whose executable BASENAME is exactly a proxy binary
#     (droid-proxy / cursor-proxy), OR
#   * a process that OWNS one of the proxy ports.
#
# A process matched only by a repo-path (or any) substring in its argv is NOT
# selected — argv is carried in the table for the audit log only, never for
# selection. The selector's own process and any excluded PIDs (the caller passes
# the current shell + its ancestors) are never emitted.
#
# This unit KILLS NOTHING and calls no `ps`/`lsof` itself, so it is unit-testable
# with fixture inputs. The wrapper (01-clean-old-proxies.sh) gathers the real
# process/port data, pipes it here, and acts on the result.
#
# Stdin: one process per line, tab-separated: "<pid>\t<comm>\t<args>"
#        (comm may be a full path; its basename is used).
# Env:
#   PROXY_PORT_OWNER_PIDS  space/newline-separated PIDs owning proxy ports
#   PROXY_EXCLUDE_PIDS     space/newline-separated PIDs to never select
#   PROXY_BINARIES         space-separated proxy basenames
#                          (default: "droid-proxy cursor-proxy")

set -uo pipefail

typeset -A exclude owners is_binary selected

# Never select the selector's own process.
exclude[$$]=1
for pid in ${=PROXY_EXCLUDE_PIDS:-}; do
  [[ "$pid" == <-> ]] && exclude[$pid]=1
done
for pid in ${=PROXY_PORT_OWNER_PIDS:-}; do
  [[ "$pid" == <-> ]] && owners[$pid]=1
done
for b in ${=PROXY_BINARIES:-droid-proxy cursor-proxy}; do
  is_binary[$b]=1
done

while IFS=$'\t' read -r pid comm args; do
  [[ "$pid" == <-> ]] || continue
  (( ${+exclude[$pid]} )) && continue
  base="${comm:t}"
  if (( ${+is_binary[$base]} )) || (( ${+owners[$pid]} )); then
    selected[$pid]=1
  fi
done

# Port owners are candidates even if they had no row in the process table.
for pid in ${(k)owners}; do
  (( ${+exclude[$pid]} )) && continue
  selected[$pid]=1
done

for pid in ${(onk)selected}; do
  print -r -- "$pid"
done
