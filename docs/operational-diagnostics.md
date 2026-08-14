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
attach, switch, kill, and tmux apply; codes are stable failure/health
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
even when a user cancels. Explicit read variants (`status`, `list`, `get`,
`preview`, config printing, plain welcome, and the diagnostics viewer) remain
read-only. The successful automatic hook/poll paths `ai ingest`, `attention
arm`, `attention clear`, `attention window`, `tmux autosave-session-state`, and
`window record` are also read-only so high-frequency operation does not append
to the journal; an error from any of them still records exactly one safe error
outcome. Explicit user mutations such as `attention toggle` retain their
state-changing success record. Direct top-level help and explicit preview-only intents (`upgrade
--dry-run`, `update apply --dry-run`, AI integration dry-runs, and the
currently preview-only session restore) are also read-only. Doctor is a stricter
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

Use `projmux diagnostics log`; see [cli.md](cli.md#diagnostics). All text,
JSONL, tail, and filter views consume the same tolerant reader. A successful
viewer read is excluded from success logging, so inspection does not create a
recursion loop.

The older bounded `ai-ingest.log` and subsystem-specific `PROJMUX_*_DEBUG`
surfaces retain their current paths, formats, and behavior. They are not
migrated by this foundation.

`PROJMUX_FOCUS_DEBUG` remains available with its existing one-line byte
contract. Focus diagnostics share its request classification seam, but do not
copy the debug line's raw target, session/window/pane, socket, client, source,
or kind values into the journal. Removal or migration of the debug variable is
deferred to the later legacy-diagnostics inventory.

## Explicit support report

`projmux diagnostics report [--output <path>]` previews and then atomically
publishes a private local `tar.gz`; see [cli.md](cli.md#diagnostics). The
manifest records report schema version 2, `default-hash-v1` redaction, every
included entry, and stable missing/corrupt/permission omission reasons. Doctor
JSON schema version 2 and the bounded operations decoder are reused rather than
duplicated. AI ingest contributes count-only allowlisted source/result rows,
never raw legacy lines. Existing output files survive collisions and partial
temporary archives are removed.
