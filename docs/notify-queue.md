# Notify queue

`projmux` keeps a persistent JSON queue of pending AI notifications.
`attention` is live tmux pane state; `notify` is the user's pending
notification source of truth. The queue is derived from live state and user
pushes. Live entries remain pending until explicit ack unless they are among
the oldest rows beyond the 256-entry hard cap; reconcile can also collect an
expired entry after its tmux target disappears. Each entry can route
the status-bar notify segment or notify sidebar to the originating tmux pane
via `projmux focus`, and feeds the HUD pill rendered by
`projmux status notify`.

## File layout

```
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/notify.json
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/notify.json.lock
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/notify-queue-events/refresh-*.sock
```

The lock file is acquired via `O_CREATE|O_EXCL` with bounded retry
(default ~200 attempts, 2ms–50ms jittered backoff) and a 30s
stale-after window so a crashed writer never permanently wedges the
queue.

The queue file is a pretty-printed JSON array of `Notification`
objects, sorted newest-first on read. `expires_at` is freshness metadata;
expired entries are not filtered or deleted by `list`. Reconcile removes an
expired row only when the real tmux inventory also shows its pane/session is
gone; if inventory is unavailable, that target-based removal is skipped.

Open native notify sidebars also create per-process Unix datagram sockets for
queue-write refresh events. If the state-dir socket path would exceed Unix
socket path limits, projmux uses a short per-state-dir temp runtime path for
the socket directory. These sockets are transient UI delivery endpoints only:
they are not queue state, do not change the JSON schema, and are removed when
the sidebar exits.

## Data model

`internal/core/notify`:

```go
type Notification struct {
    ID        string    // stable key for dedupe / ack
    Text      string    // capped at 80 runes
    Severity  string    // info | warn | critical
    Source    string    // ai | k8s | git | external
    Metadata  map[string]string // optional producer metadata
    CreatedAt time.Time
    ExpiresAt time.Time
    Target            // session, window, pane, socket
}

type Target struct {
    Socket  string  // tmux -L socket path; empty = default
    Session string
    Window  string  // optional; "" means session-only
    Pane    string  // optional; "" means window-level
}
```

Defaults: `DefaultTTL = 600s`, `MaxTextLength = 80`. TTL is retained as a
freshness/display field, not a removal condition. `Severity` and
`Source` are validated against the constants above; an invalid value
returns `ErrInvalidSeverity` / `ErrInvalidSource` which the CLI maps to
exit code 2.

`Metadata` is optional and is omitted when empty. Hook producers use it for
routing/debug context such as `agent`, `thread_id`, `turn_id`, `cwd`,
`model`, and `client`; Claude hook rows also carry event-specific keys such as
`tool_name`, `tool_input.command`, `error_type`, `subagent_type`, and
`teammate_name`. Antigravity hook rows carry `agent=antigravity`,
`conversation_id`, `termination_reason`, `fully_idle`,
`tool_confirmation_pending`, `agent_state`, and `context_window` when present.
The same `conversation_id` can seed session-state restore via
`agy --conversation <uuid>` when it is UUID-shaped. Antigravity
account quota remains outside notify attention semantics: `context_window` is a
separate conversation-local gauge and is never treated as quota data.
Tmux bell fallback rows carry `agent=bell`, `event=bell`, and tmux target
context such as pane title, command, session, window, pane, and socket.
`notify list --json` includes this metadata as the structured data channel
while human table/sidebar output keeps the compact text body. Existing entries
without metadata remain valid.

## CLI surface

### push

```
projmux notify push --text <s> --target <SESSION[:WINDOW[.PANE]]>
                    [--socket <s>] [--severity info|warn|critical]
                    [--source ai|k8s|git|external]
                    [--ttl <seconds>] [--id <s>] [--json]
```

Appends one entry. With `--id` an existing entry is overwritten in
place (text + timestamp refresh), enabling idempotent producers like
the AI reply-ready transition. `--ttl` accepts a positive integer
number of seconds. `--json` prints `{id, queued}` for scripting.

### list

```
projmux notify list [--live] [--json] [--limit N] [--ui table|sidebar] [--client <tty>]
                    [--severity ...] [--source ...]
```

Newest-first pending queue entries. Default output is the tab-aligned
table `ID AGE SEV SRC TARGET TEXT`. `--severity` / `--source` are
repeatable filters. Without `--live`, this command reads only the queue and
preserves the stable JSON array used by scripts.

