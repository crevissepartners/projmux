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
- `Window` and `Pane` carry **no stored liveness field**, deliberately. Their
  `status` block holds observed conditions only; live/offline is derived from a
  live tmux observation at read time. See *Runtime observation and resource
  status* below.
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
  re-derived. A declared Window create uses explicit name → initial command
  basename → configured shell basename → `window`; a Window newly imported
  from tmux always uses the literal `window` allocator base. Its observed
  `window_name`, Pane label, provider, command, shell, topic, and title are all
  excluded from stable identity. Shell Pane uses command basename → shell
  basename → `pane`; a Pane managed by an Agent uses `<agent-name>-pane`; Agent
  uses explicit `--name` → normalized provider id → `agent`.
- For a Window, the observed tmux `window_name` is
  `metadata.displayName`: duplicate-allowed, visible in `describe window`, and
  never a selector, ownerRef, or identity input. Existing Window uids, names,
  owners, and name reservations are preserved; there is no bulk naming
  migration.
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

Agent provider session ref:

- `status.sessionRef` is the durable pointer from an Agent to the provider
  conversation it belongs to. It is in **status, not spec**, because nothing
  declares it: a provider hook reports it after the fact, exactly like
  `status.paneRef`.
- The two status refs have deliberately different lifetimes. `status.paneRef` is
  the *current* managed-Pane binding and is cleared by `ReleaseAgentPane`,
  `DeletePane`, and every non-`Running` transition. `status.sessionRef` is
  cleared by none of them: an `Offline` Agent that has lost its Pane still knows
  which conversation it is.
- It is **not** a duplicate of the tmux pane option `@projmux_ai_session_id`,
  and that option is not going away. The pane option is the *live routing
  index*: hook ingest scans the live pane list and matches on it to decide which
  pane an incoming event belongs to, so following pane lifetime is correct for
  it. `status.sessionRef` answers "which conversation is this Agent" and must
  outlive the Pane. Ingest writes both.
- The shape is a **per-provider discriminated union**, not one flat string:
  `provider` is the discriminator and exactly one of `claude`, `codex`, or
  `antigravity` is populated. Providers disagree on what identifies a
  conversation — Claude reports a session id plus a transcript path, Codex a
  thread id and a session id, Antigravity a single conversation id — and
  flattening them would assert a false equivalence between a Codex thread id and
  a Claude session id.
- Codex's turn id is deliberately **not** stored. A turn addresses one turn
  inside the conversation and changes on every hook event, so it is not a
  pointer to the conversation.
- Transcript **paths** are recorded as the hook reported them. Nothing reads
  provider config files or transcript **contents**; that is permanently out of
  scope.
- The field is additive inside `schemaVersion: 1`. It is an optional pointer
  with `omitempty`, so a registry written before it existed decodes with a nil
  ref, validates, and re-encodes byte-identically. Bumping the envelope would
  make every already-installed build refuse the file fail-closed with
  `ErrSchemaTooNew`, which is a hard downgrade break bought for nothing.
- **"One conversation ↔ at most one live Agent" is deliberately NOT enforced.**
  Two Agents may carry the same conversation, and `get agents` / `describe agent`
  will show both. Enforcing it would mean a best-effort hook observation could be
  *refused*, making the registry describe a world that does not exist: the same
  conversation really can be attached twice (a manual resume of the same session
  id in a second pane already does that), and an `Offline` Agent keeps its ref
  forever, so a later Agent observing the same conversation would be permanently
  unable to record it. Choosing between several Agents that point at one
  conversation is a resume-time decision and belongs to the resume
  materialization Phase, not to an observation write.
- The write is narrow and idempotent: it touches `status.sessionRef` and nothing
  else — not the phase, not `lastTransitionAt`, not `paneRef` — and
  re-observing the same conversation opens no registry transaction at all. A
  hook whose provider contradicts the Agent's `spec.provider` is refused with
  zero mutations.

Agent resume:

