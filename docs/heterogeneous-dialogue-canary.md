# Heterogeneous Dialogue Canary

The required release test is the selectorless offline `L20` E2E. This runbook
is an additional opt-in observation against real provider binaries. It is not a
provider-version guarantee and must never run against an existing fleet,
Project, Window, Agent, tmux socket, XDG root, or global Claude settings.

## Isolation gate

Use an absolute disposable root outside `$HOME`. `prepare` creates isolated XDG,
tmux, and Claude config directories, snapshots the global Claude settings bytes
and metadata, and publishes the cleanup plan before any provider is launched:

```sh
export PMX_DIALOGUE_CANARY_ROOT=/tmp/projmux-dialogue-canary-<unique>
export PMX_DIALOGUE_CANARY_RECEIPT=/tmp/projmux-dialogue-canary-<unique>.receipt.json
export PMX_DIALOGUE_PROJMUX_BIN=/absolute/path/to/projmux
export PMX_DIALOGUE_REAL_CLAUDE_BIN=/absolute/path/to/claude
export PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE=/absolute/path/to/explicit/.credentials.json
export PMX_DIALOGUE_MESSAGE_REF=message-heterogeneous-live-canary
scripts/agent-dialogue-live-canary.sh prepare
```

The root must be a clean path below an already-existing non-symlink parent and
must not resolve to `$HOME` or below it; the only accepted symlink-parent
exception is the operating system's standard macOS `/tmp`/`/var` alias. The
external receipt must be a fresh non-symlink path whose parent already exists
and which resolves outside the root.

`PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE` is the one explicitly authorized
authentication source. Prepare snapshots its SHA-256/size/mode/mtime without
printing content and copies it mode 0600 into the owned root; it never symlinks
Claude back to the source.
Prepare sets `HOME=$root/home`, generates `$root/home/.claude/settings.json` by
running the public integrate dry-run and enable commands there, and writes an
exact `claude` wrapper under `$root/bin`. Global settings are not an input to
integration and stay read-only baseline evidence. Prepare also creates a
private versioned sentinel and cleanup plan that bind the real owned root and
exact candidate binary. `run` performs no cleanup mutation unless that root,
plan, sentinel, candidate, Registry, and tmux socket pass their exact ownership
checks.

Only after the cleanup-plan receipt exists, create one disposable Project and
Window in the isolated roots. The exact environment for every tmux and Projmux
setup call is:

```sh
root=$PMX_DIALOGUE_CANARY_ROOT
socket=dialogue-canary-<unique>
canary_env=(env -u TMUX -u TMUX_PANE \
  HOME="$root/home" CODEX_HOME="$root/codex-home" PATH="$root/bin:$PATH" \
  XDG_CONFIG_HOME="$root/xdg-config" XDG_STATE_HOME="$root/xdg-state" \
  XDG_RUNTIME_DIR="$root/xdg-runtime" XDG_CACHE_HOME="$root/xdg-cache" \
  TMUX_TMPDIR="$root/tmux" PROJMUX_MANAGED_ROOTS="$root")
mkdir -p "$root/work/project"
anchor="$("${canary_env[@]}" tmux -L "$socket" new-session -d -P -F '#{pane_id}' \
  -s work-project -c "$root/work/project" sleep 3600)"
socket_path="$("${canary_env[@]}" tmux -L "$socket" display-message -p -t "$anchor" '#{socket_path}')"
server_pid="$("${canary_env[@]}" tmux -L "$socket" display-message -p -t "$anchor" '#{pid}')"
"${canary_env[@]}" tmux -L "$socket" set-option -t work-project -q \
  @projmux_project_path "$root/work/project"
project_uid="$("${canary_env[@]}" "$PMX_DIALOGUE_PROJMUX_BIN" create project \
  --root "$root/work/project" --name dialogue-canary -o uid)"
"${canary_env[@]}" "$PMX_DIALOGUE_PROJMUX_BIN" reconcile resources \
  --socket-path "$socket_path"
window_uid="$("${canary_env[@]}" tmux -L "$socket" show-options -wqv -t "$anchor" @projmux_window_uid)"
inside() {
  local pane=$1; shift
  "${canary_env[@]}" TMUX="$socket_path,$server_pid,0" TMUX_PANE="$pane" \
    "$PMX_DIALOGUE_PROJMUX_BIN" "$@"
}
codex_pane="$(inside "$anchor" create agent --provider codex \
  --project "uid:$project_uid" --window "uid:$window_uid" -o pane-id)"
claude_pane="$(inside "$anchor" create agent --provider claude \
  --project "uid:$project_uid" --window "uid:$window_uid" -o pane-id)"
```

