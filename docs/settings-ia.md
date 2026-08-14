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
- Every rendered non-empty row value is classified as navigation, actionable,
  or passive information/disabled state and is mapped to a closed owner-loop
  contract before rendering. An unowned value is a Settings error; Enter on a
  passive row is consumed as a no-op.
- `Settings > Project Picker > Workdirs` is the list/overview entry. Add/remove
  actions live inside that view.
- `Settings > Project Picker > Project Root` shows effective and saved values
  first, then the edit actions, then the explanatory hints.
- `Settings > Keybindings` is a single action list plus a simple action detail.
  It does not expose `Bindings`, `Diagnostic`, `Probe`, or `Init` as first-class
  chips/tabs in the Settings root flow.
- `Settings > Keybindings` is a keybinding discovery surface, not only a
  launch-toggle editor. It must show `Toggle Project Sidebar` with the
  guaranteed `Alt-1` / `M-1` default, plus sidebar-local commands, picker-local
  commands, `Pane navigation`, `Window navigation`, and `Rename` groups or
  equivalent searchable rows.
- `Settings > Keybindings > Action` keeps the user-facing edit path small:
  action label, state, a flat Keys list, Options, and a collapsed
  Troubleshooting row. Keys shows only currently active/effective keys plus
  `+ Add key`. Pressing a key row opens key detail, where Remove key and Test
  key live. Options offers Unbind and Reset to default/Use default when
  state-appropriate. Add key opens the default Press a key flow with Cancel and
  Advanced...; typed key-name entry and raw diagnostics live under Advanced. It
  does not expose Default key, Apply State, Delivery, Advanced Delivery,
  key-role replacement, terminal mapping preview, or terminal mapping apply
  rows as always-visible sections.
- Terminal delivery remediation lives outside Settings primary flow. The
  supported order is `projmux shell` first, then `projmux setup`, then
  `projmux setup terminal` for supported terminal adapters.
- Rows that cannot safely be edited still stay visible. Mark diagnostic-only
  rows with the delivery path and reason instead of hiding them or turning them
  into unsupported editable keys. Transport-dependent rows stay visible with
  their default transport key and additive custom-key entry; replacing or
  disabling the transport default is not exposed.
- `Alt-1..5` are the only guaranteed zero-config launch defaults. `UserN` and
  `CSI-u` are legacy/removal/unsupported targets, not supported fallback
  guidance for Settings, setup, init, or docs.
- The launcher checkout policy applies in Settings: the project sidebar is a
  first-class row, action rows use human-readable labels, internal IDs appear
  only in detail/source/keymap contexts, and runtime footers are status hints,
  not key discovery.
- `Settings > Theme` (Global) is the single theme view. There is no separate
  `Effective theme` item: the Global theme view shows each color token's value
  inline. A token set globally (explicit value or via a global preset) shows its
  set/override/preset summary; an UNSET token shows the resolved fallback value
  with a dim swatch, a `(fallback)` label, and a `fallback` source. Resolver
  warnings render as dim info rows after the token rows. Project `.projmux`
  `[theme]` is never resolved or shown here.
- `Settings > Notifications` owns notification delivery IA. Desktop notification
  mode, AI desktop notification dedupe duration, delivery source diagnostics,
  and AI hook quiet policy live together without mixing mutation boundaries.
  The in-app queue is consumed from the statusbar/sidebar, not from a standalone
  Settings row. `PROJMUX_NOTIFY_HOOK` override presence is folded into Delivery
  sources summary/detail instead of appearing as a separate root row.
- `Settings > Notifications > Desktop notifications` owns the desktop
  notification mode. The detail choices are exactly `off` and `notify`. The
  retired `raise` mode is not offered; existing saved `raise` values are read
  as `notify`.
- `Settings > Notifications > AI notification dedupe` owns the duplicate
  desktop AI notification collapse window. It stores integer seconds and shows
  the effective source; `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` remains the top
  override. The tmux bell fallback keeps its fixed 5 second window.
- `Settings > Notifications > Delivery sources` shows Codex, Claude,
  Antigravity, and tmux producer diagnostics, the effective desktop sender
  override state, and copyable install/remove/dry-run commands. Settings copies
  command text only; it does not install or remove external notify wiring. The
  legacy Codex notify source is intentionally omitted from Settings.
