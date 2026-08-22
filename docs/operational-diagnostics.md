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

Command and subcommand names come from static allowlists. Unknown argv values,
paths, flags, and arguments are dropped. Messages have control/format
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

Session State mutations use one outcome-only `session-state.outcome` record
per selected attempt. The closed operations are `session-state.save`,
`session-state.autosave`, `session-state.restore`, and `session-state.delete`;
an error uses only the matching `.failed` code, `kind=runtime`, and an empty
message. Optional sources are limited to `manual`, `settings-latest`,
`settings-named`, `autosave`, `startup-latest`, `startup-named`, and `prune`.
Successful save and actual startup restore outcomes contain only exact
non-negative `window_count`, `pane_count`, `shell_recipe_count`,
`agent_recipe_count`, and `startup_recipe_count` aggregates. Successful delete
contains only `item_count`; errors contain no counts. Snapshot paths/content,
project paths, pane cwd/commands, snapshot names, and agent or conversation
identifiers are never projected.

Direct and popup save, Settings latest/named save, direct and Settings delete,
deduplicated prune delete, and actual latest/named project-startup replay own
these outcomes. Preview, dry-run, and nested store/replay calls do not.
Autosave success and disabled, not-due, or fresh no-ops always write zero
records; a real autosave failure writes exactly one error even when `--quiet`
preserves its historical successful exit. Session State logical ownership
suppresses a generic top-level outcome even when journal append fails. An
actual restore may also produce its runtime lifecycle pair with the same run
ID; the lifecycle pair and Session State terminal outcome describe different
contracts and are not duplicates.

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
`tool`, `session`, `compact`, `subagent`, `teammate`, `statusline`, `invocation`,
`lifecycle`, `bell`, `payload`, or `unknown`. A raw or future event name can
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
the source. Windows ACL privacy is reported as unverified because `os.FileMode`
cannot prove it; a separate finding preserves the metadata-only writability
result, and Doctor does not modify ACLs.

### Registry materialization invariant audit

`projmux doctor --section registry` reports the admission difference between
what the Registry writer accepts and what activation can rebuild.
`Registry.Validate` and the materialization planner do not share a
precondition: a Window that owns no shell Pane carries an empty
`spec.primaryPaneRef`, which validation accepts explicitly and which activation
cannot build a Window from. That difference set is reachable through ordinary
use, so it needs a detection surface of its own.

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
5 MiB, a platform-specific atomic replacement retains approximately the
newest 2 MiB, beginning at a complete valid record; Windows uses replace-
existing semantics rather than plain rename. A trailing partial record is
discarded before the next append, and the reader skips malformed or truncated
records.

Classification is intentionally conservative for mutation-capable interactive
commands: opening session/project/settings/popup flows is treated as changing
even when a user cancels. Explicit read variants (`internal status`, `list`,
`get`, read-only restore preview, config rendering, plain welcome, and the diagnostics viewer) remain
read-only. The successful automatic hook/poll paths `internal agent-hook ingest`, `attention
arm`, `attention clear`, `attention window`, `internal tmux autosave-session-state`, and
`window record` are also read-only so high-frequency operation does not append
to the journal; an error from any of them still records exactly one safe error
outcome. Explicit user mutations such as `attention toggle` retain their
state-changing success record. Direct top-level help and explicit preview-only intents (`update
apply --dry-run`, AI integration dry-runs, and snapshot projection restore with
`--dry-run`) are also read-only. Approved snapshot projection restore is a
state-changing operation. Doctor is a stricter
boundary: successes and errors never append to this journal, so diagnostics do
not make its filesystem contract self-defeating. Support report success and
errors likewise never append; its strict reader shares the viewer's tolerant
decoder but never creates/locks/chmods/repairs/truncates the source journal.
Multi-mode commands such as AI status/topic,
terminal apply, snapshot delete, update check, and welcome popup inspect only
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
