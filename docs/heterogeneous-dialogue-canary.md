# Heterogeneous Dialogue Canary

This opt-in Linux canary exercises current Claude 2.1.263 and Codex 0.153.2
through the public reply-only activation. The required selectorless E2E remains
one offline L20 scenario. An actual provider run requires the roadmap owner's
reviewed R1 plan and explicit execution gate; these scripts do not grant it.
The first actual run covers qualification followed by one idle request. It does
not certify active-tool overlap, human overlap, recovery or installed behavior.

## Public reply-only activation

On Linux, explicitly opt one next activation into the restricted transport profile:

```sh
projmux create agent --provider claude --dialogue-reply-only --project <project> --window <window>
# After normal exit, resume the same Agent UID and provider conversation:
projmux agent resume uid:<same-agent> --dialogue-reply-only
# From the exact current Codex source activation:
projmux agent message qualify uid:<claude-agent> --confirm-isolated-provider-push -o json
```

The flag changes only that activation. It does not persist an Agent default,
change a running Agent, or enable ordinary Claude sessions implicitly. Each new
activation requires a fresh opt-in and exact-version qualification. Without
this profile or another explicitly validated execution guard, inbound eligibility
remains unqualified; an ordinary `integrate`/resume alone does not create the guard.

This headless profile starts one fixed `Reply READY.` turn, enables only Bash,
and enforces its exact public reply command through the pinned execution gate.
It uses restricted mode, no Chrome or slash commands/prompt suggestions, empty
settings sources, strict empty MCP configuration and owned exec-form hooks.
The public init must confirm Bash alone, no MCP/plugins and the current version.
Normal SessionStart, UserPromptSubmit and Stop state callbacks retain badge
ownership; Stop never publishes a peer reply. Provider session persistence stays
on so a normal same-UID resume can retain its conversation.

The pane is an activation status/EOF surface. Typed terminal text is discarded;
it is **not** model-visible human input. Ctrl-D closes provider stdin and lets the
current turn finish before normal exit. The owned observer validates public
stdout/stderr in memory and forwards only bounded shape/effect assertions to
the existing coordination helper. Raw text, reasoning, signatures and messaging
credentials are never evidence files. EOF, stderr, unknown output or observer
replacement invalidates inbound and issued reply actions. The supervisor requires
exact observer birth and pidfd exit readiness before removing its private profile;
uncertain exit retains the profile and reports cleanup failure.

The public qualifier obtains fresh observed init evidence from that exact helper
when `--evidence` is omitted. A supplied evidence file cannot override a live
profile's missing/invalid observer. Qualification still requires the current
Codex source, confirmation and a broker-owned explicit reply challenge; observed
init alone does not admit general inbound traffic. Actual model execution,
human overlap, active-tool ordering and installed same-UID recovery remain
separate R2 evidence requirements; these deterministic tests do not prove them.

The existing helper's read-only profile evidence also reports bounded observed
model tool IDs, their parsed original request/target, paired result and public
reply ref. It separately compares the guard's exact selection and the successful
broker commit. An issued/consumed permit or a model result alone does not prove
that commit. Missing, mismatched or late observations cannot grant execution;
raw commands, bodies, reasoning, signatures and tickets are omitted. A currently
alive execution PID is only a point-in-time observation, not proof that a second
push overlapped an active tool. Actual model action and exact Codex claim still
require the revised owned runner and owner gate.

## Exact execution boundary

