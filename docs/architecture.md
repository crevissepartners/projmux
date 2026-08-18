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
- managed roots (scan roots for candidate discovery; never managed identity)
- default home-like roots
- preview preferences
- session naming exceptions
- ephemeral session retention defaults

## State model

Persistent state:
- pins (typed: managed Project uid, or unregistered candidate path)
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
- `internal/core/resourcegraph` is pure: the resolved resource graph that joins
  the Registry's desired topology to one exact tmux server, the typed
  session/window/pane inventory it is resolved against, the closed attribution
  and status vocabularies, and the transport descriptor. It performs no I/O.
- `internal/core/runtimediag` is pure: the read-only projection of one resolved
  graph's runtime half -- every observed tmux object with its attribution, its
  exact coordinate, and the Registry resource it is bound to, plus the scopes
  that could not be observed. It performs no I/O and re-derives no attribution.
- `internal/core/registryview` is pure: the primary navigation view model. It
  projects a resolved graph plus the caller's filesystem discovery onto the rows
  the Projects, Sessions, and Recent Windows surfaces list -- Registry resources
  in Registry order, discovered directories in their own section, and one Runtime
  link -- with a status overlay and the actions each resource state is eligible
  for. It performs no I/O.
- `internal/core/controller` is pure: the command-scoped controller kernel. It
  owns the closed intent x attribution authority table, the guard evidence, and
  the totally ordered plan every convergence producer is authorized through. It
  performs no I/O and holds no tmux dependency; the guard field spellings are
  supplied by the caller.
- `internal/integrations/metadata` owns the registry file (lock, atomic write,
  migration), the tmux transport mirror, and the bounded observation adapter that
  fills a `resourcegraph.Inventory` from one exact server.
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

Termination evidence transport:

- A managed Pane's `status.activation` names one **materialization** of that
  Pane, not the Pane. It carries an opaque `generation` minted per launch,
  resume, and topology materialization, the exact `%N` handle it landed on, the
  owning Agent uid for an Agent-managed Pane, and the operation id that issued
  it. The uid survives kill/recreate and resume; the generation does not, and
  that is what lets a receipt from a replaced process be recognized as stale
  instead of applied to the Pane that now holds the uid.
- Every managed launch execs `projmux internal supervise --pane-uid <uid>
  --generation <gen> [--agent-uid <uid>] -- <command>...`. The supervisor gives
  the child this pane's exact stdin/stdout/stderr -- the pty tmux allocated, not
  a pipe -- puts it in its own process group, and makes that group the
  terminal's foreground group, so job control works and
  `#{pane_current_command}` keeps naming the child. argv, cwd, and the
  environment are untouched. tmux-side signals aimed at the pane process are
  relayed to the child's group, because the pane pid and the child pid used to
  be the same process. The foreground handoff is attempted and retried without
  it rather than probed for: there is no portable way to ask "is fd 0 my
  controlling terminal" without an ioctl, and a start that fails forks no
  surviving child.
- A managed shell Pane -- one created with no command of its own -- is
  supervised over the process tmux itself would have started: `default-command`
  run by `default-shell` when it is set, and a **login** shell (argv[0] prefixed
  with `-`) when it is empty. Both values are read from the same exact server
  the pane is created on.
- `status.lastTermination` on the Pane, mirrored onto the owning Agent, is the
  minimal durable receipt: closed `source` and `classification` vocabularies,
  `observedAt`, the Pane uid, the optional Agent uid, the generation, and either
  an exit code or a signal name, plus the operation id. It carries no command
  text, no pane content, and no provider conversation data. Like
  `status.sessionRef` it is an optional pointer with `omitempty` and additive
  inside `schemaVersion: 1`.
- The classification vocabulary is four **kinds of proof**, not a severity
  ladder. `intentional` is a canonical control action's own written record and
  may only come from `source: control-action`. `normal` and `abnormal` mean a
  supervisor actually reaped the child: exit 0 and everything else,
  respectively. `unknown` is an explicitly evidence-free record. **Exit 0 is
  never promoted to intent**: a provider that exits because the operator quit
  and one that exits because it finished a batch produce byte-identical wait
  statuses.
- Receipts are applied under a generation guard: the Pane must still exist, the
  receipt's generation must be the Pane's current one, a receipt naming an Agent
  must name the Agent that owns the Pane and still binds it, a receipt the
  registry already stores verbatim is a no-op, and recorded intent is sticky for
  its generation. The last rule is load bearing -- a canonical delete records
  intent and then kills the pane, and the supervisor watching it reports the
  resulting signal; letting the observation win would turn every deliberate
  deletion into a crash report.
