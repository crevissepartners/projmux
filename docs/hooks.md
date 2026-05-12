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
| `send-noti` | After `projmux notify push` (or the in-process AI notify producer) successfully writes a queue entry | Fired asynchronously and best-effort; queue write and desktop notifications continue even if the hook fails or times out | Receives JSON on stdin; stdout/stderr are logged with `[send-noti] ` |

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
<repo>/.projmux/config.toml
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
the global hook first, then the project-local hook, then any matching
declarative config command from `.projmux/config.toml`. For `pane-startup`, the
last non-empty trimmed stdout wins. Config `pane-startup` commands therefore
override file hooks deterministically.

## Project Config

Repositories may declare hook behavior in `.projmux/config.toml`. projmux
supports only this narrow TOML subset in Phase B:

```toml
[startup]
run = "git status --short"

[hooks.pre-create]
run = "echo checking"

[hooks.post-create]
run = "echo created $PROJMUX_SESSION"

[hooks.pane-startup]
run = "echo make test"

[hooks.post-attach]
run = "echo attached"

[hooks.send-noti]
run = "jq -r '.message' | xargs -I{} send-slack \"{}\""

[env]
FOO = "bar"

[kube]
context = "dev-cluster"
namespace = "tools"
```

Only quoted string values are supported. Unknown sections or keys make the
config file invalid for this run. Supported hook events are the public lifecycle
events listed above: `pre-create`, `post-create`, `pane-startup`,
`post-attach`, and `send-noti`. Internal tmux hook names such as
`after-select-pane` are rejected. Phase C secret interpolation is not
implemented; values are used literally.

`[hooks.<event>] run` executes through `sh -c` with the same timeout, logging,
environment, and failure model as file hooks. For `pane-startup`, stdout is
captured as the pane command.

`[hooks.send-noti] run` is the declarative forward path for queue-backed
notifications. It does not replace desktop notifications or the in-app queue;
it runs in parallel after the queue write succeeds so users can fan out to
Slack, webhooks, or custom scripts without changing the built-in notification
path.

`[startup] run` is a direct shorthand for a startup pane command. It is not
executed as shell by the hook runner; the string itself is sent to the new pane.
When both `[hooks.pane-startup] run` and `[startup] run` are present,
`[startup] run` wins because it is applied last.

`[env]` values are added to hook process environments and to newly-created tmux
session environments. For new sessions, projmux passes them to
`tmux new-session` as sorted `-e KEY=VALUE` arguments before the first pane is
created, so the initial shell and `[startup]` command can see them. projmux also
refreshes the tmux session environment with `tmux set-environment -t <session>
KEY VALUE` after creation for later panes. projmux's reserved `PROJMUX_*` hook
variables are appended after `[env]` in hook process environments, so the hook
contract cannot be overridden by project config.

`[kube] context` and `namespace` are reflected as hook and session environment:
`PROJMUX_KUBE_CONTEXT`, `KUBE_CONTEXT`, `PROJMUX_KUBE_NAMESPACE`, and
`KUBE_NAMESPACE`. projmux does not synthesize kubeconfig files from these two
values.

## Trust Model

Global hooks under `$XDG_CONFIG_HOME` are prompt-free.

Project-local hooks and `.projmux/config.toml` are gated by trust-on-first-use
for every user-facing hook event in this file. A repository hook file or config
file must be approved before projmux runs or applies it. Approving "always"
records the file content hash in:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/trusted-projects.json
```

The trust key is the absolute repository path and each executable hook or config
path is stored relative to that repository. Each project entry has a
`trusted_at` timestamp and a `files` map of relative paths to SHA-256 hashes.
When file content changes, projmux asks again and shows the old and new SHA-256
hashes. In non-interactive contexts such as tmux run-shell or CI, untrusted or
changed project-local files fail closed with a warning.

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

`pane-startup` is on the deprecation path. projmux still runs it today, but
`projmux hook list --effective` and the Settings effective-merge view label it
as `pane-startup (deprecated)`, and execution logs a warning that points users
to `[startup] run`. Migrate new startup commands to `[startup]` now; the
compatibility shim is planned to disappear in the next breaking release.

## Send Noti

`send-noti` fires only after the notify queue write succeeds. The queue entry is
already durable before the hook starts, so a failing or slow hook cannot drop
the notification or block the normal desktop notification flow.

The hook runs asynchronously with the same default timeout (`5s`) as other
hooks. projmux does not wait for completion before returning from
`projmux notify push`.

stdin receives one JSON object:

```json
{
  "event": "send-noti",
  "id": "ai:main:%9",
  "type": "ai-reply-ready",
  "agent": "claude",
  "topic": "worker loop",
  "pane": "%9",
  "session": "main",
  "message": "claude: reply ready · worker loop",
  "created_at": "2026-05-12T02:03:04Z"
}
```

The hook environment also includes:

| Variable | Description |
| --- | --- |
| `PROJMUX_NOTIFY_ID` | Queue entry id |
| `PROJMUX_NOTIFY_TYPE` | Notification kind (`ai-reply-ready`, `external`, etc.) |
| `PROJMUX_NOTIFY_AGENT` | AI agent label when known |
| `PROJMUX_NOTIFY_TOPIC` | AI topic when known |
| `PROJMUX_NOTIFY_PANE` | Target pane id when known |
| `PROJMUX_NOTIFY_SESSION` | Target tmux session |
| `PROJMUX_NOTIFY_MESSAGE` | Human-readable message text |
| `PROJMUX_NOTIFY_HOOK_DEPTH` | Recursion guard; values `>= 1` suppress nested `send-noti` dispatch |

If a `send-noti` hook itself calls `projmux notify push`, projmux sees
`PROJMUX_NOTIFY_HOOK_DEPTH=1` in the child environment and skips another
`send-noti` hook fire. The queue write itself still succeeds.

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
  projmux skip hook files silently by design. `.projmux/config.toml` does not
  need an execute bit.
- **`project hook ... requires trust; skipping in non-interactive context`** or
  **`project config ... requires trust; skipping in non-interactive context`.**
  Run the same projmux command from an interactive terminal to approve the file,
  or set `PROJMUX_PROJECT_HOOKS=off` if project-local execution should be
  disabled.
- **`projmux: <event> hook: ... timed out after 5s`.** Long-running work
  belongs in a backgrounded child (`(slow-thing &) >/dev/null 2>&1`). The hook
  itself must return within 5s or projmux kills it.
- **`projmux: <event> hook: hook ... exited with status N`.** The script
  returned non-zero. For `pre-create`, creation aborts; for other events,
  projmux logs once and moves on.
- **Lines appear with `[post-create] `, `[pre-create] `, or `[post-attach] `
  prefixes.** Expected; hook stdout/stderr are multiplexed into projmux's
  stderr stream. `pane-startup` stdout is captured as the pane command instead.
