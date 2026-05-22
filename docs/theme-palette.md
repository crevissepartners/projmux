# Theme Palette

This document records the built-in fallback palette that native projmux UI
surfaces use before project or global theme settings exist. The source of
truth in code is `internal/theme/palette.go`.

## Scope

The fallback palette is a semantic token layer. Theme settings resolve project
and global config into the resolver-facing token inventory below, then fall
back to the built-in values from `internal/theme/palette.go`.

- Native picker truecolor SGR tokens.
- Native sidebar and chip-strip 256-color SGR tokens.
- Tmux statusbar and generated-config color tokens.
- Settings/action/state/trust/attention helper tokens.

Renderer adapters apply resolver-backed background/foreground colors to native
picker frame chrome and to tmux status/window background tokens when an
`EffectiveTheme` is supplied by the caller. Fallback-sourced fields still
render through the historical constants so built-in default output remains
byte-identical. Settings and native project picker surfaces load `[theme]`
values from global and project config through the shared effective-theme source.
Theme marketplace/import/export and Visual palette reselection remain out of
scope.

## Resolver Token Inventory

The public resolver inventory is intentionally smaller than the current
renderer literal inventory. Surface-specific renderers map their detailed roles
onto these stable names:

| Token | Meaning | Shared surfaces |
| --- | --- | --- |
| `background` | base popup/sidebar/status surface background | native picker, frame titlebar, notify sidebar, settings popup, statusbar |
| `surface` | raised or inactive chrome surface | frame titlebar, chips, switch cards, settings popup |
| `surface_active` | selected/current row or active chip surface | native picker current row, frame chips, statusbar active window |
| `foreground` | primary readable text | native picker, titlebar, statusbar, notify sidebar, settings popup |
| `muted` | secondary text, divider, disabled or stale details | picker metadata, titlebar rule, notify age/stale, settings descriptions |
| `accent` | pointer, primary action, highlight, active affordance | native picker pointer/highlight, settings actions, chips |
| `critical` | destructive/error/critical state | settings remove/quit, notify critical badge, statusbar critical usage |
| `warning` | progress, pending, warning, busy state | AI busy/thinking indicators, notify pending title, usage warning |

Renderer-only role names such as `accent.ai`, `state.progress`, `git.branch`,
and trust colors remain in `internal/theme/palette.go` until Phase 2+ maps each
surface to the resolver tokens. The key product contract for this phase is that
native picker, frame titlebar, chips, statusbar, notify sidebar, and settings
popup all consume a shared effective token set instead of independently
choosing colors.

Font is not part of this universal token inventory. `font_family` and
`font_size` are resolved as terminal capability/profile hints: projmux can
store and display the desired value, but tmux/ANSI rendering cannot force a
font family or size across terminal emulators. In environments without a
supported terminal font adapter, projmux reports the desired font as
`not applied` instead of treating storage as a successful font change.

## Mapping Policy

Native picker rows can emit truecolor SGR, while tmux statusbar/config strings
must use tmux color specs. The resolver therefore carries both forms for each
color token.

Rules:

- Truecolor tokens keep exact `#RRGGBB` values and can be converted to
  foreground/background SGR fragments such as `38;2;R;G;B` or `48;2;R;G;B`.
- Tmux tokens keep `colourN` strings where tmux owns rendering.
- The built-in `projmux-dark` fallback uses the established ANSI and tmux
  tokens from `internal/theme/palette.go` to preserve current output.
- Explicit `#RRGGBB` overrides keep exact truecolor and derive the closest
  xterm 256-color `colourN` token for tmux surfaces.
- Native chip/sidebar badge tokens use 256-color SGR when they intentionally
  mirror tmux colors.
- Output compatibility wins inside this baseline. For example, the kube
  segment keeps tmux's named `red` and `blue` behind semantic tokens until that
  segment gets a separate redesign.
- Renderers should reference semantic names instead of spelling color literals
  directly. Test fixtures may still pin rendered escape strings.

## Resolver Contract

Theme resolution is field-by-field after validating each layer:

1. Project `.projmux/config.toml`
2. Global `~/.config/projmux/config.toml`
3. Built-in fallback preset `projmux-dark`

Rules:

- Project values override global values for the same field.
- Missing or `inherit` project values fall back to global values.
- Missing global values fall back to built-in values.
- A preset fills missing color tokens in its own layer.
- Explicit color tokens in the same layer override preset colors.
- An unknown preset invalidates only that layer and emits a warning.
- An invalid color, `font_family`, or `font_size` invalidates only that layer
  and emits a warning.
- Every effective field reports `project`, `global`, or `fallback` as its
  source label.

Built-in preset config values are:

- `projmux-dark`
- `midnight`
- `forest`
- `rose`
- `high-contrast`

## Fallback Inventory

Chrome and text:

| Role | Native SGR | Tmux |
| --- | --- | --- |
| `surface.active` | `48;2;44;56;61` with selected white text | window active `colour240` / `colour231` |
| `surface.raised` | `48;2;24;34;38` with `216;224;228` text | window inactive `colour235` / `colour245` |
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
| `state.warning` | usage/status popup ANSI 256 wrapper; pane-border approval/input-required badge | `colour214` |
| `state.danger` | `255;107;107` | `colour160` |
| `state.success` | settings/trust green families; pane-border response-complete badge | `colour72`, `colour151` |
| `state.ahead` | switch/git metadata and notify age ANSI 256 wrapper | `colour153` |
| `git.branch` | switch/sidebar branch badge uses the statusbar git branch block colors | `colour30` bg / `colour231` fg |

Surface-specific tokens:

| Surface | Tokens |
| --- | --- |
| Native picker | current row, titlebar, rule, pointer, highlight, muted text, chip active/inactive/disabled |
| Statusbar row 1 | session identity, cwd secondary text, divider, git branch block, git dirty/staged/ahead/behind, settings action chip, clock |
| App pane borders | semantic AI badge marker style (`dot`, `emoji`, or spacing-preserving `off`) plus warning/success/progress state color |
| Notify HUD/sidebar | line bg/fg, project badge, info/warn/crit/stale/gone badges, AI agent badge, count/age text |
| Usage HUD/popup | OK/warning/critical/over-limit bars and numbers, empty cells, muted sync age |
| Settings | add/type/open action, destructive remove/quit, back/cancel, info/read-only, dim description, root action/dim rows, trust trusted/stale/untrusted |
| Switch picker cards | path metadata, active/inactive git branch badges aligned with statusbar git branch block colors, statusbar-like window tabs, inline attention/progress dots |

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