- Canonical `delete window|pane|agent` commits its intentional receipt in **its
  own transaction, before the first live mutation**. A failure to make that
  evidence durable aborts with zero tmux mutations. Every refusal after it
  withdraws the receipt again, scoped by the operation id so it can only remove
  what it wrote; a partial delete that really did kill something keeps the
  evidence that explains it.
- Nothing here consumes a receipt. Turning evidence into an Agent phase or a
  Pane release is a separate concern with its own review.
- The supervisor resolves its state paths from the pane's own inherited
  environment, which is the tmux **server's** environment rather than the
  environment of the CLI call that created the pane. That is the correct
  production binding -- the server is started from the operator's session -- and
  it is why an isolated test has to start its server with the same state root it
  reads the receipts back from.
- Losing a receipt is a supported outcome, not a failure mode. A supervisor
  killed with `SIGKILL`, a lost tmux server, an unwritable registry, and a pane
  whose supervisor could not be constructed all leave no receipt, and the pane
  behaves exactly as it did before supervision existed. An absent receipt is the
  input that resolves to `unknown`; it is never read as a normal exit.
- A Pane **adopted** from a runtime object created for another reason -- the
  first pane a `new-session` brings with it -- carries no generation until it is
  relaunched. Adoption is not supervision: the process was already running, so
  there is nothing to have launched it with.

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

Agent launch argv (workspace / task boundary):

- One Agent launch hands the provider CLI two independent things in a single
  argv: the **workspace** (`--cwd` and every `--add-dir` the create validated)
  and the **initial task payload** given after `--`. Where the workspace stops
  is a property of the provider's own parser, so the boundary is provider
  grammar data in `internal/app/agent_launch_argv.go`, not a concatenation at
  the call site. `create agent` and `agent resume` read that one grammar, so a
  provider's option arity cannot be spelled two ways.
- Claude's `--add-dir <directories...>` is **variadic**: it consumes every
  following operand until an option-looking token or `--` stops it. So every
  root travels in one occurrence and the payload is introduced by `--`. A
  payload appended straight after the roots is parsed as one more directory, and
  the session then starts with no task at all — an installed regression that is
  invisible in the argv and surfaces only as an unacknowledged activation.
- Codex's `-C <DIR>` and `--add-dir <DIR>` each take exactly one value, so roots
  repeat the option and no payload can be absorbed. Codex's argv is deliberately
  left byte-identical, which is also why a Codex prompt beginning with `-` is
  still read in option position, exactly as before.
- projmux gives additional roots only to Codex and Claude. A stored root for any
  other provider is refused at launch construction rather than translated into a
  flag this seam never validated, so an Agent never starts with access narrower
  than what it records.
- An empty payload contributes nothing, so the interactive create and the resume
  argv (where the provider's own conversation option, not a terminator, ends the
  variadic root option) are unchanged.

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
- A file that is absent, empty, or whitespace-only is the legitimate "no
  registry yet" case **only before the first successful write**; see the durable
  envelope below. Only a file with actual content and no usable `schemaVersion`
  is refused as unknown.
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

Durable recovery envelope:

- The registry is the source of truth for managed identity and desired topology,
  so `registry.json` is not the whole state: beside it the store keeps
  `registry.initialized`, the marker that records a completed write, and
  `recovery/`, a bounded set of the bytes replaced by semantic writes. The marker
  and every copy are 0600, `recovery/` is 0700 like the directory above it, and
  **no read creates any of them**.
- **First use versus state loss.** Before the first successful write there is no
  marker, and an absent, empty, or whitespace-only registry is the empty
  first-use registry — the zero-write read contract is unchanged, including the
  `LoadReadOnly` short-circuit that must not materialize
  `<state>/projmux/metadata/` for an operator who has never registered a
  resource. Once the marker exists, the same content-free registry is
  `ErrRegistryStateLost` on **every** route, reads and mutations alike. Answering
  an empty registry there would hide the loss of every uid, name reservation, and
  offline resource, and the next mutation would mint a second identity domain on
  top of it. A registry written before the marker existed is ordinary state, not
  a loss, and gains the marker on its next write.
- **Rolling recovery copies.** A same-version semantic write copies the bytes it
  is about to replace to `recovery/registry-<stamp>-<seq>.json` before the
  replace. Only *verified* bytes are copied: an absent, empty, or
  structurally invalid prior file yields no copy, and an invalid one does not
  block the write that repairs it. Retention keeps the newest five and removes
  the rest deterministically — names sort chronologically, and the sequence
  continues past the newest name rather than reusing one retention freed. A
  migration keeps its own versioned `.bak` instead, so it never spends a
  recovery slot on bytes that already have a backup.
- **A convergent no-op writes nothing at all.** `UpdateConvergent` on an
  unchanged registry takes no recovery copy, publishes no marker, and leaves the
  registry's bytes, mtime, and inode untouched. Convergence agreeing with stored
  state is not a reason to replace it.
