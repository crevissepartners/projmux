# Agent Guide

Pull requests are accepted from maintainers only. Everyone else, please open an issue.

## Commands

Run these in order for every change. `## Workflow` explains the rules behind the order.

```sh
# start: confirm the checkout and its state
pwd
git status --short

# base check (on failure: rebase onto origin/main first)
git fetch origin main
git merge-base --is-ancestor origin/main HEAD

# Local checks
make fmt
make fix
make test

# publish: re-run the base check right before pushing
git fetch origin main
git merge-base --is-ancestor origin/main HEAD
git push -u origin <branch>            # after a rebase: git push --force-with-lease
gh pr create --title '<type>(<scope>): <summary>' --body-file <body.md>   # body: docs/pr-guideline.md

# CI checks on the same head: the five required checks (`Format`, `Unit Tests`, `NPM Packages`,
# `Integration Tests`, `E2E Tests`) and the aggregate `Test`. Integration and e2e are judged here, not run locally.
gh pr checks <num> --watch

# merge only when the Local checks passed and the CI checks are green on the same head
gh pr merge <num> --squash --delete-branch   # primary checkout; add --auto to queue it

# from a linked worktree, merge without --delete-branch (it checks out `main`, which another worktree holds)
# and delete the remote branch separately
gh pr merge <num> --squash
git push origin --delete <branch>

# after merge only (never before the merge and pull)
git pull --ff-only
make install
```

## Scope
- `projmux` is a standalone tmux session-management application.
- Keep portable session-management behavior in `projmux`.
- Keep machine-local policy outside the application.
- This file is the shareable, tool-agnostic repo contract. Personal agent recipes, reverse-engineering notes, and machine-local operating memos belong in your own untracked notes, not this tracked file.
- Local-only agent overlays may live in an untracked `AGENTS.local.md`.

## Repo map
| Path | Purpose |
| --- | --- |
| `cmd/projmux` | Binary entrypoint; CLI wiring only. |
| `internal/app` | Command implementations and app wiring. |
| `internal/cli` | Canonical command catalog, help, output, and receipts. |
| `pkg/projmux` | Experimental v0 public Go SDK; instructions and resource reads, public core types and typed refusals. |
| `internal/services` | Transport-independent execution shared by CLI and SDK. |
| `internal/core` | Product rules and state that are testable without tmux. |
| `internal/config` | Config files and saved settings. |
| `internal/integrations` | Adapters: tmux, AI agents, hooks, metadata, session state. |
| `internal/integrations/processhost` | Owned provider process lifetime, supervisors, bounded streams, and control admission. |
| `internal/integrations/agents/codexappserver` | Codex JSON-RPC framing, handshake, sessions, and turns. |
| `internal/ui` | Native picker and rendering. |
| `internal/state` | Simple file-backed state helpers. |
| `internal/i18n`, `internal/theme` | Message catalog and locales; built-in palette and theme resolution. |
| `internal/aiprovider` | AI provider registry: IDs, names, binaries, and capability flags. |
| `internal/diagnostics` | Bounded operational event journal read by `projmux diagnostics`. |
| `internal/platformkeys` | Physical key-chord capture for native keybindings; macOS only, a stub elsewhere. |
| `internal/systemstatus` | Host CPU and memory sampling for the status bar. |
| `internal/version` | Release version string that release-please bumps. |
| `internal/testutil` | Test-only support packages; product code never imports them. |
| `internal/tools/gendocs` | Build-time generator for `docs/cli.md` (`make docs`). |
| `internal/tools/gennotices` | Build-time generator for `THIRD_PARTY_NOTICES` (`make notices`). |
| `test/` | `integration/`, `e2e/`, and `install/` suites, Docker images, fixtures, and workflow contract tests. |
| `scripts/` | Development, CI, and security tooling only; no product logic. |
| `npm/` | npm launcher and per-platform packages. |
| `docs/` | User and contributor docs. |

See [docs/architecture.md](docs/architecture.md), [docs/repo-layout.md](docs/repo-layout.md), [docs/testing.md](docs/testing.md), [docs/registry.md](docs/registry.md), and [docs/cli.md](docs/cli.md).

