# Agent Guide

## Scope
- `projmux` is a standalone tmux session-management application.
- Keep portable session-management behavior in `projmux`.
- Keep machine-local policy outside the application unless the migration plan explicitly calls for it.
- Keep `AGENTS.md` focused on repo contract. Personal agent recipes, reverse-engineering notes, and machine-local operating memos belong in external local docs (Obsidian/dotfiles anchors), not this tracked file.
- Local-only agent overlays may live in an untracked `AGENTS.local.md` anchored via dotfiles. This tracked file stays shareable and tool-agnostic.

## Startup Checks
- Confirm you are in the intended checkout/worktree with `pwd`.
- Check local state with `git status --short`.

## Branch And Checkout Rules
- Use one branch per task. Preferred names: `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, `refactor/<topic>`, `chore/<topic>`.
- Use a dedicated checkout/worktree per task when parallel work would otherwise collide.
- Keep one agent per checkout/worktree. Do not share a dirty checkout across agents.
- If another agent owns a file, do not overwrite their changes. Adjust around them or coordinate a handoff.
- Keep changes narrow. Split docs, bootstrap, migration, and feature work into separate branches unless they are inseparable.

## Branch Protection And PR Flow
- `main` is protected by the repository ruleset `main-protect`. Direct pushes to `main` are blocked even for repository admins.
- Every change ships through a pull request. The required status checks are five CI **job names**: `Format`, `Unit Tests`, `NPM Packages`, `Integration Tests`, `E2E Tests`. The aggregate `Test` job is **not** required; it fans in every child, including the security and Darwin jobs the ruleset does not require.
- A required check is a job *name*. Renaming or splitting one of those five stops that context from ever being reported, and GitHub holds the PR at `expected` forever — every check green, merge blocked. Keep a thin aggregate job under the original name with `needs: [<new jobs>]` and `if: always()`; without `if: always()` the job skips on child failure, which is neither green nor red. `test/e2e/shard-contract.sh` fails when the `E2E Tests` aggregate is missing.
- Admin bypass mode is `pull_request`: the admin can self-merge a PR without approvals, but the PR itself is mandatory.
- Default merge method is **squash**. The PR title becomes the squash commit subject and is what release-please parses, so write it as a Conventional Commit. Follow [docs/pr-guideline.md](docs/pr-guideline.md) for full conventions.
- `make install` cannot run before the PR is merged into `main`. The full team-lead loop is: push branch → open PR → wait for CI → merge → `git pull --ff-only` → `make install`.

## Standard Dev Flow
- Make targets are the contract for local validation. Keep them stable and predictable.
- Refresh and validate the candidate base before every first push or force-push:
  1. `git fetch origin main`.
  2. Require `git merge-base --is-ancestor origin/main HEAD`. If it fails, rebase onto latest `origin/main` before continuing. The repository-policy range scan rejects a PR base that is not an ancestor of its head, so knowingly publishing that state only creates a deterministic failed CI run.
  3. Run the fast local gates in order: `make fmt` → `make fix` → `make test`.
  4. Refresh `origin/main` and check ancestry once more immediately before publishing. If main advanced, rebase and restart the fast local gates for the new head.
- Publish the validated head as soon as the fast gates pass; do not serialize remote CI behind the long-running local gates:
  5. `git push -u origin <branch>` for the first push, or `git push --force-with-lease` after a rebase.
  6. Create or refresh the PR with a Conventional Commit title (see [docs/pr-guideline.md](docs/pr-guideline.md)).
  7. While CI runs on that exact head, finish the remaining local gates in order: `make test-integration` → `make test-e2e`.
  8. If a local or remote gate fails, keep merge blocked, fix the cause, and publish a new validated head through the same flow. Any rebase invalidates the previous local gate evidence, so rerun the full local sequence for the rebased head while starting its CI after the fast gates.
  9. Wait for both the complete local gate sequence and the required CI `Test` check to turn green (`gh pr checks <num> --watch`). Use `--auto` on `gh pr merge` if you want it queued.
  10. `gh pr merge <num> --squash --delete-branch`.
- Promote the build only after merge:
  11. `git -C <repo> pull --ff-only`
  12. `make install` — atomic replace of `$(go env GOPATH)/bin/projmux` plus `projmux config apply`. **Never run it before step 11**; pre-merge state has not cleared CI yet and may not match what `main` will hold.
  13. Retire the merged checkout/worktree with your local tooling if you used one.
- If a target is missing for the area you are changing, add it or leave the repository in a state where the gap is explicit in docs and review notes.
- If behavior changes, update the maintained test list in [docs/agent-workflow.md](docs/agent-workflow.md) in the same branch.
- Do not skip `fmt` or `fix` because tests passed. Formatting, automatic fixes, and test execution are separate gates.

## Hook Contract Stability
- The post-create hook contract (`[hooks.post-create]`, `PROJMUX_*` env vars, 5s timeout) is part of the public API. Adding, removing, or renaming any `PROJMUX_*` env var requires at minimum a minor release input; use a `feat(hooks): ...` PR title and leave the version/manifest update to release-please.
- `PROJMUX_SOCKET` is the app socket name (`projmux`) supplied as hook routing metadata; it does not change how the tmux client invokes commands.
- `PROJMUX_PANE` is the exact first pane id returned by standard persistent/ephemeral session creation for `post-create`. It is intentionally absent from `pre-create`, which runs before that pane exists, and from snapshot replay whose multi-pane replay boundary exposes no single returned pane.

## Release Flow
- `release-please-action` watches `main`, accumulates Conventional Commit subjects, and opens or refreshes a "chore(main): release X.Y.Z" PR. That PR contains the version bump (`internal/version/version.go` + `.release-please-manifest.json`), `CHANGELOG.md` updates, and the release notes.
- Merging the release PR pushes the `vX.Y.Z` tag and creates the GitHub Release with auto-generated notes.
- `.github/workflows/release.yml` triggers on the tag push, builds the linux/darwin × amd64/arm64 matrix, and uploads tarballs to the release that already exists (`gh release upload --clobber`). Do not add hardcoded notes back to that workflow — release-please owns the notes.
- Non-Conventional commit subjects on `main` are silently skipped by release-please. Keep PR titles strict; squash merge ensures the PR title is the only subject that lands.

## Configuration And Environment

Project root and discovery:

- `PROJMUX_PROJDIR` is the canonical project-root env. It accepts an OS-native PATH-style multi-value (`filepath.SplitList`): the first non-empty entry is the primary repo root (memoized to `~/.config/projmux/projdir`), and any additional entries are prepended to managed roots. The legacy `PROJDIR`/`RP` env vars are no longer honored.
- `PROJMUX_MANAGED_ROOTS` is the colon-separated search-root override (priority: env > saved file > defaults). Legacy alias `TMUX_SESSIONIZER_ROOTS` is still honored at runtime.
- `~/.config/projmux/workdirs` stores the cumulative workdirs list managed via the Settings UX. It is read only when no env list is set.
- `tmux set-option -g @projmux_projdir <path>` is a declarative source for `PROJMUX_PROJDIR` that the switch command reads through `tmuxProjdirOption`.

Notifications:

- `PROJMUX_NOTIFY_HOOK` — external executable that receives AI desktop notifications instead of the built-in sender. The hook is invoked with positional arguments: summary, body, OS urgency, app name, tag, group, icon path. The urgency argument is the transient OS notification urgency, not the persistent notify-queue severity. When unset, projmux uses `notify-send` on Linux and PowerShell toasts on WSL.
- `PROJMUX_NOTIFY_EXPIRE_MS` — AI desktop notification expiration in milliseconds. Defaults to `5000`; unset, zero, negative, and non-numeric values fall back to the default.
- `PROJMUX_WSL_TOAST_ICON_DIR` — override directory for the icon copied into a Windows-readable path before the WSL toast call.

Usage tracking:

- `PROJMUX_USAGE_STATE_DIR` — override for the snapshot cache directory. Defaults to `<state>/projmux/usage`. Point at a synced location (Dropbox, iCloud Drive) to share authoritative usage between machines. Resolved verbatim, no `~` expansion.
- `PROJMUX_USAGE_DEBUG` — when non-empty, surfaces adapter errors from `projmux status usage` to stderr instead of swallowing them.
- `PROJMUX_USAGE_LIMITS_PATH` — deprecated. v2 takes limits straight from the upstream APIs; the variable is read but ignored.

Focus:

- `PROJMUX_FOCUS_DEBUG` — when non-empty, `projmux focus` prints a one-line telemetry record (target/session/window/pane/socket/source/kind) to stderr.

Post-create hook contract environment:

- `PROJMUX_SESSION` — session name being created.
- `PROJMUX_CWD` — absolute path of the project directory.
- `PROJMUX_SESSION_KIND` — `persistent` or `ephemeral`.
- `PROJMUX_SOCKET` — tmux app socket name (`projmux`), supplied as metadata so hook commands can use `tmux -L "$PROJMUX_SOCKET"`.
- `PROJMUX_PANE` — exact first pane id returned by `tmux new-session`, such as `%7`, for standard persistent/ephemeral creation; omitted for snapshot replay.
- `PROJMUX_VERSION` — the binary's `internal/version` string.

Tunables (rarely touched):

- `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` — top-level override for the AI desktop notification dedupe window; Settings saved value and default are used only when this env is unset or invalid.
- `PROJMUX_CODEX_TITLE_WATCH_INTERVAL`, `PROJMUX_CODEX_REPLY_SETTLE_LOOPS` — pacing knobs for the AI title-watch loop.

## Review Expectations
- Reviews should be small enough to reason about quickly.
- Include the command list you ran, especially the `make` targets and any parity checks.
- Call out behavior changes separately from refactors.
- Flag unverified areas instead of implying coverage you did not run.
- If migration parity is incomplete, state the exact gap and the follow-up branch or issue.

## Migration Discipline
- Port one stable slice at a time. Do not mix bootstrap, feature redesign, and parity fixes in one change without a strong reason.
- Match existing behavior first, then simplify or redesign in a later change.
- When replacing shell logic with Go, keep the user-facing entrypoints stable until the adapter layer is intentionally updated.
- Compare new behavior against the maintained parity tests whenever the migrated feature already has coverage.
- Record intentional behavior differences in docs and review notes.

## Testing Policy
- Unit tests cover pure naming, selection, parsing, and state logic.
- Integration tests cover tmux command orchestration, config loading, and state file interactions.
- End-to-end tests cover full session flows against real tmux behavior.
- When adding a feature, decide where it belongs in that stack and add or update the corresponding test entry.

## Communication
- Use concise progress updates.
- Report blockers early, especially if they involve parity uncertainty or overlap with another agent's files.
- When handing off, state the branch, checkout/worktree path, changed files, and remaining risks.