- **Write sequence.** Stage into a temp file in the same directory → `fsync` it →
  re-read and validate it → copy the prior verified bytes → publish the marker if
  absent → `fsync` the directory → atomic `rename` → `fsync` the directory again.
  The live registry is only ever touched by that rename, and every step before it
  is undone on failure, so an injected or real failure at any step leaves the
  prior registry byte-identical with no staged file, no orphan copy, and no
  half-created marker. Directory `fsync` is best effort for filesystems that
  reject it (DrvFs and friends, the same ones that reject the permission repair),
  because losing the ability to write state there would be worse than losing the
  ordering guarantee.
- The marker is published **before** the rename so that its own failure cannot
  leave a replaced registry behind. The cost is a crash window of one rename: a
  hard crash between the marker and the very first registry rename leaves a
  marker with no registry, which reads as state loss rather than first use. That
  direction is deliberate — it asks the operator instead of silently starting
  over — and the diagnostic names the marker so it can be removed to accept an
  empty registry.
- **Distinct diagnostics.** Missing-after-initialization
  (`ErrRegistryStateLost`), malformed (`ErrMalformedRegistry`), too new
  (`ErrSchemaTooNew`), and unreadable (`ErrRegistryPermission`) stay four
  separate causes classified with `errors.Is`, because they ask for four
  different repairs. None of them creates an empty registry or a uid.
- **Restore is a separate operation.** Producing and bounding the copies is a
  property of a write; selecting one and putting it back is an operator decision,
  so it lives in the recovery boundary below rather than in the write path.

Registry recovery boundary (`projmux reconcile registry`):

- **Two operations with deliberately different powers.** Planning classifies the
  current registry and every bounded candidate and writes nothing at all — no
  lock, no permission repair, no directory creation, no tmux mutation. Restoring
  publishes exactly one source the operator named. There is no "just fix it"
  mode: which copy is the truth is a judgment about which mutations were wanted,
  and the command never makes it.
- **Classification, not authority.** A plan reports the current registry and each
  candidate as `valid`, `first-use`, `missing`, `empty`, `malformed`,
  `schema-too-new`, `invalid`, or `unreadable`, with a `sha256:` digest of the
  exact bytes, size, mtime, and the resource/reservation counts a verified
  envelope holds. Only `valid` is restorable. The same classifier runs at publish
  time, so a source is never previewed one way and validated another.
- **Fail-closed on the source.** Malformed JSON, an empty file, an envelope newer
  than this build, and a graph that decodes but holds a duplicate uid, a dangling
  `ownerRef`, or a broken name reservation are all refused. Restoring an
  unverified source would replace a known-damaged registry with an
  unknown-damaged one, and the second state is worse because it looks healthy.
- **Byte-semantic restore.** The verified bytes are published verbatim rather than
  re-encoded, so uids, owner relations, and name reservations are preserved
  exactly, a repeat restore is a byte comparison instead of a normalization
  argument, and an older-but-known schema stays readable through the existing safe
  read and migrates on the next semantic write.
- **The bytes being replaced are kept.** A restore copies the current registry to
  `recovery/replaced-<stamp>-<seq>.json` before replacing it, and unlike the
  write-side copy it keeps content that does **not** verify — that damaged
  registry is the only remaining evidence if the restore turns out to be the wrong
  call. Replaced copies are their own bounded family, so a restore never consumes
  the automatic write history and never grows without bound.
- **Race guards.** `--expect-source-checksum` and `--expect-current-checksum` tie
  a restore to the plan it was read from, and the preview prints the exact guarded
  command. Underneath, the source is re-read and re-verified under the store lock,
  the staged copy is re-validated, and both inputs are re-hashed immediately
  before the single rename. Anything that moved refuses with the registry
  byte-identical and tells the operator to re-run the preview.
- **A repeat restore is a byte no-op.** Bytes already equal to the source mean no
  rename, no preserved copy, and no marker write.
- **Restore establishes the boundary.** Restoring into a state directory with no
  marker publishes one, so a later loss on that machine reads as state loss rather
  than as a fresh first use.
- **The live tmux mirror is evidence, never a source.** When no verified copy
  exists, the plan reports what identity the *exact* server can still testify to —
  mirrored Project/Window/Pane uids, names, the Project root, and containment
  resolved from stable tmux ids — beside a fixed statement of what no mirror can
  return: offline resources, every Agent (no tmux option carries an Agent uid),
  an Agent-owned Pane's `ownerRef`, the name reservation table,
  `spec.primaryPaneRef`, and labels/annotations/timestamps/status. A pane carrying
  a provider option is counted as proof that an Agent existed whose own uid is
  nowhere on the server. Nothing is imported and no registry is generated:
  rebuilding from fragments would convert a visible loss into an invisible one.