- `agent resume <ref>` rebinds an existing Agent: it builds the provider's
  **resume** argv from `status.sessionRef`, splits a new managed Pane detached on
  the target Window's `spec.primaryPaneRef`, and attaches it to that Agent. The
  `metadata.uid` and `metadata.name` do not change, `status.phase` becomes
  `Running`, and `status.paneRef` points at the new Pane. `status.sessionRef`
  itself is read and never rewritten by resume.
- **A resume that cannot happen fails; it never becomes a create.** `create agent`
  always mints a new uid and `agent resume` always reuses one, and the two are
  not one code path: the resume route holds a launch seam whose only argv builder
  takes a conversation id, so there is no fresh-start argv it can produce. Every
  refusal below is decided against a read-only registry snapshot, so it opens
  zero registry transactions and issues zero `split-window` calls.
- The refusals are: a `Running` Agent (usage error naming `focus pane`); any
  phase other than `Offline`/`Failed`; **an Agent with no `sessionRef` at all**,
  which is the normal state of an Agent whose provider hook never ran and which
  names `create agent` rather than performing it; a ref whose conversation id the
  provider's own resume builder rejects; a ref contradicting a declared
  `spec.provider`; a provider disabled in Settings; a provider binary that is not
  installed; a `MissingRoot` Project; and a Window with no resolvable anchor Pane.
- **Several Agents may point at one conversation, and the conversation is never a
  selector.** Resume rebinds exactly the Agent the reference resolves to and
  never searches the registry by conversation id to choose a different one, so
  duplicates neither redirect nor block a rebind — refusing them would make the
  state the observation write deliberately allows permanently unusable. The
  duplicates are disclosed on stderr in uid order, which makes the disclosure
  byte-identical regardless of registry order.
- **`observedAt` is not a resume gate.** It records when projmux last *saw* the
  conversation, not a provider timestamp, so it cannot answer "is this ref
  stale": a conversation untouched for a month is perfectly resumable and one
  observed a minute ago may already be deleted. The only authority is the
  provider, and reading its store is permanently out of scope, so projmux checks
  what it can see and hands the rest to the provider's resume argv.
- Resume is **conversation-granularity for every provider**. Codex's turn id is
  not stored and `codex resume <thread-id>` has no turn slot, so turn-level
  resume is not something this surface can express today.

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
- `rename window` is the explicit stable-identity path: it changes only
  `Window.metadata.name`, its scoped name reservation, and the exact live
  `@projmux_window_name` transport mirror. It does not
  change `metadata.displayName` or tmux `window_name`.
- `rename project` likewise writes only `Project.metadata.name` and the exact
  live session's `@projmux_project_name`; it never renames the tmux session.
  `rebind project` preserves the Project uid and session name while updating
  `spec.root` and the exact live session's `@projmux_project_path`. Neither
  operation moves files.
- `rename agent` changes only the Window-scoped Agent `metadata.name` and its
  reservation. Agent topic annotations, provider, lifecycle status, and the
  managed Pane's name and raw title are independent and receive no tmux write.
- Rename/rebind commits the authoritative Registry transaction before its
  field-specific live projection. Immediate projection is enabled only inside
  tmux from an inherited absolute socket path; every inventory and write is
  routed through that exact `-S` socket, and Project/Window writes target stable
  `$N`/`@N` handles rather than mutable names or indices. Outside tmux no server
  is probed and the operation is Registry-only. If no exact UID target is
  observed, including when the exact inventory is unavailable, the resource is
  treated as offline and the durable drift is left for a later explicit-socket
  reconciliation. Once an exact target is found, a write failure or duplicate
  UID claim is nonzero and reports that Registry state committed plus
  `projmux reconcile resources` as the retry boundary.
- The configured `window.rename` action (`Ctrl-M` by default) is the runtime
  display path and invokes tmux `rename-window` directly. Reconciliation
  observes that `window_name` back into `metadata.displayName` without changing
  stable identity.
