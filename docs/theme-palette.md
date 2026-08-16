# Theme Palette

This document records the implemented theme contract and the built-in
fallback palette that native projmux UI surfaces use when no global user theme
is configured. The source of truth for current fallback values in code is
`internal/theme/palette.go`, and the source of truth for the resolver and the
semantic role map is `internal/theme/resolve.go`.

## Scope

The fallback palette is a semantic token layer. The effective theme is a global
user theme resolved from `~/.config/projmux/config.toml`, followed by the
built-in fallback values from `internal/theme/palette.go`. The resolver derives
a semantic role map (`RenderRoles` for tmux chrome, `ANSIRoles` for native ANSI
surfaces) from the effective theme, and renderers consume those roles instead of
bare palette literals.

- Native picker truecolor SGR tokens.
- Native sidebar and chip-strip 256-color SGR tokens.
- Tmux statusbar and generated-config color tokens.
- Settings/action/state/trust/attention helper tokens.

Renderer adapters apply resolver-backed background and `chrome_foreground`
colors to native picker frame chrome and to tmux status/window background tokens
when an `EffectiveTheme` is supplied by the caller. Fallback-sourced foreground,
status, and state fields still render through their historical constants, while
pane and popup backgrounds may intentionally inherit the terminal default.
Project `.projmux/config.toml` `[theme]` is deprecated and ignored: it is not an
effective theme source, and the resolver and Settings treat any leftover project
`[theme]` keys as inert migration data. Theme
marketplace/import/export and Visual palette reselection remain out of scope.

## Resolver Token Inventory

The public resolver inventory is intentionally smaller than the current
renderer literal inventory. Surface-specific renderers map their detailed roles
onto these stable names:

| Token | Meaning | Shared surfaces |
| --- | --- | --- |
| `background` | inactive pane body background | tmux inactive panes via `window-style` |
| `surface` | popup/native frame base background | tmux popup body, native picker rows, frame titlebar/rule, settings popup |
| `status_background` | bottom status bar background | tmux `status-style` background |
| `surface_active` | selected/current row or active chip surface | native picker current row, frame chips, statusbar active window |
| `chrome_foreground` | app chrome readable text | native picker frame/title/search chrome, tmux status/window foregrounds, popup body style |
| `text_primary` | primary content text | settings/info rows and native terminal-rendered content text |
| `foreground` | legacy alias/fill for split foreground tokens | accepted in config for compatibility; Settings uses `text_primary` / `chrome_foreground` |
| `muted` | secondary text, divider, disabled or stale details | picker metadata, titlebar rule, notify age/stale, settings descriptions |
| `accent` | pointer, primary action, highlight, active affordance | native picker pointer/highlight, settings actions, chips |
| `critical` | destructive/error/critical state | settings remove/quit, notify critical badge, statusbar critical usage |
| `warning` | progress, pending, warning, busy state | AI busy/thinking indicators, notify pending title, usage warning |
| `progress` | in-progress / working state color | AI progress badge, statusbar progress, state.progress |
| `success` | completed / success state color | AI success badge, state.success |
| `action_required` | AI needs-input/approval badge color | AI action-required badge (independent of `critical`) |
| `pane_active_bg` | active-pane background tint | active-pane window-active-style tint (tmux pane chrome) |
| `focus` | active-pane border color | active-pane border (tmux pane chrome) |

`text_primary` and `chrome_foreground` split the old broad foreground behavior:
changing primary content text no longer repaints frame/title/search/border/status
chrome as a side effect. The legacy `foreground` key remains readable; it fills
both split fields unless either split field is explicitly set.

`progress`, `success`, and `action_required` are public `[theme]` keys, no
longer renderer-only candidates. The fallback contract is progress yellow,
success green, action-required amber-orange. These roles are separate from
notify queue severity and desktop notification urgency; an AI approval row can
be `critical` in the notify queue while the live status badge uses
`action_required`, not red. `action_required` is independent of `critical`:
repainting `critical` never changes it. `critical` remains reserved for error,
failure, destructive, over-limit, or risk states. `pane_active_bg` and `focus`
are also public keys driving the active-pane tint and border (tmux-only pane
chrome, with no ANSI/native role).

Renderer-only role names such as `accent.ai`, `state.progress`, `git.branch`,
and trust colors remain in `internal/theme/palette.go` until Phase 2+ maps each
surface to the resolver tokens. The key product contract for this phase is that
native picker, frame titlebar, chips, statusbar, notify sidebar, and settings
popup all consume a shared effective token set instead of independently
choosing colors.

Font is not part of this token inventory. The earlier `font_family` and
`font_size` theme keys were removed in Phase 1b: tmux/ANSI rendering cannot
force a font family or size across terminal emulators, so the values never
applied to the terminal. Leftover font keys in an existing config are accepted
but ignored. See `docs/upgrading.md`.

## Mapping Policy

