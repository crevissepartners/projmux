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

The machine-readable manifest contains 211 route-effect records, including hidden plumbing that the public route sections omit.

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
| [`projmux agent`](#projmux-agent) | canonical | Manage Agent state, messages, waits, capabilities, integrations, and account usage |
| [`projmux attention`](#projmux-attention) | canonical | View and manage live tmux pane attention state |
| [`projmux attach`](#projmux-attach) | canonical | Enter a Project runtime from outside tmux |
| [`projmux config`](#projmux-config) | canonical | Edit AI split-mode, enabled-provider, locale, and agent-question settings; render or apply generated tmux configuration |
| [`projmux create`](#projmux-create) | canonical | Create Projmux resources |
| [`projmux delete`](#projmux-delete) | canonical | Delete Projmux resources with an explicit cascade plan |
| [`projmux describe`](#projmux-describe) | canonical | Describe one Projmux resource |
| [`projmux doctor`](#projmux-doctor) | shortcut | Run read-only runtime and integration diagnostics |
| [`projmux diagnostics`](#projmux-diagnostics) | canonical | Read operational events or create an explicit local support report |
| [`projmux focus`](#projmux-focus) | canonical | Move the current client to a live resource |
| [`projmux get`](#projmux-get) | canonical | Read Projmux resources by selector |
| [`projmux hook`](#projmux-hook) | canonical | List, edit, validate, and trust lifecycle hook config |
| [`projmux label`](#projmux-label) | canonical | Set or remove Projmux resource metadata.labels after creation |
| [`projmux notification`](#projmux-notification) | canonical | Manage pending notification workflow state |
| [`projmux open`](#projmux-open) | canonical | Open a Project runtime and move the current client to it |
| [`projmux instructions`](#projmux-instructions) | canonical | List, show, edit, set, and delete Agent instruction files |
| [`projmux persona`](#projmux-persona) | compatibility | List, show, edit, set, and delete Agent persona files |
| [`projmux profile`](#projmux-profile) | canonical | List, show, set, and delete named Agent profiles |
| [`projmux pin`](#projmux-pin) | canonical | Manage pinned project directories |
| [`projmux prune`](#projmux-prune) | canonical | Prune stale Projects and Agents |
| [`projmux quit`](#projmux-quit) | shortcut | Quit the app-owned projmux tmux runtime |
| [`projmux reconcile`](#projmux-reconcile) | canonical | Preview or repair Registry and exact tmux resource drift |
| [`projmux rebind`](#projmux-rebind) | canonical | Rebind a Project to a new absolute root without moving files |
| [`projmux rename`](#projmux-rename) | canonical | Rename a Projmux resource metadata.name |
| [`projmux resources`](#projmux-resources) | shortcut | Inspect live Project, Window, and Pane CPU/RSS attribution |
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

Manage Agent state, messages, waits, capabilities, integrations, and account usage

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
projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--dialogue-reply-only]
projmux agent persona attach <agent-ref> <persona> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
projmux agent persona detach <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
projmux agent turn start|steer <agent-ref> -- <text>
projmux agent turn interrupt <agent-ref>
projmux agent approval review <agent-ref> [--request <normalized-id>]
projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]
projmux agent integrate <codex|claude|antigravity|tmux-bell> [--remove] [--dry-run]
projmux agent usage [--model <codex|claude|all>] [--window <name>] [--json] [--force]
projmux agent capabilities [<agent-ref> | --provider <codex|claude|antigravity>] [-o json] [--json]
projmux agent models [--provider claude] [-o json]
projmux agent message send <agent-ref> [--source <agent-ref>] [--message-ref <ref>] [--reply-to <ref>] [--ttl <duration>] -- <text>
projmux agent message status <message-ref> [-o json]
projmux agent message qualify <claude-agent-ref> --evidence <absolute-private-json> --confirm-isolated-provider-push -o json [--timeout <duration>]
projmux agent wait <agent-ref> [--until idle] [--timeout <duration>] [-o json]
projmux agent question enable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
projmux agent question disable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
projmux agent question list <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o json]
projmux agent question answer <agent-ref> <question-id> [--option <n>=<label>]... [--index <n>=<k>]... [--text <n>=<text>]... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent status`](#projmux-agent-status) | Read or set semantic Agent interaction independently of lifecycle |
| [`projmux agent topic`](#projmux-agent-topic) | Read, set, or clear one exact Agent topic annotation |
| [`projmux agent resume`](#projmux-agent-resume) | Rebind an Offline or Failed Agent detached on its Window's exact shell or Agent anchor |
| [`projmux agent instructions`](#projmux-agent-instructions) | Attach or detach an instruction on one exact Claude Agent and resume it on the same conversation |
| [`projmux agent persona`](#projmux-agent-persona) | Attach or detach a persona on one exact Claude Agent and resume it on the same conversation |
| [`projmux agent turn`](#projmux-agent-turn) | Send, steer, or interrupt one exact native Codex turn |
| [`projmux agent approval`](#projmux-agent-approval) | Review one exact pending native Codex approval |
| [`projmux agent review`](#projmux-agent-review) | Start a native review on an exact-bound Codex Agent |
| [`projmux agent integrate`](#projmux-agent-integrate) | Install, remove, or preview provider hooks and tmux-bell integration |
| [`projmux agent usage`](#projmux-agent-usage) | Read provider account usage quota snapshots |
| [`projmux agent capabilities`](#projmux-agent-capabilities) | Read static provider support or one exact Agent's Registry-backed runtime eligibility |
| [`projmux agent models`](#projmux-agent-models) | List the Claude model names projmux suggests for --model; other names are still accepted |
| [`projmux agent message`](#projmux-agent-message) | Exchange bounded untrusted coordination messages; --source selects a source Agent anchor, not caller authentication (default: active Pane) |
| [`projmux agent wait`](#projmux-agent-wait) | Wait read-only for one exact Agent's Registry-backed idle observation |
| [`projmux agent question`](#projmux-agent-question) | Answer one exact opted-in Claude Agent's AskUserQuestion prompts from the command line |

Canonical spelling: `projmux agent status`, `projmux agent topic`, `projmux agent resume`, `projmux agent instructions attach`, `projmux agent instructions detach`, `projmux agent turn start`, `projmux agent turn steer`, `projmux agent turn interrupt`, `projmux agent approval review`, `projmux agent review`, `projmux agent integrate`, `projmux agent usage`, `projmux agent capabilities`, `projmux agent models`, `projmux agent message send`, `projmux agent message status`, `projmux agent message qualify`, `projmux agent wait`, `projmux agent question enable`, `projmux agent question disable`, `projmux agent question list`, `projmux agent question answer`

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
projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--dialogue-reply-only]
```

### `projmux agent instructions`

Attach or detach an instruction on one exact Claude Agent and resume it on the same conversation

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
projmux agent instructions attach <agent-ref> <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
projmux agent instructions detach <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent instructions attach`](#projmux-agent-instructions-attach) | Give one exact Claude Agent an instruction and restart it on the same conversation |
| [`projmux agent instructions detach`](#projmux-agent-instructions-detach) | Take the instructions off one exact Claude Agent and restart it on the same conversation |

Canonical spelling: `projmux agent instructions attach`, `projmux agent instructions detach`

#### `projmux agent instructions attach`

Give one exact Claude Agent an instruction and restart it on the same conversation

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|reused`
- `address=unchanged`
- `topology=unchanged|replaced`
- `desired-state=unchanged|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent instructions attach <agent-ref> <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Output modes (`-o`): `json`

#### `projmux agent instructions detach`

Take the instructions off one exact Claude Agent and restart it on the same conversation

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|reused`
- `address=unchanged`
- `topology=unchanged|replaced`
- `desired-state=unchanged|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent instructions detach <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Output modes (`-o`): `json`

### `projmux agent persona`

Attach or detach a persona on one exact Claude Agent and resume it on the same conversation

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
projmux agent persona attach <agent-ref> <persona> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
projmux agent persona detach <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent persona attach`](#projmux-agent-persona-attach) | Give one exact Claude Agent a persona and restart it on the same conversation |
| [`projmux agent persona detach`](#projmux-agent-persona-detach) | Take the persona off one exact Claude Agent and restart it on the same conversation |

Canonical spelling: `projmux agent instructions attach`, `projmux agent instructions detach`

#### `projmux agent persona attach`

Give one exact Claude Agent a persona and restart it on the same conversation

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|reused`
- `address=unchanged`
- `topology=unchanged|replaced`
- `desired-state=unchanged|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent persona attach <agent-ref> <persona> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Output modes (`-o`): `json`

Canonical spelling: `projmux agent instructions attach`

#### `projmux agent persona detach`

Take the persona off one exact Claude Agent and restart it on the same conversation

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged|reused`
- `address=unchanged`
- `topology=unchanged|replaced`
- `desired-state=unchanged|replaced`
- `runtime=unchanged|materialized`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=null`

```
projmux agent persona detach <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--yes] [--dry-run] [--socket <name> | --socket-path <absolute>] [-o json]
```

Output modes (`-o`): `json`

Canonical spelling: `projmux agent instructions detach`

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
projmux agent usage [--model <codex|claude|all>] [--window <name>] [--json] [--force]
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
projmux agent capabilities [<agent-ref> | --provider <codex|claude|antigravity>] [-o json] [--json]
```

Output modes (`-o`): `json`

### `projmux agent models`

List the Claude model names projmux suggests for --model; other names are still accepted

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
projmux agent models [--provider claude] [-o json]
```

Output modes (`-o`): `json`

### `projmux agent message`

Exchange bounded untrusted coordination messages; --source selects a source Agent anchor, not caller authentication (default: active Pane)

Selectorless authority: `refusal` — there is no safe selectorless action; refuse before output or mutation.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=agent-delivery`

```
projmux agent message send <agent-ref> [--source <agent-ref>] [--message-ref <ref>] [--reply-to <ref>] [--ttl <duration>] -- <text>
projmux agent message status <message-ref> [-o json]
projmux agent message qualify <claude-agent-ref> --evidence <absolute-private-json> --confirm-isolated-provider-push -o json [--timeout <duration>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent message send`](#projmux-agent-message-send) | Submit bounded peer coordination; --source selects a source Agent anchor, not caller authentication (default: active Pane); exits nonzero after printing a failed, refused, expired, or stale receipt |
| [`projmux agent message status`](#projmux-agent-message-status) | Read a payload-free broker delivery receipt |
| [`projmux agent message qualify`](#projmux-agent-message-qualify) | Explicitly qualify one exact Claude target using owned current-version isolation evidence and one marker push |

Canonical spelling: `projmux agent message send`, `projmux agent message status`, `projmux agent message qualify`

#### `projmux agent message send`

Submit bounded peer coordination; --source selects a source Agent anchor, not caller authentication (default: active Pane); exits nonzero after printing a failed, refused, expired, or stale receipt

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=agent-delivery`

```
projmux agent message send <agent-ref> [--source <agent-ref>] [--message-ref <ref>] [--reply-to <ref>] [--ttl <duration>] -- <text>
```

#### `projmux agent message status`

Read a payload-free broker delivery receipt

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=unchanged`
- `domain-effect=agent-delivery`

```
projmux agent message status <message-ref> [-o json]
```

Output modes (`-o`): `json`

#### `projmux agent message qualify`

Explicitly qualify one exact Claude target using owned current-version isolation evidence and one marker push

Selectorless authority: `explicit-target` — the route or caller must name the exact target.

Allowed effects:

- `identity=unchanged`
- `address=unchanged`
- `topology=unchanged`
- `desired-state=unchanged`
- `runtime=unchanged`
- `focus=unchanged`
- `cardinality=exact-one`
- `domain-effect=agent-delivery`

```
projmux agent message qualify <claude-agent-ref> --evidence <absolute-private-json> --confirm-isolated-provider-push -o json [--timeout <duration>]
```

Output modes (`-o`): `json`

### `projmux agent wait`

Wait read-only for one exact Agent's Registry-backed idle observation

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
projmux agent wait <agent-ref> [--until idle] [--timeout <duration>] [-o json]
```

Output modes (`-o`): `json`

### `projmux agent question`

Answer one exact opted-in Claude Agent's AskUserQuestion prompts from the command line

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
projmux agent question enable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
projmux agent question disable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
projmux agent question list <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o json]
projmux agent question answer <agent-ref> <question-id> [--option <n>=<label>]... [--index <n>=<k>]... [--text <n>=<text>]... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent question enable`](#projmux-agent-question-enable) | Opt one exact Claude Agent into answering its AskUserQuestion prompts from the command line |
| [`projmux agent question disable`](#projmux-agent-question-disable) | Opt one exact Claude Agent out and hand its waiting questions back to its own prompt |
| [`projmux agent question list`](#projmux-agent-question-list) | List one exact Claude Agent's waiting and recent questions |
| [`projmux agent question answer`](#projmux-agent-question-answer) | Answer one waiting question by option label, option number, or explicit free text |

Canonical spelling: `projmux agent question enable`, `projmux agent question disable`, `projmux agent question list`, `projmux agent question answer`

#### `projmux agent question enable`

Opt one exact Claude Agent into answering its AskUserQuestion prompts from the command line

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
projmux agent question enable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

#### `projmux agent question disable`

Opt one exact Claude Agent out and hand its waiting questions back to its own prompt

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
projmux agent question disable <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

#### `projmux agent question list`

List one exact Claude Agent's waiting and recent questions

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
projmux agent question list <agent-ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o json]
```

Output modes (`-o`): `json`

#### `projmux agent question answer`

Answer one waiting question by option label, option number, or explicit free text

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
projmux agent question answer <agent-ref> <question-id> [--option <n>=<label>]... [--index <n>=<k>]... [--text <n>=<text>]... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

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
projmux attention list [--json] [--all]
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
projmux attention window [window] [style]
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

Edit AI split-mode, enabled-provider, locale, and agent-question settings; render or apply generated tmux configuration

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
projmux config providers [--enable <id>|--disable <id>]
projmux config locale [--set <value>]
projmux config agent-questions [--answering <claude|projmux>] [--window <seconds|unlimited>]
projmux config render standalone|app [--bin <path>]
projmux config apply [--bin <path>] [--config <path>] [--socket <name>] [--no-reload]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux config edit`](#projmux-config-edit) | Edit the AI split-mode configuration |
| [`projmux config providers`](#projmux-config-providers) | List AI providers as enabled or disabled; --enable or --disable changes one |
| [`projmux config locale`](#projmux-config-locale) | Show the [ui] locale setting and its config.toml; --set stores a new one |
| [`projmux config agent-questions`](#projmux-config-agent-questions) | Show how Claude agent questions are answered and how long they wait; --answering or --window changes them |
| [`projmux config render`](#projmux-config-render) | Print a generated tmux config to stdout; writes nothing |
| [`projmux config apply`](#projmux-config-apply) | Write the generated app tmux config and reload the live projmux server |

Canonical spelling: `projmux config edit`, `projmux config providers`, `projmux config locale`, `projmux config agent-questions`, `projmux config render`, `projmux config apply`

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

`--get` prints the TUI split default `tmux-ai-split-mode` when it holds a valid mode, else the central `ai-new-window-mode`, else `selective`. `--set` writes only `tmux-ai-split-mode`.

### `projmux config providers`

List AI providers as enabled or disabled; --enable or --disable changes one

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
projmux config providers
projmux config providers --enable <id>
projmux config providers --disable <id>
```

### `projmux config locale`

Show the [ui] locale setting and its config.toml; --set stores a new one

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
projmux config locale
projmux config locale --set <value>
```

### `projmux config agent-questions`

Show how Claude agent questions are answered and how long they wait; --answering or --window changes them

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
projmux config agent-questions
projmux config agent-questions --answering <claude|projmux>
projmux config agent-questions --window <seconds|unlimited>
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
projmux config apply [--bin <path>] [--config <path>] [--socket <name>] [--no-reload]
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
projmux create window [--project <ref> | -p <ref>] [--provider shell|<provider>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--model <model>] [--effort <level>] [--instructions <name> | --persona <name>] [--profile <name>] [--dialogue-reply-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--instructions <name> | --persona <name>] [--profile <name>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
projmux create claude [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--model <model>] [--effort <level>] [--instructions <name> | --persona <name>] [--profile <name>] [--dialogue-reply-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
projmux create antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--profile <name>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>] [--severity info|warn|critical] [--source <source>] [--ttl <seconds>] [--id <id>] [--json]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux create project`](#projmux-create-project) | Register one exact filesystem path as a Registry Project; no runtime is materialized |
| [`projmux create window`](#projmux-create-window) | Create a Window below one Project, opening on a shell Pane or on one Agent; the runtime is materialized detached |
| [`projmux create pane`](#projmux-create-pane) | Create a shell Pane detached on an explicit Pane or the Window's exact shell or Agent anchor |
| [`projmux create agent`](#projmux-create-agent) | Create an Agent detached on an explicit Pane or the Window's exact shell or Agent anchor; --provider is required |
| [`projmux create notification`](#projmux-create-notification) | Create a pending notification row |

Provider shortcuts:

| Route | Summary |
| --- | --- |
| [`projmux create codex`](#projmux-create-codex) | Provider shortcut for create agent --provider codex |
| [`projmux create claude`](#projmux-create-claude) | Provider shortcut for create agent --provider claude |
| [`projmux create antigravity`](#projmux-create-antigravity) | Provider shortcut for create agent --provider antigravity |

Canonical spelling: `projmux create project`, `projmux create window`, `projmux create pane`, `projmux create agent`, `projmux create notification`, `projmux create codex`, `projmux create claude`, `projmux create antigravity`

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

Without `--name` a new Project is named after its root directory, sanitized into a valid name (`my repo` becomes `my-repo`). When that basename is empty (the filesystem root) or another Project already holds it, the Project is named by its exact uid instead; no numbered variant is ever invented.

An explicit `--name` that another Project holds exits 2 with no Registry write. A root that is already registered reuses its Project; an explicit `--name` there must match the stored name.

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`, `receipt`

### `projmux create window`

Create a Window below one Project, opening on a shell Pane or on one Agent; the runtime is materialized detached

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
projmux create window [--project <ref> | -p <ref>] [--provider shell|<provider>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
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
projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
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
projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--model <model>] [--effort <level>] [--instructions <name> | --persona <name>] [--profile <name>] [--dialogue-reply-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
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
projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>] [--severity info|warn|critical] [--source <source>] [--ttl <seconds>] [--id <id>] [--json]
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
projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--instructions <name> | --persona <name>] [--profile <name>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
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
projmux create claude [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--model <model>] [--effort <level>] [--instructions <name> | --persona <name>] [--profile <name>] [--dialogue-reply-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
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
projmux create antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--profile <name>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--all-windows | --primary-window] [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]
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
projmux delete project [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
projmux delete notification <id> | --all
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux delete project`](#projmux-delete-project) | Deprecated alias of unregister project; unregisters Projects and Registry descendants while preserving roots, Git/worktrees, and runtime |
| [`projmux delete window`](#projmux-delete-window) | Delete Registry Windows and every descendant Agent and Pane, killing an exact live tmux mirror when present; no selector inside tmux means the active Window, and --all means every Window in the registry |
| [`projmux delete pane`](#projmux-delete-pane) | Delete Panes; an Agent-owned current Pane leaves its Agent Offline; no selector inside tmux means the active Pane, and --all means every Pane in the registry |
| [`projmux delete agent`](#projmux-delete-agent) | Delete Agents and their managed Panes; no selector inside tmux means the active Agent, and --all means every Agent in the registry |
| [`projmux delete notification`](#projmux-delete-notification) | Delete pending notification rows |

Canonical spelling: `projmux unregister project`, `projmux delete window`, `projmux delete pane`, `projmux delete agent`, `projmux delete notification`

### `projmux delete project`

Deprecated alias of unregister project; unregisters Projects and Registry descendants while preserving roots, Git/worktrees, and runtime

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
projmux delete project [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
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
projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
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
projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
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
projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
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
projmux delete notification <id> | --all
```

Aliases: `notifications`

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
projmux describe project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]
projmux describe window [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
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
projmux describe project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]
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
projmux describe window [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
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
projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
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
projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
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
projmux diagnostics log [--json] [--tail <n>] [--level info|error] [--component <name>] [--path]
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
projmux diagnostics log [--json] [--tail <n>] [--level info|error] [--component <name>] [--path]
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
projmux diagnostics report [--output <path>]
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
projmux focus project <ref> [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus window <ref> {--project <ref> | -p <ref>} [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus window uid:<uid> [--project <ref> | -p <ref>] [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>} [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus pane uid:<uid> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>] [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
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
projmux focus project <ref> [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
```

`<ref>` is the live tmux session name, or `uid:<uid>` for a Registry Project, which resolves to its `status.session.name`. A `uid:` ref uses the Project's recorded socket; an explicit `--socket` naming another server exits 2. The resolved session must still be live.

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
projmux focus window <ref> {--project <ref> | -p <ref>} [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus window uid:<uid> [--project <ref> | -p <ref>] [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
```

A plain `<ref>` is a live window name or `@id` and requires `--project`. `uid:<uid>` names a Registry Window, which resolves to its `status.runtimeID` inside its owning Project's session, so `--project` is optional; when given it must be that Project (`uid:` or its session name) or the route exits 2. `--project uid:<uid>` also works with a plain `<ref>`.

A `uid:` resolution uses the Project's recorded socket; an explicit `--socket` naming another server exits 2. The resolved window must still be live.

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
projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>} [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
projmux focus pane uid:<uid> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>] [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>] [--json]
```

A plain `<ref>` is a live pane name or `%id` and requires `--project` and `--window`. `uid:<uid>` names a Registry Pane, which resolves to its `status.activation.runtimeID` inside its owning Window and Project, so both flags are optional; when given they must match that owner chain or the route exits 2. `--project` and `--window` also accept `uid:<uid>` with a plain `<ref>`.

A `uid:` resolution uses the Project's recorded socket; an explicit `--socket` naming another server exits 2. The resolved pane must still be live.

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
projmux get pane [--current] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o wide|json|none]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux get projects`](#projmux-get-projects) | List Project resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context |
| [`projmux get windows`](#projmux-get-windows) | List Window resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get panes`](#projmux-get-panes) | List Pane resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get agents`](#projmux-get-agents) | List Agent resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry |
| [`projmux get runtime`](#projmux-get-runtime) | List every tmux Session, Window, and Pane on one exact server with its attribution |
| [`projmux get notifications`](#projmux-get-notifications) | List pending notification rows |
| [`projmux get pane`](#projmux-get-pane) | Read one Pane resource; with no selector inside tmux, the active Pane |

Canonical spelling: `projmux get projects`, `projmux get windows`, `projmux get panes`, `projmux get agents`, `projmux get runtime sessions`, `projmux get runtime windows`, `projmux get runtime panes`, `projmux get notifications`, `projmux get pane`

### `projmux get projects`

List Project resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context

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

List Window resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

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

List Pane resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

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

List Agent resources as NAME STATUS ACTIONS AGE; route-implied KIND is omitted, shifting stdout positions; -o wide retains KIND and diagnostics, and -o json retains kind and invocation context; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry

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
projmux get notifications [--json] [--live] [--limit <n>] [--ui table|sidebar] [--client <tty>] [--severity <severity>]... [--source <source>]...
```

Aliases: `notification`

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

Events:
  post-attach, post-create, pre-create, send-noti

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
projmux hook list [--global | --project | --effective]
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
projmux hook edit [--global | --project] [--editor] <event>
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
projmux hook trust [<project>]
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
projmux hook untrust [<project>]
```

## `projmux label`

Set or remove Projmux resource metadata.labels after creation

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
projmux label project [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--selector key=value]...
projmux label window [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
projmux label pane [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]...
projmux label agent [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux label project`](#projmux-label-project) | Set or remove Project labels; with no selector inside tmux, the active Project |
| [`projmux label window`](#projmux-label-window) | Set or remove Window labels; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window |
| [`projmux label pane`](#projmux-label-pane) | Set or remove Pane labels; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane |
| [`projmux label agent`](#projmux-label-agent) | Set or remove Agent labels within the active Project or ControlSession without changing its name, topic, provider, or managed Pane |

Canonical spelling: `projmux label project`, `projmux label window`, `projmux label pane`, `projmux label agent`

### `projmux label project`

Set or remove Project labels; with no selector inside tmux, the active Project

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
projmux label project [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--selector key=value]...
```

Aliases: `projects`

### `projmux label window`

Set or remove Window labels; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window

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
projmux label window [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

Aliases: `windows`

### `projmux label pane`

Set or remove Pane labels; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane

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
projmux label pane [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]...
```

Aliases: `panes`

### `projmux label agent`

Set or remove Agent labels within the active Project or ControlSession without changing its name, topic, provider, or managed Pane

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
projmux label agent [<ref>] <key=value|key->... [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]...
```

Aliases: `agents`

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

## `projmux instructions`

List, show, edit, set, and delete Agent instruction files

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
projmux instructions list
projmux instructions show <name>
projmux instructions edit <name>
projmux instructions set <name> [--file <path> | -]
projmux instructions delete <name> --yes
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux instructions list`](#projmux-instructions-list) | List every stored instructions with their digest, size, and modification time |
| [`projmux instructions show`](#projmux-instructions-show) | Print one instruction's content exactly as stored |
| [`projmux instructions edit`](#projmux-instructions-edit) | Edit one instruction in $EDITOR or $VISUAL, creating it when missing |
| [`projmux instructions set`](#projmux-instructions-set) | Write one instruction from a file or stdin without an editor |
| [`projmux instructions delete`](#projmux-instructions-delete) | Delete one instruction file; Agents already started with it keep their snapshot |

Canonical spelling: `projmux instructions list`, `projmux instructions show`, `projmux instructions edit`, `projmux instructions set`, `projmux instructions delete`

### `projmux instructions list`

List every stored instructions with their digest, size, and modification time

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
projmux instructions list
```

### `projmux instructions show`

Print one instruction's content exactly as stored

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
projmux instructions show <name>
```

### `projmux instructions edit`

Edit one instruction in $EDITOR or $VISUAL, creating it when missing

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
projmux instructions edit <name>
```

### `projmux instructions set`

Write one instruction from a file or stdin without an editor

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
projmux instructions set <name> [--file <path> | -]
```

### `projmux instructions delete`

Delete one instruction file; Agents already started with it keep their snapshot

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
projmux instructions delete <name> --yes
```

## `projmux persona`

List, show, edit, set, and delete Agent persona files

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
projmux persona list
projmux persona show <name>
projmux persona edit <name>
projmux persona set <name> [--file <path> | -]
projmux persona delete <name> --yes
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux persona list`](#projmux-persona-list) | List every stored persona with its digest, size, and modification time |
| [`projmux persona show`](#projmux-persona-show) | Print one persona's content exactly as stored |
| [`projmux persona edit`](#projmux-persona-edit) | Edit one persona in $EDITOR or $VISUAL, creating it when missing |
| [`projmux persona set`](#projmux-persona-set) | Write one persona from a file or stdin without an editor |
| [`projmux persona delete`](#projmux-persona-delete) | Delete one persona file; Agents already started with it keep their snapshot |

Canonical spelling: `projmux instructions list`, `projmux instructions show`, `projmux instructions edit`, `projmux instructions set`, `projmux instructions delete`

### `projmux persona list`

List every stored persona with its digest, size, and modification time

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
projmux persona list
```

Canonical spelling: `projmux instructions list`

### `projmux persona show`

Print one persona's content exactly as stored

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
projmux persona show <name>
```

Canonical spelling: `projmux instructions show`

### `projmux persona edit`

Edit one persona in $EDITOR or $VISUAL, creating it when missing

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
projmux persona edit <name>
```

Canonical spelling: `projmux instructions edit`

### `projmux persona set`

Write one persona from a file or stdin without an editor

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
projmux persona set <name> [--file <path> | -]
```

Canonical spelling: `projmux instructions set`

### `projmux persona delete`

Delete one persona file; Agents already started with it keep their snapshot

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
projmux persona delete <name> --yes
```

Canonical spelling: `projmux instructions delete`

## `projmux profile`

List, show, set, and delete named Agent profiles

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
projmux profile list
projmux profile show <name>
projmux profile set <name> [--file <path> | -]
projmux profile delete <name> --yes
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux profile list`](#projmux-profile-list) | List every built-in and stored profile with its source, roles, digest, and validity |
| [`projmux profile show`](#projmux-profile-show) | Print one profile's content exactly as stored or built in |
| [`projmux profile set`](#projmux-profile-set) | Validate one profile from a file or stdin and write it only when valid |
| [`projmux profile delete`](#projmux-profile-delete) | Delete one stored profile file; built-in profiles cannot be deleted |

Canonical spelling: `projmux profile list`, `projmux profile show`, `projmux profile set`, `projmux profile delete`

### `projmux profile list`

List every built-in and stored profile with its source, roles, digest, and validity

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
projmux profile list
```

### `projmux profile show`

Print one profile's content exactly as stored or built in

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
projmux profile show <name>
```

### `projmux profile set`

Validate one profile from a file or stdin and write it only when valid

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
projmux profile set <name> [--file <path> | -]
```

### `projmux profile delete`

Delete one stored profile file; built-in profiles cannot be deleted

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
projmux profile delete <name> --yes
```

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
projmux pin project list|add|remove|toggle|clear|migrate
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
projmux pin project list|add|remove|toggle|clear|migrate
```

Pins are presentation preferences in two kinds:
  project    a Registry Project uid; its root and name are projected from the Registry
  candidate  a filesystem path that no Registry Project claims

Discovery roots (workdirs) are a separate collection; manage them in `projmux settings`.

## `projmux prune`

Prune stale Projects and Agents

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
projmux prune project --missing --older-than <duration> [--yes]
projmux prune agent --older-than <duration> [--no-session-ref] [--no-pane] [--exclude <agent-ref>]... [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux prune agent`](#projmux-prune-agent) | Delete Offline or Failed Agents past a bounded age whose session ref or managed Panes are gone; live Panes and Running Agents are never selected |
| [`projmux prune project`](#projmux-prune-project) | Delete Projects whose spec.root has been missing for a bounded age |

Canonical spelling: `projmux prune agent`, `projmux prune project`

### `projmux prune agent`

Delete Offline or Failed Agents past a bounded age whose session ref or managed Panes are gone; live Panes and Running Agents are never selected

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
projmux prune agent --older-than <duration> [--no-session-ref] [--no-pane] [--exclude <agent-ref>]... [--yes]
```

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
projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--import-orphan-mirrors] [--yes] [--socket <name> | --socket-path <absolute>] [-o json]
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
projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--import-orphan-mirrors] [--yes] [--socket <name> | --socket-path <absolute>] [-o json]
```

Output modes (`-o`): `json`

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

Output modes (`-o`): `json`

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
projmux rebind project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... --root <absolute-path>
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
projmux rebind project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... --root <absolute-path>
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
projmux rename project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... --name <name> [-o <mode>]
projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux rename project`](#projmux-rename-project) | Rename a Projmux Project resource; with no selector inside tmux, the active Project |
| [`projmux rename window`](#projmux-rename-window) | Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession, no selector means the active Window, and the tmux tab is renamed with it |
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
projmux rename project [<ref>] [--project <ref> | -p <ref>] [--selector key=value]... --name <name> [-o <mode>]
```

Aliases: `projects`

Output modes (`-o`): `receipt`, `none`

### `projmux rename window`

Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession, no selector means the active Window, and the tmux tab is renamed with it

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
projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
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
projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
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
projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [-o <mode>]
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
projmux runtime sessions [--ui popup|sidebar]
projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui popup|sidebar]
projmux runtime attach [--keep <n>] [--fallback home|ephemeral]
projmux runtime stop [<session>...]
projmux runtime tag list|clear
projmux runtime tag toggle <name>
projmux runtime prune [--keep <n>]
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
projmux runtime sessions [--ui popup|sidebar]
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
projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui popup|sidebar]
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
projmux runtime attach [--keep <n>] [--fallback home|ephemeral]
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
projmux runtime stop [<session>...]
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
projmux runtime tag list|clear
projmux runtime tag toggle <name>
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
projmux runtime prune [--keep <n>]
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
projmux setup [--timeout <duration>] [--non-interactive]
projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]
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
projmux shell [--session <name>] [--socket <name>] [--config <path>] [--bin <path>] [--no-install]
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
projmux switch [--ui popup|sidebar] [--anchor <pane>]
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
projmux unregister project [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux unregister project`](#projmux-unregister-project) | Unregister Projects and their Registry descendants while preserving roots, Git/worktrees, and runtime |

Canonical spelling: `projmux unregister project`

### `projmux unregister project`

Unregister Projects and their Registry descendants while preserving roots, Git/worktrees, and runtime

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
projmux unregister project [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]
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
projmux update status [--json]
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
projmux update check [--json]
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
projmux update apply [--dry-run] [--no-apply]
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
