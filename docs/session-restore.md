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

The statusbar Session State popup remains a secondary shortcut backed by the
same read model and CLI behavior:

- `Save now` captures the current tmux session, bypassing the autosave debounce
  just like `projmux session-state save`.
- `Preview restore` prints the dry-run restore plan and does not execute tmux
  replay commands.

Delete for current-session snapshots, auto-save/startup picker toggles, and
restore execution remain in Settings or CLI for now. Destructive restore
execution stays deferred until the popup has a dedicated safe policy for
existing or non-empty live sessions.

Project layout presets in `<project>/.projmux/layouts/*.toml` reuse the same
window, pane, cwd, and startup recipe concepts as session snapshots. They are
discoverable with `projmux layout list`, inspectable with
`projmux layout show <name>`, capturable from the current tmux session with
`projmux layout save <name>`, and removable with
`projmux layout remove --force <name>`.

`projmux layout apply <name> --dry-run` is the safe apply precursor. It requires
a current tmux session, converts the preset to a session-state snapshot for that
session, and prints the same restore preview/read model as
`projmux session-state restore --dry-run`. It does not execute replay commands,
does not autosave the live session, and does not update the saved snapshot.
The preview includes a source label: `layout(<name>)` for normal presets or
`fresh` for `mode = "fresh-each-time"` presets.

`projmux layout apply <name> --force` is the conservative destructive live
apply path for project presets. It requires a current tmux session, uses that
current session name as the only target, stages the converted snapshot through
the session-state replay path, moves the staged windows into the live session,
and removes extra live windows. General `session-state restore` execution
remains dry-run-only; the live overwrite policy is currently scoped to layout
presets. Applying a fresh preset marks the live tmux session as `fresh`, which
the autosave tick honors by skipping autosave for that session. A successful
fresh apply also removes the current saved snapshot for that session, preventing
an older saved row from reappearing in the next startup picker.

`projmux shell` opens an interactive startup picker before creating a new app
session when the startup picker is enabled and saved snapshot or project layout
candidates exist. Fresh project targets with no saved snapshot or layout preset
also show the empty-session row as a blocking startup decision. When saved or
preset candidates exist, the picker lists the saved snapshot first, then project
presets alphabetically, then empty session. Choosing a saved snapshot uses the
same saved replay path as the startup picker, choosing a preset converts it to a
session-state snapshot and replays it, and choosing empty keeps the prior
empty-session behavior. Closing the picker or making no selection falls back to
empty startup. When the startup picker is disabled, default `projmux shell` skips
candidate lookup and the picker, then follows the normal empty attach path.

The explicit startup selectors `--saved`, `--layout <name>`, and `--empty`
bypass the picker and startup picker toggle, then route directly to the same
saved, preset, or empty startup paths. All startup selectors are only applied
before a new target app session exists; existing sessions are attached without
picker display or replay.
