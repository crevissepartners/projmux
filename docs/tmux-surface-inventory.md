# tmux Surface Inventory

Phase 0 artifact for the Mux substrate independence roadmap.

This document inventories the tmux command, format variable, hook, and option
surface that projmux currently calls or generates. It is intentionally an
inventory, not a backend abstraction design. Runtime behavior must remain on
the existing tmux path until a later phase extracts a mux contract.

## Scope And Method

Primary production sources:

- `internal/integrations/tmux/`
- `internal/integrations/sessionstate/`
- `internal/app/`
- Generated tmux config from `projmux tmux print-config`,
  `projmux tmux print-app-config`, and `projmux shell`

Support and fixture sources:

- `test/`
- `scripts/poc-native-picker-no-fzf-*.sh`
- Existing docs that describe generated tmux snippets

Searches used for this inventory:

- `rg 'run\("tmux"|read\("tmux"|exec\.Command\("tmux"' .`
- `rg 'tmux ' .`
- `rg 'display-message -p' .`
- `rg 'display-popup' .`
- `rg 'capture-pane' .`
- `rg 'set-hook' .`
- `rg '@projmux_' .`
- `rg '#\{[^}]+\}' .`

The tables below separate production runtime surface from e2e/support fixtures
and legacy cleanup candidates. Test fixture command strings are used to confirm
the production contract, but they are not treated as separate product surface
unless the corresponding production path exists.

## Phase 1 Runner Boundary

Phase 1 introduces `internal/integrations/mux` as a thin command-runner
boundary over the existing tmux backend. New app-layer subprocess calls to the
`tmux` binary should use `mux.Run`, `mux.Read`, or `mux.ReadTrimmed` so raw
binary invocation does not spread further. Existing typed tmux clients and
test-injected runner fields remain valid and do not need a broad rewrite in
this phase.

## Phase 2A Semantic Mux Reads And Pane Options

Phase 2A adds semantic helpers in `internal/integrations/mux` for the pane
metadata/read surfaces that psmux must eventually model:

- `SetPaneOption`, `UnsetPaneOption`, and `ShowPaneOption` cover
  `set-option -p` writes/unsets and `display-message -p "#{@...}"` reads.
- `DisplayMessage` and `DisplayMessageTrimmed` cover `display-message -p`
  format reads while preserving fake-runner injection through app-owned mux
  runners.
- `TmuxFormat`, `PaneOptionFormat`, and `JoinFormats` centralize common tmux
  format assembly so converted call sites do not hand-build `#{...}` strings.

Phase 2A intentionally does not introduce list-pane/list-window, popup, focus,
capture, lifecycle, or psmux backend behavior.

## Phase 2B Pane And Window Inventory Reads

Phase 2B adds semantic structured inventory helpers in
`internal/integrations/mux` while keeping tmux as the only production backend:

- `ListPanes` covers fixed-format `list-panes -F` reads for pane inventory.
- `ListWindows` covers fixed-format `list-windows -F` reads for window
  inventory.
- `DisplayPaneFields` covers one-pane `display-message -p` field reads.
- `FieldDelimiter` and `ParseFormatRows` centralize delimiter choice, escaped
  unit-separator compatibility, malformed-row skipping, and field trimming.

Converted app-layer read paths include AI hook pane matching, tmux bell pane
field reads, `attention list`/window badge reads, and notify queue reconcile.
The session-state typed tmux client remains on its existing capture path in this
phase so replay schema and generation logic stay unchanged.

### Phase 2B psmux Audit Capability Mapping

The table below maps the tmux format variables used by Phase 2B inventory reads
to the semantic capability that a future psmux audit/backend must provide.

| Capability | Current tmux read | Format variables / options | Converted read paths |
| --- | --- | --- | --- |
| `ListPanes` pane identity | `list-panes -a -F` | `#{session_name}`, `#{window_id}`, `#{pane_id}`, `#{pane_active}`, `#{socket_path}` | `attention list`, notify reconcile |
| `ListPanes` pane labels/state | `list-panes -a -F`, `list-panes -t <window> -F` | `#{pane_title}`, `#{@projmux_attention_state}`, `#{@projmux_ai_state}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_topic}` | `attention list`, attention window badge, notify reconcile |
| `ListPanes` AI hook match fields | `list-panes -a -F` | `#{pane_id}`, `#{pane_current_path}`, `#{@projmux_ai_thread_id}`, `#{@projmux_ai_session_id}` | AI hook pane matching |
| `DisplayPaneFields` bell pane fields | `display-message -p -t <pane>` | `#{session_name}`, `#{window_id}`, `#{window_name}`, `#{pane_id}`, `#{pane_title}`, `#{pane_current_command}`, `#{socket_path}` | tmux bell ingest |
| `ListWindows` window inventory | `list-windows -F` | `#{window_index}`, `#{window_id}`, `#{window_name}`, `#{window_layout}`, `#{window_panes}`, `#{pane_current_path}` | API available for the existing typed tmux window reads; conversion deferred outside Phase 2B app read slice |

