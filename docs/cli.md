# CLI Reference

Every subcommand exposed by the `projmux` binary as of v0.4.0. Run
`projmux help` for the live top-level list, or `projmux <cmd> --help` /
`projmux <cmd> help` for the per-command usage string.

Exit codes:

- `0` — success.
- `1` — runtime failure.
- `2` — usage error (unknown flag, bad enum, missing required flag) or a
  deterministic semantic exit (e.g. `focus` cannot resolve the target).

## Top-level

```
projmux <command> [args...]
```

| Command | Purpose |
| --- | --- |
| `ai` | Manage tmux AI splits (Codex/Claude) and per-pane status. |
| `attention` | View and manage live tmux pane attention state. |
| `attach` | Open tmux lifecycle entry helpers. |
| `current` | Resolve the active tmux pane path. |
| `doctor` | Diagnose runtime dependencies. |
| `focus` | Switch the active client to a session/window/pane target. |
| `init` | Apply supported terminal keybinding fallbacks. |
| `kill` | Terminate tagged tmux sessions. |
| `notify` | Manage the pending AI notify queue (push/list/ack/reconcile). |
| `pin` | Manage pinned project directories. |
| `preview` | Manage persisted tmux preview selection. |
| `prune` | Trim stale tmux lifecycle state. |
| `sessions` | Pick and open an existing tmux session. |
| `session-popup` | Read tmux popup preview state. |
| `settings` | Configure projmux. |
| `setup` | Probe terminal key delivery for projmux bindings. |
| `shell` | Open the isolated projmux tmux app. |
| `status` | Render tmux status bar segments. |
| `statusbar` | Dispatch projmux status bar clicks and shortcuts. |
| `switch` | Pick and open a project tmux session. |
| `tag` | Manage tagged tmux sessions. |
| `tmux` | Open tmux popup entry helpers / install generated config. |
| `update` | Check installer-aware GitHub release update status. |
| `upgrade` | Self-update via `go install`. |
| `usage` | Report AI token usage across 5h and weekly windows. |
| `version` | Print the current version. |

## switch

```
projmux switch [path]
projmux switch toggle-tag | toggle-pin | kill | settings | preview
projmux switch cycle-pane | cycle-window | sidebar-focus
```

Project picker. With no positional argument, opens the fzf-driven popup or
sidebar (depending on entry helper). With a path, jumps directly. The
sub-verbs are entry hooks invoked by tmux keybindings (e.g.
`sidebar-focus` is wired to the sidebar's focus binding so navigation keeps
the active session in sync).

`PROJMUX_PICKER_BACKEND=native` opts the switcher into the experimental native
picker backend. fzf remains the default and the only backend with full preview
pane, sidebar focus, and key-action parity. Native currently provides
multiline card rendering, title-focused search, numeric selection, and the
shared close-action contract.

## setup

```
projmux setup [--timeout DURATION] [--non-interactive]
```

Probes which projmux key sequences (`Alt-1..5`, `Ctrl-N`, `Ctrl-Shift-{R,L,M}`,
`Ctrl-M`, `Alt-Shift-{Left,Right}`) reach this process and which the
terminal swallows. Reports `plain`, `csi-u`, `unknown`, or `timeout` for
each. `--non-interactive` skips the TTY probe and prints the expected key
map. Default `--timeout` is `5s`. Run it outside tmux after trying
`projmux shell`; keys reported as `plain` or `csi-u` already work with zero
terminal config.

## init

```
projmux init [terminal] [--apply | --dry-run] [--config <path>]
             [--allow-symlink]
```

Applies a terminal-specific fallback for shortcuts that `projmux setup`
reports as swallowed. When `terminal` is omitted, autodetects from
`$TERM_PROGRAM`/`$TERMINAL_EMULATOR`.
Known terminals: `ghostty`, `windows-terminal`. Default is dry-run; pass
`--apply` to write (timestamped `.bak.<timestamp>` is created). Refuses to
write through a symlink unless `--allow-symlink` is passed (dotfiles repos).
`--config <path>` overrides the candidate list when the adapter has more
than one default location (Ghostty `config` vs `config.ghostty`). If setup
shows every key arriving, skip init.

## doctor

```
projmux doctor [--json]
projmux doctor --install-missing [--dry-run] [--include-optional]
```

