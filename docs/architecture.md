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
Picker data is modeled independently from row rendering. The app builds
backend-neutral `picker.Item` values (`Title`, `Value`, `SearchText`,
`MetaLines`, `Badges`, `PreviewTarget`) and renders them through the native
picker.

Responsibilities:
- rows for popup and sidebar views
- preview rendering
- keybind-to-action dispatch
- selection handoff into core actions
- picker-agnostic close/dismiss actions

Picker-specific display, search, input, and popup rules are tracked in
[native-picker.md](native-picker.md).

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

## Resource attribution model

The Linux resource-attribution core is an ephemeral, read-only projection. A
tmux-specific typed inventory supplies socket/session/window/pane identities,
pane PID/TTY, and the stable session `@projmux_project_path` anchor. A one-pass
procfs collector supplies PID+starttime identity, SID, CPU ticks, RSS, and host
capacity. Pure aggregation builds pane, unique-window, and project rows without
using labels, topics, titles, or cwd-derived names as ownership keys.

Resource snapshots are not Session State and are never saved or restored. See
[resource-attribution.md](resource-attribution.md) for metric, partial-state,
host-remainder, privacy, and measurement contracts.

## Resource metadata model

`projmux` owns a persistent resource model that is independent of tmux
lifecycle. It is the storage, ownership, and name-allocation foundation for the
CLI information architecture v2 resource routes.

Packages:

- `internal/core/metadata` is pure: the resource model, validation, name
  allocation, schema migration, snapshot reconciliation, and the operation
  transaction. It performs no I/O; the clock, uid source, and root-directory
  probe are injected through `Mutator`.
- `internal/integrations/metadata` owns the registry file (lock, atomic write,
  migration) and the tmux transport mirror.
- `internal/integrations/tmuxopts` is a dependency-free leaf holding the
  canonical spelling of every projmux-owned tmux option name, so the generated
  tmux config, session-state replay, and the resource mirror cannot drift.

Resources and ownership:

- Kinds are `Project`, `Window`, `Pane`, and `Agent`, stamped with
  `apiVersion: projmux.io/v1alpha1`.
- `ownerRef` runs Project → Window → (shell Pane | Agent), and an Agent owns
  its current managed Pane.
- A persistent tmux **Session is not a resource**. It is a 1:1 runtime
  projection of a Project recorded in `Project.status.session` with a `live`
  flag, and it owns no uid, name, or ownerRef. Auto-attach ephemeral sessions
  live only in runtime inventory, outside the Project hierarchy.
- Every Window owns an initial Pane and stores its uid in
  `spec.primaryPaneRef`. Project registration creates this topology **offline**,
  with no tmux involvement, so Project and Window metadata stays queryable
  while tmux is down.

Identity and naming:

- `metadata.uid` is opaque, immutable, and independent of tmux lifecycle. It
  survives snapshot/restore, runtime creation, and root rebind.
- `metadata.name` is the stable unique-within-scope query key. Project names
  are unique across the registry; Window, Pane, and Agent names are unique
  within their `ownerRef` scope.
- `metadata.displayName` may duplicate and is never a selector, an ownerRef, or
  identity. `metadata.labels` is key/value classification;
  `metadata.annotations` is non-identifying metadata such as an AI topic.
- Name bases are assigned once, at create or migration time, and are never
  re-derived: Window uses explicit name → initial command basename → configured
  shell basename → `window`; shell Pane uses command basename → shell basename
  → `pane`; a Pane managed by an Agent uses `<agent-name>-pane`; Agent uses
  explicit `--name` → normalized provider id → `agent`. Agent topics and raw
  pane titles are excluded as name seeds everywhere.
- Automatic collisions take the lowest free suffix (`Projmux-1`, `codex-1`)
  from the persisted `nameReservations` table, scanning integer suffixes rather
  than resource or map iteration order. An **explicit** `--name` or rename
  collision never receives an implicit suffix: it fails with exit code 2 and
  zero mutations.