## Classification Key

| Class | Meaning |
| --- | --- |
| `required MVP` | Needed for baseline session create/list/open, app shell, AI split, and project navigation. |
| `interactive UI` | Needed for popups, statusbar clicks, key bindings, picker launch, visible messages, or clipboard helpers. |
| `hooks/status` | Needed for generated tmux hooks, AI/attention/status segments, notification queue reconciliation, or settings-driven live options. |
| `e2e/support` | Used by install/apply flows, smoke scripts, POCs, or test harnesses. |
| `legacy/cleanup candidate` | Backward compatibility, migration markers, old option aliases, or code paths called out as future cleanup candidates. |

## Command Inventory

### Session And App Runtime

| Command | Class | Production call sites | Purpose / notes |
| --- | --- | --- | --- |
| `has-session -t <target>` | `required MVP` | `Client.sessionExists`, `tmuxSessionExists` | Check whether a project/app session exists before create/open. |
| `new-session -d -s <session> -c <cwd> [-e KEY=VALUE]` | `required MVP` | `Client.createDetachedSession` | Create detached project sessions. With lifecycle hooks enabled, adds `-P -F "#{pane_id}"` to capture the first pane id. |
| `new-session -A -s <session> [-c <cwd>]` | `required MVP` | `projmux shell` | Attach/create the isolated app session. Wrapped with `-L <socket> -f <config>`. |
| `attach-session -t <target>` | `required MVP` | `Client.OpenSession`, `Client.OpenSessionTarget` | Outside-tmux open path. Pane targets degrade to session/window where attach cannot select a pane. |
| `switch-client [-c <client>] -t <target>` | `required MVP`, `focus` | `Client.OpenSession`, `focusCommand`, generated status key table | Inside-tmux open/focus path. `PROJMUX_SWITCH_TARGET_CLIENT` can force the originating client. |
| `list-sessions -F ...` | `required MVP`, `focus`, `e2e/support` | `RecentSessions`, `RecentSessionSummaries`, `ListEphemeralSessions`, `focus`, `tmux apply` | Drives session picker rows, focus fallback, app reload probe, and ephemeral cleanup. |
| `list-windows -F ...` | `required MVP`, `session-state`, `hooks/status` | `ListSessionWindows`, `runRebalancePanes`, session-state capture/replay | Reads window inventory, window ids, pane counts, layouts. |
| `list-panes -F ...` | `required MVP`, `hooks/status`, `session-state` | `ListAllPanes`, AI matching, attention list, notify reconcile, session-state capture | Main pane inventory primitive. |
| `set-environment -t <session> KEY VALUE` | `required MVP`, `hooks/status` | `Client.applyProjectSessionEnv` | Applies project hook environment to newly-created sessions. |
| `kill-session -t <session>` | `required MVP`, `legacy/cleanup candidate` | `Client.KillSession`, session-state live replay cleanup | User/session lifecycle and destructive live replay cleanup. |
| `kill-server` | `required MVP` for app quit | `quitCommand` | Quits only an app-owned `tmux -L projmux` server after `@projmux_app=1` verification. |

### Popup And Interactive UI

| Command | Class | Production call sites | Purpose / notes |
| --- | --- | --- | --- |
| `display-popup ... <command>` | `interactive UI` | `tmux popup-*`, AI picker, hook trust prompt, welcome popup, statusbar pwd/usage popups | Native tmux popup surface. Uses `-E`, `-B`, `-c`, `-t`, `-d`, `-e`, `-x`, `-y`, `-w`, `-h`, `-T` depending on mode. |
| `display-popup [-c <client>] [-t <pane>] -C` | `interactive UI` | `tmux popup-toggle` | Closes a scoped popup. Notify sidebar close targets client instead of origin pane. |
| `display-message [message]` | `interactive UI`, `hooks/status` | AI/status/settings/attention/statusbar fallback paths | User-visible toasts and error fallbacks that avoid tmux `run-shell` error popups. |
| `select-pane -T <title> -t <pane>` | `interactive UI`, `hooks/status`, `session-state` | attention toggle/clear, AI topic/title, `tmux rename-pane`, replay shell wrapper | Sets pane title and topic-adjacent UI metadata. |
| `select-pane -t <target>` | `focus`, `session-state` | `focusCommand`, session-state replay | Selects target pane after focus or replay. |
| `select-window -t <target>` | `interactive UI`, `focus`, `session-state` | statusbar window-list passthrough, focus, live replay | Restores native window click behavior and selects replay/focus targets. |
| `split-window [-h|-v] [-P -F "#{pane_id}"] [-t <pane>] [-c <cwd>] <cmd>` | `required MVP`, `interactive UI` | AI split, session-state replay | Creates AI/shell panes. AI agent split reads the new pane id from `-P -F`. |
| `resize-pane -t <pane> -x|-y <size>` | `interactive UI` | AI split layout rebalance | Best-effort equal sizing after AI split. |
| `set-buffer -w -- <text>` | `interactive UI` | Settings copy helpers | Copies generated install/remove/dry-run commands to the tmux clipboard. |
| `command-prompt` | `interactive UI` | generated keymap | Prompt-based rename/topic actions in generated config. |
| `bind-key`, `unbind-key`, `switch-client -T` | `interactive UI`, `hooks/status` | generated config | Installs popup/statusbar/keybinding UX, including `MouseDown1Status` and the `projmux-status` key table. |
| `new-window -c "#{pane_current_path}"` | `interactive UI` | generated keymap | App keybinding for opening a new shell window in the current pane path. |
| `previous-window`, `next-window`, `select-pane -L/-R/-U/-D` | `interactive UI` | generated app keymap | Navigation bindings inside app config. |

