# Hooks

projmux runs an optional user script when it creates a new tmux session. The
hook is the project-agnostic seam for things projmux itself stays out of:
injecting per-session env via `tmux set-environment`, picking a `GH_TOKEN` for
the repo, exporting a Kubernetes context, kicking off a background sync, etc.
projmux never ships behavior specific to any of those — that lives in the
hook.

## Where it lives

```
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/hooks/post-create
```

The file must exist, be a regular file or symlink (not a directory), and have
the owner-execute bit set. Anything else is silently skipped — no warning,
no log. There is no enable flag.

```sh
mkdir -p ~/.config/projmux/hooks
chmod +x ~/.config/projmux/hooks/post-create
```

## When it runs

After projmux creates a brand-new tmux session via `EnsureSession` (the
persistent path used by `current` and `switch`) or `CreateEphemeralSession`
(used by `attach`). It does **not** run when projmux attaches to an existing
session.

The hook's exit code is ignored — session creation always succeeds.
Hook stdout and stderr are forwarded to projmux's stderr line-by-line,
prefixed with `[post-create] `. The hook is killed after 5 seconds.

## Environment

The hook inherits projmux's environment, plus:

| Variable | Always set | Description |
| --- | --- | --- |
| `PROJMUX_SESSION` | yes | tmux session name |
| `PROJMUX_CWD` | yes | absolute working directory of the new session |
| `PROJMUX_SESSION_KIND` | yes | `persistent` or `ephemeral` |
| `PROJMUX_VERSION` | yes | projmux version string |
| `PROJMUX_SOCKET` | only if projmux used `tmux -L <socket>` | tmux socket name |

## Examples

### Stub

```bash
#!/usr/bin/env bash
echo "session=$PROJMUX_SESSION cwd=$PROJMUX_CWD kind=$PROJMUX_SESSION_KIND"
```

### Per-session GH_TOKEN by repo

```bash
#!/usr/bin/env bash
set -euo pipefail

case "$PROJMUX_CWD" in
  "$HOME"/source/repos/personal/*)  token=$GH_TOKEN_PERSONAL ;;
  "$HOME"/source/repos/work/*)      token=$GH_TOKEN_WORK ;;
  *) exit 0 ;;
esac

tmux set-environment -t "$PROJMUX_SESSION" GH_TOKEN "$token"
```

`set-environment` only seeds the session env that newly-spawned panes inherit;
it does not retroactively change the current shell. Open new panes via tmux
(`Ctrl-b c`, `Ctrl-b "`, etc.) to pick up the value.

## Troubleshooting

- **Nothing happens.** Check the execute bit (`ls -l ~/.config/projmux/hooks/post-create`).
  A missing bit makes projmux skip silently by design.
- **`projmux: post-create hook: ... timed out after 5s`.** Long-running work
  belongs in a backgrounded child (`(slow-thing &) >/dev/null 2>&1`). The hook
  itself must return within 5s or projmux kills it.
- **`projmux: post-create hook: hook ... exited with status N`.** The script
  returned non-zero. projmux logs it once and moves on; the session is still
  created.
- **Lines appear with `[post-create] ` prefix.** Expected — that is how the
  hook's stdout/stderr is multiplexed into projmux's stderr stream.
