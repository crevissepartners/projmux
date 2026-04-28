# PR Guideline

Audience: every contributor — humans and agents alike. Agents working in this
repo (`claude` / `codex` panes, the team-lead session, etc.) MUST follow these
rules; the conventions here are what `release-please` parses for the next
release notes, so a sloppy PR title silently breaks the changelog.

For the surrounding workflow (worktree, validation gates, post-merge install)
see [AGENTS.md](../AGENTS.md). This document covers only the PR itself.

## PR title — Conventional Commits

Default merge method is **squash**, so the PR title becomes the only commit
subject that lands on `main`. Format:

```
<type>(<optional scope>): <imperative summary>
```

Examples:

```
feat(ai): add codex split picker keybinding
fix(ai): prepend agent bin dir to PATH so node-managed CLIs find node
docs(readme): drop Releases and Configuration sections
chore: bump release-please manifest to 0.3.0
refactor(picker): collapse duplicate fzf bootstrap code
```

Rules:

- Subject is in the imperative ("add", "fix", "drop"), no trailing period.
- Keep the title under ~70 characters when possible. Long detail goes in the
  body.
- The scope is optional but recommended for non-trivial diffs (`ai`, `picker`,
  `tmux`, `readme`, `ci`, etc.).
- A `!` after the type or scope marks a breaking change:
  `feat(ai)!: rename PROJMUX_NOTIFY_HOOK to PROJMUX_NOTIFY_BIN`.
- Or include a `BREAKING CHANGE: <description>` footer in the body. Either form
  bumps the major version on the next release-please run.

### Allowed types

| type | use for | release impact |
| --- | --- | --- |
| `feat` | user-visible new behavior or capability | minor bump |
| `fix` | bug fix that ships to users | patch bump |
| `perf` | measurable runtime/memory improvement | patch bump |
| `refactor` | code restructure with no user-visible change | none |
| `docs` | docs-only change | none |
| `test` | adding or restructuring tests | none |
| `build` | build system, Makefile, dependencies | none |
| `ci` | CI workflow / GitHub Actions | none |
| `chore` | release plumbing, tooling, repo housekeeping | none |
| `style` | formatting only, no logic change | none |

If the change includes both a feat and a fix, split it into two PRs. release-please
classifies the whole PR by its title type, not by content.

## PR body

Use this template:

```markdown
## Summary
- 1–3 bullets describing what changed and why.

## Test plan
- [ ] make fmt-check
- [ ] make test
- [ ] manual verification step (if relevant)
```

Notes:

- **Why** matters more than **what**. Diff already shows the what.
- Reference issues with `Closes #<n>` so they auto-close on merge.
- Mention follow-ups explicitly when scope was deliberately deferred.

## Branch protection in effect

`main` is governed by ruleset `main-protect`:

- Direct push to `main` is blocked. Even repository admin must use a PR.
- Required status check: the CI `Test` job. The PR cannot merge until it is
  green.
- Admin bypass is `pull_request` mode — admin can self-merge without
  approvals, but the PR itself is mandatory.
- Linear history is enforced. The merge methods exposed are
  `merge` / `squash` / `rebase`; **default is squash** and that is what the
  team-lead session uses unless the change explicitly needs preserved history.
- Force pushes and branch deletions on `main` are blocked.

`gh pr merge <num> --squash --delete-branch` is the canonical merge command.
Use `--auto` if you want the merge queued automatically once CI passes.

## Release-please coupling

Every PR title that lands on `main` is parsed by `release-please-action`.
A `feat:` or `fix:` PR adds an entry to the next release notes; `chore:` /
`docs:` / `refactor:` etc. do not. To force a release of accumulated non-user
changes, open a `chore` PR titled `chore: release X.Y.Z` (or wait for any
real change). The `internal/version/version.go` constant carries the
`x-release-please-version` marker so release-please bumps it automatically.

Do not hand-author CHANGELOG.md or version bumps. release-please owns both.