### Hook, Status, And Notification Surface

| Command | Class | Production call sites | Purpose / notes |
| --- | --- | --- | --- |
| `set-hook -g pane-focus-out run-shell -b ...` | `hooks/status` | generated standalone/app config | Arms attention focus state on pane focus out. Uses `#{hook_pane}`. |
| `set-hook -g pane-focus-in run-shell -b ...` | `hooks/status` | generated standalone/app config | Clears attention state when pane receives focus. Uses `#{hook_pane}`. |
| `set-hook -g after-select-pane run-shell -b ...` | `hooks/status` | generated standalone/app config | Clears attention on selected pane. Uses `#{pane_id}`. |
| `set-hook -g pane-exited` / `after-kill-pane` | `hooks/status` | generated standalone/app config | Calls `projmux tmux rebalance-panes` after pane removal. |
| `set-hook -g client-attached` | `hooks/status` | generated app config | Opens the welcome popup once after client attach. |
| `set-hook -ag alert-bell ...` | `hooks/status` | `projmux ai integrate tmux-bell` | Installs bell fallback to `projmux ai ingest bell --pane "#{pane_id}"`. |
| `set-hook -gu alert-bell[...]` | `legacy/cleanup candidate`, `hooks/status` | `projmux ai integrate tmux-bell --remove` | Removes projmux-managed alert-bell entries. |
| `show-hooks -g alert-bell` | `hooks/status` | tmux bell integration planning | Detects existing managed bell fallback. |
| `run-shell -b <command>` | `hooks/status`, `interactive UI` | generated hooks, generated statusbar binds, AI watch-title | Async hook/status dispatch and title watcher launch. |
| `show-option -gqv <option>` | `hooks/status`, `legacy/cleanup candidate` | notification mode, statusbar decoration, `@projmux_projdir`, app ownership | Reads global user options and generated app markers. Some call sites still use direct `exec.Command("tmux", ...)`. |
| `show-option -gv @projmux_app` | `required MVP` for app quit | `quitCommand` | Confirms a socket is app-owned before `kill-server`. |
| `set-option -g <option> <value>` | `hooks/status` | settings, notification registration, generated config, tmux bell integration | Writes statusbar decoration, desktop notify mode markers, toast URI markers, and tmux bell options. |
| `set-option -g -u <option>` | `legacy/cleanup candidate` | notification URI migration | Unsets older URI registration markers. |
| `set-option -p [-u] -t <pane> <option> [value]` | `hooks/status`, `required MVP` for AI | AI state, attention, topics, notification dedupe, session-state recipe metadata | Core pane metadata storage. Phase 2A mux API: `SetPaneOption`, `UnsetPaneOption`. |
| `set-option -t <session> -q <option> <value>` | `session-state`, `required MVP` | session autosave/source, ephemeral sessions | Stores session-level live markers. |
| `source-file <config>` | `e2e/support`, app runtime support | `tmux apply`, install smoke | Reloads generated app config into live `-L projmux` server. |

### Capture And Replay

| Command | Class | Production call sites | Purpose / notes |
| --- | --- | --- | --- |
| `capture-pane -p -J -S -80 -t <pane>` | `capture`, `hooks/status` | AI watch-title/notification inference | Reads recent pane text joined into logical lines. |
| `capture-pane -p -t <pane> -S <n>` | `capture`, `required MVP` if generic pane viewer is used | `Client.CapturePane` | Generic typed tmux client capture helper. |
| `rename-window -t <target> <name>` | `session-state` | session-state replay | Restores first window name. |
| `new-window -d -t <target> -c <cwd> [-n <name>] [cmd...]` | `session-state` | session-state replay | Recreates additional windows. |
| `select-layout -t <target> <layout>` | `session-state` | session-state replay | Restores captured window layouts. |
| `move-window -d -k -s <window-id> -t <target>` | `legacy/cleanup candidate`, `session-state` | destructive live replay primitive | Used by `ApplyToExistingSession`, called out in roadmap backlog as possible cleanup. |
| `kill-window -t <window-id>` | `legacy/cleanup candidate`, `session-state` | destructive live replay primitive | Removes live windows not present in staged snapshot. |
| `send-keys -t <target> <command> Enter` | `required MVP`, `session-state` | startup commands and replay recipes | Runs startup or agent resume commands in target panes. |

