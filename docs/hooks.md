# Hooks

projmux runs optional user scripts at selected tmux lifecycle points. Hooks are
the project-agnostic extension point for behavior projmux itself stays out of:
injecting per-session env via `tmux set-environment`, selecting repository
tokens, kicking off a background sync, or sending an initial pane command.

projmux-owned internal tmux hooks such as `pane-focus-in`, `pane-focus-out`,
`after-select-pane`, and `after-kill-pane` are not exposed as user hook events.

## Events

| Event | When it runs | Failure behavior | Stdout behavior |
| --- | --- | --- | --- |
| `pre-create` | Before projmux creates a missing persistent or ephemeral session | Non-zero exit, exec error, or timeout aborts creation | Logged with `[pre-create] ` |
| `post-create` | After projmux creates a brand-new persistent or ephemeral session | Logged and ignored; creation continues | Logged with `[post-create] ` |
| `post-attach` | After projmux switches the current tmux client to an existing session/target from inside tmux | Logged and ignored | Logged with `[post-attach] ` |
| `send-noti` | After `projmux create notification` (or the in-process AI notify producer) successfully writes a queue entry | Fired asynchronously and best-effort; queue write and desktop notifications continue even if the hook fails or times out | Receives JSON on stdin; stdout/stderr are logged with `[send-noti] ` |

Deferred Phase A candidates remain future work until their behavior can be
specified without exposing projmux's internal tmux hook machinery: pane exit,
window create/rename, and focus-change hook events.

## Where Hooks Live

Global hooks live in the XDG config file:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/config.toml
```

Project-local hooks are discovered from the lifecycle context's `PROJMUX_CWD`:

```text
<repo>/.projmux/config.toml
```

File-form hooks from the historical `.projmux/<event>` and
`.projmux/hooks/<event>` layouts are no longer executed. Use declarative
`[hooks.<event>] run` entries instead.

If both a global hook and a project-local hook exist for an event, projmux runs
the global hook first, then the project-local hook.

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

[hooks.post-attach]
run = "echo attached"

[hooks.send-noti]
run = "jq -r '.message' | xargs -I{} send-slack \"{}\""

[env]
FOO = "bar"
```

Only quoted string values are supported. Unknown sections or keys make the
config file invalid for this run. Supported hook events are the public lifecycle
events listed above: `pre-create`, `post-create`, `post-attach`, and
`send-noti`. Internal tmux hook names such as
`after-select-pane` are rejected. Phase C secret interpolation is not
implemented; values are used literally.

`[hooks.<event>] run` executes through `sh -c` with the same timeout, logging,
environment, and failure model for all hook events.

`[hooks.send-noti] run` is the declarative forward path for queue-backed
notifications. It does not replace desktop notifications or the in-app queue;
it runs in parallel after the queue write succeeds so users can fan out to
Slack, webhooks, or custom scripts without changing the built-in notification
path.

`[startup] run` is a direct shorthand for a startup pane command. It is not
executed as shell by the hook runner; the string itself is sent to the new pane.

`[env]` values are added to hook process environments and to newly-created tmux
session environments. For new sessions, projmux passes them to
`tmux new-session` as sorted `-e KEY=VALUE` arguments before the first pane is
created, so the initial shell and `[startup]` command can see them. projmux also
refreshes the tmux session environment with `tmux set-environment -t <session>
KEY VALUE` after creation for later panes. projmux's reserved `PROJMUX_*` hook
variables are appended after `[env]` in hook process environments, so the hook
contract cannot be overridden by project config.

### Removed `[kube]` input

The former `[kube]` section is no longer a product or runtime feature. projmux
does not project Kube-specific hook/session variables and does not rewrite a
legacy file. If a config still contains `[kube]`, every config write stops
before changing the file and reports this exact diagnostic:

```text
legacy [kube] support was removed; manually move context to [env] KUBE_CONTEXT and namespace to [env] KUBE_NAMESPACE, then remove [kube]; original config was not changed
```

