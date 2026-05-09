# Experimental Native Picker Engine

This note tracks the experimental native picker engine. It does not change the
public doctor dependency policy: `fzf` remains a required production dependency
and the `internal/ui/fzf` backend remains the default/fallback path.

The fzf compatibility surface for the native engine is tracked in
[native-picker-parity.md](native-picker-parity.md).

## What This Covers

- `internal/ui/picker` is the backend-neutral contract for native picker rows,
  actions, filtering, and typed-query prompts.
- `internal/ui/projmuxpicker` is the projmux-specific native picker surface for
  frame, redraw updates, theme tokens, ANSI width/truncation,
  prompt/footer/list rendering, and preview pane layout. The POC keeps `picker`
  responsible for backend routing, keyboard input, filtering, preview command
  execution, and result contracts, while moving visual composition into
  `projmuxpicker` so projmux can evolve a native picker design without coupling
  every visual tweak to the fzf adapter.
- `internal/ui/fzf` now owns the adapter between fzf's legacy option/result
  shape and the backend-neutral `picker.Options` contract. App code should
  describe picker intent as rows, actions, preview commands, and initial focus;
  the fzf adapter is responsible for translating that into `--expect`,
  `--bind execute-silent(...)`, `+refresh-preview`, and `start:pos(N)`.
- `intfzf.NewPickerRunner()` wraps fzf behind the same `picker.Runner`
  interface as the native runner. This is the current DI boundary for swapping
  picker engines while keeping fzf available.
- Settings > Labs > Picker Engine stores the experimental selection in
  `~/.config/projmux/picker-backend` and updates the live tmux environment so
  new picker popups can switch between `fzf` and `native` without restarting
  the app server.
- `PROJMUX_PICKER_BACKEND=native` remains an explicit environment override and
  takes priority over the saved Labs setting.
- Picker flows covered by the native path include AI picker/settings, shell
  update prompt, settings hub sections, switch settings/add-pin, the main
  project switcher list, recent sessions, and notify sidebar.
- The native picker supports ranked fuzzy search/filter, arrow-key selection in
  normal CSI and tmux application-cursor modes, Enter, Esc, Ctrl-C, Backspace,
  Ctrl-U, Ctrl-W, PageUp/PageDown, Home/End, modified CSI keys, custom expect
  keys such as Ctrl-X/Alt-P, printable expect keys such as notify `x`, control
  expect keys such as notify `Ctrl-X`, `start:pos(N)` initial focus, preview
  command output, preview cycle command bindings, and sidebar focus command
  bindings.
- FZF-style movement keys are supported for native selection: `Ctrl-N` moves
  down, while `Ctrl-P` and `Ctrl-K` move up unless the app claims the key as a
  custom action.
- Typed-query prompts support cursor-aware insertion/deletion with a visible
  prompt cursor, Left/Right, Ctrl-A/E, Delete, Backspace, Ctrl-U, and Ctrl-W
  for settings path entry.
- The native key parser recognizes the app's CSI-u keybind-probe sequences
  such as `ESC [ 9005 u` for `Alt-1`, plus generic modified CSI-u forms such as
  `ESC [ 115 ; 7 u` for `Ctrl-Alt-S`.
- Native interactive picker screens use an alternate screen lifecycle to better
  match fzf fullscreen behavior and restore the tmux pane after exit.
- Native interactive picker screens render inside a full-screen border frame to
  match the app's fzf `--height 100% --border` surface more closely.
- Popup-toggle commands use tmux `display-popup -B` when the native backend is
  active so the native picker owns the visible frame and does not double-draw
  with tmux's outer popup border.
- Simple native lists use the available terminal height after header, prompt,
  footer, and preview reservations instead of a fixed page-sized viewport.
- Navigation-only native lists mirror fzf `--disabled --no-input`: the input
  prompt is hidden, printable non-action keys do not alter the query, and
  expect/action keys still work.
- Native frame content now uses the full inner border width so prompt/list/footer
  separators reach the right border like fzf.
- Simple and multi-line native rows share the same projmux current-row style and
  pointer marker rather than falling back to terminal inverse video for simple
  pickers.
- Selected multi-line rows use a continuation marker on metadata lines so
  switch/session/notify cards read as one focused block.