### E2E, POC, And Support Scripts

| Command | Class | Source | Purpose / notes |
| --- | --- | --- | --- |
| `tmux -L <socket> new-session -d ...` | `e2e/support` | integration/install smoke scripts | Creates isolated smoke servers. |
| `tmux -L <socket> show-option -gqv @projmux_app` | `e2e/support` | install/integration/e2e smoke scripts | Confirms generated app config was sourced. |
| `tmux set-option -p ... @projmux_ai_*` | `e2e/support` | e2e smoke | Seeds AI/status metadata for visual smoke. |
| `tmux display-message -p ...`, `list-panes`, `list-windows` | `e2e/support` | native picker POC scripts | Verifies tmux state during native picker POC runs. |
| `set-environment -g PROJMUX_*` in generated POC config | `e2e/support` | native picker POC scripts | Seeds environment for sandboxed native picker tests. |

## `display-message -p` Format Variable Inventory

This section lists the format variables read through `tmux display-message -p`
only. Other `-F` uses in `list-*` commands are covered in the next section.

| Format | Class | Read by | Purpose / notes |
| --- | --- | --- | --- |
| `#{pane_current_path}` | `required MVP`, `interactive UI`, `hooks/status` | tmux client, AI split, status git, statusbar pwd, popup context | CWD for project switch, popup launch, status segments, and AI context. |
| `#{session_name}` | `required MVP`, `session-state` | tmux client, autosave, settings/sessionstate | Current session identity. |
| `#S` | `hooks/status`, `focus` | AI notify, status kube, popup context, focus URI translation | Short session name alias used in legacy/direct paths. |
| `#W` | `hooks/status` | AI desktop notification | Window name in notification body. |
| `#I` | `focus` | focus URI translation | Window index when converting a pane id from a toast URI. |
| `#{pane_id}` | `required MVP`, `interactive UI`, `hooks/status`, `focus` | popup context, AI split, watch-title gate, notify producer | Resolves target/origin pane ids and checks pane liveness. |
| `#{pane_title}` | `hooks/status`, `interactive UI` | attention, AI watch-title, notify producer | Pane label/topic evidence and attention title cleanup. |
| `#{pane_current_command}` | `required MVP`, `session-state`, `hooks/status` | startup wait, AI watch-title, bell ingest | Shell readiness and AI/bell classification. |
| `#{socket_path}` | `hooks/status`, `focus` | notification toast, attention/notify producer, bell ingest | Carries socket path into notify queue and toast focus URI. |
| `#{window_id}` | `hooks/status` | notify producer, bell ingest | Stable window target for notification rows. |
| `#{window_name}` | `hooks/status` | bell ingest | Bell notification fallback context. |
| `#{client_tty}` | `interactive UI`, `statusbar` | popup-toggle, statusbar generated bindings | Scopes popup markers and statusbar click origin. |
| `#{client_pid}` | `interactive UI` | popup context fallback | Fallback popup marker key when `client_tty` is empty. |
| `#{client_width}` / `#{client_height}` | `interactive UI` | popup sizing | Calculates popup dimensions for picker/status surfaces. |
| `#{@projmux_statusbar_decoration}` | `interactive UI`, `hooks/status` | popup context | Live fallback for popup/statusbar decoration mode. |
| `#{@projmux_sessionstate_autosave_at}` | `session-state` | autosave debounce | Per-session autosave gate timestamp. |
| `#{@projmux_sessionstate_source}` | `session-state` | session-state source check | Marks fresh/restored/autosave source. |
| `#{@projmux_attention_state}` | `hooks/status` | attention, AI, notify | Pane reply/busy state. |
| `#{@projmux_attention_ack}` | `hooks/status` | AI watch-title | Reply acknowledgement state. |
| `#{@projmux_attention_focus_armed}` | `hooks/status` | attention | Focus-gated attention clear behavior. |
| `#{@projmux_ai_agent}` | `required MVP`, `hooks/status`, `session-state` | AI/notify/session-state | Agent kind metadata. |
| `#{@projmux_ai_context}` | `hooks/status` | AI watch-title | AI context directory metadata. |
| `#{@projmux_ai_topic}` | `hooks/status`, `session-state` | AI/status/session-state | Display topic and restore recipe label. |
| `#{@projmux_ai_topic_manual}` | `hooks/status` | AI watch-title | Blocks automatic topic overwrite. |
| `#{@projmux_ai_state}` | `hooks/status` | attention/AI/notify | AI thinking/waiting/idle state. |
| `#{@projmux_ai_hook_active}` | `hooks/status` | AI watch-title gate | Prevents title watcher from overriding hook-driven metadata. |
| `#{@projmux_desktop_notification_key}` | `hooks/status` | AI notification dedupe | Dedupe key for desktop notifications. |
| `#{@projmux_desktop_notification_at}` | `hooks/status` | AI notification dedupe | Dedupe timestamp. |
| `#{@projmux_desktop_notified}` | `legacy/cleanup candidate`, `hooks/status` | AI notification reset/tests | Older notification marker still reset/written. |

