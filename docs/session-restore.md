# Project Startup

A closed Project starts from its Registry desired state. projmux keeps no other
saved Project state: the Registry (`registry.json`) is the only input.

A registered closed Project has exactly two actions. A root that is not a
registered Project is not asked: it opens fresh, which registers it.

- `Continue project` opens the current Registry desired state with the ordinary
  materializer. A retained graph keeps its Project, Window, Pane, and Agent
  UIDs. A zero-Window Project keeps its Project UID and atomically receives one
  new canonical Window and shell UID before materialization.
- `Clear layout and open` clears the Project's saved Window and Agent layout
  and opens it again; the folder, its files, `.projmux/config.toml`, and trust
  stay. It atomically replaces the same-root graph with a new Project
  UID and one new canonical Window/shell UID chain, after a confirmation naming
  the exact old Project UID and its Window/Pane/Agent counts. Declining returns
  to the startup rows and writes nothing. It does not archive or retain the old
  generation. Its new Window's first Pane follows the saved launch default
  (`tmux-ai-split-mode`), exactly as a Window created from the UI does: the
  choice is made before anything is cleared. The first open of an unregistered
  root, which resolves to the same fresh start, behaves the same.

Esc/cancel returns to Projects; it is not an action row. Picker failure falls
back to the non-destructive `Continue project` action.

`Continue project` needs a registered Project. On a root that is not a
registered Project it refuses with zero Registry writes and points to
`Clear layout and open` (`continue project unavailable: <root> is not a registered
Project; choose Clear layout and open`). It never falls back to Fresh on its own.

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

`Clear layout and open` preserves the root, Git/worktrees, trust decision, and all unrelated Registry
graphs while changing the Project identity. A rejected commit retains the
exact old Registry preimage. Repeating `Clear layout and open` replaces identity again;
each successful result has exactly one Project claiming the root.

The saved launch default is used only when the open carries the exact client
that pressed the row. The order is:

1. Ask. A picker mode (`selective`, the unset default, or `resume`) opens its
   picker on the Pane the row was pressed in, before the old layout is cleared
   or the new Session exists; a provider mode and `shell` are already the
   answer and open nothing.
2. Clear the layout and create the new Session with its one shell Pane.
3. Fill it: an Agent answer is created in that Window first, and the shell is
   then removed through the canonical Pane delete.
4. Move the pressing client onto the finished Session.

The client therefore never sees a shell Pane that is about to be replaced.
Closing the picker without a choice does not stop the open: the Session opens
with its shell Pane and nothing is said. An open without the exact client -- a
detached `start project`, a scripted open -- asks nothing, in every mode, and
keeps the plain shell Pane. A question that cannot be asked, or an answer that
cannot be filled in, costs one line on that client after the move and keeps the
shell Pane; the Project stays open either way. `Continue project` and the
`start project`/`open project` verbs never use the saved launch default.
