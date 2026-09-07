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

## Review and launch

Before owner R1 approval, freeze the pushed commit, independently built candidate
and SHA-256, these three scripts, resolved real provider executables/versions,
a fresh root, an external receipt path and its separate `<receipt>.audit.jsonl` path.
Both external paths must be fresh; the audit is exclusive 0600 and never a success
receipt. Pin candidate ownership/mode as well: it must be an owned regular
executable with no group/world write bits (for example 0755, never 0775). The
preflight checks this before credential copying or actor creation. The candidate image and scripts must
come from that reviewed head. No old root, running Agent, qualification or copied
provider evidence is reusable. Runtime-generated UIDs/PIDs/births are discovered
inside the private setup after approval and frozen before the first inbound.

The canonical entrypoint below runs prepare and setup as one cleanup transaction.
Do not manually create actors between `prepare` and `run`. Substitute the exact
reviewed values; this example is a plan, not approval to launch:

```sh
PMX_DIALOGUE_LIVE_CANARY=1 \
PMX_DIALOGUE_CANDIDATE_HEAD=<reviewed-40-character-commit> \
PMX_DIALOGUE_PROJMUX_BIN=/absolute/reviewed/projmux \
PMX_DIALOGUE_REAL_CLAUDE_BIN=/absolute/current/claude \
PMX_DIALOGUE_REAL_CODEX_BIN=/absolute/current/codex \
PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE=/absolute/authorized/.credentials.json \
PMX_DIALOGUE_CANARY_ROOT=/tmp/projmux-dialogue-<fresh-reviewed-run> \
PMX_DIALOGUE_CANARY_RECEIPT=/tmp/projmux-dialogue-<fresh-reviewed-run>.receipt.json \
PMX_DIALOGUE_MESSAGE_REF=message-heterogeneous-live-canary \
python3 scripts/agent-dialogue-canary-setup.py
```

Prepare pins the candidate digest/head, runner file digests and provider paths,
copies only the explicitly selected authentication source into an owned 0600
file, and records its metadata. Credential values/hashes are not receipts. The
source/copy bytes are compared only in memory before success. Ambient global
Claude settings are observed and must remain unchanged.

Setup constructs a fresh environment: private HOME/CODEX_HOME/XDG/TMUX_TMPDIR,
owned provider aliases, and no inherited TMUX, TMUX_PANE, CLAUDE_CONFIG_DIR,
messaging credentials or ambient Projmux routing. It uses a unique tmux `-L`
name, immediately verifies the actual socket is below the owned root, then uses
public project creation/reconcile and these exact actor command shapes in the
owned anchor context:

```text
projmux create agent --provider codex --project uid:<P> --window uid:<W> -o pane-id
projmux create agent --provider claude --dialogue-reply-only --project uid:<P> --window uid:<W> -o pane-id
```

Codex receives no initial payload. The shipped launcher/broker supplies its
current composite authority; setup fabricates no binding. Claude uses the product's
public observer, with no external collector, FIFO or body relay. Setup writes
private `canary-input.json` version 2 after both runtime-first Agent↔Pane chains
resolve uniquely in the owned Project/Window. It waits boundedly for the observed
profile and current source route, then hands off to the same-root run trap.

## Qualification and idle evidence

Before traffic, the companion checks candidate/route/process/socket incarnations,
exact helper SO_PEERCRED, credential-key absence and current helper-memory public
init. Bash alone, empty MCP/plugins, no prior tool/stderr effects and unqualified
general admission are required. Profile evidence is read through the existing
coordination UDS; the companion never opens the vendor inbox.

The original Codex context performs these public operations:

```text
agent message wait uid:<source> --timeout 1ms -o json
agent message qualify uid:<Claude> --confirm-isolated-provider-push -o json
agent message wait uid:<source> --timeout 5s -o json
agent message status <qualification-ref> -o json
agent message send --message-ref <ordinary-ref> --ttl 2m uid:<Claude> -- <harmless explicit acknowledgement request>
agent message wait uid:<source> --timeout 120s -o json
agent message status <ordinary-ref> -o json
```

The initial/after-claim empty waits must fail with only the documented no-compatible-message
diagnostic. Every wait reuses public runtime-owner and live composite-route
admission; a harness PID alone is not self-claim proof. Qualification and idle
have distinct original refs, tool IDs, broker commits and claims. Each requires
public envelope version 2, exact reversed routes, peer coordination authority,
matching conversation/replyTo/payload, `target-self-claim`, unknown-outcome false,
and the independently observed tool/result matched to the successful guarded
broker commit. Full-frame `delivered` alone does not meet this proof. Codex state
files must remain unchanged; authentication files are excluded from hashing.

Only read-only observation may retry for a late tool result. Missing/invalid
observer, ambiguous/partial write, unmatched action or timeout ends the run;
there is no qualification or request resend. Raw stdout/model text, thinking,
signatures and commands are not copied into the external receipt. Unknown helper
response fields fail closed or are omitted by the explicit projection.

## Automatic cleanup and remaining cases

The setup wrapper owns failures before the first/second actor or input file is
ready. Its bounded external audit records closed stage identifiers, numeric
command exit codes and output byte counts; it records no raw argv, output,
model content or credential material. Stage evidence reports only which boundary
was entered/completed, never infers qualification from elapsed time. The audit
retains the exact captured PID/birth writer proof before root removal, including
an explicitly incomplete proof when writer exit fails. It is limited to 128
records, 128 KiB per record and 1 MiB total; replacement, unsafe permissions or
missing proof fail closed and retain the root. A terminal record distinguishes
root absence from semantic success. Earlier failures whose root-local evidence
was deleted cannot be retroactively assigned a stage or cleanup proof. The run trap owns every later outcome. Both use the same exact-root pidfd
writer barrier, capturing owned process births before public deletion and exact
socket teardown. Cwd/ancestry preserve ownership across delayed and reparented
writers. No discovered process receives a broad signal; an unproven writer keeps
the root and reports failure. The owned authentication copy is removed even on
a retained-root cleanup error when its original root identity remains valid.
A run cleanup attempt is never retried by the setup wrapper.

Success is published to the fresh external receipt only after automatic writer
exit, empty owned Registry/profile/lease/socket checks and exact root removal.
Cleanup failure is not PASS. The receipt explicitly lists these unverified R2
cases: allowed-tool active overlap, model-visible human overlap, multiple ordinary
A/B requests, same-UID normal-exit recovery and installed smoke. Later reviewed
runs must retain separate evidence for each; idle success cannot expand to them.
Typed terminal text is discarded and cannot prove a model-visible human prompt.

`test/agent_dialogue_canary_test.py` exercises offline receipt/route mutations,
prepare-without-launch, fresh environment and delayed/early-failing setup cleanup.
`TestClaudeDialogueCanaryAcceptsProductionStoreAndPublicClaimReceipts` sends actual
production store/CLI JSON through the companion. Product Go stream tests cover
public output isolation and privacy. Provider/version stress remains separately
opt-in through `scripts/agent-dialogue-version-stress.sh` and never joins required
selectorless E2E.