- **No transport is a reason, not an error.** A restore is a filesystem
  operation, so planning works outside tmux; the mirror section simply reports
  that it has no exact target. The diagnostic is also skipped entirely when a
  verified copy exists or the registry is healthy, so it never answers a question
  nobody asked.

Resolved resource graph (`internal/core/resourcegraph`):

- **One join, consumed by everything.** The Registry is the source of truth for
  managed identity and logical desired topology; a runtime observation is a status
  overlay. `Resolve(registry, inventory)` produces the typed read model that the
  controller, the runtime diagnostics surface, and the primary UI all consume, so
  "is this Window live" and "may I mutate this pane" have one answer instead of
  one per call site.
- **Rows come from the Registry, objects come from the machine.** Every Registry
  row is emitted whatever the observation said, and every observed tmux object is
  named and classified even when projmux owns none of it. Neither direction can
  delete or invent the other's members.
- **Exact evidence only.** Attribution uses mirrored uids, the mirrored owner uid,
  the exact session role value, and the stable containment ids tmux itself
  reports. Session name, working directory, and running command are never
  ownership keys: a heuristic merge here would attach an operator's unrelated
  shell to a managed resource, and a wrong identity is worse than an unattributed
  object.
- **Closed attribution set.** `managed` is a Registry resource, or the object bound
  to one; `recoverable` mirrors a uid this Registry does not contain; `control` is
  an app-owned session carrying the exact `@projmux_session_role=control` marker;
  `ephemeral` is an auto-attach scratch session; `unattributed` has no mirrored
  identity but sits inside a managed enclosure or on a server projmux started;
  `foreign` has neither and belongs to the operator's own tmux; `conflict` is
  evidence that contradicts itself.
- **Contradiction refuses to bind.** One uid claimed by two live objects, a uid
  mirrored onto the wrong kind of object, and a claim whose live containment names
  a different owner than the Registry does are all recorded as conflicts with both
  tmux handles, and the row is never reported live and never handed a transport
  handle. Absent containment evidence is not a contradiction: a session that lost
  its Project option says nothing about ownership, so the object's own exact uid
  still binds. A binding that would cross a Project boundary is impossible by
  construction.
- **Status is derived, never stored.** `missing-root` outranks every runtime
  answer, a bound handle is `live`, a scope that could not be observed is
  `unknown` with a stated reason, and only a readable observation with no handle is
  `offline`. An empty or failed observation can only downgrade a row; it can never
  invent a live one. An Agent has no tmux object of its own, so its status is its
  current managed Pane's status and its phase is reported from the Registry
  verbatim.
- **Partial failure stays partial.** The host-ownership probe and the three list
  queries are independent scopes. A failed windows query leaves Window rows
  `unknown` while Pane rows keep their own observation, because a pane that is
  provably gone is still offline. A socket with no server behind it is different
  again: that is definite knowledge that nothing is live, so rows read `offline`
  and only host ownership is unavailable.
- **Both hosts, one identity.** `@projmux_app=1` on the server is the only proof
  of an app-owned host; anything else is a standalone host projmux is a guest on.
  The same Registry and the same objects produce identical managed rows under both,
  and a control-role marker on a server projmux does not own is refused, because
  any process can set an option on the operator's tmux.
- **Explicit transport or none.** An observation is routed through exactly one
  `-L <name>` or `-S <absolute path>`, resolved from the explicit socket flags
  first and the inherited `$TMUX` socket path second. There is no implicit
  default-server probe: with no transport the graph is a Registry-only snapshot
  whose runtime answers are all `unknown`, and a sibling socket is never read.
- **Bounded and pure.** One observation costs one option probe plus three list
  queries whatever the size of the server, is memoized for the invocation rather
  than cached with a TTL — closing a pane must make the *next* command report it
  offline — and issues no write verb. `Resolve` itself touches no filesystem, no
  process, and no tmux, so the same inputs always produce byte-identical output
  and a read can never materialize state.

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
- Explicit canonical deletion is the authority that retires that preserved
  desired topology. A non-implicit `delete window` target (an explicit
  reference or `--all`) accepts zero exact Window mirrors on its selected
  socket as a Registry-only cascade through the Window's Agents and Panes. One
  exact mirror is killed before the Registry commit; duplicate, foreign,
  stale-owner, inventory-failure, and plan-to-execution race states remain
  fail-closed. An implicit active Window is never treated as offline.
