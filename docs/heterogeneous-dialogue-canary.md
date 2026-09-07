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

## Genuine source transaction

The maintained setup, source action, bounded observation reader and parent
cleanup are connected and have offline regression coverage. The prior
payload-free source recipe is superseded. **Actual execution is not approved.**
The current runner now checks public config/read values/origins before source
creation and release, with offline fixtures. Actual current-version policy
responses and observation shapes remain unverified. Neither config generation
nor command-item observation proves that no earlier endpoint-startup effect
occurred. Those actual boundaries remain open R1 review items,
not completed acceptance evidence or permission to run the command below.
Historical failed attempts and their receipts remain unchanged.

`scripts/agent-dialogue-canary-setup.py` is the single transaction entrypoint.
It invokes prepare, starts the exact owned direct app-server endpoint, creates
the two public Agents, transfers observation to the parent runner and owns
partial-setup cleanup. Do not run prepare and then create actors manually, run
the source action from an operator shell, or invoke the run stage separately.
Those sequences do not establish genuine model-tool origin or the full cleanup
boundary.

Before owner approval, pin the reviewed clean commit and a separately built
0755 candidate, both provider executables, every runner/schema file, the exact
generated prompt/config, a fresh short root, external receipt and external audit.
Use the public current versions Claude 2.1.263 and Codex 0.153.2. The candidate
and direct Codex image must be regular, owned executable files without group or
world write. Authentication inputs must be explicitly selected regular files,
owned by the current UID with mode 0600. Pin their paths and metadata only;
never record their contents or hashes. Runtime UIDs, process births and native
thread/turn/item IDs are obtained after approved creation and frozen before the
first Claude push; they are not fabricated in the pre-run packet.

After that separate approval, the reviewed invocation has this form. Each
uppercase input below is a concrete value from the approved packet; the root,
receipt and audit must all be absent before invocation:

```sh
env -i PATH=/usr/local/bin:/usr/bin:/bin HOME="$HOME" \
  PMX_DIALOGUE_LIVE_CANARY=1 \
  PMX_DIALOGUE_CANDIDATE_HEAD="$REVIEWED_HEAD" \
  PMX_DIALOGUE_PROJMUX_BIN="$CANDIDATE_BINARY" \
  PMX_DIALOGUE_REAL_CLAUDE_BIN="$CLAUDE_BINARY" \
  PMX_DIALOGUE_REAL_CODEX_BIN="$CODEX_BINARY" \
  PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE="$CLAUDE_CREDENTIAL_FILE" \
  PMX_DIALOGUE_CODEX_AUTH_FILE="$CODEX_AUTH_FILE" \
  PMX_DIALOGUE_CANARY_ROOT="$FRESH_ROOT" \
  PMX_DIALOGUE_CANARY_RECEIPT="$EXTERNAL_RECEIPT" \
  PMX_DIALOGUE_MESSAGE_REF="$IDLE_MESSAGE_REF" \
  python3 "$REVIEWED_CHECKOUT/scripts/agent-dialogue-canary-setup.py"
```

The audit path is exactly `${EXTERNAL_RECEIPT}.audit.jsonl`, exclusive mode
0600 outside the disposable root. It records bounded stage/exit/byte counts,
closed source freeze/completion facts and captured writer exit proof before
root removal. It contains no raw provider output, command, reasoning or auth
values. An absent success receipt is a failed/incomplete transaction even if
cleanup removed the root. No failure or unknown write outcome permits resend
or another transaction under the old approval.

## Source action and first-push proof

The parent uses the fixed public default socket constructor
`<root>/codex-home/app-server-control/app-server-control.sock`; its encoded path
must be shorter than 100 bytes. It directly launches the pinned Codex executable
with `app-server --listen unix://<socket>`, retains that child handle and checks
its UID, PID/birth, executable, argv and socket/kernel peer. It does not start
or stop an ambient daemon service. The existing public Project projection
selects the tmux session name. A separate TMUX_TMPDIR, unique `-L`, observed
socket_path and exact socket identity scope both actor creation and cleanup.

The existing public source create receives the genuine approved task:

```text
<candidate> create agent --provider codex --project uid:<P> --window uid:<W> -o pane-id -- <one genuine source prompt argument>
```

The prompt is generated by `source_prompt(root)` and contains exactly the one
`source_command(root)` invocation:

```text
python3 <root>/bin/agent-dialogue-source-action.py <root>
```

The source model is asked to execute that command once from `<root>/work`,
wait for its result, then reply DONE. It is explicitly told not to execute other
commands, inspect authentication/config/history, create subagents, resend or
clean up. This is a genuine initial Codex user task, with normal private original
thread/tool-result writes expected. Claude's reply-only activation separately
starts the fixed `Reply READY.` turn. Neither startup turn is peer-to-user-turn
relay; the runner performs no direct peer history/input API writes.

The source action writes its independently observed process identity and waits.
Before release the parent verifies both runtime-first Agent/Pane chains, exact
current routes/composite source authority and the guarded Claude public init.
It freezes the actual native thread/turn/in-progress command item and separately
matches the action PID/birth, exact script argv and ancestry to the owned native
child. Inherited tmux context or a provider `processId` alone is not origin proof.
The source action builds its public CLI environment from the frozen own Pane,
server and socket; it does not use an inherited create-anchor context.

