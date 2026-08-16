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
the live tmux config after the binary changes. `--no-apply` suppresses the
reload only; the new binary still runs to migrate the keymap schema and write
the generated config. See [Keymap schema migration](#keymap-schema-migration).

Shell Upgrade invokes only `projmux update apply`, then reports success or the
specific failure inline and continues into the shell either way (a failed
upgrade is never fatal to shell entry). Shell Skip until next stores the current
latest release tag in `update-skip.json`; the prompt appears again when the
cached latest tag changes. For `source` and unknown installer sources, Upgrade
prints guidance and continues shell entry without applying anything.

The installer is detected from an explicit `PROJMUX_INSTALLER` first, then
inferred from the running binary when unset (npm install tree → `npm`,
`go install …@latest` binary in `$GOBIN`/`$GOPATH/bin`/`~/go/bin` → `go`, a
local `go build`/`make install` `(devel)` binary → `source`). `github-release`
still requires an explicit `PROJMUX_INSTALLER=github-release`.

## Behavior Changes

### Terminal init command removed

The deprecated top-level `projmux init` command and its legacy-only
`--dry-run` flag have been removed. Use the exact replacement
`projmux setup terminal`; it previews by default, and accepts `--apply`,
`--config <path>`, and `--allow-symlink` when those behaviors are needed.

### Keymap schema migration

`~/.config/projmux/keymap.toml` is now versioned by a root `schema_version`
marker. A file without one is v0 and uses the action ids projmux has always
written; `schema_version = 1` uses canonical dotted ids such as
`window.create` and `project-sidebar.runtime.stop`. Both read — the v0 spelling
of every action stays a permanent read alias.

The migration needs no command of its own. Every installer path ends by running
the newly installed binary's `projmux tmux apply`, which migrates first and only
then writes generated config and reloads the live server. An install that never
went through an updater converges on the first Settings key save or the next
`projmux config apply`.

Before it replaces anything it writes `keymap.toml.pre-v1-<digest>.bak`. Running
the migration again on an already-migrated file writes no bytes. If it cannot
proceed — most often because a file carries both a legacy and a canonical table
for one action with different keys — nothing is written at all and the report
says so.

**Downgrading to a projmux that predates the schema:** restore the backup first.

```sh
cp ~/.config/projmux/keymap.toml.pre-v1-<digest>.bak ~/.config/projmux/keymap.toml
```

An older binary reads `schema_version` as an unsupported root key and refuses
the file, so restoring before installing avoids the failure.
[docs/keybindings.md](keybindings.md#schema-versions) has the id table and the
full ordering.

### Pane rename keymap action ID removed

The deprecated `rename-pane-topic` keybinding action ID has been removed. If
`~/.config/projmux/keymap.toml` still contains
`[bindings.rename-pane-topic]`, rename that table to
`[bindings.rename-pane-label]` before running Settings or
`projmux tmux apply`. Projmux now rejects the stale table with that exact
replacement instead of silently applying its keys to the user-label action.

This removal does not change `projmux ai topic set/clear`: those advanced CLI
commands continue to write only AI topic and manual-ownership state. User pane
rename continues to write only the pane label, and raw pane title remains an
independent fallback.

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

### Foreground split

The old broad `foreground` theme key is now split into two clearer public keys:
`text_primary` for primary native content text, and `chrome_foreground` for
frame/title/search/status/window chrome foreground roles. Existing global
`foreground` values still work as a legacy alias/fill and will feed both split
roles unless either new key is set explicitly. Settings shows and writes the new
split names instead of encouraging new `foreground` writes.

If your previous `foreground` override made both content and chrome change
together, you do not need to migrate immediately. To tune them separately, copy
the value into `text_primary` and/or `chrome_foreground`, then remove
`foreground` when you no longer need the compatibility fill.

### New public theme keys

Seven new public `[theme]` keys are now available: `text_primary`,
`chrome_foreground`, `progress`, `success`, `action_required` (AI/status
colors), `pane_active_bg` (active-pane tint), and `focus` (active-pane border).
Leaving a key unset keeps the historical built-in color; setting it repaints the
matching role. `action_required` is independent of `critical` — repainting
`critical` never changes it. The full public token set is documented in
`docs/configuration.md` and `docs/theme-palette.md`.

The active-pane tint (`pane_active_bg`) defaults to the terminal background in
the built-in `projmux` preset; set it to a concrete color when the active pane
should visibly sink. The active-pane border (`focus`) defaults to cyan
`colour51`. Both apply only to tmux pane chrome.

Built-in presets are intentionally small: `projmux`, `high-contrast`,
`blue-hour`, `carbon-violet`, `daylight`, `ember`, `forest`, and `rose`
(`daylight` is the fully-light one). Terminal-default
backgrounds are configured per token with the `default` sentinel rather than
through separate terminal preset variants.

### Pane body vs popup backgrounds

The general pane, bottom status bar, and popup/native frame backgrounds are now
driven by separate public tokens. The pane body follows `background` (unset
keeps the terminal default), the status bar follows `status_background`, and
native popup bodies plus the settings/notify/recent/picker frames follow
`surface`. Because the `surface` fallback equals `background`, leaving those two
unset looks exactly as before; set `surface` separately to make popups read as a
distinct surface from the pane body. Set `status_background` separately to
repaint only the bottom status bar.

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
npm install -g projmux@latest
projmux tmux apply
```

`npm install -g projmux@latest` is used instead of `npm update -g projmux`
because `npm update -g` honors the installed semver range and frequently
refuses to move a global install across a newer minor/major release, leaving it
stuck on an old version. `install -g …@latest` always fetches the newest
published release and re-resolves the per-platform optional dependency.

The npm shim sets `PROJMUX_INSTALLER=npm` so projmux can detect this path
automatically. Even when the shim is bypassed, projmux infers the npm install
from the binary path (`node_modules/@projmux/...`). You can also update manually
with `npm install -g projmux@latest`.

## Go Installs

If you installed with `go install`, `projmux update apply` delegates to the
same atomic replacement flow as `projmux upgrade`:

```sh
projmux upgrade                                  # @latest, replace + apply
projmux upgrade --ref @v0.4.0                    # pin a specific tag
projmux upgrade --ref @main                      # track a branch
projmux upgrade --target /usr/local/bin/projmux  # replace another path
projmux upgrade --no-apply                       # migrate + write config, skip the live reload
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
then runs the new binary's `projmux tmux apply` — or `tmux apply --no-reload`
when `--no-apply` is set, so the keymap schema migration still happens.

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
