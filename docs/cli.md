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

## Commands

```
projmux <command> [args...]
```

| Command | Kind | Summary |
| --- | --- | --- |
| [`projmux agent`](#projmux-agent) | canonical | Manage Agent state, topic, integrations, and account usage |
| [`projmux ai`](#projmux-ai) | compatibility | Manage tmux AI split launch and settings |
| [`projmux attention`](#projmux-attention) | canonical | View and manage live tmux pane attention state |
| [`projmux attach`](#projmux-attach) | compatibility | Open tmux lifecycle entry helpers |
| [`projmux config`](#projmux-config) | canonical | Render or apply the generated tmux configuration |
| [`projmux create`](#projmux-create) | canonical | Create Projmux resources |
| [`projmux current`](#projmux-current) | compatibility | Resolve the active tmux pane path |
| [`projmux delete`](#projmux-delete) | canonical | Delete Projmux resources with an explicit cascade plan |
| [`projmux describe`](#projmux-describe) | canonical | Describe one Projmux resource |
| [`projmux doctor`](#projmux-doctor) | shortcut | Run read-only runtime and integration diagnostics |
| [`projmux diagnostics`](#projmux-diagnostics) | canonical | Read operational events or create an explicit local support report |
| [`projmux focus`](#projmux-focus) | compatibility | Switch the active client to a session/window/pane target |
| [`projmux get`](#projmux-get) | canonical | Read Projmux resources by selector |
| [`projmux hook`](#projmux-hook) | canonical | List, edit, validate, and trust lifecycle hook config |
| [`projmux kill`](#projmux-kill) | compatibility | Terminate tagged tmux sessions |
| [`projmux notify`](#projmux-notify) | compatibility | Manage the pending AI notify queue (push/list/ack/reconcile) |
| [`projmux pin`](#projmux-pin) | compatibility | Manage pinned project directories |
| [`projmux prune`](#projmux-prune) | compatibility | Trim stale lifecycle state and inspect preserved snapshots |
| [`projmux quit`](#projmux-quit) | shortcut | Quit the app-owned projmux tmux runtime |
| [`projmux rebind`](#projmux-rebind) | canonical | Rebind a Project to a new absolute root without moving files |
| [`projmux rename`](#projmux-rename) | canonical | Rename a Projmux resource metadata.name |
| [`projmux resources`](#projmux-resources) | shortcut | Inspect live Project, Window, and Pane CPU/RSS attribution |
| [`projmux restore`](#projmux-restore) | canonical | Preview a saved session snapshot restore (--dry-run only in this release) |
| [`projmux runtime`](#projmux-runtime) | canonical | Manage the live and ephemeral tmux runtime inventory |
| [`projmux sessions`](#projmux-sessions) | compatibility | Pick and open an existing tmux session |
| [`projmux session-state`](#projmux-session-state) | compatibility | Inspect and manage saved tmux session snapshots |
| [`projmux settings`](#projmux-settings) | shortcut | Configure projmux |
| [`projmux setup`](#projmux-setup) | canonical | Probe terminal keys or remediate them with setup terminal |
| [`projmux shell`](#projmux-shell) | shortcut | Open the isolated projmux tmux app |
| [`projmux switch`](#projmux-switch) | shortcut | Pick and open a project tmux session |
| [`projmux tag`](#projmux-tag) | compatibility | Manage tagged tmux sessions |
| [`projmux update`](#projmux-update) | canonical | Check installer-aware release update status |
| [`projmux upgrade`](#projmux-upgrade) | compatibility | Self-update projmux via go install |
| [`projmux usage`](#projmux-usage) | compatibility | Report AI token usage across 5h and weekly windows |
| [`projmux welcome`](#projmux-welcome) | shortcut | Reprint the shell welcome guide |
| [`projmux window`](#projmux-window) | canonical | Open recent window navigation surfaces |
| [`projmux help`](#projmux-help) | canonical | Show bootstrap help |
| [`projmux version`](#projmux-version) | canonical | Print the current version |

## `projmux agent`

Manage Agent state, topic, integrations, and account usage

```
projmux agent status [set <state> [pane]]
projmux agent topic [set|clear] ...
projmux agent resume <ref> [--project <ref>] [--window <ref>]...
projmux agent integrate <provider> [--dry-run]
projmux agent usage [--model <name>] [--window <name>] [--json] [--force]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux agent status`](#projmux-agent-status) | Read or set the Agent status state |
| [`projmux agent topic`](#projmux-agent-topic) | Read, set, or clear the Agent topic annotation |
| [`projmux agent resume`](#projmux-agent-resume) | Rebind an Offline or Failed Agent to a new managed Pane |
| [`projmux agent integrate`](#projmux-agent-integrate) | Install or remove provider hook integrations |
| [`projmux agent usage`](#projmux-agent-usage) | Read provider account usage quota snapshots |

Canonical spelling: `projmux agent status`, `projmux agent topic`, `projmux agent resume`, `projmux agent integrate`, `projmux agent usage`

### `projmux agent status`

Read or set the Agent status state

```
projmux agent status [set <state> [pane]]
```

### `projmux agent topic`

Read, set, or clear the Agent topic annotation

```
projmux agent topic [set|clear] ...
```

### `projmux agent resume`

Rebind an Offline or Failed Agent to a new managed Pane

```
projmux agent resume <ref> [--project <ref>] [--window <ref>]... [--selector key=value]...
```

### `projmux agent integrate`

Install or remove provider hook integrations

```
projmux agent integrate <provider> [--dry-run]
```

### `projmux agent usage`

Read provider account usage quota snapshots

```
projmux agent usage [--model <name>] [--window <name>] [--json] [--force]
```

## `projmux ai`

Manage tmux AI split launch and settings

```
projmux ai split|picker|settings|status|notify|watch-title|ingest|integrate|topic
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux ai split`](#projmux-ai-split) | Launch an AI agent or shell pane split |
| [`projmux ai picker`](#projmux-ai-picker) | Open the interactive AI agent picker |
| [`projmux ai settings`](#projmux-ai-settings) | Open the AI settings surface |
| [`projmux ai status`](#projmux-ai-status) | Read or set the AI pane status state |
| [`projmux ai notify`](#projmux-ai-notify) | Dispatch an AI pane notification |
| [`projmux ai watch-title`](#projmux-ai-watch-title) | Run the AI pane title watcher |
| [`projmux ai ingest`](#projmux-ai-ingest) | Ingest provider hook and log events |
| [`projmux ai integrate`](#projmux-ai-integrate) | Install or remove provider hook integrations |
| [`projmux ai topic`](#projmux-ai-topic) | Read, set, or clear the AI pane topic |

Canonical spelling: `projmux create agent`, `projmux create pane`, `projmux get agents`, `projmux describe agent`, `projmux agent status`, `projmux agent topic`, `projmux agent resume`, `projmux agent integrate`

### `projmux ai split`

Launch an AI agent or shell pane split

```
projmux ai split [--agent <provider>] [right|down] [--print-pane-id] [-- <prompt>]
```

Output modes (`-o`): `pane-id`

Canonical spelling: `projmux create agent`, `projmux create pane`

### `projmux ai picker`

Open the interactive AI agent picker

```
projmux ai picker
```

Canonical spelling: `projmux create agent`

### `projmux ai settings`

Open the AI settings surface

```
projmux ai settings
```

### `projmux ai status`

Read or set the AI pane status state

```
projmux ai status [set ...]
```

Canonical spelling: `projmux agent status`

### `projmux ai notify`

Dispatch an AI pane notification

```
projmux ai notify ...
```

### `projmux ai watch-title`

Run the AI pane title watcher

```
projmux ai watch-title ...
```

### `projmux ai ingest`

Ingest provider hook and log events

```
projmux ai ingest <source>
```

### `projmux ai integrate`

Install or remove provider hook integrations

```
projmux ai integrate <provider> [--dry-run]
```

Canonical spelling: `projmux agent integrate`

### `projmux ai topic`

Read, set, or clear the AI pane topic

```
projmux ai topic [set|clear] ...
```

Canonical spelling: `projmux agent topic`

## `projmux attention`

View and manage live tmux pane attention state

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

```
projmux attention toggle
```

### `projmux attention clear`

Clear attention state for a pane

```
projmux attention clear
```

### `projmux attention arm`

Arm focus-only attention consumption

```
projmux attention arm
```

### `projmux attention list`

List live pane attention state

```
projmux attention list
```

### `projmux attention window`

Render window-scoped attention badges

```
projmux attention window
```

## `projmux attach`

Open tmux lifecycle entry helpers

```
projmux attach auto [--keep=N] [--fallback=home|ephemeral]
projmux attach project <ref>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux attach auto`](#projmux-attach-auto) | Auto-attach with keep and fallback policy |
| [`projmux attach project`](#projmux-attach-project) | Enter a Project runtime from outside tmux, materializing it when offline |

Canonical spelling: `projmux attach project`, `projmux runtime attach`

### `projmux attach auto`

Auto-attach with keep and fallback policy

```
projmux attach auto [--keep=N] [--fallback=home|ephemeral]
```

Canonical spelling: `projmux runtime attach`

### `projmux attach project`

Enter a Project runtime from outside tmux, materializing it when offline

```
projmux attach project <ref>
```

## `projmux config`

Render or apply the generated tmux configuration

```
projmux config render standalone|app [--bin <path>]
projmux config apply [--bin <path>] [--config <path>] [--socket <name>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux config render`](#projmux-config-render) | Print a generated tmux config to stdout; writes nothing |
| [`projmux config apply`](#projmux-config-apply) | Write the generated app tmux config and reload the live projmux server |

Canonical spelling: `projmux config render`, `projmux config apply`

### `projmux config render`

Print a generated tmux config to stdout; writes nothing

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

```
projmux config render standalone [--bin <path>]
```

Canonical spelling: `projmux config render`

#### `projmux config render app`

Print the config the app-owned projmux tmux server runs from

```
projmux config render app [--bin <path>]
```

Canonical spelling: `projmux config render`

### `projmux config apply`

Write the generated app tmux config and reload the live projmux server

```
projmux config apply [--bin <path>] [--config <path>] [--socket <name>]
```

## `projmux create`

Create Projmux resources

```
projmux create window --project <ref> [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
projmux create pane --project <ref> [--window <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create agent --provider <provider> --project <ref> [--window <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]
projmux create codex|claude|antigravity --project <ref> [--window <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux create window`](#projmux-create-window) | Create a Window and its initial Pane below one Project; the runtime is materialized detached |
| [`projmux create pane`](#projmux-create-pane) | Create a shell Pane; --project splits the resolved Windows detached, without it the current Window |
| [`projmux create agent`](#projmux-create-agent) | Create an Agent and its managed Pane; --provider is required, and --project splits the resolved Windows detached |

Provider shortcuts:

| Route | Summary |
| --- | --- |
| [`projmux create codex`](#projmux-create-codex) | Provider shortcut for create agent --provider codex |
| [`projmux create claude`](#projmux-create-claude) | Provider shortcut for create agent --provider claude |
| [`projmux create antigravity`](#projmux-create-antigravity) | Provider shortcut for create agent --provider antigravity |

Canonical spelling: `projmux create window`, `projmux create pane`, `projmux create agent`, `projmux create codex`, `projmux create claude`, `projmux create antigravity`

### `projmux create window`

Create a Window and its initial Pane below one Project; the runtime is materialized detached

```
projmux create window --project <ref> [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

### `projmux create pane`

Create a shell Pane; --project splits the resolved Windows detached, without it the current Window

```
projmux create pane --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
projmux create pane [--placement right|down] [-o <mode>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

### `projmux create agent`

Create an Agent and its managed Pane; --provider is required, and --project splits the resolved Windows detached

```
projmux create agent --provider <provider> --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
projmux create agent --provider <provider> [--placement right|down] [-o pane-id|none] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

### `projmux create codex`

Provider shortcut for create agent --provider codex

```
projmux create codex --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
projmux create codex [--placement right|down] [-o pane-id|none] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

### `projmux create claude`

Provider shortcut for create agent --provider claude

```
projmux create claude --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
projmux create claude [--placement right|down] [-o pane-id|none] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

### `projmux create antigravity`

Provider shortcut for create agent --provider antigravity

```
projmux create antigravity --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]
projmux create antigravity [--placement right|down] [-o pane-id|none] [-- <payload>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `pane-id`, `none`

## `projmux current`

Resolve the active tmux pane path

```
projmux current
```

Field projections (`-o`): `cwd`

Canonical spelling: `projmux get pane`

## `projmux delete`

Delete Projmux resources with an explicit cascade plan

```
projmux delete window <ref>... [--project <ref>] [--selector key=value]... [--dry-run] [--yes]
projmux delete pane <ref>... [--project <ref>] [--window <ref>]... [--dry-run] [--yes]
projmux delete agent <ref>... [--project <ref>] [--window <ref>]... [--dry-run] [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux delete window`](#projmux-delete-window) | Delete Windows and every descendant Agent and Pane |
| [`projmux delete pane`](#projmux-delete-pane) | Delete Panes; an Agent-owned current Pane leaves its Agent Offline |
| [`projmux delete agent`](#projmux-delete-agent) | Delete Agents and their managed Panes |
| [`projmux delete notification`](#projmux-delete-notification) | Delete pending notification rows |
| [`projmux delete snapshot`](#projmux-delete-snapshot) | Delete saved session snapshots |

Canonical spelling: `projmux delete window`, `projmux delete pane`, `projmux delete agent`, `projmux delete notification`, `projmux delete snapshot`

### `projmux delete window`

Delete Windows and every descendant Agent and Pane

```
projmux delete window
```

### `projmux delete pane`

Delete Panes; an Agent-owned current Pane leaves its Agent Offline

```
projmux delete pane
```

### `projmux delete agent`

Delete Agents and their managed Panes

```
projmux delete agent
```

### `projmux delete notification`

Delete pending notification rows

```
projmux delete notification
```

### `projmux delete snapshot`

Delete saved session snapshots

```
projmux delete snapshot
```

## `projmux describe`

Describe one Projmux resource

```
projmux describe project [<ref>] [-o <mode>]
projmux describe window [<ref>] [--project <ref>] [-o <mode>]
projmux describe pane [<ref>] [--project <ref>] [--window <ref>]... [-o <mode>]
projmux describe agent [<ref>] [--project <ref>] [--window <ref>]... [-o <mode>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux describe project`](#projmux-describe-project) | Describe one Project resource; with no selector inside tmux, the active Project |
| [`projmux describe window`](#projmux-describe-window) | Describe one Window resource; with no selector inside tmux, the active Window |
| [`projmux describe pane`](#projmux-describe-pane) | Describe one Pane resource; with no selector inside tmux, the active Pane |
| [`projmux describe agent`](#projmux-describe-agent) | Describe one Agent resource; with no selector inside tmux, the Agent owning the active Pane |

Canonical spelling: `projmux describe project`, `projmux describe window`, `projmux describe pane`, `projmux describe agent`

### `projmux describe project`

Describe one Project resource; with no selector inside tmux, the active Project

```
projmux describe project
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe window`

Describe one Window resource; with no selector inside tmux, the active Window

```
projmux describe window
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe pane`

Describe one Pane resource; with no selector inside tmux, the active Pane

```
projmux describe pane
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux describe agent`

Describe one Agent resource; with no selector inside tmux, the Agent owning the active Pane

```
projmux describe agent
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

## `projmux doctor`

Run read-only runtime and integration diagnostics

```
projmux doctor [--json] [--section <name>] [--verbose]
```

## `projmux diagnostics`

Read operational events or create an explicit local support report

```
projmux diagnostics log [--json] [--tail <n>]
projmux diagnostics report [--output <path>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux diagnostics log`](#projmux-diagnostics-log) | Read the bounded local operations journal |
| [`projmux diagnostics report`](#projmux-diagnostics-report) | Create an explicit redacted local support report |

Canonical spelling: `projmux diagnostics log`, `projmux diagnostics report`

### `projmux diagnostics log`

Read the bounded local operations journal

```
projmux diagnostics log
```

### `projmux diagnostics report`

Create an explicit redacted local support report

```
projmux diagnostics report
```

## `projmux focus`

Switch the active client to a session/window/pane target

```
projmux focus --target <target>
projmux focus --uri <uri>
projmux focus project <ref>
projmux focus window <ref> --project <ref>
projmux focus pane <ref> --project <ref> --window <ref>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux focus project`](#projmux-focus-project) | Move the current client to an already-live Project; never materializes |
| [`projmux focus window`](#projmux-focus-window) | Move the current client to an already-live Window; never materializes |
| [`projmux focus pane`](#projmux-focus-pane) | Move the current client to an already-live Pane; never materializes |

Canonical spelling: `projmux focus project`, `projmux focus window`, `projmux focus pane`

### `projmux focus project`

Move the current client to an already-live Project; never materializes

```
projmux focus project <ref> [--socket <path>] [--client <tty>] [--json]
```

### `projmux focus window`

Move the current client to an already-live Window; never materializes

```
projmux focus window <ref> --project <ref> [--socket <path>] [--client <tty>] [--json]
```

### `projmux focus pane`

Move the current client to an already-live Pane; never materializes

```
projmux focus pane <ref> --project <ref> --window <ref> [--socket <path>] [--json]
```

## `projmux get`

Read Projmux resources by selector

```
projmux get projects|windows|panes|agents [--project <ref>] [--selector key=value]... [-o <mode>]
projmux get pane --current -o cwd
projmux get pane [--project <ref>] [--window <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux get projects`](#projmux-get-projects) | List Project resources |
| [`projmux get windows`](#projmux-get-windows) | List Window resources |
| [`projmux get panes`](#projmux-get-panes) | List Pane resources |
| [`projmux get agents`](#projmux-get-agents) | List Agent resources |
| [`projmux get notifications`](#projmux-get-notifications) | List pending notification rows |
| [`projmux get snapshots`](#projmux-get-snapshots) | List saved session snapshots |
| [`projmux get pane`](#projmux-get-pane) | Read one Pane resource; with no selector inside tmux, the active Pane |

Canonical spelling: `projmux get projects`, `projmux get windows`, `projmux get panes`, `projmux get agents`, `projmux get notifications`, `projmux get snapshots`, `projmux get pane`

### `projmux get projects`

List Project resources

```
projmux get projects
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux get windows`

List Window resources

```
projmux get windows
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux get panes`

List Pane resources

```
projmux get panes
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux get agents`

List Agent resources

```
projmux get agents
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

### `projmux get notifications`

List pending notification rows

```
projmux get notifications
```

### `projmux get snapshots`

List saved session snapshots

```
projmux get snapshots
```

### `projmux get pane`

Read one Pane resource; with no selector inside tmux, the active Pane

```
projmux get pane [--current] [--project <ref>] [--window <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]
```

Output modes (`-o`): `uid`, `name`, `ref`, `metadata`, `json`, `none`

Field projections (`-o`): `cwd`

## `projmux hook`

List, edit, validate, and trust lifecycle hook config

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

```
projmux hook list
```

### `projmux hook edit`

Edit lifecycle hook config

```
projmux hook edit
```

### `projmux hook validate`

Validate lifecycle hook config

```
projmux hook validate
```

### `projmux hook trust`

Trust the current project hook config

```
projmux hook trust
```

### `projmux hook untrust`

Revoke project hook config trust

```
projmux hook untrust
```

## `projmux kill`

Terminate tagged tmux sessions

```
projmux kill tagged [<session>...]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux kill tagged`](#projmux-kill-tagged) | Terminate the tagged session selection |

Canonical spelling: `projmux runtime stop`

### `projmux kill tagged`

Terminate the tagged session selection

```
projmux kill tagged
```

Canonical spelling: `projmux runtime stop`

## `projmux notify`

Manage the pending AI notify queue (push/list/ack/reconcile)

```
projmux notify push|list|ack|reconcile
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux notify push`](#projmux-notify-push) | Push a pending notification row |
| [`projmux notify list`](#projmux-notify-list) | List pending notification rows |
| [`projmux notify ack`](#projmux-notify-ack) | Acknowledge notification rows |
| [`projmux notify reconcile`](#projmux-notify-reconcile) | Reconcile the notification queue against live targets |

Canonical spelling: `projmux get notifications`, `projmux delete notification`

### `projmux notify push`

Push a pending notification row

```
projmux notify push
```

### `projmux notify list`

List pending notification rows

```
projmux notify list
```

Canonical spelling: `projmux get notifications`

### `projmux notify ack`

Acknowledge notification rows

```
projmux notify ack
```

### `projmux notify reconcile`

Reconcile the notification queue against live targets

```
projmux notify reconcile
```

## `projmux pin`

Manage pinned project directories

```
projmux pin list|add|remove|toggle|clear
projmux pin project list|add|remove|toggle|clear
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux pin project`](#projmux-pin-project) | Manage pinned project directories (canonical spelling) |
| [`projmux pin list`](#projmux-pin-list) | List pinned project directories |
| [`projmux pin add`](#projmux-pin-add) | Pin a project directory |
| [`projmux pin remove`](#projmux-pin-remove) | Unpin a project directory |
| [`projmux pin toggle`](#projmux-pin-toggle) | Toggle a project directory pin |
| [`projmux pin clear`](#projmux-pin-clear) | Clear all project directory pins |

Canonical spelling: `projmux pin project`

### `projmux pin project`

Manage pinned project directories (canonical spelling)

```
projmux pin project list|add|remove|toggle|clear
```

### `projmux pin list`

List pinned project directories

```
projmux pin list
```

Canonical spelling: `projmux pin project`

### `projmux pin add`

Pin a project directory

```
projmux pin add
```

Canonical spelling: `projmux pin project`

### `projmux pin remove`

Unpin a project directory

```
projmux pin remove
```

Canonical spelling: `projmux pin project`

### `projmux pin toggle`

Toggle a project directory pin

```
projmux pin toggle
```

Canonical spelling: `projmux pin project`

### `projmux pin clear`

Clear all project directory pins

```
projmux pin clear
```

Canonical spelling: `projmux pin project`

## `projmux prune`

Trim stale lifecycle state and inspect preserved snapshots

```
projmux prune ephemeral [--keep=N]
projmux prune session-state [--older-than <duration>]
projmux prune session-state delete <session>...
projmux prune snapshot [--older-than <duration>]
projmux prune project --missing --older-than <duration> [--yes]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux prune ephemeral`](#projmux-prune-ephemeral) | Trim old ephemeral tmux sessions |
| [`projmux prune session-state`](#projmux-prune-session-state) | Inspect or delete preserved session snapshots |
| [`projmux prune project`](#projmux-prune-project) | Delete Projects whose spec.root has been missing for a bounded age |
| [`projmux prune snapshot`](#projmux-prune-snapshot) | Inspect or delete preserved session snapshots (canonical spelling) |

Canonical spelling: `projmux runtime prune`, `projmux prune project`, `projmux prune snapshot`

### `projmux prune ephemeral`

Trim old ephemeral tmux sessions

```
projmux prune ephemeral
```

Canonical spelling: `projmux runtime prune`

### `projmux prune session-state`

Inspect or delete preserved session snapshots

```
projmux prune session-state
```

Canonical spelling: `projmux prune snapshot`, `projmux delete snapshot`

### `projmux prune project`

Delete Projects whose spec.root has been missing for a bounded age

```
projmux prune project --missing --older-than <duration> [--yes]
```

### `projmux prune snapshot`

Inspect or delete preserved session snapshots (canonical spelling)

```
projmux prune snapshot [--older-than <duration>]
projmux prune snapshot delete <session>...
```

Canonical spelling: `projmux delete snapshot`

## `projmux quit`

Quit the app-owned projmux tmux runtime

```
projmux quit [--yes|--force]
```

## `projmux rebind`

Rebind a Project to a new absolute root without moving files

```
projmux rebind project [<ref>] --root <absolute-path>
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux rebind project`](#projmux-rebind-project) | Rewrite one Project spec.root; no filesystem move, no heuristic uid merge |

Canonical spelling: `projmux rebind project`

### `projmux rebind project`

Rewrite one Project spec.root; no filesystem move, no heuristic uid merge

```
projmux rebind project [<ref>] --root <absolute-path>
```

## `projmux rename`

Rename a Projmux resource metadata.name

```
projmux rename project [<ref>] --name <name>
projmux rename window [<ref>] --name <name> [--project <ref>]
projmux rename pane [<ref>] --name <name> [--project <ref>] [--window <ref>]...
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux rename project`](#projmux-rename-project) | Rename a Projmux Project resource; with no selector inside tmux, the active Project |
| [`projmux rename window`](#projmux-rename-window) | Rename a Projmux Window resource; with no selector inside tmux, the active Window |
| [`projmux rename pane`](#projmux-rename-pane) | Rename a Projmux Pane resource; with no selector inside tmux, the active Pane; does not change tmux pane_title |

Canonical spelling: `projmux rename project`, `projmux rename window`, `projmux rename pane`

### `projmux rename project`

Rename a Projmux Project resource; with no selector inside tmux, the active Project

```
projmux rename project
```

### `projmux rename window`

Rename a Projmux Window resource; with no selector inside tmux, the active Window

```
projmux rename window
```

### `projmux rename pane`

Rename a Projmux Pane resource; with no selector inside tmux, the active Pane; does not change tmux pane_title

```
projmux rename pane
```

## `projmux resources`

Inspect live Project, Window, and Pane CPU/RSS attribution

```
projmux resources
```

## `projmux restore`

Preview a saved session snapshot restore (--dry-run only in this release)

```
projmux restore snapshot --dry-run [--session <name>]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux restore snapshot`](#projmux-restore-snapshot) | Preview a saved session snapshot restore; --dry-run is required |

Canonical spelling: `projmux restore snapshot`

### `projmux restore snapshot`

Preview a saved session snapshot restore; --dry-run is required

```
projmux restore snapshot --dry-run [--session <name>]
```

## `projmux runtime`

Manage the live and ephemeral tmux runtime inventory

```
projmux runtime sessions [--ui=popup|sidebar]
projmux runtime attach [--keep=N] [--fallback=home|ephemeral]
projmux runtime stop [<session>...]
projmux runtime tag list|toggle|clear
projmux runtime prune [--keep=N]
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux runtime sessions`](#projmux-runtime-sessions) | Pick a live or ephemeral tmux session |
| [`projmux runtime attach`](#projmux-runtime-attach) | Attach a live or ephemeral runtime without Project identity |
| [`projmux runtime stop`](#projmux-runtime-stop) | Terminate live tmux sessions by tagged selection |
| [`projmux runtime tag`](#projmux-runtime-tag) | Manage the ephemeral tagged session selection |
| [`projmux runtime prune`](#projmux-runtime-prune) | Trim old ephemeral tmux sessions |

Canonical spelling: `projmux runtime sessions`, `projmux runtime attach`, `projmux runtime stop`, `projmux runtime tag`, `projmux runtime prune`

### `projmux runtime sessions`

Pick a live or ephemeral tmux session

```
projmux runtime sessions
```

### `projmux runtime attach`

Attach a live or ephemeral runtime without Project identity

```
projmux runtime attach
```

### `projmux runtime stop`

Terminate live tmux sessions by tagged selection

```
projmux runtime stop
```

### `projmux runtime tag`

Manage the ephemeral tagged session selection

```
projmux runtime tag
```

### `projmux runtime prune`

Trim old ephemeral tmux sessions

```
projmux runtime prune
```

## `projmux sessions`

Pick and open an existing tmux session

```
projmux sessions [--ui=popup|sidebar]
```

Canonical spelling: `projmux runtime sessions`

## `projmux session-state`

Inspect and manage saved tmux session snapshots

```
projmux session-state status|save|delete|restore|preview|popup
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux session-state status`](#projmux-session-state-status) | Show saved snapshot status |
| [`projmux session-state save`](#projmux-session-state-save) | Save a session snapshot |
| [`projmux session-state delete`](#projmux-session-state-delete) | Delete saved snapshots |
| [`projmux session-state restore`](#projmux-session-state-restore) | Restore a snapshot (CLI allows --dry-run only) |
| [`projmux session-state preview`](#projmux-session-state-preview) | Review a restore plan |
| [`projmux session-state popup`](#projmux-session-state-popup) | Open the snapshot review popup |

Canonical spelling: `projmux get snapshots`, `projmux delete snapshot`, `projmux restore snapshot`

### `projmux session-state status`

Show saved snapshot status

```
projmux session-state status
```

Canonical spelling: `projmux get snapshots`

### `projmux session-state save`

Save a session snapshot

```
projmux session-state save
```

### `projmux session-state delete`

Delete saved snapshots

```
projmux session-state delete
```

Canonical spelling: `projmux delete snapshot`

### `projmux session-state restore`

Restore a snapshot (CLI allows --dry-run only)

```
projmux session-state restore
```

Canonical spelling: `projmux restore snapshot`

### `projmux session-state preview`

Review a restore plan

```
projmux session-state preview
```

Canonical spelling: `projmux restore snapshot`

### `projmux session-state popup`

Open the snapshot review popup

```
projmux session-state popup
```

Canonical spelling: `projmux restore snapshot`

## `projmux settings`

Configure projmux

```
projmux settings
```

## `projmux setup`

Probe terminal keys or remediate them with setup terminal

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

```
projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]
```

## `projmux shell`

Open the isolated projmux tmux app

```
projmux shell [--session <name>]
```

## `projmux switch`

Pick and open a project tmux session

```
projmux switch [<project>]
```

Canonical spelling: `projmux focus project`

## `projmux tag`

Manage tagged tmux sessions

```
projmux tag list|toggle|clear
projmux tag project list|toggle|clear
```

Subcommands:

| Route | Summary |
| --- | --- |
| [`projmux tag project`](#projmux-tag-project) | Manage the tagged session selection (canonical spelling) |
| [`projmux tag list`](#projmux-tag-list) | List the tagged session selection |
| [`projmux tag toggle`](#projmux-tag-toggle) | Toggle a session tag |
| [`projmux tag clear`](#projmux-tag-clear) | Clear the tagged session selection |

Canonical spelling: `projmux tag project`, `projmux runtime tag`

### `projmux tag project`

Manage the tagged session selection (canonical spelling)

```
projmux tag project list|toggle|clear
```

### `projmux tag list`

List the tagged session selection

```
projmux tag list
```

Canonical spelling: `projmux runtime tag`

### `projmux tag toggle`

Toggle a session tag

```
projmux tag toggle
```

Canonical spelling: `projmux runtime tag`

### `projmux tag clear`

Clear the tagged session selection

```
projmux tag clear
```

Canonical spelling: `projmux runtime tag`

## `projmux update`

Check installer-aware release update status

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

```
projmux update status
```

### `projmux update check`

Check for a newer release and refresh the cache

```
projmux update check
```

### `projmux update apply`

Apply an available update

```
projmux update apply
```

## `projmux upgrade`

Self-update projmux via go install

```
projmux upgrade [--ref <ref>] [--target <path>] [--no-apply] [--dry-run]
```

Canonical spelling: `projmux update apply`

## `projmux usage`

Report AI token usage across 5h and weekly windows

```
projmux usage [--model <name>] [--window <name>] [--json] [--force]
```

Canonical spelling: `projmux agent usage`

## `projmux welcome`

Reprint the shell welcome guide

```
projmux welcome [--popup [--force]]
```

## `projmux window`

Open recent window navigation surfaces

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

```
projmux window record
```

Canonical spelling: `projmux get windows`

### `projmux window recent`

Open the recent-window navigation picker

```
projmux window recent
```

Canonical spelling: `projmux get windows`

## `projmux help`

Show bootstrap help

```
projmux help
projmux --help
projmux <route> --help
```

## `projmux version`

Print the current version

```
projmux version
projmux --version
```

