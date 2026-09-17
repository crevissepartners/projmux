# Project Startup

A closed Project starts from its Registry desired state. projmux keeps no other
saved Project state: the Registry (`registry.json`) is the only input.

A closed Project has exactly two actions:

- `Continue project` opens the current Registry desired state with the ordinary
  materializer. A retained graph keeps its Project, Window, Pane, and Agent
  UIDs. A zero-Window Project keeps its Project UID and atomically receives one
  new canonical Window and shell UID before materialization.
- `Recreate Project` atomically replaces the same-root graph with a new Project
  UID and one new canonical Window/shell UID chain, after a confirmation naming
  the exact old Project UID and its Window/Pane/Agent counts. Declining returns
  to the startup rows and writes nothing. It does not archive or retain the old
  generation. Its new Window's shell Pane then follows the saved launch default
  (`tmux-ai-split-mode`), exactly as a Window created from the UI does. The
  first open of an unregistered root, which resolves to the same fresh start,
  behaves the same.

Esc/cancel returns to Projects; it is not an action row. Picker failure falls
back to the non-destructive `Continue project` action.

`Continue project` needs a registered Project. On a root that is not a
registered Project it refuses with zero Registry writes and points to
`Recreate Project` (`continue project unavailable: <root> is not a registered
Project; choose Recreate Project`). It never falls back to Fresh on its own.

projmux does not save Project state on its own at quit or on a timer.
`projmux quit` offers only `Quit projmux` and `Cancel`. The hidden `internal tmux autosave-session-state`
route is kept only so status lines rendered by older installs keep working, and
it does nothing.

Continue resumes an Agent's exact recorded conversation after interrupted,
killed, abnormal, unknown, or unrecorded termination. Intentional and normal
termination remain excluded. A recorded receipt must agree on the Agent and
its retained Pane's current managed activation. Without a receipt, Running
requires its exact paneRef; Offline or Failed requires one unambiguous retained
Agent-owned Pane activation. Pending Agents are skipped. Live Agents are not
launched again.

A missing, blank, malformed, or mismatched conversation ref, a disabled provider,
a missing workspace, or a resume preparation failure skips the Agent with a
reason. Continue never substitutes a new conversation. Shells and other
recoverable Agents still converge; an unrecoverable Agent that is itself a
Window's required anchor keeps the existing Window refusal. `agent resume`
retains its separate authority.

After Continue commits, its startup summary shows the resumed and skipped Agent
totals and `projmux diagnostics log --component topology`. The same counts are
available in the public materialize-project JSON result's `recovery` field.
Already-live Agents count in neither total. Full per-Agent explanations remain
on stderr; the transient summary stays within 220 UTF-8 bytes, including any
ellipsis, independently of Agent names or how many were skipped.

The private operations journal stores one `topology.outcome` and one
`topology.agent.skipped` row per reason in the same invocation `run_id`. Reason
counts sum to the committed skipped total. The ten codes use `topology.agent.`
followed by `termination-excluded`, `phase-ineligible`, `activation-unproven`,
`termination-invalid`, `session-ref-missing`, `session-ref-invalid`,
`session-ref-mismatch`, `provider-unavailable`, `workspace-unavailable`, or
`resume-prepare-failed`. `diagnostics report` includes recent closed recovery
events in `topology-recovery.json`, including successful partial and 0/0 results.
Journal rows contain no resource IDs/names, conversation IDs, payloads, or raw
errors. Dry-run writes no execution event; failed or rolled-back execution
records an error with zero committed counts. Journal and display failures are
best effort and never change the topology result.

`Recreate Project` preserves the root, Git/worktrees, trust decision, and all unrelated Registry
graphs while changing the Project identity. A rejected commit retains the
exact old Registry preimage. Repeating `Recreate Project` replaces identity again;
each successful result has exactly one Project claiming the root.

The saved launch default is applied only when the open carries the exact client
that pressed the row, after that client has been moved onto the new Session; an
open without one -- a detached `start project`, a scripted open -- keeps the
plain shell Pane and says nothing. A default that cannot be applied costs one
line on that client and keeps the shell Pane; the Project stays open either way.
