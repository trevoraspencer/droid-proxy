#!/usr/bin/env bash
# Phase 0: create a single-commit orphan branch for public release.
# Default is dry-run. Set APPLY=1 to mutate local git state.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

APPLY="${APPLY:-0}"
PUBLIC_BRANCH="${PUBLIC_BRANCH:-public-main}"
SOURCE_BRANCH="${SOURCE_BRANCH:-$(git branch --show-current)}"
BACKUP_TAG="private-archive/$(date +%Y%m%d)"

COMMIT_MSG="Initial public release of droid-proxy

Localhost HTTP proxy for Factory Droid BYOK and custom models.

Supports Anthropic, OpenAI-compatible providers, OAuth (Codex/xAI),
reasoning replay, and Codex multi-account pooling.

See README.md and docs/ for configuration and provider setup."

info() { printf '[create-public-history] %s\n' "$*"; }

commit_count="$(git rev-list --count "${SOURCE_BRANCH}" 2>/dev/null || echo 0)"
tracked_count="$(git ls-files | wc -l | tr -d ' ')"

info "Phase 0 — create public history"
info "Mode: $([[ "$APPLY" == "1" ]] && echo APPLY || echo DRY-RUN)"
info "Source branch: ${SOURCE_BRANCH}"
info "Public branch: ${PUBLIC_BRANCH}"
info "Commits to collapse: ${commit_count}"
info "Tracked files in tree: ${tracked_count}"
info "Backup tag (on source): ${BACKUP_TAG}"
echo
info "Commit message preview:"
printf '%s\n' "$COMMIT_MSG" | sed 's/^/  /'
echo

if [[ "$APPLY" != "1" ]]; then
  info "DRY-RUN: no git changes made."
  info "Run: APPLY=1 make create-public-history"
  info "Or:  APPLY=1 bash scripts/create-public-history.sh"
  exit 0
fi

info "Running public-release-preflight before applying"
bash "${ROOT}/scripts/public-release-preflight.sh"

if git show-ref --verify --quiet "refs/heads/${PUBLIC_BRANCH}"; then
  info "Removing existing local branch ${PUBLIC_BRANCH}"
  git branch -D "${PUBLIC_BRANCH}"
fi

if git show-ref --verify --quiet "refs/tags/${BACKUP_TAG}"; then
  BACKUP_TAG="${BACKUP_TAG}-$(date +%H%M%S)"
  info "Backup tag collision — using ${BACKUP_TAG}"
fi

info "Tagging ${SOURCE_BRANCH} as ${BACKUP_TAG}"
git tag "${BACKUP_TAG}" "${SOURCE_BRANCH}"

info "Creating orphan branch ${PUBLIC_BRANCH}"
git checkout --orphan "${PUBLIC_BRANCH}"
git add -A

if git diff --cached --quiet; then
  info "ERROR: orphan branch has no staged changes"
  git checkout "${SOURCE_BRANCH}"
  exit 1
fi

git commit -m "${COMMIT_MSG}"

info "Created ${PUBLIC_BRANCH} at $(git rev-parse --short HEAD)"
info "Backup of ${SOURCE_BRANCH} saved as tag ${BACKUP_TAG}"
info ""
info "Next steps:"
info "  git log --oneline ${PUBLIC_BRANCH}"
info "  git diff ${SOURCE_BRANCH}..${PUBLIC_BRANCH} --stat"
info "  make test && make pre-public-audit"
info ""
info "To publish (manual, while repo is still private):"
info "  git push origin ${BACKUP_TAG}"
info "  git branch -M ${PUBLIC_BRANCH} main"
info "  git push -f origin main"
info ""
info "Return to dev branch: git checkout ${SOURCE_BRANCH}"

exit 0
