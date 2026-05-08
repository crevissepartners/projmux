# Notify queue

`projmux` keeps a persistent JSON queue of pending AI notifications.
`attention` is live tmux pane state; `notify` is the user's pending
notification source of truth. The queue is derived from live state and user
pushes, but an entry remains pending until explicit ack. Each entry can route
the status-bar notify segment or notify sidebar to the originating tmux pane
via `projmux focus`, and feeds the HUD pill rendered by
`projmux status notify`.

## File layout

```
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/notify.json
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/notify.json.lock
```

The lock file is acquired via `O_CREATE|O_EXCL` with bounded retry
(default ~200 attempts, 2ms–50ms jittered backoff) and a 30s
stale-after window so a crashed writer never permanently wedges the
queue.

The queue file is a pretty-printed JSON array of `Notification`
objects, sorted newest-first on read. `expires_at` is freshness metadata;
expired entries are not filtered or deleted by `list`.

## Data model

`internal/core/notify`:

```go
type Notification struct {
    ID        string    // stable key for dedupe / ack
    Text      string    // capped at 80 runes
    Severity  string    // info | warn | critical
    Source    string    // ai | k8s | git | external
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
projmux notify list [--live] [--json] [--limit N] [--ui table|sidebar]
                    [--severity ...] [--source ...]
```

Newest-first pending queue entries. Default output is the tab-aligned
table `ID AGE SEV SRC TARGET TEXT`. `--severity` / `--source` are
repeatable filters. Without `--live`, this command reads only the queue and
preserves the stable JSON array used by scripts.

`--ui=sidebar` opens the notify queue as an interactive right-side list when
run inside the tmux popup surface. Enter focuses the selected target pane and
acks the row after focus succeeds. `a` acks the selected row. `Ctrl-A` clears
all rows via `notify ack --all`. Rows are intentionally compact: the visible
label keeps notification text first, then age, project, window, and pane
metadata; id/source/severity/target remain searchable.

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
- `queue-stale` — an `ai:` queue entry exists, but the live pane no longer
  matches reply+agent state; it remains pending until explicit ack.
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
  pane no longer matches that condition as stale, without acking it.

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
swallowed so the live tmux UI never blocks on disk IO.

Manual `projmux attention toggle` on a pane without an agent option
does NOT push — the queue is intentionally AI-driven; reconcile honours
the same contract.

## Consumer (status-bar click)

`internal/app/statusbar.go::handleNotify`. A click on the notify range
or the `prefix s n` chord reads the newest queue entry and dispatches:

```
projmux focus --target <target> --source status-bar --kind segment-click [--socket <s>]
```

Outcomes:

- **Focus succeeded** — ack the entry. Focus is the single consume path for
  routed notification clicks.
- **Focus exited 2 (target unresolved)** — keep the entry pending and toast
  `notify target gone; ack to clear`.
- **Other failure** — keep the entry, toast `focus failed: <reason>`
  so the user can retry without losing the row.

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
