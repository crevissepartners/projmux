# Shell Auto-Start

If you want every new interactive bash or zsh shell to drop you straight into
the projmux app, add a guarded hook to `~/.bashrc`, `~/.zshrc`, or the
equivalent interactive rc file for your shell:

```sh
if [[ $- == *i* && -z "${TMUX:-}" ]] && command -v projmux >/dev/null 2>&1; then
  exec projmux shell
fi
```

The three guards each prevent a common breakage:

- `$- == *i*` — only fire for interactive shells. Without this you would
  break `scp`, `ssh host cmd`, `git` over SSH, and any `bash -c '...'` or
  `zsh -c '...'` invocation.
- `-z "${TMUX:-}"` — skip when already inside tmux. Without this the hook
  recurses every time projmux opens a new pane.
- `command -v projmux >/dev/null 2>&1` — skip on machines where projmux is not
  installed yet. Without this a fresh login on a new box hangs at a missing
  binary.

To bypass the hook for one shell, set `TMUX` before launching:

```sh
TMUX=1 bash
TMUX=1 zsh
```

This is **opt-in** behavior. projmux does not assume you want every shell to
auto-start the app; the snippet above is here only as a known-safe starting
point for users who do.
