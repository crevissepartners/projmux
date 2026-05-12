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
`projmux layout remove --force <name>`. Applying presets to live tmux sessions
remains deferred.
