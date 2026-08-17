# Legacy CLI Retirement Ledger

Phase 2 is a breaking CLI release. Removed human-facing compatibility argv
returns exit 2, writes no stdout, performs no command or pre-dispatch migration
side effect, and prints the replacement below on stderr. The seven removed old
internal top-level aliases instead follow the root unknown-command contract:
exit 1, no stdout or side effect, and root help plus `unknown command: <token>`
on stderr.

| Removed argv | Replacement |
| --- | --- |
| `ai split`, `ai picker` | `create agent`, `create pane`, or a provider shortcut |
| `ai settings` | `config edit` |
| `ai status`, `ai topic`, `ai integrate` | `agent status`, `agent topic`, `agent integrate` |
| `ai notify [notify] [pane]` | `create notification --text ... --target ...`; translate the old pane/payload because the input and semantics changed |
| `ai notify reset [pane]` | No direct replacement for desktop-notification dedupe-state reset; queue maintenance uses `notification ack` or `notification reconcile` |
| `ai watch-title` | `internal agent-hook watch-title` |
| `ai ingest log` | `diagnostics agent-hook` |
| `attach auto` | `runtime attach` |
| bare `current` | `get pane --current -o cwd` |
| `focus --target`, `focus --uri` | `focus project|window|pane`; machine ingress uses `internal focus` |
| `kill tagged` | `runtime stop` |
| `sessions` | `runtime sessions` |
| `upgrade` | `update apply` |
| `usage` | `agent usage` |
| `notify push`, `notify list`, `notify ack`, `notify reconcile` | `create notification`, `get notifications`, `notification ack`, `notification reconcile` |
| direct `pin list|add|remove|toggle|clear` | `pin project ...` |
| `prune ephemeral` | `runtime prune` |
| `prune session-state ...` | `prune snapshot ...` or `delete snapshot ...` |
| `session-state status|save|delete|restore|preview|popup` | `get snapshots`, `create snapshot`, `delete snapshot`, `restore snapshot` |
| direct `tag list|toggle|clear`, `tag project ...` | `runtime tag ...` |

The surviving mixed-root commands are exactly `attach project`, `focus
project|window|pane`, `pin project`, and `prune project|snapshot`. The Shortcut
routes `doctor`, `quit`, `resources`, `settings`, `shell`, `switch`, and
`welcome` remain. Singular/plural resource-kind aliases remain.

The removed pre-namespace internal aliases are `key-broker`, `popup-wait-key`,
`preview`, `session-popup`, `status`, `statusbar`, and `tmux`. Their only
remaining entrypoints are the corresponding `internal ...` routes, subject to
the exact updater handoff exception below. Public configuration work uses
`config render` and `config apply`.

## Updater handoff exception

The immutable v0.10.1 GitHub Release updater replaces its own executable and
then invokes the replacement with exact argv `tmux apply`. The replacement
accepts only those two tokens as a hidden handoff and routes them through the
current `config apply` convergence path. Exit status, stdout, stderr, managed
producer migration, generated-config writes, rollback, and live tmux mutation
are therefore the current apply contract rather than a second implementation.

This exception does not restore a `tmux` catalog node or top-level handler and
is absent from root help and generated CLI documentation. Bare `tmux`, every
other old tmux subcommand, and `tmux apply` with any extra argv still use the
removed-root contract: exit 1, no stdout or side effect, and root help plus
`unknown command: tmux` on stderr.

## Internal migration exception

Phase 2 intentionally retains one hidden compatibility dispatcher until the
next release gate: exact old managed-producer argv only. The accepted bytes are
`ai ingest codex-hook`, `ai ingest claude-hook`, `ai ingest antigravity-hook
--event <event>` where `<event>` is exactly `PreInvocation`, `PostInvocation`,
`PostToolUse`, `Stop`, or `Statusline`, and `ai ingest bell --pane <pane-id>`.
The flag order is fixed and no extra argv is accepted. Human and machine callers
with identical argv cannot be distinguished.

Every other `ai` argv, including `ai ingest log`, a missing/future/custom
Antigravity event, reordered flags, or extra argv, returns the public retirement
usage error before stdin, tmux, diagnostics, or stdout is touched. Managed
producers already emit `internal agent-hook ingest`; this exception exists only
for the binary-replacement-before-migration window. Do not remove it until the
post-release `R_migrate` evidence gate is closed.

The `ai notify` split is intentional parity guidance, not an alias mapping.
The old notify action drove an immediate desktop-notification path from pane
state; `create notification` creates a queue row and therefore requires an
explicit `--text` and `--target`. The old reset action cleared transient
desktop dedupe state, which the public queue commands do not reproduce.
