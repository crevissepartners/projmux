# Theme Palette

This document records the built-in fallback palette that native projmux UI
surfaces use before project or global theme settings exist. The source of
truth in code is `internal/theme/palette.go`.

## Scope

The fallback palette is a semantic token layer, not a user configuration
schema. It gives current renderers shared names for the colors stabilized by
the Visual palette baseline work:

- Native picker truecolor SGR tokens.
- Native sidebar and chip-strip 256-color SGR tokens.
- Tmux statusbar and generated-config color tokens.
- Settings/action/state/trust/attention helper tokens.

Future Theme settings should resolve project/global values into this token
shape, then keep the built-in values as the final fallback. This phase does not
add `config.toml` fields, a resolver, a Settings editor, presets, import, or
export.

## Mapping Policy

Native picker rows can emit truecolor SGR, while tmux statusbar/config strings
must use tmux color specs. The fallback therefore stores both forms when a
role crosses surfaces.

Rules:

- Truecolor tokens keep exact SGR strings for native picker chrome and
  Settings rows.
- Tmux tokens keep `colourN` strings where tmux owns rendering.
- Native chip/sidebar badge tokens use 256-color SGR when they intentionally
  mirror tmux colors.
- Output compatibility wins inside this baseline. For example, the kube
  segment keeps tmux's named `red` and `blue` behind semantic tokens until that
  segment gets a separate redesign.
- Renderers should reference semantic names instead of spelling color literals
  directly. Test fixtures may still pin rendered escape strings.

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
| `accent.attention` | notify sidebar `colour204` family | `colour53`, `colour225`, `colour204`, project `colour90` |
| `accent.ai` | notify agent `colour37` family | `colour37` bg / `colour121` fg |
| `state.progress` | `255;204;102`; switch attention/busy dot `colour220` | `colour220` |
| `state.warning` | usage/status popup ANSI 256 wrapper | `colour214` |
| `state.danger` | `255;107;107` | `colour160` |
| `state.success` | settings/trust green families | `colour72`, `colour151` |
| `state.ahead` | switch/git metadata ANSI 256 wrapper | `colour153` |

Surface-specific tokens:

| Surface | Tokens |
| --- | --- |
| Native picker | current row, titlebar, rule, pointer, highlight, muted text, chip active/inactive/disabled |
| Statusbar row 1 | session identity, cwd secondary text, divider, git branch block, git dirty/staged/ahead/behind, settings action chip, clock |
| Notify HUD/sidebar | line bg/fg, project badge, info/warn/crit/stale/gone badges, AI agent badge, count/age text |
| Usage HUD/popup | OK/warning/critical/over-limit bars and numbers, empty cells, muted sync age |
| Settings | add/type/open action, destructive remove/quit, back/cancel, info/read-only, dim description, root action/dim rows, trust trusted/stale/untrusted |
| Switch picker cards | path metadata, active/inactive git branch badges, statusbar-like window tabs, inline attention/progress dots |

## Current Literal Inventory

After the Phase 3 token pass, raw color values intentionally remain in:

- `internal/theme/palette.go`, the fallback palette source of truth.
- Unit and golden fixtures that assert exact rendered output.
- `internal/app/welcome.go`, which is outside the Visual palette baseline
  Phase 3 scope and should move only in welcome/update policy work.
- Older switch picker test fixtures that pin legacy compatibility output.

The converted implementation paths include:

- `internal/ui/projmuxpicker/ansi.go`
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
