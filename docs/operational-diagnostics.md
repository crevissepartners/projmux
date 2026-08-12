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
no generic metadata map.

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

## Explicit support report

`projmux diagnostics report [--output <path>]` previews and then atomically
publishes a private local `tar.gz`; see [cli.md](cli.md#diagnostics). The
manifest records report schema version 1, `default-hash-v1` redaction, every
included entry, and stable missing/corrupt/permission omission reasons. Doctor
JSON schema version 1 and the bounded operations decoder are reused rather than
duplicated. AI ingest contributes count-only allowlisted source/result rows,
never raw legacy lines. Existing output files survive collisions and partial
temporary archives are removed.