- Registry-managed Windows are set to `automatic-rename off` so a focused-Pane
  change cannot overwrite the Window name. The **global** `automatic-rename on`
  plus visible-pane-label `automatic-rename-format` default in the generated
  app config is unchanged, so unmanaged windows keep their existing behavior.
- Legacy import gives every newly discovered Window the literal `window` base,
  uniquified in Project scope (`window`, `window-1`, ...), and projects its
  current `window_name` into duplicate-allowed `metadata.displayName` before
  switching it to `automatic-rename off`. Re-observing an existing Window may
  refresh only that display field; it never changes the uid, stable name,
  ownerRef, or name reservation. An existing `@projmux_pane_label` remains the
  migration seed and transport mirror for the Pane **name**; Pane naming is a
  separate contract and is unchanged here.
- `Pane.metadata.name` is the primary pane display source. The derived
  `Pane.status.displayTitle` (Agent topic → known shell → raw pane title) is
  secondary and is never a selector, an identity, or a Window name source.

Runtime observation and resource status:

- **Status is an observation; spec is stored and authoritative.** A read verb
  never trusts a stored liveness value. `Window` and `Pane` status is derived
  per invocation from a live tmux snapshot: a resource is `live` only while a
  live tmux object still mirrors its `@projmux_window_uid` /
  `@projmux_pane_uid`. A registry object bound to nothing live is an **orphan**,
  and an orphan is not live.
- `selector.ObservedStatus(missingRoot, bound)` is the **single** derivation
  rule in the codebase, and every kind goes through it. `missing-root` outranks
  everything, then `bound` decides `live` vs `offline`. The MissingRoot
  precedence contract is unchanged, and it now applies to a Window or Pane whose
  owning Project lost its root even while tmux is still running them.
- A **Project** is the one kind whose runtime object is a tmux *session*, which
  has no `@projmux` uid of its own, so Project status still reads
  `status.session` as refreshed by the reconciler.
- An **Agent** owns no tmux object of its own — there is no `@projmux_agent_uid`
  and there must not be one, because an Agent outlives the managed Pane it is
  bound to. Its runtime object is **that managed Pane**, named by
  `status.paneRef`, and that is what is observed: an Agent is `live` only while
  a live tmux pane still mirrors the uid its `paneRef` points at. An empty
  `paneRef` — the state of every released, pending, or failed Agent — is
  `offline`, and `missing-root` still outranks both.
- Agent status is **not** inherited from the owning Window. It used to be, and
  that was the last surviving inheritance path: once one Window was adopted and
  went live, every Agent under it read `live` whether or not it had a pane, so
  `get agents` said `live` for a resource `describe agent` reported as `Offline`
  with no managed pane. The Window now contributes exactly one fact — whether
  the owning Project carries `MissingRoot` — and specifically not its liveness.
- `status.phase` is **not** an input to status either. Phase is lifecycle (a
  stored value, owned by the Agent liveness rules) and Status is observation;
  folding a stored value back into the observation is what the contract forbids.
  They cannot contradict anyway: every non-`Running` transition clears
  `paneRef`, so a non-`Running` Agent has no runtime object to observe.
- The observation is taken **at the command entrypoint, once per process
  invocation**, and costs exactly two reads: `list-panes -a` and
  `list-windows -a` (~3ms each). It is lazy, so a route that never renders
  status never pays for it. It is **not** cached and **not** persisted: closing
  a pane must make the *next* query report it offline, and a TTL would defeat
  exactly that. It is also not a per-route reconcile, because the read verbs
  load the registry read-only and must never materialize
  `<state>/projmux/metadata/`.
- A failed inventory query yields an **empty** observation, never a fallback to
  a stored value. Empty can only downgrade a resource to offline; it can never
  invent a live one, and "nothing is live" is the truthful answer for a machine
  whose tmux server is not up.
