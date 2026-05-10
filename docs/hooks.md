# Hooks

projmux runs an optional user script when it creates a new tmux session. The
hook is the project-agnostic extension point for things projmux itself stays out of:
injecting per-session env via `tmux set-environment`, picking a `GH_TOKEN` for
the repo, exporting a Kubernetes context, kicking off a background sync, etc.
projmux never ships behavior specific to any of those — that lives in the
hook.

## Where it lives

Global hook:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/hooks/post-create
```

Project-local hooks, discovered from the new session's `PROJMUX_CWD`:

```text
<repo>/.projmux/post-create
<repo>/.projmux/hooks/post-create
```

Each hook file must exist, be a regular file or symlink (not a directory), and
have the owner-execute bit set. Anything else is silently skipped — no warning,
no log.

```sh
mkdir -p ~/.config/projmux/hooks
chmod +x ~/.config/projmux/hooks/post-create
```

For project-local hooks, projmux runs at most one file: first
`.projmux/post-create` if executable, otherwise `.projmux/hooks/post-create` if
executable. Discovery does not walk parent directories and does not run hooks
from status, preview, or picker hot paths.

Project-local hooks are gated by trust-on-first-use. The global hook under
`$XDG_CONFIG_HOME` remains prompt-free, but a repository hook must be approved
before projmux runs it. Approving "always" records the hook content hash in:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/trusted-projects.json
```

The trust key is the absolute repository path and each executable hook path is
stored relative to that repository. Each project entry has a `trusted_at`
timestamp and a `files` map of relative hook paths to SHA-256 hashes. When the
file content changes, projmux asks again and shows the old and new SHA-256
hashes. In non-interactive contexts such as tmux run-shell or CI, untrusted or
changed project-local hooks fail closed with a warning and session creation
continues.

Set `PROJMUX_PROJECT_HOOKS=off` to disable project-local hook discovery
entirely. Project-local hooks can also be disabled from
`projmux settings` under Labs. The global hook still runs either way.

## When it runs

After projmux creates a brand-new tmux session via `EnsureSession` (the
persistent path used by `current` and `switch`) or `CreateEphemeralSession`
(used by `attach`). It does **not** run when projmux attaches to an existing
session.

If both a global hook and a project-local hook exist, projmux runs the global
hook first, then the project-local hook. A failure or timeout in either hook is
logged once and does not block session creation or the other hook.

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

### Global stub

```bash
#!/usr/bin/env bash
echo "session=$PROJMUX_SESSION cwd=$PROJMUX_CWD kind=$PROJMUX_SESSION_KIND"
```

### Project-local stub

```bash
mkdir -p .projmux
cat > .projmux/post-create <<'EOF'
#!/usr/bin/env bash
echo "project hook for $PROJMUX_CWD"
EOF
chmod +x .projmux/post-create
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

- **Nothing happens.** Check the execute bit on the global hook
  (`ls -l ~/.config/projmux/hooks/post-create`) or the project hook
  (`ls -l .projmux/post-create .projmux/hooks/post-create`). A missing bit
  makes projmux skip silently by design.
- **`project hook ... requires trust; skipping in non-interactive context`.**
  Run the same projmux command from an interactive terminal to approve the hook,
  or set `PROJMUX_PROJECT_HOOKS=off` if project-local hooks should be disabled.
- **`projmux: post-create hook: ... timed out after 5s`.** Long-running work
  belongs in a backgrounded child (`(slow-thing &) >/dev/null 2>&1`). The hook
  itself must return within 5s or projmux kills it.
- **`projmux: post-create hook: hook ... exited with status N`.** The script
  returned non-zero. projmux logs it once and moves on; the session is still
  created.
- **Lines appear with `[post-create] ` prefix.** Expected — that is how the
  hook's stdout/stderr is multiplexed into projmux's stderr stream.