Runs a dependency check: `tmux ≥ 3.4`, `fzf ≥ 0.55`, `git`, `stty` (POSIX
only), and `kubectl` (optional). Exit code `0` even when optional deps are
missing; non-zero only when a required dep is missing or stale. `--json`
emits a machine-readable array; the default is the human report with
suggested install commands per platform. `--install-missing` is explicit
opt-in and runs generated install commands only for missing or stale required
dependencies. `--dry-run` prints those commands without executing them.
`--include-optional` also includes optional missing dependencies such as
`kubectl` when an install command is available. Install flags cannot be
combined with `--json`. Doctor does not diagnose terminal key delivery; use
`projmux setup` for that.

## focus

```
projmux focus --target SESSION[:WINDOW[.PANE]] [--socket <path>]
              [--source ai|status-bar|external|os-notification]
              [--kind reply-ready|busy-cleared|segment-click|custom]
              [--json]
```

Unified switch-client dispatch. Resolves the session against the live tmux
inventory (with prefix/fuzzy fallback), then redirects one suitable attached
client on the selected socket. It never force-detaches other clients. If no
client is attached on that socket, it emits the configured desktop
notification instead. `--socket` is explicit; when omitted, the socket is
derived from `$TMUX`.

Exit codes:

- `0` — focused (or the notify-only fallback fired).
- `2` — target session could not be resolved (`focusExitNotResolved`).

`--source`/`--kind` are telemetry labels logged when
`PROJMUX_FOCUS_DEBUG` is set. `--json` prints a single-line JSON payload
with `{ok, fallback, target, socket, resolved_session, client, dispatch,
session_state, window_state, pane_state, reason, note}`. Callers can
distinguish unresolved sessions (`reason=session-unresolved`, exit 2),
session rename/prefix fallback (`session_state=fallback`), window index
fallback (`window_state=index-fallback-session`), pane index fallback
(`pane_state=index-fallback-window`), and explicit id failures
(`window-id-unresolved` / `pane-id-unresolved`).

## notify

Pending AI notify queue. `attention` is live tmux pane state; `notify` is
the explicit-ack pending notification source of truth used by the status-bar
notify segment and notify sidebar. It is not the source of truth for all live pane attention. See
[notify-queue.md](notify-queue.md) for the full data model.

```
projmux notify push  --text <s> --target <SESSION[:WINDOW[.PANE]]>
                     [--socket <s>] [--severity info|warn|critical]
                     [--source ai|k8s|git|external] [--ttl <seconds>]
                     [--id <s>] [--json]
projmux notify list  [--live] [--json] [--limit N] [--ui table|sidebar]
                     [--severity ...] [--source ...]
projmux notify ack   <id> | --all
projmux notify reconcile [--json]
```

- `push` — append (or refresh, with `--id`) one entry. `--ttl` defaults to
  `600` seconds as freshness metadata; it does not remove rows from
  `notify list`. `--text` is hard-capped to 80 runes (longer text is
  truncated server-side).
- `list` — newest-first pending queue table `ID AGE SEV SRC TARGET TEXT`
  (or JSON). `--severity` and `--source` are repeatable filters.
  `--live` adds a non-mutating explanation table (or JSON report) that
  compares queued entries with live pane attention. It calls out manual
  reply badges that do not queue because no AI agent is attached, live AI
  reply panes missing a queue entry, matched AI reply entries, and stale
  queue entries whose live pane no longer matches. `--ui=sidebar` opens the
  compact interactive notify list where Enter focuses and acks a target, `a`
  acks the selected row, and `Ctrl-A` clears all. The sidebar row keeps target
  details searchable but displays only age, non-info severity, and text.
- `ack <id>` removes one entry; `--all` flushes the queue.
- `reconcile` — walks `tmux list-panes -a` and back-fills entries for
  panes whose attention state is `reply` AND whose AI agent option is
  set, reporting stale `ai:` entries that no longer match a live pane without
  acking them.
  Soft-fails (no error, populated `errors` field in the summary) when
  tmux is not running. Use this as the recovery path when the queue and
  live pane state drift.

## usage

Authoritative AI token usage. See [usage-tracking.md](usage-tracking.md)
for adapter detail.

```
projmux usage [--model codex|claude|all] [--window 5h|weekly|all]
              [--json] [--force|-f]
```

Renders a tab-aligned `MODEL WINDOW PCT RESETS_AT STALE` table; appends a
backoff note when an adapter is in 429 cooldown. `--force` clears any
active backoff and bypasses the per-adapter throttle floor (Claude `5m`,
Codex shares the global `30s`). `--json` emits the snapshot array; when
backoff is active the wrapper `{snapshots, backoff}` object is emitted
instead.

## status

Per-segment status-bar renderers. All four are silent on failure — the
tmux status interval polls them and must never produce a stack trace.

```
projmux status git    [path]
projmux status kube   [session]
projmux status usage  [--max-width N] [--force|-f]
projmux status notify [--max-width N]
```