`--ui=sidebar` opens the notify queue as an interactive right-side pane/session
inbox when run inside the tmux popup surface. The queue source of truth remains
flat; the sidebar builds a read-only grouped view for display. The first screen
shows collapsed group rows keyed by pane when available, then window, then
session/external fallback. Each group row is a fixed three-line card: line 1
keeps project/session plus agent/provider with newest age, line 2 keeps
topic/pane-title/task context plus severity/live-state aggregate
metadata, and line 3 keeps the latest notification preview. Collapsed group
cards do not promote window/pane ids as primary information. A `+N` badge is
shown only when the group can unfold, and `N` is the number of child
notification rows that will appear; one-notification group headers omit both
the count badge and strong fold marker. Right/Left show and hide child rows for
foldable groups inside the native sidebar only; this fold state is
session-local and is not persisted. Right on a childless group refreshes
without adding rows. Enter on a group row, whether folded or expanded, focuses
the group's representative pane and acknowledges every visible notification in
that group only after focus succeeds. Inactive means an `ai:` queue entry points
to a pane that no longer matches live reply+agent state; it is not a time-age
TTL state, and Enter still focuses the target when it is routable. If the
representative target is gone/unroutable, Enter treats the selected pane inbox
as explicit cleanup and acknowledges/prunes the visible group without focusing,
including critical notifications. If a live- or inactive-looking representative target
disappears during focus, Enter uses the same gone-group cleanup policy. Other
focus failures keep the group pending, show a clear message, and refresh/prune
the list. Expanded child notification rows are compact event rows with age,
message preview, and severity/state while keeping the existing focus/ack-one
behavior. The surface actions
`NotifySidebar:Ack`, `NotifySidebar:AckGroup`,
`NotifySidebar:ClearNonCritical`, and `NotifySidebar:ClearAll` are internal
picker actions; direct launch aliases are edited in Settings, while internal
picker aliases are adjusted in `keymap.toml` when needed.
`NotifySidebar:AckGroup` defaults to uppercase `A` and explicitly
acknowledges every visible notification in the selected group, including
critical notifications. Runtime footer key guides read the merged keymap and
show the default alias when present, otherwise the first configured alias, so
custom aliases do not make the UI stale.
`NotifySidebar:Ack`, `NotifySidebar:AckGroup`, and
`NotifySidebar:ClearNonCritical` refresh rows, live state, and selection inside
the same native picker session; `NotifySidebar:ClearAll` still closes the
popup and prints a summary. Rows are intentionally compact: hidden queue ids
remain action values but the sidebar has no search input and intentionally does
not expose a separate metadata detail view.

When a new pending notification is successfully pushed by any app producer
(`notify push`, reply-ready, reconcile backfill, or bell fallback), open native
notify sidebars receive a best-effort queue-write event and rerun the same
`DeferredUpdate` row/live-state refresh path used by `a` ack and `x`
non-critical clear. Event delivery errors are ignored after the queue write:
the push still succeeds, and reopening the sidebar remains the recovery path
for seeing the latest queue.

`--live` adds a non-mutating explanation view that reads
`tmux list-panes -a` and compares the queue with live reply-state panes. It
does not push, ack, or otherwise repair anything. Human output keeps the
queue table and adds a `STATE TARGET ID EXPLANATION TEXT` section; JSON
output becomes `{queue, live, rows, errors}`. Typical states:

- `live-manual-reply` — a live reply/green badge exists, but no queue entry
  is expected because the pane has no AI agent metadata.
- `live-ai-reply-queued` — a live AI reply pane has the matching actionable
  queue entry.
- `live-ai-reply-missing-queue` — a live AI reply pane lacks the derived
  queue entry; run `projmux notify reconcile` to back-fill it.
- `queue-stale` — preserved machine-readable state for an inactive target:
  an `ai:` queue entry whose pane still EXISTS in the live tmux pane inventory,
  but no longer matches reply+agent state. It is not TTL/time age. Surfaced in
  the sidebar/statusbar as `INACTIVE` / `INA`; Enter still focuses and acks if
  the target is routable.
- `queue-gone` — the queue entry's target is gone. This is now determined two
  ways: (a) the entry has no routable target (empty session), or (b) the entry
  carries a pane target whose pane id is absent from the real tmux live pane
  inventory (`tmux list-panes -a`). Surfaced as `GONE` / `GON`, and Enter/ack
  cleans it up without focusing. The inventory check is best-effort: when the
  pane inventory cannot be read (tmux error, or an empty/unrecognized reply),
  membership-based GONE is skipped so a missing tmux server never falsely
  dims/gones every row, and only pane-target rows are eligible (window/session-
  only rows keep the empty-session check only).
- `queue-only` — a non-AI/external queue entry is pending and has no live AI
  reply-pane requirement.

To inspect live pane attention without queue context, use
`projmux attention list`.

### ack

```
projmux notify ack <id>
projmux notify ack --all
```

Removes one entry by id, or flushes the queue. `--all` returns the
removed count.

### reconcile

```
projmux notify reconcile [--json]
```

Walks `tmux list-panes -a -F` with the producer-key format (session,
window id, pane id, attention state, AI state, agent, topic, socket
path), then:

- pushes/refreshes one `ai:<session>:<pane>` entry for every pane
  whose attention state is `reply` AND whose agent option is non-empty;
- reports every existing queue entry whose id starts with `ai:` and whose
  pane no longer matches that condition as inactive/`queue-stale`, without
  acking it.

