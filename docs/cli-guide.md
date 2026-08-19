# CLI Task Guide

The hand-maintained half of the CLI documentation: what each command is for,
which flags change its behavior, and the contracts a route listing cannot
express.

The route inventory itself is not here. [CLI Reference](cli.md) is generated
from the command manifest the binary renders its own help from, so it is the
authority on which routes, sub-routes, output modes, and field projections
exist in a given build. This guide is the authority on everything a one-line
summary leaves out. When the two disagree about whether a route exists, the
generated page is right and this one is stale.

Run `projmux help` for the live top-level list, or `projmux <cmd> --help` /
`projmux <cmd> help` for the per-command usage string. See
[Help boundary](#help-boundary) for the exit-code and stream contract shared by
every `--help` invocation.

Exit codes:

- `0` — success.
- `1` — runtime failure.
- `2` — usage error (unknown flag, bad enum, missing required flag) or a
  deterministic semantic exit (e.g. `focus` cannot resolve the target).

## Help boundary

Help is handled once, at the root, from the shared command manifest rather than
by each leaf parser. Matching is on the flag **name** — the part after the
leading dashes and before any `=` — so every spelling the Go `flag` package
treats as help goes through this one boundary: `--help`, `-help`, `--h`, `-h`,
and any `=value` form of those (`--help=true`, `--help=false`, ...). `flag`
returns its help result for all of them regardless of the value, so aligning
with it keeps one notion of help; the boundary never interprets a flag value:

- Every help invocation exits `0`, writes to **stdout** only, records no
  operational error, and performs no tmux, runtime, or lifecycle-migration
  access. This includes nested routes (`projmux config edit --help`) and the
  hidden internal namespace (`projmux internal popup-wait-key --help`).
- Help resolves to the deepest documented route and shows its summary, usage
  synopsis, sub-routes, route-local output projections, and the canonical route
  spelling it will move to. The synopsis names the route's flags (for example
  `projmux setup terminal [terminal] [--apply] [--config <path>]
  [--allow-symlink]`); the per-flag prose descriptions come from the command's
  own parser and are not reproduced here.
- A help flag after the first bare `--` is payload, not help:
  `projmux create agent -- --help` forwards `--help` to the launched process
  unchanged.
- An unknown command keeps its `unknown command: <token>` error and exit `1`
  even with `--help`, and `projmux help` / bare `projmux` keep printing the
  top-level list. A bare `help` word nested under a command
  (`projmux pin help`) still reaches that command's own handler.
- Because a help invocation runs no handler, it is never recorded as a state
  change in the operations journal, at any depth — `projmux agent topic set --help`
  logs neither an error nor a state-changing success.

## Command inventory

The full route list -- every public top-level command, its sub-routes, its
usage synopsis, its shared `-o` output modes, and its route-local field
projections -- lives in the generated [CLI Reference](cli.md). That page is
rendered from the same command manifest the binary renders `projmux help`
from and is verified against it on every `make test`, so it cannot drift.
Nothing in this guide restates it.

## Resource selectors and the active target

The resource routes (`get`, `describe`, `create`, `rename`, `rebind`, `delete`,
`agent resume`) address stored resources through one shared selector grammar.

The grammar in one paragraph: a value is either `uid:<uid>` or a bare
`metadata.name`. There is no bare-uid form, values are never split on commas,
and a `displayName`, a `spec.root` path, or a raw tmux `%N`/`@N`/`$N` handle
never resolves anything. `--project`/`-p` occurs at most once and fixes the
Project scope; `--window`/`-w` and `--pane` repeat and union in argv order;
`--selector key=value`
repeats and ANDs. A singular route also accepts the target as a positional
`<ref>`, which may appear before or after the flags. How many resolved targets
each `<verb, kind>` pair accepts is a declared matrix, not a per-route rule, and
a violation is exit `2` with a candidate listing bounded to five rows.

`-p` always means Project, never Pane; Pane has no shorthand. The three plural
reads `get windows|panes|agents` also accept `-A` as the exact alias of
`--all-projects`. That escape is mutually exclusive with either spelling of the
Project selector and is not registered on any other route. Long-only,
short-only, and mixed scope occurrences share the same value sinks, so repeated
Window order, cardinality, output, and mutation plans do not depend on spelling.
For resource-backed `create pane|agent|<provider>`, only aliases before the
first `--` are Projmux scope flags; every token after it, including `-p`, `-w`,
`--project`, and `--window`, remains opaque payload in its original order.

### Empty selector: the active tmux target

Inside tmux, an invocation that carries **no selector at all** addresses the
resource you are looking at instead of the whole registry:

```
projmux describe pane            # the active pane
projmux describe window          # the window the active pane is in
projmux describe project         # the Project that owns that window
projmux describe agent           # the Agent that owns the active pane
projmux rename pane --name build # renames the active pane
projmux rebind project --root /new/path
projmux get pane -o uid
```

The contract:

- **Omission is the only spelling.** There is no `--pane current` and no
  `--pane active`. Those are legal `metadata.name` values today — `rename pane
  --name current` succeeds — so a sentinel token would silently shadow a real
  resource.