- `git` — `#[bold,fg=colour16,bg=colour45] <branch> #[default]` for the
  pane's `pane_current_path` (or the supplied path). Empty when not in a
  repo.
- `kube` — `⎈ <context>/<namespace>` segment. Reads
  `~/.cache/tmux/kube-segment-<session>.txt` first (TTL governed by
  `TMUX_KUBE_CACHE_TTL`, default `5s`). Picks up a per-session
  `KUBECONFIG` from `${XDG_RUNTIME_DIR:-~/.cache}/kube-sessions/<session>.yaml`.
- `usage` — HUD-style `Claude (Nm) 5h [bar] N% · weekly [bar] N%   Codex 5h
  [bar] N% · weekly [bar] N%`. Degrades through six tiers as `--max-width`
  shrinks. Triggers an opportunistic, throttled refresh (per-adapter
  throttle, `30s` floor) so a stale cache self-heals.
- `notify` — newest-first HUD pill `[ <agent|SEV> ] <text> · <target> ·
  <age>   +<extras>`. Degrades through six tiers; default `--max-width`
  is `200` runes.

## statusbar

```
projmux statusbar click <range-id> [--socket <s>] [--mouse-window <id>]
                                   [--mouse-x N] [--mouse-y N]
```

Click/keyboard dispatcher for the two-line status bar. Implemented range ids:
`session pwd kube git usage notify`. The bare `window` /
`window|<idx>` token (tmux's built-in window-list range) and the empty
range fall through to `select-window -t @<mouse_window>` so the native
click-to-switch tab affordance is preserved on row 0. Unknown range ids are
non-specialized placeholders and no-op. `session` opens the existing-session
popup; `pwd` copies the current pane path to the tmux paste buffer and shows
it in a compact popup; `kube` and `git` open the project switcher popup;
`usage` opens the detailed `projmux usage` table popup; `notify` focuses the
newest actionable queue target without acking it.
`MouseDown1Status` errors are
swallowed and surfaced as `display-message` toasts so a transient
failure does not raise a tmux error popup. See [statusbar.md](statusbar.md).

## attention

```
projmux attention toggle [pane]
projmux attention clear  [pane]
projmux attention arm    [pane]
projmux attention list   [--json] [--all]
projmux attention window [window]
```

Toggles the `✳` pane title prefix and the `@projmux_attention_state` pane
option. `toggle` flips between cleared and `reply`; `clear` always
clears; `arm` sets a pre-reply armed state used by the AI flow. The
producer side pushes the matching entry into the notify queue when the pane
has an associated AI agent option; clearing attention does not ack the queue
row (manual toggles on shell panes do not push). `list` reads `tmux list-panes -a` and shows live pane
attention state without reading or mutating the notify queue; by default
it shows panes with an attention option or title marker, and `--all`
includes every pane. `window` renders the status-bar window badge for the
supplied window.

## ai

```
projmux ai split    --inside <right|down> [--agent <name>] ...
projmux ai picker   --inside <right|down>
projmux ai settings
projmux ai status   set <thinking|waiting|idle> [--pane <id>]
projmux ai notify   <reset|notify> [--pane <id>]
projmux ai watch-title [--pane <id>]
projmux ai topic     ...
```

Manages the AI split lifecycle and the per-pane state machine that drives
the `attention` badge, the `notify` queue producer, and the desktop
notifier. `status set waiting` is the trigger that flips a pane to the
reply-ready state — that transition pushes an `ai:<session>:<pane>`
entry into the notify queue.

## tmux

```
projmux tmux popup-toggle <mode>
projmux tmux popup-switch
projmux tmux popup-sessions
projmux tmux popup-preview <session>
projmux tmux rebalance-panes
projmux tmux rename-pane <pane> <title>
projmux tmux print-config     [--bin <path>]
projmux tmux print-app-config [--bin <path>]
projmux tmux install     [--bin <path>] [--config <path>] [--include <path>]
projmux tmux install-app [--bin <path>] [--config <path>]
projmux tmux apply
```

Helpers tmux's keybindings and the install pipeline call into. Modes
accepted by `popup-toggle` mirror the historical sessionizer surface:
`session-popup`, `sessionizer`, `sessionizer-sidebar`,
`ai-split-picker-right`, `ai-split-picker-down`, `ai-split-settings`.
`apply` reloads the live `-L projmux` server's config without restarting
it; `make install` and `projmux upgrade` invoke it after replacing the
binary.

## update

```
projmux update status [--json]
projmux update check  [--json]
projmux update apply  [--dry-run] [--no-apply]
```

Installer-aware update status foundation. `status` is read-only: it
prints the current version, cached latest GitHub Release tag when present,
cache freshness (`fresh`, `stale`, or `unknown`), update state, detected
installer source, and cache path. It never reaches the network, so it is
safe for interactive use and shell startup paths.

`check` fetches the latest GitHub Release metadata for
`crevissepartners/projmux`, atomically writes
`${XDG_CACHE_HOME:-~/.cache}/projmux/update.json`, then prints the concise
latest/update/cache result. `--json` emits the same machine-readable
status shape for both subcommands.

`projmux shell` reads the same cache before opening the isolated tmux app.
When the cache is fresh, an update is available, and the installer supports
`update apply`, shell startup shows a small picker with Update Now, Later,
and Skip This Version actions. This startup prompt never reaches the network;
run `projmux update check` first when you want it to see the newest release.

Installer detection honors
`PROJMUX_INSTALLER=npm|go|github-release|source`. When unset or invalid,
the source is reported as `unknown` with guidance to set the variable.
`apply` is installer-aware and only runs after explicit user selection.
For npm installs, it runs `npm update -g projmux` and then
`projmux tmux apply` unless `--no-apply` is set. For Go installs, it
delegates to the existing atomic `projmux upgrade` flow. For
`github-release` installs, it downloads the latest matching
`projmux_<version>_<goos>_<goarch>.tar.gz` release asset, extracts the
binary, atomically replaces the current executable, then applies tmux
configuration unless `--no-apply` is set. `source` installs report an
actionable error because they must be updated from the source checkout.

## upgrade

```
projmux upgrade [--ref @latest|@<tag>|@<branch>]
                [--target <path>] [--no-apply] [--dry-run]
```

`go install`s the binary, atomically replaces the on-disk file, then
runs `projmux tmux apply` (skipped with `--no-apply`). Reads
`PROJMUX_PROJDIR` from the calling shell and memoizes the primary entry
to `~/.config/projmux/projdir`. npm-installed binaries reject this
command; use `projmux update apply` or `npm update -g projmux` for npm
installs.

## sessions / session-popup / preview / pin / kill / prune / tag

The lifecycle helpers retained from earlier releases. They share their
flags with the top-level `switch` UX:

- `sessions` — list/pick existing tmux sessions, supports `--ui=popup`.
- `session-popup` — read/write the popup-marker state used during
  preview cycling.
- `preview` — manage the persisted window/pane preview selection.
- `pin add|remove|toggle|list|clear` — pin set CRUD, persisted under
  `~/.config/projmux/pins`.
- `kill <session>` / `kill tagged` — terminate sessions; `tagged`
  consumes the active tagged-selection set.
- `prune ephemeral` — drop ephemeral sessions older than the configured
  retention window.
- `tag` — manage the tagged-selection set.

## current / shell / attach / settings

- `current` — print `pane_current_path` for the active tmux pane (used
  by the shell jump binding).
- `shell` — boot the isolated `-L projmux` tmux server with the
  generated config. The generated app config uses absolute `$SHELL` as the
  tmux default shell when set, otherwise `/bin/sh`.
- `attach auto [--keep=N] [--fallback=home|ephemeral]` — auto-attach to
  the most recent session, with bounded retention and a fallback policy.
- `settings` — interactive configuration UI for the project picker, AI
  splits, Project Root management, the switcher's saved workdirs list, and
  About/Update status. In Project Picker, `Project Root` manages the saved
  primary root (`~/.config/projmux/projdir`) and displays whether the effective
  value comes from `PROJMUX_PROJDIR`, tmux `@projmux_projdir`, saved config, or
  no configured source. When no source is configured, the direct-set prompt
  starts with `$HOME` as an editable fallback, but `$HOME` is not used as the
  effective root unless saved. `Workdirs` remains separate: those entries are
  additional search roots, not the primary root. The About section reads the
  cached update status without network access;
  selecting Check Updates runs `projmux update check`, and Update Now runs
  `projmux update apply`. The same About section also lists the keybinding
  diagnostic path: zero-config first, `setup` for swallowed keys, `init` for
  supported terminal fallbacks, and `doctor` for dependencies.

## See also

- [npm-distribution.md](npm-distribution.md) — npm binary package layout.
- [statusbar.md](statusbar.md) — two-line layout and click range catalogue.
- [notify-queue.md](notify-queue.md) — queue file format and lifecycle.
- [usage-tracking.md](usage-tracking.md) — adapter HTTP/file behaviour.
- [keybindings.md](keybindings.md) — terminal key delivery and CSI-u.
- [hooks.md](hooks.md) — `post-create` hook contract.