- Native redraws use terminal synchronized-update wrappers and row-diff updates
  after the first frame. The frame/redraw renderer lives in `projmuxpicker`
  rather than the backend loop, skips unchanged frames, and avoids a trailing
  newline after the bottom border. This reduces visible keyboard-navigation
  flicker and prevents exact-height popups from scrolling the top border off
  screen.
- In app TTY contexts, the native picker opens the controlling terminal
  (`/dev/tty`) before entering raw mode. This avoids stdin/stdout mismatch and
  line-mode escape leakage such as arrow keys appearing as `^[[`.
- Raw TTY reads keep polling briefly across empty reads while decoding
  escape-key sequences, so split arrow/Alt key bytes are consumed by the picker
  instead of leaking into the query or parent shell.

## Experimental Boundaries

- The `projmuxpicker` package is intended as a foundation that can be carried
  forward, along with the fzf-to-picker adapter boundary in `internal/ui/fzf`.
  Its frame, row, preview, theme, ANSI, and redraw modules are foundation code;
  Docker sandbox scripts and dependency-policy notes remain support
  scaffolding.
- Switch and sessions preview panes are native previews for the concrete
  projmux option shapes. Wide right-side preview windows render beside the
  list, and sidebar-style `down,25%,border-top` previews render below the list
  without a synthetic preview title row, using fzf-measured percent sizing.
  The full fzf preview-window grammar remains outside this POC surface.
- Preview cycle state is covered in Docker e2e against real tmux sessions: the
  switch and sessions popup flows type a filtered query, send `Right` and
  `Alt-Down`, and assert the stored preview window/pane cursor for the selected
  session.
- Public doctor dependency policy still treats `fzf` as required. The Labs
  setting is experimental and does not make native the default production
  backend.

## Interactive No-fzf Sandbox

Use this when you want to enter a Docker container and experience this build
directly without `fzf`. It builds the no-fzf dependency image, mounts this
worktree, builds `projmux` inside the container, creates sample projects under
`/workspace/projects`, stores the native picker backend in the sandbox config,
writes a tmux config with the same backend in the tmux server environment, and
launches `projmux shell`. It also forces UTF-8 locale inside the container. This
uses `wt path` instead of `wt run` because `docker run -it` needs the current
terminal TTY:

```sh
bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
```

Inside the tmux shell, try:

```sh
projmux switch
projmux settings
projmux doctor --json
```

Manual UX checks for the Docker sandbox:

- Alt-1 opens with the top border/title visible, not clipped.
- Vertical borders stay continuous while moving Up/Down.
- Alt-1 closes the sidebar immediately when pressed again.
- Alt-2, Alt-3, Alt-4, and Alt-5 open their matching native popups and close
  on the same Alt key immediately.
- Arrow keys move selection without leaking `^[[` text into the query.

`fzf` is intentionally not installed in the image.

## Automated No-fzf Docker E2E Command

Run this from the repository root. It builds a Go 1.24 Trixie no-fzf
dependency image from `test/docker/no-fzf-poc.Dockerfile`, including Go module
cache, then mounts the repository into an isolated `--network none` container,
builds `projmux`, asserts `fzf` is not on `PATH`, runs the focused native-picker
tests, stores the native backend through Settings > Labs, verifies the saved
backend works without an env override, exercises `projmux switch --ui=sidebar`
search/selection under a container PTY,
exercises `projmux switch --ui=popup` and `projmux sessions --ui=popup` against
existing tmux sessions, sends `Right` and `Alt-Down` once to smoke the
preview-cycle bindings, launches `projmux shell` under a container PTY, verifies
that it creates a tmux session, verifies immediate launch-key close behavior for
Alt-1 through Alt-5 native popup surfaces, exercises `notify list --ui=sidebar`
with the printable `x` expect key, and exercises the settings picker under a PTY
using Enter and arrow-key navigation through the native backend.

Short tmux-friendly form:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

The script contains the Docker image build and isolated `docker run` command:

```sh
scripts/poc-native-picker-no-fzf-e2e.sh
```

To compare another base image without editing the repo, override the build arg:

```sh
PROJMUX_POC_NO_FZF_BASE_IMAGE=golang:1.24-bookworm bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
```