The owned exec-form PreToolUse hook permits only Bash's literal public
`<candidate> agent message send uid:<original-source> --reply-to <original-ref> -- <one text argument>`.
The documented [shell prefix](https://code.claude.com/docs/en/env-vars) receives
one opaque shell invocation. The pinned candidate extracts one canonical
one-use ticket and directly executes its approved argv; it never evaluates the
carrier. Exact provider ancestry/birth, session, route, tool ID and image remain
required at preparation, consumption and commit. Missing, foreign, reused or
changed tickets/images fail before execution. An observed tool action grants no
permission. Private coord v5 fences earlier helpers; the frozen provider frame
is unchanged.

## Genuine source runner preparation

The maintained source-action and bounded Codex observation reader are now
available, with offline fixtures. **The setup/orchestrator wiring is incomplete;
this revision is not an actual-run candidate.** The prior payload-free Codex
setup does not establish a fresh native composite authority and must not be
used as the genuine-source method. Owner review and fresh execution pins remain
required before any actual provider run. No historical failed run is promoted.

The intended public source creation includes the actual user-authorized task:

```text
projmux create agent --provider codex --project uid:<P> --window uid:<W> -o pane-id -- '<genuine qualification/send/self-claim task>'
```

The task invokes the pinned `agent-dialogue-source-action.py` once. It waits
for the parent to resolve both runtime-first Agent/Pane chains and freeze the
native thread/turn/started command item plus independent process birth and
ancestry. The parent must first prepare an exact owned endpoint and a private
config/auth/state domain; ordinary native creation only attaches to a ready
endpoint. No fixture binding, dummy task or peer-to-user-turn relay is involved.

The source action uses only existing public `agent message` commands: qualify
the exact receiver with `--confirm-isolated-provider-push`, claim that original
reply from the original Codex inbox, send one independent idle request, then
claim its correlated reply. It rechecks frozen live routes before dispatch and
uses the existing Claude helper's observed tool/result/guarded-commit evidence.
Empty follow-up claims check claim-once behavior. Ambiguous command outcomes
stop the action without resend. The action never tears down an Agent, Project,
provider or broker. Only bounded correlation facts go back to the source model;
raw CLI bodies and stderr are not written or returned.

`agent-dialogue-codex-observation.py` validates the frozen public 0.153.2
`thread/read(includeTurns=true)` and `item/started`/`item/completed` schemas.
The four public exports under `scripts/agent-dialogue-codex-schema/` retain their
original hashes. The reader uses only the Python standard library. Schema names
are closed; names explicitly listed as required remain known even when an
export omits their property declaration. Unknown fields/effects, incomplete
history views, missing source, changed routes/items, output mismatch, EOF and
bounds violations fail closed. The schema's optional source default is never
applied, and `processId` is never interpreted as an OS PID.

Observation is limited to 1 MiB per JSONL frame, 8 MiB/128 frames per reader,
32 items in the sole expected turn, and a bounded connection deadline. The
existing initialized UDS peer must match the independently pinned PID/UID/birth
before and after reads. The reader sends only `thread/read`; initialization and
owned endpoint/socket binding are still parent-orchestrator integration work.
No raw command, output, user text or reasoning enters the returned facts.

A started item may be frozen before the first push. Completion is checked only
after the action returns its closed qualification/idle result. A completed tool
record and completed original turn are distinct facts; they do not evaluate
model answer quality. Ordinary Codex task/tool-result history writes in the
private original thread are expected, while peer history/user-turn API writes
remain excluded. Actual field availability, model action selection, source
policy isolation and this ordering are not proven by offline fixtures.

The remaining maintained wiring must pin an owned direct app-server launch
handle/executable/default private socket, private HOME/CODEX_HOME/XDG/SQLite and
file-based auth policy, copied script/schema digests, and the exact action
command. It must preserve public Project session projection, isolated tmux
socket validation, and runtime-generated IDs before release. System config and
requirements cannot be assumed absent merely because HOME is private.

The parent must own one cleanup transaction on every partial failure and after
the source tool result returns. Capture reader/daemon/broker and all owned writer
births before teardown. Only proven owned processes may receive the existing
graceful termination request; the broker's default 30-second idle lifetime is
not covered by assuming the current 20-second writer deadline will suffice.
Retain exact-root pidfd exit proof before one root removal, remove both owned
auth copies even on retained-root failure, and preserve a bounded external
0600 audit before removal. Unknown writer exit or audit/removal failure retains
failure evidence; no fixed sleep, broad signal, retry removal or manual-cleanup
PASS is permitted. No protected/shared runtime is an owned seed.

The selectorless E2E remains the single deterministic L20 case. Actual active
tool/human overlap, multiple ordinary requests, same-UID recovery and installed
smoke remain separate acceptance evidence. The next reviewed slice completes
the parent orchestrator and replaces this preparation section with its pinned
single-transaction invocation; the source action alone is not that transaction.

The maintained setup now prepares the direct private default control endpoint,
then passes the genuine task through the existing public Codex create command.
`agent-dialogue-native-source.py` owns initialization, read-only item observation,
the source release and completed tool/turn result proof. `agent-dialogue-source-action.py`
executes the public qualification/claim/idle/claim commands and never cleans up.
The parent uses the existing pidfd writer barrier, with exact owned daemon and
private broker peer roles, and removes both private authentication copies.

Preparation additionally requires `PMX_DIALOGUE_CODEX_AUTH_FILE`, an explicitly
selected owned regular 0600 input. No ambient Codex config/history is copied.
The generated [public configuration](https://developers.openai.com/codex/config-reference)
selects file authentication, never approvals, workspace-write with only the
owned root writable, sandbox network disabled, and web search disabled. Fresh
HOME/CODEX_HOME/CODEX_SQLITE_HOME/XDG paths are passed to the direct child.
This configuration describes requested policy; effective current-version policy,
managed system layers, public item availability, and actual source execution
still require reviewed pins and explicit actual-run approval. No old attempt is
rerun by these offline tests. The first actual candidate remains unapproved.
