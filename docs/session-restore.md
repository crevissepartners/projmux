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

The statusbar Session State popup now exposes a minimal action picker backed by
the same read model and CLI behavior:

- `Save now` captures the current tmux session, bypassing the autosave debounce
  just like `projmux session-state save`.
- `Preview restore` prints the dry-run restore plan and does not execute tmux
  replay commands.

Delete, auto-save/auto-restore toggles, and restore execution remain in Settings
or CLI for now. Destructive restore execution stays deferred until the popup has
a dedicated safe policy for existing or non-empty live sessions.

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

`projmux layout apply <name> --force` is the conservative destructive live
apply path for project presets. It requires a current tmux session, uses that
current session name as the only target, stages the converted snapshot through
the session-state replay path, moves the staged windows into the live session,
and removes extra live windows. General `session-state restore` execution
remains dry-run-only; the live overwrite policy is currently scoped to layout
presets.
