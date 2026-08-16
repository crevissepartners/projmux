# Settings Information Architecture

Settings is container-first with progressive disclosure. A category row is a
**View** you navigate into; a simple value is edited inside the View that owns
it. The visible tree below is the shipped architecture — it is generated from
the navigation catalog in `internal/app/settings_nav.go` and frozen by
`internal/app/testdata/settings-nav-tree.golden`, so an IA change is a diff on
that golden rather than a behavior discovered at runtime.

## Row affordances

Every rendered row has exactly one affordance. A row never both navigates and
mutates.

- `[View]` — a navigation boundary: a category, a collection, or a component
  with more than one setting. Entering a View never changes a saved value.
- `[State]` — read-only current value, source, and availability. Enter is a
  no-op consumed by the owning loop.
- `[Toggle]` — a simple, immediately reversible boolean inside its owning View.
  A boolean never gets a `Visibility View → Show/Hide` route of its own.
- `[Choice]` — a small enum/value row. Enter opens a compact chooser and
  returns to the same View; it is not a separate breadcrumb route.
- `[Edit]` — a path, command, or name change that needs typed input or a picker.
- `[Action]` — check, copy, preview, test, update: an observable execution.
- `[Confirm]` — remove, reset, unbind, untrust, quit. The row names both the
  target and the consequence, so a confirmation is never a bare yes/no.

Collections own `Add`; the item owns `Remove`. Current values are visible on the
leaf row itself, and a non-default source, conflict, or unavailability is a
badge or secondary text rather than a mandatory intermediate screen. A control
that cannot act is a disabled row carrying its reason and the canonical next
step, never a silent no-op.

## Global

- **Projects** — Project discovery, pins, and sidebar policy. Settings manages
  discovery and pinning; the runtime picker UI keeps the name `Project Picker`.
  - `Primary discovery root [View]` — effective/saved/source state, then
    `Use current directory`, `Enter path`, `Clear saved root`.
  - `Additional discovery roots [View]` — the collection owns the two add rows;
    each saved root is an item View that owns `Remove discovery root`.
  - `Pinned Projects [View]` — each pin is an item View showing the Project's
    display name, unique name, uid, bound root, condition, missing-since and
    runtime separately. A `MissingRoot` Project is never hidden, deleted, or
    re-pinned under a new identity: the item offers `Rebind Project root`, which
    calls the canonical `rebind project` route and keeps the same uid. Settings
    performs no heuristic uid merge and no automatic prune.
  - `Project Sidebar [View]` — `Closed Project startup` chooses between using
    the stored Project topology and asking for a Snapshot. The saved file keeps
    its `sidebar-startup-picker` spelling.
- **AI** — `AI` is a product category, never an addressable resource.
  - `Default launch target [Choice]` — an Agent Provider, a Shell Pane, or
    choose-at-launch. It is a keybinding/picker preference and does not weaken
    the canonical `create agent` explicit-provider requirement.
  - `Enabled providers [View]` — Claude, Codex, and Antigravity are Providers;
    `Shell`, `Selective` and `Resume` are not Providers and never appear here.
  - `Agent Resume Picker [View]` — states that resume targets an existing Agent
    in the `Offline` or `Failed` phase and that its new action is
    `Create New Agent`, then the picker limit and scan depth.
- **Notifications** — the persistent Notification queue and how it is delivered.
  Live Pane attention is a presentation concern and lives under Appearance.
  - `Desktop delivery [View]` — effective sender/source, `Delivery mode`
    (`off`/`notify`), `Dedupe window`, and the `PROJMUX_NOTIFY_HOOK` external
    sender as read-only state. The external sender is never presented as an
    alternative to the `[hooks.send-noti]` fan-out, which is Automation.
  - `Provider Integrations [View]` — the Claude/Codex/Antigravity Provider
    inventory and nothing else. Each Provider item shows wiring status,
    conflict, config path and setup guidance, offers `Check integration` to
    re-read the wiring and report what it observed, and offers copyable
    install/remove commands. Settings copies command text; it never installs or
    removes wiring.
  - `tmux event source [View]` — the tmux bell producer, flat: bell wiring
    status, source, conflict, setup guidance, and `Check`. It is an event
    source, not a Provider, so it is not a member of the Provider inventory and
    does not render as a Provider item.
  - `Agent event behavior [View]` — per Provider, per event: default / notify /
    state only / quiet. It covers the same Claude/Codex/Antigravity enum the
    Provider inventory uses, so a Provider can never be wireable here and
    missing there. It writes only
    `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json`.
  - Provider wiring and Provider event behavior are separate destinations:
    install/remove/conflict/source rows exist only under Provider Integrations,
    and the notify/state/quiet projection exists only under Agent event
    behavior. Neither surface renders the other's controls.
