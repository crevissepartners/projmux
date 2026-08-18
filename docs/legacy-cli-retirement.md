# Legacy CLI Retirement Ledger

Phase 2 removed the human-facing compatibility argv below. The mixed roots
retain only their canonical children: rejected `attach`, `focus`, `pin`, and
`prune` compatibility shapes return exit 2 with exact replacement guidance and
no stdout, handler reach, or pre-dispatch migration. The fully removed roots
`current`, `kill`, `notify`, `sessions`, `session-state`, `tag`, `upgrade`, and
`usage` are absent from the catalog and handler map, so every argv under them
uses the ordinary root unknown-command contract (exit 1, no stdout or side
effect, and root help plus `unknown command: <root>` on stderr).

Phase 3 removed the final hidden `ai` producer dispatcher and catalog node.
The `ai` root and the seven removed old internal top-level aliases use that same
unknown-command contract.

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

## Final ingest compatibility removal

Phase 2 temporarily retained one hidden compatibility dispatcher for exact old
managed-producer argv. The accepted historical bytes were
`ai ingest codex-hook`, `ai ingest claude-hook`, `ai ingest antigravity-hook
--event <event>` where `<event>` is exactly `PreInvocation`, `PostInvocation`,
`PostToolUse`, `Stop`, or `Statusline`, and `ai ingest bell --pane <pane-id>`.
The v0.11.1 `R_migrate` release migrated marker-owned Codex, Claude,
Antigravity/Statusline, and tmux-bell producers to `internal agent-hook ingest`
before this dispatcher was removed. The old bytes are now history, not an
executable alias; every `ai` argv is rejected at the root before stdin, tmux,
diagnostics, or automatic migration is touched.

A markerless or otherwise unmanaged hook is intentionally never rewritten.
If one still invokes the old spelling, decide that the hook is yours, preserve
its provider arguments, redirection, and fallback, and manually replace only
the command prefix with `projmux internal agent-hook ingest`. For example, the
historical `projmux ai ingest claude-hook` prefix becomes `projmux internal
agent-hook ingest claude-hook`. Run `projmux agent integrate <provider>
--dry-run` afterward; a remaining conflict must be resolved by the owner, not
by projmux taking ownership of the entry.

The `ai notify` split is intentional parity guidance, not an alias mapping.
The old notify action drove an immediate desktop-notification path from pane
state; `create notification` creates a queue row and therefore requires an
explicit `--text` and `--target`. The old reset action cleared transient
desktop dedupe state, which the public queue commands do not reproduce.

## Post-retirement invariants

This breaking boundary removes only the compatibility argv listed above. It
does not narrow the canonical resource model that shipped after the retirement
work began. In particular:

- Resource Project and Window scope keeps the paired `--project` / `-p` and
  `--window` / `-w` options. Plural Window, Pane, and Agent reads keep
  `--all-projects` / `-A`.
- Registry-first startup and explicit reconciliation keep materializing the
  recorded Project, Window, and Pane topology without adopting a foreign tmux
  identity.
- An explicitly selected Offline Window remains canonically deletable from the
  Registry, cascading through its descendant Agent and Pane records without
  issuing a tmux `kill-window` for a mirror that does not exist.
- Root help and `docs/cli.md` remain generated from the current command
  manifest. They advertise the canonical resource routes and their current
  scope options, never the retired roots.

These are preservation constraints for the retirement release, not aliases
for the removed commands. A future change to one of these contracts needs its
own feature or fix decision and must not be smuggled into compatibility
cleanup.
