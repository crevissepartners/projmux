# Session Restore

Session snapshots store tmux layout metadata and replay recipes for shell,
startup, and supported agent panes. Manual CLI actions remain available through:

```sh
projmux session-state status [--session <name>]
projmux session-state save
projmux session-state preview [--session <name>]
projmux session-state restore --dry-run [--session <name>]
projmux session-state delete [--session <name>]
```

Snapshots preserve source metadata, not a final display label. Window records
keep `window_name`; pane records keep `pane_title`, recipe fields, AI topic
metadata (`@projmux_ai_topic`), and resume metadata when available. There is no
`display_label` field in the snapshot schema. After restore, pane borders and
app window tabs are display-time tmux policy: the app config derives both from
the active pane's visible label expression, while raw shell or terminal titles
remain metadata that may change independently. Branch names continue to appear
in the statusbar git segment; branch-based shell title overwrites are not the
canonical Projmux window naming source.

The primary inspection surface is `Projects > Sessions > State`. It shows a
read-only overview first: latest snapshot status, named snapshots, window ->
pane structure, cwd, recipe, and agent resume health. Mutation belongs one
level deeper in explicit actions; the overview does not immediately save,
delete, preview, or restore.

Project Session State actions are scoped to the project-derived session
identity, not the currently attached tmux session. `Save latest snapshot`
captures the live project session when that session exists. `Save named
snapshot` captures the same live project session into a project-local named
snapshot and stores cwd values with portable project-root placeholders. Closed
project sessions disable save actions with the live-session reason. `Preview
restore` prints the read-only dry-run restore plan for the project snapshot, and
`Delete snapshot` requires a confirmation picker before removing the project
snapshot. Destructive restore execution remains outside Settings in this slice.

Agent panes in Settings and restore dry-run previews show resume metadata
health next to the pane recipe. `available` means a resume id is present and
recent enough for replay, `stale` means the stored id exists but the metadata is
missing or older than the snapshot policy, and `unavailable` means projmux
cannot safely resume that agent pane from the snapshot. Confidence is derived
from the metadata source: direct session ids and hook ingest are high
confidence, transcript/log fallbacks are medium confidence, and missing or
unknown sources are low or none. The old statusbar Session State shortcut has
been removed; use `Projects > Sessions > State` or the `projmux session-state`
CLI for inspection/actions.

Agent restore direct-starts supported resume commands when creating fresh tmux
panes, matching the `projmux ai split` wrapper shape: the wrapper prepends the
agent binary directory to `PATH`, changes to the saved cwd, sets the terminal
and tmux pane title from the saved agent topic, then execs `codex resume <id>`,
`claude --resume <id>`, or `agy --conversation <uuid>`. Antigravity restore
uses only the stable statusline `conversation_id` or hook `conversationId`
metadata captured as the pane resume id; missing or non-UUID Antigravity ids
render as `resume unavailable` rather than falling back silently to a shell
recipe. This avoids typing agent resumes with
`tmux send-keys`. The restore wrapper is still a non-interactive shell command
tail, so it does not replay the original pane's interactive shell startup,
environment, shell functions, aliases, or live process state. Startup recipes
continue to use their saved `send-keys` command replay, and shell recipes only
restore cwd/layout.

Settings > Session State is global settings only: global auto-save, auto-save
interval, and storage/retention policy. It does not show the current
snapshot tree. Delete for current-session snapshots and destructive restore
execution stay deferred until those actions have a dedicated safe policy for
existing or non-empty live sessions.

Named snapshots may currently be backed by legacy project files in
`<project>/.projmux/layouts/*.toml`. They reuse the same window, pane, cwd, and
startup recipe concepts as session snapshots, but the user-facing restore model
is still `Latest snapshot`, `Named snapshot`, or `Empty session`. The legacy
files are imported read-only by Project open
when building `Named snapshot` candidates; new primary surfaces should describe
the restore unit as a snapshot, not as a separate layout or preset feature.

Project open from the Alt-1 sidebar defaults to opening a closed project as an
`Empty session`. `Settings > Session State > Sidebar startup picker` is an opt-in toggle;
when it is on, closed project open advances inside the sidebar to the native
`Start project` step. Rows are ordered `Latest snapshot`, named snapshot rows,
`Empty session`, then `Back`. `Latest snapshot` is the auto-saved snapshot that
keeps changing as auto-save runs. `Named snapshot` is a fixed, user-named
snapshot and is not updated by auto-save. Rows include saved-at date/time
metadata when available. `Back` returns to the project list without creating,
replaying, or opening a session. After the startup mode is selected, project
automation trust is evaluated if needed. For a named snapshot with a startup
`command`, projmux rejects symlink artifacts, reads the selected layout once,
and authorizes the SHA-256 of the exact bytes used to parse the in-memory
restore plan. The trust prompt names the layout file and shows terminal-safe
command previews. Config absence and `PROJMUX_PROJECT_HOOKS=off` do not bypass
this layout gate; commandless layouts keep their existing prompt-free restore
behavior. Approval continues the selected path, while deny/cancel/error aborts
before session or pane creation, snapshot replay, or `send-keys`. Project-owned
layout names, descriptions, window names, agent topics, and command previews
escape terminal control characters before rendering. The Alt-1 sidebar does
not render trust approve/deny rows inline:
before trust is requested, projmux snapshots the sidebar query/selection
context and hands the selected open continuation to a detached tmux job. That
job closes the sidebar popup state for the client and opens the shared `Trust
project automation` popup as a client-scoped decision surface, so no code relies on
the self-closing sidebar popup process continuing after `display-popup -C`.
Deny/cancel or trust-popup errors return to the sidebar near the same
query/selection with a visible status message; only a missing/invalid context
may fall back to a closed popup plus tmux message. Existing sessions switch
directly without a startup picker or trust gate.

Default `projmux shell` no longer opens a compatibility startup picker and no
longer accepts startup selector flags for session-state restore. It always
follows the normal empty attach path after resolving the target app session name
and startup directory. Use `Settings > Session State > Sidebar startup picker` for
interactive Latest snapshot / Named snapshot / Empty session selection.
