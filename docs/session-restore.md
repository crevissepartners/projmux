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

The primary inspection surface is Settings > Project > Session State. It uses
the current project path to derive the same session identity as `projmux shell`,
then shows project context, snapshot source/age, and a window-title -> pane-title
preview before count metadata. The legacy Settings > Session State path remains
available for the current live tmux session.

Project Session State actions are scoped to the project-derived session
identity, not the currently attached tmux session. `Save snapshot` captures the
live project session when that session exists, `Preview restore` prints the
read-only dry-run restore plan for the project snapshot, and `Delete snapshot`
requires a confirmation picker before removing the project snapshot. Destructive
restore execution remains outside Settings in this slice.

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

Delete for current-session snapshots, auto-save/startup picker toggles, and
restore execution remain in Settings or CLI for now. Destructive restore
execution stays deferred until the popup has a dedicated safe policy for
existing or non-empty live sessions.

Named snapshots may currently be backed by legacy project files in
`<project>/.projmux/layouts/*.toml`. They reuse the same window, pane, cwd, and
startup recipe concepts as session snapshots. They are discoverable with
`projmux layout list`, inspectable with
`projmux layout show <name>`, capturable from the current tmux session with
`projmux layout save <name>`, and removable with
`projmux layout remove --force <name>`.

`projmux layout apply <name> --dry-run` is the safe apply precursor. It requires
a current tmux session, converts the named snapshot to the session-state shape
for that session, and prints the same restore preview/read model as
`projmux session-state restore --dry-run`. It does not execute replay commands,
does not autosave the live session, and does not update the latest snapshot.
The preview includes a legacy source label: `layout(<name>)` for normal named snapshots or
`fresh` for named snapshots stored with `mode = "fresh-each-time"`.

`projmux layout apply <name> --force` is the conservative destructive live
apply path for named snapshots. It requires a current tmux session, uses that
current session name as the only target, stages the converted snapshot through
the session-state replay path, moves the staged windows into the live session,
and removes extra live windows. General `session-state restore` execution
remains dry-run-only; the live overwrite policy is currently scoped to this
legacy command. Applying a fresh named snapshot marks the live tmux session as
`fresh`, which the autosave tick honors by skipping autosave for that session. A
successful fresh apply also removes the current latest snapshot for that
session, preventing an older auto-saved row from reappearing in the next startup
picker.

Project open is the canonical startup picker path. Opening a closed project
session from the Alt-1 sidebar checks trust first, then shows `Start project`
when the startup picker is enabled. Rows are ordered `Latest snapshot`, named
snapshot rows, then `Empty session`. `Latest snapshot` is the auto-saved
snapshot that keeps changing as auto-save runs. `Named snapshot` is a fixed,
user-named snapshot and is not updated by auto-save. `Empty session` creates a
new session without restoring a snapshot. Existing sessions switch directly
without a startup picker.

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