- **`delete window|pane|agent` names its server the same way `reconcile
  resources` does**: explicit `--socket <name>`, explicit `--socket-path
  <absolute>`, or the inherited absolute `$TMUX`, and outside tmux with no flag
  it refuses. There used to be a fourth branch -- a hardcoded `-L projmux` --
  which meant a delete issued against an isolated server inventoried one host
  and killed objects on another. Refusing is the only remaining honest answer,
  and it names the two flags that fix it.
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
  *into*, and a pane produced by the earlier non-resource direct Agent bridge
  has none, so it stayed unbound forever and
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
  `projmux config apply --socket <name>` first completes config preflight and a
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
- `config apply --no-reload` stops before any live-server query, and config or
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

Command-scoped controller kernel:

- One seam runs the whole sequence: observe one exact server, resolve it into a
  `resourcegraph.Graph`, plan, commit the Registry, guard tmux, execute, and
  reobserve. It is command-scoped and event-triggerable; there is no daemon.
- Authority is a closed table over intent x attribution plus one explicit grant,
  not a predicate. The grant is `OperatorTargeted`: this invocation names one
  exact server the operator chose. `reconcile resources` cannot run without such
  a target, and that selection -- nothing else -- is what makes an unmarked
  object on a host projmux does not own repairable. Without the grant `foreign`
  is refused, and with it every lifecycle intent still is.
  `start`, `import`, and `delete` are refused for every class, so an offline
  resource, Home, an ephemeral session, and an unattributed Pane cannot be
  created, adopted, or removed by convergence. Repair is allowed on `managed`
  and on `unattributed` -- an unmarked object inside projmux's own runtime world
  carries no competing identity, so restoring a Registry-owned mirror overwrites
  nobody. `recoverable`, `foreign`, and `conflict` are refused; `control` and
  `ephemeral` are observe-only. An unknown class fails closed.
- A planned write must also carry one of the two convergence verbs,
  `set-option` or `rename-window`. The verb gate is what makes "convergence
  never created or killed a runtime object" structural rather than a property of
  which candidates happen to exist today.
- The plan is totally ordered: registry surface before tmux surface, then
  outermost containment first, then by stable key. Containment order is load
  bearing -- a Pane uid written into a Window that does not yet carry its own uid
  is attributable to nothing, and the next pass reads it as a Pane outside its
  owner scope.
- Guards are exact evidence captured at observation time and re-proved
  immediately before the first live write, all or nothing: the server's own
  `#{socket_path}`, the target's mirrored uid, and the containing object's id.
  A stale guard aborts having written nothing and reports the exact retry.
- After a run that changed anything, the kernel replans against fresh bytes and
  reports whether a repeat would write. Convergence is observed, not assumed.
- Explicit topology materialization keeps its own engine and its own
  plan-time guard, because it plans against objects it is about to create, which
  no prior observation can have seen.

Runtime diagnostics escape hatch:

- A Registry-first surface is not an inventory, and that is the point of this
  one. The managed UI lists Registry resources, so an operator's own shell, the
  Home control session, a scratch session, and anything on a server projmux is a
  guest on are all correctly absent from it -- and "correctly absent" is
  indistinguishable from "lost" without a surface that shows the machine as it
  is. `projmux get runtime sessions|windows|panes` and the `projmux runtime
  diagnostics` picker are that surface.
- It is a projection of `resourcegraph`, not a second join. Every row comes from
  the resolved graph, which already decided attribution from exact uid, owner,
  and role evidence; nothing here re-derives a class and nothing here consults a
  session name, a working directory, or a running command. Every observed object
  is emitted, managed ones included, because a managed object that needs no
  repair is exactly the row an operator looks for when the managed UI shows it
  and the machine seems not to.
- Two handles per row, and they are not interchangeable. The stable tmux id is
  the only thing worth storing; the qualified coordinate -- a session name,
  `<session>:@N`, `<session>:@N.%N` -- is what an operator and the focus route
  address the object by. The session half of a coordinate degrades from the
  observed name to the `$N` id, and an object whose enclosing session cannot be
  resolved gets no coordinate at all rather than an unqualified handle the focus
  grammar would read as a session name.
- One exact host, and no transport is an answer. The routing is an explicit
  `--socket`/`--socket-path` or the inherited `$TMUX` socket path, never a
  default-server probe and never a second socket. Outside tmux the read succeeds
  and reports every scope unavailable with a stated reason, where `reconcile
  resources` refuses the same case because it is about to write.
- The whole surface is read-only. The Registry is opened without creating it,
  the observation is the bounded four-query adapter that owns no write verb, and
  the projection is pure, so a refresh is indistinguishable from not having run
  it. An empty item list next to a populated unavailability list is a different
  answer from an empty item list beside none.
- The picker's actions are forwards, not features. `focus` moves a client and
  never materializes, `attach project` is the outside-tmux Project entry point,
  and the Resource Inspector is read-only; each is offered only where it
  applies, and where it does not the row states why. There is deliberately no
  adopt, import, rename, or kill: a diagnostic surface that could adopt what it
  found would be the heuristic merge the resolved graph refuses, wearing a menu.