Root lifecycle:

- `spec.root` is an absolute path. Rebind changes only `spec.root`, atomically,
  never moves files, and never changes the uid. Rebinding onto a root already
  bound to another Project fails with exit code 2 and zero mutations.
- uids are never merged heuristically. Basename, git origin, inode, and scan
  order are not consulted; only an exact saved root that reappears reuses its
  uid.
- A disappeared root records a `MissingRoot` condition with its first-observed
  timestamp and preserves both the metadata and the name reservations. A
  returning root recovers the same uid and clears the condition.

Agent lifecycle:

- The phase set is exactly `Pending`, `Running`, `Offline`, `Failed`. A normal
  managed-Pane exit or an explicit pane deletion resolves to `Offline`; a launch
  failure or an abnormal exit resolves to `Failed`. The Agent survives its Pane
  as a resumable resource.

Registry file and schema:

- The registry lives at `<state>/projmux/metadata/registry.json` (0600 below a
  0700 directory) behind an `O_CREATE|O_EXCL` lock file with bounded retry and
  stale-lock breaking, matching the notify queue and recent-windows stores.
- The envelope carries `schemaVersion: 1`. **v1 is the first envelope projmux
  has ever written, and no migration step ships today**, so the current version
  is the only version the registry accepts.
- Everything else fails closed: the file is refused as unreadable and **no
  write happens at all** — no rewrite, no backup, no staged temp file. This
  covers a **newer** schemaVersion (which would destroy state a newer build
  owns), malformed JSON, and a document that parses but carries **no**
  `schemaVersion`. An absent field decodes as version `0`, which means unknown
  rather than pre-release: migrating it would rewrite a corrupt or foreign file
  at the registry path, which is exactly the write-on-unknown-input that
  fail-closed exists to prevent. The registry is deliberately not quarantined
  or reset the way a corrupt recent-windows file is.
- A file that is absent, empty, or whitespace-only is still the legitimate
  "no registry yet" case and yields a fresh empty registry. Only a file with
  actual content and no usable `schemaVersion` is refused.
