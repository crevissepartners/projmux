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
    HUD`, `Working directory` and `Git` are component Views because each owns a
    `Visible` Toggle plus an icon `Choice`; cwd/Git icon `off` removes only the
    icon and leaves the text segment visible. `Agent Usage HUD` is a component
    View with `Visible`, then Claude/Codex/Antigravity provider Views in the
    usage-supported catalog order. Each provider owns `Visible` plus only its
    explicit HUD windows: Claude/Codex own `5h` and `Weekly`; Antigravity owns
    `Weekly` only. Parent off states gate effective visibility without rewriting
    saved child values. Provider/window rows show saved, effective, and source.
    `Project`, `Clock` and `Settings launcher` are direct visibility Toggles. These global
    presentation values default on and report saved/default source; hiding the
    Settings launcher removes only its mouse chip, not the CLI or keybinding
    entry. `Resources` remains one enablement Toggle backed only by
    `live-resources` (default off): off removes its segment and sampler/cache
    mutation. Notifications and every Agent Usage visibility depth remain
    presentation-only and do not disable their underlying producers, cache,
    backoff, explicit table/JSON command, or cached popup.
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
  anchor, the exact shipped handler the key dispatches to, then separate
  **Single Keys** and **Sequences** collections followed by unbind/reset
  confirmations. Action detail owns one `+ Add binding` native reader and one
  `Enter binding manually` row; picker-local actions reject multi-stroke input
  with an explicit reason. The reader continuously accumulates one to four
  logical strokes without closing: Enter saves once, Escape cancels with no
  write, and Backspace pops only the last stroke or is an empty-draft no-op.
  Reserved control/navigation keys and modifiers never become candidates. The
  first plain printable is rejected, while later safe plain printables are allowed.
  One stroke saves as a single key and two to four as a sequence. Typed comma
  form (`C-o,o`) and legacy space form (`C-o o`) share the capture classifier,
  conflict preflight, and save boundary; display uses commas while schema,
  routes, generated config, and runtime keep spaces. Single and sequence detail
  replace routes pass explicit old-binding context through the same pipeline,
  so cross-kind replacement is atomic. Native macOS on/off and Linux/WSL expose
  identical logical authoring routes; only the Delivery diagnostic names the
  transport. Navigation, cancellation, Backspace editing, and delivery tests
  write no keymap/generated config and issue no live reload. There is no `Advanced` and
  no `Troubleshooting` container: both named an implementation layer, and both
  fronted rows that did nothing.
  A key detail shows the canonical key, the delivery path and `Test delivery`.
  `Test delivery` reports the logical key, the raw observation, the key tmux
  received, and one of `delivered` / `key-did-not-arrive` / `ambiguous-key` /
  `adapter-needed`. It never writes `keymap.toml` and never reloads tmux, so a
  raw observation can never become a stored logical key. Exactly one reader is
  ever live: inside a tmux popup the picker's own recorder reads the key, and
  outside tmux the controlling-TTY probe does. Where neither can be owned — or
  where the chord is a plain `Enter`/`Escape` that the recorder consumes as its
  own control — the row is disabled and carries the reason plus the canonical
  next step (`projmux setup`, then `projmux setup terminal <terminal> --apply`).

  The handler row is projected from the shipped CLI command manifest rather than
  from a second hand-kept table, so an action detail cannot advertise a route
  the binary does not have. `Open Project for Current Directory` is where that
  matters: the manifest classifies `projmux current` as a compatibility route
  whose canonical projection is the read-only `get pane` cwd field, while the
  route itself also ensures and attaches the derived runtime. The detail names
  both halves — the result kind is the ensure/attach outcome, the anchor says
  the Pane cwd is a read-only input rather than the outcome, and the handler
  boundary row states that the canonical cwd projection covers the input step
  only. The read-only query can therefore never read as the action succeeding.
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
an explicit anchor. The action detail names that anchor as what the handler
actually carries — the Pane's raw `%N` transport id, resolved at press time from
an explicit `TMUX_SPLIT_TARGET_PANE` or from `#{pane_id}` — and not as a
`metadata.uid`, because no keybinding handler reads a uid mirror and a raw pane
id is never a canonical uid. The two properties the row does assert are the ones
that hold: the target is explicit and pinned at press time, and it is never the
Window's persisted `spec.primaryPaneRef`. Automation that omits an anchor uses
`spec.primaryPaneRef` and never silently recovers a stale reference from the
focused Pane. `Create <Provider> Agent` always creates a new
Agent and `Open Agent Resume Picker` only resumes an existing Offline or Failed
Agent; the two are separate actions with separate result kinds and are never
normalized into one.

## Retired destinations

`Labs`, the separate `Theme` root, `Project recipe`, `Effective merge view` and
the flat Keybindings action wall are gone from navigation. They are not visible
rows and not hidden redirects: their values carry no catalog metadata and
`runSection` refuses them. The project recipe and effective-merge handlers,
catalog entries, and localization strings have also been removed.

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
