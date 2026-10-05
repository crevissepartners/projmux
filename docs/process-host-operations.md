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
| Change a Claude launch configuration | `projmux agent relaunch <agent-ref> --model <model> -- <first-prompt>` (foreground caller; busy Agents require `--yes`) |
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
| `ResumeState` | `resumable` after the owner recorded the provider's exit for an established conversation; `unknown` while it runs or when no exit was recorded |
| `PendingControls` | Number of unanswered questions and approvals |

`HostPID` and `ChildPID` appear until the owner records the provider's exit.

`Status` in `describe` and `get` is `live` while the owner answers and
`offline` after the owner recorded the provider's exit. When the owner cannot
be reached and no exit was recorded, for example after the owner
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
accepts input. Otherwise stop the owner and continue in a new generation with
[`projmux agent resume`](cli.md#projmux-agent-resume); the stopped generation
never reopens.

Process Codex Agents have no such reservation. A message becomes a Codex turn
directly, and its receipt settles as soon as Codex accepts or refuses that
turn; a message to a Codex Agent that is still busy is refused with the reason
`host-busy`.

## Damaged attention store

Process attention state lives in
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/process-attention.json`. Commands
that only read it report a damaged file as an error and leave it unchanged; the
status bar keeps showing tmux attention and reports only the process read
error. The
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

## Changing the launch configuration

Use [`agent relaunch`](cli.md#projmux-agent-relaunch) from an external terminal
to change a process Claude or Codex Agent's model, effort or profile. It keeps
the Agent, Pane and conversation, stops the old owned provider, verifies its
actual supervisor Wait and owner retirement, then starts a new foreground
owner. Keep stdin open for that owner's lifetime. Codex can reattach without
one. On the same process host, an omitted or whitespace-only Claude prompt
instead stops the old provider and waits without a child, even with unchanged
settings. It prints `foreground=claimed`, then the usual relaunch result after
first input. JSON puts ownership lines on stderr. The new resolved arguments,
workspace and referenced configuration snapshots are durably frozen in private
state; the environment and first input are not part of that launch record.

The first peer message starts that configuration with its existing untrusted
coordination envelope. An operator can instead submit `agent turn start` from
another terminal; that text becomes the raw first user frame. Success is
reported only after verified same-session init. Concurrent accepted input is
refused as busy. The internal user deadline is 30 seconds: pending cancellation
or expiry prevents submission, while an uncertain handoff is reported without
automatic replay. Terminal input records remove the raw text.

EOF, INT or TERM before input release the claim and exit 0, preserving the
configuration. After SIGKILL, `agent resume <agent-ref> --wait-for-peer` reclaims
it. A changed referenced snapshot or a conflicting model/effort override is
refused without replacing the stored configuration. Failed startup retains the
new configuration only with exact supervisor Wait and the same conversation;
ambiguous evidence requires inspection. Prompt-bearing relaunch retains its
existing failure recovery. Claude host transfer still requires a prompt.

Codex cannot replace the instructions of an existing thread. A request that
changes those instructions is refused before Stop, as is a profile switch
that would silently keep the previous sandbox or approval policy. The new
writer uses `thread/resume` after the old dedicated app server exits; an
active-writer (`-32600`) refusal does not create a replacement conversation.

## Waiting to resume on a peer message

An Offline Agent with a recorded resumable conversation can have one foreground
claimant, without starting a provider yet:

```sh
projmux agent resume <agent-ref> --wait-for-peer
```

This works for Claude and Codex. Keep stdin open: EOF, `SIGINT`, or `SIGTERM`
before resume releases the claim and exits 0. With `</dev/null` or a cron stdin,
the command ends immediately. After resume the command owns the provider and
reports its actual exit, as ordinary `agent resume` does.

Default output starts with
`agent uid:<agent> pane uid:<pane> runtime=process foreground=claimed`;
successful resume then prints the existing `foreground=owned` result.
`-o none` suppresses output. The waiting flag cannot accompany a prompt,
`--dialogue-reply-only`, or a tmux Agent.

While that exact claimant process lives, peer sends are accepted with
`held` / `target-awaiting-resume` and exit 0. Check `agent message status <ref>`;
do not resend. The oldest unexpired message becomes the original conversation's
first frame, retaining its untrusted coordination envelope. The remaining held
messages follow in acceptance order after each provider turn finishes, within
their deadlines. Successful receipts become `delivered`. The first receipt
retains the retired target generation; subsequent receipts show the resumed
generation with the same Agent, Pane, provider and conversation incarnation.

A second claimant or ordinary resume is refused with `process-resume-owned`.
Without a live claim, sending to an Offline Agent keeps its existing refusal.
Claims use the exact OS process identity and a private state file; a restarted
claimant can replace a dead claim, including after `SIGKILL`, and wake preserved
messages. This does not automatically restart Agents. Resume failures leave the
conversation resumable and settle the selected message as `failed` with
`process-resume-refused`; expired messages settle as `expired`. A crash after a
frame might have been submitted settles that message as `failed` with
`outcomeUnknown`, without replaying it.

Claude explicit replies (`agent message send --reply-to`) use this deferred
route after the source's existing reply checks. Claude helpers run the binary
image used at SessionStart: helpers started before this feature was installed
retain the previous refusal until their Agent is relaunched. Installing the
binary does not replace those running helpers.

## Moving an Agent between hosts

`agent relaunch --host process -- <first-prompt>` moves an interactive Claude
Agent to a foreground process owner. `agent relaunch --host tmux` moves it back
to an interactive Pane after the process owner records the old child's actual
Wait. Both directions preserve the Agent UID and provider session, create a new
Pane, and remove the old Pane. Omitting `--host` keeps the current host.

Claude moves to process with a nonempty first prompt, including dry-run.
Codex can move without a prompt:

```sh
projmux agent relaunch <agent-ref> --host process --yes
projmux agent relaunch <agent-ref> --host tmux --yes
```

Codex keeps the same Agent, thread and rollout and creates a new Pane in either
direction. Moving to tmux takes no first prompt. Keep stdin open while the
process owner should run. Omitting `--host` retains the existing host.

`--dry-run -o json` reports `currentHost`, `targetHost`, and the additive
`host-changed` relaunch reason. Busy Agents require `--yes`. Reply-only Agents
are refused before mutation. If the exact old writer's
retirement is unknown, no new child starts. A failed target launch retains the
previous launch recipe and conversation; follow its exact recovery command.


For tmux-to-process Codex moves, projmux freezes only the exact thread's broker
admission, drains admitted work, stops its interactive Pane, and waits until
the installed app server reports the thread absent from its complete loaded
list. An unsubscribe acknowledgement alone does not prove retirement. Other
threads and the shared daemon keep running. The new dedicated writer resumes
that thread and its settings before input is admitted. Moving back waits for
the dedicated child's actual exit before the shared endpoint resumes it.

If a move or its caller fails after retirement starts, the thread remains
fenced. Run the exact recovery command printed by the failed operation,
using `agent relaunch <agent-ref> --host tmux --yes` with the original socket
and without launch overrides or a prompt. Recovery first proves any target
writer stopped and checks the current source and target recipe; it refuses
unknown exits and changed state. It then resumes the original thread with a
fresh native activation and completes the reservation after that writer is
ready. Successful recovery retains the failed target's actual exit in the
private operation record. A dry-run or refusal starts no provider and changes
no stored recipe. The same instruction, sandbox, approval and reply-only guards
apply to moves as to existing relaunch operations.
