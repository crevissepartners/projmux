# Operational Diagnostics and Privacy

Projmux records a small local-only operational journal so command failures and
state changes can be inspected after the originating process exits. It does
not upload the journal, contact an issue tracker, or provide a background
telemetry service. A support archive is created only by an explicit
`projmux diagnostics report` invocation and is never transmitted.

## Safe event contract

Each JSONL record has a closed schema: `at`, `level`, `component`, `event`,
`result`, `duration_ms`, `run_id`, `version`, `mux_backend`, and optional
allowlisted `command`, `subcommand`, `kind`, and sanitized `message`. There is
no generic metadata map. Runtime lifecycle records add only closed
`operation` and `code` enums. The allowed operations are session create,
attach, switch, kill, and config apply; codes are stable failure/health
classifications and never carry routing identity or subprocess details.

Command and subcommand names come from static allowlists. The allowlist covers
every route of the CLI route graph, `internal <namespace>` included, and its
direct child subcommand; a sweep test over the graph enforces this. Routes
covered only for this purpose gain error attribution: whether a success is
recorded still follows the state-changing rules below. An alias records the
canonical child name (`get project` records `get projects`). Unknown argv stays
unnamed, and argv values, paths, flags, and arguments are dropped. A log written
by a newer binary may carry names an older reader does not know; that reader
skips those lines. Messages have control/format
characters removed, whitespace normalized, the current home path abbreviated
to `~`, and length capped at 512 Unicode code points. Top-level outcomes never
copy `error.Error()` into the journal: their message is one of three stable,
lossy phrases (`command failed`, `invalid command usage`, or a classified
non-success status). Error `kind` is stored separately from that phrase.

The journal must never contain raw argv, stdin, prompts, notification bodies,
pane captures/output/title/topic/content, transcripts, raw hook payloads,
configuration secrets, or arbitrary environment values. Phase 0 also does not
add session/window/pane or other routing identifiers.

One explicit state-changing command owns at most one lifecycle pair. Its
`lifecycle.start` and `lifecycle.outcome` share the process `run_id`, and a
composite create-then-attach/switch flow keeps the first real mutation as its
operation instead of recording nested outcomes. Lifecycle ownership replaces
the generic top-level `command.outcome`; it never duplicates it. Start/outcome
append failures are ignored and do not change the command result.

The `lifecycle.outcome` of `operation=tmux.apply` (`config apply` and the
hidden `internal tmux apply`) also carries a step and Registry lock
breakdown. It is measurement only: the apply's steps, their order, its
stdout, stderr, exit status, rollback, and every lock boundary are exactly
as without it. The record is appended after the apply route returned, so
after every Registry lock it took was released, and a journal failure
changes nothing about the apply. Only closed names and integers are added;
every other event family, including `lifecycle.outcome` of any other
operation and `lifecycle.start`, rejects each of these fields.

- One `step_<name>_ms` field per apply step the apply entered, in order:
  `step_keymap_migration_ms` (keymap migration),
  `step_hook_file_migration_ms` (managed agent hook file migration),
  `step_retired_file_reclaim_ms` (retired snapshot, Codex generation, and
  sidebar startup file reclaim and the status bar default seed),
  `step_route_bind_ms` (binding to the exact live app server),
  `step_bell_hook_migration_ms` (managed tmux bell hook migration),
  `step_config_write_ms` (writing the generated config),
  `step_key_sequence_retire_ms` (retiring recorded key sequence state),
  `step_source_file_ms` (the route guard and `source-file`),
  `step_route_marker_ms` (the logical socket marker),
  `step_exhausted_replay_ms` (replaying retry-exhausted clean exits), and
  `step_converge_ms` (the controller convergence). A step the apply did not
  enter -- `--no-reload`, no live server, or an earlier failure -- is absent.
  Steps are measured on the same clock as `duration_ms`, start no earlier
  than it, and do not overlap; each opens where the previous one closes, so
  the few statements between two steps count toward the earlier one. Each is
  rounded down to whole milliseconds, and `sum(step ms) <= duration_ms`
  always holds on disk: a step set that would exceed it is dropped whole.
- `lock_acquisition_count`, `lock_wait_total_ms`, and `lock_held_total_ms`
  total every Registry lock acquisition the apply made while it ran, whatever
  site made it, with the wait and hold the Registry Store measured for
  `registry.lock.acquisition`. They are present whenever the breakdown is,
  and zero when the apply took no lock.
- `longest_lock_kind`, `longest_lock_step`, `longest_lock_wait_ms`, and
  `longest_lock_held_ms` describe the one released acquisition that held the
  lock longest. The kind is the Registry transaction it belonged to:
  `preexisting-dead-agent`, `control-targets`, `mirror-recovery`,
  `binding-converge`, `lifecycle-reconcile`, `session-lower`, or `other` for
  an acquisition no site named. The step is the apply step it ran in, or
  `other` outside every step. All four are absent when no acquisition held
  the lock, and the longest wait and hold never exceed the totals.
- `longest_lock_observe_ms`, `longest_lock_plan_ms`, `longest_lock_commit_ms`,
  and `longest_lock_store_write_ms` split that hold: live tmux reads and
  their classification, plan and reconcile computation, Registry and tmux
  writes inside the transaction, and from the transaction's callback
  returning to the release being observed (normalize, validate, the durable
  write, the unlock). A phase the transaction did not mark is absent, and the
  grant and the Store's locked read before the first mark are left
  unattributed. `sum(phase ms) <= longest_lock_held_ms` always holds on disk:
  a breakdown that would exceed the hold is dropped whole and the rest of the
  longest lock is kept.

Project lifecycle operator diagnostics also keep plans mutually exclusive:
`stop`, `close-window`, `delete-project`, and `fresh` are distinct operation
classes. Startup and unregister failures print the closed action, failing
stage, old Project UID, and new Project UID (or `-` when absent). These opaque
UIDs and stage labels are bounded control data; root paths, pane content,
history, prompts, and transcripts are never identity or
intent authority.