## Workflow
- Use one branch per task, named `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, `refactor/<topic>`, or `chore/<topic>`.
- Use a dedicated checkout/worktree per task when parallel work would otherwise collide.
- Keep one agent per checkout/worktree. Do not share a dirty checkout across agents.
- If another agent owns a file, do not overwrite their changes. Adjust around them or coordinate a handoff.
- Keep changes narrow. Split docs, refactor, and feature work into separate branches unless they are inseparable.
- Make targets are the contract for local validation. Keep them stable and predictable.
- If a target is missing for the area you are changing, add it or leave the gap explicit in docs and review notes.
- Check ancestry before every first push or force-push. The repository-policy range scan rejects a PR base that is not an ancestor of its head, so publishing that state only produces a failed CI run.
- If `main` advanced before publishing, rebase and restart the Local checks for the new head.
- Do not skip `fmt` or `fix` because tests passed. Formatting, automatic fixes, and tests are separate gates.
- Publish as soon as the Local checks pass. Integration and e2e coverage comes from the CI checks `Integration Tests` and `E2E Tests` on the same head, not from local runs.
- If a Local check or a CI check fails, keep the merge blocked, fix the cause, and publish a new head the same way: Local checks, then new CI checks on that head.
- Any rebase invalidates earlier evidence from both Local checks and CI checks. Rerun the Local checks for the rebased head, publish it, and judge it by its own new CI checks.
- `make install` atomically replaces `$(go env GOPATH)/bin/projmux` and runs `projmux config apply`.
- Never run `make install` before the merge and `git pull --ff-only`. Pre-merge state has not cleared CI and may not match `main`.
- In a linked worktree, `go build` stamps the enclosing checkout's `vcs.revision` (Go only treats a `.git` directory as a repository root), so `make build` there stamps no revision and says why. Prove a build's provenance with the binary's sha256 and the merge commit, not `vcs.revision`; a worktree build without `vcs.revision` is expected.
- After merge, retire the merged checkout/worktree with your local tooling if you used one.

Behavior changes:
- Change behavior and restructure code in separate changes; when restructuring, match existing behavior first.
- Keep user-facing entrypoints (commands, flags, config keys) stable unless the change is intentional, and record intentional behavior differences in docs and the PR body.

Communication:
- Report blockers early, especially overlap with another agent's files.
- When handing off, state the branch, checkout/worktree path, changed files, and remaining risks.

## PR
- The default merge method is squash. The PR title becomes the squash subject that release-please parses, so write it as a Conventional Commit.
- release-please silently skips non-Conventional subjects.
- The PR body uses the six sections of [docs/pr-guideline.md](docs/pr-guideline.md), which holds the full conventions.
- Keep reviews small enough to reason about quickly.
- List the commands you ran, especially the `make` targets.
- Call out behavior changes separately from refactors.
- Flag unverified areas instead of implying coverage you did not run.

Branch protection:
- `main` is protected by the ruleset `main-protect`. Direct pushes are blocked, even for repository admins.
- Every change ships through a pull request. Admin bypass mode is `pull_request`: an admin can self-merge without approvals, but the PR is mandatory.
- The required checks are five CI job names: `Format`, `Unit Tests`, `NPM Packages`, `Integration Tests`, `E2E Tests`.
- The aggregate `Test` job is not required. It fans in every child, including security and Darwin jobs the ruleset does not require.
- Never rename or split a required job without keeping an aggregate under the old name. See [docs/pr-guideline.md](docs/pr-guideline.md#branch-protection-in-effect).

## Testing
- Unit tests cover pure naming, selection, parsing, and state logic.
- `make test-process-host-cli` builds this checkout and runs the process-host CLI fixture tests against a copy of that binary, with an isolated HOME and protocol fixtures instead of real providers. CI runs it as `Process Host CLI Tests` behind the aggregate `Test`; it is not one of the five required checks or a required Local check. Run it manually when you change process-host or provider-process code, the default agent guidance, or how Agent creation builds prompts and instructions: its projection fixtures pin the guidance digest a create records, which `make test` does not check. Details: [docs/testing.md](docs/testing.md).
- Integration tests cover tmux command orchestration, config loading, and state file interactions.
- End-to-end tests cover full session flows against real tmux behavior.
- When adding a feature, decide where it belongs in that stack and add or update the test there.
- Details: [docs/testing.md](docs/testing.md).

## Security
- Never commit credentials or tokens. gitleaks uses `.gitleaks.toml`.
- `.security/security-current-findings.json` is the only place a reviewed baseline digest is defined. Never restate a digest anywhere else; `make security-pin-contract` (part of `make test`) fails on a second copy.
- If you add or remove a Go package, run `make security-pin-refresh` and commit the resulting diff in the same change.
- Before changing anything under `.security/`, a scanner version, or a reviewed baseline, read [Security gates](docs/testing.md#security-gates) in `docs/testing.md`.

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
- Process host `post-create` hooks run only when declaring `runtime = "process"`. This opts the hook into process-hosted Agents as well; tmux execution remains unchanged. Other values or events are configuration errors. Eligible contexts add `PROJMUX_RUNTIME=process`, remove `PROJMUX_PANE` even from inherited/configured environments, remove inherited `TMUX` and `TMUX_PANE`, and supply present but empty `PROJMUX_SESSION` and `PROJMUX_SESSION_KIND`. `PROJMUX_CWD` is the effective Agent workspace; socket metadata and the 5s timeout retain their meaning. Process post-create failures are returned to the creator; tmux failures remain logged and ignored. Tmux hook scripts must guard process contexts before issuing commands with empty targets. Hook failure stops only its owned provider, persists actual Wait, and rolls back its Agent/Pane; incomplete rollback reports exact resource refs and cleanup commands.
- Process Agent lifecycle (`create agent --host process` with or without `--detach`, `agent stop`, `agent resume --detach`, `--wait-for-peer --detach`, `agent relaunch --detach`, and the `config agent-host` default for new CLI Agents) is public CLI contract, specified in [docs/process-host-operations.md](docs/process-host-operations.md#lifecycle-contract). Unsupported process combinations fail before effects with no tmux fallback.
- Details: [docs/hooks.md](docs/hooks.md#environment).

Configuration and environment:
- [docs/configuration.md](docs/configuration.md#environment-variables), [rare tunables](docs/configuration.md#rare-tunables), and [hook environment](docs/hooks.md#environment).

Release:
- release-please owns version bumps, `CHANGELOG.md`, and release notes. The squash subject is its input.
- Release candidates are cut only by manually dispatching `.github/workflows/release-rc.yml`. npm publishes cannot be recalled.
- Do not add `prerelease` or `prerelease-type` to `release-please-config.json`.
- The release archives and the npm platform packages ship `THIRD_PARTY_NOTICES`, the license notices of what the binary links. If you change a module the binary links or the toolchain in `go.mod`, run `make notices` and commit the diff; `make test` fails until you do and names the section that is missing, extra, or stale.
- Details: [docs/release.md](docs/release.md).
