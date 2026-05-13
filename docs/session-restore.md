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

Agent panes in Settings, the statusbar popup, and restore dry-run previews show
resume metadata health next to the pane recipe. `available` means a resume id is
present and recent enough for replay, `stale` means the stored id exists but the
metadata is missing or older than the snapshot policy, and `unavailable`
means projmux cannot safely resume that agent pane from the snapshot. Confidence
is derived from the metadata source: direct session ids and hook ingest are high
confidence, transcript/log fallbacks are medium confidence, and missing or
unknown sources are low or none.

The statusbar Session State popup remains a secondary shortcut backed by the
same read model and CLI behavior:

- `Save snapshot` captures the current tmux session, bypassing the autosave debounce
  just like `projmux session-state save`.
- `Preview restore` prints the dry-run restore plan and does not execute tmux
  replay commands.

Settings > Session State is global settings only: global auto-save, global
startup picker, and storage/retention policy. It does not show the current
snapshot tree. Delete for current-session snapshots and destructive restore
execution stay deferred until those actions have a dedicated safe policy for
existing or non-empty live sessions.

Named snapshots may currently be backed by legacy project files in
`<project>/.projmux/layouts/*.toml`. They reuse the same window, pane, cwd, and
startup recipe concepts as session snapshots, but the user-facing restore model
is still `Latest snapshot`, `Named snapshot`, or `Empty session`. The legacy
files are imported read-only by Project open and shell compatibility startup
when building `Named snapshot` candidates; new primary surfaces should describe
the restore unit as a snapshot, not as a separate layout or preset feature.

Project open from the Alt-1 sidebar defaults to opening a closed project as an
`Empty session`. `Settings > Labs > Sidebar startup picker` is an opt-in toggle;
when it is on, closed project open advances inside the sidebar to the native
`Start project` step. Rows are ordered `Latest snapshot`, named snapshot rows,
`Empty session`, then `Back`. `Latest snapshot` is the auto-saved snapshot that
keeps changing as auto-save runs. `Named snapshot` is a fixed, user-named
snapshot and is not updated by auto-save. Rows include saved-at date/time
metadata when available. `Back` returns to the project list without creating,
replaying, or opening a session. After the startup mode is selected, project
hook/config trust is evaluated if needed; approval continues the selected path
and deny/cancel aborts before session create, snapshot replay, startup recipe,
or `pane-startup`. Existing sessions switch directly without a startup picker.

`projmux shell` still accepts the legacy startup selectors in this release, but
it is no longer the primary project restore model. Its compatibility picker is
titled `Start app session` and uses the same row labels: `Latest snapshot`,
`Named snapshot`, and `Empty session`. Closing the picker or making no selection
falls back to empty startup. When the startup picker is disabled, default
`projmux shell` skips candidate lookup and the picker, then follows the normal
empty attach path.

The explicit startup selectors `--saved`, `--layout <name>`, and `--empty`
bypass the picker and startup picker toggle, then route directly to the same
latest snapshot, named snapshot, or empty startup paths. The flag names are kept
for compatibility in this phase. All startup selectors are only applied before a
new target app session exists; existing sessions are attached without picker
display or replay.
