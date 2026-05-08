# Architecture

## Core model

`projmux` is built around a small set of domain objects:

- `ProjectRoot`: a directory that may map to a tmux session
- `SessionIdentity`: the stable session name derived from a directory
- `SessionTarget`: the current selected session/window/pane target
- `CandidateSet`: the ordered list of project directories presented to the user
- `PinSet`: user-curated candidate priority state
- `PreviewState`: selected window/pane state used by popup and session previews

## Layers

### 1. Core
Pure rules and state transitions.

Responsibilities:
- directory normalization
- session naming
- candidate ordering
- pin state changes
- tagged selection state
- lifecycle decisions such as reuse, create, kill, fallback

This layer should not shell out directly.

### 2. Integrations
Adapters for external systems.

Initial adapters:
- tmux
- kubeconfig per-session state
- filesystem
- git metadata for preview enrichment

Responsibilities:
- execute commands
- parse command output
- convert failures into typed errors

### 3. UI orchestration
Picker data is modeled independently from fzf rows. The app builds
backend-neutral `picker.Item` values (`Title`, `Value`, `SearchText`,
`MetaLines`, `Badges`, `PreviewTarget`) and then adapts them to the selected
backend. `fzf` remains the default, stable backend; the native backend is
opt-in through `PROJMUX_PICKER_BACKEND=native` while it reaches full parity.

Responsibilities:
- rows for popup and sidebar views
- preview rendering
- keybind-to-action dispatch
- selection handoff into core actions
- picker-agnostic close/dismiss actions

Picker-specific display and search rules are tracked in
[picker-ui-plan.md](picker-ui-plan.md).

This keeps parity with the existing shell workflow while moving state and behavior into Go.

### 4. Local environment
This repo owns the portable application behavior and generated tmux config.

Responsibilities that remain outside `projmux`:
- terminal emulator key dispatch
- shell startup policy
- install-time package checks
- machine-specific path and symlink choices

## Configuration model

Config should be explicit and file-backed.

Candidate areas:
- managed roots
- default home-like roots
- preview preferences
- session naming exceptions
- kube session settings
- ephemeral session retention defaults

## State model

Persistent state:
- pins
- lightweight user preferences

Ephemeral runtime state:
- preview selection
- popup marker files
- current tagged selection set

## Notify queue

`projmux` keeps a single JSON-backed queue of pending notifications at
`<state>/projmux/notify.json` (typically `~/.local/state/projmux/notify.json`,
following XDG). Writes go through an `O_CREATE|O_EXCL` lock file
(`notify.json.lock`) with bounded retry + jittered backoff so the queue
is safe across concurrent producers (the AI flow, the manual `attention
toggle`, the `notify push` CLI) on a local filesystem.

Attention and notify are intentionally separate surfaces: attention is live
tmux pane state, while notify is the short-lived actionable queue derived from
AI reply panes and explicit pushes. The queue helps clicks route to work; it
does not own the truth of every live badge.

- **Push** — `projmux notify push` (or the in-process producer in
  `internal/app/notify_producer.go`) appends an entry. Entries carry a
  stable id (caller-supplied or `ai:<session>:<pane>` for the producer
  path), text (capped at 80 runes), severity (`info|warn|critical`),
  source (`ai|k8s|git|external`), TTL (default 600s), and a
  `Target{Socket, Session, Window, Pane}`. Re-pushing an existing id
  refreshes the entry's text and timestamp.
- **List** — `projmux notify list` returns newest-first. TTL'd entries
  are filtered out at read time, not at write time, so a slow ack pass
  never resurrects stale rows. `projmux notify list --live` adds a
  read-only comparison against live pane state, explaining manual reply
  badges without queue entries, live AI replies with/missing queue entries,
  and stale `ai:` entries.
- **Ack** — `projmux notify ack <id>` removes one entry; `--all`
  flushes everything. The status-bar click handler acks the entry
  it focused so the queue self-clears.
- **Reconcile** — `projmux notify reconcile` walks
  `tmux list-panes -a` and back-fills entries for panes whose
  attention state is `reply` AND whose AI agent option is set,
  acking stale `ai:` entries that no longer match a live pane.
  `make install` and `projmux upgrade` invoke it so the queue
  recovers from any drift introduced by a lost daemon.

The producer is wired to the attention state machine: a pane
transitioning to `reply` with an AI agent option set pushes an
`ai:<session>:<pane>` entry; the matching `clear` (or the AI
flow's `status set idle`) acks it. Manual `attention toggle` on a
shell pane does not push because the agent option is empty —
the queue is intentionally AI-driven only.

See [notify-queue.md](notify-queue.md) for the full reference.

## Usage snapshots

`projmux usage` and `projmux status usage` share a single `Manager`
that walks two registered adapters (Claude, Codex) and persists the
result to `<state>/projmux/usage/snapshots.json` (or
`PROJMUX_USAGE_STATE_DIR`). The cache file is the authoritative source
for the HUD render path so the tmux status interval never blocks on a
network call.

- **Per-adapter throttle** — Claude reports a 5-minute hint via the
  `ThrottleHinter` interface; Codex falls through to the global
  `30s` floor used by `status usage`. `MaybeCollect` only invokes an
  adapter when `now - last_collect >= throttle`. `--force` bypasses the
  gate.
- **429 backoff** — Claude implements `BackoffStater`. On HTTP 429
  the adapter persists `BackoffState{Until, Consecutive}`: the
  default cooldown is 30 minutes, doubling per consecutive 429 up to a
  60-minute cap. A `Retry-After` header (when present) raises the floor.
  During backoff `Collect` short-circuits (no network call). A clean
  200 resets the streak. `--force` clears the persisted state via the
  `BackoffResetter` interface so the next call attempts the network
  call regardless of streak.
- **Failure preservation** — adapter failures do not erase prior
  rows. The Manager merges new snapshots over the on-disk slice, so a
  transient 429 keeps the last known good numbers visible.

See [usage-tracking.md](usage-tracking.md) for adapter detail (token
refresh, rollout schema).

## Two-line clickable status bar

projmux configures tmux with `status 2`. Line 0 is the existing
session/window/path/git/kube/clock row. Line 1 splits the notification bar
(left half, capped at 80 cells) and the AI usage HUD (right half, capped at
120 cells) using tmux `#[align=left]` / `#[align=right]`. Each clickable
segment is wrapped in a tmux user-defined range (`#[range=user|<id>]...
#[norange]`) and dispatched through `projmux statusbar click <range-id>`. A
single `bind -n MouseDown1Status` covers both lines because tmux fires
`MouseDown1Status` from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to whichever range the cursor was over.

| Range id | Line | Click action                              | Keybinding   |
|----------|------|-------------------------------------------|--------------|
| session  | 0    | popup `projmux sessions --ui=popup`       | prefix+s s   |
| pwd      | 0    | copy pane_current_path and show path popup | prefix+s p   |
| kube     | 0    | popup `projmux switch --ui=popup`         | prefix+s k   |
| git      | 0    | popup `projmux switch --ui=popup`         | prefix+s g   |
| usage    | 1    | popup `projmux usage`                     | prefix+s u   |
| notify   | 1    | focus origin pane of newest notification  | prefix+s n   |

The keyboard chord uses `bind-key s switch-client -T projmux-status` so the
prefix-then-`s`-then-letter shortcut routes through the same dispatcher as
the mouse click. Empty `#{mouse_status_range}` (clicks on whitespace) is a
no-op so the binding never flashes a spurious error.

## Non-goals

- replacing tmux
- owning terminal emulator bindings
- becoming a generic worktree orchestrator
- implementing a fully custom TUI before parity is reached