- `Settings > Notifications > Hook quiet policy` shows Codex/Claude hook
  runtime action values and writes only
  `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json`. It does not
  edit catalog `install` values or run agent install/remove commands.
- `Settings > Session State > Sidebar startup picker` controls the Alt-1
  project-open startup selector. The saved file remains
  `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sidebar-startup-picker`.
- `Settings > Labs` contains only Live system resources and Project Hooks.
  Keybindings live at `Settings > Keybindings`; Labs has no visible or hidden
  keybindings redirect. The native picker is the product picker, so Labs does
  not render picker source information.
- `Settings > Labs > Live system resources` is a direct global on/off toggle
  for the macOS/Linux/WSL lower-status-row `CPU N%  MEM N%` segment. It defaults
  off, updates live tmux state when toggled, and renders unavailable on
  unsupported platforms. CPU and memory use fixed independent semantic
  thresholds (CPU warning/critical at 70/90; memory at 75/90); the toggle does
  not expose threshold customization. WSL values describe the Linux guest/VM
  view.
- `Settings > Labs > Project Hooks` is overview-first. The Labs root opens the
  overview, and the on/off mutation rows live one level deeper.
- `Settings > AI Settings` is view-first. The root contains `Default split
  mode`, `Enabled agents`, and `Resume picker`; the default-mode detail contains
  the `Claude`, `Codex`, `Antigravity`, `Shell`, and `Selective` choices.
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
- `Settings > Appearance > Language / Locale` is the global/user language
  detail. The root row shows the saved `[ui].locale` value and the currently
  effective locale. The detail shows `Current`, `[ui].locale`, optional
  `PROJMUX_LOCALE` env override, and direct choices for `auto`, `en-US`, and
  `ko-KR`. When `auto` is active it must show the detected source (`LC_ALL`,
  `LC_MESSAGES`, `LANG`, or fallback). Unsupported locale tags must remain
  visible as warnings and fall back to `en-US`.
- Global root descriptions keep ownership explicit: Appearance owns language,
  AI badge style, and status/notification icon decoration; Theme owns presets,
  and color tokens. About describes only the surface it retains.
- `Settings > About` is intentionally compact: Version, Source, update
  status/actions (including Latest, Update state, Installer, and Release notes
  when available), Welcome, and Quit. It does not reproduce static key,
  terminal, dependency, terminal-emulator, or documentation guides. Key
  delivery discovery lives in `projmux setup`, supported terminal remediation
  in `projmux setup terminal`, read-only dependency/runtime diagnostics in
  `projmux doctor`, and broader orientation in Welcome and maintained docs.
- Without an actionable project context, the Project surface renders one
  passive context-guidance row instead of repeating the same disabled reason
  for Trust, Hooks, Project recipe, and Effective merge view. With project
  context, those four rows and Session State retain their existing actions.

Settings mutation feedback follows one transient contract. The next picker
frame inserts one passive `Feedback` row after Back; the row uses the catalogued
`settingsNoopValue`, so Enter cannot create an unknown action. Selecting another
navigation/action clears the old row before that operation runs, and a handled
result replaces it. The inventory includes AI defaults/enabled agents/resume
limits, notification modes/dedupe/hook policy, Appearance and locale choices,
Labs toggles, project roots/workdirs/pins, project hooks/recipe/trust, Theme,
Session State, direct keybinding reset/remove/toggle operations, and About
update apply/check. Typed validation and staged apply failures stay in the
popup instead of being visible only on stdout/stderr.

The generic feedback inventory deliberately excludes Welcome, Quit,
read-only hook/effective/notification diagnostics, Session State preview, and
key capture/probe/diagnostic bodies. Those flows own a viewer, confirmation, or
multi-step output surface; only an actual Settings write at their boundary is
eligible for transient mutation feedback.

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

- `projmux welcome` remains the stdout revisit command.
- `Settings > About > Welcome` opens a visible native viewer independent of
  shell skip state.
- Legacy shell `skip_version` state remains readable for compatibility but no
  longer suppresses the automatic `projmux shell` prompt; release skips live in
  `update-skip.json` and do not hide manual revisit surfaces.