- The reconciler runs the same diff on the mutation routes and records **why** a
  runtime object went away as a `MissingRuntime` condition
  (`reason: RuntimeUnbound`) on the Window or Pane, with `firstObservedAt`
  preserved across repeat observations and cleared when the object rebinds.
  `describe` renders it. This is an inventory diff, not an event handler, so it
  **converges with no hook firing**: the pane-exit hooks only accelerate the
  read verbs, which never reconcile.
- **A vanished runtime never deletes, prunes, or re-identifies a resource, and
  never releases a name reservation** — the same preservation contract
  `MissingRoot` established for a Project whose root disappeared. There is no
  auto-prune.
- The inventory is a pure **read**. It never writes, re-mirrors, or adopts a uid
  onto a live tmux object; reattaching a lost binding belongs to the reconciler
  (see *Binding reapply and adoption* below). After a tmux server restart the
  objects survive but the options do not, so everything reads offline until the
  next mutation route reconciles.
- The observation shells out as bare `tmux`, like every other mirror read, so
  inside a client `$TMUX` selects the projmux socket. Introducing a second
  socket convention for this one query would let the observation disagree with
  the mirror writes it is diffed against.

Binding reapply and adoption:

- The `@projmux_*_uid` tmux options are the binding store, and they used to be
  written **once**, at legacy-session import time. A tmux server restart, an
  option reset, or a registry written before the mirror existed leaves live
  windows and panes carrying no uid at all, and nothing ever put one back. The
  measured symptom: `projmux delete pane` with no selector fails with *the
  active tmux pane carries no `@projmux_pane_uid`* in almost every pane on the
  machine, making the shipped "omit the selector, act on the active target"
  behavior unreachable.
- Reconcile now **reapplies** bindings. Every live window and pane that resolves
  to a Project gets its uid options written again, through the same
  `Mirror.MirrorWindow` / `Mirror.MirrorPane` path an imported object uses.
  There is one write convention, not a uid-only variant: a reattached object
  must end up configured exactly like an imported one.
- The old import guard (*skip a Project that already owns Windows*) is gone. It
  **avoided** duplicates instead of repairing drift, so once the registry and
  the machine disagreed no later pass could bring them back together. Each
  observed tmux window now resolves into one of four outcomes — **rebound**
  (still carries a uid the Project owns), **adopted** (blank, pairs with the
  next unbound registry Window), **created** (blank, no candidate left), or
  **refused**. Panes cascade the same way inside a window that was itself
  matched. A drifted registry converges in a single pass.
- **The matching key is structural, never content.** Two layers: the Project
  scope, then ordinal alignment inside it — the session's tmux windows in
  `window_index` order against that Project's registry Windows in creation
  order, and panes the same way inside an adopted Window. That is the alignment
  the import path already created and `mirrorImported` already maps back
  through; adoption restores it rather than inventing one. `window_name`,
  `@projmux_window_name`, `@projmux_pane_label`, pane cwd, basename, git origin,
  and inode are explicitly **not** matching keys. Names are worthless here: the
  registry carries the Window name `zsh` across nine different Projects.
- **Two ways a session resolves to a Project, and they are disjoint.**
  `@projmux_project_path` is the *import* key — it is what turns an unknown
  session into a Project, and it is written only at session creation, so a
  session older than it has none. For those, reconcile uses the
  Project↔session-name edge it already maintains: a Project's session name is
  `status.session.name` when set, otherwise the name `sessionNameFor` gives its
  root. That is used **forward only** — compute the expected name from the
  Project and compare. Parsing a session name back into a path would be the
  heuristic. The session-name path never creates a **Window**; only the anchored
  import path does.