Migration is deliberately manual: copy the former `context` value to an
explicit `KUBE_CONTEXT` key under generic `[env]`, and the former `namespace`
value to `KUBE_NAMESPACE`, then remove `[kube]`. Those names are examples, not
special fields—`[env]` preserves any valid user-supplied key verbatim and no
`PROJMUX_KUBE_*` variable is synthesized.

## Trust Model

Global hooks under `$XDG_CONFIG_HOME` are prompt-free.

Project-local executable automation is gated by trust-on-first-use. This
includes `.projmux/config.toml` before projmux runs hooks or applies
startup/session environment settings, and a selected
`.projmux/layouts/*.toml` named snapshot before projmux replays any declared
`command`.
Approving "always"
records the file content hash in:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/trusted-projects.json
```

The trust key is the absolute repository path and each artifact path is stored
relative to that repository. Each project entry has a
`trusted_at` timestamp and a `files` map of relative paths to SHA-256 hashes.
When file content changes or the layout path is replaced, projmux asks again
and shows the old and new SHA-256 hashes. Layout symlinks, including symlinked
`.projmux` or `layouts` path components, are rejected. The selected layout is
read once; those exact bytes are both hashed and parsed, and the resulting
in-memory snapshot is what restore uses. In non-interactive contexts such as
tmux run-shell or CI, untrusted or changed project-local executable files fail
closed with a warning.

Set `PROJMUX_PROJECT_HOOKS=off` to disable project-local hook discovery
entirely. Project-local hooks can also be disabled from `projmux settings`
under Labs. The global hook still runs either way. This setting does not trust
or bypass executable named-snapshot commands. A layout without a startup
`command` does not require executable-artifact approval.

## Startup Commands

`[startup] run` applies only to the initial pane created with a new
projmux-managed session. It runs after `post-create` so `post-create` can seed
session-level tmux environment before the startup command is sent. It does not
run when projmux attaches to an existing session or target.

Before sending the startup command, projmux polls tmux `pane_current_command`
for the new pane and waits until it reports a shell command. This avoids fixed
sleeps as the primary readiness mechanism.

The configured string is sent into the new pane:

```text
tmux send-keys -t <pane-id> <command> Enter
```

An empty `[startup] run` is a no-op.

## Send Noti

`send-noti` fires only after the notify queue write succeeds. The queue entry is
already durable before the hook starts, so a failing or slow hook cannot drop
the notification or block the normal desktop notification flow.

The hook runs asynchronously with the same default timeout (`5s`) as other
hooks. projmux does not wait for completion before returning from
`projmux create notification`.

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
  "message": "worker loop",
  "metadata": {
    "agent": "claude",
    "category": "response_complete",
    "state": "need"
  },
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

If a `send-noti` hook itself calls `projmux create notification`, projmux sees
`PROJMUX_NOTIFY_HOOK_DEPTH=1` in the child environment and skips another
`send-noti` hook fire. The queue write itself still succeeds.

`Settings > Notifications > Delivery sources` surfaces the active Codex,
Claude, Antigravity, and tmux AI notify diagnostics: status, conflicts, config
paths, and copyable CLI install/remove/dry-run commands. It also shows whether
`PROJMUX_NOTIFY_HOOK` overrides the built-in desktop sender. It does not install
or remove external Codex, Claude, Antigravity, or tmux settings.

`PROJMUX_NOTIFY_HOOK` is separate from `[hooks.send-noti]`: it replaces the
desktop sender and receives positional arguments
`summary body urgency app-name tag group icon-path`. That `urgency` value is
the OS notification urgency, not the notify-queue severity. AI approval,
input, selection, and confirmation rows can stay critical in the queue and UI
while the desktop notification hook receives `normal`. Live AI status badges
are a third surface: permission/input-required panes use the action-required
amber-orange status role, response-complete panes use success green, and
in-progress panes use progress yellow. They do not inherit the critical queue
severity, and permission/input status badges do not use red.

## Codex Hooks Engine

`projmux doctor` reports Codex hooks-engine wiring separately from legacy
notify, including unmanaged hooks/conflict details and the relevant
`projmux agent integrate codex` commands.

`projmux internal agent-hook ingest codex-hook` is the conservative core ingest path for Codex
hooks-engine events. It reads a single JSON payload from stdin. The embedded
default install catalog is based on Codex CLI 0.130.0:

For an exact native app-server Agent, this raw hook path is fallback-only. The
pane is marked `pending` before its lifecycle observer starts; `pending`,
`provider-control-plane`, and `invalidating` authority suppress every Codex hook
event before any badge, queue, desktop, or Registry interaction write. Only
after the native epoch is invalidated may `provider-hook` become current and the
table below apply. Existing hook installation and runtime overrides are kept
byte-for-byte. `PermissionRequest` and `Stop` use the same semantic policy as
native lifecycle events unless an explicit raw runtime override exists; that
override regains its existing meaning only in fallback. Catalog defaults are
not semantic overrides.

| Event | Behavior |
| --- | --- |
| `PreToolUse` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `UserPromptSubmit` | marks the matched pane hook-active and sets AI state to thinking/busy; no notify queue entry is pushed |
| `PermissionRequest` | pushes a critical approval row with the tool name and a concise tool/action summary |
| `PostToolUse` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PreCompact` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PostCompact` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `SessionStart` | marks the matched pane hook-active and writes a quiet ingest diagnostic; for an exact managed initial-task binding it records `pending` startup readiness and opens the separately bounded acknowledgement window, but never acknowledges the task; no notify queue entry is pushed |
| `Stop` | pushes an info Codex completion row |

Codex hook payload parsing accepts the common fields
`hook_event_name`/`event_name`, `thread_id`, `session_id`, `turn_id`, `cwd`,
`transcript_path`, `model`, `tool_name`, nested `tool.name`, `tool_input`, and
`input`. For pane matching, Codex hook ingest uses `thread_id` when available
and falls back to treating `session_id` as the thread identity so existing
`matchAIPane` matching can reuse cached pane metadata.

`projmux agent integrate codex` manages a separate
`~/.codex/config.toml` marker block for the hooks engine. If a `[features]`
table already exists, projmux merges `hooks = true` into that table instead of
creating a duplicate table. Older projmux-managed `codex_hooks = true` entries
are migrated to `hooks = true` to avoid Codex's deprecation warning:

```toml
[features]
hooks = true

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PermissionRequest]]
matcher = "*"
[[hooks.PermissionRequest.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PostToolUse]]
matcher = "*"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PreCompact]]
matcher = "*"
[[hooks.PreCompact.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.PostCompact]]
matcher = "*"
[[hooks.PostCompact.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.SessionStart]]
matcher = "*"
[[hooks.SessionStart.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.UserPromptSubmit]]
matcher = "*"
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"
```

Repeated installs are idempotent and preserve unrelated Codex config, including
unmanaged hook entries for the same events. If projmux sees a
`projmux internal agent-hook ingest codex-hook` command it did not author —
a different matcher, a wrapped command, or an extra key — it refuses to install
over it rather than guessing ownership. `--dry-run` previews the TOML update.
`--remove` removes projmux-managed Codex hooks wiring.

The marker block is not a projmux-owned byte range. Projmux owns only the hook
definitions it wrote and, inside the block, the `[features] hooks = true`
toggle; everything else in the file belongs to Codex or to you and is handed
back verbatim. That split is what keeps hook trust alive across convergence:
Codex records the hooks you approved as `[hooks.state]` subtables holding a
`trusted_hash`, and it can write them anywhere in the same file, including
between the projmux markers. Convergence lifts that state back out of the block
unchanged instead of rewriting the block wholesale, so `projmux agent integrate
codex`, `projmux config apply`, and the `make install` convergence leave every
approval you already made in place.

A Codex TOML re-serialization can also drop the marker comments while leaving
the hook wiring projmux wrote behind. Projmux recognizes that wiring as its own
and restores the markers around the byte-identical definitions rather than
refusing, so a marker-less config still converges and keeps its trust. Recovery
stops where ownership does: hook entries you hand-wrote keep the refusal above,
and so does any layout where re-adopting the projmux entries would renumber a
hook entry projmux does not own, because Codex keys trust state by array
position.

The Codex hooks install list is catalog-driven. Projmux ships an embedded
default catalog at `internal/app/ai_hook_catalogs/codex.json`, and merges an
optional local override from:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/codex.json
```

