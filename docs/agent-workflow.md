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
- `make npm-pack`: local npm binary package staging and `npm pack --dry-run` for the root package plus platform packages.
- `make test`: fast unit coverage for app-layer AI split native agent launch/selective popup-toggle/settings/status/notification parity including AI pane option metadata, watcher metadata bootstrap for existing panes, capture-backed reply detection, missing-pane watcher shutdown, armed focus-only reply badge clearing, busy attention preservation on focus clear, manual AI topic preservation while watcher status still updates, agent-labeled desktop notification message context with pane-title-first body text, global/project-local lifecycle hook dispatch for post-create/pre-create/pane-startup/post-attach/send-noti with project hook and `.projmux/config.toml` trust-store hashing plus env/settings kill-switch gating, declarative startup/hook run precedence, `send-noti` stdin JSON delivery plus `PROJMUX_NOTIFY_*` env payload, notify queue write success/failure/depth-guard dispatch rules, `pane-startup (deprecated)` labeling, project config env/kube session environment application, Settings project config env/kube/startup form writes with trust-store refresh and preserved hook commands, pane-startup command capture/send-keys orchestration plus startup pane replay markers, pre-create abort behavior, and shared projmux notification icon paths, and scoped even row/column resizing after shell and agent splits, status-bar git/kube segment parity including branch block styling, statusbar pwd display-only native-framed path popup, no clipboard or tmux buffer copy, and simple Enter-close popup command, statusbar usage native HUD popup alignment/threshold/sync-staleness/fallback coverage without raw CLI popup or popup-toggle stacking, statusbar settings click popup fallback, isolated `projmux shell` tmux app launch/config generation including home-project default session targeting, app-owned project-name statusbar layout, distinct project badge color, and quiet debounced session-state autosave command/app-config trigger, first-run/version-bump shell welcome state and inline update handling, shell startup update prompt actions for fresh installer-aware cached updates, pane/window keybindings, keymap.toml tmux override rendering/stale unbinds, Settings Keybindings root/list/detail capture flows including parse-error rows, unsafe raw capture and timeout guards, disable/reset writes, app config regeneration, live tmux source-file reload, and no-live-tmux save behavior, window rename bindings, pane rename helper/binding, pane-exit rebalance command/hooks, hook-pane and after-select-pane based attention focus hooks, attention badge toggle/clear/list/window rendering, attach/current/kill/pin/preview/prune/sessions/session-popup/settings commands, switch, tag, tmux helper commands, update status/check/apply cache and installer detection including GitHub Release binary asset selection/extraction/replacement, doctor install-missing command selection, and Settings About update status/check action wiring, untitled standalone popup-toggle marker close/config install, direct popup minimum sizing, AI picker minimum width and height, sidebar minimum width and compact badge spacing, preview select writes, popup render output after cycling, switch picker pin action behavior without inline settings rows, nested settings hub sections for AI defaults, project picker filesystem scan/pin actions, Project Root settings source/shadowing/set/current/clear flows, app/keybinding info including Ctrl-M rename forwarding, and About version/source rendering, switch picker focused-session kill, switch picker launcher-key abort bindings, switch explicit project-root, unconfigured-root, and weak managed-root heuristic parity, switch popup hiding new-session candidates while sidebar keeps create-capable rows, switch row project-name display with `~` pinned to the top and live-session-first sorting, pretty-path, preview-context including kube context/namespace, switch settings subcommand flows including add-current-pin, interactive add-pin picker, and settings label/preview polish, native preview wiring, baseline picker surface parity including prompt/footer/header fallback without app-name filler and search-key scoped card matching, sidebar compact action-only key footer, sidebar preview-window/start-position behavior without focus-time session switching, sidebar row/window ANSI styling with pane-aggregated attention badge state and AI topic labels, Alt+2/Alt+3 legacy popup row, preview-window, pane metadata, and pane-snapshot parity, switch read0 card rows with active/inactive title styling, right-side status badges, combined directory/git metadata with muted inactive branch styling, statusbar-matched block window tabs with window attention badges, read0 expect-key action parsing, restored pin/tag card badges, and restrained selected-row marker styling, switch preview metadata without duplicated directory/git rows, preview metadata rendering, popup pane display names for AI agents, AI topics, and shell commands, switch preview cycle bindings, sessions picker preview/cycle/open/kill wiring including attached-session fallback behavior, sessions picker launcher-key abort bindings, popup/switch preview summary formatting, popup sessions tmux entry helpers, switch/popup/session rendering, session identity, session-state Claude/Codex resume and declarative startup replay, session snapshot capture/autosave recipe classification, candidate discovery, config path derivation, popup preview read-models, and pure state rules including preview, tag, and lifecycle stores.
- `make test` also covers Settings IA regression guards for `send-noti` visibility in Hooks, no nested Project recipe inside Hooks, Project recipe/AI/Labs view-first detail rows, and Desktop notifications under Labs.
- `make test` also covers Settings > Session State view-first rows, auto-save/auto-restore toggles, current-session snapshot summary/delete behavior, autosave disabled gate, user-facing `projmux session-state` status/save/delete/restore dry-run actions including explicit manual save bypass of disabled autosave, and `projmux shell` auto-restore gating for enabled/disabled/missing-snapshot/existing-session/replay-failure paths including nested `TMUX` env stripping.
- `make test` also covers the statusbar Session State read-only popup entrypoint, native-framed snapshot status/preview rendering, missing-snapshot messaging, and `projmux tmux sessionstate-status` helper wiring.
- `make test` also now covers onboarding revisit and welcome wiring (`projmux welcome`, `Settings > About > Welcome`, `pending_attach_welcome`, attach-time `welcome --popup` one-time claim/env suppression/tmux popup payload, and generated `client-attached` app config wiring) via shell welcome tests.
- Current focused unit coverage also includes strict notify SOT behavior
  (TTL does not remove rows, focus success and target-gone clicks ack,
  reconcile reports stale rows), `notify list --live` queue/live explanations, notify sidebar
  two-line card rendering with age/project/window/pane metadata plus focus/ack/clear-all
  actions, and `focus` dispatch diagnostics for session fallback, unresolved
  targets, window fallback, pane fallback, explicit id failures as unresolved exits, and
  notify-only fallback.