- **An orphan live pane is registered, and that is the one thing the
  session-name path creates.** Adoption needs an existing registry Pane to adopt
  *into*, and a pane produced by a route that registers nothing — `projmux ai
  split` is the measured one — has none, so it stayed unbound forever and
  `projmux delete pane` with no selector kept refusing in the operator's own
  active pane. Reconcile therefore mints a **shell-role Pane owned by the
  already-paired Window** for every live pane inside it that matches nothing, and
  mirrors that Pane's uid back through the same `Mirror.MirrorPane` write. The
  Project boundary is structural rather than checked: the Window was paired
  earlier in the same walk and belongs to the single Project the session name
  resolved to, so there is no code path from a live pane to a Project it does not
  already sit under. A live *window* that matches nothing still creates nothing,
  and neither does a **refused** pane — a refusal means a real registry Pane sits
  on the other side of the ambiguity, so minting beside it would leave two Panes
  describing one tmux pane.
- The registered Pane is named from `FallbackPaneNameBase` (`pane`, `pane-1`, …)
  through the registry's own allocator, never from `pane_current_command`:
  `metadata.name` is not derived from a runtime attribute, and the command
  changes the moment the operator runs something else. The runtime reading goes
  to `status.displayTitle` instead. The mint itself never creates an Agent: it
  adds one Pane and stops. Linking that Pane to an Agent is the separate step
  below, which runs on the Pane the walk just settled on.
- **Nothing is ever re-identified.** Adoption changes no uid, merges no uid, and
  reassigns no uid; it only decides which registry object a live tmux object is
  the runtime of, and then writes that object's existing uid. Adopted objects
  are reported to the reconciler so their bindings get written, but they are not
  recorded as *created* in the transaction — rolling an operation back must not
  delete an object that predates it.
- **Everything ambiguous is refused, and a refusal writes nothing.** The session
  resolves to no Project, or to more than one. The live object carries a uid
  another Project — or a sibling Window — owns. A candidate is already the
  binding of a different live tmux object, so the walk moves on rather than
  stealing it. Two live objects claim one uid. The parent window was not
  matched, so none of its panes are considered.
- A uid the registry has **never heard of** is its own case. It is never
  adopted: pointing an existing registry object at it would be re-identification
  off a failed lookup. The anchored import path still *mints* a new object for
  it, because projmux itself produces unknown uids — a reconcile rolled back by
  a pre-create hook refusal has already written its allocated uids onto tmux,
  and tmux options are not transactional — and refusing outright would leave
  those windows permanently unmanageable. Minting changes no existing uid. The
  session-name repair path, which has no anchor, mints only the Pane case and
  skips the window.
- The "already bound elsewhere" set is one observation taken **before** the pass
  writes anything, shared by both binding steps, so "already bound" means "bound
  before we got here". The binding writes land before the runtime-observation
  step, or that step would stamp `MissingRuntime` on a Window this same pass
  just reattached.

Managed runtime binding convergence:

- Binding repair has two explicit mutation boundaries. A normal
  `projmux tmux apply --socket <name>` first completes config preflight and a
  successful `source-file`, then runs the existing registry reconciler against
  that same exact `tmux -L <name>` server. The app-generated config also owns
  synchronous `after-new-window` and `after-split-window` hooks. Each hook
  expands tmux's absolute `#{socket_path}` and passes it to hidden internal
  plumbing that routes every reconciler read and mirror write through
  `tmux -S <absolute-path>`. Neither path falls back to the default socket or
  inherited `$TMUX`.
- The lifecycle hooks are synchronous so a newly bindable Window or Pane has a
  registry binding before the creating tmux command returns and before the next
  implicit read can run. Mirror writes use `set-option` and `rename-window`, not
  creation commands, so they cannot recursively fire either creation hook.
- A canonical resource create already owns the registry transaction while it
  issues `new-window` or `split-window`. It therefore installs a private,
  session-scoped create lease before the mutation (`new-session -e` on first
  materialization), and the exact-socket hook inspects that lease using the
  expanded `#{session_id}`. Only a live lease defers the hook's registry entry;
  stale or malformed leases are cleared and normal synchronous convergence
  continues. The create path explicitly mirrors its objects, reconciles the
  resulting runtime again inside the same transaction, then ownership-checks
  and clears its lease after commit or rollback. This avoids lock reentry
  without weakening standalone lifecycle ordering.
