# Native Picker Without fzf POC

This note is POC-only. It does not change the public doctor dependency policy:
`fzf` remains a required production dependency and the `internal/ui/fzf`
backend remains the fallback with full preview/cycle/sidebar behavior.

## What This POC Covers

- `internal/ui/picker` is the backend-neutral contract for native picker rows,
  actions, filtering, and typed-query prompts.
- `PROJMUX_PICKER_BACKEND=native` routes simple app pickers through the native
  runner instead of shelling out to `fzf`.
- Simple picker flows covered by the native path include AI picker/settings,
  shell update prompt, settings hub sections, switch settings/add-pin, and the
  main project switcher list.
- The main project switcher native path supports search/filter, numeric
  selection, and close. It deliberately ignores fzf preview commands.

## Explicit Follow-Up Gaps

- `sessions` still depends on the fzf backend.
- Switch preview panes still depend on fzf for production parity.
- Switch sidebar focus behavior is not implemented in the native picker.
- Full switch preview cycle parity is not implemented in the native picker.
- Public docs and doctor dependency policy still treat `fzf` as required.

## No-fzf Docker E2E Command

Run this from the repository root. It builds a Node-based no-fzf dependency
image from `test/docker/no-fzf-poc.Dockerfile`, including Go module cache, then
mounts the repository into an isolated `--network none` container, builds
`projmux`, asserts `fzf` is not on `PATH`, runs the focused native-picker tests,
and exercises the settings CLI picker through the native backend.

Short tmux-friendly form:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

The script contains the Docker image build and isolated `docker run` command:

```sh
scripts/poc-native-picker-no-fzf-e2e.sh
```
