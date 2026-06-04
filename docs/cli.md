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
| `ai` | Manage tmux AI splits (Codex/Claude/Antigravity) and per-pane status. |
| `attention` | View and manage live tmux pane attention state. |
| `attach` | Open tmux lifecycle entry helpers. |
| `current` | Resolve the active tmux pane path. |
| `doctor` | Diagnose runtime dependencies. |
| `focus` | Switch the active client to a session/window/pane target. |
| `init` | Preview or apply supported terminal key delivery mappings. |
| `kill` | Terminate tagged tmux sessions. |
| `notify` | Manage the pending AI notify queue (push/list/ack/reconcile). |
| `pin` | Manage pinned project directories. |
| `preview` | Manage persisted tmux preview selection. |
| `prune` | Trim stale tmux lifecycle state. |
| `quit` | Quit the app-owned projmux tmux runtime. |
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
| `welcome` | Print the shell onboarding guide again. |
| `usage` | Report AI token usage across 5h and weekly windows. |
| `version` | Print the current version. |

## switch

```
projmux switch [path]
projmux switch toggle-tag | toggle-pin | kill | settings | preview
projmux switch cycle-pane | cycle-window | sidebar-focus
```

Project picker. With no positional argument, opens the configured picker popup
or sidebar (depending on entry helper). With a path, jumps directly. The
sub-verbs are entry hooks invoked by tmux keybindings (e.g.
`sidebar-focus` is wired to the sidebar's focus binding so navigation keeps
the active session in sync).

Settings > Labs remains available for experimental settings, but picker backend
selection has been retired. The native picker is always used.

## setup

```
projmux setup [--timeout DURATION] [--non-interactive]
```

Probes the guaranteed launch defaults (`Alt-1..5`) plus transport candidates
such as `Ctrl-N`, `Ctrl-Shift-{R,L,M}`, `Ctrl-M`, and
`Alt-Shift-{Left,Right}`. Reports whether each key arrived as the expected
plain bytes, an unsupported legacy/app-specific sequence, an unknown sequence,
or a timeout. `--non-interactive` skips the TTY probe and prints the expected
key map. Default `--timeout` is `5s`. Run it outside tmux after trying
`projmux shell`; `Alt-1..5` are the only guaranteed zero-config defaults.
Settings > Keybindings edits in-app action aliases; terminal delivery
diagnostics stay in this CLI flow.

## init

```
projmux init [terminal] [--apply | --dry-run] [--config <path>]
             [--allow-symlink]
```

Previews or applies terminal-specific key delivery mappings for supported
terminals when `projmux setup` reports swallowed shortcuts. When `terminal` is
omitted, autodetects from `$TERM_PROGRAM`/`$TERMINAL_EMULATOR`.
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

Runs a dependency check: `tmux ≥ 3.4`, `git`, `stty` (POSIX only), and
`kubectl` (optional), then reports read-only AI notify integration diagnostics
for Codex hooks, Claude Code hooks, and the tmux bell
fallback. AI notify integration statuses are `installed`, `missing`, or
`conflict`; missing or conflicting integrations are informational and do not
make doctor fail. It also reports read-only Session State resume metadata
diagnostics for saved agent panes, including `available`, `stale`, or
`unavailable` status plus confidence, source, updated-at, and the affected
snapshot/window/pane.

Exit code `0` even when optional deps or AI notify integrations are missing;
non-zero only when a required dep is missing or stale. `--json` emits a
machine-readable object with `dependencies`, `ai_notify_integrations`, and
`session_state_resume`; the default is the human report with suggested install
commands per platform, AI integration install/remove/dry-run commands, and
Session State resume metadata health. `--install-missing` is explicit opt-in
and runs generated install commands only for missing or stale required
dependencies. `--dry-run` prints those commands without executing them.
`--include-optional` also includes optional missing dependencies such as
`kubectl` when an install command is available. Install flags cannot be combined
with `--json`. Doctor does not diagnose terminal key delivery; use `projmux
setup` for that.

`Settings > Notifications > Delivery sources` shows active Codex hooks, Claude,
Antigravity manual hook ingest, and tmux statuses, conflicts, config paths, and
copyable AI integration commands where available. Settings does not install or
remove external Codex, Claude, Antigravity, or tmux notify wiring.

## focus