Override events replace matching embedded events by name and append new names,
so a local file can disable a stale event with `"install": false` or add a new
Codex event before projmux itself is released. Supported `action` values are
`notify`, `state`, and `quiet`; install uses only events whose effective
`install` value is true. Removal remains marker-block based rather than catalog
based, so older projmux-managed Codex hook blocks are still removed after the
catalog changes.

```json
{
  "provider": "codex",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

Codex ingest has built-in notify/state handlers for the known reply-ready,
approval, and prompt-submit events. Events without a specialized handler,
including override-added events, are quiet/log-only after pane matching; a
catalog `"action": "quiet"` makes that quiet fallback explicit. Catalog
`"notify"` and `"state"` entries still need a built-in handler before they can
push queue rows or change pane state.

Runtime action overrides are stored separately from the install catalog at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json
```

`Settings > Notifications > Agent event behavior` reads and writes that file. The
runtime file only changes ingest behavior (`notify`, `state`, or `quiet`);
`projmux agent integrate codex` still uses the catalog `install` field to decide
which hooks to write. Runtime overrides also apply to known specialized
events, so `Stop` or `PermissionRequest` can be made state-only or quiet
without changing which hook commands are installed while hook fallback is
current. Without such an explicit override, app-server and hook-fallback
approval/completion use the same semantic policy described in
[Configuration](configuration.md#notifications). When a known Codex event
without a specialized handler, such as `PreToolUse` or `PostToolUse`, is set to
runtime `notify`, projmux pushes a generic in-app notify row such as
`PreToolUse · Bash` with agent/category metadata. That generic path is
queue/sidebar/statusbar only: it does not dispatch `[hooks.send-noti]`,
`PROJMUX_NOTIFY_HOOK`, `notify-send`, or Windows toast.
Generic metadata is limited to safe summary fields such as provider, event,
tool, cwd, thread, session, turn, and model; raw payloads and tool input are
not stored.

Codex may require reviewing or trusting hooks through its `/hooks` flow before
commands run. Projmux only writes the managed config block; it does not attempt
to auto-trust hooks. It never computes, copies, moves, or deletes a
`trusted_hash`, and it never carries an approval from one hook identity to
another: if a hook's matcher, type, command, or event changes, Codex asks you to
review that hook again while the other hooks keep the trust you already gave
them.

## Tmux Bell Fallback

`projmux doctor` reports whether the current tmux server has a
projmux-managed bell hook installed. The diagnostic is read-only; it only
inspects current `alert-bell` hooks and prints the matching integrate commands.

`projmux agent integrate tmux-bell` is the opt-in fallback for AI CLIs that do
not expose structured hooks but do emit BEL or OSC 9. It mutates the current
tmux server only; it does not edit tmux config files. Install applies these
server settings and appends a marked `alert-bell` hook:

```text
tmux set-option -g allow-passthrough on
tmux set-option -g monitor-bell on
tmux set-option -g bell-action other
tmux set-hook -ag alert-bell run-shell -b 'projmux internal agent-hook ingest bell --pane "#{pane_id}" >/dev/null 2>&1 || true # projmux-managed:tmux-bell:v1'
```

The hook calls `projmux internal agent-hook ingest bell --pane <pane_id>`. Bell ingest resolves
the target pane through tmux and pushes an info notify queue row such as
`bell · Claude CLI`, with metadata including `agent=bell`, `event=bell`,
session/window/pane, pane title, command, and socket path when available. The
pane does not need `@projmux_ai_agent` or any other AI-managed option because
this path is for unknown tools. tmux `alert-bell` is a window alert; tmux 3.4
does not expose `#{hook_pane}` there, so projmux uses `#{pane_id}` as the best
available pane context.

Repeated bells from the same pane are suppressed for 5 seconds using a
pane-local tmux timestamp option. The notify row also uses a stable
`ai:bell:<session>:<pane>` id, so later non-suppressed bells refresh the same
queue entry rather than creating unbounded duplicates.

`--dry-run` prints the tmux commands without applying them. `--remove` reads
the current `alert-bell` hooks and unsets only entries carrying
`projmux-managed:tmux-bell:v1`, preserving unmanaged user hooks.

## Claude Code Hook Ingest

`projmux doctor` reports Claude Code hook wiring in `~/.claude/settings.json`,
including unmanaged projmux ingest command conflicts and the relevant
`projmux agent integrate claude` commands.

`projmux internal agent-hook ingest claude-hook` is the conservative core ingest path for
Claude Code hooks. It reads a single JSON payload from stdin. The embedded
default install catalog is based on Claude Code 2.1.140 and represents the
29 hook events visible in that version:

| Event | Behavior |
| --- | --- |
| `PreToolUse` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PostToolUse` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PostToolUseFailure` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PostToolBatch` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PermissionDenied` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `Notification` | pushes a Claude notify row for response-ready, approval-required, or input-ready based on `notification_type` |
| `UserPromptSubmit` | marks the matched pane hook-active and sets AI state to thinking/busy; no notify queue entry is pushed |
| `UserPromptExpansion` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `SessionStart` | marks the matched pane hook-active and writes a quiet ingest diagnostic; for an exact managed initial-task binding it records `pending` startup readiness and opens the separately bounded acknowledgement window, but never acknowledges the task; no notify queue entry is pushed |
| `Stop` | pushes a Claude completion row, using the last assistant transcript text when `transcript_path` is readable |
| `StopFailure` | pushes a critical Claude error row with error type/message metadata when present |
| `SubagentStart` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `SubagentStop` | marks the pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PreCompact` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PostCompact` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `SessionEnd` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `PermissionRequest` | pushes a critical approval row with the tool name and a concise tool input summary |
| `Setup` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `TeammateIdle` | pushes an info Claude teammate waiting row with teammate context metadata when present |
| `TaskCreated` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `TaskCompleted` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `Elicitation` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `ElicitationResult` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `ConfigChange` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `InstructionsLoaded` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `WorktreeCreate` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `WorktreeRemove` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `CwdChanged` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |
| `FileChanged` | marks the matched pane hook-active and writes a quiet ingest diagnostic; no notify queue entry is pushed |

Hook-generated queue rows use the same compact body catalog: agent label,
event category, then the best available summary (Codex assistant text, Claude
tool/action summary, transcript summary, error, or teammate labels). Structured
payload details remain in `metadata`, which is passed through the `send-noti`
JSON payload and `get notifications --json`; the sidebar stays compact and does not
expand a separate metadata detail view. `SubagentStop` remains parsed for
diagnostics but is intentionally excluded from hook notifications because it can
fire at high volume.

Pane matching follows the shared AI ingest order: inherited `$TMUX_PANE`, then
payload `cwd`, then cached session id pane options. A matched pane is marked
with `@projmux_ai_hook_active=1`, so `projmux internal agent-hook watch-title` skips the pane
after a minimal hook-active gate instead of polling pane title/capture output;
hook payloads become the primary signal. The tmux bell fallback does not mark
panes hook-active, so title/capture fallback remains available for panes that
only emit bells.

The Claude hook payload is intentionally accepted directly at the ingest
boundary. Core identity fields accept `hook_event_name`/`event_name`,
`session_id`/`session-id`, `cwd`/`workspace`/`project_dir`, and nested
`workspace.cwd|path`. Extra-event fields are intentionally tolerant while the
upstream schemas settle:

| Event | Accepted fields |
| --- | --- |
| `StopFailure` | `error_type`, `errorType`, `failure_type`, `failureType`; `error_message`, `errorMessage`, `message`, `reason`; nested `error.type`, `error.name`, `error.code`, `error.message`, `error.text`, `error.reason` |
| `SubagentStop` | `subagent_type`, `subagentType`, `agent_type`, `agentType`; `subagent_id`, `subagentId`, `agent_id`, `agentId`; nested `subagent.type`, `subagent.name`, `subagent.kind`, `subagent.id`, `subagent.subagent_id`, `subagent.agent_id` |
| `TeammateIdle` | `teammate_name`, `teammateName`, `teammate`; `teammate_id`, `teammateId`; `teammate_context`, `teammateContext`, `context`, `reason`, `message`; nested `teammate.name`, `teammate.type`, `teammate.kind`, `teammate.id`, `teammate.teammate_id`, `teammate.context`, `teammate.status`, `teammate.reason`, `teammate.message` |

`projmux agent integrate claude` manages user-level Claude Code hook settings in
`~/.claude/settings.json`. Claude Code hooks are configured under the top-level
`hooks` object, with each event containing matcher entries and each matcher
entry containing a `hooks` array. Projmux omits `matcher`, which Claude Code
treats as "match all" for the event; this keeps `Notification` notification
types and `PermissionRequest` tool names broad while this integration remains
an observability hook:

```json
{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "StopFailure": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "SubagentStop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "TeammateIdle": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux internal agent-hook ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ]
  }
}
```

The marker lives inside the command string because JSON settings cannot carry
comments. Repeated installs are idempotent: projmux removes its marked commands
and re-adds the current managed command while preserving unrelated settings and
hooks. `--remove` deletes only marked commands, and `--dry-run` previews the
JSON without writing. Removal and unmanaged conflict detection scan every event
under the settings `hooks` object, not just the current catalog. If any event
already has an unmanaged command that invokes `projmux internal agent-hook ingest claude-hook`,
projmux refuses to install over it because it cannot tell whether the command
is user-owned or stale projmux wiring.

The Claude hooks install list is catalog-driven. Projmux ships an embedded
default catalog at `internal/app/ai_hook_catalogs/claude.json`, and merges an
optional local override from:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/claude.json
```

Override events replace matching embedded events by name and append new names,
so a local file can disable a stale event with `"install": false` or add a new
Claude Code event before projmux itself is released. Supported `action` values
are `notify`, `state`, and `quiet`; install uses only events whose effective
`install` value is true. Removal and unmanaged conflict detection scan every
event under `~/.claude/settings.json` for the projmux command marker rather
than trusting the current catalog, so stale managed events from older catalogs
are still removed safely. `SubagentStop` is wired for hook-active pane marking
and ingest debugging only.

```json
{
  "provider": "claude",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

Claude ingest has built-in notify/state handlers for the known completion,
notification, approval, prompt-submit, error, subagent-stop, and teammate-idle
events. Events without a specialized handler, including unknown future events,
are quiet/log-only after pane matching. Catalog `"action": "quiet"` is used for
that fallback; catalog `"notify"` and `"state"` entries still need built-in
handler code for event-specific queue rows or state transitions. Runtime action
overrides live in the same
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json` file used by
Codex and are managed from `Settings > Notifications > Agent event behavior`.
They only affect ingest delivery; `projmux agent integrate claude` still uses the
catalog `install` field for installed hook events.

## Antigravity Hook Ingest

`projmux internal agent-hook ingest antigravity-hook --event <event> < payload.json` accepts
Antigravity CLI `agy` hook payloads. Antigravity v1.1.12 stdin does
not include the event name, so the command's `--event` value is authoritative.
Payload event aliases remain a compatibility fallback when `--event` is
omitted. `projmux agent integrate antigravity [--dry-run|--remove]` owns exactly
the named `projmux` entry in `~/.gemini/config/hooks.json` and separately owns
only `statusLine` in `~/.gemini/antigravity-cli/settings.json`. Other named hooks,
their fields, and unknown JSON values remain untouched. The generated commands
use the stable absolute projmux executable because Antigravity runs handlers
with the config directory as cwd. The command refuses unmanaged name/command
conflicts, malformed JSON, symlink paths, and permission failures with an
actionable diagnostic. Doctor/Settings distinguish installed, missing,
conflicting, and stale managed entries; stale covers executable, event/schema,
or stdout-fallback drift and is refreshed by the install command.
The statusline object uses the official v1.1.12 command shape with
`enabled=true` and `stack_with_default=true`. Its direct explicit `Statusline`
ingest command emits empty stdout, preserving the built-in line. Existing
custom statusline commands are conflicts and are never wrapped or chained.

The default Antigravity catalog records the five official v1.1.12 events and
installs four non-permission events:

| Event/signal | Behavior |
| --- | --- |
| `PreToolUse` | known permission-changing event; never installed and no permission decision is synthesized |
| `PreInvocation` | marks the matched pane hook-active and moves it to thinking/busy; no notify queue entry is pushed |
| `PostInvocation` | marks the matched pane hook-active and writes a quiet bookkeeping diagnostic; no notify queue entry is pushed |
| `PostToolUse` | marks the matched pane hook-active, retains tool error metadata in quiet diagnostics, and pushes no notify queue entry |
| `Stop` | pushes an info completion unless an explicit error signal requires a critical error row |
| `Statusline` with `tool_confirmation_pending=true` | pushes/replaces a deduped critical approval-required row outside the hook catalog |
| `Statusline` with `agent_state=thinking|working|tool_use` | moves the matched pane to thinking/busy without notifying, unless a terminal completion/approval state must be preserved from a late refresh; a new `PreInvocation` resets the next generation to busy |
| `Statusline` with `agent_state=idle` or `tool_confirmation_pending=false` | quiet update; does not clear completion/approval attention and creates no notification |
| unknown events | mark the matched pane hook-active and write quiet ingest diagnostics only |

Antigravity notify rows use `agent=antigravity` metadata. The v1.1.12 parser
retains common camelCase `conversationId`, `workspacePaths`, `transcriptPath`,
`artifactDirectoryPath`, and `modelName`; invocation `invocationNum` and
`initialNumSteps`; post-tool `toolCall`, `stepIdx`, and `error`; and Stop
`executionNum`, `terminationReason`, `error`, and `fullyIdle`. Existing aliases
such as `conversation_id`, `cwd`, `workspace.path`, `agent_state`, and nested
`statusline.tool_confirmation_pending` remain accepted. The first non-empty
`workspacePaths` value is only a cwd fallback candidate; empty/absent arrays do
not become the process cwd, and inherited `$TMUX_PANE` still wins attribution.
`NO_TOOL_CALL`, `MODEL_STOP`, and known normal reasons are info completions.
Non-empty `error`, explicit `ERROR`, and `MAX_STEPS_EXCEEDED` families are
critical; unknown reasons retain diagnostic metadata but default to info.
Explicit non-permission hook ingest writes valid hook stdout: `{}` for
invocation/post-tool events and `{"decision":"stop"}` for Stop. The latter
allows the requested stop to complete; projmux does not emit `continue` or a
`PreToolUse` permission decision.
Each managed command includes its explicit event selector and a valid stdout
fallback: `{}` for invocation/post-tool events and `{"decision":"stop"}` for
Stop. The fallback is non-blocking and never returns `continue`,
`allow`, `deny`, or `ask`.

The named entry in `hooks.json` remains the install source of truth. Use
`agy -p '/hooks' --output-format json` only as a read-only runtime diagnosis of
loaded sources/events; its result is never used to generate or rewrite config.
Antigravity ingest uses `conversationId` as pane thread metadata for matching
and as session-state resume metadata. Session restore uses
`agy --conversation <uuid>` only when that id is present and UUID-shaped;
otherwise preview and doctor render `resume unavailable`. Official snake_case
`cwd`, `conversation_id`, `transcript_path`, `agent_state`,
`tool_confirmation_pending`, and structured `context_window.used_percentage`
plus token fields are parsed directly. The structured percentage is persisted
with conversation identity to the usage state dir; the legacy string percentage
remains a fallback so the usage HUD can surface it as the conversation-local
`context` row. The official top-level `quota` map is persisted independently:
valid `remaining_fraction` values become used percent, exact upstream bucket
IDs render as separate `quota/<bucket>` account rows, and `reset_time` plus
optional `reset_in_seconds` retain their independent meanings. Invalid,
disabled, missing, or empty quota shapes degrade without inventing a cadence or
reinterpreting context. Raw payloads or transcript contents are not stored.

## Ingest Debug Log

Every `projmux internal agent-hook ingest ...` path appends compact JSONL diagnostics to
`$XDG_STATE_HOME/projmux/ai-ingest.log`, or
`~/.local/state/projmux/ai-ingest.log` when `XDG_STATE_HOME` is unset. The log
records source, event, result, pane, match identifiers, and a short reason for
parse errors, unsupported events, missing pane matches, deduped bells,
state-only transitions, quiet high-volume events, and notify pushes. Raw hook
payloads are not stored.

Use `projmux diagnostics agent-hook` to inspect recent entries:

```text
projmux diagnostics agent-hook --tail 20
projmux diagnostics agent-hook --json --tail 20
projmux diagnostics agent-hook --path
```

The human legacy ingest-log reader has been removed. Use
`projmux diagnostics agent-hook`; it reads the same bytes.

The file is capped at 1 MiB. When an append grows it past the cap, projmux
keeps the most recent roughly 512 KiB and trims from the next JSONL boundary so
the remaining file still starts at a whole entry.

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

Hooks inherit projmux's environment. The lifecycle variables below are added by
event; “omitted” means the variable is not added when its context value is
empty.

| Variable | `pre-create` | `post-create` | `post-attach` | `send-noti` |
| --- | --- | --- | --- | --- |
| `PROJMUX_SESSION` | new session name | new session name | target session name | target session when known; otherwise empty |
| `PROJMUX_CWD` | requested session directory | created session directory | resolved target directory; may be empty if lookup fails | dispatcher working directory |
| `PROJMUX_SESSION_KIND` | `persistent` or `ephemeral` | `persistent` or `ephemeral` | empty | empty |
| `PROJMUX_VERSION` | projmux version | projmux version | projmux version | projmux version |
| `PROJMUX_SOCKET` | app socket metadata (`projmux`) | app socket metadata (`projmux`) | app socket metadata (`projmux`) | queue-entry socket when known; otherwise omitted |
| `PROJMUX_PANE` | omitted: no pane exists yet | exact id returned by standard persistent/ephemeral `tmux new-session`, such as `%7`; omitted for snapshot replay | omitted | target pane when known; otherwise omitted |

`pre-create` intentionally has no `PROJMUX_PANE`: it runs before
`tmux new-session` creates the first pane. Standard persistent and ephemeral
`post-create` paths run after creation and therefore receive that exact pane
id. Snapshot replay can restore multiple panes and does not expose a single
returned pane at its lifecycle boundary, so it omits `PROJMUX_PANE`.
`PROJMUX_SOCKET` is routing metadata for hook commands; it does not imply that
the tmux client itself adds `-L` to its commands. The `post-attach` and
`send-noti` cells describe their existing contexts; this contract adds no pane
context to either event.

## Examples

### Global Post Create Stub

```bash
#!/usr/bin/env bash
echo "session=$PROJMUX_SESSION cwd=$PROJMUX_CWD kind=$PROJMUX_SESSION_KIND"
tmux -L "$PROJMUX_SOCKET" set-option -p -t "$PROJMUX_PANE" @projmux_initialized 1
```

### Project Startup Command

```toml
[startup]
run = "git status --short"
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

tmux -L "$PROJMUX_SOCKET" set-environment -t "$PROJMUX_SESSION" GH_TOKEN "$token"
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
  stderr stream.