## `list-* -F` Format Variable Inventory

| Command | Formats | Class | Purpose |
| --- | --- | --- | --- |
| `list-sessions -F` | `#{session_activity}`, `#{session_name}`, `#{session_attached}`, `#{session_windows}` | `required MVP` | Recent session ordering and picker summaries. |
| `list-sessions -F` | `#{session_name}`, `#{session_attached}`, `#{session_last_attached}`, `#{@projmux_ephemeral}` | `required MVP` | Ephemeral lifecycle inventory. |
| `list-sessions -F` | `#{session_id}` | `e2e/support` | `tmux apply` live server probe/count. |
| `list-sessions -F` | `#{session_activity}`, `#{session_name}`, `#{session_attached}` | `focus` | Focus fallback inventory. |
| `list-clients -F` | `#{client_active_pane}` | `hooks/status` | Detect whether a pane is visible to any attached client. |
| `list-clients -F` | `#{client_activity}`, `#{session_id}` | `required MVP` | Outside-tmux AI split target fallback. |
| `list-clients -F` | `#{client_name}`, `#{client_session}` | `focus` | Pick a client for focus dispatch. |
| `list-windows -F` | `#{window_index}`, `#{?window_active,1,0}`, `#{window_name}`, `#{window_panes}`, `#{pane_current_path}` | `required MVP` | Session preview and window inventory. |
| `list-windows -F` | `#{window_id}`, `#{window_panes}` | `hooks/status` | Pane rebalance after exits. |
| `list-windows -F` | `#{window_index}`, `#{window_name}`, `#{window_layout}` | `session-state` | Snapshot capture. |
| `list-windows -F` | `#{window_id}`, `#{window_index}` | `session-state` | Staged/live replay window mapping. |
| `list-panes -a -F` | `#{session_name}`, `#{pane_id}`, `#{window_index}`, `#{pane_index}`, `#{?pane_active,1,0}`, `#{pane_title}`, `#{@projmux_attention_state}`, `#{@projmux_ai_state}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_topic}`, `#{@projmux_attention_ack}`, `#{@projmux_attention_focus_armed}`, `#{pane_current_command}`, `#{pane_current_path}` | `required MVP`, `hooks/status` | Full pane inventory. |
| `list-panes -a -F` | `#{session_name}`, `#{window_id}`, `#{pane_id}`, `#{pane_active}`, `#{pane_title}`, `#{@projmux_attention_state}`, `#{@projmux_ai_state}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_topic}`, `#{socket_path}` | `hooks/status` | `attention list` inventory. |
| `list-panes -a -F` | `#{session_name}`, `#{window_id}`, `#{pane_id}`, `#{@projmux_attention_state}`, `#{@projmux_ai_state}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_topic}`, `#{socket_path}` | `hooks/status` | Notify queue reconcile. |
| `list-panes -a -F` | `#{pane_id}`, `#{pane_current_path}`, `#{@projmux_ai_thread_id}`, `#{@projmux_ai_session_id}` | `hooks/status` | AI hook payload to live pane matching. |
| `list-panes -t <window> -F` | `#{pane_title}`, `#{@projmux_attention_state}` | `hooks/status` | Window badge rendering. |
| `list-panes -t <target> -F` | `#{pane_id}`, `#{pane_left}`, `#{pane_top}`, `#{pane_width}`, `#{pane_height}` | `interactive UI` | AI split post-layout equalization. |
| `list-panes -s -t <session> -F` | `#{window_index}`, `#{pane_index}`, `#{pane_title}`, `#{?pane_active,1,0}`, `#{pane_current_path}`, `#{@projmux_recipe_kind}`, `#{@projmux_startup_command}`, `#{@projmux_ai_managed}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_topic}`, `#{@projmux_ai_resume_id}`, `#{@projmux_ai_resume_source}`, `#{@projmux_ai_resume_updated_at}` | `session-state` | Snapshot pane capture. |
| `list-panes -s -t <session> -F` | `#{pane_id}`, `#{pane_current_path}`, `#{@projmux_ai_managed}`, `#{@projmux_ai_agent}`, `#{@projmux_ai_session_id}`, `#{@projmux_ai_resume_id}`, `#{@projmux_ai_transcript_path}` | `session-state` | Refresh AI resume metadata before save. |

## Option And Hook Surface

### Projmux User Options