Automatic Window teardown decisions use `component=topology` and
`event=topology.teardown.decision`: one `info`/`success` record per decision
the `internal tmux converge` hook path consumes. An unpaired `window-unlinked`
records `retain` when its own hook first waits and again when its bounded pair
wait is exhausted, never for the carried retries between; its Window/Pane UID
is resolved read-only from the exact `$N/@N` handles and omitted when
ambiguous. The record adds only
a closed `decision` (`retain`, `delete-pane-agent`, `delete-window`, `refuse`),
a `code` from the closed `topology.teardown.<reason>` set that mirrors every
core teardown reason, an optional closed termination `classification`, and
optional opaque `window_uid` (`win-…`) and `pane_uid` (`pane-…`) Registry UIDs.
tmux `%N`/`@N`/`$N` handles, socket paths, session names, cwd, argv, and free
text are never recorded, and every other event family rejects these fields.

Create transactions use `component=create` and `event=create.outcome`: one
record per create transaction, written under the process `run_id`. It is
measurement only and changes nothing about the create itself. The kind of the
create is carried in the closed `operation` field: `window` (`create window`
and the UI new Window, including one whose answer is an Agent), `pane`
(`create pane`, the split UI's shell Pane, and the pane-menu split), `agent`
(`create agent` and the split UI's Agent Pane, including a resume-picker
pick, which creates a new Agent), or `resume` (`agent resume`, including the
resume `agent relaunch` runs). A successful transaction is `info`/`success`;
a failed one, including one that was rolled back, is `error`/`error` with
`kind=runtime`, so the support report's existing error-only projection
carries it. The record adds two timings and, inside the lock hold, their
breakdown:

- `duration_ms` runs from entering the create transaction to its return. It
  includes the runtime route bind, the wait for the Registry lock, the time
  the lock is held, and on failure the rollback, and in every case the
  create-operation lease clear. It excludes everything outside the
  transaction: process start and exit, argument parsing, scope and selector
  resolution, the Settings enabled-agents gate, the time a picker or prompt
  waits for the operator, the result line printed after the commit, and
  Agent activation observed after the transaction returns.
- `lock_held_ms` runs from entering the Registry mutation to the Registry
  update returning. It includes the create's guards, reconciliation, Registry
  and tmux mutations, the store's own validate and write, and the unlock. It
  excludes the wait for the lock and the Registry read before the mutation
  starts. It is absent when the transaction failed before it entered the
  mutation, and `0 <= lock_held_ms <= duration_ms` always holds.
- Six phase fields split `lock_held_ms` into the stages of the mutation, in
  order, each starting where the previous one ended:
  `phase_guard_ms` (the ownership guards' preflight against tmux),
  `phase_first_reconcile_ms` (the reconcile pass before the create's own
  writes), `phase_operation_ms` (the create itself: tmux splits and mirrors,
  the Registry edit, and for an Agent the supervised child spawn),
  `phase_second_reconcile_ms` (the reconcile pass after those writes),
  `phase_reprove_ms` (the re-proof of any route identity the transaction
  reused), and `phase_store_write_ms` (from the mutation callback returning
  to the Registry update returning: normalize, validate, the durable write,
  the unlock, and anything the store does after the unlock before it
  returns). A phase the transaction never reached, because an earlier stage
  failed, is absent; the store-write phase is present whenever the mutation
  was entered. The phases appear only with `lock_held_ms`, and
  `sum(phase ms) <= lock_held_ms` always holds on disk: a breakdown that
  would exceed the hold (a clock that went backwards) is dropped whole.
- `spawn_to_release_ms` is written for `agent` and `resume` only. It runs
  from the first supervised child spawn -- the split that starts the managed
  Pane's supervisor returning that Pane -- to the Registry update returning:
  how much of the child's own Registry lock budget the creator consumed
  before the child could take the lock. It is absent when no supervised
  child was spawned, and `0 <= spawn_to_release_ms <= lock_held_ms` always
  holds.

The record is appended after the transaction returns, so never while the
Registry lock is held, and a journal failure never changes the create's
result, exit status, stdout, or stderr. It does not replace the invocation's
`command.outcome`, and it does not count guard reads or time individual
guards. The generated Window rename also runs through the same transaction
and is not recorded, because it is not a create. Every event family other
than `create.outcome` rejects the `create` component, the six phase fields,
and `spawn_to_release_ms`; every family other than `create.outcome` and
`registry.lock.acquisition` rejects `lock_held_ms`.

Registry lock acquisitions use `component=registry` and
`event=registry.lock.acquisition`. Every acquisition of the Registry mutation
lock -- a Registry read, update, convergent update, schema migration, or
admission barrier -- is measured, and one record is written for an
acquisition that waited at least one second, held the lock at least one
second, or timed out; every other acquisition writes nothing. The record is
appended after the lock is released (or after the acquisition gave up), under
the invocation's `run_id`, and it is best effort: a journal failure, or any
failure in the recorder, never changes the Registry operation's result or
the bytes it wrote. When a create's own acquisition is recorded, that append
runs after the unlock but before the Registry update returns, so its cost
falls inside that create's `lock_held_ms` and `phase_store_write_ms`. The
record carries only:

- `command` and `subcommand`: the invocation's catalog command, classified
  like `command.outcome` and absent for an unclassified invocation. No argv
  value, prompt, flag, path, pid, or process name is recorded.
- `operation`: the Registry entry point that took the lock, one of `update`,
  `update-convergent`, `load`, `migrate`, or `admission-barrier`.
- `wait_ms`: from just before the acquisition to the grant, or to giving up.
- `lock_held_ms`: from the grant to just after the release; present only when
  the lock was held.
- `duration_ms`: always exactly `wait_ms + lock_held_ms`.
- `result`, `level`, `kind`, and `code`: a held and released lock whose work
  succeeded is `success` with no kind or code, at level `info`, or `warn` when
  it held the lock for five seconds or more. A held lock whose work failed
  (a refused callback, a failed validation or write) is `error`/`error`,
  `kind=runtime`, `code=registry.mutation.failed`. A deadline timeout is
  `error`/`error`, `kind=runtime`, `code=registry.lock.timeout`, and any other
  acquisition failure is `code=registry.lock.acquire-failed`; neither carries
  `lock_held_ms`.

`warn` is used by this event alone, and every other event family rejects
`wait_ms`, the `registry` component, and level `warn`. `diagnostics log
--level warn` selects these records; the support report's error-only
projection carries the `error` ones.

A Registry lock timeout error names the process the lock marker records as
holder, when that process is observed running, by the leading command words
of its `/proc/<pid>/cmdline`: the program's base name followed by the words
after it while they look like catalog command words (a lowercase letter, then
lowercase letters, digits, and hyphens), stopping at the first flag, `--`,
uid, path, number, or prompt, and at four words in all -- for example
`holder: pid 1234 (projmux create agent), running`. When the command line
cannot be read or yields no word, the kernel process name is used, and when
that cannot be read either, the command is reported as unavailable.

An accepted `agent message send` whose Claude source Agent's registered
Claude process is not an ancestor of the sender writes one `component=agent`,
`event=agent.message.foreign-source` `info`/`success` record. It adds only the
opaque source `agent_uid` (`agent-…`) and its `pane_uid` (`pane-…`); the
provider session id, process ids, and environment shown in the stderr warning
are never recorded, and every family other than the three `agent` events below
rejects `agent_uid` and the `agent` component. A journal failure never changes
the send.

The Claude messaging endpoint registration writes `component=agent`,
`event=agent.claude.registration` records. The chain is the SessionStart hook
`internal claude-endpoint-register`, which builds a bootstrap and starts the
detached helper `internal claude-endpoint-helper`; the helper claims and
records the registration in one Registry transaction and becomes Ready. Every
refusal along it, the helper's Ready, and the end of a Ready helper's serving
loop each write one record. A record carries only:

- `source`: the process that wrote it, `hook` or `helper`.
- `code`: `claude.registration.<reason>`, one reason of the closed table
  below. A reason the table does not list, or does not allow for that source,
  drops the record.
- `result`, `level`, and `kind`: a refusal before Ready is
  `error`/`error`, `kind=runtime`. `ready` and the `ended-*` reasons are
  `info`/`success` with no kind: the registration ran, and the `ended-*`
  reason says how its lifetime ended.
- `duration_ms`: from that process's route entry to the append.
- `agent_uid` (`agent-…`) and `pane_uid` (`pane-…`): only once the Agent and
  Pane matched the Registry, and omitted when not strictly shaped. The hook
  sets them from the provider process check onward, once its bootstrap matched
  the Pane and its Agent against the Registry, and for every helper start
  refusal. The helper sets them only after its producer check passed: the
  bootstrap then provably came from the live hook that made that Registry
  match, so its UIDs are the Registry-matched ones.

The provider session id, the registration nonce (`registrationGeneration`),
the messaging token and socket path, lease and coordination socket paths,
process ids and start identities, argv, error text, and the working directory
are never recorded.

| code (`claude.registration.…`) | source | where |
| --- | --- | --- |
| `hook-arguments-present` | hook | the hook route got arguments |
| `registry-path-invalid` | hook | the activation Registry path is set but not the exact shape |
| `registry-unreadable` | hook, helper | the hook's Registry read failed, or the helper's read after its claim |
| `hook-input-unreadable` | hook | stdin failed to read or exceeded 64 KiB |
| `payload-not-session-start` | hook | the payload is not a parsable `SessionStart` |
| `pane-binding-mismatch` | hook | the Pane, activation generation, or Claude process binding does not match |
| `agent-mismatch` | hook | the Pane's Agent is not the running Claude Agent that owns it |
| `provider-process-mismatch` | hook | the hook's parent is not the bound Claude process |
| `messaging-credential-invalid` | hook | the messaging socket or token is missing or malformed |
| `session-id-embeds-credential` | hook | the session id contains the token or socket |
| `messaging-socket-unavailable` | hook, helper | the messaging socket failed inspection |
| `nonce-unavailable` | hook | no registration nonce could be generated |
| `authority-invalid` | hook, helper | the registration authority is not valid |
| `hook-identity-unavailable` | hook | the hook's own process identity is unavailable |
| `reply-tool-policy-unavailable` | hook | the reply tool policy could not be captured |
| `helper-executable-unavailable` | hook | the running binary could not be located |
| `helper-bootstrap-unavailable` | hook | the bootstrap could not be encoded |
| `helper-ack-pipe-unavailable` | hook | the acknowledgement pipe could not be created |
| `helper-start-failed` | hook | the helper process did not start |
| `helper-admission-unconfirmed` | hook | no acknowledgement arrived before the 3s deadline or EOF |
| `helper-arguments-invalid` | helper | the helper got arguments or inherited a messaging credential variable |
| `helper-ack-missing` | helper | fd 3 is absent |
| `helper-ack-not-pipe` | helper | fd 3 is not a pipe |
| `helper-input-unreadable` | helper | stdin failed to read, exceeded 64 KiB, or is not a bootstrap |
| `producer-mismatch` | helper | the parent is not the hook that built the bootstrap |
| `bootstrap-invalid` | helper | the bootstrap Registry path or token is invalid |
| `helper-identity-unavailable` | helper | the helper's own process identity is unavailable |
| `lease-unavailable` | helper | the private lease directory, socket, or its mode could not be set up |
| `coordination-unavailable` | helper | the coordination listener could not be opened |
| `lease-owner-unavailable` | helper | the lease owner receipt could not be written |
| `provider-process-gone` | helper | in the transaction, the Claude process is gone |
| `claim-refused-activation` | helper | in the transaction, the activation is no longer this helper's claim target |
| `claim-refused-competing` | helper | in the transaction, the same registration generation carries another session or lease |
| `claim-refused-newer` | helper | in the transaction, a newer SessionStart claimed another generation |
| `lock-timeout` | helper | the transaction gave up on the Registry lock deadline |
| `lock-acquire-failed` | helper | the transaction never ran for any other Store reason (the one fallback, below) |
| `registry-degraded` | helper | the Store refused the transaction on a degraded Registry |
| `registry-write-failed` | helper | the transaction's validation or durable write failed |
| `route-mismatch` | helper | the recorded route does not resolve to this registration |
| `dialogue-broker-unavailable` | helper | the dialogue broker could not start |
| `reply-tool-gate-unavailable` | helper | the reply tool gate could not be built |
| `stale-before-ack` | helper | the registration stopped being current before the acknowledgement |
| `ready` | helper | the acknowledgement byte was written |
| `ended-context-done` | helper | the serving loop's context ended |
| `ended-not-current` | helper | the serving loop found the registration no longer current |
| `ended-accept-failed` | helper | the lease listener failed |

A Claude session projmux did not launch writes nothing. The user-wide
SessionStart hook still runs for it, but with no activation Registry path set
at all (`PMX_INTERNAL_CLAUDE_REGISTRY_PATH` empty or unset) it is outside any
managed activation: the hook stops with the closed reason `unmanaged-session`,
which the recorder drops and every reader rejects, so it is never journaled
at any level. A set but malformed path is still `registry-path-invalid`. A
nested unmanaged Claude that inherited a managed Pane's activation environment
is refused as `provider-process-mismatch`, because its parent is not the bound
Claude process; that record names the managed Pane's `agent_uid` and
`pane_uid`, whose activation environment it inherited.

`lock-acquire-failed` is the one fallback of the transaction's
classification: the claim callback never ran, and the Store error is neither
a lock timeout nor a degraded Registry. That is a failed lock acquisition, or
a locked Registry read or recovery inspection failure the Store did not
classify as degraded. A failure after the callback ran is always
`registry-write-failed`.

The hook writes at most one record, and only once its attempt is over: at a
refusal before it started the helper, or after its acknowledgement wait
returned, which is `helper-admission-unconfirmed` or a start refusal. It never
appends between starting the helper and the helper's producer check, and a
confirmed admission writes nothing from the hook because the helper writes
`ready`. The helper writes `ready` exactly once, right after the
acknowledgement byte, and one more record when it returns: the refusal that
stopped it before Ready, or the `ended-*` reason after it. That last record is
appended only after the helper closed fd 3, so the hook's EOF never waits on
the journal. Appends are best effort: a failing or slow journal never changes
the hook's empty output, its nil result, or the helper's argv, environment,
and stdin. Every other event family rejects the `hook` and `helper` sources
and every `claude.registration.*` code.

A Ready helper that refuses a push before its durable handoff, because one end
of the envelope route can no longer be proved, writes one `component=agent`,
`event=agent.message.claude-handoff-route` `error`/`error` record,
`kind=runtime`. A process Claude target's helper proves the route before its
busy reservation, which precedes the handoff, and writes the same record when
that proof fails. The sender's receipt keeps `broker-handoff-persist-failed`
(`provider-prewrite-refused` for that reservation proof), the known zero-write
reason every sender version reads; a new receipt reason
would read as an invalid helper response on an older sender and turn a known
zero write into an outcome-unknown failure. The record carries only:

- `code`: `claude.handoff.source-route-unproven` or
  `claude.handoff.target-route-unproven`, the end that failed.
- `source`: that Agent's host and provider as the Registry records them, one of
  `tmux-claude`, `tmux-codex`, `process-claude`, `process-codex`, or
  `unknown` when the Registry has no such Agent. The envelope's claimed
  provider never chooses it.
- `agent_uid` (`agent-…`): the unproved Agent, omitted when not strictly
  shaped.

Message refs, payloads, provider sessions, process identities, sockets, and
error text are never recorded. Like the registration records, it is best
effort and never changes the receipt.

Both routes are classified as the internal-only commands
`claude-endpoint-register` and `claude-endpoint-helper`, so the helper's slow
`registry.lock.acquisition` records carry `command=claude-endpoint-helper`.
Neither is state-changing and both always return nil, so neither writes a
`command.outcome`.

projmux no longer emits `session-state.outcome` records. Project snapshots
were removed, and the retained `internal tmux autosave-session-state` route is
a no-op that writes nothing. Records written by older versions keep their
closed vocabulary: the `session-state.outcome` event, the
`session-state.save`, `session-state.autosave`, `session-state.restore`, and
`session-state.delete` operations with their `.failed` codes, the sources
`manual`, `settings-latest`, `settings-named`, `autosave`, `startup-latest`,
`startup-named`, and `prune`, and the aggregate `window_count`, `pane_count`,
`shell_recipe_count`, `agent_recipe_count`, `startup_recipe_count`, and
`item_count` fields still validate and parse when the journal is read.

Notify and focus transitions use the same process `run_id` and add only closed
`transition`, `disposition`, `provider`, `category`, and `route` enums. Notify
transitions are `enqueue` and `delivery`; focus uses `request`. Enqueue records
distinguish queued, stable-ID deduplicated, and failed outcomes. Delivery
records distinguish delivered, dedupe/visibility/setting suppression, and
failed outcomes across the external sender hook, WSL Toast and fallback, and
Linux `notify-send` routes. Focus request records distinguish focused,
notify-only, session-only, window-only, and failed outcomes. Failure codes are
closed stage classifications and messages stay empty.

Provider and category values are projected through fixed allowlists. Unknown
values become `other`; arbitrary provider payloads and notification metadata
cannot extend the event schema. Notification summary/body, tag/group, terminal
title/topic, paths, queue or routing IDs, UUIDs, and AI/conversation/session
identifiers are never recorded. The notifier sender owns one terminal delivery
outcome after fallback selection, so failed intermediate WSL adapters do not
produce duplicate records when a later route succeeds.

One process writes at most one copy of an identical safe notify/focus tuple.
This fixes repeated stable-ID queue replacement, desktop dedupe, visible-pane
suppression, and reconcile hot paths to a finite per-run volume; the journal's
existing size/retention cap remains the cross-process bound. Explicit `notify
push` and `focus` outcomes logically replace the generic top-level
`command.outcome`, including when append fails. Secondary automatic enqueue or
delivery events do not claim an unrelated outer command. A successful focus
that switches a tmux client may coexist with the shipped runtime
`session.switch` lifecycle pair under the same run ID: the pair describes the
tmux mutation, while `focus.transition` describes the request-level result.

AI watcher and hook-ingest diagnostics use the same process `run_id` and add
only closed `provider`, `ai_kind`, `ai_result`, and `failure` enums. The event
families are `ai.watcher.transition` and `ai.ingest.outcome`. Watcher provider
is the generic `ai`; ingest providers are `codex`, `claude`, `antigravity`, or
`tmux-bell`. Each provider accepts only its own closed semantic-kind catalog;
for example, `tmux-bell` can only emit `bell`, while watcher events can only use
the generic `ai` provider and `watcher` kind. Provider event names are projected
into semantic kinds such as `prompt`, `permission`, `stop`, `notification`,
`tool`, `session`, `compact`, `subagent`, `teammate`, `invocation`,
`lifecycle`, `bell`, `payload`, or `unknown`. `statusline` is still accepted
when reading older records but is no longer written. A raw or future event name can
therefore be diagnosed as `unknown` but can never extend the journal schema.

One watcher process emits at most one `started` transition, one terminal
`pane-gone` or `hook-active` transition, and one copy of each distinct safe
failure tuple. The existing observable launch seam uses only
`watcher-launch-failed`; status application remains the pre-existing
best-effort operation and does not claim to expose swallowed tmux write errors.
The terminal watcher event logically replaces its generic top-level outcome,
including when append fails.
The polling loop does not record snapshots, observed titles, captures, pane
state, or a record per iteration.

Hook ingest projects only anomalies: invalid/read/oversized payloads, unmatched
or invalid targets, unsupported event classification, and terminal route
failure. Route failures are limited to the observable bell queue/store and
Antigravity explicit-response seams. Identical safe anomaly tuples are
coalesced per process. Successful state, notification, quiet, and bell-dedupe
traffic emits zero common AI events. Notify enqueue/delivery remains owned by
the Phase 4 notify recorder, so ingest does not add a second AI success outcome
or claim a secondary notify outcome. An ingest failure owns the top-level error
logically before its best-effort append, preventing a duplicate generic
`command.outcome`.

The common AI event never contains the raw hook payload or event name, prompt,
transcript, tool name/input/output, notification summary/body, pane content,
cwd/path/command/title/topic, tmux target, queue ID, provider conversation or
session identifier, UUID, or arbitrary reason/error string. `failure` is a
stage enum, not `error.Error()`.

Resource attribution diagnostics use `component=resource` and the single
`resource.sampler.outcome` family. The Resource Inspector lifecycle is the
only writer: its Linux collector owns tmux inventory, project-root discovery,
and procfs attribution, while its refresh gate owns derived staleness. The
closed `source` values are `sampler`, `tmux-inventory`, `project-discovery`,
and `refresh`; the closed `resource_result` values are `unavailable`,
`partial`, `stale`, `error`, and `scan-budget-exceeded`. `failure` is limited
to the matching safe stage enum. Impossible source/result/failure combinations
are rejected.

The lifecycle's existing overlap and trigger-drop counters remain UI-only
refresh feedback and do not create an operational event by themselves. When
the retained last-complete sample crosses the stale boundary, the refresh
owner records the single coalesced `stale` transition instead.

Normal warming/ready samples, periodic samples, successful automatic/manual
refreshes, and the separate host-only `projmux internal status resources` sampler emit
zero common resource events. The two-second Resource Inspector lifecycle budget
measures inventory, discovery, and procfs collection together. Tmux inventory
and procfs observe its context directly. Project discovery does not currently
accept a context, so an overrun is classified as budget-exceeded when discovery
returns rather than being preempted; making that seam cancellable is a separate
follow-up. Budget exhaustion preserves the established unavailable UI/CLI
result and adds only the safe budget tuple. A persistent identical anomaly
emits once. Recovery is silent and resets that transition, so a later re-entry
emits once again. Journal append failure does not affect snapshot selection,
popup refresh, stdout/stderr, or exit status.

No resource event contains CPU or memory values, PID/process counts, process
command/cwd/title, project/session/window/pane identifiers, socket/TTY,
attribution details, snapshot status text, arbitrary errors, paths, UUIDs, or
privacy seeds. Partial and stale outcomes are info-level and remain in the
private local journal only. Unavailable, collection/inventory/discovery error,
and scan-budget outcomes are error-level, so the explicit support report may
include their closed enums after hashing run/version correlation. The report's
existing error-only projection omits partial/stale and never exports metrics or
identity.

### Resource drift diagnosis and repair

`projmux doctor` remains a read-only health report. Registry/tmux identity drift
is diagnosed and, only when explicitly requested, repaired with
`projmux reconcile resources`. Its human and `-o json` result are the operation
record: exact socket target, deterministic missing/stale/foreign/orphan items,
changed/no-op/failed counts, completed stages, remaining drift, and an exact
retry command after partial failure.

`--dry-run` performs no Registry, tmux, or filesystem write. Execute commits
Registry authority before live mirror writes, prevalidates exact targets, and
never guesses or falls back to another socket. A failed Registry commit exposes
no allocated UID to tmux; a live-write failure keeps the durable identity
retryable and reports what completed. The report omits Registry source bytes,
prompt/credential data, and private hook payloads. No `config apply` or
unrelated configuration reload is part of diagnosis or repair.

The diagnostics package exposes a typed `ReadRuntimeHealth` projection for
read-only Doctor consumers. It reports the fixed `tmux` backend, latest
socket/apply state, and a bounded tail/count of safe failures using only
`Store.ReadOnly`; it does not create, chmod, lock, truncate, apply, restart, or
repair anything. Doctor schema 2 consumes that seam for its `logs` findings
and adds one fixed-argv, one-second `tmux -L projmux show-options` probe for
actual socket/config health. The probe neither generates nor applies config.
Its captured output is capped at 4 KiB. Doctor reads only a pre-existing
regular generated config (at most 1 MiB) without following symlinks, and the
shared read-only journal seam rejects non-regular inputs and files above 5 MiB.
These conditions degrade to typed findings rather than blocking or repairing
the source. The `privacy-unverified` finding remains in the schema for a path
whose privacy `os.FileMode` cannot prove, but no supported platform emits it:
Linux and macOS are the only build targets and POSIX mode bits are
authoritative on both. Doctor does not modify permissions.

### Registry materialization invariant audit

`projmux doctor --section registry` reports the admission difference between
what the Registry writer accepts and what activation can rebuild.
`Registry.Validate` and the materialization planner now share the final-v2
Window contract. Validation allows a same-Window managed Agent
`spec.anchorPaneRef` with an empty optional `spec.defaultShellPaneRef`; the
materializer classifies that shape as convergent and plans an explicit
`allocate default shell` stage before Window/Agent activation. A repeated
successful materialization is write-free. The audit remains useful for damaged
or stale refs, but an intentional offline Agent-only Window is not a finding.

The verdict is the consumer predicate itself, not a maintained list of suspect
shapes. Every Registry Project is planned through the shipped topology planner
with no observed sessions, and the refusals that offline plan records *are* the
difference. Nothing in the audit re-decides which stored topology can be
materialized, so the section cannot drift away from the route it describes.

The section is read-only in the strongest available sense. The Registry read is
the zero-write snapshot read, so running diagnostics on a machine that never
created a Project neither creates nor repairs the state directory, and the tmux
runner handed to the planner refuses every call instead of reaching a server.
The audit never writes, repairs, or migrates a Registry; repairing a stored
topology the materializer cannot build is a separate, explicitly requested
operation.

Findings use the shared severity/code/remediation/count shape:

| Code | Severity | Meaning |
| --- | --- | --- |
| `registry.materialize.audited` | info | `count` is the number of Projects planned. Always emitted. |
| `registry.materialize.clean` | info | The difference set is empty. Emitted explicitly, and printed without `--verbose`, because a silent clean audit is indistinguishable from a section that never ran. |
| `registry.materialize.unavailable` | warning | The Registry could not be read; nothing was planned. |
| `registry.materialize.fatal.<kind>` | error | `count` stored resources of that kind are refused in a way that stops the whole Project from activating. |
| `registry.materialize.skipped.<kind>` | warning | `count` stored resources of that kind are refused as single items; the Project still opens without them. |

`<kind>` is one of `project`, `window`, `pane`, `agent`, or `other`.
`fatal` and `skipped` are read off the planner's own refusal split rather than
re-decided here.

The refusal reasons are the planner's own wording and are rendered only under
`--verbose`, following the report's rule that path-bearing detail is opt-in: a
stale-cwd reason quotes a stored absolute path. Those reasons are never
serialized in any format. The support report therefore carries the same codes,
kinds, and counts as the text report and no reason wording at all, rather than
relying on the redaction allowlist to hash a private path out of an archive.

### Codex broker refusals

Every typed refusal a projmux process receives from the Codex endpoint broker
writes one `component=codex-broker`, `event=codex.broker.refusal`
`info`/`success` record, under that process's `run_id`. A refusal is recorded
each time it is received, including one the caller absorbs by retrying: an
exact-turn delivery that waits out the one-second `lifecycle-retry` window
records the refused read and, if it is refused again, the second one. The
record states that a refusal was received, not that the command failed --
the command keeps its own outcome record -- so it never enters Doctor's
recent errors or the support report's error tail. It carries only:

- `source`: the receiving process, `observer` (the managed Codex lifecycle
  observer, `internal agent-hook ingest codex-broker-watch`) or `probe`
  (`internal codex-broker probe`).
- `operation`: the refused broker operation, `ensure` (reach the runtime:
  discovery, dial, handshake, and starting one when allowed) or `bind` for
  either source; `lifecycle-read`, `turn-start`, `turn-steer`,
  `turn-interrupt`, or `approval-answer` for the observer alone.
- `code`: the broker's own closed refusal token, unprefixed and spelled as
  the CLI prints it, for example `host-unavailable`, `drain-required`, or
  `lifecycle-retry`. A token the broker does not declare drops the record.
- `dial_stage`: `discovery`, `dial`, or `handshake`, only on an `ensure`
  refused while dialing; absent when the refusal came from anywhere else.

Only a typed broker refusal is recorded: a context deadline, a local
identity or fence check, or a revocation read off a closed binding's stream
records nothing. The thread id, Agent and Pane, the state domain and socket
paths, the runtime id, and the wrapped transport cause are never recorded.
Doctor, the support report, and `install-replace` (which keeps its own
`install-replacement.json`) write no such record, and the broker process
never writes one for the refusals it sends. Appends are best effort: a
failing journal never changes the refused operation, the response, the
printed error, or the exit code. Every other event family rejects the
`codex-broker` component and `dial_stage`.

Count refusals by reason:

```sh
jq -rR 'fromjson? | select(.event == "codex.broker.refusal") | .code' "$(projmux diagnostics log --path)" | sort | uniq -c
```

Replace `.code` with `"\(.source) \(.operation) \(.code)"` to split the
counts by receiving process and operation.

## Storage and retention

The path is
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/logs/operations.jsonl`.
On POSIX systems the `projmux` state and `logs` directories are private
(`0700`) and the journal is private (`0600`); accesses make a best-effort
repair of older permissive modes.

Append and trim share an OS-owned advisory inter-process lock. The kernel
releases ownership when a process exits, so an orphaned lock path needs no
path deletion or stale-owner reclamation and cannot race a successor owner.
Lock acquisition has an explicit 200 ms total budget so this side channel
cannot materially delay the original command result. When the file exceeds
5 MiB, an atomic `rename` replacement retains approximately the newest 2 MiB,
beginning at a complete valid record. A trailing partial record is discarded
before the next append, and the reader skips malformed or truncated records.

Classification is intentionally conservative for mutation-capable interactive
commands: opening session/project/settings/popup flows is treated as changing
even when a user cancels. Explicit read variants (`internal status`, `list`,
`get`, config rendering, plain welcome, and the diagnostics viewer) remain
read-only. Read-only classification governs `command.outcome` only: a
read-only command whose locked Registry read waited for or held the lock for a
second or more still writes its `registry.lock.acquisition` measurement, except
Doctor, the support report, and the retired no-write argv, which never append.
The successful automatic hook/poll paths `internal agent-hook ingest`, `attention
arm`, `attention clear`, `attention window`, `internal tmux autosave-session-state`, and
`window record` are also read-only so high-frequency operation does not append
to the journal; an error from any of them still records exactly one safe error
outcome. Explicit user mutations such as `attention toggle` retain their
state-changing success record. Direct top-level help and explicit preview-only intents (`update
apply --dry-run` and AI integration dry-runs) are also read-only. Doctor is a stricter
boundary: successes and errors never append to this journal, so diagnostics do
not make its filesystem contract self-defeating. Support report success and
errors likewise never append; its strict reader shares the viewer's tolerant
decoder but never creates/locks/chmods/repairs/truncates the source journal.
Multi-mode commands such as AI status/topic,
terminal apply, update check, and welcome popup inspect only
allowlisted mode/flag names; boolean `=false` values retain mutation-capable
classification, and no flag values are ever recorded. Help-looking tokens
after the direct command position stay conservatively mutation-capable because
they may be values rather than help intent.

Failures to resolve the path, create/repair permissions, lock, append, or trim
are ignored by the top-level command boundary. They do not change the original
command's stdout, stderr, exit code, or success/failure meaning, and journal
failures are never recursively journaled.

## Inspecting records

Use `projmux diagnostics log`; see [cli-guide.md](cli-guide.md#diagnostics). All text,
JSONL, tail, and filter views consume the same tolerant reader. A successful
viewer read is excluded from success logging, so inspection does not create a
recursion loop.

The older bounded `ai-ingest.log` and subsystem-specific `PROJMUX_*_DEBUG`
surfaces retain their current paths, formats, and behavior. Canonical
`internal agent-hook ingest` writes the same JSONL bytes and `diagnostics
agent-hook` reads them. `diagnostics report` still emits
its allowlisted source/result count summary. Append failure remains best-effort
and independent of common-journal append failure.

The measured migration parity is:

| Legacy `ai-ingest.log` result | Common operational projection |
| --- | --- |
| parse error with no classified event | `payload / failed / payload-invalid` |
| bell queue/store or Antigravity response route error | allowlisted semantic kind / `failed / route-failed` |
| no matching pane | allowlisted semantic kind / `ignored / target-unmatched` |
| pane-not-found bell target | `bell / ignored / target-unmatched` |
| unknown event recorded as quiet | `unknown / ignored / unsupported-event` |
| normal `state`, `notify`, known `quiet`, or `deduped` | zero common AI events; existing state/notify owner remains authoritative |
| stdin read or payload-size rejection before the legacy append seam | common-only `payload-read` or `payload-oversized`; no legacy row existed |
| blank `internal agent-hook ingest bell` CLI target rejected before the append seam | common-only `bell / ignored / target-invalid`; exit semantics unchanged |

Operators who need the detailed local view use `diagnostics agent-hook`;
support archives remain count-only for that file. The common journal is the
safe correlated source for watcher lifecycle and anomalous ingest
classification, while the bounded file retains detailed normal-state rows.

## Detached process SIGQUIT artifacts

The detached Codex lifecycle watcher and endpoint broker keep provider hooks
quiet and non-blocking: hook stdout and stderr are still discarded and hook
failure still cannot block the provider. For process-local deadlock diagnosis,
sending `SIGQUIT` to the exact projmux-owned `codex-broker-watch` or
`codex-broker serve` PID writes one artifact before retaining SIGQUIT's fatal,
non-zero exit behavior:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/crash/codex-broker-watch-<pid>.sigquit.txt
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/crash/codex-broker-serve-<pid>.sigquit.txt
```

The `crash` directory is mode `0700`, each file is mode `0600`, and publication
is bounded to 1 MiB and atomic. A dump that reaches the bound ends with an
explicit truncation marker. Normal startup creates no crash artifact or
append-style crash log. These files are local-only and are deliberately not
included in `diagnostics report`: goroutine stacks can contain absolute source
paths and other sensitive local data. Inspect and share them only as carefully
as any other local crash dump.

`PROJMUX_FOCUS_DEBUG` remains available with its existing one-line byte
contract. Focus diagnostics share its request classification seam, but do not
copy the debug line's raw target, session/window/pane, socket, client, source,
or kind values into the journal. It is a documented **Deprecate candidate**
because the common focus transition is the safe support path; its raw routing
byte contract remains unchanged until a separate breaking deprecation.

The complete file/surface inventory and decisions are maintained in
[legacy-diagnostics-inventory.md](legacy-diagnostics-inventory.md). These are
candidates only; Phase 6 removes, renames, ignores, or changes none of them.

## Explicit support report

`projmux diagnostics report [--output <path>]` previews and then atomically
publishes a private local `tar.gz`; see [cli-guide.md](cli-guide.md#diagnostics). The
manifest records report schema version 2, `default-hash-v1` redaction, every
included entry, and stable missing/corrupt/permission omission reasons. Doctor
JSON schema version 2 and the bounded operations decoder are reused rather than
duplicated. AI ingest contributes count-only allowlisted source/result rows,
never raw legacy lines. Existing output files survive collisions and partial
temporary archives are removed.

## Install residue ledger

`make install` and `npm install` replace the executable on disk. Neither
replaces the image of a process that is already running, so every long-lived
projmux child — the Codex broker runtime, the lifecycle observers, the per-pane
supervisors — keeps executing the code it started with until it exits on its
own. The install reports success while most of the running fleet is still on
the previous build.

`projmux internal install-residue` is the census of exactly that. It is hidden
internal plumbing invoked by the installer, not a command to type: `make
install` runs it as its last step, and the npm bin wrapper runs it once on the
first interactive run after an install. It reads the process table, appends one
record, prints at most one notice, and always exits `0` — a diagnostic that can
fail the install it reports on is worse than no diagnostic.

The notice goes to **stderr** and is printed only when the census was taken and
found residual processes:

```
>> 24 projmux processes are still running the image this install replaced
     supervisor          20   oldest 3h02m   median 45m
     lifecycle-observer   3   oldest 2h14m   median 41m
     broker-runtime       1   oldest 2h14m   median 2h14m
   They keep executing code from before this install until each one exits.
   Recreating a pane moves that pane onto the installed build; the broker
   follows when its last binding goes.
   Recorded to ~/.local/state/projmux/install-residue.jsonl (17 installs, last 38m ago).
```

Live process Agent owners are printed in a second block whenever any exist,
because an install never stops them:

```
>> 1 live process owner retained; this install does not stop or replace it
     reviewer (uid:agent-…)  codex  revision 4f1308af…  coordination unknown
   Each keeps its own build until its Agent is stopped and resumed with
   `projmux agent resume <agent-ref>`.
```

That block names Agents and builds on the terminal only; the record keeps the
count. An install that reached the whole fleet and retained no owner prints
nothing at all. A platform with
no readable process table (macOS, which has no `/proc/<pid>/exe`) also prints
nothing: the census cannot be taken there and the operator has no action
available, so a line at every install would be permanent noise. Both cases
still write a record.

### Ledger format

The path is `${XDG_STATE_HOME:-$HOME/.local/state}/projmux/install-residue.jsonl`,
resolved through the same `internal/config` layout as every other state file.
It is JSON Lines, appended one object per install, created `0600` in the
private state directory, and trimmed to approximately the newest 1000 records.
Every failure — an unresolvable state directory, an unwritable file — is
silent.

```json
{
  "at": "2026-09-06T04:12:33Z",
  "installer": "make",
  "supported": true,
  "observed": 41,
  "replaced": 37,
  "sinceLastInstallSeconds": 3612,
  "roles": [
    {"role":"supervisor","processes":22,"current":2,"replaced":20,
     "replacedAgeSeconds":[610,1200,4300,8800]}
  ]
}
```

| field | meaning |
| --- | --- |
| `at` | when the census was taken, RFC3339 UTC |
| `installer` | the existing `PROJMUX_INSTALLER` value; `unknown` when unset |
| `supported` | whether this platform exposes a process table the census can be taken from |
| `observed` | projmux processes classified |
| `replaced` | how many of them run the image this install replaced |
| `sinceLastInstallSeconds` | gap to the previous record; absent on the first |
| `roles[].role` | a role from the process role vocabulary in [replacement-contract.md](replacement-contract.md#process-role-vocabulary), such as `broker-runtime`, `supervisor`, `process-host-helper`, or `other` |
| `roles[].processes` / `current` / `replaced` | that role's census |
| `roles[].replacedAgeSeconds` | ascending age distribution of that role's residual processes, whole seconds |
| `roles[].replacedAgeCapped` | present when the 512-sample per-role bound was reached, so the distribution above is a prefix |
| `processOwners` | live process Agent owners the install retained, counted from the Registry; absent when there were none |

The record carries **no pid, no executable path, and no argv**, on the terminal
and in the file alike. Counts and durations are the whole of it; process
identity is exactly what the census is built not to record. A residual process
whose start time could not be read is still counted in `replaced` and
contributes no entry to `replacedAgeSeconds`, so that array can be shorter than
`replaced`.

Ages come from the modification time of `/proc/<pid>`, which the kernel stamps
at process creation and never moves. That is chosen over `/proc/<pid>/stat`
field 22 plus `/proc/stat` `btime` because it needs no `USER_HZ` assumption, no
boot-time read, and no parsing around the `comm` field, which is an executable
name in parentheses and may itself contain `)` and spaces.

### What the ledger is for

The ledger is the deliverable; the notice is a courtesy. An automatic
replacement of residual processes cannot be designed without a termination
condition for the drain, and these three fields are what such a condition is
derived from:

| field | the question it answers |
| --- | --- |
| `roles[].replaced` (per role) | what to make a replacement target |
| `roles[].replacedAgeSeconds` (the full sorted distribution, not a mean) | does a bounded drain finish in finite time, and what cutoff `T` covers which fraction |
| `at` + `sinceLastInstallSeconds` across records | how often this happens per install, hence whether a forced cutoff is needed at all |

A mean would destroy the second answer: twenty supervisors averaging forty
minutes says nothing about the one that outlives every plausible bound. That is
why the distribution is stored whole.

## Headless owner shutdown

`projmux diagnostics log --component agent --tail 50` shows one
`agent.owner.stop` record for the first shutdown path of each owned process
generation. `code` is one of `owner.stop.signal-sigint`,
`owner.stop.signal-sigterm`, `owner.stop.stdin-eof`, `owner.stop.control-stop`,
`owner.stop.generation-abandoned`, `owner.stop.provider-exit`, or
`owner.stop.other` (including synchronization and launch cleanup failures).
The private journal includes `agent_uid`, `pane_uid`, `generation`, `owner_pid`,
`owner_ppid`, and `parent_comm`. The parent name is a bounded basename and is
empty when unavailable (including systems without `/proc`). It is a clue about
ancestry, not evidence of who sent a signal.

The owner also writes one `agent owner stop: reason=...` line to stderr. CLI
create/resume/relaunch owners and detached owners using the same lifecycle
share this recording. Competing shutdown paths retain the first recorded
cause. Journal and stderr writes are best-effort; failures do not change Stop
or the provider's normal/abnormal classification. SIGKILL cannot be recorded
by the killed owner. Older journal readers skip this new event.
