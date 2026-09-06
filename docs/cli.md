<!-- Code generated from the projmux command manifest (internal/cli/catalog.go). DO NOT EDIT. -->
<!-- Regenerate with: make docs -->

# CLI Reference

This page is generated from the projmux command manifest, the same source the
binary renders `projmux help` and `projmux <route> --help` from. Editing it by
hand is pointless: run `make docs` instead, and a drift test fails the build when the
checked-in page and the manifest disagree.

It documents the routes that exist in this build and nothing else. Contract
spellings a later release will introduce are absent on purpose, as is the
hidden internal plumbing namespace, which is not part of the public surface.

Prose that a manifest cannot hold -- the help boundary contract, exit codes,
per-flag behavior, and task-oriented walkthroughs -- lives in the
[CLI Task Guide](cli-guide.md).

## Selectorless authority

Each label describes what omission means for one graph route; explicit selectors still replace a natural target.

- `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.
- `explicit-target` — the route or caller must name the exact target.
- `refusal` — there is no safe selectorless action; refuse before output or mutation.
- `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Root parser bridges outside the route graph are censused from their parser token lists:

| Bridge | Selectorless authority |
| --- | --- |
| `<bare invocation>` | `natural-omitted` |
| `<root help flag:help>` | `explicit-fan-out` |
| `<root help flag:h>` | `explicit-fan-out` |
| `--version` | `explicit-fan-out` |
| `-version` | `explicit-fan-out` |

## Command effects

Every route declares one allowed-effect record over seven independent resource axes. A pipe separates conditional success outcomes; preflight refusal remains zero-effect. `domain-effect=null` means the route has no typed extension beyond this resource tuple.

The machine-readable manifest contains 184 route-effect records, including hidden plumbing that the public route sections omit.

| Axis | Closed vocabulary |
| --- | --- |
| `identity` | `unchanged|created|reused|removed|replaced` |
| `address` | `unchanged|allocated|renamed|released` |
| `topology` | `unchanged|established|reparented|removed|replaced` |
| `desired-state` | `unchanged|created|reused|removed|replaced` |
| `runtime` | `unchanged|materialized|already-live|reparented|stopped|preserved` |
| `focus` | `unchanged|moved-current-client|attached-caller` |
| `cardinality` | `unchanged|exact-one|one-or-more|zero-or-more` |
| `domain-effect` | `null|agent-delivery` |

## Commands

```
projmux <command> [args...]
```

