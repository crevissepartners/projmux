# Native Picker Without fzf POC

This note is POC-only. It does not change the public doctor dependency policy:
`fzf` remains a required production dependency and the `internal/ui/fzf`
backend remains the fallback with full preview/cycle/sidebar behavior.

## What This POC Covers

- `internal/ui/picker` is the backend-neutral contract for native picker rows,
  actions, filtering, and typed-query prompts.
- `PROJMUX_PICKER_BACKEND=native` routes simple app pickers through the native
  runner instead of shelling out to `fzf`.
- Picker flows covered by the native path include AI picker/settings, shell
  update prompt, settings hub sections, switch settings/add-pin, the main
  project switcher list, recent sessions, and notify sidebar.
- The native picker supports search/filter, arrow-key selection in normal CSI
  and tmux application-cursor modes, Enter, Esc, Ctrl-C, Backspace, Ctrl-U,
  Ctrl-W, PageUp/PageDown, Home/End, modified CSI keys, custom expect keys such
  as Ctrl-X/Alt-P, preview command output, preview cycle command bindings, and
  sidebar focus command bindings.

## Explicit Follow-Up Gaps

- Switch and sessions preview panes are approximate native previews. Wide
  right-side preview windows render beside the list, but exact fzf
  preview-window sizing, color, scrolling, and border parity is still missing.
- Full switch preview cycle parity still needs hands-on validation against real
  tmux sessions.
- Public docs and doctor dependency policy still treat `fzf` as required.

## Interactive No-fzf Sandbox

Use this when you want to enter a Docker container and experience this POC build
directly. It builds the no-fzf dependency image, mounts this worktree, builds
`projmux` inside the container, creates sample projects under
`/workspace/projects`, sets `PROJMUX_PICKER_BACKEND=native`, and launches
`projmux shell` so you can experience the POC directly. This uses `wt path`
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
launches `projmux shell` under a container PTY, verifies that it creates a tmux
session, and exercises the settings CLI picker through the native backend.

Short tmux-friendly form:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

The script contains the Docker image build and isolated `docker run` command:

```sh
scripts/poc-native-picker-no-fzf-e2e.sh
```