Successful backfill pushes publish the same best-effort open-sidebar refresh
event as other pending queue additions.

Soft-fails when tmux is not running (returns a populated `errors`
field rather than a non-zero exit) so the post-install hook does not
break. Run this as the recovery path when the on-disk queue has drifted
from live pane state.

Output: `reconcile: pushed N, acked M, kept K, stale S`.

Ack behavior is intentionally queue-local: only `ack` removes explicit ids or
flushes the queue. TTL is freshness metadata, and `reconcile` only repairs
derived `ai:` entries. Manual attention badges without agent metadata remain
live attention only.

## Producer (AI reply-ready)

`internal/app/notify_producer.go`. The attention state machine calls
`PushReplyReady` when a pane flips to `reply`. The producer reads
`@projmux_ai_pane_agent`, `@projmux_ai_pane_topic`, `#S`,
`#{window_id}`, `#{pane_id}`, `#{socket_path}` off the pane and writes
an entry with:

- id: `ai:<session>:<pane>`
- text: `<agent>: <topic>` (or `<agent>: ready` when no topic is set)
- severity: `info`, source: `ai`, freshness TTL: 10 minutes

When the pane leaves the reply state (manual `attention clear`,
`status set idle`, or a window close), `AckReplyReady` intentionally does not
remove the entry. The user consumes it through explicit ack. Store errors are
swallowed so the live tmux UI never blocks on disk IO. After a successful
queue write and same-pane non-critical compaction, the producer publishes the
same best-effort notify-sidebar queue-write refresh event used by
`projmux notify push`.

Manual `projmux attention toggle` on a pane without an agent option
does NOT push — the queue is intentionally AI-driven; reconcile honours
the same contract.

## Producer (tmux bell fallback)

`projmux internal agent-hook ingest bell --pane <pane_id>` is the opt-in fallback producer
installed by `projmux agent integrate tmux-bell`. It reads the target pane from
tmux and writes an info/source-ai row with:

- id: `ai:bell:<session>:<pane>`
- text: `bell · <pane title>` with command/window fallback context
- metadata: `agent=bell`, `event=bell`, pane/session/window/socket fields
- freshness TTL: 10 minutes

Unlike reply-ready reconcile, bell ingest does not require AI pane metadata.
It is intentionally available for arbitrary CLIs that only signal attention
through BEL or OSC 9. Repeated bells from the same pane are suppressed for 5
seconds before a later bell refreshes the stable queue id. Successful
non-deduped bell queue writes publish the same best-effort open-sidebar
refresh event as other pending queue additions.

## Consumer (status-bar click)

`internal/app/statusbar.go::handleNotify`. A click on the notify range
or the `prefix s n` chord reads the newest queue entry and dispatches:

```
projmux focus --target <target> --source status-bar --kind segment-click [--socket <s>] [--client <tty>]
```

The status bar passes the clicked tmux client as `--client` when tmux provides
it. The notify sidebar does the same for row selection. If that origin client
is no longer attached, `projmux focus` falls back to the existing target-session
client selection policy.

Outcomes:

- **Focus succeeded** — ack the selected entry, even when it is critical.
  Then bulk-ack older same-session/same-pane non-critical AI rows. Bulk cleanup
  never consumes `critical`, permission-request, stop-failure, `external`,
  `git`, or `k8s` rows; those remain pending unless the user selected that row
  directly.
- **Focus exited 2 (target unresolved)** — ack the entry and toast
  `notify target gone; cleared`.
- **Other failure** — keep the entry, toast `focus failed: <reason>`
  so the user can retry without losing the row.

The same consume policy is shared by notify-sidebar Enter and any
`projmux focus --uri` invocation, after a real tmux focus dispatch succeeds.
Desktop notifications are passive as of 0.11.0 — projmux emits no clickable
Toast and registers no `projmux://` handler — so the URI ack path is a
compatibility surface only. The in-app sidebar/statusbar consume path works in
every desktop notification mode.
Pane focus hooks and attention clear paths remain live-attention-only and do
not ack the notify queue; their response-complete badge consume is limited to
live tmux pane badge/state options. Non-critical AI completion producers also compact
older same-pane non-critical AI rows after replacing/pushing their latest row,
so reply-ready/stop/bell-style completion rows stay latest-state centered
without changing the queue schema or TTL contract.

The handler never returns a non-zero error to tmux's `run-shell`; every
failure becomes a `display-message` toast so a transient miss does not
trigger a tmux error popup.

## Render (status segment)

`projmux status notify` is the HUD-style renderer wired to the tmux
status interval. See [statusbar.md](statusbar.md) for the layout and
degradation tiers. It shows the newest pending item as a single notification
block with project, state, optional agent, text, age, and an extra-count
marker. Window/pane ids remain routable metadata but are not displayed in the
compact HUD. The renderer is silent on every failure mode (no store, list
error, empty queue) so the status line never carries a stack trace.