- `projmux runtime diagnostics` stays separate from `projmux runtime sessions`.
  That picker lists recent sessions to open one; this one lists every object on
  the server to explain what it is. Merging them would put an operator's own
  shell into the open-a-session list.

Registry-first primary navigation:

- The primary surfaces enumerate the Registry, not the machine. `internal/core/
  registryview` builds their rows from a resolved graph, so a Project is a row
  because the Registry contains it and not because a tmux session exists. The
  runtime contributes a status -- live, offline, missing-root, or unknown -- and
  an exact handle, and nothing else.
- Identity is the Registry's. Membership and order in `registryview` are the
  Registry's own slice order, which is insertion order, and the pure view model
  applies no preference of its own: the same Registry projects the same rows in
  the same order on an app-owned server, on a standalone server, and outside tmux
  entirely.
- Presentation order is the sidebar's, and only the sidebar's. The Projects list
  projects the managed rows onto three tiers -- pinned, then live, then closed --
  and preserves Registry order inside each tier as a stable tie-break. Pinned
  outranks live because a pin is a stated preference and liveness is an accident
  of the moment, so a pinned offline Project stays above an unpinned live one. The
  live tier is an overlay of one exact host, which makes two things contractual:
  the tier of a row may differ between hosts and between refreshes, and the
  selection may not follow a position. It follows the Project uid -- the old
  selection is resolved to its Project and that Project back to whatever row it
  renders as now -- so a tier change moves the row and not the resource the cursor
  is on. Nothing about a tier reaches the Registry: it is not stored, not
  reconciled, and not part of desired topology.
- Row identity is the resource uid. A managed Project's *selection* is still its
  `spec.root` so the shipped open flow is unchanged, except for a Project whose
  root is gone: that row carries `uid:<uid>` and selecting it opens the read-only
  resource surface, which is where rebind is stated. Before this, such a row
  failed the whole picker on directory validation.
- Filesystem discovery is kept and demoted. A discovered directory that no
  Project root claims is an unregistered bootstrap candidate in its own section;
  one that is already a Project root is dropped rather than listed twice with a
  second set of actions. Opening a candidate is the explicit gesture that
  registers it -- see the authority split below.
- Home is chrome, not a Project, and the two senses of "Home" stay separate. The
  Home *control session* is never a managed row: it is app control runtime with no
  `resourceRef`, the only evidence that a session is one is the exact
  `@projmux_session_role` value the graph reads, a session named `home` with no
  marker is honestly unattributed, and the marker's writer belongs to the
  control-session track. The Home *navigation row* is the operator's own root as
  filesystem discovery offers it, and it leads the Projects list because it is
  where the surface starts from rather than a member of what the surface orders.
  It is synthesized from nothing: it carries no managed identity, it is not a
  reconcile or create target, and if discovery does not offer `$HOME` there is no
  Home row.
- The Sessions and Recent Windows surfaces list managed rows only, attributed by
  tmux's own `$N` and `@N` ids rather than by a name join, and carry the Registry
  resource name beside the exact tmux handle their actions target. What they
  withhold is tallied by class on a Runtime link that forwards to the escape
  hatch above.
- Every action forwards to a route that already owns it: `focus` for a live row,
  `attach project` for an offline Project -- the one shipped route that
  materializes one -- and `agent resume` for an Agent. Rebind and delete are
  listed as eligible with the exact command that performs them rather than
  executed from a read surface.
- A navigation refresh is a read. It opens the Registry read-only, takes the
  bounded four-query observation through one exact socket, and projects it
  purely: no Registry or tmux write, no reconcile, no materialize, and no
  default-server probe when there is no transport.

Project discovery and pin authority:

Five things used to share two files, and each of them answered a different
question wrongly as a result. Workdirs were a scan source *and* the thing that
decided which Projects existed. The pin file was a presentation preference *and*
a discovery input *and* the only record that a directory mattered. They are five
separate authorities now, and the boundaries are the point.

- **Workdirs and project roots are scan roots.** `PROJMUX_MANAGED_ROOTS`,
  `PROJMUX_PROJDIR` and `~/.config/projmux/workdirs` name directories to look
  inside. Looking inside a directory registers nothing. On Windows they are
  OS-native paths and stay OS-native paths; nothing normalizes them into identity.
- **A discovered child is an unregistered candidate.** It is a filesystem fact
  with no uid, no name reservation, and no Registry row. It stays one until
  something explicitly registers it, however many times it is scanned, rendered,
  or reconciled.