Create exactly those two Agents before traffic and retain their exact Agent UID,
Pane UID, tmux `%N`, and activation generation from public get/describe output.
The payload-free Codex create must produce a current exact native route; if it
does not, stop and clean up rather than starting a Codex prompt. Message send is
not an Agent creation route. Prepare also links `$root/bin/projmux` to the exact
candidate binary, and the gate records its SHA-256 so official hook children
cannot accidentally resolve an older installed protocol. The generated Claude
wrapper is a hook-enabled long-lived `--input-format stream-json` process with restricted,
strict empty MCP, empty tools, explicit owned settings, no session persistence,
and public stream/stderr capture. It intentionally does not use `--safe-mode`.
Raw provider stdout flows only through an owned in-memory collector. The
collector admits the frozen 2.1.263 `system/init`, text-only `assistant`, and
successful `result` shapes, replaces `messaging_socket_path` with the boolean
`messaging_socket_present`, and rejects unknown event types or fields before
writing the sanitized JSONL. It never writes or hashes the messaging socket or
token and enables no provider debug-log directory.
The wrapper opens its owned FIFO read/write, enqueues exactly this initial human
instruction before `exec`, and retains the write descriptor so the same Claude
process remains long-lived:

```text
When a Projmux coordination message contains payload HETEROGENEOUS_REQUEST:message-heterogeneous-live-canary, answer with exactly HETEROGENEOUS_REPLY:message-heterogeneous-live-canary and nothing else. Do not use tools.
```

Wait for that turn's public result and exact SessionStart registration. There
is no Stop ingress waiter. Confirm the wrapper PID is still alive and leave
`provider.jsonl` untouched. The run command re-probes registration capability,
requires target coordination to remain unqualified, reads and freezes the live
stream once, writes identical owned `init.jsonl` and `pre-inbound.jsonl`
snapshots plus a private 0600 qualification-evidence file, and rechecks the live
stream hash immediately before qualification. There is no manual copy race.

```sh
test ! -s "$root/evidence/provider.stderr"
test -s "$root/evidence/provider.jsonl"
kill -0 "<exact-provider-pid>"
```

Launch the real Claude process once, with the owned settings and public
stream-json capture. It must remain that same process through the canary. A
safe-mode one-shot is not evidence because safe mode disables hooks. Before
inbound traffic, make these owned evidence files available to the gate:

- the live public JSONL containing exactly one `system/init` record and every
  public event observed before inbound (the gate freezes the exact bytes);
- provider stderr;
- an external-effects JSON object exactly equal to
  `{"connectorWrites":0,"externalWrites":0,"preInboundToolUse":0}`;
- the exact owned Claude settings JSON.

The sanitized init record must report Claude Code exactly `2.1.263`, the same registered
Claude `session_id`, and exact `tools=[]`, `mcp_servers=[]`, and `plugins=[]`.
It must also carry only the current frozen field allowlist and sanitized
`messaging_socket_present=true`. No recursive `tool_use` object, unknown event
or field, stderr byte, connector call, or external write is allowed. A
different installed version fails before traffic and needs its own isolated
qualification; `2.1.261` evidence never carries forward. The script also proves
that the public process PID/owner/birth identity is the one in the current exact
Pane/generation registration, the exact helper PID/owner/birth runs the fixed
`projmux internal claude-endpoint-helper` argv from the candidate binary, both
messaging credential keys are absent from that helper's environment, and the
derived private activation lease belongs to the same Pane/generation. Only key
absence is observed; values and hashes are never read into a receipt. The
cleanup plan must predate provider launch.

Write `$PMX_DIALOGUE_CANARY_ROOT/canary-input.json` with this closed shape. All
evidence, Registry, settings, and tmux socket paths must resolve beneath the
owned root; only the executable may be elsewhere.

