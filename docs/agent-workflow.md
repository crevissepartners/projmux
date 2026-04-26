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
- `make test`: fast unit coverage for app-layer AI split native agent launch/selective popup-toggle/settings/status/notification parity including AI pane option metadata, watcher metadata bootstrap for existing panes, capture-backed reply detection, missing-pane watcher shutdown, armed focus-only reply badge clearing, busy attention preservation on focus clear, manual AI topic preservation while watcher status still updates, agent-labeled desktop notification message context with pane-title-first body text, hook-provider dispatch, and agent-specific notification icons, and scoped even row/column resizing after shell and agent splits, status-bar git/kube segment parity, isolated `projmux shell` tmux app launch/config generation including app-owned project-name statusbar layout, pane/window keybindings, window rename bindings, pane rename helper/binding, pane-exit rebalance command/hooks, hook-pane and after-select-pane based attention focus hooks, attention badge toggle/clear/window rendering, attach/current/kill/pin/preview/prune/sessions/session-popup/settings commands, switch, tag, and tmux helper commands, untitled standalone popup-toggle marker close/config install, direct popup minimum sizing, AI picker minimum width and height, sidebar minimum width and compact badge spacing, preview select writes, popup render output after cycling, switch picker pin action behavior without inline settings rows, nested settings hub sections for AI defaults, project picker filesystem scan/pin actions, app/keybinding info including Ctrl-M rename forwarding, and About version/source rendering, switch picker focused-session kill, switch picker launcher-key abort bindings, switch default-root and inferred `~/source/repos` repo-root parity, switch popup hiding new-session candidates while sidebar keeps create-capable rows, switch row project-name display with `~` pinned to the top and live-session-first sorting, switch/session hidden search text for picker rows, pretty-path, preview-context including kube context/namespace, switch settings subcommand flows including add-current-pin, interactive add-pin picker, and settings label/preview polish, fzf preview wiring, fzf baseline surface parity including prompt/footer/header fallback, hidden value/search fields, and `[projmux]` footer labeling, sidebar preview-window/start-position behavior without focus-time session switching, sidebar row/window ANSI styling with pane-aggregated attention badge state and AI topic labels, Alt+2/Alt+3 legacy popup row, preview-window, pane metadata, and pane-snapshot parity, switch preview git metadata rendering, preview metadata rendering, popup pane display names for AI agents, AI topics, and shell commands, switch preview cycle bindings, sessions picker preview/cycle/open/kill wiring including attached-session fallback behavior, sessions picker launcher-key abort bindings, popup/switch preview summary formatting, popup sessions tmux entry helpers, switch/popup/session rendering, session identity, candidate discovery, config path derivation, popup preview read-models, and pure state rules including preview, tag, and lifecycle stores.
- `make test-integration`: tmux-facing integration coverage, preview inventory parsing, lifecycle session inventory parsing, and state/config IO coverage including default tag path wiring.
- `make test-e2e`: real workflow coverage for session creation, switching, preview cycling/selection, popup preview, tag flows, attach/prune flows, and cleanup.

## When To Update This List
- A feature moves between unit, integration, and e2e coverage levels.
- A new subsystem introduces a new validation target or removes one.
- Behavior changes require new parity assertions or a different e2e scenario.
- A target stops being authoritative and must be replaced.

## Review Checklist
- The branch stays within its stated scope.
- The change preserves boundaries between portable `projmux` behavior and local machine policy.
- The required `make` targets were run in order.
- Test inventory updates are included when behavior changed.
- Known parity gaps are explicit.