| Option | Scope | Class | Read / write purpose |
| --- | --- | --- | --- |
| `@projmux_app` | global | `required MVP` | Generated app config sets `1`; quit/install smoke verify before app runtime shutdown. |
| `@projmux_projdir` | global | `required MVP` | Declarative project root source read by `projmux switch` when inside tmux. |
| `@projmux_ephemeral` | session | `required MVP` | Marks ephemeral sessions for lifecycle inventory. |
| `@projmux_statusbar_decoration` | global | `hooks/status` | Legacy/fallback status decoration. |
| `@projmux_statusbar_decoration_cwd` | global | `hooks/status` | CWD segment decoration mode. |
| `@projmux_statusbar_decoration_git` | global | `hooks/status` | Git segment decoration mode. |
| `@projmux_statusbar_decoration_notify` | global | `hooks/status` | Notify segment decoration mode. |
| `@projmux_desktop_notify_mode` | global | `hooks/status` | Current 3-way desktop notify mode. |
| `@projmux_desktop_notify` | global | `legacy/cleanup candidate` | Legacy boolean desktop notify mode still read for compatibility. |
| `@projmux_uri_protocol_registered_v6` | global | `hooks/status` | WSL toast URI registration marker. |
| `@projmux_uri_protocol_registered` through `_v5` | global | `legacy/cleanup candidate` | Old URI markers unset during v6 registration. |
| `@projmux_legacy_appid_cleaned` | global | `legacy/cleanup candidate` | One-shot marker for old WSL toast AppID cleanup. |
| `@projmux_attention_state` | pane | `hooks/status` | `busy` / `reply` state for statusbar, notify, and attention UX. |
| `@projmux_attention_ack` | pane | `hooks/status` | Acknowledgement gate for AI reply detection. |
| `@projmux_attention_focus_armed` | pane | `hooks/status` | Prevents background focus from clearing pending reply too early. |
| `@projmux_ai_managed` | pane | `required MVP`, `session-state` | Marks projmux-managed AI panes. |
| `@projmux_ai_agent` | pane | `required MVP`, `session-state` | Agent kind: codex/claude/shell-derived metadata. |
| `@projmux_ai_context` | pane | `hooks/status` | AI context directory. |
| `@projmux_ai_state` | pane | `hooks/status` | thinking/waiting/idle status. |
| `@projmux_ai_topic` | pane | `hooks/status`, `session-state` | Pane topic shown in status/pane border and saved in snapshots. |
| `@projmux_ai_topic_manual` | pane | `hooks/status` | Manual topic overwrite guard. |
| `@projmux_ai_hook_active` | pane | `hooks/status` | Marks hook-driven panes. |
| `@projmux_ai_thread_id` | pane | `hooks/status` | Codex hook matching metadata. |
| `@projmux_ai_session_id` | pane | `hooks/status`, `session-state` | Hook/session resume matching metadata. |
| `@projmux_ai_transcript_path` | pane | `session-state` | Claude transcript fallback for resume id refresh. |
| `@projmux_ai_resume_id` | pane | `session-state` | Restore resume identifier. |
| `@projmux_ai_resume_source` | pane | `session-state` | Source of resume id: hook/session-id/transcript/log. |
| `@projmux_ai_resume_updated_at` | pane | `session-state` | Resume metadata freshness timestamp. |
| `@projmux_ai_bell_notified_at` | pane | `hooks/status` | Bell notification dedupe timestamp. |
| `@projmux_desktop_notified` | pane | `legacy/cleanup candidate` | Older desktop notification marker still written/reset. |
| `@projmux_desktop_notification_key` | pane | `hooks/status` | Desktop notification dedupe key. |
| `@projmux_desktop_notification_at` | pane | `hooks/status` | Desktop notification dedupe timestamp. |
| `@projmux_recipe_kind` | pane | `session-state` | Startup recipe marker. |
| `@projmux_startup_command` | pane | `session-state` | Startup recipe command. |
| `@projmux_sessionstate_source` | session | `session-state` | Fresh/restored/autosave source marker. |
| `@projmux_sessionstate_autosave_at` | session | `session-state` | Autosave debounce timestamp. |

### Generated tmux Options

The app and standalone generated configs also own normal tmux options. psmux
does not need to emulate every cosmetic option for an MVP, but it must account
for options that affect visible product behavior:

- App identity/runtime: `default-terminal`, `default-shell`,
  `default-command`, `update-environment`, `history-limit`, `set-clipboard`.
- Input behavior: `mouse`, `mode-keys`, `status-keys`, `escape-time`.
- Statusbar behavior: `status`, `status-position`, `status-interval`,
  `status-left`, `status-right`, `status-left-length`,
  `status-right-length`, `status-format[0]`, `status-format[1]`,
  `status-format[2]` unset.
- Window/pane labels: `automatic-rename`, `automatic-rename-format`,
  `window-status-format`, `window-status-current-format`,
  `window-status-separator`, `pane-border-status`, `pane-border-format`,
  pane/status/message style options.
- Bell fallback: `allow-passthrough`, `monitor-bell`, `bell-action`.

### Hook Names