```json
{
  "binary": "/absolute/path/to/projmux",
  "registryPath": "/tmp/owned/xdg-state/projmux/metadata/registry.json",
  "tmuxSocketPath": "/tmp/owned/tmux/tmux-1000/owned",
  "tmuxSocketName": "owned",
  "tmuxServerPID": 1234,
  "projectUID": "project-uid-without-prefix",
  "windowUID": "window-uid-without-prefix",
  "messageRef": "message-heterogeneous-live-canary",
  "sender": {
    "agentUID": "agent-uid",
    "paneUID": "pane-uid",
    "paneID": "%7",
    "generation": "activation-generation"
  },
  "receiver": {
    "agentUID": "agent-uid",
    "paneUID": "pane-uid",
    "paneID": "%8",
    "generation": "activation-generation"
  },
  "provider": {
    "pid": 4321,
    "ownerUID": 1000,
    "start": "linux:<boot-id>:<start-ticks>",
    "startedAtEpochNs": 1788680000000000000,
    "sessionID": "public-init-session-id"
  },
  "evidence": {
    "providerLiveJSONL": "/tmp/owned/evidence/provider.jsonl",
    "providerStderr": "/tmp/owned/evidence/provider.stderr",
    "externalEffectsJSON": "/tmp/owned/evidence/external-effects.json",
    "ownedSettingsJSON": "/tmp/owned/home/.claude/settings.json",
    "providerStartedAt": "/tmp/owned/evidence/provider-started-at"
  }
}
```

The provider start epoch is the integer written by the generated wrapper to
`evidence/provider-started-at`; its PID/owner/birth/session values come from the
same receiver Pane's Registry registration and public init. The `uid:` prefix
is added by the script; input UID values are raw Registry UIDs. Replace every
`/tmp/owned` with the exact prepared root.

## Run and receipts

Review `cleanup-plan.json`, the exact resource chain, and the frozen public
evidence. Then opt in:

```sh
PMX_DIALOGUE_LIVE_CANARY=1 scripts/agent-dialogue-live-canary.sh run
```

The script contains no `agent message` call before it atomically writes
`evidence/traffic-gate.json`. After the gate, the dedicated explicit
`agent message qualify` operation pushes one unique marker and waits for its
exact ordinary Stop correlation; only then can target coordination become
eligible. It next starts the exact Codex self-claim, sends one semantic marker
to the existing exact Claude Agent, and requires one correlated reply with the
same `conversationRef`, `replyTo` equal to the original `messageRef`, payload exactly
`HETEROGENEOUS_REPLY:<messageRef>`, exact reversed
Agent/Pane/generation/incarnation routes, and `target-self-claim`. Agent count
stays exactly two. It snapshots the isolated `CODEX_HOME` tree immediately
before and after traffic and requires identical digests, proving the dialogue
made no Codex app-server, user-turn, or model-history write.

Cleanup uses only the exact Project UID and root-contained tmux socket. Before
removing the disposable root it requires Registry Project/Agent rows, owned
processes, tmux/helper sockets, connector/external writes, and credential
residue to be zero, and requires the global Claude settings snapshot to be
byte/size/mode/mtime identical. It checks the captured exact provider and helper
birth identities rather than relying on argv search, and removes only the
derived exact-owned `/tmp/pmx-ce-*` activation lease after both births are gone.
After provider/tmux shutdown it deletes the owned credential copy, proves it
absent, and proves the authorized source's SHA-256/size/mode/mtime unchanged.
The receipt outside the root records exact UID/generation/message/conversation/
reply, sanitized helper credential-key absence, and all residual counters. It
does not expose the credential source path or credential hashes.

If a later gate or traffic step fails, the registered trap still deletes the
exact Project, kills only the same validated tmux socket inode, and removes only
the same validated prepared root inode. If ownership preflight fails, no cleanup
mutation is armed. It never signals an ambient process or edits global settings.

## Optional version stress

Version stress is not required CI. Create a TSV whose non-comment rows contain
one label and one absolute executable runner separated by a tab. Each runner
must independently perform the prepare/evidence/run sequence above with a
different owned root and provider tuple.

```text
# label<TAB>runner
claude-a_codex-a	/tmp/dialogue-matrix/claude-a-codex-a.sh
claude-b_codex-a	/tmp/dialogue-matrix/claude-b-codex-a.sh
```

Run it only with explicit opt-in:

```sh
PMX_DIALOGUE_VERSION_STRESS=1 \
PMX_DIALOGUE_VERSION_MATRIX=/tmp/dialogue-matrix/matrix.tsv \
scripts/agent-dialogue-version-stress.sh
```

The matrix wrapper adds no traffic authority; every row must pass its own live
isolation gate and exact cleanup.
