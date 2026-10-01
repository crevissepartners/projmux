## Related design and inventory notes

### Plan-only managed runtime mutation

Managed lifecycle/topology changes are printable `runtimeMutationPlan` rows.
Each row carries an exact invocation route, immutable observed socket path,
printable server-generation authority, a stable tmux handle and Registry
UID/owner chain, a closed guard, total order,
expected effect, and printable typed operands bound to that handle. Execution
validates printable target/route authority before pre-effect reobservation and
every pending semantic guard before the first write;
owned rollback runs in reverse order. Materialization is intentionally staged:
after each dynamic handle is returned, it is reobserved and the next stage is
planned, so no later action guesses a Window or Pane handle. A successful
reobserve/replan is empty; an unknown observation authorizes no delete or kill.
App-owned execution requires exact path/pid/app/logical evidence. An inherited
standalone route is separately closed by exact `TMUX=path,pid,index` plus a
producer-verified Pane receipt and prints/executes through `-S`; partial app
markers never downgrade to standalone. Explicit controller reconciliation may
instead use an operator-selected `--socket-path` plus PID/blank-marker receipt,
but only action-specific UID and containment guards authorize its writes.
Fresh app bootstrap is the only
pre-server declaration without a generation receipt, and binds path/pid/$@%
before its route marker and all later rows.

The maintained product table in `internal/app/runtime_mutation_surface.go` maps
generated catalog/menu producers, native provider/resume picker selections,
sidebar/session-picker stops, and app lifecycle entrypoints in both directions
to their handler and plan verb. It also records exact semantic exemptions for
focus, labels, operator-requested layout, mouse forwarding,
ephemeral maintenance, app quit, and human runtime maintenance. Managed argv
verbs are selected only by the typed executor seam; generated Window
create/rename, Pane-menu create/delete, and automatic post-split layout writes
reach typed intent/operand routes rather than embedding tmux lifecycle commands.

Contributor-facing companions to this document. They are design records and
inventories rather than user documentation, so they are linked from here rather
than from the README docs index.

- [globalization.md](../globalization.md) — the globalization contract: which
  user-facing string families are translatable and how they are classified.
- [migration-plan.md](../migration-plan.md) — the standalone plan the shell-to-Go
  migration follows, slice by slice.
- [settings-ia.md](../settings-ia.md) — the Settings information architecture:
  section ownership, row density, and feedback rules.
- [shell-autostart.md](../shell-autostart.md) — shell auto-start integration and
  its opt-out behavior.
- [tmux-surface-inventory.md](../tmux-surface-inventory.md) — the inventory of tmux
  options, hooks, and bindings projmux owns.