```
projmux focus --target SESSION[:WINDOW[.PANE]] [--socket <path>]
              [--client <tty>]
              [--source ai|status-bar|external|os-notification|toast]
              [--kind reply-ready|busy-cleared|segment-click|toast-click|custom]
              [--json]
projmux focus --uri "projmux://focus?pane_id=%N&socket=<path>&source=toast"
              [--json]
```

Unified switch-client dispatch. Resolves the session against the live tmux
inventory (with prefix/fuzzy fallback), then redirects one suitable attached
client on the selected socket. It never force-detaches other clients. If no
client is attached on that socket, it emits the configured desktop
notification instead. `--socket` is explicit; when omitted, the socket is
derived from `$TMUX`.

After a successful tmux focus, `projmux focus` dispatches host-terminal
osfocus only when Desktop notification mode is `raise`. Modes `off` /
`none` and `notify` keep focus in tmux without a host-window raise.

`--client` is a preferred origin tmux client. In-app consumers such as the
status bar and notify sidebar pass the clicked client so focus redirects that
display first. If that client is gone, focus falls back to an attached client
already viewing the target session, then the stable first attached client.
Toast clicks do not pass `--client`.

`--uri` is the entry point used by the WSL Toast click handler (see
[configuration.md](configuration.md#toast-click-handler-wsl--windows-terminal)).
The pane id from the URI is resolved to a `SESSION:WINDOW.%paneID` target
via `tmux display-message`, and the URI's `socket` overrides any
`--socket` flag so the click round-trips back to the right tmux server.
`--uri` and `--target` are mutually exclusive.

Exit codes:

- `0` — focused (or the notify-only fallback fired).
- `2` — target session/window/pane could not be resolved
  (`focusExitNotResolved`).

`--source`/`--kind` are telemetry labels logged when
`PROJMUX_FOCUS_DEBUG` is set. `--json` prints a single-line JSON payload
with `{ok, fallback, target, socket, resolved_session, client, dispatch,
session_state, window_state, pane_state, reason, note}`. Callers can
distinguish unresolved sessions (`reason=session-unresolved`, exit 2),
session rename/prefix fallback (`session_state=fallback`), window index
fallback (`window_state=index-fallback-session`), pane index fallback
(`pane_state=index-fallback-window`), and explicit id failures
(`window-id-unresolved` / `pane-id-unresolved`, exit 2).

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
projmux notify list  [--live] [--json] [--limit N] [--ui table|sidebar] [--client <tty>]
                     [--severity ...] [--source ...]
projmux notify ack   <id> | --all
projmux notify reconcile [--json]
```

- `push` — append (or refresh, with `--id`) one entry. `--ttl` defaults to
  `600` seconds as freshness metadata; it does not remove rows from
  `notify list`. `--text` is hard-capped to 80 runes (longer text is
  truncated server-side). After a successful queue write, projmux sends a
  best-effort refresh event to open native notify sidebars and fires
  declarative `[hooks.send-noti]` asynchronously if configured. Event delivery
  failure does not fail the queue write; reopening the sidebar still shows the
  latest queue. The hook gets a JSON payload on stdin plus
  `PROJMUX_NOTIFY_*` env vars, and it does not replace the normal desktop
  notification path.
- `list` — newest-first pending queue table `ID AGE SEV SRC TARGET TEXT`
  (or JSON). `--severity` and `--source` are repeatable filters.
  `--live` adds a non-mutating explanation table (or JSON report) that
  compares queued entries with live pane attention. It calls out manual
  reply badges that do not queue because no AI agent is attached, live AI
  reply panes missing a queue entry, matched AI reply entries, and stale
  queue entries whose live pane no longer matches. `--ui=sidebar` opens the
  compact interactive notify list where Enter focuses and acks a target, `a`
  acks the selected row, `x` clears non-critical rows, and `Ctrl-X` clears all;
  opening or navigating the sidebar does not ack. While open, the native
  sidebar refreshes its row list on successful queue-write events without an
  Alt-2 close/reopen toggle, using the same deferred refresh path as `a` and
  `x`. The sidebar uses two-line cards with notification text first and
  compact age/project/window/pane metadata below. Hidden queue ids remain
  action values, but the sidebar has no search input. `--client` is used by
  tmux popup launchers to keep row-select focus on the clicked client.
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
projmux usage [--model codex|claude|antigravity|all] [--window 5h|weekly|all]
              [--json] [--force|-f]
```

Renders a tab-aligned `MODEL WINDOW PCT RESETS_AT STALE` table; appends a
backoff note when an adapter is in 429 cooldown. `--force` clears any
active backoff and bypasses the per-adapter throttle floor (Claude `5m`,
Codex shares the global `30s`). `--json` emits the snapshot array; when
backoff is active the wrapper `{snapshots, backoff}` object is emitted
instead.

Antigravity has no supported 5-hour/weekly quota adapter. `--model
antigravity` renders an explicit unsupported note: the stable Antigravity
signal is `context-window-only` statusline data, which is not mixed into the
Claude/Codex quota HUD.

## status

Per-segment status-bar renderers. All four are silent on failure — the
tmux status interval polls them and must never produce a stack trace.

```
projmux status git    [path]
projmux status kube   [session]
projmux status usage  [--max-width N] [--force|-f]
projmux status notify [--max-width N]
```

- `git` — `#[bold,fg=colour16,bg=colour45] <branch> <state> #[default]` for
  the pane's `pane_current_path` (or the supplied path). Empty when not in a
  repo. `<state>` is omitted when clean, otherwise it may include `*` for
  local changes, `+N` staged entries, and `↑N`/`↓N` ahead/behind counts, with
  compact per-token colors in tmux output.
- `kube` — `⎈ <context>/<namespace>` segment. Reads
  `~/.cache/tmux/kube-segment-<session>.txt` first (TTL governed by
  `TMUX_KUBE_CACHE_TTL`, default `5s`). Picks up a per-session
  `KUBECONFIG` from `${XDG_RUNTIME_DIR:-~/.cache}/kube-sessions/<session>.yaml`.
- `usage` — HUD-style `Claude (Nm) 5h [bar] N% · weekly [bar] N%   Codex 5h
  [bar] N% · weekly [bar] N%`. Degrades through six tiers as `--max-width`
  shrinks. Triggers an opportunistic, throttled refresh (per-adapter
  throttle, `30s` floor) so a stale cache self-heals.
- `notify` — newest-first HUD block with project, state, optional agent, text,
  age, and `+<extras>`. Window/pane ids remain routable metadata but are not
  displayed in the compact HUD. Degrades through width tiers; default
  `--max-width` is `200` runes.

## statusbar

```
projmux statusbar click <range-id> [--socket <s>] [--mouse-window <id>]
                                   [--client <tty>] [--mouse-x N] [--mouse-y N]
```

Click/keyboard dispatcher for the two-line status bar. Implemented range ids:
`session pwd kube git usage notify settings`. The bare `window` /
`window|<idx>` token (tmux's built-in window-list range) and the empty
range fall through to `select-window -t @<mouse_window>` so the native
click-to-switch tab affordance is preserved on row 0. Unknown range ids are
non-specialized placeholders and no-op. `session` opens the existing-session
popup; `pwd` shows the current pane path in a native-framed display-only
popup; `kube` and `git` open the project switcher popup;
`settings` toggles the settings popup for the tmux client; `usage` opens the
detailed `projmux usage` table popup; `notify` focuses and acks the newest
actionable queue target.
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
projmux ai split    [--agent <claude|codex|antigravity|shell|selective>] [--force-agent] [right|down] [-- <extra-arg>...]
projmux ai picker   --inside <right|down>
projmux ai settings
projmux ai status   set <thinking|waiting|idle> [--pane <id>]
projmux ai notify   <reset|notify> [--pane <id>]
projmux ai watch-title [--pane <id>]
projmux ai ingest   codex-hook < payload.json
projmux ai ingest   claude-hook < payload.json
projmux ai ingest   antigravity-hook < payload.json
projmux ai ingest   bell --pane <pane_id>
projmux ai ingest   log [--tail N] [--json] [--path]
projmux ai integrate codex [--dry-run] [--remove]
projmux ai integrate claude [--dry-run] [--remove]
projmux ai integrate tmux-bell [--dry-run] [--remove]
projmux ai topic     ...
```

Manages the AI split lifecycle and the per-pane state machine that drives
the `attention` badge, the `notify` queue producer, and the desktop
notifier. `status set waiting` is the trigger that flips a pane to the
reply-ready state — that transition pushes an `ai:<session>:<pane>`
entry into the notify queue.
The durable semantic status badge is stored separately in
`@projmux_ai_badge_kind` as `approval_required`, `input_required`,
`response_complete`, `in_progress`, or unset. The live status surfaces color
those values with action-required amber-orange, success green, and progress
yellow respectively. That palette is independent from notify queue
`--severity` and from OS desktop notification urgency; a critical approval row
can still render a non-red action-required status badge.

`ai split right|down` uses the configured default split mode. Add
`--agent claude`, `--agent codex`, `--agent antigravity`, `--agent shell`, or
`--agent selective` for a one-shot launch without changing that default.
Concrete `--agent claude|codex|antigravity` invocations create a new managed
agent pane every time; existing managed AI panes in the same project/session are
not selected or reused.
`--agent selective` opens the existing picker flow; `--agent shell` opens the
existing plain shell split. Arguments after `--` are extra arguments appended to
the resolved `claude`, `codex`, or `agy` executable inside the managed wrapper;
projmux still sets the context directory, tmux title, AI pane metadata, title
watcher, and split layout.
Settings > AI Settings > Enabled agents controls Claude/Codex/Antigravity launch
visibility. Disabled agents are hidden from the selective picker and from the
default-mode picker. A saved default that later becomes disabled fails clearly
instead of falling back to another agent. Direct
`--agent claude|codex|antigravity` launches also fail when disabled, including
shortcuts that call the same command. For a deliberate one-shot direct CLI
launch, pass `--force-agent`; picker and saved default paths do not use this
override. If all AI agents are disabled, the selective picker still offers the
plain `shell` split and shows guidance to re-enable Claude/Codex/Antigravity.
For user-level skill, slash-command, editor, or launcher registrations that
call this contract, see [AI Agent Shortcuts](ai-agent-shortcuts.md).

`ingest codex-hook` is the hook-facing entrypoint for Codex hooks-engine JSON.
It reads one JSON payload from stdin and handles the default Codex hook catalog
`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreCompact`, `PostCompact`,
`SessionStart`, `UserPromptSubmit`, and `Stop` as exposed by Codex CLI 0.130.0.
It accepts the common Codex hook fields `hook_event_name`/`event_name`,
`thread_id`, `session_id`, `turn_id`, `cwd`, `transcript_path`, `model`,
`tool_name`, nested `tool.name`, `tool_input`, and `input`. `UserPromptSubmit`
marks the matched pane hook-active and moves it to thinking/busy without
pushing a queue entry. `Stop` pushes an info completion row. `PermissionRequest`
pushes a critical approval row with the tool name and concise action summary.
The other Codex events are quiet: they mark the pane hook-active and write
ingest diagnostics, but do not push notify queue entries.
For event names without a specialized notify/state handler, ingest falls back
to quiet/log-only handling. A local catalog entry with `"action": "quiet"`
therefore lets newly discovered events be installed and observed without
creating notification noise; `"notify"` and `"state"` still require a built-in
handler before they can change pane state or push queue rows.
Runtime action overrides from
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json` take
precedence over catalog `action` during ingest, including known events such as
`Stop` and `PermissionRequest`. These overrides are managed by
`Settings > Notifications > Hook quiet policy` and do not change the catalog
`install` field used by `projmux ai integrate codex`. A runtime `notify`
override for a known Codex event without a specialized handler, such as
`PreToolUse` or `PostToolUse`, pushes a short generic in-app row like
`PreToolUse · Bash` with agent/category metadata. Generic rows are
queue/sidebar/statusbar only and
do not dispatch OS desktop notifications, `PROJMUX_NOTIFY_HOOK`, or
`[hooks.send-noti]`.

`ingest claude-hook` is the hook-facing entrypoint for Claude Code hooks. It
reads one JSON payload from stdin and handles the default Claude Code 2.1.140
hook catalog: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`,
`PostToolBatch`, `PermissionDenied`, `Notification`, `UserPromptSubmit`,
`UserPromptExpansion`, `SessionStart`, `Stop`, `StopFailure`,
`SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`, `SessionEnd`,
`PermissionRequest`, `Setup`, `TeammateIdle`, `TaskCreated`,
`TaskCompleted`, `Elicitation`, `ElicitationResult`, `ConfigChange`,
`InstructionsLoaded`, `WorktreeCreate`, `WorktreeRemove`, `CwdChanged`, and
`FileChanged`. It uses the same pane matching order as Codex ingest
(`$TMUX_PANE`, payload `cwd`, then cached session id), marks matched panes
hook-active, and writes metadata-bearing notify queue entries only for
reply-ready, input-ready, approval-required, error, and teammate-idle events.
`SubagentStop` and the other lifecycle/tool events are quiet: they mark the
pane hook-active and write ingest diagnostics, but do not push notify queue
entries. `UserPromptSubmit` only moves the pane to thinking/busy and does not
push a queue entry.
Unknown Claude events also fall back to quiet/log-only handling after pane
matching. Catalog `action` is honored for quiet fallback events; notify/state
actions need built-in handlers for event-specific body text and state changes.
Runtime action overrides from
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json` take
precedence over catalog `action` for known Claude events too; for example a
noisy notify event can be made state-only or quiet without changing installed
Claude hook commands.

`ingest antigravity-hook` is the manual hook/statusline entrypoint for
Antigravity CLI `agy` payloads observed in the Phase 0b smoke. Projmux does not
provide `projmux ai integrate antigravity`, does not install Antigravity hooks,
and does not mutate Antigravity user config. If users wire Antigravity manually,
the hook/statusline command should call an absolute `projmux` path or run from a
known cwd because relative command paths failed smoke.

The default known Antigravity catalog is intentionally narrow:
`PostInvocation` is quiet/log-only, `Stop` is notify, and `Statusline` is notify
only when manually wired and `tool_confirmation_pending=true`. `Stop` with
`error` or a non-normal `terminationReason` pushes a critical error row; `Stop`
without those error signals pushes an info completion row. Accepted identity
fields include `conversationId`/`conversation_id`, `cwd` or `workspace.path`,
`transcriptPath`, `fullyIdle`, `agent_state`, and `context_window`.
Antigravity notify metadata uses `agent=antigravity`. Phase 3 session-state
restore is included: Antigravity ingest stores `conversationId` as pane thread
metadata for matching and as session-state resume metadata. Restore uses
`agy --conversation <uuid>` when that id is present and UUID-shaped; otherwise
session-state preview/doctor render `resume unavailable`. Usage quota HUD
support remains unsupported because the only stable usage signal is
`context-window-only` statusline data. Transcript contents are not read.

`ingest bell --pane <pane_id>` is the narrow tmux-bell fallback ingest path.
It does not require the pane to be AI-managed. Projmux resolves session,
window, pane, title, command, and socket metadata from tmux, pushes an info
queue row such as `bell · <pane title>`, and suppresses repeat bell rows from
the same pane for 5 seconds.

`ingest log` prints recent ingest diagnostics from
`$XDG_STATE_HOME/projmux/ai-ingest.log`, or `~/.local/state/projmux/ai-ingest.log`
when `XDG_STATE_HOME` is unset. Ingest paths append compact JSONL records for
parse errors, unsupported events, missing pane matches, deduped bells,
state-only transitions, quiet high-volume events, and notify pushes. Raw hook
payloads are not stored. The log is capped at 1 MiB and trimmed to the most
recent roughly 512 KiB when it grows past the cap. Use `--json` for raw JSONL
and `--path` to print the resolved file path.

For `Stop`, projmux reads `transcript_path` when present and extracts the last
assistant text from the transcript tail; if that is unavailable, it falls back
to a generic Claude completion row. `PermissionRequest` rows expose the tool
name plus a concise input summary, preferring Bash commands, file paths, and
URLs when those fields exist.

Hook row text is intentionally compact: agent label, event category, then the
best available summary. Structured details remain in queue metadata and are
available from `notify list --json`; the sidebar does not add a separate
metadata detail view.

The extra Claude events accept conservative field aliases while Claude's event
schemas settle. `StopFailure` reads `error_type`/`errorType`/`failure_type` and
`error_message`/`errorMessage`/`message`/`reason`, plus nested
`error.type|name|code` and `error.message|text|reason`. `SubagentStop` reads
`subagent_type`/`subagentType`/`agent_type`, `subagent_id`/`subagentId`, and
nested `subagent.type|name|kind|id`. `TeammateIdle` reads
`teammate_name`/`teammateName`, `teammate_id`/`teammateId`,
`teammate_context`/`teammateContext`/`context`/`reason`/`message`, and nested
`teammate.name|id|context|status|reason|message`.

Claude Code hook ingest is available through `ingest claude-hook`, but
`integrate claude` is the opt-in user-level wiring command for
`~/.claude/settings.json`. It installs command hooks for every event whose
effective Claude hook catalog entry has `"install": true`. The embedded default
catalog is based on Claude Code 2.1.140 and lives at
`internal/app/ai_hook_catalogs/claude.json`; a local override may be placed at
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/claude.json` to disable
or add events before projmux itself is released:

```json
{
  "provider": "claude",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

The embedded Claude catalog contains all 29 Claude Code 2.1.140 events:
`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`,
`PermissionDenied`, `Notification`, `UserPromptSubmit`,
`UserPromptExpansion`, `SessionStart`, `Stop`, `StopFailure`,
`SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`, `SessionEnd`,
`PermissionRequest`, `Setup`, `TeammateIdle`, `TaskCreated`,
`TaskCompleted`, `Elicitation`, `ElicitationResult`, `ConfigChange`,
`InstructionsLoaded`, `WorktreeCreate`, `WorktreeRemove`, `CwdChanged`, and
`FileChanged`. `SubagentStop` remains quiet/log-only.

The managed command receives Claude's hook JSON on stdin, keeps stdout/stderr
quiet, and exits successfully even if ingest fails so it does not block Claude
Code behavior. `--dry-run` previews the JSON update, and `--remove` deletes
only commands carrying the projmux marker. Removal and unmanaged conflict
detection scan every `hooks` event in the settings file rather than trusting the
current catalog, so stale managed events from older catalogs are still removed.
Existing unrelated Claude settings and hooks are preserved. If any event already
contains an unmanaged `projmux ai ingest claude-hook` command, projmux refuses
to install over it and leaves the settings file untouched.

`projmux ai integrate codex` manages a hooks-engine
block in `~/.codex/config.toml`. It enables `[features] hooks = true`,
merging into an existing `[features]` table when present, and installs broad
command hooks for every event whose effective Codex hook catalog entry has
`"install": true`. The embedded default catalog is based on Codex CLI 0.130.0
and lives at `internal/app/ai_hook_catalogs/codex.json`; a local override may
be placed at `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/codex.json`:

```json
{
  "provider": "codex",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

The embedded Codex catalog contains the 8 Codex CLI 0.130.0 events
`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreCompact`,
`PostCompact`, `SessionStart`, `UserPromptSubmit`, and `Stop`.

```toml
[features]
hooks = true

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PermissionRequest]]
matcher = "*"
[[hooks.PermissionRequest.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PostToolUse]]
matcher = "*"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PreCompact]]
matcher = "*"
[[hooks.PreCompact.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PostCompact]]
matcher = "*"
[[hooks.PostCompact.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.SessionStart]]
matcher = "*"
[[hooks.SessionStart.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.UserPromptSubmit]]
matcher = "*"
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"
```

Codex hooks are the default integration mode. The hooks install is idempotent,
preserves unrelated Codex config and unmanaged hook entries, and refuses to
install over unmanaged projmux Codex hook commands it cannot safely own.
`--remove` removes projmux-managed Codex hooks wiring. `--dry-run` prints the
planned change without writing. Codex may still require reviewing or trusting
the hook through its `/hooks` flow before commands run.

`integrate tmux-bell` is opt-in server-level tmux wiring for arbitrary tools
that emit BEL or OSC 9. It applies `allow-passthrough on`, `monitor-bell on`,
`bell-action other`, and appends a marked `alert-bell` hook that invokes
`projmux ai ingest bell --pane "#{pane_id}"`. `--dry-run` prints the tmux
commands. tmux `alert-bell` is a window alert and does not expose
`#{hook_pane}` on tmux 3.4, so `#{pane_id}` is the best available pane context.
`--remove` unsets only hook entries carrying the projmux marker and leaves
user-owned `alert-bell` hooks alone.