Native picker rows can emit truecolor SGR, while tmux statusbar/config strings
accept tmux style color specs. The resolver therefore carries exact hex plus a
256-color approximation for renderer paths that still need it.

Rules:

- Truecolor tokens keep exact `#RRGGBB` values and can be converted to
  foreground/background SGR fragments such as `38;2;R;G;B` or `48;2;R;G;B`.
- Tmux style roles use exact `#RRGGBB` for explicit theme/preset colors and
  keep historical `colourN` strings only for fallback literals. Generated tmux
  config declares `xterm*:RGB` in `terminal-features` so capable terminals render
  those exact colors instead of tmux downsampling them to xterm-256 colors.
- The built-in `projmux` fallback keeps text/accent/state tokens from the
  established palette while pane and popup backgrounds ride the terminal
  default.
- Explicit `#RRGGBB` overrides also retain the closest xterm 256-color
  `colourN` token for 256-color-only renderer roles.
- Native chip/sidebar badge tokens use 256-color SGR when they intentionally
  mirror tmux colors.
- Renderers should reference semantic names instead of spelling color literals
  directly. Test fixtures may still pin rendered escape strings.

## Resolver Contract

The effective theme resolves theme fields from:

1. Global `~/.config/projmux/config.toml`
2. Built-in fallback preset `projmux`

Rules:

- Missing global values fall back to built-in values.
- A preset fills missing color tokens in the global layer.
- Explicit global color tokens override preset colors.
- Legacy `foreground` fills `text_primary` and `chrome_foreground` unless either
  split key is explicitly set.
- An unknown preset invalidates only the global layer.
- An invalid color invalidates only the global layer.
- Every effective field reports `global` or `fallback` as its source label.

### Terminal default sentinel

The `background`, `surface`, `status_background`, `surface_active`, and
`pane_active_bg` tokens accept the special value `default` ("Terminal default"
in Settings). It pins that surface to the terminal background instead of a
concrete color, even when a preset is selected:

- Priority is **explicit `default` > preset fill > unset (fallback)**. Setting a
  token to `default` overrides the preset's color for that token while leaving
  every other token preset-filled.
- On the tmux side the derived roles emit `bg=default` (for example
  `window-style "bg=default"` from `background`, popup body style from
  `surface`, `window-active-style "bg=default"` from `pane_active_bg`, and
  `status-style bg=default` from `status_background`).
- On the ANSI side the corresponding surface emits no background sequence (no
  `48;2;…` / `48;5;…`), so the terminal background shows through.
- `default` is only valid on `background` / `surface` / `status_background` /
  `surface_active` / `pane_active_bg`. On any other token it is treated as an
  invalid hex color and invalidates the global layer (same as any other invalid
  color).

Historical project `[theme]` values in `.projmux/config.toml` are migration
data only. They are not a current or target source in this contract.

Built-in preset config values are:

- `projmux`
- `high-contrast`
- `blue-hour`
- `carbon-violet`
- `daylight`
- `ember`
- `forest`
- `rose`
- `blue-hour` — dark theme tuned around a terminal blue accent; it uses a dark
  blue-tinted inactive pane body, black popup surface, deep navy status bar,
  and a deep indigo active pane tint.
- `carbon-violet` — charcoal/violet dark theme with darker popup/status
  surfaces and a black active pane tint.
- `daylight` — the fully-light preset: warm paper pane/popup surfaces, an
  explicit light status bar, dark slate text, a teal accent, a strong blue
  focus border, and darkened saturated state colors (red/amber/blue/green/
  orange) tuned to stay distinguishable on light chrome after 256-color
  quantization. The active pane tint sits one tone darker than the pane body
  (the inverse of the dark presets).
- `high-contrast` — black surfaces, white text, a dark blue active surface,
  near-black active pane tint, vivid cyan focus, and bright
  cyan/yellow/red/green state colors.

Preset colors paint projmux chrome only — the tmux status bar, picker/popup
frames, pane borders, and the pane background tint. Pane contents (shell
prompt, editor, command output) follow the terminal/shell theme, so a fully
light experience with `daylight` also requires a light terminal theme.

`daylight` palette values:

| Token | Hex | Nearest tmux |
| --- | --- | --- |
| `background` | `#f2efe9` | `colour255` |
| `surface` | `#faf7f1` | `colour255` |
| `status_background` | `#dfdad0` | `colour188` |
| `surface_active` | `#cfe0f0` | `colour189` |
| `chrome_foreground` | `#3a4550` | `colour238` |
| `text_primary` | `#2c3338` | `colour236` |
| `foreground` | `#2c3338` | `colour236` |
| `muted` | `#6b7680` | `colour243` |
| `accent` | `#0f766e` | `colour6` |
| `critical` | `#c62828` | `colour160` |
| `warning` | `#b45309` | `colour130` |
| `progress` | `#1d4ed8` | `colour26` |
| `success` | `#15803d` | `colour29` |
| `action_required` | `#d97706` | `colour172` |
| `pane_active_bg` | `#e8e4dc` | `colour254` |
| `focus` | `#2563eb` | `colour26` |