| Hook | Class | Installed by | Payload |
| --- | --- | --- | --- |
| `pane-focus-out` | `hooks/status` | generated config | `run-shell -b '<bin> attention arm #{hook_pane}'` |
| `pane-focus-in` | `hooks/status` | generated config | `run-shell -b '<bin> attention clear #{hook_pane}'` |
| `after-select-pane` | `hooks/status` | generated config | `run-shell -b '<bin> attention clear #{pane_id}'` |
| `pane-exited` | `hooks/status` | generated config | `run-shell -b 'sleep 0.05; <bin> tmux rebalance-panes'` |
| `after-kill-pane` | `hooks/status` | generated config | `run-shell -b 'sleep 0.05; <bin> tmux rebalance-panes'` |
| `client-attached` | `hooks/status` | app generated config | `run-shell -b '<bin> welcome --popup >/dev/null 2>&1'` |
| `alert-bell` | `hooks/status` | `projmux ai integrate tmux-bell` | `run-shell -b 'projmux ai ingest bell --pane "#{pane_id}" ...'` |

## Surface-Specific Command Sets

### Popup

Required tmux commands:

- `display-message -p -F #{client_tty|client_pid|pane_id|#S|pane_current_path|client_width|client_height|@projmux_statusbar_decoration}`
- `display-popup` with sizing, target/client, env, cwd, border, title, and close-on-exit flags.
- `display-popup -C` to close toggle popups.

Dependent generated commands:

- `bind-key ... run-shell '<bin> tmux popup-toggle --client #{client_tty} <mode>'`
- `run-shell` from statusbar key table and mouse handlers.

### Statusbar

Required tmux commands/config:

- `set -g status 2`
- `set -g status-left`, `status-right`, `status-format[0]`, `status-format[1]`
- `#[range=user|...]`, `#[align=...]`, `#(<bin> status ...)`, and native window list `#{W:...}` format support.
- `bind-key -n MouseDown1Status if-shell -F "#{==:#{mouse_status_range},window}" { select-window -t = } { run-shell ... }`
- `bind-key s switch-client -T projmux-status`
- `bind-key -T projmux-status <key> run-shell ...`
- Runtime handlers: `display-message`, `display-popup`, `select-window`, `show-option`, `list-panes`.

### Hook

Required tmux commands:

- `set-hook -g` for generated focus/pane/client hooks.
- `set-hook -ag`, `set-hook -gu`, `show-hooks -g` for the optional bell fallback.
- `run-shell -b` payload execution.
- Hook format vars: `#{hook_pane}`, `#{pane_id}`.
- Pane option reads/writes via `display-message -p` and `set-option -p`.

### Capture

Required tmux commands:

- AI title/watch inference: `capture-pane -p -J -S -80 -t <pane>`.
- Generic tmux client helper: `capture-pane -p -t <pane> -S <start>`.

psmux audit should check both forms separately because joined-line capture
(`-J`) is product-visible for AI topic/notification inference.

### Focus

Required tmux commands:

- `list-sessions -F "#{session_activity}<sep>#{session_name}<sep>#{session_attached}"`
- `list-clients -F "#{client_name}<sep>#{client_session}"`
- `switch-client [-c <client>] -t <session>`
- `select-window -t <session>:<window>`
- `select-pane -t <session>:<window>.<pane>`
- URI translation: `display-message -p -t <pane-id> "#S<sep>#I"`
- Optional socket wrapper: `tmux -S <socket> ...`

### Session-State

Required tmux commands:

- Capture: `list-windows`, `list-panes -s`, `display-message -p`, `set-option`.
- Save refresh: `list-panes -s`, pane `set-option` resume metadata.
- Replay: `new-session`, `rename-window`, `new-window`, `split-window`,
  `select-layout`, `select-pane`, `send-keys`.
- Live overwrite primitive: `move-window`, `kill-window`, `kill-session`,
  `select-window`. This is classified as a legacy/cleanup candidate because
  the roadmap already tracks unused destructive live overwrite removal.

## Legacy And Cleanup Candidates

These items should not block Phase 1, but they are useful pressure points when
raw tmux calls are centralized:

- Direct `exec.Command("tmux", "show-option", ...)` remains in
  `tmuxProjdirOption` and the settings desktop notification resolver.
- Legacy desktop notify option `@projmux_desktop_notify` remains a read alias
  for old boolean state.
- URI registration markers `@projmux_uri_protocol_registered` through `_v5`
  are explicitly unset after successful v6 registration.
- `@projmux_desktop_notified` is still written/reset, but dedupe uses
  `@projmux_desktop_notification_key` and `_at`.
- Session-state parser accepts an older 11-field pane format while the current
  capture format emits 13 fields.
- `ApplyToExistingSession` uses destructive live overwrite commands
  (`move-window`, `kill-window`, temp `kill-session`) and is already tracked in
  the roadmap backlog as a possible removal.
- Native picker POC scripts contain raw tmux setup and assertions; keep them in
  e2e/support unless they graduate into production paths.

## psmux Parity Audit Draft Matrix

Phase 3 can use this section as the initial matrix location. If the table grows
too large, split it to `docs/psmux-parity-audit.md` and leave a link here.

Status values for Phase 3: `pass`, `partial`, `missing`, `unknown`.