## tmux

```
projmux tmux popup-toggle [--client <key>] <mode>
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
`notify-sidebar`, `ai-split-picker-right`, `ai-split-picker-down`,
`ai-split-settings`.
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
When the cache is missing or stale, shell startup attempts a bounded
best-effort refresh, then continues even if the network check fails. When a
fresh cached update is available, the shell welcome uses Enter=Continue,
`u`=Upgrade, and `s`=Skip until next. Upgrade invokes only
`projmux update apply`; Skip until next writes the latest release tag to
`${XDG_CACHE_HOME:-~/.cache}/projmux/update-skip.json` and suppresses that tag
until a newer latest release appears.

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

## welcome

```
projmux welcome [--popup [--force]]
```

Prints the shell-entry release prompt and bootstrap reminder without starting
tmux. This is useful when you want to revisit the welcome view at any time.

`--popup` is the tmux attach-helper form. It shows the popup only when a
pending attach welcome marker exists. `--popup --force` opens the popup without
consulting pending or skip state. Passing positional arguments prints usage and
returns a usage error.

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

## current / shell / attach / settings / quit

- `current` — print `pane_current_path` for the active tmux pane (used
  by the shell jump binding).
- `shell` — boot the isolated `-L projmux` tmux server with the
  generated config. The generated app config uses absolute `$SHELL` as the
  tmux default shell when set, otherwise `/bin/sh`. `shell` starts or attaches
  the app session directly after resolving the target app session name and
  startup directory. Alt-1 sidebar project open defaults to `Empty session`; the
  Session State `Sidebar startup picker` opt-in shows `Latest snapshot`, `Named
  snapshot`, and `Empty session` before creating a closed project session.
  `Latest snapshot` is auto-saved; named snapshots are fixed until the user
  saves or replaces them.
- `quit` — open an action picker with `Quit projmux` and `Cancel`. Selecting
  `Quit projmux` terminates only a `tmux -L projmux` runtime whose global
  `@projmux_app` option is set by the generated app config. Missing servers,
  default tmux servers, embedded tmux servers, and other tmux runtimes without
  that marker are no-ops. Non-interactive callers must pass `--yes` or
  `--force`; the default command always goes through the action picker.
- `attach auto [--keep=N] [--fallback=home|ephemeral]` — auto-attach to
  the most recent session, with bounded retention and a fallback policy.
- `settings` — interactive configuration UI for the project picker, AI
  splits, Notifications, Appearance mode, Project Root management, the
  switcher's saved workdirs list, Labs (experimental), Settings > Keybindings,
  and About/Update status. The keybinding flow is a single
  `Settings > Keybindings` action list with simplified action details for
  aliases and reset. Terminal diagnostics and terminal mapping application stay
  in the `projmux shell` -> `projmux setup` -> `projmux init` remediation path.
  The About section includes the `Welcome` entry. In Project
  Picker, `Project Root` manages the saved
  primary root (`~/.config/projmux/projdir`) and displays whether the effective
  value comes from `PROJMUX_PROJDIR`, tmux `@projmux_projdir`, saved config, or
  no configured source. When no source is configured, the direct-set prompt
  starts with `$HOME` as an editable fallback, but `$HOME` is not used as the
  effective root unless saved. `Workdirs` remains separate: those entries are
  additional search roots, not the primary root. Appearance stores per-surface
  path/git/notify icon decoration as `off` (default), `symbol`, or `emoji` and
  updates the matching live tmux option when available. Labs remains
  available for experimental settings. The About section reads the cached
  update status without network access;
  selecting Check Updates runs `projmux update check`, Update Now runs
  `projmux update apply`, and Welcome opens a Settings-native viewer
  independent of shell skip state. `Settings > About > Quit projmux` routes
  through the same `projmux quit` action picker. The same About section also
  lists the keybinding diagnostic path: try `projmux shell` first, use `setup`
  for swallowed keys, use `init` for supported terminal mappings, and use
  `doctor` for dependencies.

## See also

- [npm-distribution.md](npm-distribution.md) — npm binary package layout.
- [statusbar.md](statusbar.md) — two-line layout and click range catalogue.
- [notify-queue.md](notify-queue.md) — queue file format and lifecycle.
- [usage-tracking.md](usage-tracking.md) — adapter HTTP/file behaviour.
- [keybindings.md](keybindings.md) — terminal key delivery and aliases.
- [hooks.md](hooks.md) — lifecycle hooks, startup commands, and `send-noti` payload contract.