- **Automation** — Projmux-owned user scripts and the policy that gates the
  project-local ones. It does not own Provider wiring or desktop delivery.
  - `Projmux session lifecycle [View]` — `Before session create`,
    `After session create`, `After session attach`. Each event is a View showing
    the command, its effective state, and its source before any mutation row.
  - `After notification queued [View]` — `[hooks.send-noti]`, the user fan-out
    that runs after the durable queue write. The row says so from its own side:
    it runs after the queue write, not instead of the desktop sender. Setting
    `PROJMUX_NOTIFY_HOOK` and `[hooks.send-noti]` at the same time is a
    supported combination, not a conflict — one chooses which process delivers
    the OS notification, the other is a script that runs on the queue write.
  - `Project automation policy [Toggle]` — the promoted execution gate for
    project-local automation.
  - Global `[hooks.*]` stays read-only in app: its edit and remove rows render
    as disabled rows naming the config path and `projmux hook edit <event>`.
- **Appearance** — every visual surface has one destination here.
  - `Theme [View]` — `Preset`, `Tokens [View] → Core/Surface/State/Chrome →
    <token>`, and `Reset theme`. Only the global `[theme]` block is edited;
    project-local `[theme]` remains deprecated migration data.
  - `Status Bar [View]` — the Projmux-owned status segments. `Notifications
    HUD`, `Working directory` and `Git` are component Views with a current/
    source/preview state row and an icon `Choice`; `Resources` is a plain
    `Toggle` whose off state stops the segment and the host sampler together.
    Per-component visibility storage for the remaining components is a later
    slice; this container ships only the rows whose control exists, because a
    row that cannot act is an empty placeholder.
  - `Language / Locale [Choice]` and `Agent attention badge style [Choice]`.
- **Snapshots** — the visible noun is the Snapshot resource. `session-state`
  remains the config/route spelling and appears only as source detail.
- **Keybindings** — `Launch & popups`, `Agent & Pane launch`,
  `Pane & Window navigation`, `Sidebar & picker actions` (nested by surface:
  Project Sidebar, Session Picker, Notification Sidebar, Settings), and
  `Input delivery`. Every catalog action belongs to exactly one category, the
  assignment is explicit metadata rather than an ID-prefix inference, and the
  category rows carry their members' search text so search crosses categories.
  An action detail shows the action, its target kind, result kind, placement and
  anchor, its effective keys and source, then the Keys collection and the
  unbind/reset confirmations.
- **About** — version/source state, `Updates [View]` (current/latest/installer/
  release notes, `Check for updates`, `Update now`), `Welcome`, and
  `Quit Projmux`, whose confirmation names the app-owned runtime and socket.

## Project

- **Automation** — `Trust [View]` (trust state, project config hash, source, and
  the approve/revoke actions) and `Project hooks [View]` (`Session lifecycle`
  plus `After notification queued`, with the same per-event Views the global
  scope uses, extended by a trust state row).
- **Snapshots** — the auto-save override and the saved snapshots.

Without an actionable project context the Project surface renders a single
passive guidance row rather than repeating a disabled reason per row.

## Vocabulary and compatibility

Visible nouns follow the shared resource vocabulary: `Project`, `Window`,
`Pane`, `Agent`, `Provider`, `Notification`, `Snapshot`, with `AI` as a category
and `Session` as the runtime projection. The Agent Usage HUD is a presentation
of what the canonical `agent usage` command provides; there is no addressable
`Usage` resource, and Settings never spells usage as a readable resource kind.

Renaming is display-only. Keymap action IDs (`new-window`, `Sidebar:KillSession`,
`ai-split-right`, …), config keys, `[hooks.*]` tables, environment variables and
runtime compatibility routes keep their shipped spelling, so a saved
`keymap.toml`, a saved config file and every runtime route behave exactly as
before the rename.

Interactive right/down keybindings pass the Pane the user pressed the key in as
an explicit anchor. Automation that omits an anchor uses the Window's persisted
`spec.primaryPaneRef` and never silently recovers a stale reference from the
focused Pane.

## Retired destinations

`Labs`, the separate `Theme` root, `Project recipe`, `Effective merge view` and
the flat Keybindings action wall are gone from navigation. They are not visible
rows and not hidden redirects: their values carry no catalog metadata and
`runSection` refuses them. The project recipe and effective-merge handlers still
exist in the tree; their hard removal is a later slice.

Removed guidance keeps canonical destinations elsewhere: key delivery discovery
in `projmux setup`, supported terminal remediation in `projmux setup terminal`,
and read-only dependency/runtime diagnostics in `projmux doctor`.

## Mutation feedback

Settings mutation feedback follows one transient contract. The next picker frame
inserts one passive `Feedback` row after Back; the row uses the catalogued
`settingsNoopValue`, so Enter cannot create an unknown action. Selecting another
navigation/action clears the old row before that operation runs, and a handled
result replaces it. Typed validation and staged apply failures stay in the popup
instead of being visible only on stdout/stderr.

The generic feedback inventory deliberately excludes Welcome, Quit, read-only
hook/effective/notification diagnostics, Snapshot preview, and key
capture/probe/diagnostic bodies. Those flows own a viewer, confirmation, or
multi-step output surface; only an actual Settings write at their boundary is
eligible for transient mutation feedback.