| Capability | tmux command / format surface | Current class | psmux status | Audit notes |
| --- | --- | --- | --- | --- |
| Create detached project session | `new-session -d -s -c [-e] [-P -F "#{pane_id}"]` | `required MVP` | `unknown` | Must return first pane id when lifecycle/startup hooks need it. |
| Attach or switch to session | `attach-session`, `switch-client [-c] -t` | `required MVP`, `focus` | `unknown` | Outside/inside mux behavior may differ on Windows Terminal. |
| App shell server | `tmux -L <socket> -f <config> new-session -A -s` | `required MVP` | `unknown` | Need socket/server equivalent or explicit unsupported capability. |
| Session inventory | `list-sessions -F` plus session formats | `required MVP`, `focus` | `unknown` | Requires activity, attached count, windows, ids. |
| Window inventory | `list-windows -F` plus window formats | `required MVP`, `session-state` | `unknown` | Requires layout and window id for restore/live replay. |
| Pane inventory | `list-panes -a/-s -F` plus pane/user-option formats | `required MVP`, `hooks/status`, `session-state` | `unknown` | Highest-risk metadata surface. |
| Popup launch | `display-popup` with target/client/env/cwd/size/border/title/close flags | `interactive UI` | `unknown` | Roadmap already flags popup as a major risk. |
| Popup close | `display-popup -C` | `interactive UI` | `unknown` | Needed for toggle semantics. |
| Current context formats | `display-message -p -F #{pane_current_path}`, `#{pane_id}`, `#{client_tty}`, `#{client_width}`, `#{client_height}` | `interactive UI` | `unknown` | Popup and AI split depend on these. Phase 2A mux API: `DisplayMessage`, `DisplayMessageTrimmed`. |
| Focus URI translation | `display-message -p -t %N "#S<sep>#I"` | `focus` | `unknown` | Must map pane id to session/window for toast clicks. |
| Client inventory | `list-clients -F #{client_name} #{client_session} #{client_active_pane}` | `focus`, `hooks/status` | `unknown` | Needed for focus and reply auto-ack correctness. |
| Pane split | `split-window -h/-v [-P -F "#{pane_id}"] [-t] [-c] <cmd>` | `required MVP` | `unknown` | MVP AI/shell split requirement. |
| Resize panes | `resize-pane -x/-y` | `interactive UI` | `unknown` | Can be `partial` if MVP can tolerate default split sizing. |
| Pane title | `select-pane -T` and `#{pane_title}` | `interactive UI`, `hooks/status` | `unknown` | Used by attention and AI labels. |
| Pane options | `set-option -p`, `display-message -p "#{@...}"` | `required MVP`, `hooks/status`, `session-state` | `unknown` | Needs arbitrary user option storage or replacement metadata store. Phase 2A mux API: `SetPaneOption`, `UnsetPaneOption`, `ShowPaneOption`, `PaneOptionFormat`. |
| Global/session options | `set-option -g`, `show-option -gqv`, `set-option -t -q` | `hooks/status`, `session-state` | `unknown` | Required for settings, decoration, app markers, session-state source. |
| Generated statusbar | `status`, `status-format`, `#[range=user|...]`, `#(...)`, `#{W:...}` | `hooks/status` | `unknown` | May need a separate psmux-native status model. |
| Statusbar mouse/key dispatch | `MouseDown1Status`, `if-shell -F`, `switch-client -T`, `run-shell` | `interactive UI`, `hooks/status` | `unknown` | Product UX should degrade explicitly if unsupported. |
| Hooks | `set-hook`, `show-hooks`, `run-shell -b`, `#{hook_pane}` | `hooks/status` | `unknown` | Alert bell and focus hooks may be unavailable. |
| Bell fallback | `monitor-bell`, `bell-action`, `alert-bell`, `#{pane_id}` | `hooks/status` | `unknown` | Optional but important for unknown AI tools. |
| Capture pane | `capture-pane -p`, `capture-pane -p -J` | `capture` | `unknown` | Joined capture is separately audited from raw capture. |
| Session-state replay | `new-window`, `split-window`, `rename-window`, `select-layout`, `select-pane`, `send-keys` | `session-state` | `unknown` | Likely outside psmux MVP except basic create/split. |
| Live overwrite replay | `move-window`, `kill-window`, `select-window` | `legacy/cleanup candidate` | `unknown` | Candidate to exclude from psmux MVP and possibly remove. |
| Config reload | `source-file`, generated config file semantics | `e2e/support` | `unknown` | Required only if psmux has config-file parity. |
| Clipboard helper | `set-buffer -w` | `interactive UI` | `unknown` | Settings copy helper can fall back to OS clipboard later. |
| Socket targeting | `-L <socket>`, `-S <socket>` | `required MVP`, `focus` | `unknown` | psmux equivalent may be named server/session context. |
| Quoting/process launch | POSIX shell wrappers, tmux config quoting, `run-shell` | `required MVP`, `interactive UI` | `unknown` | Phase 3 must audit PowerShell-native quoting separately. |
