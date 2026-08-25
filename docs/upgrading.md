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
the live tmux config after the binary changes. `--no-apply` suppresses live
tmux access only; the new binary still migrates the keymap schema and
marker-owned provider files, then writes the generated config. See
[Keymap schema migration](#keymap-schema-migration) and
[Managed Agent hook producer migration](#managed-agent-hook-producer-migration).

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

### Registry schema v2 final Window anchors

The unreleased intermediate schema-v2 Window field `primaryPaneRef` has been
replaced by the final v2 shape:

- `anchorPaneRef` is required and names a same-Window shell or managed Agent
  Pane.
- `defaultShellPaneRef` is optional; when present it names a direct
  Window-owned shell Pane.

The on-disk `schemaVersion` remains `2` because the intermediate shape was not
published. A schema-v1 Registry migrates directly to these fields. An
intermediate-v2 Registry is identified by raw field presence and normalized
under the Registry lock. The migration publishes an exact mode-0600 backup and
a checksum/repair report before staging and atomically replacing the Registry.
Mixed legacy/final fields, mixed Window authorities, dangling or cross-Window
anchors, and invalid default shells fail closed without changing the source.
Repeating migration on final-v2 is a byte-level no-op.

The final writer never emits `primaryPaneRef`. A matching intermediate
pre-release validator therefore sees final-v2 as invalid because its required
legacy authority is absent. Not every old read surface necessarily runs that
validator: an old command may ignore the new fields and return a partial view.
That is evidence of incompatibility, not permission to proceed. Operators must
refuse a binary-only downgrade before installation and must not permit the old
binary to write final-v2 bytes. To roll back during the prerelease window, stop
the final writer, restore the exact pre-normalization Registry backup, and
restore the matching intermediate binary as a pair. Snapshots are not migration
or rollback inputs and are never rewritten by this normalization.

Rollback rehearsal is byte-oriented: record the backup path, mode, SHA-256,
and matching pre-release binary revision; stop every final-v2 writer; atomically
restore those exact intermediate-v2 bytes; verify their checksum; install the
recorded matching binary; then run its read-only validation before permitting a
write. Installing only the intermediate binary while leaving final-v2 Registry
bytes in place is deliberately rejected and is not a rollback procedure.

### `create` is resource-backed on every spelling

**Breaking.** `create pane`, `create agent`, and the `create
codex|claude|antigravity` shortcuts no longer have two product models behind the
presence of `--project`. Previously an invocation *with* `--project` created
Registry resources and materialized them detached, while an invocation *without*
it ran a runtime-only split of the current tmux window that created no Projmux
resource and moved the client. That second model is gone.

What changes for an existing invocation:

| Invocation | Before | Now |
| --- | --- | --- |
| `create codex --placement right` inside a managed Project | Runtime-only split of the current tmux window; no Registry resource; the client followed the new pane | Creates an Agent and its managed Pane below the **active managed Project's active Window**, anchored on the active Pane, materialized detached; the client does not move |
| `create codex -w hi --create-window` | `flag provided but not defined: -w` | Creates Window `hi` under the active managed Project, then the Agent and its managed Pane inside it |
| `create pane --placement down` inside a managed Project | Runtime-only shell split | Creates a Pane resource below the active Window and splits it detached |
| Any `create` inside Home, a control session, an unattributed or foreign pane | Runtime-only split | Exit `2` naming `--project`, with zero Registry writes and zero tmux mutations |
| Any `create` from outside tmux with no `--project` | Runtime-only split against the default server | Exit `2` naming `--project`; no server is probed |
| Any `create` with an explicit `--project` | Resource-backed | Unchanged |
| `create pane -o uid\|name\|ref\|metadata\|json` with no `--project` | Exit `2` saying the compatibility split creates no resource | Works: the projections resolve the created resource |

Two consequences are worth calling out:

- **The client no longer follows the new pane.** Every create is detached. Use
  `projmux focus pane uid:<uid>` — or `-o pane-id` and your own `select-pane` —
  when you want to move there.
- **Panes created this way are now managed.** They appear in `get panes`,
  in the primary navigation surface, and in `reconcile resources`. Nothing
  adopts the runtime-only panes created by older releases; they stay visible in
  the Runtime diagnostics surface and are never imported automatically.

The default keybindings whose bodies are `create codex|claude|pane --placement
right|down` keep their spelling and their "split where I am" meaning, and now
produce Registry resources. `internal agent-pane launch-default` — the saved
default split mode, bound to Ctrl-Shift-R/L in the terminal adapters — kept its
spelling and its meaning, and now produces the same Registry resources: see
[Every split surface is Registry-first](#every-split-surface-is-registry-first).

The generated tmux config changed shape to support that. Every projmux
`run-shell` binding is now rendered as `run-shell "TMUX_PANE=#{pane_id} <bin>
..."`, because tmux exports `$TMUX` to a `run-shell` child but never
`$TMUX_PANE`, and without the exact pane a binding cannot tell where it was
pressed. `projmux config apply` — which `make install` and `projmux update
apply` run for you — rewrites the generated file. If you hand-copied a projmux
`run-shell` line into your own `~/.tmux.conf`, add the same prefix.

If you need a raw, unmanaged tmux split, use tmux itself (`split-window`).
projmux does not spell that as a resource verb.

### Every split surface is Registry-first

The saved-default split binding, the `Alt-7` provider picker, and the resume
picker used to open a pane by calling tmux's `split-window` directly. Those panes
were runtime-only: no uid, no owner Window, no Agent row, and no row in `get
panes` or the primary navigation surface. They now go through the same canonical
`create` route the `create codex|claude|pane` bindings and typed commands use, so
every pane a Projmux surface opens is a managed resource.

What changes for you:

- **Panes from the picker and the saved default are managed.** They appear in
  `get panes`, `get agents`, the primary navigation surface, and `reconcile
  resources`, and they are deleted with `projmux delete pane|agent`. Panes those
  surfaces created in older releases are not adopted; they stay visible in the
  Runtime diagnostics surface.
- **A launch needs a resolvable Project.** These surfaces create resources, so
  they follow [Create scope](cli-guide.md#create-scope) like any other create. A
  UI action taken outside a managed Project fails with the same
  `pass --project <ref>` guidance instead of opening an unmanaged pane.
- **The legacy `ai split` route is gone**, along with its `--agent`,
  `--force-agent`, and `--print-pane-id` flags. The `ai` root itself was retired
  earlier; nothing reaches that handler now. Automation that wants the new pane's
  handle uses `-o pane-id` on a canonical create — see
  [AI Agent Shortcuts](ai-agent-shortcuts.md).
- **The AI title watcher no longer starts automatically.** The canonical create
  route never started it, and these surfaces now behave the same way. Pane
  titles, topics, and status come from provider hooks and from `projmux agent
  topic`. `projmux internal agent-hook watch-title` still exists for the hook
  contract.

If you need a raw, unmanaged tmux split, use tmux itself (`split-window`).

### One lifecycle trigger route

The generated tmux config used to install two lifecycle routes: `internal tmux
reconcile-bindings` on `after-new-window`/`after-split-window`, and `internal tmux
release-dead-agent-panes` on `pane-exited`/`after-kill-pane`. Both answered the
same question with a different subset of one reconciliation, so a pane exit
inside a session that had also just gained a window paid for two registry
transactions to reach the state one pass reaches.

All four hooks now invoke one hidden route, `internal tmux converge
--socket-path <path> --session <id> --reason <config-apply|runtime-created|runtime-exited>`,
and so does `projmux config apply`. At most one convergence worker runs per exact
tmux server, so a burst of hooks costs one pass instead of one per event. Which
hooks each config installs is unchanged: the app config carries all four, and the
standalone snippet carries the two pane-exit hooks only, so a raw `new-window` in
a session projmux does not own still stays unmanaged.

`projmux config apply` — which `make install` and `projmux update apply` run for
you — rewrites the generated file. A tmux server still holding config from an
older binary keeps invoking the two retired routes, which now fail; the hooks
guard every invocation with `>/dev/null 2>&1`, so nothing surfaces, and the next
`config apply` replaces them. If you hand-copied a projmux `set-hook` line into
your own `~/.tmux.conf`, re-copy it from `projmux config render standalone`.

### Discovery no longer registers Projects

**Breaking.** A directory found under a discovery root used to become a Registry
Project on its own. Any mutation route ran a reconcile prelude that walked the
configured workdirs and registered every child it did not recognize, so a single
`projmux create pane` in one repository could add a Project for every sibling
directory beside it. Scanning a directory is now a scan and nothing else.

Three collections, three authorities:

| Thing | What it is | What it is not |
| --- | --- | --- |
| Workdirs / `PROJMUX_MANAGED_ROOTS` / `PROJMUX_PROJDIR` | Scan roots to look inside | Not a statement that anything inside them is a Project |
| A discovered child | An unregistered candidate | Not managed identity, however often it is scanned |
| A Registry Project | Managed identity, with a stable uid | Not derived from a path, a name, or a scan |

What registers a Project now:

```sh
projmux create project --root /abs/path/to/repo
```

…or opening that one candidate from the Projects sidebar, which performs the same
registration for that exact path. Both are idempotent: a root an existing Project
already claims writes nothing at all. Sibling candidates under the same discovery
root stay unregistered.

What changes for an existing invocation:

| Invocation | Before | Now |
| --- | --- | --- |
| `create window --project <name>` where `<name>` is a discovered-but-unregistered directory | The reconcile prelude registered it (and every other discovered child) first, then the create succeeded | Exits non-zero naming the exact `--root` to register, or open it once from the sidebar |
| Any mutation route, with N children under a scan root | Up to N Projects appeared | Zero Projects appear |
| `switch open <unregistered directory>` | Opened a session; the Project appeared later, as a side effect of some unrelated mutation | Registers that one path, then opens it exactly as before |

If you relied on the old behavior to populate the Registry, register the roots
you actually want once:

```sh
for root in ~/src/*; do projmux create project --root "$root"; done
projmux get projects
```

### Pins are typed, and migrate on request

**Breaking.** `~/.config/projmux/pins` held one absolute path per line, which
could not say whether the path was a Project. It is now a typed envelope:

```text
projmux-pins v2
project proj-kwo4qozry2sr2ycij2g45zyvam
candidate /home/dev/src/scratch
```

A `project` pin references a Registry Project uid; its displayed root and name
are projected from the Registry on every read, so the pin survives a rebind, a
rename, and a missing root. A `candidate` pin references a path that no Project
claims and stays a path.

Nothing migrates behind your back. Reads project a pre-v2 file in memory, so the
sidebar looks the same before and after; the file is rewritten only when you ask:

```sh
projmux pin project migrate --dry-run   # report only
projmux pin project migrate             # store the typed form
```

Per line, migration has exactly three outcomes:

| Registry Projects claiming the path | Result |
| --- | --- |
| exactly one | becomes that Project's `project <uid>` pin |
| none | stays a `candidate <path>` pin |
| two or more | the **entire** migration is refused; the pin file and the Registry keep their bytes, and the message names the repair |

Repair an ambiguous pin with `projmux rebind project` so one Project claims the
path, or pin the Project you meant directly with `projmux pin project add
uid:<uid>`, then re-run the migration. A corrupt file, or one written by a newer
projmux, is refused rather than partially read.

`pin project add|remove|toggle <dir>` is unchanged in argv and now resolves to a
typed pin under the same rule (one Project → managed, none → candidate, more than
one → refused). `pin project list` gained a kind column and a `--kind
project|candidate` filter; the first mutation after an upgrade migrates the file
first, so it can refuse for the reasons above.

Settings > Projects shows the three collections separately: **Additional
discovery roots** (scan roots), **Pinned Projects** (managed, by uid, with rebind
and unpin), and **Candidate Pins** (unregistered paths, with register and unpin).
On Windows the discovery roots stay OS-native paths; drive-letter, case, and
separator differences are folded only when matching a candidate or migrating a
legacy pin, never to mint or merge a Project uid.

### Legacy compatibility routes removed

This release removes the human-facing compatibility argv and old
pre-namespace internal aliases listed in the [retirement ledger](legacy-cli-retirement.md).
Rejected compatibility argv below a surviving mixed root exits 2 with its exact
canonical replacement, no stdout, and no side effect. Fully removed human roots
(`current`, `kill`, `notify`, `sessions`, `session-state`, `tag`, `upgrade`, and
`usage`) and removed internal aliases (`tmux`, `status`, `statusbar`, `preview`,
`session-popup`, `key-broker`, and `popup-wait-key`) are unknown top-level
commands and exit 1. Use `internal ...` for generated plumbing and `config
render|apply` for public configuration work.

The mixed roots retain only `attach project`, `focus project|window|pane`, `pin
project`, and `prune project|snapshot`. Shortcuts and singular/plural resource
kind aliases are unchanged. The ledger contains the complete removed
argv/replacement/error matrix.

`ai notify` requires action-aware migration. The old notify action can move to
`create notification --text ... --target ...`, but its pane/payload input and
queue semantics differ. The old `ai notify reset` action cleared desktop
notification dedupe state and has no direct public replacement; use
`notification ack` or `notification reconcile` only when the intended work is
queue maintenance.

### Managed Agent hook producer migration

The installer now writes Codex, Claude, Antigravity named hooks, Antigravity
Statusline, and the tmux bell fallback with the canonical
`projmux internal agent-hook ingest ...` entrypoint. Copyable integration
commands use `projmux agent integrate <provider>`.

The v0.11.1 migration release used hook-first ordering: the new binary was
atomically installed while the exact old managed-producer argv described in the
ledger still dispatched to the same handler; it then migrated every
marker-owned producer before generating and applying tmux config. Phase 3's
next breaking release removes that temporary dispatcher after the public
fresh/upgrade/`--no-apply` migration evidence gate. Upgrade an old installation
through v0.11.1 before the next release when you need the migration-window
event guarantee.

Migration is ownership- and transaction-aware. Codex managed blocks and the
Claude, Antigravity, Statusline, and tmux-bell markers are the only ownership
evidence. Missing integrations and markerless commands are not installed or
rewritten. Provider file plans and conflicts are collected before the first
file write. Live bell state is then inventoried on the exact target socket
before its first command; a later bell failure rolls back both that live state
and the provider-file ledger. Repeating the migration on current state performs
no file or tmux mutations.

Use each provider's dry-run to see every target, conflict, and old→canonical
command before changing anything:

```sh
projmux agent integrate codex --dry-run
projmux agent integrate claude --dry-run
projmux agent integrate antigravity --dry-run
projmux agent integrate tmux-bell --dry-run
```

`update apply --no-apply` still migrates marker-owned
files, but they neither inspect nor modify a live tmux server. A later normal
`projmux config apply --socket <name>` converges a marker-owned bell hook only on
that exact `-L <name>` socket.

**Upgrading directly from v0.10.1:** its GitHub Release updater predates the
current post-update argv. A normal
`PROJMUX_INSTALLER=github-release projmux update apply` replaces the executable
and then calls exact legacy `tmux apply`; the replacement accepts that hidden
handoff and performs the same managed-producer migration, generated-config
write, and live reload as current `config apply`. The old route is not restored
in help or the command catalog, and no other `tmux` argv is accepted.

The v0.10.1 updater's own `--no-apply` behavior is historically different: it
returns immediately after binary replacement and never executes the new
binary. Treat that invocation as replace-only, verify that the executable is
the intended new version, and then run the replacement explicitly:

```sh
PROJMUX_INSTALLER=github-release projmux update apply --no-apply
projmux version
projmux config apply --no-reload
```

The explicit last step migrates marker-owned provider files and writes the
generated config without inspecting or mutating live tmux. This split-stage
recovery applies only when the command that started the update was v0.10.1;
current `update apply --no-apply` already executes the new binary's
`config apply --no-reload` automatically.

If a conflict is reported, keep the unmanaged entry unchanged while deciding
which tool owns it; remove or rewrite it manually only after that decision,
then rerun the dry-run and installer. A failed transaction is safe to retry
because it restores its starting state.

**Stale unmanaged legacy hooks after the final removal:** projmux does not take
ownership of markerless hooks and does not rewrite them automatically. Such a
hook now fails with `unknown command: ai`. After confirming that you own the
entry, preserve its provider/event arguments, redirection, and fallback and
manually replace the old `projmux ai ingest` prefix with `projmux internal
agent-hook ingest`. Then run `projmux agent integrate <provider> --dry-run` and
resolve any reported ownership conflict before installing a managed entry.

**Downgrading to 0.10.1 or older:** those binaries do not expose the canonical
internal ingress. Before replacing the current binary, remove each managed
integration with `projmux agent integrate <provider> --remove`. After the old
binary is installed, use that version's integration command to
write its legacy managed producer again. Never rewrite markerless user hooks as
part of downgrade remediation.

### Terminal init command removed

The deprecated top-level `projmux init` command and its legacy-only
`--dry-run` flag have been removed. Use the exact replacement
`projmux setup terminal`; it previews by default, and accepts `--apply`,
`--config <path>`, and `--allow-symlink` when those behaviors are needed.

### Keymap schema migration

`~/.config/projmux/keymap.toml` is now versioned by a root `schema_version`
marker. A file without one is v0 and uses the action ids projmux has always
written; `schema_version = 1` uses canonical dotted ids such as
`window.create` and `project-sidebar.runtime.stop`; `schema_version = 2` keeps
those ids and adds two-to-four-stroke `sequences`. All versions read — the v0
spelling of every action stays a permanent read alias.

The migration needs no command of its own. Every installer path ends by running
the newly installed binary's `projmux config apply`, which migrates first and only
then writes generated config and reloads the live server. An install that never
went through an updater converges on the first Settings key save or the next
`projmux config apply`.

Before it replaces anything it writes a digest-named backup: v0 keeps
`keymap.toml.pre-v1-<digest>.bak`, while v1→v2 writes
`keymap.toml.pre-v2-<digest>.bak` and changes only the schema marker. Running the
migration again on an already-current file writes no bytes. If it cannot
proceed — most often because a file carries both a legacy and a canonical table
for one action with different keys — nothing is written at all and the report
says so.

Configured sequences compile to generated tmux key tables. Escape and unknown
continuations cancel without pane input; each apply unbinds the roots and tables
its predecessor recorded before sourcing the current trie. Duplicate/strict-prefix
sequences, a first stroke already owned by a no-prefix chord, and unsafe strokes
fail before the keymap, generated config, or live server changes.

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
`projmux config apply`. Projmux now rejects the stale table with that exact
replacement instead of silently applying its keys to the user-label action.

This removal does not change `projmux agent topic set/clear`: those advanced CLI
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
projmux config apply
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

If you installed with `go install`, `projmux update apply` uses the atomic
replacement flow:

```sh
projmux update apply             # replace + apply
projmux update apply --no-apply  # migrate + write config, skip the live reload
projmux update apply --dry-run   # print the steps only
```

`projmux update apply` reinstalls via `go install`, atomically replaces the active
file, and reapplies the live tmux config so a running `-L projmux` server picks
up new bindings without a restart.

The command reads `PROJMUX_PROJDIR` from the calling shell and memoizes the
primary path to `~/.config/projmux/projdir`, so the new binary keeps the same
project root context as the one it replaces.

To switch the saved project root during the upgrade:

```sh
PROJMUX_PROJDIR=/new/path projmux update apply

# Multi-path also works; only the primary entry is persisted.
PROJMUX_PROJDIR="/main/repos:/secondary/repos" projmux update apply
```

## GitHub Release Installs

When `PROJMUX_INSTALLER=github-release`, `projmux update apply` downloads the
latest matching `projmux_<version>_<goos>_<goarch>.tar.gz` asset from GitHub
Releases, extracts the binary, atomically replaces the current executable, and
then runs the new binary's `projmux config apply` — or `config apply --no-reload`
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
