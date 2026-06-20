# Upgrading

projmux has two update surfaces:

- `projmux shell` reads the cached release status before opening the app. When
  the cache is missing or stale, startup attempts a short best-effort refresh
  and continues if it fails. When a newer release is available, the shell
  welcome offers Continue, Upgrade, and Skip until next actions.
- Settings > About > Update shows the current version, detected installer,
  cached latest version, Check Updates, and Update Now actions.

Refresh the cache explicitly when you want a full foreground GitHub Releases
check:

```sh
projmux update check
```

Apply the cached update through the detected installer:

```sh
projmux update apply
```

Use `--dry-run` to see the planned action and `--no-apply` to skip reloading
the live tmux config after the binary changes.

Shell Upgrade invokes only `projmux update apply`. Shell Skip until next stores
the current latest release tag in `update-skip.json`; the prompt appears again
when the cached latest tag changes. For `source` and unknown installer sources,
Upgrade prints guidance and continues shell entry without applying anything.

## Behavior Changes

### Theme is now global-only

Theme is a global user preference. The effective theme resolves from the global
`[theme]` in `~/.config/projmux/config.toml` plus a built-in fallback preset.

If you previously set a `[theme]` section in a project's `.projmux/config.toml`,
it is now **deprecated and ignored** — it no longer overrides the global theme
and does not affect the native picker, statusbar, popups, or any tmux chrome.
The project `[theme]` keys are left in the file untouched (no warning, no
removal); they simply have no effect.

To restore your previous look, copy the values into the global
`~/.config/projmux/config.toml` `[theme]` section, or edit them through
Settings > Theme. Settings no longer exposes a Project theme editor, and the
separate Effective theme view has been merged into the Global theme view: each
token row now shows its resolved value inline, with unset tokens shown as their
dimmed `(fallback)` value.

### New public theme keys

Five new public `[theme]` keys are now available: `progress`, `success`,
`action_required` (AI/status colors), `pane_active_bg` (active-pane tint), and
`focus` (active-pane border). Leaving a key unset keeps the historical built-in
color; setting it repaints the matching chrome. `action_required` is independent
of `critical` — repainting `critical` never changes it. The full public token
set is documented in `docs/configuration.md` and `docs/theme-palette.md`.

The active-pane tint (`pane_active_bg`) defaults to `colour234`, one tone darker
than the base background so the active pane visibly sinks; the active-pane
border (`focus`) defaults to cyan `colour51`. Both apply only to tmux pane
chrome.

### Pane body vs popup backgrounds

The general (pane) background and the popup/chrome background are now driven by
separate public tokens. The pane body follows `background` (unset keeps the
terminal default), while the status bar, native popup bodies, and the
settings/notify/recent/picker frames follow `surface`. Because the `surface`
fallback equals `background`, leaving both unset looks exactly as before; set
them to different values to make popups read as a distinct surface from the pane
body.

### Theme font keys removed

The `[theme]` `font_family` and `font_size` keys were removed. They never
applied to the terminal — tmux/ANSI rendering cannot force a font family or
size across terminal emulators — so they only stored and displayed a desired
value that was always reported as `not applied`.

Leftover `font_family` / `font_size` keys in a global or project
`config.toml` are accepted but ignored: they no longer parse into the theme,
appear in Settings, or affect any surface. You can delete them at your
convenience. Set your terminal font through your terminal emulator's own
profile settings instead.

## npm Installs

The recommended install path is:

```sh
npm install -g projmux
```

For npm-managed installs, `projmux update apply` runs:

```sh
npm update -g projmux
projmux tmux apply
```

The npm shim sets `PROJMUX_INSTALLER=npm` so projmux can detect this path
automatically. You can also update manually with `npm update -g projmux`.

## Go Installs

If you installed with `go install`, `projmux update apply` delegates to the
same atomic replacement flow as `projmux upgrade`:

```sh
projmux upgrade                                  # @latest, replace + apply
projmux upgrade --ref @v0.4.0                    # pin a specific tag
projmux upgrade --ref @main                      # track a branch
projmux upgrade --target /usr/local/bin/projmux  # replace another path
projmux upgrade --no-apply                       # skip 'projmux tmux apply'
projmux upgrade --dry-run                        # print the steps only
```

`projmux upgrade` reinstalls via `go install`, atomically replaces the active
file, and reapplies the live tmux config so a running `-L projmux` server picks
up new bindings without a restart.

The command reads `PROJMUX_PROJDIR` from the calling shell and memoizes the
primary path to `~/.config/projmux/projdir`, so the new binary keeps the same
project root context as the one it replaces.

To switch the saved project root during the upgrade:

```sh
PROJMUX_PROJDIR=/new/path projmux upgrade

# Multi-path also works; only the primary entry is persisted.
PROJMUX_PROJDIR="/main/repos:/secondary/repos" projmux upgrade
```

## GitHub Release Installs

When `PROJMUX_INSTALLER=github-release`, `projmux update apply` downloads the
latest matching `projmux_<version>_<goos>_<goarch>.tar.gz` asset from GitHub
Releases, extracts the binary, atomically replaces the current executable, and
then runs `projmux tmux apply` unless `--no-apply` is set.

Set the installer explicitly if you manage a release binary outside npm or Go:

```sh
export PROJMUX_INSTALLER=github-release
```

## Source Checkouts

Source installs are updated from the checkout:

```sh
git pull --ff-only
make install
```

`projmux update apply` reports an actionable error for
`PROJMUX_INSTALLER=source` because source trees may have local changes and need
the repository's normal review and test flow.
