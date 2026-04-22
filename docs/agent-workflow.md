# Agent Workflow

## Parallel Work With `wt`
- Start with `wt --version` and `wt list`.
- Create the task path with `wt path --create <branch>`.
- Work only inside the returned path.
- Keep file ownership clear. If two agents need the same file, one should finish or hand off before the other edits it.
- Use `wt cleanup` or `wt prune` only after previewing what will be removed.

## Branch Naming
- `feat/<topic>` for new behavior.
- `fix/<topic>` for bug fixes.
- `docs/<topic>` for documentation-only changes.
- `refactor/<topic>` for structure changes without intended behavior changes.
- `chore/<topic>` for maintenance or tooling.

## Standard Development Loop
1. Sync the branch context and inspect `git status --short`.
2. Implement the smallest coherent change.
3. Run `make fmt`.
4. Run `make fix`.
5. Run `make test`.
6. Run `make test-integration`.
7. Run `make test-e2e`.
8. Update the maintained test list below if behavior or coverage expectations changed.
9. Prepare review notes with parity status, commands run, and remaining risks.

## Maintained Test List
- `make fmt`: repository formatting for Go, shell snippets, and generated docs where applicable.
- `make fix`: safe automatic fixes such as `go fix` and repository-approved cleanup steps.
- `make test`: fast unit coverage for app-layer attach/current/kill/pin/preview/prune/sessions/session-popup commands, switch, tag, and tmux helper commands, preview select writes, popup render output after cycling, switch picker tag actions and loop behavior, switch/popup/session rendering, session identity, candidate discovery, config path derivation, popup preview read-models, and pure state rules including preview, tag, and lifecycle stores.
- `make test-integration`: tmux-facing integration coverage, preview inventory parsing, lifecycle session inventory parsing, and state/config IO coverage including default tag path wiring.
- `make test-e2e`: real workflow coverage for session creation, switching, preview cycling/selection, popup preview, tag flows, attach/prune flows, and cleanup.

## When To Update This List
- A feature moves between unit, integration, and e2e coverage levels.
- A new subsystem introduces a new validation target or removes one.
- Behavior changes require new parity assertions or a different e2e scenario.
- A target stops being authoritative and must be replaced.

## Review Checklist
- The branch stays within its stated scope.
- The change preserves migration boundaries between `projmux` core and `dotfiles` adapters.
- The required `make` targets were run in order.
- Test inventory updates are included when behavior changed.
- Known parity gaps are explicit.