- Picker focused unit coverage includes backend-neutral picker item/action mapping, native title-focused filtering, numeric selection, shared close actions including raw and CSI-u Ctrl-X native custom actions, deprecated picker backend value normalization, AI picker title chrome and stable search-key ordering, Settings title chrome and root section order, Settings Labs shell without backend choices plus keybinding diagnostic list/detail/probe outcome/init delegation coverage, environment override normalization, compact multi-line metadata gutters with one-column-indented metadata, proportional native scrollbar thumb rendering, multiline partial next/previous item row rendering with rendered-row scrollbar units, fixed split-preview and sidebar list viewports with scrollbar tracks, native up/down-family navigation wrap with empty-list safety and PageUp/PageDown/Home/End clamp regression coverage, native mouse down/follow-drag/release behavior, preview tab/control normalization before width clipping, optional native frame titlebars without same-line rule fill and with titlebar border reset guards, titled native Alt-1 sidebar chrome, statusbar-preserving sidebar popup height, native-only compact project sidebar popup sizing, and notify sidebar title/popup sizing.
- `make test-integration`: Docker-backed Linux integration smoke with real `tmux`, `git`, and `stty`; covers `doctor`, tmux config print/install/apply, and notify queue CRUD against isolated HOME/XDG paths.
- `make test-install-smoke`: Docker-backed source install smoke; covers `make install`, atomic binary replacement, `tmux apply` against a live `projmux` socket, and post-install notify queue initialization.
- `make test-e2e`: Docker-backed real-tmux workflow smoke for session/pane setup, app config sourcing, reply-state notify reconciliation, focus notify fallback, and status notify rendering with contextual project/state/agent badges. Host-only terminal, WSL, macOS, and GUI notification behavior remains outside Docker; see [docs/testing.md](testing.md).

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