## Fallback Inventory

Chrome and text:

| Role | Native SGR | Tmux |
| --- | --- | --- |
| `surface.active` | `48;2;44;56;61` with selected white text | window active `colour240` / `colour231` |
| `surface.raised` | terminal-default background with `216;224;228` text | popup/native frame surface |
| `text.primary` | `216;224;228` | `colour231` or `colour254` for identity text |
| `text.secondary` | `164;176;182` | `colour245` |
| `text.muted` | `117;132;140` or ANSI dim | `colour244`, `colour238`, `colour240` for low-signal blocks |

Accents and state:

| Role | Native SGR | Tmux |
| --- | --- | --- |
| `accent.identity` | not emitted directly today | `colour60` bg / `colour254` fg |
| `accent.action` | `141;205;142`, strong `122;199;173` | `colour29` bg / `colour230` fg |
| `accent.attention` | notify HUD background/project family | `colour53`, project `colour90` |
| `accent.ai` | notify agent `colour37` family | `colour37` bg / `colour121` fg |
| `state.progress` | `255;204;102`; switch attention/busy dot, pane-border in-progress badge, and pending notify title/bell/badge `colour220` | `colour220` |
| `state.action_required` | AI approval/input-required status badge amber-orange; currently aliases the established warning token | `colour214` |
| `state.warning` | usage/status popup warning ANSI 256 wrapper; non-AI warning chrome | `colour214` |
| `state.danger` | `255;107;107` | `colour160` |
| `state.success` | settings/trust green families; pane-border response-complete badge | `colour72`, `colour151` |
| `state.ahead` | switch/git metadata and notify age ANSI 256 wrapper | `colour153` |
| `git.branch` | switch/sidebar branch badge uses the statusbar git branch block colors | `colour30` bg / `colour231` fg |

Surface-specific tokens:

| Surface | Tokens |
| --- | --- |
| Native picker | current row, titlebar, rule, pointer, highlight, muted text, chip active/inactive/disabled |
| Statusbar row 1 | session identity, cwd secondary text, divider, git branch block, git dirty/staged/ahead/behind, settings action chip, clock |
| App pane borders | semantic AI badge marker style (`dot`, `emoji`, or spacing-preserving `off`) plus action-required/success/progress state color |
| Notify HUD/sidebar | line bg/fg, project badge, info/warn/crit/stale/gone badges, AI agent badge, count/age text |
| Usage HUD/popup | OK/warning/critical/over-limit bars and numbers, empty cells, muted sync age |
| Settings | add/type/open action, destructive remove/quit, back/cancel, info/read-only, dim description, root action/dim rows, trust trusted/stale/untrusted |
| Switch picker cards | path metadata, active/inactive git branch badges aligned with statusbar git branch block colors, statusbar-like window tabs, inline attention/progress dots |

Active-pane focus: tmux draws a single shared border between adjacent panes, so
a full active-pane rectangle (e.g. tinting the whole active pane edge-to-edge)
is not guaranteed. Active focus is instead reinforced by the `pane-border-status
top` topic line, an active-pane border (`pane-active-border-style`, fallback
cyan `colour51`, the public `focus` token), and a subtle dark background tint
applied via `window-active-style` (fallback `colour234` — one tone darker than
the base `colour235` so the active pane visibly sinks; the public `pane_active_bg`
token). Inactive panes keep the terminal default background via `window-style
"bg=default"` unless the public `background` token is set.

Pane body vs popup vs status background: the general pane body, popup/native
frame background, and bottom status bar are derived from separate public tokens.
The pane body follows `background` (tmux `window-style`; unset keeps
`bg=default`), popup/native frames follow `surface`, and the bottom status bar
follows `status_background` (tmux `status-style`; unset keeps `colour235`).
Built-in presets fill `status_background` from their surface color to preserve
the old look, but an explicit `surface` override no longer repaints the bottom
status line.

## Current Literal Inventory

After the Phase 3 token pass, raw color values intentionally remain in:

- `internal/theme/palette.go`, the fallback palette source of truth.
- Unit and golden fixtures that assert exact rendered output.
- `internal/app/welcome.go`, which is outside the Visual palette baseline
  Phase 3 scope and should move only in welcome/update policy work.
- Older switch picker test fixtures that pin legacy compatibility output.

The converted implementation paths include:

- `internal/ui/projmuxpicker/ansi.go`
- `internal/ui/projmuxpicker/frame.go`
- `internal/app/tmux.go`
- `internal/app/status.go`
- `internal/app/statusbar.go`
- `internal/app/statusbar_decoration.go`
- `internal/app/notify.go`
- `internal/app/settings.go`
- `internal/app/settings_render.go`
- `internal/app/trust.go`
- `internal/app/usage.go`
- `internal/core/usage/hud.go`
- `internal/ui/render/switch.go`
- `internal/ui/render/popup.go`
- `internal/ui/render/switch_preview.go`
