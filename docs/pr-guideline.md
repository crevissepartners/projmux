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
refactor(picker): simplify native picker bootstrap code
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

Use this template. Keep all six sections in this order.
[`.github/pull_request_template.md`](../.github/pull_request_template.md)
pre-fills new PRs with the same template; keep the two in sync when either
changes.

```markdown
## Summary
- What changed, in 1–3 bullets.

## Background
- Why this is needed: the problem, a reproduction, related issues or PRs.

## Changes
- Behavior and code changes, grouped by area.
- Breaking: what breaks and how to migrate (if any).

## Scope
- In scope:
- Out of scope (and follow-ups):

## Verification
- [ ] Fast local gates: `make fmt` → `make fix` → `make test`
- [ ] Long local gates: `make test-integration` → `make test-e2e`
- [ ] Required CI checks green
- [ ] Manual steps (if relevant):
- Globalization (check exactly one):
  - [ ] No user-facing string changes.
  - [ ] User-facing strings are behind `internal/i18n` catalog keys with tests.
  - [ ] Non-translated strings are classified as literal/data/debug-only.

## Measurements
- (If relevant) before/after numbers, method, environment, run ids.
  Mark each number as observed or inferred.
```

Per-section rules:

- **Summary** — the short version a reviewer reads first. The diff already
  shows the code; say what changed in terms of behavior.
- **Background** — **why** matters more than **what**. State the problem, how
  to reproduce it, and related issues or PRs. Reference issues with
  `Closes #<n>` so they auto-close on merge.
- **Changes** — group by area rather than by file. Any breaking change must be
  listed here with a migration note, and the title must also carry `!` or the
  body a `BREAKING CHANGE:` footer (see the title rules above).
- **Scope** — say what is deliberately left out and name the follow-ups
  (issue or PR) instead of leaving deferred work implicit.
- **Verification** — list the gates in the order
  [AGENTS.md](../AGENTS.md) runs them: fast local gates, then the long local
  gates (which may still be running while CI runs on the published head), then
  the required CI checks, then any manual steps. For any new or changed
  user-facing text, check exactly one Globalization item. Normal UX copy needs
  a catalog key and test coverage. Commands, paths, config keys, env vars,
  provider payloads, locale enum values, product names, debug logs, and
  internal diagnostics may stay out of the catalog only when explicitly
  classified in the PR body.
- **Measurements** — fill it when the PR claims a change in speed, size, or
  resource use (for example a `perf` PR). Give the method, environment, and
  run ids so the numbers can be reproduced, and mark which values were
  observed and which inferred.

For small PRs, **Background** and **Measurements** may be written as `N/A`
with a one-line reason. Do not delete them, so the section order stays the
same across PRs.

## Branch protection in effect

`main` is governed by ruleset `main-protect`:

- Direct push to `main` is blocked. Even repository admin must use a PR.
- Required status checks are the five CI job names `Format`, `Unit Tests`,
  `NPM Packages`, `Integration Tests`, and `E2E Tests`. The aggregate `Test`
  job is observed as the project-wide fan-in, but it is not ruleset-required.
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
