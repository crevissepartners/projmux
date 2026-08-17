# Legacy CLI Retirement Ledger

This ledger records the executable replacement for every top-level route still
classified as `compatibility`. Phase 1 adds missing doors and moves machine
writers; it does not remove any old route. Phase 2 must preserve the differences
called out here rather than assuming that every replacement is a spelling-only
rename.

| Compatibility route | Executable canonical replacement | Parity and error differences |
| --- | --- | --- |
| `ai` | `create agent`, `create pane`, `config edit`, `agent status`, `agent topic`, `agent integrate`, `create notification`, `diagnostics agent-hook`, `internal agent-hook` | `config edit [--get|--set <mode>]` forwards exactly to AI-scoped `ai settings`; status/topic/integrate and hook routes likewise reuse existing handlers. Create routes normalize their public flags before reaching the split handler. `ai picker` remains a UI shortcut. `ai notify` has no byte-identical queue command: `create notification` writes a queue row, while the legacy route dispatches or resets desktop-notification state; removal must keep that distinction explicit. |
| `attach` | `attach project`, `runtime attach` | `runtime attach` prefixes `auto` and preserves raw argv. `attach project` is the resource-aware outside-tmux route and intentionally refuses inside a client. |
| `current` | `get pane --current -o cwd` | Same live cwd scalar; the replacement spells the exact-one read and field projection explicitly, so malformed flags use the canonical usage vocabulary. |
| `focus` | `focus project`, `focus window`, `focus pane`; machine callers use `internal focus` | `internal focus` is an exact raw-argv forwarder for `--target`/`--uri` and preserves fallback behavior. Resource focus is exact-only and rejects legacy prefix/index fallback with exit 2. |
| `kill` | `runtime stop` | Exact handler, streams, exit, and side effects; the canonical route prefixes `tagged`. |
| `notify` | `create notification`, `get notifications`, `delete notification`, `notification ack`, `notification reconcile` | Each Phase 1 queue route forwards raw argv to the same notify handler. `delete notification` is the older delete bridge and currently prefixes `ack`; use `notification ack` when the intent is acknowledgement. |
| `pin` | `pin project` | Exact handler and action argv; `project` is a routing token for the same directory-lines store, not persistent Project metadata. |
| `prune` | `runtime prune`, `prune project`, `prune snapshot`, `delete snapshot` | Runtime and snapshot paths reuse the existing prune/session-state handlers. Project pruning is registry-aware and has its own required safety flags. |
| `sessions` | `runtime sessions` | Exact handler, streams, exit, and side effects with unchanged raw argv. |
| `session-state` | `get snapshots`, `create snapshot`, `delete snapshot`, `restore snapshot` | `create snapshot` is an exact `session-state save` forwarder. Read/delete/restore already forward to the same session-state handler; restore remains dry-run-only. Preview/popup remain UI shortcuts until their removal decision. |
| `tag` | `runtime tag` | Direct `tag list|toggle|clear` and the still-executable project-qualified `tag project ...` compatibility argv share the same ephemeral tagged-session handler. `tag project` is not a canonical target and no persistent Project-tag target state exists. |
| `upgrade` | `update apply` | The canonical updater is installer-aware and may reject or guide unsupported sources differently; Go/source installs delegate to the existing upgrade implementation. |
| `usage` | `agent usage` | Exact handler instance and raw argv; stdout, stderr, exit, cache refresh, and API side effects are identical. |

The remaining routes without a canonical mapping are shortcuts, not
compatibility gaps: `doctor`, `quit`, `resources`, `settings`, `shell`, and
`welcome`. Phase 1's `config edit` is AI-scoped, so it does not rename the
general Settings UI. They stay a separate audited set in
`internal/cli/catalog_test.go`.

Machine producers in a newly built binary emit only these canonical paths:

- provider hooks and title watching: `internal agent-hook`;
- statusbar and notification-sidebar target dispatch: `internal focus`;
- installer/update convergence: `config apply`;
- post-install queue convergence: `notification reconcile`.

The exact legacy `ai ingest log` reader remains executable for compatibility;
the public replacement is `diagnostics agent-hook`. Both are read-only and
share the same AI ingest-log handler.