| Command | Kind | Summary |
| --- | --- | --- |
| [`projmux agent`](#projmux-agent) | canonical | Manage Agent state, topic, capabilities, integrations, and account usage |
| [`projmux attention`](#projmux-attention) | canonical | View and manage live tmux pane attention state |
| [`projmux attach`](#projmux-attach) | canonical | Enter a Project runtime from outside tmux |
| [`projmux config`](#projmux-config) | canonical | Edit AI split-mode settings; render or apply generated tmux configuration |
| [`projmux create`](#projmux-create) | canonical | Create Projmux resources |
| [`projmux delete`](#projmux-delete) | canonical | Delete Projmux resources with an explicit cascade plan |
| [`projmux describe`](#projmux-describe) | canonical | Describe one Projmux resource |
| [`projmux doctor`](#projmux-doctor) | shortcut | Run read-only runtime and integration diagnostics |
| [`projmux diagnostics`](#projmux-diagnostics) | canonical | Read operational events or create an explicit local support report |
| [`projmux focus`](#projmux-focus) | canonical | Move the current client to a live resource |
| [`projmux get`](#projmux-get) | canonical | Read Projmux resources by selector |
| [`projmux hook`](#projmux-hook) | canonical | List, edit, validate, and trust lifecycle hook config |
| [`projmux notification`](#projmux-notification) | canonical | Manage pending notification workflow state |
| [`projmux open`](#projmux-open) | canonical | Open a Project runtime and move the current client to it |
| [`projmux pin`](#projmux-pin) | canonical | Manage pinned project directories |
| [`projmux prune`](#projmux-prune) | canonical | Prune stale Projects and snapshots |
| [`projmux quit`](#projmux-quit) | shortcut | Quit the app-owned projmux tmux runtime |
| [`projmux reconcile`](#projmux-reconcile) | canonical | Preview or repair Registry and exact tmux resource drift |
| [`projmux rebind`](#projmux-rebind) | canonical | Rebind a Project to a new absolute root without moving files |
| [`projmux rename`](#projmux-rename) | canonical | Rename a Projmux resource metadata.name |
| [`projmux resources`](#projmux-resources) | shortcut | Inspect live Project, Window, and Pane CPU/RSS attribution |
| [`projmux restore`](#projmux-restore) | canonical | Project a saved snapshot into one exact closed Project desired state |
| [`projmux runtime`](#projmux-runtime) | canonical | Manage the live and ephemeral tmux runtime inventory |
| [`projmux settings`](#projmux-settings) | shortcut | Configure projmux |
| [`projmux setup`](#projmux-setup) | canonical | Probe terminal keys or remediate them with setup terminal |
| [`projmux shell`](#projmux-shell) | shortcut | Open the isolated projmux tmux app |
| [`projmux start`](#projmux-start) | canonical | Start a Project runtime without moving any client |
| [`projmux stop`](#projmux-stop) | canonical | Stop a Project runtime without unregistering anything |
| [`projmux switch`](#projmux-switch) | shortcut | Pick a project and compose create project with open project |
| [`projmux unregister`](#projmux-unregister) | canonical | Unregister Projects from the Registry while preserving runtime and files |
| [`projmux update`](#projmux-update) | canonical | Check installer-aware release update status |
| [`projmux welcome`](#projmux-welcome) | shortcut | Reprint the shell welcome guide |
| [`projmux window`](#projmux-window) | canonical | Open recent window navigation surfaces |
| [`projmux help`](#projmux-help) | canonical | Show bootstrap help |
| [`projmux version`](#projmux-version) | canonical | Print the current version |

## `projmux agent`

Manage Agent state, topic, capabilities, integrations, and account usage

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]
projmux agent topic get|clear [<agent-ref>] [--agent <ref>]
projmux agent topic set <text> [<agent-ref>] [--agent <ref>]
projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...
projmux agent turn start|steer <agent-ref> -- <text>
projmux agent turn interrupt <agent-ref>
projmux agent approval review <agent-ref> [--request <normalized-id>]
projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]
projmux agent integrate <codex|claude|antigravity|tmux-bell> [--remove] [--dry-run]
projmux agent usage [--model <codex|claude|antigravity|all>] [--window <name>] [--json] [--force]
projmux agent app-server upgrade plan|apply --request <absolute-json>
projmux agent app-server upgrade resume|abort --operation <ref>
projmux agent app-server handover plan|apply --request <absolute-json>
projmux agent app-server handover resume|abort --operation <ref>
projmux agent capabilities [<agent-ref> | --provider <codex|claude|antigravity>] [-o json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent status`](#projmux-agent-status) | Read or set semantic Agent interaction independently of lifecycle |
| [`projmux agent topic`](#projmux-agent-topic) | Read, set, or clear one exact Agent topic annotation |
| [`projmux agent resume`](#projmux-agent-resume) | Rebind an Offline or Failed Agent detached on its Window's exact shell or Agent anchor |
| [`projmux agent turn`](#projmux-agent-turn) | Send, steer, or interrupt one exact native Codex turn |
| [`projmux agent approval`](#projmux-agent-approval) | Review one exact pending native Codex approval |
| [`projmux agent review`](#projmux-agent-review) | Start a native review on an exact-bound Codex Agent |
| [`projmux agent integrate`](#projmux-agent-integrate) | Install, remove, or preview provider hooks and tmux-bell integration |
| [`projmux agent usage`](#projmux-agent-usage) | Read provider account usage quota snapshots |
| [`projmux agent app-server`](#projmux-agent-app-server) | Manage explicitly requested private Codex app-server generation operations |
| [`projmux agent capabilities`](#projmux-agent-capabilities) | Read static provider support or one exact Agent's Registry-backed runtime eligibility |

Canonical spelling: `projmux agent status`, `projmux agent topic`, `projmux agent resume`, `projmux agent turn start`, `projmux agent turn steer`, `projmux agent turn interrupt`, `projmux agent approval review`, `projmux agent review`, `projmux agent integrate`, `projmux agent usage`, `projmux agent app-server upgrade plan`, `projmux agent app-server upgrade apply`, `projmux agent app-server upgrade resume`, `projmux agent app-server upgrade abort`, `projmux agent app-server handover plan`, `projmux agent app-server handover apply`, `projmux agent app-server handover resume`, `projmux agent app-server handover abort`, `projmux agent capabilities`

### `projmux agent status`

Read or set semantic Agent interaction independently of lifecycle

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]
```

### `projmux agent topic`

Read, set, or clear one exact Agent topic annotation

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent topic get|clear [<agent-ref>] [--agent <ref>]
projmux agent topic set <text> [<agent-ref>] [--agent <ref>]
```

### `projmux agent resume`

Rebind an Offline or Failed Agent detached on its Window's exact shell or Agent anchor

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=reused|created`
- `address=unchanged|allocated`
- `topology=unchanged|established`
- `desired-state=unchanged|created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

### `projmux agent turn`

Send, steer, or interrupt one exact native Codex turn

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent turn start|steer <agent-ref> -- <text>
projmux agent turn interrupt <agent-ref>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent turn start`](#projmux-agent-turn-start) | Send a new turn to one exact idle Codex thread |
| [`projmux agent turn steer`](#projmux-agent-turn-steer) | Request provider acceptance for one exact current Codex turn; delivery remains unconfirmed |
| [`projmux agent turn interrupt`](#projmux-agent-turn-interrupt) | Interrupt one exact current Codex turn |

Canonical spelling: `projmux agent turn start`, `projmux agent turn steer`, `projmux agent turn interrupt`

#### `projmux agent turn start`

Send a new turn to one exact idle Codex thread

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent turn start <agent-ref> -- <text>
```

#### `projmux agent turn steer`

Request provider acceptance for one exact current Codex turn; delivery remains unconfirmed

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent turn steer <agent-ref> -- <text>
```

#### `projmux agent turn interrupt`

Interrupt one exact current Codex turn

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent turn interrupt <agent-ref>
```

### `projmux agent approval`

Review one exact pending native Codex approval

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent approval review <agent-ref> [--request <normalized-id>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent approval review`](#projmux-agent-approval-review) | Review one exact pending native Codex approval |

Canonical spelling: `projmux agent approval review`

#### `projmux agent approval review`

Review one exact pending native Codex approval

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent approval review <agent-ref> [--request <normalized-id>]
```

### `projmux agent review`

Start a native review on an exact-bound Codex Agent

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]
```

### `projmux agent integrate`

Install, remove, or preview provider hooks and tmux-bell integration

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent integrate <codex|claude|antigravity|tmux-bell> [--remove] [--dry-run]
```

### `projmux agent usage`

Read provider account usage quota snapshots

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent usage [--model <codex|claude|antigravity|all>] [--window <name>] [--json] [--force]
```

### `projmux agent app-server`

Manage explicitly requested private Codex app-server generation operations

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent app-server upgrade`](#projmux-agent-app-server-upgrade) | Plan, apply, resume, or abort one exact rolling generation operation |
| [`projmux agent app-server handover`](#projmux-agent-app-server-handover) | Plan, apply, resume, or abort one exact generation-wide handover |

Canonical spelling: `projmux agent app-server upgrade plan`, `projmux agent app-server upgrade apply`, `projmux agent app-server upgrade resume`, `projmux agent app-server upgrade abort`, `projmux agent app-server handover plan`, `projmux agent app-server handover apply`, `projmux agent app-server handover resume`, `projmux agent app-server handover abort`

#### `projmux agent app-server upgrade`

Plan, apply, resume, or abort one exact rolling generation operation

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server upgrade
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent app-server upgrade plan`](#projmux-agent-app-server-upgrade-plan) | Read the mutation-zero plan for one exact private generation upgrade |
| [`projmux agent app-server upgrade apply`](#projmux-agent-app-server-upgrade-apply) | Apply one exact crash-resumable private generation admission switch |
| [`projmux agent app-server upgrade resume`](#projmux-agent-app-server-upgrade-resume) | Resume one exact durable rolling generation operation |
| [`projmux agent app-server upgrade abort`](#projmux-agent-app-server-upgrade-abort) | Abort one pre-admission operation and clean only its exact candidate |

Canonical spelling: `projmux agent app-server upgrade plan`, `projmux agent app-server upgrade apply`, `projmux agent app-server upgrade resume`, `projmux agent app-server upgrade abort`

##### `projmux agent app-server upgrade plan`

Read the mutation-zero plan for one exact private generation upgrade

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server upgrade plan --request <absolute-json>
```

##### `projmux agent app-server upgrade apply`

Apply one exact crash-resumable private generation admission switch

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server upgrade apply --request <absolute-json>
```

##### `projmux agent app-server upgrade resume`

Resume one exact durable rolling generation operation

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server upgrade resume --operation <ref>
```

##### `projmux agent app-server upgrade abort`

Abort one pre-admission operation and clean only its exact candidate

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server upgrade abort --operation <ref>
```

#### `projmux agent app-server handover`

Plan, apply, resume, or abort one exact generation-wide handover

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server handover
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent app-server handover plan`](#projmux-agent-app-server-handover-plan) | Read the exact target-set generation handover plan |
| [`projmux agent app-server handover apply`](#projmux-agent-app-server-handover-apply) | Apply one crash-resumable generation-wide handover |
| [`projmux agent app-server handover resume`](#projmux-agent-app-server-handover-resume) | Resume one exact durable generation handover |
| [`projmux agent app-server handover abort`](#projmux-agent-app-server-handover-abort) | Abort one exact pre-stop generation handover |

Canonical spelling: `projmux agent app-server handover plan`, `projmux agent app-server handover apply`, `projmux agent app-server handover resume`, `projmux agent app-server handover abort`

##### `projmux agent app-server handover plan`

Read the exact target-set generation handover plan

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server handover plan --request <absolute-json>
```

##### `projmux agent app-server handover apply`

Apply one crash-resumable generation-wide handover

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server handover apply --request <absolute-json>
```

##### `projmux agent app-server handover resume`

Resume one exact durable generation handover

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server handover resume --operation <ref>
```

##### `projmux agent app-server handover abort`

Abort one exact pre-stop generation handover

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux agent app-server handover abort --operation <ref>
```

### `projmux agent capabilities`

Read static provider support or one exact Agent's Registry-backed runtime eligibility

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent capabilities [<agent-ref> | --provider <codex|claude|antigravity>] [-o json]
```

Output modes (`-o`): `json`

## `projmux attention`

View and manage live tmux pane attention state

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux attention toggle|clear|arm|list|window
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux attention toggle`](#projmux-attention-toggle) | Toggle attention state for a pane |
| [`projmux attention clear`](#projmux-attention-clear) | Clear attention state for a pane |
| [`projmux attention arm`](#projmux-attention-arm) | Arm focus-only attention consumption |
| [`projmux attention list`](#projmux-attention-list) | List live pane attention state |
| [`projmux attention window`](#projmux-attention-window) | Render window-scoped attention badges |

Canonical spelling: `projmux attention list`, `projmux attention toggle`, `projmux attention clear`, `projmux attention arm`, `projmux attention window`

### `projmux attention toggle`

Toggle attention state for a pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux attention toggle [pane]
```

### `projmux attention clear`

Clear attention state for a pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux attention clear [pane]
```

### `projmux attention arm`

Arm focus-only attention consumption

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux attention arm [pane]
```

### `projmux attention list`

List live pane attention state

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux attention list
```

### `projmux attention window`

Render window-scoped attention badges

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux attention window
```

## `projmux attach`

Enter a Project runtime from outside tmux

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux attach project <ref>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux attach project`](#projmux-attach-project) | Enter a Project runtime from outside tmux, materializing it when offline |

Canonical spelling: `projmux attach project`

### `projmux attach project`

Enter a Project runtime from outside tmux, materializing it when offline

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=materialized|already-live`
- `focus=attached-caller`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux attach project <ref>
```

## `projmux config`

Edit AI split-mode settings; render or apply generated tmux configuration

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config edit [--get|--set <mode>]
projmux config render standalone|app [--bin <path>]
projmux config apply [--bin <path>] [--config <path>] [--socket <name>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux config edit`](#projmux-config-edit) | Edit the AI split-mode configuration |
| [`projmux config render`](#projmux-config-render) | Print a generated tmux config to stdout; writes nothing |
| [`projmux config apply`](#projmux-config-apply) | Write the generated app tmux config and reload the live projmux server |

Canonical spelling: `projmux config edit`, `projmux config render`, `projmux config apply`

### `projmux config edit`

Edit the AI split-mode configuration

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config edit [--get|--set <mode>]
```

### `projmux config render`

Print a generated tmux config to stdout; writes nothing

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config render standalone [--bin <path>]
projmux config render app [--bin <path>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux config render standalone`](#projmux-config-render-standalone) | Print the snippet you source from your own tmux.conf |
| [`projmux config render app`](#projmux-config-render-app) | Print the config the app-owned projmux tmux server runs from |

#### `projmux config render standalone`

Print the snippet you source from your own tmux.conf

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config render standalone [--bin <path>]
```

Canonical spelling: `projmux config render`

#### `projmux config render app`

Print the config the app-owned projmux tmux server runs from

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config render app [--bin <path>]
```

Canonical spelling: `projmux config render`

### `projmux config apply`

Write the generated app tmux config and reload the live projmux server

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux config apply [--bin <path>] [--config <path>] [--socket <name>]
```

## `projmux create`

Create Projmux resources

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]
projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create claude|antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]
projmux create snapshot
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux create project`](#projmux-create-project) | Register one exact filesystem path as a Registry Project; no runtime is materialized |
| [`projmux create window`](#projmux-create-window) | Create a Window and its initial Pane below one Project; the runtime is materialized detached |
| [`projmux create pane`](#projmux-create-pane) | Create a shell Pane detached on an explicit Pane or the Window's exact shell or Agent anchor |
| [`projmux create agent`](#projmux-create-agent) | Create an Agent detached on an explicit Pane or the Window's exact shell or Agent anchor; --provider is required |
| [`projmux create notification`](#projmux-create-notification) | Create a pending notification row |
| [`projmux create snapshot`](#projmux-create-snapshot) | Create a session snapshot |

Provider shortcuts:

| Route | Summary |
| --- | --- |
| [`projmux create codex`](#projmux-create-codex) | Provider shortcut for create agent --provider codex |
| [`projmux create claude`](#projmux-create-claude) | Provider shortcut for create agent --provider claude |
| [`projmux create antigravity`](#projmux-create-antigravity) | Provider shortcut for create agent --provider antigravity |

Canonical spelling: `projmux create project`, `projmux create window`, `projmux create pane`, `projmux create agent`, `projmux create notification`, `projmux create snapshot`, `projmux create codex`, `projmux create claude`, `projmux create antigravity`

### `projmux create project`

Register one exact filesystem path as a Registry Project; no runtime is materialized

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=created|reused`
- `address=allocated|unchanged`
- `topology=established|unchanged`
- `desired-state=created|reused`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `receipt`

### `projmux create window`

Create a Window and its initial Pane below one Project; the runtime is materialized detached

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

### `projmux create pane`

Create a shell Pane detached on an explicit Pane or the Window's exact shell or Agent anchor

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

### `projmux create agent`

Create an Agent detached on an explicit Pane or the Window's exact shell or Agent anchor; --provider is required

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

### `projmux create notification`

Create a pending notification row

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]
```

### `projmux create snapshot`

Create a session snapshot

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux create snapshot
```

### `projmux create codex`

Provider shortcut for create agent --provider codex

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

### `projmux create claude`

Provider shortcut for create agent --provider claude

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux create claude [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

### `projmux create antigravity`

Provider shortcut for create agent --provider antigravity

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=created`
- `address=allocated`
- `topology=established`
- `desired-state=created`
- `runtime=materialized`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux create antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`, `receipt`

## `projmux delete`

Delete Projmux resources with an explicit cascade plan

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]
projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux delete project`](#projmux-delete-project) | Deprecated alias of unregister project; unregisters Projects and Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime |
| [`projmux delete window`](#projmux-delete-window) | Delete Registry Windows and every descendant Agent and Pane, killing an exact live tmux mirror when present; no selector inside tmux means the active Window, and --all means every Window in the registry |
| [`projmux delete pane`](#projmux-delete-pane) | Delete Panes; an Agent-owned current Pane leaves its Agent Offline; no selector inside tmux means the active Pane, and --all means every Pane in the registry |
| [`projmux delete agent`](#projmux-delete-agent) | Delete Agents and their managed Panes; no selector inside tmux means the active Agent, and --all means every Agent in the registry |
| [`projmux delete notification`](#projmux-delete-notification) | Delete pending notification rows |
| [`projmux delete snapshot`](#projmux-delete-snapshot) | Delete saved session snapshots |

Canonical spelling: `projmux unregister project`, `projmux delete window`, `projmux delete pane`, `projmux delete agent`, `projmux delete notification`, `projmux delete snapshot`

### `projmux delete project`

Deprecated alias of unregister project; unregisters Projects and Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=preserved`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]
```

Aliases: `projects`

Canonical spelling: `projmux unregister project`

### `projmux delete window`

Delete Registry Windows and every descendant Agent and Pane, killing an exact live tmux mirror when present; no selector inside tmux means the active Window, and --all means every Window in the registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=unchanged|stopped`
- `focus=unchanged|moved-current-client`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
```

Aliases: `windows`

### `projmux delete pane`

Delete Panes; an Agent-owned current Pane leaves its Agent Offline; no selector inside tmux means the active Pane, and --all means every Pane in the registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=unchanged|stopped`
- `focus=unchanged|moved-current-client`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
```

Aliases: `panes`

### `projmux delete agent`

Delete Agents and their managed Panes; no selector inside tmux means the active Agent, and --all means every Agent in the registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=unchanged|stopped`
- `focus=unchanged|moved-current-client`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
```

Aliases: `agents`

### `projmux delete notification`

Delete pending notification rows

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux delete notification
```

Aliases: `notifications`

### `projmux delete snapshot`

Delete saved session snapshots

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux delete snapshot
```

Aliases: `snapshots`

## `projmux describe`

Describe one Projmux resource

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]
projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]
projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]
projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux describe project`](#projmux-describe-project) | Describe one Project resource; with no selector inside tmux, the active Project |
| [`projmux describe window`](#projmux-describe-window) | Describe one Window resource; inside tmux a reference resolves within the active Project and no selector means the active Window |
| [`projmux describe pane`](#projmux-describe-pane) | Describe one Pane resource; inside tmux a reference resolves within the active Project and no selector means the active Pane |
| [`projmux describe agent`](#projmux-describe-agent) | Describe one Agent resource; inside tmux a reference resolves within the active Project and no selector means the Agent owning the active Pane |

Canonical spelling: `projmux describe project`, `projmux describe window`, `projmux describe pane`, `projmux describe agent`

### `projmux describe project`

Describe one Project resource; with no selector inside tmux, the active Project

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]
```

Aliases: `projects`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe window`

Describe one Window resource; inside tmux a reference resolves within the active Project and no selector means the active Window

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]
```

Aliases: `windows`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe pane`

Describe one Pane resource; inside tmux a reference resolves within the active Project and no selector means the active Pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]
```

Aliases: `panes`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe agent`

Describe one Agent resource; inside tmux a reference resolves within the active Project and no selector means the Agent owning the active Pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]
```

Aliases: `agents`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

## `projmux doctor`

Run read-only runtime and integration diagnostics

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux doctor [--json] [--section <name>] [--verbose]
```

## `projmux diagnostics`

Read operational events or create an explicit local support report

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux diagnostics log [--json] [--tail <n>]
projmux diagnostics agent-hook [--tail <n>] [--json] [--path]
projmux diagnostics report [--output <path>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux diagnostics log`](#projmux-diagnostics-log) | Read the bounded local operations journal |
| [`projmux diagnostics agent-hook`](#projmux-diagnostics-agent-hook) | Read the bounded Agent hook ingest journal |
| [`projmux diagnostics report`](#projmux-diagnostics-report) | Create an explicit redacted local support report |

Canonical spelling: `projmux diagnostics log`, `projmux diagnostics agent-hook`, `projmux diagnostics report`

### `projmux diagnostics log`

Read the bounded local operations journal

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux diagnostics log
```

### `projmux diagnostics agent-hook`

Read the bounded Agent hook ingest journal

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux diagnostics agent-hook [--tail <n>] [--json] [--path]
```

### `projmux diagnostics report`

Create an explicit redacted local support report

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux diagnostics report
```

## `projmux focus`

Move the current client to a live resource

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux focus project <ref>
projmux focus window <ref> {--project <ref> | -p <ref>}
projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>}
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux focus project`](#projmux-focus-project) | Move the current client to an already-live Project; never materializes |
| [`projmux focus window`](#projmux-focus-window) | Move the current client to an already-live Window in an exact live root session; never materializes |
| [`projmux focus pane`](#projmux-focus-pane) | Move the current client to an already-live Pane in an exact live root session; never materializes |

Canonical spelling: `projmux focus project`, `projmux focus window`, `projmux focus pane`

### `projmux focus project`

Move the current client to an already-live Project; never materializes

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=moved-current-client`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux focus project <ref> [--socket <path>] [--client <tty>] [--json]
```

### `projmux focus window`

Move the current client to an already-live Window in an exact live root session; never materializes

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=moved-current-client`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux focus window <ref> {--project <ref> | -p <ref>} [--socket <path>] [--client <tty>] [--json]
```

### `projmux focus pane`

Move the current client to an already-live Pane in an exact live root session; never materializes

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=moved-current-client`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>} [--socket <path>] [--json]
```

## `projmux get`

Read Projmux resources by selector

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]
projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
projmux get pane --current -o cwd
projmux get pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux get projects`](#projmux-get-projects) | List Project resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context |
| [`projmux get windows`](#projmux-get-windows) | List Window resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get panes`](#projmux-get-panes) | List Pane resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get agents`](#projmux-get-agents) | List Agent resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get runtime`](#projmux-get-runtime) | List every tmux Session, Window, and Pane on one exact server with its attribution |
| [`projmux get notifications`](#projmux-get-notifications) | List pending notification rows |
| [`projmux get snapshots`](#projmux-get-snapshots) | List saved session snapshots |
| [`projmux get pane`](#projmux-get-pane) | Read one Pane resource; with no selector inside tmux, the active Pane |

Canonical spelling: `projmux get projects`, `projmux get windows`, `projmux get panes`, `projmux get agents`, `projmux get runtime sessions`, `projmux get runtime windows`, `projmux get runtime panes`, `projmux get notifications`, `projmux get snapshots`, `projmux get pane`

### `projmux get projects`

List Project resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]
```

Aliases: `project`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `wide`

### `projmux get windows`

List Window resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
```

Aliases: `window`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `wide`

### `projmux get panes`

List Pane resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `wide`

### `projmux get agents`

List Agent resources as NAME STATUS ACTIONS; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]
```

Aliases: `agent`

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `wide`

### `projmux get runtime`

List every tmux Session, Window, and Pane on one exact server with its attribution

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux get runtime sessions`](#projmux-get-runtime-sessions) | List every tmux session on one exact server with its attribution |
| [`projmux get runtime windows`](#projmux-get-runtime-windows) | List every tmux window on one exact server with its attribution |
| [`projmux get runtime panes`](#projmux-get-runtime-panes) | List every tmux pane on one exact server with its attribution |

Canonical spelling: `projmux get runtime sessions`, `projmux get runtime windows`, `projmux get runtime panes`

#### `projmux get runtime sessions`

List every tmux session on one exact server with its attribution

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Output modes (`-o`): `wide`, `json`, `none`

#### `projmux get runtime windows`

List every tmux window on one exact server with its attribution

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Output modes (`-o`): `wide`, `json`, `none`

#### `projmux get runtime panes`

List every tmux pane on one exact server with its attribution

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Output modes (`-o`): `wide`, `json`, `none`

### `projmux get notifications`

List pending notification rows

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get notifications
```

Aliases: `notification`

### `projmux get snapshots`

List saved session snapshots

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux get snapshots
```

Aliases: `snapshot`

### `projmux get pane`

Read one Pane resource; with no selector inside tmux, the active Pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux get pane [--current] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

Field projections (`-o`): `cwd`

## `projmux hook`

List, edit, validate, and trust lifecycle hook config

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook list|edit|validate|trust|untrust
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux hook list`](#projmux-hook-list) | List global and project lifecycle hooks |
| [`projmux hook edit`](#projmux-hook-edit) | Edit lifecycle hook config |
| [`projmux hook validate`](#projmux-hook-validate) | Validate lifecycle hook config |
| [`projmux hook trust`](#projmux-hook-trust) | Trust the current project hook config |
| [`projmux hook untrust`](#projmux-hook-untrust) | Revoke project hook config trust |

Canonical spelling: `projmux hook list`, `projmux hook edit`, `projmux hook validate`, `projmux hook trust`, `projmux hook untrust`

### `projmux hook list`

List global and project lifecycle hooks

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook list
```

### `projmux hook edit`

Edit lifecycle hook config

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook edit
```

### `projmux hook validate`

Validate lifecycle hook config

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook validate
```

### `projmux hook trust`

Trust the current project hook config

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook trust
```

### `projmux hook untrust`

Revoke project hook config trust

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux hook untrust
```

## `projmux notification`

Manage pending notification workflow state

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux notification ack <id> | --all
projmux notification reconcile [--json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux notification ack`](#projmux-notification-ack) | Acknowledge notification rows |
| [`projmux notification reconcile`](#projmux-notification-reconcile) | Reconcile the notification queue against live targets |

Canonical spelling: `projmux notification ack`, `projmux notification reconcile`

### `projmux notification ack`

Acknowledge notification rows

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux notification ack <id> | --all
```

### `projmux notification reconcile`

Reconcile the notification queue against live targets

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux notification reconcile [--json]
```

## `projmux open`

Open a Project runtime and move the current client to it

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux open project <ref> [-o receipt|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux open project`](#projmux-open-project) | Materialize an offline Project runtime when needed and move the current tmux client to it; outside tmux it refuses and points at attach |

Canonical spelling: `projmux open project`

### `projmux open project`

Materialize an offline Project runtime when needed and move the current tmux client to it; outside tmux it refuses and points at attach

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=materialized|already-live`
- `focus=moved-current-client`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux open project <ref> [-o receipt|none]
```

Output modes (`-o`): `receipt`, `none`

## `projmux pin`

Manage pinned project directories

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux pin project list|add|remove|toggle|clear
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux pin project`](#projmux-pin-project) | Manage pinned project directories (canonical spelling) |

Canonical spelling: `projmux pin project`

### `projmux pin project`

Manage pinned project directories (canonical spelling)

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux pin project list|add|remove|toggle|clear
```

## `projmux prune`

Prune stale Projects and snapshots

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux prune snapshot [--older-than <duration>]
projmux prune project --missing --older-than <duration> [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux prune project`](#projmux-prune-project) | Delete Projects whose spec.root has been missing for a bounded age |
| [`projmux prune snapshot`](#projmux-prune-snapshot) | Inspect or delete preserved session snapshots (canonical spelling) |

Canonical spelling: `projmux prune project`, `projmux prune snapshot`

### `projmux prune project`

Delete Projects whose spec.root has been missing for a bounded age

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux prune project --missing --older-than <duration> [--yes]
```

### `projmux prune snapshot`

Inspect or delete preserved session snapshots (canonical spelling)

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux prune snapshot [--older-than <duration>]
projmux prune snapshot delete <session>...
```

Canonical spelling: `projmux delete snapshot`

## `projmux quit`

Quit the app-owned projmux tmux runtime

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=stopped`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux quit [--yes|--force]
```

## `projmux reconcile`

Preview or repair Registry and exact tmux resource drift

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]
projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux reconcile resources`](#projmux-reconcile-resources) | Preview or repair exact anchor-aware Registry and tmux topology on one exact socket |
| [`projmux reconcile registry`](#projmux-reconcile-registry) | Plan Registry state-loss recovery with zero writes, then restore one explicitly named verified source |

Canonical spelling: `projmux reconcile resources`, `projmux reconcile registry`

### `projmux reconcile resources`

Preview or repair exact anchor-aware Registry and tmux topology on one exact socket

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged|created`
- `address=unchanged|allocated`
- `topology=unchanged|established|reparented`
- `desired-state=unchanged|created|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged`
- `cardinality=exact-one|zero-or-more`
- `domain-effect=null`

```
projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]
```

### `projmux reconcile registry`

Plan Registry state-loss recovery with zero writes, then restore one explicitly named verified source

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|created|removed|replaced`
- `address=unchanged|allocated|renamed|released`
- `topology=unchanged|established|reparented|removed|replaced`
- `desired-state=unchanged|created|removed|replaced`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]
```

## `projmux rebind`

Rebind a Project to a new absolute root without moving files

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux rebind project`](#projmux-rebind-project) | Rewrite one Project spec.root; no filesystem move, no heuristic uid merge |

Canonical spelling: `projmux rebind project`

### `projmux rebind project`

Rewrite one Project spec.root; no filesystem move, no heuristic uid merge

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=replaced`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>
```

## `projmux rename`

Rename a Projmux resource metadata.name

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>
projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]
projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...
projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux rename project`](#projmux-rename-project) | Rename a Projmux Project resource; with no selector inside tmux, the active Project |
| [`projmux rename window`](#projmux-rename-window) | Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window |
| [`projmux rename pane`](#projmux-rename-pane) | Rename a Projmux Pane resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane; does not change tmux pane_title |
| [`projmux rename agent`](#projmux-rename-agent) | Rename an Agent stable resource name within the active Project or ControlSession without changing its topic, provider, or managed Pane |

Canonical spelling: `projmux rename project`, `projmux rename window`, `projmux rename pane`, `projmux rename agent`

### `projmux rename project`

Rename a Projmux Project resource; with no selector inside tmux, the active Project

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=renamed`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>
```

Aliases: `projects`

Output modes (`-o`): `receipt`, `none`

### `projmux rename window`

Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=renamed`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]
```

Aliases: `windows`

Output modes (`-o`): `receipt`, `none`

### `projmux rename pane`

Rename a Projmux Pane resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane; does not change tmux pane_title

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=renamed`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...
```

Aliases: `panes`

Output modes (`-o`): `receipt`, `none`

### `projmux rename agent`

Rename an Agent stable resource name within the active Project or ControlSession without changing its topic, provider, or managed Pane

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=renamed`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...
```

Aliases: `agents`

Output modes (`-o`): `receipt`, `none`

## `projmux resources`

Inspect live Project, Window, and Pane CPU/RSS attribution

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux resources
```

## `projmux restore`

Project a saved snapshot into one exact closed Project desired state

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux restore snapshot`](#projmux-restore-snapshot) | Project a saved snapshot into one exact closed Project desired state |

Canonical spelling: `projmux restore snapshot`

### `projmux restore snapshot`

Project a saved snapshot into one exact closed Project desired state

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|created|reused|removed|replaced`
- `address=unchanged|allocated|released`
- `topology=unchanged|established|removed|replaced`
- `desired-state=unchanged|created|removed|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged|moved-current-client|attached-caller`
- `cardinality=unchanged|one-or-more`
- `domain-effect=null`

```
projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]
```

## `projmux runtime`

Manage the live and ephemeral tmux runtime inventory

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux runtime sessions [--ui=popup|sidebar]
projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]
projmux runtime attach [--keep=N] [--fallback=home|ephemeral]
projmux runtime stop [<session>...]
projmux runtime tag list|toggle|clear
projmux runtime prune [--keep=N]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux runtime sessions`](#projmux-runtime-sessions) | Pick a live or ephemeral tmux session |
| [`projmux runtime diagnostics`](#projmux-runtime-diagnostics) | Inspect every tmux object on one exact server, with attribution and safe actions |
| [`projmux runtime attach`](#projmux-runtime-attach) | Attach a live or ephemeral runtime without Project identity |
| [`projmux runtime stop`](#projmux-runtime-stop) | Terminate live tmux sessions by tagged selection |
| [`projmux runtime tag`](#projmux-runtime-tag) | Manage the ephemeral tagged session selection |
| [`projmux runtime prune`](#projmux-runtime-prune) | Trim old ephemeral tmux sessions |

Canonical spelling: `projmux runtime sessions`, `projmux runtime diagnostics`, `projmux runtime attach`, `projmux runtime stop`, `projmux runtime tag`, `projmux runtime prune`

### `projmux runtime sessions`

Pick a live or ephemeral tmux session

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged|materialized|already-live|stopped`
- `focus=unchanged|moved-current-client|attached-caller`
- `cardinality=unchanged|exact-one`
- `domain-effect=null`

```
projmux runtime sessions
```

### `projmux runtime diagnostics`

Inspect every tmux object on one exact server, with attribution and safe actions

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged|materialized|already-live`
- `focus=unchanged|moved-current-client|attached-caller`
- `cardinality=unchanged|exact-one`
- `domain-effect=null`

```
projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]
```

### `projmux runtime attach`

Attach a live or ephemeral runtime without Project identity

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=already-live`
- `focus=attached-caller`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux runtime attach
```

### `projmux runtime stop`

Terminate live tmux sessions by tagged selection

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=stopped`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux runtime stop
```

### `projmux runtime tag`

Manage the ephemeral tagged session selection

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux runtime tag
```

### `projmux runtime prune`

Trim old ephemeral tmux sessions

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=stopped`
- `focus=unchanged`
- `cardinality=zero-or-more`
- `domain-effect=null`

```
projmux runtime prune
```

## `projmux settings`

Configure projmux

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux settings
```

## `projmux setup`

Probe terminal keys or remediate them with setup terminal

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux setup
projmux setup terminal [terminal] [--apply]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux setup terminal`](#projmux-setup-terminal) | Show or apply terminal key remediation |

Canonical spelling: `projmux setup terminal`

### `projmux setup terminal`

Show or apply terminal key remediation

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]
```

## `projmux shell`

Open the isolated projmux tmux app

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged|created|reused`
- `address=unchanged|allocated`
- `topology=unchanged|established`
- `desired-state=unchanged|created|reused`
- `runtime=materialized|already-live`
- `focus=attached-caller`
- `cardinality=exact-one|one-or-more`
- `domain-effect=null`

```
projmux shell [--session <name>]
```

## `projmux start`

Start a Project runtime without moving any client

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux start project <ref> [-o receipt|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux start project`](#projmux-start-project) | Materialize an offline Project runtime detached; no client is moved and no Registry identity changes |

Canonical spelling: `projmux start project`

### `projmux start project`

Materialize an offline Project runtime detached; no client is moved and no Registry identity changes

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=materialized|already-live`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux start project <ref> [-o receipt|none]
```

Output modes (`-o`): `receipt`, `none`

## `projmux stop`

Stop a Project runtime without unregistering anything

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux stop project <ref> [-o receipt|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux stop project`](#projmux-stop-project) | Terminate the exact persistent tmux session of a Project; the Registry graph, root, and external assets are preserved |

Canonical spelling: `projmux stop project`

### `projmux stop project`

Terminate the exact persistent tmux session of a Project; the Registry graph, root, and external assets are preserved

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=stopped`
- `focus=unchanged|moved-current-client`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux stop project <ref> [-o receipt|none]
```

Output modes (`-o`): `receipt`, `none`

## `projmux switch`

Pick a project and compose create project with open project

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged|created|reused|replaced`
- `address=unchanged|allocated|released`
- `topology=unchanged|established|replaced`
- `desired-state=unchanged|created|reused|replaced`
- `runtime=unchanged|materialized|already-live|stopped`
- `focus=unchanged|moved-current-client|attached-caller`
- `cardinality=unchanged|exact-one`
- `domain-effect=null`

```
projmux switch [<project>]
```

Canonical spelling: `projmux create project`, `projmux open project`

## `projmux unregister`

Unregister Projects from the Registry while preserving runtime and files

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux unregister project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux unregister project`](#projmux-unregister-project) | Unregister Projects and their Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime |

Canonical spelling: `projmux unregister project`

### `projmux unregister project`

Unregister Projects and their Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=removed`
- `address=released`
- `topology=removed`
- `desired-state=removed`
- `runtime=preserved`
- `focus=unchanged`
- `cardinality=one-or-more`
- `domain-effect=null`

```
projmux unregister project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]
```

Aliases: `projects`

## `projmux update`

Check installer-aware release update status

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux update status|check|apply
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux update status`](#projmux-update-status) | Show read-only update status |
| [`projmux update check`](#projmux-update-check) | Check for a newer release and refresh the cache |
| [`projmux update apply`](#projmux-update-apply) | Apply an available update |

Canonical spelling: `projmux update status`, `projmux update check`, `projmux update apply`

### `projmux update status`

Show read-only update status

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux update status
```

### `projmux update check`

Check for a newer release and refresh the cache

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux update check
```

### `projmux update apply`

Apply an available update

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux update apply
```

## `projmux welcome`

Reprint the shell welcome guide

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux welcome [--popup [--force]]
```

## `projmux window`

Open recent window navigation surfaces

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux window record|recent
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux window record`](#projmux-window-record) | Record the current window into the MRU store |
| [`projmux window recent`](#projmux-window-recent) | Open the recent-window navigation picker |

Canonical spelling: `projmux get windows`, `projmux describe window`, `projmux create window`, `projmux focus window`, `projmux rename window`

### `projmux window record`

Record the current window into the MRU store

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux window record
```

Canonical spelling: `projmux get windows`

### `projmux window recent`

Open the recent-window navigation picker

Selectorless authority: `natural-omitted` — omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged|materialized|already-live`
- `focus=unchanged|moved-current-client|attached-caller`
- `cardinality=unchanged|exact-one`
- `domain-effect=null`

```
projmux window recent
```

Canonical spelling: `projmux get windows`

## `projmux help`

Show bootstrap help

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux help
projmux --help
projmux <route> --help
```

## `projmux version`

Print the current version

Selectorless authority: `explicit-fan-out` — the route spelling is an intentional global or whole-set opt-in.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=null`

```
projmux version
projmux --version
```
