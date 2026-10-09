# Operating Process-Hosted Agents

`projmux create agent --host process` runs one Claude or Codex Agent without
tmux, for scripts and CI. The creating command is the Agent's **owner**: it
stays in the foreground, and the Agent lives exactly as long as that command.
This page covers what you see and do while such an Agent runs. Creation flags,
output, exit codes, and the opt-in `post-create` hook contract are in
[Process Host Post-Create](hooks.md#process-host-post-create). The Registry
fields are in [Registry process evidence](registry.md#process-evidence).

Every headless Claude launch explicitly selects `--permission-mode auto`: fresh
creation, resume, same-host relaunch and moves into the process host share the
same policy. Deferred launches freeze that argument with the rest of their
prepared command and replay it unchanged. Auto does not guarantee that Claude
will never ask for approval; profile allow/deny rules still apply. Ordinary
tmux Claude and Codex launch policies are unchanged.

An older deferred recipe without exactly one auto mode is refused before
provider spawn or configuration writes. Use `projmux agent relaunch <agent-ref>`
from an external terminal to explicitly prepare a new auto recipe, then submit
first input; add `-- <first-prompt>` to start it immediately. Replacement retains
the existing conversation and exact claim/recipe checks. Projmux does not edit
the old argv or migrate it automatically.

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

A Codex Agent runs one turn at a time. A turn start while another turn or
message is still in progress is refused with `process admission capacity
exhausted` and exit status 1; wait for the turn to finish, or interrupt it,
then retry.

A Claude Agent follows the turns Claude itself reports. A turn opens with the
first provider frame, such as `system/init`, and closes with its `result`.
Claude also opens turns on its own once the conversation exists: a background
task finishing, a message from another Claude Code session, or a scheduled
wake-up. The host follows those turns instead of stopping the Agent. A
permission or question inside one is answered the usual way, and peer
messages sent during it are held until it ends.

`agent turn start` while a Claude turn is running joins that turn. The text is
written to Claude, which reads it at its next tool boundary and answers within
the same turn, as typing into the terminal would. The result line adds
`delivery=joined running-turn=<turn> origin=<host|message|provider>`; `turn=`
still names your input's operation. A joined input is refused, with nothing
written and exit status 1, when:

| Reason | When |
| --- | --- |
| `control-pending` | A permission or question in the running turn is unanswered; answer it first |
| `joined-input-limit` | The running turn already holds 8 joined inputs or 262144 bytes of them |
| `turn-not-open` | Claude has not yet visibly started the admitted turn |
| `message-handoff-pending` | A peer message turn is reserved and Claude has not yet visibly started it |
| `message-handoff-expired` | A peer message handoff never reported its outcome and Claude never visibly started the turn |
| `interrupt-pending` | An interrupt of the running turn is in flight |
| `event-limit` | The running turn holds the host's event limit |

Each refusal reads `process admission capacity exhausted: <reason>: <advice>`.
A turn started for a peer message accepts joined input as soon as Claude
visibly starts it, even when the handoff outcome is late or never arrives.

If a joined input reaches Claude just as its turn ends, Claude may answer it in
a turn of its own. The stream cannot tell that turn from one Claude started for
another reason, so the host records the input as written with its result
attribution unknown. It is never sent again.

Terminal operations have no meaning for a process Pane and are refused before
anything changes, with a stable token as the error prefix:

| Token | Refused operation |
| --- | --- |
| `process-attach-unsupported`, `process-focus-unsupported` | Attaching to or focusing the Pane |
| `process-send-keys-unsupported`, `process-capture-unsupported` | Sending keys to or capturing the Pane |
| `process-popup-unsupported`, `process-relaunch-unsupported` | Opening a popup on, or relaunching, the Pane |
| `process-create-pane-unsupported`, `process-split-unsupported` | Using the Pane as the anchor of a new Pane |
| `process-host-unavailable` | A turn, interrupt, or answer when the owner cannot be reached |
| `process-host-unsupported-action` | A request the owner does not implement, typically because an older projmux started it; relaunch the Agent to run a current owner |

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

`describe agent` also asks the running owner which build it is:

| Row | Meaning |
| --- | --- |
| `HostRevision` | The 40-character commit the owner was built from, or `unknown` when the owner cannot be reached within a short read budget, was started by a projmux that predates this row, or was built without a commit |

An owner keeps running the binary it started with, so after an upgrade
`HostRevision` tells you which Agents still run an older owner until you
relaunch or resume them. `describe -o json` stays the stored Registry
resource and does not carry this live row.

The owner answers observation with control protocol 1. Next to its process
identities and state it reports `Protocol` (`1`), `Revision` (the same commit,
empty when unknown), `Actions` (the requests it implements), `Turn` (whether a
provider turn is running), `Pending` (the number of unanswered questions and
approvals, never their content), `OwnerMode` (`foreground`, the owner is
the command that created, resumed, or relaunched the Agent), and
`Coordination` (the Claude coordination version the owner's build speaks). An
answer without these fields comes from an owner that predates them and is read
as protocol 0; an answer without `Coordination` alone leaves that version
unknown.

`Status` in `describe` and `get` is `live` while the owner answers and
`offline` after the owner recorded the provider's exit. When the owner cannot
be reached and no exit was recorded, for example after the owner
was killed with `SIGKILL`, the status is `unknown`: projmux does not guess that
the provider stopped.

## Installing while owners run

An install never stops or replaces a running owner. `make install` lists the
live owners in its residue census, each with its Agent, provider, `Revision`,
and coordination version (`coordination unknown` when the owner does not report
one), and leaves them on the build they started with.

Before it publishes anything, `make install` runs `projmux internal
install-preflight`. When the new build would migrate the Registry to a newer
`schemaVersion`, or a live Claude owner reports a coordination version the new
build does not speak, and at least one owner is live, the install stops with
exit 1. The binary and the live config stay unchanged, and the message lists
the owners and how to stop them: end each foreground owner (Ctrl-C or close its
standard input) or run `projmux delete agent <agent-ref>`, install again, then
`projmux agent resume <agent-ref>`. An owner whose coordination version is
unknown does not stop an install. A live owner is the recorded host process of
a current process activation, matched by pid and start time, so owners the web
server hosts are included.

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
- the reserved turn stays taken, so later messages to this Agent are held and
  `agent turn start` is refused with `message-handoff-expired`, unless Claude
  has visibly started the turn, in which case the input joins it;
- projmux does not assume the message arrived, does not resend it, and does not
  stop the provider.

If the provider later finishes that turn, the Agent becomes idle again and
accepts input. Otherwise stop the owner and continue in a new generation with
[`projmux agent resume`](cli.md#projmux-agent-resume); the stopped generation
never reopens.

Process Codex Agents have no such reservation. A message to an idle Codex
Agent starts a Codex turn, and one to a Codex Agent that is running a turn is
steered into that exact turn, as with a tmux Codex Agent. Its receipt settles
as soon as Codex accepts or refuses that turn or steer: `delivered` with the
reason `host-turn-accepted` for a start or `host-turn-steered` for a steer. A
refused or uncertain steer is never retried as a new turn; its receipt is
`refused` with `provider-refused`, or `failed` with
`delivery-outcome-unknown`.

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

Private samehost consumers can receive a typed `Preview`, `Unchanged`, `Owned`
or `Prepared` result. `Owned` carries the actual target, synchronization and an
independent lifetime: the receiver registers its exact Wait before acknowledging
success, and registration failure or shutdown cancels that target and completes
its durable Wait. Ending the producer request after handoff leaves the child
owned by the receiver. The existing self-target and confirmation guards still
apply; an owning consumer must Stop, finish actual Wait and read fresh Offline
state before applying another configuration.

Claude preparation without first input returns `Prepared` with its durable
recipe proof and retained conversation/last termination, with no child, active
claim or standby waiter. A private explicit Resume validates an existing frozen
plan without replacing it; when absent, it prepares the recorded configuration
through the samehost planner and refuses implicit recipe drift. `Prepared` has no
live lifetime to register. The first actual user input acquires the existing
claim and uses the frozen resume engine. These are CORE producer contracts for
a future consumer; they do not establish web UI support. The public CLI retains
its foreground ownership, stdin/signal cancellation and deferred peer waiting.

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
