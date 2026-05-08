# Native Picker Without fzf POC

This note is POC-only. It does not change the public doctor dependency policy:
`fzf` remains a required production dependency and the `internal/ui/fzf`
backend remains the fallback with full preview/cycle/sidebar behavior.

The fzf compatibility surface for this POC is tracked in
[native-picker-parity.md](native-picker-parity.md).

## What This POC Covers

- `internal/ui/picker` is the backend-neutral contract for native picker rows,
  actions, filtering, and typed-query prompts.
- `PROJMUX_PICKER_BACKEND=native` routes simple app pickers through the native
  runner instead of shelling out to `fzf`.
- Picker flows covered by the native path include AI picker/settings, shell
  update prompt, settings hub sections, switch settings/add-pin, the main
  project switcher list, recent sessions, and notify sidebar.
- The native picker supports ranked fuzzy search/filter, arrow-key selection in
  normal CSI and tmux application-cursor modes, Enter, Esc, Ctrl-C, Backspace,
  Ctrl-U, Ctrl-W, PageUp/PageDown, Home/End, modified CSI keys, custom expect
  keys such as Ctrl-X/Alt-P, printable expect keys such as notify `a`, control
  expect keys such as notify `Ctrl-A`, `start:pos(N)` initial focus, preview
  command output, preview cycle command bindings, and sidebar focus command
  bindings.
- Typed-query prompts support cursor-aware insertion/deletion with Left/Right,
  Ctrl-A/E, Delete, Backspace, Ctrl-U, and Ctrl-W for settings path entry.
- The native key parser recognizes the app's CSI-u keybind-probe sequences
  such as `ESC [ 9005 u` for `Alt-1`, plus generic modified CSI-u forms such as
  `ESC [ 115 ; 7 u` for `Ctrl-Alt-S`.
- Native interactive picker screens use an alternate screen lifecycle to better
  match fzf fullscreen behavior and restore the tmux pane after exit.
- Native interactive picker screens render inside a full-screen border frame to
  match the app's fzf `--height 100% --border` surface more closely.
- Simple native lists use the available terminal height after header, prompt,
  footer, and preview reservations instead of a fixed page-sized viewport.
- In app TTY contexts, the native picker opens the controlling terminal
  (`/dev/tty`) before entering raw mode. This avoids stdin/stdout mismatch and
  line-mode escape leakage such as arrow keys appearing as `^[[`.

## Explicit Follow-Up Gaps

- Switch and sessions preview panes are approximate native previews. Wide
  right-side preview windows render beside the list, and sidebar-style
  `down,25%,border-top` previews render below the list without a synthetic
  preview title row, but exact fzf preview-window sizing parity is still
  missing.
- Exact preview cycle state parity still needs hands-on validation against real
  tmux sessions. The Docker e2e now smokes the `Right` and `Alt-Down`
  preview-cycle bindings before selecting a switch/session row, but does not
  assert the final preview cursor state.
- Public docs and doctor dependency policy still treat `fzf` as required.

## Interactive No-fzf Sandbox

Use this when you want to enter a Docker container and experience this POC build
directly. It builds the no-fzf dependency image, mounts this worktree, builds
`projmux` inside the container, creates sample projects under
`/workspace/projects`, sets `PROJMUX_PICKER_BACKEND=native`, and launches
`projmux shell` so you can experience the POC directly. It also forces UTF-8
locale and the native TTY fallback inside the container. This uses `wt path`
instead of `wt run` because `docker run -it` needs the current terminal TTY:

```sh
bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
```

Inside the tmux shell, try:

```sh
projmux switch
projmux settings
projmux doctor --json
```

`fzf` is intentionally not installed in the image.

## Automated No-fzf Docker E2E Command

Run this from the repository root. It builds a Node-based no-fzf dependency
image from `test/docker/no-fzf-poc.Dockerfile`, including Go module cache, then
mounts the repository into an isolated `--network none` container, builds
`projmux`, asserts `fzf` is not on `PATH`, runs the focused native-picker tests,
exercises `projmux switch --ui=sidebar` search/selection under a container PTY,
exercises `projmux switch --ui=popup` and `projmux sessions --ui=popup` against
existing tmux sessions, sends `Right` and `Alt-Down` once to smoke the
preview-cycle bindings, launches `projmux shell` under a container PTY, verifies
that it creates a tmux session, exercises `notify list --ui=sidebar` with the
printable `a` expect key, and exercises the settings picker under a PTY using
Enter and arrow-key navigation through the native backend.

Short tmux-friendly form:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

The script contains the Docker image build and isolated `docker run` command:

```sh
scripts/poc-native-picker-no-fzf-e2e.sh
```