- A tmux creation may return non-zero after the object exists when a later
  synchronous hook fails. Materialization retains the combined output, accepts
  a reported `@N` or `%N` only when it was absent from the before-inventory and
  present in the same target's after-inventory, mirrors its operation uid, and
  rolls it back in reverse order. Ambiguous handles and changed ownership fail
  closed: unrelated objects are preserved and residual drift is reported.
- `tmux apply --no-reload` stops before any live-server query, and config or
  keymap preflight failure does the same. A server on a second socket is never
  inventoried or mutated. `get`, `describe`, and implicit active-target
  resolution remain read-only and never invoke convergence or open a registry
  transaction.
- The existing `BindingMatcher`, `registryReconciler`, and metadata `Mirror`
  remain the only matcher, orchestrator, and tmux uid writer. Missing bindings
  take the existing complete mirror path; ambiguous and foreign objects keep
  the existing refusal rules. A resource already carrying its exact registry
  uid skips the mirror, and the convergent store suppresses an atomic registry
  replace when normalization finds no semantic change. Repeating apply or the
  lifecycle boundary therefore issues no `set-option`/`rename-window` writes
  and performs no registry byte write.
- This boundary adds no public command, option, environment variable, or
  registry schema. It does not add persistent Project scope, matching by name,
  cwd, or a new ordinal heuristic, uid merge/reassignment, pruning, or forced
  adoption. The Project scope remains derived from the active binding on read.

Public resource reconciliation:

- `projmux reconcile resources` exposes the same Registry matcher, mutator,
  reconciler, and tmux Mirror as a deliberate operator repair boundary. A
  shadow tmux runner delegates reads to one exact server, records mirror writes,
  and overlays only those recorded UID values for the reconciler's final
  observation. Planning therefore executes production convergence on a cloned
  Registry while writing zero Registry, tmux, or filesystem bytes.
- Plan item identity is stable by resource kind, live target or Registry scope,
  and action. Opaque UIDs allocated while planning are display details
  normalized to deterministic placeholders; they are not matching keys and do
  not obscure owner or target identity. Human and JSON output share the same
  sorted items and missing/stale/foreign/orphan vocabulary.
- Execute rebuilds the plan from the locked current Registry. Runtime
  observation is limited to the Registry Project graphs safely attributable to
  sessions on the selected socket; absence there never marks another socket's
  graph missing or releases its Agents. The desired
  Registry is validated and committed before any non-transactional tmux mirror
  write, keeping Registry identity authoritative and retryable if a later live
  step fails. After commit, every planned live write is guarded by re-reading
  its target's Project, Window, or Pane UID binding from the exact socket; all
  guards and planned before-values must still match before the first write. A
  recycled or raced handle therefore causes zero live writes.
- A Registry commit failure performs no tmux mutation. A partial tmux failure
  leaves the durable Registry identity in place, replans current drift, and
  reports completed stages, remaining items, and the exact retry command.
  Repeating after success plans no writes and does not replace `registry.json`.
- `--socket` is exact `-L`; absolute `--socket-path` and inherited `$TMUX` are
  exact `-S`. No-flag use outside tmux is rejected before planning. No default
  socket, fallback server, config reload, state-loss recovery, or heuristic UID
  merge exists on this route.
- Public repair is stricter than lifecycle compatibility convergence for
  foreign state. An unknown, duplicate, or wrong-owner live UID makes its
  session diagnostic-only so later ordinal rows cannot slide onto a different
  Registry object. Safe drift elsewhere may converge; the refused item remains
  explicit and nonzero. `get`, `describe`, and `doctor` never enter this path.
- A known, unique `@projmux_project_uid` is the authority for Project rebind
  drift: the old non-empty `@projmux_project_path` does not turn that session
  foreign. The planner emits only the path-option repair, guards it with the
  unchanged Project UID, and becomes a no-op after convergence. Unknown or
  duplicate Project UID claims remain refused.

