# Session Restore

Session snapshots are explicit desired-state inputs for one Project. They are
not tmux replay scripts and they are not Registry backups. Snapshot save keeps
the existing v1 schema and storage behavior.

```sh
projmux get snapshots [--session <snapshot-session>]
projmux create snapshot
projmux restore snapshot --session <snapshot-session> [--project <ref> | -p <ref>] --dry-run
projmux restore snapshot --session <snapshot-session> [--project <ref> | -p <ref>] --yes [--client /dev/pts/N]
projmux delete snapshot --session <snapshot-session>
```

Restore requires an exact snapshot and an exact, closed target Project. The
dry-run validates both inputs and prints replacement, deletion, preserved-UID,
and lost-conversation-pointer counts with zero Registry, tmux, and snapshot writes.
Snapshot Project/Window/Pane metadata is checked against the exact target owner
chain. A UID held by another root, a cross-kind UID reuse, a Project mismatch,
or conflicting owner metadata refuses before commit. The ordinary Project-open
trust authorization must also approve the exact target root before the Registry
transaction begins.

After `--yes`, one atomic Registry transaction replaces only the target
Project's descendant Window/Pane/Agent graph and its descendant name
reservations. The Project UID, root, trust metadata, unrelated Projects,
ControlSessions, and source snapshot bytes are preserved. Metadata-bearing
snapshots reuse their exact target-subtree UIDs and preserve a surviving
final-v2 Agent or shell anchor plus a surviving direct default shell.
Metadata-free legacy snapshots reuse target descendants positionally, select
the first valid Window-local Pane as the role-agnostic anchor, select the first
direct shell as the optional default, and mint identities only for missing
items. An Agent-only Window is valid with an empty default. Repeating the same
projection is a Registry zero-diff.

The committed Registry is then converged by the ordinary Project materializer.
For a restored offline Agent-anchor Window, `Continue project` visibly plans a
lazy default shell, creates the Window from that shell, and stages the Agent on
its retained anchor Pane UID. A successful repeat writes neither Registry nor
topology. Agent recipes use the canonical provider launch/resume path. Stored startup
commands are not directly executed by snapshot restore. A runtime item refusal
does not roll the Registry back: desired state and the source snapshot remain
available for another `Continue project`, and the refusal is reported as an
item notice. If an explicit client is supplied, the final observable step is
`switch-client -c` to the Project's declared session even when the background
continuation has no inherited `TMUX` variable.

## Project startup

A closed Project has exactly two actions:

- `Continue project` opens the current Registry desired state with the ordinary
  materializer.
- `Open fresh` keeps the exact schema-v2 canonical Project Window. It reuses an
  existing exact direct shell when available or allocates the minimum one,
  makes that Pane both anchor and default, and removes every Agent/extra
  descendant and descendant reservation.

Esc/cancel returns to Projects; it is not an action row. Picker failure falls
back to the non-destructive `Continue project` action.

`Open fresh` never deletes or overwrites autosave or named snapshot files. It
also preserves the Project UID/root/trust decision and all unrelated Registry
graphs. An invalid canonical Window is refused with zero writes and repair
guidance; an Agent-only canonical Window receives one allocated shell instead
of being rejected, and an unregistered path may use ordinary first-use Project
bootstrap. Repeating `Open fresh` is a Registry zero-diff.

## Snapshot contents and diagnostics

Snapshots keep window names, pane cwd/label/title, shell/startup/agent recipes,
AI topic ownership, and provider resume metadata when available. Resume health
in preview is `available`, `stale`, or `unavailable`; confidence derives from
the stored source. Snapshot inspection never reads provider transcript or
conversation database content.

An approved projection restore records one safe Session State outcome with
aggregate Window/Pane/recipe counts and source `manual`. Paths, commands,
snapshot content, and provider conversation identifiers are never included.
Dry-run remains read-only and records no mutation outcome.
