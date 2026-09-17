# Agent Guide

## Commands

Run these in order for every change. `## Workflow` explains the rules behind the order.

```sh
# start: confirm the checkout and its state
pwd
git status --short

# base check (on failure: rebase onto origin/main first)
git fetch origin main
git merge-base --is-ancestor origin/main HEAD

# fast local gates
make fmt
make fix
make test

# publish: re-run the base check right before pushing
git fetch origin main
git merge-base --is-ancestor origin/main HEAD
git push -u origin <branch>            # after a rebase: git push --force-with-lease
gh pr create --title '<type>(<scope>): <summary>' --body-file <body.md>   # body: docs/pr-guideline.md

# long local gates, while CI runs on that head
make test-integration
make test-e2e

# merge only when the full local sequence, the required checks, and the aggregate `Test` check are green
gh pr checks <num> --watch
gh pr merge <num> --squash --delete-branch   # add --auto to queue it

# after merge only (never before the merge and pull)
git pull --ff-only
make install
```

## Scope
- `projmux` is a standalone tmux session-management application.
- Keep portable session-management behavior in `projmux`.
- Keep machine-local policy outside the application unless the migration plan explicitly calls for it.
- This file is the shareable, tool-agnostic repo contract. Personal agent recipes, reverse-engineering notes, and machine-local operating memos belong in your own untracked notes, not this tracked file.
- Local-only agent overlays may live in an untracked `AGENTS.local.md`.

## Repo map
| Path | Purpose |
| --- | --- |
| `cmd/projmux` | Binary entrypoint; CLI wiring only. |
| `internal/app` | Command implementations and app wiring. |
| `internal/cli` | Canonical command catalog, help, output, and receipts. |
| `internal/core` | Product rules and state that are testable without tmux. |
| `internal/config` | Config files and saved settings. |
| `internal/integrations` | Adapters: tmux, AI agents, hooks, metadata, session state. |
| `internal/ui` | Native picker and rendering. |
| `internal/state` | Simple file-backed state helpers. |
| `internal/i18n`, `internal/theme` | Message catalog and locales; built-in palette and theme resolution. |
| `internal/tools/gendocs` | Build-time generator for `docs/cli.md` (`make docs`). |
| `test/` | `integration/`, `e2e/`, and `install/` suites, Docker images, fixtures, and workflow contract tests. |
| `scripts/` | Development, CI, and security tooling only; no product logic. |
| `npm/` | npm launcher and per-platform packages. |
| `docs/` | User and contributor docs. |

See [docs/architecture.md](docs/architecture.md), [docs/repo-layout.md](docs/repo-layout.md), [docs/testing.md](docs/testing.md), and [docs/cli.md](docs/cli.md).

