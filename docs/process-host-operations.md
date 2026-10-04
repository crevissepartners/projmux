# Operating Process-Hosted Agents

`projmux create agent --host process` runs one Claude or Codex Agent without
tmux, for scripts and CI. The creating command is the Agent's **owner**: it
stays in the foreground, and the Agent lives exactly as long as that command.
This page covers what you see and do while such an Agent runs. Creation flags,
output, exit codes, and the opt-in `post-create` hook contract are in
[Process Host Post-Create](hooks.md#process-host-post-create). The Registry
fields are in [Registry process evidence](registry.md#process-evidence).

## Driving the Agent from another terminal

A process Agent has no terminal of its own, so every interaction goes through
its exact Agent reference:

| Task | Command |
| --- | --- |
| Send a turn as the operator | `projmux agent turn start <agent-ref> -- <text>` |
| Interrupt the current turn | `projmux agent turn interrupt <agent-ref> --via cli` (Claude) or `projmux agent turn interrupt <agent-ref>` (Codex) |
| Answer a question | `projmux agent question list <agent-ref>`, then `projmux agent question answer <agent-ref> <question-id> --option <n>=<label>` |
| Answer a permission request | `projmux agent approval list <agent-ref>`, then `projmux agent approval answer <agent-ref> <request-id> --allow` or `--deny` |
| Coordinate from another Agent | `projmux agent message send <agent-ref> --source <agent-ref> -- <text>` |
| Inspect it | `projmux describe agent <agent-ref>` |

`agent turn start` delivers your text as a plain user turn. `agent message send`
keeps the peer coordination envelope, so the model sees it as a message from
another Agent rather than as operator input.

Questions and approvals are always captured for process Agents, whatever the
question channel or central answering setting says, because there is no
provider screen to answer them on. They appear in `projmux attention list` and
as notifications until answered.

An Agent runs one turn at a time. A turn start while another turn or message is
still in progress is refused with `process admission capacity exhausted` and
exit status 1; wait for the turn to finish, or interrupt it, then retry.

Terminal operations have no meaning for a process Pane and are refused before
anything changes, with a stable token as the error prefix:

| Token | Refused operation |
| --- | --- |
| `process-attach-unsupported`, `process-focus-unsupported` | Attaching to or focusing the Pane |
| `process-send-keys-unsupported`, `process-capture-unsupported` | Sending keys to or capturing the Pane |
| `process-popup-unsupported`, `process-relaunch-unsupported` | Opening a popup on, or relaunching, the Pane |
| `process-create-pane-unsupported`, `process-split-unsupported` | Using the Pane as the anchor of a new Pane |
| `process-host-unavailable` | A turn, interrupt, or answer when the owner cannot be reached |

## Reading its state

`projmux describe agent <agent-ref>` and `projmux describe pane` add these rows
for a process Pane. They never include provider content.

| Row | Meaning |
| --- | --- |
| `RuntimeKind` | `process` |
| `ProcessHost` | Identifier of the owner that runs this generation |
| `HostPID` | PID of the owner command |
| `ChildPID` | PID of the provider process |
| `ResumeState` | `resumable` once a conversation and connection were recorded; otherwise `unknown` |
| `PendingControls` | Number of unanswered questions and approvals |

`HostPID` and `ChildPID` appear until the owner records the provider's exit.

`Status` in `describe` and `get` is `live` while the owner answers. When the
owner cannot be reached and no exit was recorded, for example after the owner
was killed with `SIGKILL`, the status is `unknown`: projmux does not guess that
the provider stopped.

## Stopping the Agent

The owner stops its provider and records the provider's actual exit when its
standard input reaches end of file or it receives `SIGINT` or `SIGTERM`. Keep
standard input open for as long as the Agent should run; see
[Process Host Post-Create](hooks.md#process-host-post-create) for a pattern and
the exit codes.

Process control trusts the operating-system user, as tmux does. Any program
running as the same user can stop a process Agent, for example with
`kill -TERM <HostPID>`, just as any such program can kill a tmux Pane. Run
untrusted programs under a different user if they must not stop your Agents.

## Message handoff expiry

When another Agent sends a message to a process Claude Agent, the owner reserves
the Agent's single turn for that message until the provider confirms it. If the
outcome is still unconfirmed after 30 seconds, the reservation expires:

- projmux raises an error notification for the Agent;
- the reserved turn stays taken, so later turns and messages to this Agent are
  refused with `process admission capacity exhausted`;
- projmux does not assume the message arrived, does not resend it, and does not
  stop the provider.

If the provider later finishes that turn, the Agent becomes idle again and
accepts input. Otherwise stop the owner and start the Agent again; the stopped
generation never reopens.

## Damaged attention store

Process attention state lives in
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/process-attention.json`. Commands
that only read it report a damaged file as an error and leave it unchanged. The
next owner that writes attention state first copies the damaged bytes to
`process-attention.json.damaged-<random>` in the same directory, then starts a
new empty store; each running owner restores its own entry on its next update.
If the copy fails, the original file is left as it was.

projmux never deletes these backups. Inspect them if you need the lost state,
then remove them yourself:

```sh
ls "${XDG_STATE_HOME:-$HOME/.local/state}"/projmux/process-attention.json.damaged-*
rm "${XDG_STATE_HOME:-$HOME/.local/state}"/projmux/process-attention.json.damaged-*
```
