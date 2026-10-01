## Notify queue

`projmux` keeps a single JSON-backed queue of pending notifications at
`<state>/projmux/notify.json` (typically `~/.local/state/projmux/notify.json`,
following XDG). Writes go through an `O_CREATE|O_EXCL` lock file
(`notify.json.lock`) with bounded retry + jittered backoff so the queue
is safe across concurrent producers (the AI flow, the manual `attention
toggle`, the `create notification` CLI) on a local filesystem.

Attention and notify are intentionally separate surfaces: attention is live
tmux pane state, while notify is the explicit-ack pending queue derived from
AI reply panes and explicit pushes. The queue helps clicks route to work; it
does not own the truth of every live badge.

- **Push** — `projmux create notification` (or the in-process producer in
  `internal/app/notify_producer.go`) appends an entry. Entries carry a
  stable id (caller-supplied or `ai:<session>:<pane>` for the producer
  path), text (capped at 80 runes), severity (`info|warn|critical`),
  source (`ai|k8s|git|external`), TTL freshness metadata (default 600s), and a
  `Target{Socket, Session, Window, Pane}`. Re-pushing an existing id
  refreshes the entry's text and timestamp.
- **List** — `projmux get notifications` returns newest-first without mutating the
  queue. TTL alone is not a removal condition. `projmux get notifications --live` adds a
  read-only comparison against live pane state, explaining manual reply
  badges without queue entries, live AI replies with/missing queue entries,
  and inactive (`queue-stale`) `ai:` entries.
- **Ack** — `projmux notification ack <id>` removes one entry; `--all`
  flushes everything. Interactive focus/click handlers ack after successful
  focus, and gone/unroutable targets clean up without focusing.
- **Reconcile** — `projmux notification reconcile` walks
  `tmux list-panes -a` and back-fills entries for panes whose
  attention state is `reply` AND whose AI agent option is set,
  reporting inactive `ai:` entries that no longer match a live reply+agent pane without
  acking them. It then removes rows only when they are both TTL-expired and
  gone from the real pane/session inventory, and enforces a 256-row hard cap
  by evicting oldest overflow. Live rows otherwise remain explicit-ack-only.
  `make install` and `projmux update apply` invoke it so the queue
  recovers from any drift introduced by a lost daemon.

The producer is wired to the attention state machine: a pane
transitioning to `reply` with an AI agent option set pushes an
`ai:<session>:<pane>` entry; the matching `clear` (or the AI
flow's `status set idle`) leaves it pending until explicit ack. Manual `attention toggle` on a
shell pane does not push because the agent option is empty —
the queue is intentionally AI-driven only.

See [notify-queue.md](../notify-queue.md) for the full reference.