## Workflow
- Use one branch per task, named `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, `refactor/<topic>`, or `chore/<topic>`.
- Use a dedicated checkout/worktree per task when parallel work would otherwise collide.
- Keep one agent per checkout/worktree. Do not share a dirty checkout across agents.
- If another agent owns a file, do not overwrite their changes. Adjust around them or coordinate a handoff.
- Keep changes narrow. Split docs, bootstrap, migration, and feature work into separate branches unless they are inseparable.
- Make targets are the contract for local validation. Keep them stable and predictable.
- If a target is missing for the area you are changing, add it or leave the gap explicit in docs and review notes.
- Check ancestry before every first push or force-push. The repository-policy range scan rejects a PR base that is not an ancestor of its head, so publishing that state only produces a failed CI run.
- If `main` advanced before publishing, rebase and restart the fast local gates for the new head.
- Do not skip `fmt` or `fix` because tests passed. Formatting, automatic fixes, and tests are separate gates.
- Publish as soon as the fast gates pass. Do not serialize remote CI behind the long local gates.
- If a local or remote gate fails, keep the merge blocked, fix the cause, and publish a new validated head the same way.
- Any rebase invalidates earlier local gate evidence. Rerun the full local sequence for the rebased head, and start its CI after the fast gates.
- `make install` atomically replaces `$(go env GOPATH)/bin/projmux` and runs `projmux config apply`.
- Never run `make install` before the merge and `git pull --ff-only`. Pre-merge state has not cleared CI and may not match `main`.
- After merge, retire the merged checkout/worktree with your local tooling if you used one.

Migration discipline:
- Port one stable slice at a time. Do not mix bootstrap, feature redesign, and parity fixes in one change without a strong reason.
- Match existing behavior first, then simplify or redesign in a later change.
- When replacing shell logic with Go, keep user-facing entrypoints stable until the adapter layer is intentionally updated.
- Compare new behavior against the maintained parity tests when the migrated feature already has coverage.
- Record intentional behavior differences in docs and review notes.

Communication:
- Use concise progress updates.
- Report blockers early, especially parity uncertainty or overlap with another agent's files.
- When handing off, state the branch, checkout/worktree path, changed files, and remaining risks.

## PR
- The default merge method is squash. The PR title becomes the squash subject that release-please parses, so write it as a Conventional Commit.
- release-please silently skips non-Conventional subjects.
- The PR body uses the six sections of [docs/pr-guideline.md](docs/pr-guideline.md), which holds the full conventions.
- Keep reviews small enough to reason about quickly.
- List the commands you ran, especially the `make` targets and any parity checks.
- Call out behavior changes separately from refactors.
- Flag unverified areas instead of implying coverage you did not run.
- If migration parity is incomplete, state the exact gap and the follow-up branch or issue.

Branch protection:
- `main` is protected by the ruleset `main-protect`. Direct pushes are blocked, even for repository admins.
- Every change ships through a pull request. Admin bypass mode is `pull_request`: an admin can self-merge without approvals, but the PR is mandatory.
- The required checks are five CI job names: `Format`, `Unit Tests`, `NPM Packages`, `Integration Tests`, `E2E Tests`.
- The aggregate `Test` job is not required. It fans in every child, including security and Darwin jobs the ruleset does not require.
- Never rename or split a required job without keeping an aggregate under the old name. See [docs/pr-guideline.md](docs/pr-guideline.md#branch-protection-in-effect).

## Testing
- Unit tests cover pure naming, selection, parsing, and state logic.
- Integration tests cover tmux command orchestration, config loading, and state file interactions.
- End-to-end tests cover full session flows against real tmux behavior.
- When adding a feature, decide where it belongs in that stack and add or update the test there.
- Details: [docs/testing.md](docs/testing.md).

## Security
- `make security` runs three groups in parallel; CI runs each as its own job: `make security-go` (govulncheck, gosec), `make security-static` (staticcheck), `make security-policy` (gitleaks, actionlint, shellcheck).
- `make security-tools` installs the Go-based scanners at the versions pinned in `.security/security-tools.versions`.
- gosec and staticcheck findings are compared with the reviewed baselines `.security/gosec-baseline.json` and `.security/staticcheck-baseline.json`. A finding beyond its baseline count fails the gate.
- `.security/security-current-findings.json` pins the current finding counts and the baseline hashes. A change to either must update it.
- gitleaks uses `.gitleaks.toml`. Never commit credentials or tokens.

## Compatibility
Platforms:
- Supported build targets are Linux and macOS on amd64 and arm64. That matrix is the whole contract.
- The release workflow builds only those four, and no CI job builds any other `GOOS`.
- `GOOS=windows` compilation is not supported or guaranteed.
- The repository has no `_windows.go` files and no `//go:build windows` or `//go:build !windows` constraints. Do not add them.
- WSL is not a separate target. It runs the Linux build under the Linux contract, and WSL-specific behavior such as `PROJMUX_WSL_TOAST_ICON_DIR` stays inside that build.

Hook contract:
- The post-create hook contract (`[hooks.post-create]`, `PROJMUX_*` env vars, 5s timeout) is public API.
- Adding, removing, or renaming any `PROJMUX_*` env var needs at least a minor release input: use a `feat(hooks): ...` PR title and leave the version/manifest update to release-please.
- `PROJMUX_SOCKET` is the app socket name (`projmux`), supplied as hook routing metadata. It does not change how the tmux client invokes commands.
- `PROJMUX_PANE` is the exact first pane id from standard persistent/ephemeral creation for `post-create`. It is intentionally absent from `pre-create`, which runs before that pane exists.
- Details: [docs/hooks.md](docs/hooks.md#environment).

Configuration and environment:
- [docs/configuration.md](docs/configuration.md#environment-variables), [rare tunables](docs/configuration.md#rare-tunables), and [hook environment](docs/hooks.md#environment).

Release:
- release-please owns version bumps, `CHANGELOG.md`, and release notes. The squash subject is its input.
- Release candidates are cut only by manually dispatching `.github/workflows/release-rc.yml`. npm publishes cannot be recalled.
- Do not add `prerelease` or `prerelease-type` to `release-please-config.json`.
- Details: [docs/release.md](docs/release.md).
