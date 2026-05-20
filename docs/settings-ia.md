# Settings Information Architecture

This branch finishes the current Settings/onboarding roadmap slice with a
view-first layout:

- Every Settings surface keeps the Global/Project tab strip visible so nested
  pages still show their scope. Detail pages may render the strip as passive
  context when changing tabs from that nested page would skip an explicit Back
  boundary.
- Settings uses a view-first rule: overview rows open details; details show the
  current state, source, and expected rendered result before offering mutation
  rows. If a detail opens a dedicated `Change` page, that page is mutation-only
  and does not repeat the same read-only view rows.
- `Settings > Project Picker > Workdirs` is the list/overview entry. Add/remove
  actions live inside that view.
- `Settings > Project Picker > Project Root` shows effective and saved values
  first, then the edit actions, then the explanatory hints.
- `Settings > Keybindings` is the single entry point for keybinding work. The
  page is split into four chips: `Bindings`, `Diagnostic`, `Probe`, and `Init`.
- `Settings > Keybindings > Bindings` is a keybinding discovery surface, not
  only a launch-toggle editor. It must show `Toggle Project Sidebar` with the
  guaranteed `Alt-1` / `M-1` default, plus sidebar-local commands, picker-local
  commands, `Pane navigation`, `Window navigation`, and `Rename` groups or
  equivalent searchable rows.
- Rows that cannot safely be edited still stay visible. Mark them as
  `transport-dependent` or `diagnostic-only` with the delivery path and reason
  instead of hiding them or turning them into unsupported editable aliases.
- `Alt-1..5` are the only guaranteed zero-config launch defaults. `UserN` and
  `CSI-u` are legacy/removal/unsupported targets, not supported fallback
  guidance for Settings, setup, init, or docs.
- The launcher checkout policy applies in Settings: the project sidebar is a
  first-class row, action rows use human-readable labels, internal IDs appear
  only in detail/source/keymap contexts, and runtime footers are status hints,
  not key discovery.
- `Settings > Notifications` owns notification delivery IA. Desktop notification
  mode, AI desktop notification dedupe duration, delivery source diagnostics,
  AI hook quiet policy, in-app queue status, and
  `PROJMUX_NOTIFY_HOOK` visibility live together without mixing mutation
  boundaries.
- `Settings > Notifications > Desktop notifications` owns the desktop
  notification mode. The detail choices are `none`, `notify`, and `raise`.
- `Settings > Notifications > AI notification dedupe` owns the duplicate
  desktop AI notification collapse window. It stores integer seconds and shows
  the effective source; `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` remains the top
  override. The tmux bell fallback keeps its fixed 5 second window.
- `Settings > Notifications > Delivery sources` shows Codex hooks, Claude, and
  tmux producer diagnostics plus copyable install/remove/dry-run commands.
  Settings copies command text only; it does not install or remove external
  notify wiring. The legacy Codex notify source is intentionally omitted from
  Settings.
- `Settings > Notifications > Hook quiet policy` shows Codex/Claude hook
  runtime action values and writes only
  `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json`. It does not
  edit catalog `install` values or run agent install/remove commands.
- `Settings > Labs` keeps experimental toggles, but keybindings no longer have a
  visible Labs row. The hidden compatibility action still redirects to the
  unified Keybindings page.
- `Settings > Labs > Project Hooks` is overview-first. The Labs root opens the
  overview, and the on/off mutation rows live one level deeper.
- `Settings > AI Settings` is view-first. The root contains `Default split
  mode`; the detail contains the `Claude`, `Codex`, and `Shell` choices.
- `Settings > Project > Project recipe` is the functional label for
  `.projmux/config.toml`. Search still matches `config.toml` as an alias.
- `Settings > Project > Project recipe` is view-first. The root contains section
  rows such as `Startup command`, `Kube`, and `Environment`; mutation actions
  such as `Set startup command...`, `Clear startup command`, kube edits, and env
  add/remove rows live inside those details.
- `Settings > Appearance` is view-first. The root opens `Path icon`, `Git icon`,
  and `Notify icon` details. Each detail shows the current mode plus
  immediately selectable off/symbol/emoji preview rows. There is no separate
  `Change` page for icon decoration.

Hooks remain the reference pattern for this IA:

- project-scoped rows stay editable in-app
- global/system rows are read-only in-app
- project overrides are created intentionally from the project surface or
  `projmux hook edit <event> --project`
- `Settings > Project > Hooks` lists hook events only. It does not nest the
  Project recipe row; Project recipe belongs under the Project tab.
- The Settings hook event list includes `send-noti` for both global and project
  hook views.

Shell bootstrap UX is phase-split:

- Phase 1 is complete in this branch: `projmux welcome`, the About-screen
  Welcome entry, and `pending_attach_welcome` state make the guide revisit-able.
- Phase 2 is complete in this branch: the generated projmux shell tmux config
  runs the low-noise `projmux welcome --popup` attach hook, which claims the
  pending marker once and displays the welcome guide after attach.