Only after release does the source action use existing public `agent message`
commands: qualify the exact receiver with `--confirm-isolated-provider-push`,
claim that reply from the original Codex inbox, send one independent idle request,
then claim its correlated reply. Empty follow-up claims check claim-once. Frozen
live routes are rechecked before dispatch. Claude tool/result/guarded-commit
proof is separate from full-frame delivered status. Missing or ambiguous proof
fails without resend. The parent never executes these coordination commands.

Only bounded correlation facts are returned to the original source model. The
parent then compares the completed command's closed result with the independent
qualification/idle proofs and separately observes original-turn completion.
Started-item freeze precedes the first push; completed-item proof follows the
result. The action never cleans up its own provider or parent, and parent cleanup
cannot stand in for a successfully returned source tool result.

## Observation and requested private policy

`agent-dialogue-codex-observation.py` validates the frozen public 0.153.2
`thread/read(includeTurns=true)` and `item/started`/`item/completed` schemas.
The four exports in `scripts/agent-dialogue-codex-schema/` retain their original
hashes, also checked by the reader. `agent-dialogue-native-source.py` connects to
the pinned owned endpoint and performs bounded non-experimental initialization;
the reader sends only `thread/read`. Neither component starts/resumes a thread,
subscribes a relay, sends a user turn, or copies native history to evidence.

Observation is bounded to 1 MiB per frame, 8 MiB/128 frames per reader and 32 items
in the sole expected turn. Initialization is bounded to 16 KiB/five seconds;
read connections have a ten-second deadline. The source release wait, action
result wait and completed-result observation are separately bounded. Unknown
fields/effects, incomplete views, absent source, changed items/routes, wrong
results, EOF or bounds violations fail closed. Explicitly observed non-userShell
source enums are checked against the prepared allowlist; no schema default is
applied. `processId` is never treated as an OS PID. Raw text/command/output,
reasoning and auth data stay out of the returned facts and external audit.

The generated [public configuration](https://developers.openai.com/codex/config-reference)
requests file authentication, never approvals, workspace-write with the owned
root as an additional writable root, `/tmp` and TMPDIR expansion excluded,
sandbox network disabled, web search disabled and no startup update check.
Fresh HOME/CODEX_HOME/CODEX_SQLITE_HOME/XDG paths are passed to the direct child;
ambient config/history and keyring policy are not copied. Both auth inputs are
copied only during the approved transaction to private mode-0600 files.

`agent-dialogue-native-policy.py` uses the two original public 0.153.2
ConfigRead schemas under `scripts/agent-dialogue-config-schema/`. On the same
owned endpoint it requests only `config/read` with the exact work cwd and
`includeLayers=false`. It requires the prepared approval, sandbox/network,
writable-root, web-search, file-auth and update values. Each selected dotted
leaf must have an explicit origin in the owned user config; a missing origin
is not inferred from its parent or default. Other reported origins must be
that same user file or packaged defaults under the pinned distribution.
Managed, project, session, unknown or mismatched origins fail closed. Nonempty
MCP/plugin/hook/instruction inputs and returned raw layers are refused.

Only those closed values, origin classes/counts and boolean assertions are
retained, including in the external audit. Arbitrary additional config fields,
origin revision strings, instructions and secrets are discarded. The response
has a one-MiB/five-second bound. The parent checks policy before public source
creation, then rereads it on the exact current peer immediately before release
and requires identical facts and an unchanged owned config/socket/process.
An otherwise-valid source action receives no release when policy changes.

This is resolved config evidence, not a ThreadStartResponse observation. The
existing public create sends cwd and runtime workspace roots; this reader adds
no dummy thread/start, resume, policy mutation or relay to obtain more evidence.
The original thread's actual policy/override boundary and the current public
origin representation remain explicit review/actual-verification limits.
Endpoint-startup effects preceding the read are not retroactively certified.
Offline fixtures prove rejection/ordering and data minimization, not actual
provider execution or a waiver of that remaining boundary.

## Parent cleanup and remaining acceptance evidence

The parent owns one cleanup transaction on every partial failure and after the
source tool result returns. It captures owned writer births before teardown,
including the direct daemon, source action and exact kernel peers of the private
broker discovery directory. Broker credential records are not read. Signals use
pidfds for only validated captured roles, alongside exact public Project/tmux
teardown. The broker's 30-second idle default is not treated as proof of exit
within the existing 20-second writer deadline.

Both owned auth copies are removed on success and retained-root failure paths.
The exact-root writer exit proof is preserved in the external audit before one
root removal. Unknown writer exit, audit failure or removal failure remains
failure and preserves evidence; there is no fixed-sleep proof, broad signal,
retry removal or manual-cleanup PASS. No protected/shared process is an owned
seed. Auth source files are not modified. A successful external receipt requires
closed model/tool/claim proofs and automatic cleanup; it does not certify other
acceptance cases.

The selectorless E2E remains the single deterministic L20 scenario. Actual
active-tool/human overlap, multiple ordinary requests, same-UID recovery and
installed smoke remain separate unverified evidence. They are not run by this
qualification-plus-one-idle transaction and are not inferred from its result.
