# Public release strategy (Phase 0)

This document records the **chosen strategy** for taking droid-proxy from private
development to a public GitHub repository.

## Decision

| Item | Choice |
|---|---|
| **Strategy** | Single-commit orphan branch |
| **Public branch name** | `main` (replace after review) |
| **Working branch during prep** | `public-main` (created locally by script) |
| **History** | Discard private dev commit timeline on the public `main` |
| **Backup** | Tag `private-archive/YYYYMMDD` on current `main` before rewrite |

### Why not keep full history?

The private repo has ~48 development commits including internal planning docs,
live-e2e donor-cleanup scripts, Factory validation artifacts, and iterative WIP.
A squashed public history:

- Is faster for strangers to audit (one diff to review)
- Avoids surfacing obsolete internal commit messages and file churn
- Reduces risk that an old commit contains something the current tree no longer has
- Pairs cleanly with Phase 1 secret scanning (audit the snapshot you ship)

### Alternatives considered

| Alternative | When it makes sense | Why we did not choose it |
|---|---|---|
| **Keep full history** | History is already clean and you want attribution | Private dev noise; harder pre-public audit |
| **Squash to N thematic commits** | Want some narrative without 48 commits | More review work; marginal benefit for v1 |
| **New public repo, push snapshot** | Want zero risk to private repo | Valid fallback; see below |

### Fallback: new public repository

If you prefer not to rewrite the private repo at all:

1. Create a new empty public repo on GitHub.
2. Run `make create-public-history` locally to build `public-main`.
3. Push `public-main` to the new repo's `main`.
4. Keep the private repo unchanged.

## Prerequisites (run in order)

1. Phase 5 CI audit passes (`make ci-audit`).
2. Phase 4 docs audit passes (`make docs-audit`).
3. Phase 3 cleanup is complete (`.factory/` removed, internal plan docs removed).
4. Phase 2 legal audit passes (`make legal-audit`).
5. `make pre-public-audit` — Phase 1 security scan must pass.
6. `make public-release-preflight` — Phase 0 readiness checks.
7. **Rotate** all API keys and OAuth credentials that ever touched the private repo.

## Execution

### 1. Dry run (default — no git mutations)

```bash
make public-release-preflight
make create-public-history
```

Prints the backup tag name, commit count being collapsed, and the commit message
that would be used. Makes no changes.

### 2. Create local orphan branch

```bash
make create-public-history APPLY=1
```

This will:

1. Re-run `public-release-preflight`
2. Tag current `main` as `private-archive/YYYYMMDD`
3. Create orphan branch `public-main` with one commit containing the current tree
4. **Not** push or replace `main`

### 3. Review the result

```bash
git log --oneline public-main
git diff main..public-main --stat
make test
make pre-public-audit
```

### 4. Publish (manual, destructive)

Only after review, with the GitHub repo still **private**:

```bash
git push origin private-archive/YYYYMMDD   # backup tag
git branch -M public-main main             # local rename
git push -f origin main                    # replaces remote main
```

Delete stale remote branches (`cursor/*`, etc.) after the flip.

### 5. Flip visibility

In GitHub repo settings, change visibility to **public** only after steps 1–4 pass.

## What the single commit message contains

The script uses a structured initial-public-release message summarizing the
project purpose. Edit `scripts/create-public-history.sh` (`COMMIT_MSG`) if you
want to adjust wording before `APPLY=1`.

## Post-publish

- Before the first tag, GitHub's latest-release endpoint is expected to return
  404 because no releases exist yet. That is intentional before `v0.1.0`.
- Tag `v0.1.0` on the reviewed public `main`; the release workflow builds and
  attaches `install.sh`, checksums, and macOS/Linux archives:

  ```bash
  git status --short
  make ci-audit
  make release-dry-run
  git tag -a v0.1.0 -m "droid-proxy v0.1.0"
  git push origin v0.1.0
  ```

  Review the generated GitHub release assets and release notes before
  announcing the release.
- Enable GitHub secret scanning and Dependabot (Phase 6)
- Run a fresh-machine installer smoke test:

  ```bash
  curl -fsSL https://github.com/trevoraspencer/droid-proxy/releases/latest/download/install.sh | sh
  droid-proxy doctor
  ```
