# Hooks

projmux runs optional user scripts at selected tmux lifecycle points. Hooks are
the project-agnostic extension point for behavior projmux itself stays out of:
injecting per-session env via `tmux set-environment`, selecting repository
tokens, exporting a Kubernetes context, kicking off a background sync, or
sending an initial pane command.

projmux-owned internal tmux hooks such as `pane-focus-in`, `pane-focus-out`,
`after-select-pane`, and `after-kill-pane` are not exposed as user hook events.

## Events

| Event | When it runs | Failure behavior | Stdout behavior |
| --- | --- | --- | --- |
| `pre-create` | Before projmux creates a missing persistent or ephemeral session | Non-zero exit, exec error, or timeout aborts creation | Logged with `[pre-create] ` |
| `post-create` | After projmux creates a brand-new persistent or ephemeral session | Logged and ignored; creation continues | Logged with `[post-create] ` |
| `pane-startup` | After `post-create`, once the initial pane of a brand-new session reaches a shell prompt | Logged and ignored; empty output is no-op | Captured as the command to send into the pane |
| `post-attach` | After projmux switches the current tmux client to an existing session/target from inside tmux | Logged and ignored | Logged with `[post-attach] ` |

Deferred Phase A candidates remain future work until their behavior can be
specified without exposing projmux's internal tmux hook machinery: pane exit,
window create/rename, and focus-change hook events.

## Where Hooks Live

Global hooks live under the XDG config directory:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/hooks/<event>
```

For example:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/hooks/post-create
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/hooks/pane-startup
```

Project-local hooks are discovered from the lifecycle context's `PROJMUX_CWD`:

```text
<repo>/.projmux/<event>
<repo>/.projmux/hooks/<event>
```

For example, `pane-startup` discovery checks:

```text
<repo>/.projmux/pane-startup
<repo>/.projmux/hooks/pane-startup
```

Each hook file must exist, be a regular file or symlink (not a directory), and
have the owner-execute bit set. Anything else is silently skipped.

For each event, projmux runs at most one project-local file: first
`.projmux/<event>` if executable, otherwise `.projmux/hooks/<event>` if
executable. Discovery does not walk parent directories and does not run hooks
from status, preview, or picker hot paths.

If both a global hook and a project-local hook exist for an event, projmux runs
the global hook first, then the project-local hook. For `pane-startup`, the last
non-empty trimmed stdout wins, so a project-local hook can override the global
startup command.

## Trust Model

Global hooks under `$XDG_CONFIG_HOME` are prompt-free.

Project-local hooks are gated by trust-on-first-use for every user-facing hook
event in this file. A repository hook must be approved before projmux runs it.
Approving "always" records the hook content hash in:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/trusted-projects.json
```

The trust key is the absolute repository path and each executable hook path is
stored relative to that repository. Each project entry has a `trusted_at`
timestamp and a `files` map of relative hook paths to SHA-256 hashes. When the
file content changes, projmux asks again and shows the old and new SHA-256
hashes. In non-interactive contexts such as tmux run-shell or CI, untrusted or
changed project-local hooks fail closed with a warning.

Set `PROJMUX_PROJECT_HOOKS=off` to disable project-local hook discovery
entirely. Project-local hooks can also be disabled from `projmux settings`
under Labs. The global hook still runs either way.

## Pane Startup

`pane-startup` runs only for the initial pane created with a new
projmux-managed session. It runs after `post-create` so `post-create` can seed
session-level tmux environment before the startup command is sent. It does not
run when projmux attaches to an existing session or target.

Before running `pane-startup`, projmux polls tmux `pane_current_command` for the
new pane and waits until it reports a shell command. This avoids fixed sleeps as
the primary readiness mechanism.

The hook's trimmed stdout is treated as the command to send into the new pane:

```text
tmux send-keys -t <pane-id> <command> Enter
```

Empty stdout is a no-op. Hook stderr is still forwarded to projmux's stderr with
the `[pane-startup] ` prefix. If both global and project-local hooks emit a
command, the project-local command wins because project hooks run after global
hooks.

## Pre Create Abort

`pre-create` runs before `tmux new-session` on creation paths. A non-zero exit,
exec error, or timeout aborts the session creation. If a global `pre-create`
hook aborts, the project-local `pre-create` hook does not run.

This is the only Phase A hook event that can block creation.

## Post Attach

`post-attach` is supported only when projmux is already running inside tmux and
uses `switch-client` to move the current client to an existing session or
target. Outside tmux, `tmux attach-session` blocks until the user detaches, so
projmux does not run `post-attach` for outside-tmux attach paths in this phase.

## Environment

Hooks inherit projmux's environment, plus:

| Variable | Always set | Description |
| --- | --- | --- |
| `PROJMUX_SESSION` | yes | tmux session name |
| `PROJMUX_CWD` | yes | lifecycle working directory; for creation events this is the new session directory |
| `PROJMUX_SESSION_KIND` | yes | `persistent` or `ephemeral` for creation events; empty for `post-attach` |
| `PROJMUX_VERSION` | yes | projmux version string |
| `PROJMUX_SOCKET` | only if projmux used `tmux -L <socket>` | tmux socket name |
| `PROJMUX_PANE` | only for pane events | tmux pane id such as `%7` |

## Examples

### Global Post Create Stub

```bash
#!/usr/bin/env bash
echo "session=$PROJMUX_SESSION cwd=$PROJMUX_CWD kind=$PROJMUX_SESSION_KIND"
```

### Project Pane Startup Command

```bash
mkdir -p .projmux
cat > .projmux/pane-startup <<'EOF'
#!/usr/bin/env bash
echo "git status --short"
EOF
chmod +x .projmux/pane-startup
```

### Per Session GH_TOKEN By Repo

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
  (`ls -l ~/.config/projmux/hooks/<event>`) or the project hook
  (`ls -l .projmux/<event> .projmux/hooks/<event>`). A missing bit makes
  projmux skip silently by design.
- **`project hook ... requires trust; skipping in non-interactive context`.**
  Run the same projmux command from an interactive terminal to approve the hook,
  or set `PROJMUX_PROJECT_HOOKS=off` if project-local hooks should be disabled.
- **`projmux: <event> hook: ... timed out after 5s`.** Long-running work
  belongs in a backgrounded child (`(slow-thing &) >/dev/null 2>&1`). The hook
  itself must return within 5s or projmux kills it.
- **`projmux: <event> hook: hook ... exited with status N`.** The script
  returned non-zero. For `pre-create`, creation aborts; for other events,
  projmux logs once and moves on.
- **Lines appear with `[post-create] `, `[pre-create] `, or `[post-attach] `
  prefixes.** Expected; hook stdout/stderr are multiplexed into projmux's
  stderr stream. `pane-startup` stdout is captured as the pane command instead.