Agent runtime linkage:

- Once a live tmux pane has settled on a registry Pane, reconcile decides which
  **Agent** that Pane is the managed Pane of. Without this step the registry had
  running agents with no Agent resource and Agent resources with no
  `status.paneRef`, so `get panes` printed an empty AGENT column for every row
  and `get agents` listed finished conversations while hiding running ones.
- **The evidence is authorship, not a command name.** `pane_current_command ==
  claude` says a process called claude is running; it is equally true of a pane
  the operator typed `claude` into by hand, and nothing here reads it. The
  evidence is `@projmux_ai_agent`, the pane option the AI routes write when
  *projmux itself* launches an agent into a pane. A pane without it gets no
  Agent — Phase 1's refuse rule, unchanged. The legacy import path already
  trusted exactly this option to mint an Agent on its create path; linkage makes
  the adopt and rebind paths agree with it.
- **Which Agent, in order.** (1) The Pane is already Agent-owned: that Agent is
  the answer and only `status.paneRef` is repaired. (2) An Agent in the same
  Window already records the same provider conversation in `status.sessionRef`
  as the pane carries in `@projmux_ai_session_id` / `@projmux_ai_thread_id` —
  an exact identifier equality on a value both sides got from the provider, with
  `provider` compared too so a Codex thread id is never equated with a Claude
  session id. (3) Otherwise a new Agent is minted, named through the registry's
  own allocator over the provider name base, with the observed topic going to the
  non-identifying `projmux.io/agent-topic` annotation.
- **Ambiguity mints rather than guesses.** Two Agents recording one conversation
  is legal registry state, so an ambiguous conversation match cannot be resolved
  to "the first one"; taking a binding that might belong to the other Agent is
  the mistake no later pass can undo, while an extra Agent is inert and visible.
  A candidate already claimed by this pass, or one whose `paneRef` is some other
  Pane, is not a candidate at all — one Agent is the runtime owner of at most one
  live pane. The candidate set is the paired Window's Agents and nothing else, so
  the Project boundary is structural here too.
- **A linked Pane is promoted, and that is the one rewrite of an existing
  resource.** Its `spec.role` becomes `agent` and its `ownerRef` moves from the
  Window to the Agent, with its name reservation following it into the Agent's
  scope. The uid does not change, the name does not change, and the Project and
  Window it sits under do not change — the Agent is owned by the very Window that
  owned the Pane. `ownerRef` is the single edge every other reader resolves
  Agent↔Pane through (the AGENT column, hook session-ref attribution, cascading
  delete), so expressing the link only in `status.paneRef` would create a second,
  disagreeing source of truth.
- `status.sessionRef` is **not** written here. The pane option is a live routing
  index and the durable conversation pointer belongs to hook ingest, which
  reaches the Agent on its own once the Pane is Agent-owned.
- **A promoted Pane joins the managed-Pane lifecycle**, which is a visible
  consequence: the dead-agent-pane sweep releases an Agent whose managed Pane
  died and removes that Pane row, where a Window-owned shell Pane was never in
  its reach. That is the existing managed-Pane contract applying to panes that
  genuinely are managed. The **Agent** is preserved either way — same uid, same
  name, same `sessionRef`, still resumable.
- Linkage is idempotent: the next pass finds the Pane already Agent-owned and
  reasserts nothing, so a reconciler that runs on every mutation route converges
  instead of accumulating Agents. It is tolerant like the writes around it — a
  link that cannot be made writes nothing, does not cost the pane the binding
  that already succeeded, and does not fail the operator's command.
- Reapply stays a **mutation-route** concern, like the rest of reconcile. Read
  verbs still `LoadReadOnly` and still never materialize the registry. A tmux
  server that is absent or erroring still fails closed with no error, and a
  binding write or an orphan registration that fails is skipped rather than
  escalated — the next pass sees the same drift and tries again. It is
  maintenance riding along inside somebody else's transaction: one pane it cannot
  register must not fail the `create` that happened to trigger it.