- **The Registry is managed identity.** `projmux create project --root <path>` is
  the canonical bootstrap, and opening a candidate from the Projects sidebar
  performs the same registration for that one exact path. Both go through one
  transaction and both are idempotent: a root an existing Project already claims
  is answered from the Registry and writes nothing. Nothing else registers a
  Project. In particular the reconcile prelude no longer walks the discovery
  roots, so `create pane` in one repository cannot add a Project for every
  sibling directory under a scan root -- which is exactly what it used to do.
  `--project <name>` naming an unregistered candidate is a refusal that names the
  exact `--root` and the route that would register it.
- **A managed pin is a Registry Project uid.** Its displayed root and name are
  projected from the Registry on every render, so the pin survives a rebind, a
  rename, and a `MissingRoot` condition. The sidebar tier reads the uid, never the
  path.
- **A candidate pin is a path no Project claims.** It is a preference about a
  directory, kept as one. Rendering it, listing it, and pinning it never mint a
  Project.

Storage and migration:

- The pin file is a typed envelope: a `projmux-pins v2` header followed by
  `project <uid>` and `candidate <path>` lines. The kind is stored, not inferred,
  which is what lets one file hold both collections without either surface having
  to guess.
- Reading never writes. Every rendering surface projects a pre-v2 file in memory
  through the same resolution a migration would persist, so the sidebar is
  identical before and after `projmux pin project migrate`.
- Migration is per-line and atomic as a whole. A path exactly one Project's root
  claims becomes that uid; a path no Project claims stays a candidate; a path more
  than one Project claims refuses the entire migration with the pin file and the
  Registry byte-identical, and names the repair. A corrupt or newer-version
  envelope is refused rather than partially parsed, because a wrong guess about
  which resource a preference points at is worse than declining to load one.
- Path folding is confined to two questions: candidate exact-match, and legacy
  path-to-uid migration. `candidates.MatchKeyFor` resolves symlinks on every
  platform and additionally folds separator, case, and drive-letter case on
  Windows, so `C:\Users\dev\src` and `c:/users/dev/src` are one candidate. It is
  never an identity operation: no amount of path agreement mints a Project uid or
  merges two, and the Windows rules are frozen by a compatibility table that a
  Linux test run asserts.
- `pin project add|remove|toggle <dir>` keeps working unchanged and now resolves
  to a typed pin under one rule -- exactly one Project with that root makes the pin
  managed, none makes it a candidate, more than one is refused -- with
  `uid:<uid>` available when an operator wants to be explicit. Settings shows the
  three collections as three collections: Additional discovery roots, Pinned
  Projects, and Candidate Pins.

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
- Execute runs through the controller kernel. It rebuilds the plan from the
  locked current Registry and authorizes every runtime write against the graph
  resolved from the pre-lock observation. Runtime observation is limited to the
  Registry Project graphs safely attributable to sessions on the selected
  socket; absence there never marks another socket's graph missing or releases
  its Agents. The desired Registry is validated and committed before any
  non-transactional tmux mirror write, keeping Registry identity authoritative
  and retryable if a later live step fails. After commit, the socket identity
  and every planned write's uid and containment guards are re-proved from the
  exact socket; all of them must still match before the first write. A recycled,
  moved, or raced handle therefore causes zero live writes.
- The report is one projection consumed by both renderers. Alongside the sorted
  items it carries the observed host mode, the authority rows the run
  exercised -- including the start, import, and delete refusals that are the
  evidence nothing was activated or adopted -- and the post-execute
  reobservation.
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

Explicit Registry topology materialization:

- `reconcile resources --materialize-project <name|uid:uid>` selects exactly
  one Registry Project and uses a separate pure plan. The default reconciliation
  shadow never calls the materializer, and the materialization plan never runs
  blank adoption, orphan minting, or Agent phase observation. Registry insertion
  order determines session/Window/Window-owned shell Pane creation order; report
  keys provide a separately stable rendering order.
- Registry presence is desired topology. Missing runtime sessions, Windows, and
  Window-owned `role=shell` Panes are drift; canonical Registry deletion removes
  that desire. Exact uid/name/owner mirrors are retained. Stored Pane CWD drives
  only that Pane's detached runtime cwd, while Project root remains the session
  path anchor and `PROJMUX_CWD` hook value. `Pane.spec.command`, Agent-owned
  Panes, Agent providers, snapshots, notifications, and ephemeral sessions are
  never execution inputs.
