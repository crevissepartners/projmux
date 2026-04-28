# Agent Guide

## Scope
- `projmux` is a standalone tmux session-management application.
- Keep portable session-management behavior in `projmux`.
- Keep machine-local policy outside the application unless the migration plan explicitly calls for it.

## Startup Checks
- Run `wt --version`.
- Run `wt list`.
- Confirm you are in the intended worktree with `pwd`.
- Check local state with `git status --short`.

## Branch And Worktree Rules
- Use one branch per task. Preferred names: `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, `refactor/<topic>`, `chore/<topic>`.
- Create or reuse the task worktree with `wt path --create <branch>`.
- Keep one agent per worktree. Do not share a dirty worktree across agents.
- If another agent owns a file, do not overwrite their changes. Adjust around them or coordinate a handoff.
- Keep changes narrow. Split docs, bootstrap, migration, and feature work into separate branches unless they are inseparable.

## Standard Dev Flow
- Make targets are the contract for local validation. Keep them stable and predictable.
- Run work in this order before review:
  1. `make fmt`
  2. `make fix`
  3. `make test`
  4. `make test-integration`
  5. `make test-e2e`
- `make install` is the deploy step (build + atomic replace of `$(go env GOPATH)/bin/projmux` + `projmux tmux apply`). Do not run it during routine validation; reserve it for after review when promoting a build.
- If a target is missing for the area you are changing, add it or leave the repository in a state where the gap is explicit in docs and review notes.
- If behavior changes, update the maintained test list in [docs/agent-workflow.md](/home/es5h/source/repos/projmux/docs/agent-workflow.md) in the same branch.
- Do not skip `fmt` or `fix` because tests passed. Formatting, automatic fixes, and test execution are separate gates.

## Configuration And Environment
- `PROJDIR` (preferred) and `RP` (legacy) supply the sessionizer repo root. `~/.config/projmux/projdir` memoizes whichever value was last seen.
- `PROJMUX_MANAGED_ROOTS` / `TMUX_SESSIONIZER_ROOTS` provide colon-separated search-root lists. They take priority over the saved file and the default heuristics.
- `~/.config/projmux/workdirs` stores the cumulative workdirs list managed via the Settings UX. It is read only when no env list is set.
- `tmux set-option -g @projmux_projdir <path>` is a declarative source for `PROJDIR` that the switch command reads through `tmuxProjdirOption`.

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
- When handing off, state the branch, worktree path, changed files, and remaining risks.