- The migration machinery is generic and version-indexed, ready for the first
  real schema bump: a registered older step is applied with backup → temp
  write → validate → atomic replace, so an interrupted or failing migration
  leaves either the original file or the fully migrated file, never a partial
  one. Downgrade writes are unsupported. Because production registers no step,
  that path is proven by tests that register one into a private migration set
  (`MigrationSet`, `ClassifySchemaVersionWith`, `MigrateRegistryWith`, and the
  store's private migration override) rather than by shipping a migration.
- **Field spelling:** the registry file intentionally uses the resource-model
  camelCase spelling (`apiVersion`, `schemaVersion`, `metadata`, `displayName`,
  `ownerRef`, `primaryPaneRef`, `spec`, `status`) rather than the snake_case
  used by the older projmux on-disk JSON. The two spellings coexist on purpose:
  existing snake_case files are **not** retro-changed, and the resource registry
  follows the resource-model contract.

Session State interoperability:

- Session snapshots carry resource identity through additive `omitempty`
  `metadata` blocks at the unchanged snapshot `version: 1` — one for the owning
  Project at the top level, one per Window, and one per Pane, each with
  `uid`, `name`, `labels`, `owner_kind`, and `owner_uid` in the snapshot's own
  snake_case spelling. No schema bump was needed, and a snapshot written
  without resource metadata still serializes byte-identically to the older form.
- Snapshots written before resource metadata existed still load and reconcile
  deterministically: the Project is matched by session projection and then by
  root, and Windows and Panes are matched positionally against the registry
  topology in insertion order.

tmux transport mirror:

- Live resources mirror identity into tmux options: `@projmux_project_uid` and
  `@projmux_project_name` on the session, the new window-scoped
  `@projmux_window_uid` and `@projmux_window_name`, and pane-scoped
  `@projmux_pane_uid` plus the existing `@projmux_pane_label` as the Pane
  **name** mirror. These are the first window-scoped projmux options; every
  earlier one was pane-, session-, or global-scoped.
- `rename pane` changes `Pane.metadata.name` and its `@projmux_pane_label`
  mirror only. It never writes the raw tmux `pane_title`.
- `rename window` changes `Window.metadata.name`, its option mirror, and the
  tmux `window_name`.
- Registry-managed Windows are set to `automatic-rename off` so a focused-Pane
  change cannot overwrite the Window name. The **global** `automatic-rename on`
  plus visible-pane-label `automatic-rename-format` default in the generated
  app config is unchanged, so unmanaged windows keep their existing behavior.
- Legacy naming migration seeds a Window name once: an existing
  `automatic-rename=off` window keeps its current `window_name`; an
  `automatic-rename=on` window derives a stable base from user Pane label →
  provider → known shell → `window`, and is then switched to
  `automatic-rename off`. An existing `@projmux_pane_label` is the migration
  seed and transport mirror for the Pane **name**; in the resource model that
  value is a name, not a label, and `metadata.labels` stays reserved for
  key/value classification.
- `Pane.metadata.name` is the primary pane display source. The derived
  `Pane.status.displayTitle` (Agent topic → known shell → raw pane title) is
  secondary and is never a selector, an identity, or a Window name source.

## Naming metadata model

Projmux keeps visible naming separate from source metadata:

- **User pane label** is persistent pane-scoped metadata stored in
  `@projmux_pane_label`. The Rename Pane action sets or clears only this field;
  it does not write the AI topic or raw pane title.
- **Pane border label** is the primary visible pane name. In the app tmux
  config and native previews it resolves to user pane label first, agent AI
  topic second, known interactive shell command (`zsh`, `bash`, `fish`, `sh`,
  `nu`, `xonsh`) third, and raw pane title last.
- **Window tab name** follows the active pane's visible pane label through the
  same tmux format expression used by the pane border. Historically the app
  config used raw `#{pane_title}` for `automatic-rename-format`, which let shell
  OSC titles such as branch names diverge from the pane border; generated app
  config now keeps the two aligned.
- **Terminal / pane title** remains raw title metadata owned by the running app
  or shell. It is still available to tmux and to Projmux features that need
  title evidence, but it is not the canonical Projmux window naming source.
- **AI topic** is agent-owned naming metadata stored in `@projmux_ai_topic`.
  Its set/clear CLI and watcher manual-ownership behavior remain independent of
  user pane labels.
- **Git branch** belongs in the statusbar git segment. Branch-based terminal
  title overwrites are not promoted to the primary Projmux pane or window name.
- **Session snapshots** store source metadata separately: `window_name`, raw
  `pane_title`, user `label`, `@projmux_ai_topic`, manual topic ownership, and
  agent resume metadata. Old snapshots decode with an absent label and absent
  ownership; title/topic equality never infers either. Replay writes each
  semantic field to the exact pane id returned by tmux creation and restores
  raw title from `Pane.Title` after launch/startup replay. Snapshots do not
  store a resolved `display_label`; visible labels are recomputed by display
  policy.

## Notify queue

`projmux` keeps a single JSON-backed queue of pending notifications at
`<state>/projmux/notify.json` (typically `~/.local/state/projmux/notify.json`,
following XDG). Writes go through an `O_CREATE|O_EXCL` lock file
(`notify.json.lock`) with bounded retry + jittered backoff so the queue
is safe across concurrent producers (the AI flow, the manual `attention
toggle`, the `notify push` CLI) on a local filesystem.

Attention and notify are intentionally separate surfaces: attention is live
tmux pane state, while notify is the explicit-ack pending queue derived from
AI reply panes and explicit pushes. The queue helps clicks route to work; it
does not own the truth of every live badge.

- **Push** — `projmux notify push` (or the in-process producer in
  `internal/app/notify_producer.go`) appends an entry. Entries carry a
  stable id (caller-supplied or `ai:<session>:<pane>` for the producer
  path), text (capped at 80 runes), severity (`info|warn|critical`),
  source (`ai|k8s|git|external`), TTL freshness metadata (default 600s), and a
  `Target{Socket, Session, Window, Pane}`. Re-pushing an existing id
  refreshes the entry's text and timestamp.
- **List** — `projmux notify list` returns newest-first without mutating the
  queue. TTL alone is not a removal condition. `projmux notify list --live` adds a
  read-only comparison against live pane state, explaining manual reply
  badges without queue entries, live AI replies with/missing queue entries,
  and inactive (`queue-stale`) `ai:` entries.
- **Ack** — `projmux notify ack <id>` removes one entry; `--all`
  flushes everything. Interactive focus/click handlers ack after successful
  focus, and gone/unroutable targets clean up without focusing.
- **Reconcile** — `projmux notify reconcile` walks
  `tmux list-panes -a` and back-fills entries for panes whose
  attention state is `reply` AND whose AI agent option is set,
  reporting inactive `ai:` entries that no longer match a live reply+agent pane without
  acking them. It then removes rows only when they are both TTL-expired and
  gone from the real pane/session inventory, and enforces a 256-row hard cap
  by evicting oldest overflow. Live rows otherwise remain explicit-ack-only.
  `make install` and `projmux upgrade` invoke it so the queue
  recovers from any drift introduced by a lost daemon.

The producer is wired to the attention state machine: a pane
transitioning to `reply` with an AI agent option set pushes an
`ai:<session>:<pane>` entry; the matching `clear` (or the AI
flow's `status set idle`) leaves it pending until explicit ack. Manual `attention toggle` on a
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
#[norange]`) and dispatched through `projmux internal statusbar click <range-id>`. A
single `bind -n MouseDown1Status` covers both lines because tmux fires
`MouseDown1Status` from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to whichever range the cursor was over.

| Range id | Line | Click action                              | Keybinding   |
|----------|------|-------------------------------------------|--------------|
| session  | 0    | popup `projmux sessions --ui=popup`       | prefix+s s   |
| pwd      | 0    | show pane_current_path in a display-only path popup | prefix+s p   |
| kube     | 0    | popup `projmux switch --ui=popup`         | prefix+s k   |
| git      | 0    | popup `projmux switch --ui=popup`         | prefix+s g   |
| usage    | 1    | popup `projmux usage`                     | prefix+s u   |
| notify   | 1    | focus origin pane of newest notification  | prefix+s n   |

The keyboard chord uses `bind-key s switch-client -T projmux-status` so the
prefix-then-`s`-then-letter shortcut routes through the same dispatcher as
the mouse click. Empty `#{mouse_status_range}` (clicks on whitespace) is a
no-op so the binding never flashes a spurious error.
The hardcoded `prefix s r` sibling is usage-specific: it runs the existing
throttled collector and then reopens the same display-only usage popup from
cache.

## Related design and inventory notes

Contributor-facing companions to this document. They are design records and
inventories rather than user documentation, so they are linked from here rather
than from the README docs index.

- [globalization.md](globalization.md) — the globalization contract: which
  user-facing string families are translatable and how they are classified.
- [migration-plan.md](migration-plan.md) — the standalone plan the shell-to-Go
  migration follows, slice by slice.
- [settings-ia.md](settings-ia.md) — the Settings information architecture:
  section ownership, row density, and feedback rules.
- [shell-autostart.md](shell-autostart.md) — shell auto-start integration and
  its opt-out behavior.
- [tmux-surface-inventory.md](tmux-surface-inventory.md) — the inventory of tmux
  options, hooks, and bindings projmux owns.

## Non-goals

- replacing tmux
- owning terminal emulator bindings
- becoming a generic worktree orchestrator
- implementing a fully custom TUI before parity is reached