- Preflight rejects a missing/invalid root or Pane CWD, a zero-Window Project,
  a primary ref that is not a direct Window-owned shell Pane, and foreign,
  duplicate, wrong-owner, or ambiguous live claims before the first create.
  Execute rechecks the same plan under the Registry lock. A server-wide uid
  preflight runs first, *before* the selected Project session is created,
  because creating it runs the public pre/post-create hooks whose side effects no
  rollback can undo; a missing server is read as an empty inventory. The
  inventory is then refreshed once the session exists, so the new tuple is
  covered and any race since the preflight is caught. Together, and before the
  first Window or Pane mutation, they prove that every planned live Window is
  owned by the selected Session, every planned live Pane is owned by its planned
  Window, and every planned Window/Pane uid is live on exactly the expected
  handle or nowhere at all. UID equality alone is insufficient: a
  relinked Window or a join-paned Pane keeps its uid, and a Window relinked out
  of the selected Session is invisible to a selected-Session plan. Each created
  Pane proves its own parent before its uid claim. It records only objects it
  creates -- including one that tmux mutated before reporting a synchronous
  hook failure -- and rolls those objects back in reverse order only while their
  exact uid mirror still proves ownership. External hook effects are outside
  that rollback guarantee. A successful repeat performs no session ensure,
  lease write, tmux create, Registry replace, or mirror write.
- Exact `--socket/-L` supports offline full materialization with the existing
  pre/post-create hook contract. Exact `--socket-path/-S` supports live partial
  Window/Pane repair. Offline session creation through arbitrary `-S` is a
  stable safety refusal: the public hook contract exposes name-only
  `PROJMUX_SOCKET`, so an absolute path cannot be represented without either
  silently routing hook re-entry to another server or changing that public
  contract. A future versioned hook socket-path contract can lift the refusal.
  Only the selected exact socket is claimed and mutated; sibling sockets are
  tested unchanged, and no global uniqueness across unknown sockets is claimed.

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

Resource-first create:

- **One parser, one product model.** `create window|pane|agent|<provider>` share
  a single argv surface and a single resource-backed implementation.
  `--project` is a scope flag, never a mode selector, so no flag chooses between
  two meanings of the same command. The runtime-only "split the current window"
  half that used to sit behind an absent `--project` is removed; a raw,
  unmanaged split is tmux's own verb, not a projmux resource verb.
- **Scope resolution has exactly two branches.** An explicit `--project`/`-p`
  wins inside and outside tmux and suppresses the active-target read entirely.
  With no `--project`, the Project is derived from the active exact runtime
  through the same `@projmux_window_uid` mirror and registry `ownerRef` chain
  the read verbs use.
- **Window and anchor follow the whole scope, not the Project flag.** They are
  derived only when the argv named no `--project`, `--window`, `--pane`, and no
  `--selector` at all. That keeps a bare `create pane --placement right` -- the
  generated keybinding body -- a split of the Window the operator is looking at,
  instead of a fan-out over every Window of the Project, while one explicit
  occurrence still fixes the whole target set. With a scope and no `--pane`, the
  anchor stays the target Window's stored `spec.primaryPaneRef`, and a missing
  or stale ref is a refusal rather than a silent repair.
- **Refusals cost nothing.** Home, control, unattributed, foreign, a mirrored
  uid the Registry does not hold, a Window whose Project is gone, and every
  outside-tmux invocation with no `--project` are usage errors naming
  `--project`. They are raised before the registry transaction opens, so they
  are measurably zero Registry writes and zero tmux calls. Nothing falls back to
  a runtime-only split, nothing invents a Project from `$HOME`, a session name,
  or a cwd, and no default server is probed.
- **Host neutrality is transport-level, not policy-level.** Inside an app-owned
  or a standalone server the create mutates only the inherited exact socket,
  because every tmux call it issues inherits `$TMUX` and it never enumerates
  siblings. Outside tmux an explicit Project is the gate before anything live is
  touched.
- **Everything is detached.** No create path issues `switch-client`,
  `select-window`, `select-pane`, or `attach-session`. `focus pane` and
  `-o pane-id` are how a caller ends up in the new pane.

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
  singular-target meaning; the destructive routes are unaffected. `create`
  reads the same seam under its own rule, described below.
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

See [notify-queue.md](notify-queue.md) for the full reference.

## Usage snapshots

`projmux agent usage` and `projmux internal status usage` share a single `Manager`
that walks two registered adapters (Claude, Codex) and persists the
result to `<state>/projmux/usage/snapshots.json` (or
`PROJMUX_USAGE_STATE_DIR`). The cache file is the authoritative source
for the HUD render path so the tmux status interval never blocks on a
network call.

- **Per-adapter throttle** — Claude reports a 5-minute hint via the
  `ThrottleHinter` interface; Codex falls through to the global
  `30s` floor used by `internal status usage`. `MaybeCollect` only invokes an
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
| session  | 0    | popup `projmux runtime sessions --ui=popup` | prefix+s s |
| pwd      | 0    | show pane_current_path in a display-only path popup | prefix+s p   |
| git      | 0    | popup `projmux switch --ui=popup`         | prefix+s g   |
| usage    | 1    | popup `projmux agent usage`               | prefix+s u   |
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
