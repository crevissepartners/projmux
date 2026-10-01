## Naming metadata model

Projmux keeps visible naming separate from source metadata:

- **User pane label** is pane-scoped metadata stored in `@projmux_pane_label`,
  the live mirror of the Pane's Registry `metadata.name`. The Rename Pane action
  renames the Registry Pane through the same owner as `rename pane`, which
  writes this mirror; an empty response changes nothing, and the action never
  writes the AI topic or raw pane title.
- **Pane border label** is the primary visible pane name. In the app tmux
  config and native previews it resolves to user pane label first, agent AI
  topic second, known interactive shell command (`zsh`, `bash`, `fish`, `sh`,
  `nu`, `xonsh`) third, and raw pane title last.
- **Window tab name** follows the active pane's visible pane label through the
  same tmux format expression used by the pane border. Historically the app
  config used raw `#{pane_title}` for `automatic-rename-format`, which let shell
  OSC titles such as branch names diverge from the pane border; generated app
  config now keeps the two aligned.
- **Terminal / pane title** remains raw title metadata owned by the running app
  or shell. It is still available to tmux and to Projmux features that need
  title evidence, but it is not the canonical Projmux window naming source.
- **AI topic** is agent-owned naming metadata stored in `@projmux_ai_topic`.
  Its set/clear CLI and watcher manual-ownership behavior remain independent of
  user pane labels.
- **Git branch** belongs in the statusbar git segment. Branch-based terminal
  title overwrites are not promoted to the primary Projmux pane or window name.