Selector and the implicit active target:

- A selector value is either `uid:<uid>` or a `metadata.name`. There is no
  bare-uid form; `displayName`, `spec.root`, and tmux `%N`/`@N`/`$N` handles are
  structurally unmatchable. `--project` is at-most-once and fixes the scope,
  `--window`/`--pane` repeat and union, `--selector key=value` repeats and ANDs,
  and how many targets a `<verb, kind>` pair accepts comes from one declared
  cardinality matrix rather than per-route rules.
- Inside tmux, an invocation of a **singular read or rename verb** that carries
  no selector at all resolves the **active tmux target**: `get pane`,
  `describe project|window|pane|agent`, `rename project|window|pane`, and
  `rebind project`. Any reference, scope flag, or label keeps the pre-existing
  singular-target meaning; `create` and the destructive routes are unaffected.
- Project is also the namespace-like default scope of the plural registry reads
  `get windows|panes|agents`. When `--project` is absent inside tmux, the active
  Window uid mirror and its registry owner chain derive a Project on every
  invocation. This narrows the Window universe only; it never chooses one
  Window, Pane, or Agent for the operator. `get projects` is above that scope,
  while notifications and snapshots belong to separate stores, so all three
  remain global.
- `--all-projects` is the explicit registry-wide escape for those three reads.
  It is deliberately different from destructive `delete --all`, whose existing
  whole-registry compatibility meaning is unchanged. A bare `--all` is not a
  read flag. Explicit `--project` keeps its prior result and cannot be combined
  with `--all-projects`.
- Outside tmux, an omitted Project scope keeps the historical whole-registry
  inventory. Inside tmux, a missing Window binding or broken Project owner chain
  is a usage refusal with zero stdout, never a silent global fallback. The
  selector engine's `windowScope` is the single choice point for explicit
  Project, active-derived default, or global scope, shared by Window, Pane, and
  Agent resolution.
- There is **no sentinel value token**. `current` and `active` pass
  `ValidateName`, so `--pane current` would shadow a resource that legitimately
  carries that name. Omission is the only spelling. If an explicit one is ever
  needed, `.` (reserved by `ValidateName`) and `@` (a forbidden name rune) are
  the two collision-free candidates; neither is claimed today.
- Being inside tmux is decided from `$TMUX_PANE` plus `$TMUX`, and that pane id
  is passed as an explicit `-t` target. It is deliberately **not** decided by
  whether a tmux command succeeds: a bare `display-message -p` from outside a
  client still answers, for the most-recently-used session, which would silently
  select a wrong target.
- Only two options are read: `@projmux_pane_uid` on the active pane and
  `@projmux_window_uid` on its window (window-scoped options resolve through a
  pane target). Every ancestor above them comes from `ownerRef` — the Project is
  the owner of the active Window, the Agent is the owner of the active Pane.
  The session-scoped `@projmux_project_uid` is **not** consulted: it is
  measurably empty on live sessions, so trusting it would refuse targets the
  owner chain resolves.
- There is no persistent, queryable scope and no `set-context` equivalent. The
  observation is re-read on every invocation, which costs one `display-message`
  and leaves nothing to go stale. `describe pane` with no selector is itself the
  preview of what the singular family will act on; the plural-read Project
  default consumes the same observation and owner chain without choosing an
  individual resource.
- An active target that maps onto no registry resource is a **refusal**, not a
  fallthrough: exit `2`, zero stdout, no resource selected, nothing written, and
  a message naming what was inspected. It is deliberately not the
  `matched N ..., want exactly one` cardinality error, because an unmanaged pane
  carrying no `@projmux_pane_uid` is the common case and presenting it as
  ambiguity would hide the cause.

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
session/window/path/git/clock row. Line 1 splits the notification bar
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