- **"No selector at all" means exactly that.** Any positional `<ref>`, any
  `--project`/`-p`, `--window`/`-w`, `--pane`, or any `--selector` label keeps
  picking the target itself. The active *target* is never blended into a
  partially specified selector. The active *Project* is a separate rule and does
  apply to a reference -- see [Reference scope](#reference-scope-the-active-project-namespace)
  below.
- **Only the singular routes and `create`.** The plural reads (`get
  projects|windows|panes|agents`) stay 0..N inventories over their whole scope,
  and `delete` is unchanged. `create` has its own spelling of the same rule --
  see [Create scope](#create-scope) -- because a create resolves a scope to put
  something *into* rather than a target to act *on*.
- **Inside tmux is decided by `$TMUX_PANE` plus `$TMUX`**, not by whether a tmux
  server answers. A bare `display-message` from outside a client still succeeds
  and answers for the most-recently-used session; projmux never uses that.
  Outside tmux the empty-selector invocations keep their previous
  `matched N ..., want exactly one` error and exit `2`.
- **No persistent scope.** There is no `set-context` equivalent and no stored
  "current pane": the target is read from tmux focus on every invocation, so
  there is nothing to go stale and nothing to invalidate.
- **`projmux describe pane` with no selector is the preview.** Because every
  verb in this family resolves through the same seam, whatever `describe pane`
  shows is exactly what `rename pane --name X` will act on;
  `projmux get pane -o uid` (or `-o name`) gives the same answer as a scalar.

Resolution reads only two tmux options — `@projmux_pane_uid` on the active pane
and `@projmux_window_uid` on its window — and derives every ancestor from
registry `ownerRef`. The session-scoped `@projmux_project_uid` is deliberately
not consulted.

### Reference scope: the active Project namespace

A `metadata.name` is unique inside its owner scope, never across the registry: a
Window name is unique inside its Project, a Pane name inside its Window or
Agent, an Agent name inside its Window. So inside tmux a reference is resolved
inside the Project that owns the active Window, which is the same universe
`get windows|panes|agents` already reads:

```
projmux describe window zsh    # the Window named zsh in *this* Project
projmux describe pane log      # the Pane named log in this Project
projmux describe agent codex   # the Agent named codex in this Project
projmux rename window zsh --name review
projmux rename pane log --name build
```

The applied matrix is `describe window|pane|agent` and `rename window|pane`.
`describe project` and `rename project` are not in it — a Project has no
enclosing Project — and neither are `delete`, `rebind`, `agent resume`, or
`rename agent`, which keep their whole-registry meaning.

The contract:

- **It narrows the search, it does not pick the target.** The Project fixes the
  universe and nothing else. The active Window and the active Pane are not used
  to break a tie, so two same-named resources inside the one Project stay the
  ordinary bounded `matched N ..., want exactly one` ambiguity. Resolving that
  is what `--window`/`--pane` and a `uid:` reference are for.
- **Explicit `--project`/`-p` wins, with zero observations.** Naming a Project
  never costs a tmux round trip and never depends on the pane you are sitting
  in.
- **A `uid:` reference is scoped too.** A uid that belongs to another Project is
  a no-match, not a cross-Project hit. Pass `--project` to address it.
- **Outside tmux nothing changes.** The whole registry is searched and the
  previous `matched N ..., want exactly one` ambiguity is unchanged. No default
  tmux server is probed.
- **Inside tmux a broken owner chain refuses.** A pane carrying no
  `@projmux_window_uid`, a mirrored Window uid the registry does not hold, or a
  Window with no owning Project is exit `2` with zero bytes on stdout and zero
  mutations. It is never a silent fallback to the whole registry, because that
  is exactly the cross-Project match this rule exists to prevent.

### Create scope

`create window|pane|agent|<provider>` is resource-backed on every spelling.
There is no mode flag and no second parser: the same argv means the same thing
whether or not `--project` is present, and `-w`, `--create-window`, `--pane`,
`--selector`, `--placement`, `--name`, `--label`, `--cwd`, `--add-dir`, `-o`,
and the `--` payload all reach the same parser either way.

The scope resolves in two branches:

- **Explicit `--project`/`-p` wins**, inside tmux and outside it. The active
  tmux target is not consulted at all.
- **With no `--project`, the Project comes from the active managed runtime**:
  the `@projmux_window_uid` mirrored on the pane you are in, and that Window's
  registry `ownerRef`. This is the same seam the empty-selector reads use.

The Window and the anchor Pane follow the *whole* scope rather than the Project
flag alone:

```
projmux create codex                       # active Project, active Window, split from the active Pane
projmux create codex -w hi --create-window  # active Project, new Window "hi"
projmux create codex -p beta -w main        # everything explicit
projmux create pane -p alpha                # every Window of alpha; a deliberate fan-out
```

One explicit scope occurrence (`--project`, `--window`, `--pane`, or
`--selector`) makes the whole scope explicit, so naming a Window never picks up
an anchor from somewhere you did not address. With a scope but no `--pane`, the
anchor is the target Window's stored `spec.primaryPaneRef`, and a missing or
stale ref is exit `2` rather than a silent repair.

Refusals are exit `2` with zero Registry writes and zero tmux mutations, and
they name `--project` as the fix:

- outside tmux with no `--project` — no default server is probed;
- inside Home, a control session, an unattributed pane, or a foreign pane —
  none of those carry a managed identity, and projmux never invents a Project
  from `$HOME`, a session name, or a cwd;
- a mirrored uid the Registry does not hold, or a Window whose owning Project is
  gone — a `recoverable` runtime is reported, never adopted.

Every create is **detached**: no create moves the client. Use `focus pane` or
`-o pane-id` when you want to end up in the new pane. Creates run against the
inherited exact socket inside tmux; outside tmux an explicit `--project` is
required before anything live is touched.

#### Splits started from a popup

The split UI's own pickers (`M-7`, `M-4`/`C-r`, the pane context menu, and the
default split key when the saved mode is `selective` or `resume`) run inside a
`display-popup`. tmux exports `$TMUX` to a popup job and deliberately exports no
`$TMUX_PANE`, because a popup is not a pane — so the picker has no inherited
target of its own while still knowing, from the keypress that opened it, which
pane the operator was in. That pane travels on the create intent as an explicit
anchor and resolves Project, Window and split anchor through the same identity
mirror a pane-hosted invocation reads.

The anchor is something the split UI hands to `create`, never something `create`
reads from the environment. `$TMUX_SPLIT_TARGET_PANE` is not a scope override:
typing `projmux create pane --placement right` inside a popup is still an
invocation with no target and still refuses with the `--project` usage error
above, and no read, rename, or delete verb consults it.

### Rename and rebind live convergence

`rename project|window|pane` commits the selected Registry `metadata.name` and
then updates only its exact UID-bound live transport field:

- Project: `@projmux_project_name` (never the tmux session name)
- Window: `@projmux_window_name` (never `metadata.displayName` or tmux
  `window_name`)
- Pane: `@projmux_pane_label` (never raw `pane_title`)

`rename agent` changes only the Agent's stable Window-scoped `metadata.name`.
It does not change the Agent topic, provider, lifecycle state, or managed Pane
name/title. `rebind project` preserves uid and session name, moves no files,
and updates only `spec.root` plus the exact session's
`@projmux_project_path` anchor.

The Registry commit is authoritative. Inside tmux, immediate convergence uses
only the inherited absolute socket path (`tmux -S`) and stable `$N`/`@N`/`%N`
handles. Outside tmux the command probes no default server and remains
Registry-only. If the exact UID is not present on that transport, or its
inventory cannot establish that it is live, the command succeeds with offline
drift that a later explicit-socket `projmux reconcile resources` can converge.
If an exact live target is found but its option write fails, or more than one
live object claims the UID, the command exits nonzero, states that Registry data
already committed, and names the same public reconcile route as the retry. A
valid unique Project UID wins over the old path after rebind; unknown and
duplicate UID claims remain fail-closed.

### Agent topic, interaction, activation, and workspace

`agent topic get|set|clear` and `agent status get|set` resolve exactly one
Agent, either from an explicit Agent reference or from the Agent-owned active
managed Pane. Topic is a non-identifying Registry annotation. Interaction is a
separate semantic field with the closed values `unknown`, `idle`,
`in_progress`, `approval_required`, `input_required`, and
`response_complete`; it never changes the Agent lifecycle
`Pending`/`Running`/`Offline`/`Failed`. Offline, Failed, unbound, and stale
observations read as current `unknown`, so a completed badge cannot survive as
current state after its Pane is gone.

`agent status set` always records the closed `manual` source; there is no public
free-form source flag. Compatibility `ai status` and provider hooks forward to
the same Agent authority with the closed `compatibility-ai` and `provider-hook`
sources. Registries written before this field existed may still be read with an
empty source, but new mutations cannot persist arbitrary prompt, credential, or
operator text as provenance.

For a Running Agent, the exact pane's topic and
`@projmux_ai_state`/`@projmux_ai_badge_kind`/`@projmux_attention_state` are live
projections of Registry authority. A failed exact write exits nonzero after the
Registry commit and names `projmux reconcile resources` as the retry. An
Offline topic stays stored and is projected when `agent resume` creates the new
managed Pane. Window badges remain derived presentation: Agent semantic badges
and shell Pane manual attention share the existing priority reducer, while
`dot`/`emoji`/`off`, glyphs, colors, and the Window aggregate are never stored
in resource metadata.

Resource-backed Agent create accepts provider-neutral `--cwd <absolute>` and
repeatable `--add-dir <absolute>`. Explicit paths must exist, resolve without a
symlink escape, and remain inside a registered Project tree; only Codex and
Claude accept additional writable roots. These flags change the Agent's
effective launch workspace, not the Window's owning Project. The Agent stores
the effective cwd and exact caller-provided additional roots, while its managed
Pane stores the effective cwd. Provider argv translation (`-C`/`--add-dir`) is
an implementation detail rather than roadmap prompt knowledge.

The default owner Project root is subject to the same existing-directory and
canonical-path validation before create performs any Registry or tmux mutation.
Resume revalidates the persisted workspace against the current registered-root
and provider rules before creating a Pane. A pre-workspace Agent projects its
owner Project root from `get`/`describe`, and a successful resume persists that
normalized effective workspace without changing Window Project ownership.

When `create agent -- <initial-prompt>` is used, normal resource creation and
provider activation are distinct. Projmux waits for bounded hook/lifecycle
metadata only; it never captures pane content or stores the prompt. If
activation cannot be confirmed, the command exits nonzero while naming the
exact Agent UID and Pane plus safe provider retry and `delete agent ... --yes`
cleanup options. The live resources remain explicit and retryable rather than
being reported as an ordinary success.

Activation metadata is bounded to provider-hook provenance and fixed
acknowledged/timed-out/failed diagnostics. Provider error strings and initial
prompt text are never stored. Resource-backed Agent create and resume do not
start the legacy title/content watcher; that watcher remains only for legacy
non-resource panes and exits before reading title or capture content if resource
identity appears.

### When the active target is not a Projmux resource

A pane created outside the registry-backed routes carries no
`@projmux_pane_uid`, so "the active pane is not a Pane resource" is an ordinary
situation rather than a corner case. When that happens the command **refuses**:

```
$ projmux describe pane
resolve pane: no selector was given and the active tmux pane %46 carries no
@projmux_pane_uid; nothing was selected, so pass an explicit resource reference
or --selector
```

It exits `2`, writes nothing to stdout, selects no other resource, and creates
and mutates nothing. The message is deliberately *not* the
`matched N ..., want exactly one` ambiguity error: that wording plus its
candidate listing would read as ordinary ambiguity and hide the real cause. The
variants name what was inspected — a missing `@projmux_window_uid`, a mirrored
uid the registry does not hold, or an active pane that is a shell Pane with no
owning Agent.

### `get pane --current` is a different route

`projmux get pane --current -o cwd` is unrelated to the fallback and is not
deprecated by it. It never reads the resource registry: it prints the live tmux
`#{pane_current_path}` of the focused pane as a bare path scalar, and `cwd` is
the only projection it accepts. `projmux get pane` with no selector resolves a
registry **Pane resource** and renders the shared resource projection. Different
source, different output, different failure surface — both stay.

## reconcile resources

```text
projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]
```

`reconcile resources` is the explicit repair boundary for drift between the
authoritative resource Registry and one exact tmux server. `--dry-run` and the
default execute mode use the same deterministic plan. The plan classifies
missing, stale, foreign, and orphan state; separates Registry changes from tmux
mirror writes; and reports the target plus changed, no-op, and failed counts.
`-o json` emits the same item keys, order, outcomes, remaining drift, completed
stages, and retry command as structured data.

Socket selection is fail closed:

- `--socket <name>` means exactly `tmux -L <name>`.
- `--socket-path <absolute>` means exactly `tmux -S <absolute>`.
- The two flags are mutually exclusive.
- With neither flag, an invocation inside tmux inherits only the absolute
  socket path from `$TMUX` and uses `-S`.
- With neither flag outside tmux, the command is a usage error before the
  Registry, filesystem, or tmux is mutated. There is no default-socket guess or
  fallback to another server.

Dry-run performs only reads: Registry bytes, tmux options/window names, and
other filesystem state stay unchanged. Execute rebuilds the plan from current
state while holding the Registry lock, commits Registry authority first, then
prevalidates every exact live UID target before replaying the planned mirror
writes. A repeat against converged state is a no-op. Foreign or ambiguous live
UIDs are reported and refused rather than merged or replaced; unrelated
resources and sockets are not touched.

If Registry commit or a live mirror write fails, the result identifies the
completed stages, replans the remaining drift, and prints an exact
`projmux reconcile resources ...` retry. Registry commit failure publishes no
planned UID into tmux; a later tmux failure leaves the already durable Registry
identity authoritative and retryable.

`doctor`, `get`, and `describe` remain read-only. `doctor` reports general
runtime/integration health; `reconcile resources` is the opt-in repair command.
It does not run `config apply`, reload unrelated configuration, reconstruct a
missing Registry, heuristically merge identities, or turn a read verb into a
write transaction.

`--materialize-project <name|uid:uid>` is the explicit activation opt-in for
one exact Registry Project. Its separate pure plan treats Registry insertion
order as desired topology and reports a missing persistent session, each
missing Window, and each missing Window-owned `role=shell` Pane. Execute creates
only that missing subset on the selected socket, mirrors the existing Registry
UID/name/owner graph, preserves each Pane's stored CWD, and is a true no-op when
the graph is already live. The option is exact-one: repeating it is a usage
error, and the reported retry preserves both the selector and socket route.

Opening a closed Project is the other activation authority for that same
engine. The Alt-1 sidebar/`switch open` path materializes the selected Project's
declared shell topology on the app's own `-L projmux` socket and moves the
client only after it converges; a refusal, a failed preflight, or a rolled-back
partial leaves the client where it was and reports the exact stage. The
activation is pinned to the session the open targets, so a Project whose
Registry projects a different session name is refused instead of populating a
session the open never reaches. Choosing `Latest snapshot` or `Named snapshot`
instead stays entirely on the Session State snapshot engine. Reading whether a
Project declares topology is a zero-write snapshot read, so opening a directory
that was never registered still creates no Registry state.

Materialization never starts or resumes an Agent, creates an Agent-owned Pane,
or executes `Pane.spec.command`; that field remains a one-time name seed. A
new Window binds only its own tmux-created primary Pane. On an existing Window,
every pre-existing uid-less Pane is refused rather than adopted; foreign,
duplicate, wrong-owner, or otherwise ambiguous UID state is refused before the
first create. Layout uses the existing deterministic right-axis equalizer and does
not promise historic geometry. Canonical Registry deletion removes desired
topology, so a deleted Window or Pane is not recreated; raw runtime loss while
the resource remains is drift and is materialized. A non-implicit offline
Window target (an explicit reference or `--all`) can be canonically deleted
Registry-only: its complete Agent/Pane cascade is shown under `--dry-run`, no
tmux object is killed, and unrelated live objects and sockets are untouched. A
unique live mirror keeps the exact-kill path, while duplicate, foreign,
stale-owner, inventory-failure, and revalidation-race states are refused.

`delete window|pane|agent` names the server its live half addresses the same
way `reconcile resources` does: `--socket <name>`, `--socket-path <absolute>`,
or the inherited absolute `$TMUX`. Outside tmux with neither flag it refuses
rather than reaching for the app's own socket, so a delete issued against an
isolated server can never inventory one host and kill objects on another.

Before it kills anything, a delete commits an intentional termination receipt
against every Pane whose process it is about to end, in its own Registry
transaction. If that write fails, nothing live is touched; if the delete then
refuses for any other reason, the receipt is withdrawn again. The receipt is
what tells a later reader that a process disappeared because someone asked for
it, rather than because it crashed.

## get runtime

```text
projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o json|none]
```

`get runtime` is the read-only escape hatch onto one exact tmux server. The
resource reads (`get projects|windows|panes|agents`) enumerate the Registry;
this one enumerates the machine, including everything projmux does not own, and
it accepts no selector because most of what it reports has no name to resolve.
Its kinds are tmux object kinds, not resource kinds, and they have no singular
spelling for the same reason.

Every row carries the attribution the resolved resource graph decided from exact
evidence -- `managed`, `recoverable`, `control`, `ephemeral`, `unattributed`,
`foreign`, `conflict` -- the reason for it, the stable tmux id, the fully
qualified coordinate (`<session>`, `<session>:@N`, `<session>:@N.%N`), and, for a
managed object, the Registry resource it is bound to. A refused object is named
and explained and is never handed a resource identity.

Socket selection is the same fail-closed rule `reconcile resources` uses, with
one difference at the end:

- `--socket <name>` means exactly `tmux -L <name>`.
- `--socket-path <absolute>` means exactly `tmux -S <absolute>`.
- The two flags are mutually exclusive.
- With neither flag, an invocation inside tmux inherits only the absolute socket
  path from `$TMUX` and uses `-S`.
- With neither flag outside tmux the read still succeeds. It returns the
  unavailable projection: no items, every scope reported unobservable with a
  reason, and zero tmux calls. There is no default-socket guess, and a sibling
  socket is never read.

The default projection is a table preceded by a header line naming the host mode
and the exact transport, plus one line per scope that could not be observed. The
header is always printed, even when the table is empty: "no sessions" is only
trustworthy next to which server was asked and whether the answer could be taken
at all. `-o json` emits the same data as a stable `Runtime{Session,Window,Pane}List`
envelope; `-o none` prints nothing. The Registry projections (`uid`, `name`,
`ref`, `metadata`) are deliberately not offered, because most of what this route
returns has none of them.

The read writes nothing: the Registry is opened without being created, the
observation issues one option probe and three list queries whatever the size of
the server, and no write verb is ever sent.

## runtime diagnostics

```text
projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]
```

The interactive half of the same read. It lists every tmux object on the exact
server in containment order with an attribution tally, and it is deliberately
separate from `projmux runtime sessions`: that picker lists recent sessions so
you can open one, this one lists everything so you can understand it.

Selecting a row opens its action menu, which offers only routes that already
exist:

- **Focus** hands `projmux focus` the row's exact coordinate and the server's own
  `#{socket_path}`. It moves a client and never materializes anything.
- **Attach** forwards to `projmux attach project uid:<uid>`, and is offered only
  for a session bound to a Registry Project while you are outside tmux.
- **Open Resource Inspector** opens `projmux resources` unchanged.

An action that does not apply is listed with the reason instead of being hidden:
"no Registry Project claims this session; diagnostics never adopts one" is the
diagnostic. There is no adopt, import, rename, or kill action, and opening the
surface writes nothing.

## reconcile registry

```text
projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]
```

`reconcile registry` is the recovery boundary for the Registry itself. It is a
sibling of `reconcile resources`, not a stronger version of it: `reconcile
resources` converges a Registry that loads, and this route runs when the
Registry is the thing that is wrong.

Planning writes nothing. With no `--source` — and with `--dry-run` at any time —
the command reads the current `registry.json`, the `registry.initialized`
marker, and the bounded copies under `recovery/`, then reports:

- the current state as `valid`, `first-use`, `missing`, `empty`, `malformed`,
  `schema-too-new`, `invalid`, or `unreadable`, with a `sha256:` digest of the
  exact bytes;
- every candidate newest first, each marked `eligible` or `rejected` with the
  reason, its digest, size, mtime, schema version, and the
  projects/windows/panes/agents/reservations it holds;
- the exact guarded command that would restore the candidate it suggests.

No lock is taken, no permission is repaired, and `<state>/projmux/metadata/` is
not created — a preview is safe against a first-use state directory and against
one nobody should be writing to yet.

Restoring requires `--source`. There is no "restore the newest" mode: which copy
is the truth is a judgment about which mutations were wanted. A source is an
exact copy name, a unique fragment of one, or an absolute path to a copy carried
from elsewhere; a fragment matching several copies is refused rather than ranked.
An explicit path gets exactly the same verification as a bounded copy.

Verification is fail closed. Malformed JSON, an empty file, an envelope newer
than this build, and a graph with a duplicate uid, a dangling `ownerRef`, or a
broken name reservation are all refused with the current Registry byte-identical.
The verified bytes are then published **verbatim**, so uids, owner relations, and
name reservations are preserved exactly rather than re-encoded, and a
known-older-but-valid envelope stays readable through the normal safe read and
migrates on the next semantic write.

The bytes being replaced are kept first, at
`recovery/replaced-<stamp>-<seq>.json`. Unlike the write-side copies this keeps
content that does not verify: a damaged Registry is the only remaining evidence
if the restore turns out to be the wrong call, and the preserved copy is offered
back as a candidate. Replaced copies are their own bounded family, so a restore
never consumes the automatic write history.

Race guards are the operator's tie to the plan they read:

- `--expect-source-checksum <sha256:hex>` refuses unless the source still hashes
  to that digest.
- `--expect-current-checksum <sha256:hex>` refuses unless the current Registry
  still does.
- Both are what the printed `next:` command already carries, so copy-pasting the
  preview's suggestion is guarded by construction.

Underneath, the source is re-read and re-verified under the store lock, the
staged copy is re-validated, and both inputs are re-hashed immediately before the
single atomic rename. Anything that moved refuses with nothing published, no
preserved copy, and no staged file left behind, and says to re-run the preview. A
repeat restore is a byte no-op: no rename, no preserved copy, no marker write.
Restoring into a state directory with no marker publishes one, so a later loss on
that machine reads as state loss rather than as a fresh first use.

When recovery is needed and no verified copy exists, the report adds a mirror
diagnostic — and it is **only** a diagnostic. It reports the Projmux identity the
one exact tmux server still carries (Project/Window/Pane uids, mirrored names,
the Project root, and containment resolved from stable tmux ids) beside a fixed
statement of what no mirror can return: offline resources, every Agent (no tmux
option carries an Agent uid), an Agent-owned Pane's `ownerRef`, the name
reservation table, `spec.primaryPaneRef`, and labels/annotations/timestamps/
status. Panes carrying a provider option are counted as proof that Agents existed
whose uids are nowhere on the server. Nothing is imported and no Registry is
generated from fragments. Socket selection follows the same `--socket` /
`--socket-path` / inherited-`$TMUX` rule as `reconcile resources`, except that
having no exact target is reported as a reason rather than being a usage error:
a restore is a filesystem operation, so recovery must work on a machine with no
tmux server. The diagnostic is skipped entirely when a verified copy exists or
the Registry is healthy.

## Internal plumbing (`projmux internal ...`)

`internal` is a hidden namespace for the routes generated tmux config, tmux
hooks, popup payloads, and provider hooks invoke. Nothing under it is meant to
be typed at a prompt, so it is absent from `projmux help` and from the
generated [CLI Reference](cli.md). It is listed here because operators still
have to recognize these spellings when they show up in a generated config, a
hook payload, or a `make install` log.

| Route | Purpose |
| --- | --- |
| `internal tmux` | Generated config render/install/apply, popup entry helpers, pane rebalance/rename, snapshot autosave. |
| `internal status` | Status bar segment renderers (`git`, `project`, `usage`, `notify`, `resources`). |
| `internal statusbar` | Status bar click and shortcut dispatch (`click`, `usage-refresh`). |
| `internal preview` | Persisted preview cursor (`cycle-pane`, `cycle-window`, `select`). |
| `internal session-popup` | Session popup preview/open and popup cursor movement. |
| `internal agent-hook` | Provider hook ingest (`ingest`) and the legacy non-resource pane title watcher (`watch-title`). |
| `internal focus` | Machine focus ingress used by statusbar and notification-sidebar actions. |
| `internal key-broker` | Darwin physical key transport. |
| `internal popup-wait-key` | Single-key reader that closes a display-only popup. |

The old pre-namespace top-level aliases have been removed. Invoking `status`,
`statusbar`, `preview`, `session-popup`, `tmux`, `key-broker`, or
`popup-wait-key` as the first token is an unknown command (exit 1). Generated
payloads use the `internal` routes above.

The tmux plumbing has public equivalents where an operator needs them:

| internal spelling | public spelling |
| --- | --- |
| `projmux internal tmux print-config` | `projmux config render standalone` |
| `projmux internal tmux print-app-config` | `projmux config render app` |
| `projmux internal tmux apply` | `projmux config apply` |
| `projmux internal tmux install` | *(none — installer plumbing)* |
| `projmux internal tmux install-app` | *(none — installer plumbing)* |

The public spellings forward to the same internal handlers. `make install`
calls `projmux config apply`.

The complete compatibility-to-canonical removal record, including intentional
error differences, is maintained in the [Legacy CLI Retirement Ledger](legacy-cli-retirement.md).

`install` and `install-app` deliberately have no public spelling. They write and
wire up config files as part of the install pipeline rather than answering an
operator's question, so they stay reachable only as `projmux internal tmux ...`.

## switch

```
projmux switch [--ui=popup|sidebar]
projmux switch open <path>
projmux switch toggle-tag | toggle-pin | kill | settings | preview
projmux switch cycle-pane | cycle-window | sidebar-focus
```

Project picker. With no positional argument, opens the configured picker popup
or sidebar (depending on entry helper). `switch open <path>` jumps directly. The
sub-verbs are entry hooks invoked by tmux keybindings (e.g.
`sidebar-focus` is wired to the sidebar's focus binding so navigation keeps
the active session in sync).

Settings > Labs remains available for experimental settings, but picker
selection/source rows have been retired. The native picker is always used, and
there is no picker selection configuration or migration behavior.

## setup

```
projmux setup [--timeout DURATION] [--non-interactive]
```

Probes the guaranteed launch defaults (`Alt-1..5`) plus transport candidates
such as `Ctrl-N`, `Ctrl-Shift-{R,L,M}`, `Ctrl-M`, and
`Alt-Shift-{Left,Right}`. Reports whether each key arrived as the expected
plain bytes, an unsupported legacy/app-specific sequence, an unknown sequence,
or a timeout. `--non-interactive` skips the TTY probe and prints the expected
key map. Default `--timeout` is `5s`. Run it outside tmux after trying
`projmux shell`; `Alt-1..5` are the only guaranteed zero-config defaults.
Settings > Keybindings edits in-app action aliases; terminal delivery
diagnostics stay in this CLI flow.

## setup terminal

```
projmux setup terminal [terminal] [--apply] [--config <path>]
                              [--allow-symlink]
```

Previews or applies terminal-specific key delivery mappings for supported
terminals when `projmux setup` reports swallowed shortcuts. When `terminal` is
omitted, autodetects from `$TERM_PROGRAM`/`$TERMINAL_EMULATOR`.
Known terminals: `ghostty`, `windows-terminal`. Default is a read-only
preview; pass `--apply` to write (timestamped `.bak.<timestamp>` is created). Refuses to
write through a symlink unless `--allow-symlink` is passed (dotfiles repos).
`--config <path>` overrides the candidate list when the adapter has more
than one default location (Ghostty `config` vs `config.ghostty`). If setup
shows every key arriving, skip terminal remediation.

## doctor

```
projmux doctor [--json] [--section deps|runtime|integrations|session-state|logs] [--verbose]
```

Runs read-only diagnostics, including a dependency check for `tmux ≥ 3.4`,
`git`, and `stty` (POSIX only), then reports read-only AI notify integration diagnostics
for Codex hooks, Claude Code hooks, Antigravity hooks/statusline, and the tmux bell
fallback. AI notify integration statuses are `installed`, `missing`, or
`conflict`; missing or conflicting integrations are informational and do not
make doctor fail. It also reports read-only Session State resume metadata
diagnostics for saved agent panes, including `available`, `stale`, or
`unavailable` status plus confidence, source, updated-at, and the affected
snapshot/window/pane.

Session State preview and doctor report the resume source captured on the pane.
Live `hook`/`session-id` metadata is high confidence; DB-validated Antigravity
`antigravity-last-conversation` and `antigravity-conversation-metadata` picker
sources are medium confidence; legacy `antigravity-history` is low confidence.
Disk discovery never lowers or overwrites an already captured live source.

The default text report shows per-section summaries plus failing or warning
items. `--verbose` adds successful checks and complete typed detail, including
versions, paths, confidence/source metadata, and displayed remediation.
`--section` projects the same inventory used by text and JSON: `deps` selects
dependencies, `integrations` selects AI notify integrations, and
`session-state` selects resume metadata plus retention guidance. `runtime`
selects the fixed `tmux` backend, an actual one-second read-only probe of the
app socket, and generated-versus-live config digest state. `logs` selects the
state/log/journal presence, private permissions and metadata-only writability
checks plus a bounded aggregate of recent safe operational error codes. These
sections expose only closed status codes and counts: no path, socket name,
routing identity, message, config content, or argv is rendered.
The recent-error count is the size of the newest 20-record window, not a
lifetime total; `logs.recent-errors.bounded` means older errors were omitted.
The probe captures at most 4 KiB, generated config inspection reads at most
1 MiB, and the journal seam reads at most 5 MiB.
Symlinks and non-regular inputs are rejected without following or blocking on
them. On Windows, POSIX mode bits cannot establish ACL privacy, so otherwise
valid paths report the closed `privacy-unverified` warning rather than a false
private/insecure classification, followed by a separate metadata-only `ready`
or `not-writable` finding; Doctor never changes ACLs.

JSON reports have integer `schema_version: 2`. An unfiltered report retains the
existing typed `dependencies`, `ai_notify_integrations`,
`session_state_resume`, and `session_state_prune` detail and adds ordered
`runtime` and `logs` finding arrays. Every finding has closed `severity`,
stable `code`, and closed `remediation`; bounded aggregates may add `count`
and `safe_codes`. A filtered report contains only the selected typed field(s).
`--verbose` is accepted with `--json` but does not change JSON fields or values.

Default and verbose text exit non-zero only when their projected dependency
inventory contains a required missing or stale dependency. A non-`deps`
section exits `0`. JSON preserves its successful exit after emitting a report,
even when required dependencies are missing or stale.

Doctor is read-only for every flag combination. It never creates or repairs
state/log paths, installs packages, runs displayed remediation, changes
terminal or tmux state, generates/applies config, migrates files, or writes
operational-log outcomes. Missing, malformed, or permission-denied journals
degrade to typed log findings without failing the command. The removed `--install-missing`,
`--include-optional`, and Doctor `--dry-run` mutation flags fail as unknown
usage with an exact instruction to remove the flag and run displayed
remediation explicitly outside Doctor; they are never ignored. Doctor does not
diagnose terminal key delivery; use `projmux setup` for that.

JSON migration: consumers must switch on `schema_version` before decoding.
Version 2 changes the previously empty/reserved `runtime` and `logs` arrays to
the typed finding shape above; field meanings inside the version 1 dependency,
integration, and Session State inventories are unchanged. Consumers that only
understand version 1 must reject version 2 rather than decoding the new arrays
as the old empty placeholder shape.

`Settings > Notifications > Delivery sources` shows active Codex, Claude, and
Antigravity hooks plus tmux statuses, conflicts, config paths, and
copyable AI integration commands where available. Its summary/detail also shows
whether `PROJMUX_NOTIFY_HOOK` overrides the built-in desktop sender. Settings
does not install or remove external Codex, Claude, Antigravity, or tmux notify
wiring. Pending in-app queue rows remain owned by the statusbar/sidebar rather
than a standalone Settings row.

## diagnostics

```
projmux diagnostics log [--tail N] [--json]
                        [--level info|error] [--component NAME] [--path]
projmux diagnostics report [--output <path>]
```

Reads the local operational event journal through the same tolerant JSONL
reader used by every output mode. The default text view shows the newest 50
valid records. `--tail N` changes that bound, `--json` emits the selected
records as JSONL, and `--level` / `--component` filter before tailing. `--path`
prints the resolved path without creating or reading the log.

Successful state-changing top-level commands produce one `info` outcome, and
every top-level command error produces one `error` outcome. Successful
high-frequency/read-only commands such as `internal status`, `internal
agent-hook ingest`, `attention arm`/`clear`/`window`, `internal tmux
autosave-session-state`, `window record`, and
successful `diagnostics log` views do not produce an event. Errors from those
automatic hook/poll paths still produce one safe `error` outcome. Successful
direct command help and explicit `--dry-run` preview modes also remain
read-only and do not produce an event. Journal failures are a best-effort side
channel and never change command output or exit status. See
[operational-diagnostics.md](operational-diagnostics.md) for the file,
retention, concurrency, and privacy contracts.

`internal agent-hook watch-title` emits a bounded common lifecycle: one start, one terminal
pane-gone/hook-active stop, and at most one copy of each closed watcher failure
tuple per process. Normal polling iterations emit nothing. AI hook ingest emits
common events only for malformed/read/oversized payloads, unmatched or invalid
targets, unsupported event classification, and route failures. Provider event
names, payloads, prompt/tool/transcript values, notification text, pane
metadata, paths, UUIDs, and conversation/session IDs are never stored. Normal
state/notify/quiet/dedupe hook results stay zero-volume in the common AI family;
the existing notify transition remains the owner when notification behavior
occurs.

Session create/attach/switch/kill and `config apply` use correlated
`lifecycle.start`/`lifecycle.outcome` records instead of a duplicate generic
top-level outcome. The text and JSONL views expose only the closed safe
`operation` and optional `code` enums; session names, socket paths, tmux
targets, subprocess argv, and generated configuration are never recorded.

`diagnostics report` is the explicit consent boundary for creating one local
private `tar.gz` support archive. The invocation first prints a redacted
destination label, the complete included/omitted entry list, stable omission reasons, report
schema, and redaction mode; the first parent/temp/archive write happens only
after that preview is successfully written. `--output` selects the local
destination. Without it, the command uses a timestamped archive in the current
directory. Existing destinations are never replaced.

The archive contains `manifest.json`, safe projmux version/platform/backend
metadata, a redacted projection of Doctor JSON schema version 2,
config presence states (never values), up to 50 recent errors from the existing
bounded operations reader, and count-only AI ingest diagnostics. Paths,
session/window/pane/thread/routing identifiers, run IDs, tool/version output,
commands, guidance, reasons, and other free text are field-scoped hashes unless they
match a closed diagnostic enum/static-name allowlist. Raw config/environment
values, argv/stdin, prompts, notification text, pane output, transcripts, and
hook payloads are never collected. Missing, corrupt, or unreadable sources are
recorded as stable manifest omissions. Report collection does not repair source
permissions, migrate hooks, append an operational outcome, contact a network,
upload, create an issue, or run in the background; only the explicitly selected
output parent/temp/archive can be written.

The redacted Doctor projection keeps `schema_version`, closed runtime/log
finding enums, and structural/count
numbers as numbers. Numeric routing fields such as `window_index` and
`pane_index` become field-scoped hash strings under `default-hash-v1`; consumers
must treat this support projection as redacted evidence rather than decoding it
back into the unredacted Doctor Go types.

## focus

```
projmux focus project <ref> [--socket <path>] [--client <tty>] [--json]
projmux focus window <ref> -p <project-ref> [--socket <path>] [--client <tty>] [--json]
projmux focus pane <ref> -p <project-ref> -w <window-ref> [--socket <path>] [--json]
```

Moves one attached client to an exact live Project, Window, or Pane. It never
materializes an offline resource or force-detaches another client. `--socket`
is explicit; when omitted, it is derived from `$TMUX`.

Generated statusbar and notification-sidebar payloads use the hidden
`projmux internal focus` spelling. The old public `focus --target|--uri`
machine argv has been removed.

`projmux focus` stops at the tmux layer. It never asks the host terminal
window to come forward, in any Desktop notification mode.

`--client` is a preferred origin tmux client. In-app consumers such as the
status bar and notify sidebar pass the clicked client so focus redirects that
display first. If that client is gone, focus falls back to an attached client
already viewing the target session, then the stable first attached client.
Toast clicks do not pass `--client`.

The removed public `--uri`/`--target` ingress is not accepted here. Generated
machine payloads use `projmux internal focus`; human callers select an exact
Project, Window, or Pane with the public kind routes above.

Exit codes:

- `0` — focused (or the notify-only fallback fired).
- `2` — target session/window/pane could not be resolved
  (`focusExitNotResolved`).

`--source`/`--kind` are telemetry labels logged when
`PROJMUX_FOCUS_DEBUG` is set. `--json` prints a single-line JSON payload
with `{ok, fallback, target, socket, resolved_session, client, dispatch,
session_state, window_state, pane_state, reason, note}`. Callers can
distinguish unresolved sessions (`reason=session-unresolved`, exit 2),
session rename/prefix fallback (`session_state=fallback`), window index
fallback (`window_state=index-fallback-session`), pane index fallback
(`pane_state=index-fallback-window`), and explicit id failures
(`window-id-unresolved` / `pane-id-unresolved`, exit 2).

## notification queue

Pending AI notification queue. `attention` is live tmux pane state; the queue is
the explicit-ack pending notification source of truth used by the status-bar
notify segment and notify sidebar. It is not the source of truth for all live pane attention. See
[notify-queue.md](notify-queue.md) for the full data model.

Use only the canonical queue spellings:

```
projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> ...
projmux get notifications [--live] [--json] [--limit N] ...
projmux notification ack <id> | --all
projmux notification reconcile [--json]
```

- `create notification` — append (or refresh, with `--id`) one entry. `--ttl` defaults to
  `600` seconds as freshness metadata; expiration does not remove rows from
  `get notifications` and is considered only by reconcile together with a gone
  target. Reconcile also retains only the newest 256 rows. `--text` is hard-capped to 80 runes (longer text is
  truncated server-side). After a successful queue write, projmux sends a
  best-effort refresh event to open native notify sidebars and fires
  declarative `[hooks.send-noti]` asynchronously if configured. Event delivery
  failure does not fail the queue write; reopening the sidebar still shows the
  latest queue. The hook gets a JSON payload on stdin plus
  `PROJMUX_NOTIFY_*` env vars, and it does not replace the normal desktop
  notification path.
- `get notifications` — newest-first pending queue table `ID AGE SEV SRC TARGET TEXT`
  (or JSON). `--severity` and `--source` are repeatable filters.
  `--live` adds a non-mutating explanation table (or JSON report) that
  compares queued entries with live pane attention. It calls out manual
  reply badges that do not queue because no AI agent is attached, live AI
  reply panes missing a queue entry, matched AI reply entries, inactive
  (`queue-stale`) queue entries whose live pane EXISTS but no longer matches
  reply+agent state, and gone (`queue-gone`) entries whose pane is absent from
  the real tmux live pane inventory (or which have no routable target).
  `--ui=sidebar` opens the compact interactive notify list where Enter
  focuses and acks live or inactive-routable targets, cleans gone/unroutable
  targets without focusing, `a` acks the selected row, `x` clears non-critical
  rows, and `Ctrl-X` clears all; opening or navigating the sidebar does not ack.
  While open, the native
  sidebar refreshes its row list on successful queue-write events without an
  Alt-2 close/reopen toggle, using the same deferred refresh path as `a` and
  `x`. The sidebar is a pane/session-grouped inbox whose collapsed rows are
  fixed three-line cards: project/session, agent/provider, and newest age on
  line 1; topic/pane-title/task context plus severity/live-state metadata on
  line 2; and the latest notification preview on line 3. Hidden queue ids
  remain action values, but the sidebar has no search input. `--client` is used
  by tmux popup launchers to keep row-select focus on the clicked client.
- `ack <id>` removes one entry; `--all` flushes the queue.
- `reconcile` — walks `tmux list-panes -a` and back-fills entries for
  panes whose attention state is `reply` AND whose AI agent option is
  set, reporting stale `ai:` entries that no longer match a live pane without
  acking them.
  Soft-fails (no error, populated `errors` field in the summary) when
  tmux is not running. Use this as the recovery path when the queue and
  live pane state drift.

## agent usage

Authoritative AI account usage. See [usage-tracking.md](usage-tracking.md)
for adapter detail.

```
projmux agent usage [--model codex|claude|antigravity|all] [--window 5h|weekly|context|quota|all]
                    [--json] [--force|-f]
```

Renders a tab-aligned `MODEL WINDOW PCT RESETS_AT RESET_IN STALE` table; appends a
backoff note when an adapter is in 429 cooldown. `--force` clears any
active backoff and bypasses the per-adapter throttle floor (Claude `5m`,
Codex shares the global `30s`). `--json` emits the snapshot array; when
backoff is active the wrapper `{snapshots, backoff}` object is emitted
instead.

Claude keeps the canonical aggregate `5h` and `weekly` rows and projects each
valid typed upstream `limits[]` entry as a named `quota/<exact group>` row.
Model-scoped rows append the exact upstream model display identity in text
output; terminal controls are escaped and the visible label is bounded without
normalizing the stored identity. JSON preserves the typed named-quota metadata,
including nullable scope/model ID/surface, reset, `updated_at`, and `stale`.
These percent-only rows never synthesize token counts. A malformed limits block
fails that adapter refresh so the prior complete Claude slice remains visible;
a valid aggregate-only response replaces and removes obsolete named rows.

Antigravity emits official account rows labelled
`quota/<upstream bucket ID>`. Conversation-local context remains private
hook/notify diagnostic metadata and legacy cached context rows are suppressed
from text and JSON output. `--window quota` selects named account rows; opaque
IDs such as `weekly` are not aliases for the fixed `weekly` window. Used
percent is `100 * (1 - remaining_fraction)`. `--window context` remains
accepted for compatibility and returns no Usage rows.
`reset_time` and optional `reset_in_seconds` are preserved independently,
including the distinction between absent and explicit zero. Invalid,
disabled, missing, or empty quota data degrades without reinterpreting the
private conversation context diagnostic.

## resources

```
projmux resources
```

Opens the native, read-only Resource Inspector. It samples only while this
interactive process is alive, paints `warming` immediately, then refreshes at
a non-overlapping two-second cadence. Right or Enter drills Project → Window →
Pane → Pane detail; Left returns (and is a no-op at the root), while Esc closes
the popup at every depth. Search matches the current scope's display name and
stable tmux id. Tab cycles CPU, Memory, and Name sorting; Ctrl-R requests an
immediate refresh. A plain `r` remains search input.

CPU list values are host-capacity share; pane detail also shows
core-equivalent CPU. Project and window rows count panes, while pane rows count
the attributed processes. Pane rows and detail share the resolved pane identity
and label the tmux current command, PID/SID, pane id, and TTY separately. Memory
is explicitly an RSS sum (shared pages can be counted more than once) plus its
host ratio. Concrete projects and `No project match` are the only project
drill-down groups; non-drillable `Other / unattributed` remains explicit.
Defensive ambiguous attribution stays in its stable internal bucket, is
included in Attributed totals, and appears only as a bounded CPU/RSS/pane
diagnostic rather than a project row. Warming, partial, unavailable, unknown,
and overage states also remain explicit. No process
command list, mutation, history, graph, daemon, persistence, or Session State
telemetry is created. Linux/tmux provides attribution; unsupported platforms
show an unavailable reason rather than zero metrics.

Only Resource Inspector anomalies (`unavailable`, `partial`, `stale`, closed
collection-stage errors, and scan-budget exhaustion) enter the private bounded
operations journal. Identical persistent anomalies are transition-coalesced;
healthy periodic samples and refreshes emit zero records. These records contain
no CPU/RSS values, PID/process detail, project/tmux identity, path/title/command,
or arbitrary error/status text. `diagnostics report` includes only error-level
closed resource outcomes; local-only partial/stale info rows are omitted.

This is distinct from `projmux internal status resources`, which remains the short
host-only statusbar renderer.

## internal status

Per-segment status-bar renderers. All are silent on failure — the
tmux status interval polls them and must never produce a stack trace.

```
projmux internal status git    [path]
projmux internal status usage  [--max-width N] [--force|-f]
projmux internal status notify [--max-width N]
projmux internal status resources
```

- `git` — `#[bold,fg=colour16,bg=colour45] <branch> <state> #[default]` for
  the pane's `pane_current_path` (or the supplied path). Empty when not in a
  repo. `<state>` is omitted when clean, otherwise it may include `*` for
  local changes, `+N` staged entries, and `↑N`/`↓N` ahead/behind counts, with
  compact per-token colors in tmux output.
- `usage` — HUD-style provider blocks containing only official `5h` and
  `weekly` windows. Antigravity's exact `quota/gemini-weekly` snapshot is
  projected as `weekly` without changing its cached identity; other named
  quotas and context never consume status width. Claude typed `limits[]`
  named/model rows are likewise excluded, so only its aggregate `5h` and
  `weekly` rows reach the status line. Narrow tiers keep one primary window per
  provider (`5h`, otherwise `weekly`) before hard truncation.
  Triggers an opportunistic, throttled refresh (per-adapter
  throttle, `30s` floor) so a stale cache self-heals.
- `notify` — newest-first HUD block with project, state, optional agent, text,
  age, and `+<extras>`. Window/pane ids remain routable metadata but are not
  displayed in the compact HUD. Degrades through width tiers; default
  `--max-width` is `200` runes.
- `resources` — macOS/Linux/WSL aggregate `CPU N%  MEM N%`. Linux uses
  `/proc/stat` and `/proc/meminfo`; macOS uses native Mach host statistics.
  CPU needs two invocations to establish a delta and renders `--` for the first
  sample. CPU independently warns at 70–89% and becomes bold critical at 90% or
  above; memory independently warns at 75–89% and becomes bold critical at 90%
  or above. Normal and unavailable values use the secondary status-text role.
  These values are host-scoped rather than pane/window/project/session
  attribution. WSL reports the Linux guest/VM view. Unsupported platforms and
  unreadable system metrics produce no error output.

## internal statusbar

```
projmux internal statusbar click <range-id> [--socket <s>] [--mouse-window <id>]
                                            [--client <tty>] [--mouse-x N] [--mouse-y N]
projmux internal statusbar usage-refresh
```

Click/keyboard dispatcher for the two-line status bar. Implemented range ids:
`session pwd git resources usage notify settings`. The bare `window` /
`window|<idx>` token (tmux's built-in window-list range) and the empty
range fall through to `select-window -t @<mouse_window>` so the native
click-to-switch tab affordance is preserved on row 1. Unknown range ids are
non-specialized placeholders and no-op. `session` opens the existing-session
popup; `pwd` shows the current pane path in a native-framed display-only
popup; `git` opens the project switcher popup;
`settings` toggles the settings popup for the tmux client; `usage` opens the
detailed cached account-usage popup. Legacy context rows are suppressed and
named quotas retain exact identity/reset/freshness values. Claude model-scoped
rows distinguish the exact group and model display identity with bounded,
terminal-safe labels; JSON retains their full typed metadata. `USED`, `LIMIT`,
and `LEFT` appear together only when at least one displayed row has real
absolute counts; percent-only datasets omit those columns rather than
synthesizing counts.
`notify` focuses and acks the newest actionable queue target. The internal `usage-refresh` shortcut entry point
runs the same throttled, per-adapter collection policy as `internal status usage` and
then reopens the display-only usage popup from cache.
`MouseDown1Status` errors are
swallowed and surfaced as `display-message` toasts so a transient
failure does not raise a tmux error popup. See [statusbar.md](statusbar.md).

## attention

```
projmux attention toggle [pane]
projmux attention clear  [pane]
projmux attention arm    [pane]
projmux attention list   [--json] [--all]
projmux attention window [window]
```

Toggles the `✳` pane title prefix and the `@projmux_attention_state` pane
option. `toggle` flips between cleared and `reply`; `clear` always
clears; `arm` sets a pre-reply armed state used by the AI flow. The
producer side pushes the matching entry into the notify queue when the pane
has an associated AI agent option; clearing attention does not ack the queue
row (manual toggles on shell panes do not push). `list` reads `tmux list-panes -a` and shows live pane
attention state without reading or mutating the notify queue; by default
it shows panes with an attention option or title marker, and `--all`
includes every pane. `window` renders the status-bar window badge for the
supplied window.

## Agent creation and hook ingress

```
projmux create agent --provider <claude|codex|antigravity> [--project <ref>] [--window <ref>]... [--create-window] [--placement right|down] ...
projmux create pane [--project <ref>] [--window <ref>]... [--create-window] [--placement right|down] ...
projmux config edit [--get|--set <mode>]
projmux agent status set <thinking|waiting|idle> [pane]
projmux agent topic ...
projmux internal agent-hook watch-title [pane]
projmux internal agent-hook ingest codex-hook < payload.json
projmux internal agent-hook ingest claude-hook < payload.json
projmux internal agent-hook ingest antigravity-hook [--event <PreInvocation|PostInvocation|PostToolUse|Stop|Statusline>] < payload.json
projmux internal agent-hook ingest bell --pane <pane_id>
projmux diagnostics agent-hook [--tail N] [--json] [--path]
projmux agent integrate codex [--dry-run] [--remove]
projmux agent integrate claude [--dry-run] [--remove]
projmux agent integrate antigravity [--dry-run] [--remove]
projmux agent integrate tmux-bell [--dry-run] [--remove]
```

These routes manage the Agent lifecycle and the per-pane state machine that drives
the `attention` badge, the `notify` queue producer, and the desktop
notifier. `status set waiting` is the trigger that flips a pane to the
reply-ready state — that transition pushes an `ai:<session>:<pane>`
entry into the notify queue.
Use `projmux diagnostics agent-hook` for the bounded ingest journal.
The durable semantic status badge is stored separately in
`@projmux_ai_badge_kind` as `approval_required`, `input_required`,
`response_complete`, `in_progress`, or unset. The live status surfaces color
those values with action-required amber-orange, success green, and progress
yellow respectively. That palette is independent from notify queue
`--severity` and from OS desktop notification urgency; a critical approval row
can still render a non-red action-required status badge.

`create agent --provider ...` selects a provider without changing the saved
default. Concrete provider invocations create a new Agent and a new managed
Pane every time; existing managed AI panes in the same project/session are
not selected or reused, and rebinding an existing conversation is `agent
resume`, a different verb. The scope of the new resources follows
[Create scope](#create-scope). The provider picker remains available through
`internal agent-pane picker`. Arguments after `--` are extra arguments appended to
the resolved `claude`, `codex`, or `agy` executable inside the managed wrapper;
projmux still sets the context directory, tmux title, AI pane metadata, and
split layout.

Automation callers get the new pane's handle from `-o pane-id` on the canonical
create routes: `projmux create agent --provider <p> --placement right -o pane-id`
and `projmux create pane --placement right -o pane-id` each print exactly the
managed Pane's `%N` followed by one newline. See
[AI Agent Shortcuts](ai-agent-shortcuts.md) for the shortcut spellings.

Every Projmux split surface produces the same canonical create intent. The
default `ai-split-right/down` binding reads the saved split mode and turns it
into one intent -- a provider Agent, a shell Pane, or one of the two pickers --
and the `Alt-7` picker and the resume picker do the same with what the operator
selected. Only the create route's materializer runs tmux's `split-window`, so a
pane opened from the UI is a Registry resource on the same terms as one asked for
by name, and a failed launch leaves zero Registry and zero tmux mutations. A raw
unmanaged split exists only where you make one yourself.

The resume picker lists the newest deduplicated Claude, Codex, and Antigravity
resume sessions for the current project, with `[+ New Session]` pinned first.
If there are no resume sessions it goes straight to the existing selective
picker. Selecting a row creates a managed Agent whose pane joins that
conversation -- `claude --resume <id>`, `codex resume <id>`, or
`agy --conversation <uuid>`. Rebinding an Agent the Registry already has is
`projmux agent resume`, a different verb that never falls back to a fresh
conversation.

Live Antigravity hook/session-state resume metadata remains a separate,
high-confidence lane; it is not enumerated from disk by the picker. Within the
picker's disk discovery, source order is the workspace-to-latest-UUID mapping
in `cache/last_conversations.json`, workspace-bearing summarized rows in
`cache/conversation_metadata.json`, then legacy `history.jsonl`. The two cache
sources require a normalized UUID and an exact regular
`conversations/<uuid>.db`; only DB existence and mtime are read. SQLite content,
prompt/transcript text, sidecars, symlinks, and arbitrary paths are never used.
The cache is only the history floor exposed by upstream v1.1.12, not a complete
conversation history. Cache rows have medium confidence and blank turns;
`last_conversations` uses a short UUID title, while metadata uses only a safe
summary. Legacy history is low confidence. Missing/malformed cache, stale
missing-DB mappings, workspace-less metadata, and unknown fields degrade
without failing Claude/Codex or legacy discovery.
Settings > AI Settings > Enabled agents controls Claude/Codex/Antigravity launch
visibility. Disabled agents are hidden from the selective picker and from the
default-mode picker. A saved default that later becomes disabled fails clearly
instead of falling back to another agent. Direct
Canonical `create agent --provider <p>` launches and the provider shortcuts also
fail when disabled. If all AI agents are disabled, the selective picker still offers the
plain `shell` split and shows guidance to re-enable Claude/Codex/Antigravity.
For user-level skill, slash-command, editor, or launcher registrations that
call this contract, see [AI Agent Shortcuts](ai-agent-shortcuts.md).

`ingest codex-hook` is the hook-facing entrypoint for Codex hooks-engine JSON.
It reads one JSON payload from stdin and handles the default Codex hook catalog
`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreCompact`, `PostCompact`,
`SessionStart`, `UserPromptSubmit`, and `Stop` as exposed by Codex CLI 0.130.0.
It accepts the common Codex hook fields `hook_event_name`/`event_name`,
`thread_id`, `session_id`, `turn_id`, `cwd`, `transcript_path`, `model`,
`tool_name`, nested `tool.name`, `tool_input`, and `input`. `UserPromptSubmit`
marks the matched pane hook-active and moves it to thinking/busy without
pushing a queue entry. `Stop` pushes an info completion row. `PermissionRequest`
pushes a critical approval row with the tool name and concise action summary.
The other Codex events are quiet: they mark the pane hook-active and write
ingest diagnostics, but do not push notify queue entries.
For event names without a specialized notify/state handler, ingest falls back
to quiet/log-only handling. A local catalog entry with `"action": "quiet"`
therefore lets newly discovered events be installed and observed without
creating notification noise; `"notify"` and `"state"` still require a built-in
handler before they can change pane state or push queue rows.
Runtime action overrides from
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json` take
precedence over catalog `action` during ingest, including known events such as
`Stop` and `PermissionRequest`. These overrides are managed by
`Settings > Notifications > Agent event behavior` and do not change the catalog
`install` field used by `projmux agent integrate codex`. A runtime `notify`
override for a known Codex event without a specialized handler, such as
`PreToolUse` or `PostToolUse`, pushes a short generic in-app row like
`PreToolUse · Bash` with agent/category metadata. Generic rows are
queue/sidebar/statusbar only and
do not dispatch OS desktop notifications, `PROJMUX_NOTIFY_HOOK`, or
`[hooks.send-noti]`.

`ingest claude-hook` is the hook-facing entrypoint for Claude Code hooks. It
reads one JSON payload from stdin and handles the default Claude Code 2.1.140
hook catalog: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`,
`PostToolBatch`, `PermissionDenied`, `Notification`, `UserPromptSubmit`,
`UserPromptExpansion`, `SessionStart`, `Stop`, `StopFailure`,
`SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`, `SessionEnd`,
`PermissionRequest`, `Setup`, `TeammateIdle`, `TaskCreated`,
`TaskCompleted`, `Elicitation`, `ElicitationResult`, `ConfigChange`,
`InstructionsLoaded`, `WorktreeCreate`, `WorktreeRemove`, `CwdChanged`, and
`FileChanged`. It uses the same pane matching order as Codex ingest
(`$TMUX_PANE`, payload `cwd`, then cached session id), marks matched panes
hook-active, and writes metadata-bearing notify queue entries only for
reply-ready, input-ready, approval-required, error, and teammate-idle events.
`SubagentStop` and the other lifecycle/tool events are quiet: they mark the
pane hook-active and write ingest diagnostics, but do not push notify queue
entries. `UserPromptSubmit` only moves the pane to thinking/busy and does not
push a queue entry.
Unknown Claude events also fall back to quiet/log-only handling after pane
matching. Catalog `action` is honored for quiet fallback events; notify/state
actions need built-in handlers for event-specific body text and state changes.
Runtime action overrides from
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json` take
precedence over catalog `action` for known Claude events too; for example a
noisy notify event can be made state-only or quiet without changing installed
Claude hook commands.

`ingest antigravity-hook` is the hook/statusline entrypoint for
Antigravity CLI `agy` payloads. Official v1.1.12 hook commands must pass their
event identity explicitly, for example
`projmux internal agent-hook ingest antigravity-hook --event Stop`; the official stdin payload
does not carry an event field. The explicit selector is authoritative, while
payload `eventName` and its legacy aliases remain fallback inputs for existing
manual wiring. `projmux agent integrate antigravity` manages exactly the named
`projmux` entry in `~/.gemini/config/hooks.json` and, separately, exactly the
`statusLine` member in `~/.gemini/antigravity-cli/settings.json`. The managed
statusline uses the official v1.1.12 `{type:"command", enabled:true,
stack_with_default:true}` shape and an absolute direct ingest command whose
stdout is empty, so the built-in statusline remains visible. It preserves every
other named entry and unknown JSON value, resolves the running projmux
executable to a stable absolute path, and supports `--dry-run` and `--remove`.
An existing unmanaged custom `statusLine` is an actionable conflict and is
never chained, wrapped, or rewritten. An existing
unmanaged `projmux` entry, another Antigravity projmux ingest command, malformed
JSON, symlinks, and read/write permission failures are reported without
rewriting the file. Doctor and Settings also report a managed entry as `stale`
when its absolute executable, event/schema wiring, or stdout fallback differs
from the current plan; the displayed install command refreshes it.

The embedded v1.1.12 catalog contains the five official events `PreToolUse`,
`PostToolUse`, `PreInvocation`, `PostInvocation`, and `Stop`. The managed entry
installs `PreInvocation`, `PostInvocation`, `PostToolUse`, and `Stop`, each with
an explicit `--event`; `PreToolUse` remains disabled because its response can
change permission policy. `Statusline` remains an explicit statusline selector
outside that official hook catalog.

`PreInvocation` moves the matched pane to thinking/busy without notifying.
`PostInvocation` and `PostToolUse` remain quiet bookkeeping paths, with tool
errors retained in ingest diagnostics. `Stop` keeps the completion/error notify
classification. Hook stdout is `{}` for the three non-Stop managed events and
`{"decision":"stop"}` for Stop, including a shell fallback if ingest fails, so
the hook cannot force continuation or synthesize a permission decision.
Official statusline `agent_state` values `thinking`, `working`, and `tool_use`
map to thinking/busy unless the pane already holds a terminal completion or
approval state; this prevents a late statusline refresh from regressing `Stop`.
A new `PreInvocation` resets the pane to thinking for the next generation.
`idle` is quiet and does not clear an existing completion or approval state.
`tool_confirmation_pending=true` produces a stable-ID,
deduped approval-required row; false never produces a notification.

The managed JSON is the install source of truth. The command
`agy -p '/hooks' --output-format json` is a read-only runtime diagnostic for
confirming loaded event names and sources; projmux never uses `/hooks` output
to rewrite `hooks.json`.

`workspacePaths` uses the first non-empty path as a cwd matching candidate;
an absent or empty array does not invent a cwd. Inherited `$TMUX_PANE` remains
the first pane attribution source. A Stop with non-empty `error`, explicit
`ERROR`, or a `MAX_STEPS_EXCEEDED` family reason pushes a critical error row.
`NO_TOOL_CALL`, `MODEL_STOP`, and known normal reasons push an info completion;
unknown reasons remain info completions with diagnostic metadata rather than
being promoted to critical. Official camelCase fields retained by the parser
also include `artifactDirectoryPath`, `modelName`, `invocationNum`,
`initialNumSteps`, `toolCall`, `stepIdx`, `executionNum`, and `fullyIdle`.
Antigravity notify metadata uses `agent=antigravity`. Phase 3 session-state
restore is included: Antigravity ingest stores `conversationId` as pane thread
metadata for matching and as session-state resume metadata. Restore uses
`agy --conversation <uuid>` when that id is present and UUID-shaped; otherwise
session-state preview/doctor render `resume unavailable`. Structured statusline
`context_window.used_percentage` is persisted with its conversation id as
private hook/notify diagnostic metadata and is not surfaced as account usage.
The official `quota` map is persisted independently and surfaces each valid entry as
`quota/<exact bucket ID>` with independently retained absolute and relative
reset values. Bucket IDs are never mapped to `5h`/`weekly`, and account quota
is never inferred from the conversation-local gauge.
The earlier string percentage form remains a compatibility fallback.
Transcript contents are not read.

The canonical `internal agent-hook ingest bell --pane <pane_id>` route is the
narrow tmux-bell fallback ingest path.
It does not require the pane to be AI-managed. Projmux resolves session,
window, pane, title, command, and socket metadata from tmux, pushes an info
queue row such as `bell · <pane title>`, and suppresses repeat bell rows from
the same pane for 5 seconds.

`diagnostics agent-hook` prints recent ingest diagnostics from
`$XDG_STATE_HOME/projmux/ai-ingest.log`, or `~/.local/state/projmux/ai-ingest.log`
when `XDG_STATE_HOME` is unset. Ingest paths append compact JSONL records for
parse errors, unsupported events, missing pane matches, deduped bells,
state-only transitions, quiet high-volume events, and notify pushes. Raw hook
payloads are not stored. The log is capped at 1 MiB and trimmed to the most
recent roughly 512 KiB when it grows past the cap. Use `--json` for raw JSONL
and `--path` to print the resolved file path.

The bounded log and its 1 MiB/roughly 512 KiB retention remain available to the
canonical diagnostics consumer and support-report count summary. The common
operations journal carries only the safe anomalous classification and watcher
lifecycle described above; both diagnostic stores continue to run in parallel.
See [legacy-diagnostics-inventory.md](legacy-diagnostics-inventory.md).

For `Stop`, projmux reads `transcript_path` when present and extracts the last
assistant text from the transcript tail; if that is unavailable, it falls back
to a generic Claude completion row. `PermissionRequest` rows expose the tool
name plus a concise input summary, preferring Bash commands, file paths, and
URLs when those fields exist.

Hook row text is intentionally compact: agent label, event category, then the
best available summary. Structured details remain in queue metadata and are
available from `get notifications --json`; the sidebar does not add a separate
metadata detail view.

The extra Claude events accept conservative field aliases while Claude's event
schemas settle. `StopFailure` reads `error_type`/`errorType`/`failure_type` and
`error_message`/`errorMessage`/`message`/`reason`, plus nested
`error.type|name|code` and `error.message|text|reason`. `SubagentStop` reads
`subagent_type`/`subagentType`/`agent_type`, `subagent_id`/`subagentId`, and
nested `subagent.type|name|kind|id`. `TeammateIdle` reads
`teammate_name`/`teammateName`, `teammate_id`/`teammateId`,
`teammate_context`/`teammateContext`/`context`/`reason`/`message`, and nested
`teammate.name|id|context|status|reason|message`.

Claude Code hook ingest is available through `ingest claude-hook`, but
`integrate claude` is the opt-in user-level wiring command for
`~/.claude/settings.json`. It installs command hooks for every event whose
effective Claude hook catalog entry has `"install": true`. The embedded default
catalog is based on Claude Code 2.1.140 and lives at
`internal/app/ai_hook_catalogs/claude.json`; a local override may be placed at
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/claude.json` to disable
or add events before projmux itself is released:

```json
{
  "provider": "claude",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

The embedded Claude catalog contains all 29 Claude Code 2.1.140 events:
`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`,
`PermissionDenied`, `Notification`, `UserPromptSubmit`,
`UserPromptExpansion`, `SessionStart`, `Stop`, `StopFailure`,
`SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`, `SessionEnd`,
`PermissionRequest`, `Setup`, `TeammateIdle`, `TaskCreated`,
`TaskCompleted`, `Elicitation`, `ElicitationResult`, `ConfigChange`,
`InstructionsLoaded`, `WorktreeCreate`, `WorktreeRemove`, `CwdChanged`, and
`FileChanged`. `SubagentStop` remains quiet/log-only.

The managed command receives Claude's hook JSON on stdin, keeps stdout/stderr
quiet, and exits successfully even if ingest fails so it does not block Claude
Code behavior. `--dry-run` previews the JSON update, and `--remove` deletes
only commands carrying the projmux marker. Removal and unmanaged conflict
detection scan every `hooks` event in the settings file rather than trusting the
current catalog, so stale managed events from older catalogs are still removed.
Existing unrelated Claude settings and hooks are preserved. If any event already
contains an unmanaged `projmux internal agent-hook ingest claude-hook` command, projmux refuses
to install over it and leaves the settings file untouched.

`projmux agent integrate codex` manages a hooks-engine
block in `~/.codex/config.toml`. It enables `[features] hooks = true`,
merging into an existing `[features]` table when present, and installs broad
command hooks for every event whose effective Codex hook catalog entry has
`"install": true`. The embedded default catalog is based on Codex CLI 0.130.0
and lives at `internal/app/ai_hook_catalogs/codex.json`; a local override may
be placed at `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hooks.d/codex.json`:

```json
{
  "provider": "codex",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "FutureEvent", "install": true, "action": "quiet" }
  ]
}
```

The embedded Codex catalog contains the 8 Codex CLI 0.130.0 events
`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreCompact`,
`PostCompact`, `SessionStart`, `UserPromptSubmit`, and `Stop`.

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

Codex hooks are the default integration mode. The hooks install is idempotent,
preserves unrelated Codex config and unmanaged hook entries, and refuses to
install over unmanaged projmux Codex hook commands it cannot safely own.
`--remove` removes projmux-managed Codex hooks wiring. `--dry-run` prints the
planned change without writing. Codex may still require reviewing or trusting
the hook through its `/hooks` flow before commands run.

`integrate tmux-bell` is opt-in server-level tmux wiring for arbitrary tools
that emit BEL or OSC 9. It applies `allow-passthrough on`, `monitor-bell on`,
`bell-action other`, and appends a marked `alert-bell` hook that invokes
`projmux internal agent-hook ingest bell --pane "#{pane_id}"`. `--dry-run` prints the tmux
commands. tmux `alert-bell` is a window alert and does not expose
`#{hook_pane}` on tmux 3.4, so `#{pane_id}` is the best available pane context.
`--remove` unsets only hook entries carrying the projmux marker and leaves
user-owned `alert-bell` hooks alone.

## internal tmux

```
projmux internal tmux popup-toggle [--client <key>] <mode>
projmux internal tmux popup-switch
projmux internal tmux popup-sessions
projmux internal tmux popup-preview <session>
projmux internal tmux rebalance-panes
projmux internal tmux rename-pane <pane> <label>
projmux internal tmux print-config     [--bin <path>]
projmux internal tmux print-app-config [--bin <path>]
projmux internal tmux install     [--bin <path>] [--config <path>] [--include <path>]
projmux internal tmux install-app [--bin <path>] [--config <path>]
projmux internal tmux apply
```

Helpers tmux's keybindings and the install pipeline call into. Modes
accepted by `popup-toggle` mirror the historical sessionizer surface:
`session-popup`, `sessionizer`, `sessionizer-sidebar`,
`notify-sidebar`, `recent-windows`, `resource-inspector`, `ai-split-picker-right`,
`ai-split-picker-down`, `ai-split-resume-right`, `ai-split-resume-down`,
`ai-split-settings`.
`rename-pane` sets only the pane-scoped user label
`@projmux_pane_label`; an empty label clears the option. It does not change the
raw tmux pane title, AI topic, or AI topic manual-ownership flag. The canonical
keybinding action id is `rename-pane-label`. The retired `rename-pane-topic`
keymap action is no longer accepted: replace a stale
`[bindings.rename-pane-topic]` table with `[bindings.rename-pane-label]`.
The `projmux agent topic set/clear` commands keep
AI topic ownership separate from the user pane label and raw pane title.
`apply` regenerates the app tmux config and reloads the live `-L projmux`
server without restarting it. `make install` and `projmux update apply` invoke it
after replacing the binary. Settings > Keybindings normally runs the same
save/config/reload flow automatically; use `projmux config apply` (or its
hidden equivalent `projmux internal tmux apply`) as the CLI recovery or sync path after
hand-editing `keymap.toml`, after saving Settings outside tmux, or after
resolving a reported config-generation or live-reload failure. Reload also
removes the known retired no-prefix `C-t` pane-label binding from older live
servers before installing current bindings. If the current keymap assigns `C-t`
to another action, that current action is bound after cleanup and remains the
owner.

## config

```
projmux config edit [--get|--set <mode>]
projmux config render standalone [--bin <path>]
projmux config render app        [--bin <path>]
projmux config apply             [--bin <path>] [--config <path>] [--socket <name>]
```

The public configuration domain. Every route here is a parity alias over the
handler that already owned the behavior — identical stdout, stderr, exit code,
and side effects. `edit` forwards to the existing AI split-mode settings
picker and preserves its `--get`/`--set` forms; `render` and `apply` forward to
the existing `tmux` handler. The general `settings` UI remains a separate
shortcut and is not renamed by this route.

`render` takes the artifact as a positional token because projmux generates two
different tmux configs, not two views of one:

- `standalone` is the snippet you source from your own `~/.tmux.conf`. It prints
  to stdout and writes nothing. Equivalent to `projmux internal tmux print-config`.
- `app` is the config the app-owned `-L projmux` server runs from. It carries the
  default shell and the app-only bindings on top of the standalone content, and
  it also only prints. Equivalent to `projmux internal tmux print-app-config`.

A bare `projmux config render` is a usage error (exit 2) listing the two
artifacts. There is deliberately no default: silently choosing one would leave
the other with no obvious public spelling, which is the gap this route exists to
close.

`apply` takes no artifact. It writes the app tmux config and reloads the live
`-L projmux` server, which is one operation over one file. Equivalent to
`projmux internal tmux apply`.

Writing the standalone snippet and wiring the `source-file` line into your
`~/.tmux.conf` is `projmux internal tmux install`, and writing the app config without
reloading is `projmux internal tmux install-app`. Both are install-pipeline plumbing and
have no public spelling; see
[Internal plumbing](#internal-plumbing-projmux-internal-).

## update

```
projmux update status [--json]
projmux update check  [--json]
projmux update apply  [--dry-run] [--no-apply]
```

Installer-aware update status foundation. `status` is read-only: it
prints the current version, cached latest GitHub Release tag when present,
cache freshness (`fresh`, `stale`, or `unknown`), update state, detected
installer source, and cache path. It never reaches the network, so it is
safe for interactive use and shell startup paths.

`check` fetches the latest GitHub Release metadata for
`crevissepartners/projmux`, atomically writes
`${XDG_CACHE_HOME:-~/.cache}/projmux/update.json`, then prints the concise
latest/update/cache result. `--json` emits the same machine-readable
status shape for both subcommands.

`projmux shell` reads the same cache before opening the isolated tmux app.
When the cache is missing or stale, shell startup attempts a bounded
best-effort refresh, then continues even if the network check fails. When a
fresh cached update is available, the shell welcome uses Enter=Continue,
`u`=Upgrade, and `s`=Skip until next. Upgrade invokes only
`projmux update apply`; Skip until next writes the latest release tag to
`${XDG_CACHE_HOME:-~/.cache}/projmux/update-skip.json` and suppresses that tag
until a newer latest release appears.

Installer detection honors an explicit
`PROJMUX_INSTALLER=npm|go|github-release|source` first. When it is unset,
detection falls back to inspecting the running binary: an npm install tree
(`node_modules/@projmux/...`) is reported as `npm`, a `go install …@latest`
binary in `$GOBIN`/`$GOPATH/bin`/`~/go/bin` as `go`, and a local `go build`
(`make install`) — which reports a `(devel)` main-module version — as `source`.
`github-release` installs are indistinguishable from a hand-placed binary and
still require an explicit `PROJMUX_INSTALLER=github-release`. Anything else is
reported as `unknown` with guidance.
`apply` is installer-aware and only runs after explicit user selection.
For npm installs, it runs `npm install -g projmux@latest` (which reliably
crosses minor/major versions where `npm update -g` does not, and re-resolves
the per-platform optional dependency) and then runs the new binary's
`projmux config apply`. With `--no-apply`, that convergence step uses
`--no-reload`: it still migrates marker-owned files and writes generated
configuration without accessing live tmux. For Go installs, it uses the existing atomic
replacement implementation. For `github-release` installs, it downloads the latest
matching `projmux_<version>_<goos>_<goarch>.tar.gz` release asset, extracts the
binary, atomically replaces the current executable, then performs the same
apply/`--no-reload` convergence. `source` installs report an
actionable error to update the checkout with `git pull --ff-only && make install`.

## welcome

```
projmux welcome [--popup [--force]]
```

Prints the shell-entry release prompt and bootstrap reminder without starting
tmux. This is useful when you want to revisit the welcome view at any time.

`--popup` is the tmux attach-helper form. It shows the popup only when a
pending attach welcome marker exists. `--popup --force` opens the popup without
consulting pending or skip state. Passing positional arguments prints usage and
returns a usage error.

## runtime / pin / prune / internal helpers

The live tmux inventory is under `runtime`: `runtime sessions`, `runtime
attach`, `runtime stop`, `runtime tag`, and `runtime prune`. Project pins use
`pin project list|add|remove|toggle|clear|migrate`; `list` takes `--kind
project|candidate` and `migrate` takes `--dry-run`. Resource retention uses `prune
project|snapshot`, while explicit snapshot deletion uses `delete snapshot`.

Popup-marker, preview, status, and tmux configuration plumbing is hidden under
`internal session-popup`, `internal preview`, `internal status`, `internal
statusbar`, and `internal tmux`. Generated configuration is their producer;
human configuration work should prefer `config render` and `config apply`.

## shell / attach / settings / quit

- `shell` — boot the isolated `-L projmux` tmux server with the
  generated config. The generated app config uses absolute `$SHELL` as the
  tmux default shell when set, otherwise `/bin/sh`. `shell` starts or attaches
  the app session directly after resolving the target app session name and
  startup directory. Alt-1 sidebar project open defaults to `Project topology`,
  which materializes the Project's Registry Windows and Window-owned shell Panes
  before the client moves; the Session State `Sidebar startup picker` opt-in
  shows `Latest snapshot`, `Named snapshot`, and `Project topology` before
  starting a closed project session. `Latest snapshot` is auto-saved; named
  snapshots are fixed until the user saves or replaces them. A directory with no
  Registry Project, and a Project with no Registry Window, still start as a
  single default session.
- `quit` — open an action picker with `Quit projmux` and `Cancel`. Selecting
  `Quit projmux` terminates only a `tmux -L projmux` runtime whose global
  `@projmux_app` option is set by the generated app config. Missing servers,
  default tmux servers, embedded tmux servers, and other tmux runtimes without
  that marker are no-ops. Non-interactive callers must pass `--yes` or
  `--force`; the default command always goes through the action picker.
- `attach project <ref>` — enter a Project runtime from outside tmux.
  Automatic live-runtime attachment is `runtime attach`.
- `settings` — interactive configuration UI for the project picker, AI
  splits, Notifications, Appearance mode, Project Root management, the
  switcher's saved workdirs list, Labs (experimental), Settings > Keybindings,
  and About/Update status. The keybinding flow is a single
  `Settings > Keybindings` action list with simplified action details for
  aliases and reset. Key save/reset automatically writes the key list,
  regenerates the app config, and reloads the running tmux session when
  possible; skipped or failed stages show `projmux config apply` as the recovery
  or sync command. Terminal diagnostics and terminal mapping application stay
  in the `projmux shell` -> `projmux setup` -> `projmux setup terminal`
  remediation path.
  The compact About section contains Version, Source, real update
  status/actions, Welcome, and Quit. It does not duplicate static setup or
  diagnostics guides: use `projmux setup` for key-delivery diagnosis,
  `projmux setup terminal` for supported terminal remediation, and the
  read-only `projmux doctor` report for dependency/runtime diagnostics. In Project
  Picker, `Project Root` manages the saved
  primary root (`~/.config/projmux/projdir`) and displays whether the effective
  value comes from `PROJMUX_PROJDIR`, tmux `@projmux_projdir`, saved config, or
  no configured source. When no source is configured, the direct-set prompt
  starts with `$HOME` as an editable fallback, but `$HOME` is not used as the
  effective root unless saved. `Workdirs` remains separate: those entries are
  additional search roots, not the primary root. Appearance stores per-surface
  path/git/notify icon decoration as `off` (default), `symbol`, or `emoji` and
  updates the matching live tmux option when available. Labs remains
  available for experimental settings. The About section reads the cached
  update status without network access;
  selecting Check Updates runs `projmux update check`, Update Now runs
  `projmux update apply`, and Welcome opens a Settings-native viewer
  independent of shell skip state. `Settings > About > Quit projmux` routes
  through the same `projmux quit` action picker. Settings mutations surface
  their handled success/failure as a transient passive row inside the native
  popup; selecting the next action clears or replaces that row.

## See also

- [cli.md](cli.md) — the generated CLI reference: the authoritative route,
  sub-route, output-mode, and field-projection inventory for this build.
- [npm-distribution.md](npm-distribution.md) — npm binary package layout.
- [statusbar.md](statusbar.md) — two-line layout and click range catalogue.
- [notify-queue.md](notify-queue.md) — queue file format and lifecycle.
- [usage-tracking.md](usage-tracking.md) — adapter HTTP/file behaviour.
- [keybindings.md](keybindings.md) — terminal key delivery and aliases.
- [hooks.md](hooks.md) — lifecycle hooks, startup commands, and `send-noti` payload contract.
