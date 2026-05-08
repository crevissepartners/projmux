# Native Picker fzf Parity Map

This is a POC audit note for `poc/native-picker-no-fzf`. It reverse-engineers
the subset of fzf behavior that projmux currently uses and maps it to native
picker evidence. It is not a production dependency-policy change.

## App fzf Surface

| fzf surface | projmux usage | Native status | Evidence |
| --- | --- | --- | --- |
| `--prompt` | AI, settings, shell update, switch, sessions, notify | Covered | `renderNativeInteractive`, `renderNative`; `TestNativePromptLineIncludesInlineMatchCount` |
| `--header` | AI, settings, shell update, notify | Covered | `renderNativeInteractive`, `renderNative`; settings native tests |
| `--footer` / header fallback | AI, settings, shell update, switch, sessions, notify | Covered as footer text, not fzf footer border | `renderNativeInteractive`, `renderNative` |
| `--ansi` | colored row labels from render package | Covered | native writes row labels directly; Docker e2e shows ANSI rows |
| hidden value after tab delimiter | all picker selections | Covered by `picker.Item.Value` | `pickerItemsFromFZFEntries`; `TestNativeRunnerFiltersAndSelectsByNumber` |
| search key filtering (`--nth`/reload filter file) | switch/sessions/notify entries | Covered by `Item.SearchText` | `FilterItems`; `TestFilterItemsUsesSearchTextNotMetadata` |
| fuzzy result ranking | switch/sessions/notify search UX | Covered approximately | `fuzzyScore`; `TestFilterItemsRanksBetterMatchesFirst` |
| `--read0` multi-line rows | switch, sessions, notify | Covered | `Options.MultiLine`; `TestNativeInteractiveRendersFZFLikeMultilineSelection` |
| fzf current row colors | multi-line rows | Covered approximately | `nativeCurrentStart`, `nativePointer`; `TestNativeSelectedContentKeepsCurrentStyleAfterReset` |
| `--expect` keys | Enter/Ctrl-X/Alt-P/notify keys | Covered | `pickerActionsFromFZF`; `TestNativeInteractiveSupportsCustomExpectKeys` |
| close `--bind key:abort` | Esc, Ctrl-C, Alt-N, Ctrl-Alt-S variants | Covered | `CloseActions`; `TestNativeRunnerUsesSharedCloseActions` |
| `execute-silent(...)+refresh-preview` | switch/session preview cycling | Covered for command execution and rerender loop | `pickerCommandFromFZFBinding`; `TestNativeInteractiveRunsCustomActionCommandAndRefreshes` |
| `focus:execute-silent(...)` | switch sidebar focus | Covered | `runNativeFocusAction`; `TestNativeInteractiveRunsFocusActionOnSelectionChange` |
| `start:pos(N)` | switch sidebar initial row | Covered | `pickerInitialIndexFromFZF`; `TestPickerOptionsFromFZFMapsStartPosToInitialIndex` |
| `--preview` | switch, sessions | Covered by command output | `nativePreviewLines`; `TestNativeInteractiveRendersSelectedPreview` |
| `--preview-window right,60%,border-left` | switch popup, sessions popup | Covered approximately | `renderNativeSplitPreview`; `TestNativeInteractiveRendersWidePreviewBesideList` |
| `--preview-window down,25%,border-top` | switch sidebar | Covered approximately | `renderNativeDownPreview`; `TestNativeInteractiveRendersDownPreviewBelowList` |
| preview scrolling | long switch/session preview output | Covered approximately with `Shift-Up`/`Shift-Down` | `previewOffset`; `TestNativeInteractiveRendersPreviewOffset` |
| `--query` | typed settings path defaults | Covered | `Options.InitialQuery`; settings tests |
| `--print-query` accept-query mode | typed settings path prompts | Covered | `Options.AcceptQuery`; `TestNativeRunnerAcceptsTypedQuery` |
| terminal arrow key variants | interactive selection in tmux/docker | Covered | CSI, SS3/application cursor, modified CSI tests |

## Verified Flows

- `ai` picker/settings: native backend routing covered by app tests.
- `shell` update prompt: native backend routing covered by shared fzf-to-native
  adapter and settings-style typed prompt tests.
- `settings`: native backend exercised in unit tests and Docker e2e.
- `switch --ui=sidebar`: Docker no-fzf e2e creates sample projects, types
  `bravo`, selects `bravo-web`, and confirms the opened tmux shell path.
- `switch --ui=popup`: Docker no-fzf e2e creates existing tmux sessions using
  the app's session naming convention, types `bravo`, selects `bravo-web`, and
  confirms the opened tmux shell path.
- `sessions`: native routing, preview command/window, and preview-cycle
  bindings are unit-covered; hands-on validation still needs a real tmux session
  inventory with multiple windows/panes.
- `notify sidebar`: native routing is unit-covered; queue/focus behavior remains
  better validated in app tests than in Docker e2e.

## Remaining Gaps Before Calling This Complete

- Exact fzf preview-window parity is not complete: native has approximate
  right/down layout and keyboard preview scrolling, but not exact fzf borders or
  the full fzf sizing algorithm.
- Exact fzf fuzzy scoring is not complete: native ranking is deterministic and
  close enough for projmux search, but not an implementation of fzf's scorer.
- Mouse support is not implemented. projmux does not currently expose mouse
  picker workflows, so this is outside the required app surface unless new
  workflows depend on it.
- The public doctor/docs dependency policy still says `fzf` is required. This is
  intentional for the POC branch.
- Draft PR creation is blocked by GitHub App permissions in this environment
  (`403 Resource not accessible by integration`), although the branch is pushed.

## Commands

Automated no-fzf e2e:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

Interactive no-fzf sandbox:

```sh
bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
```
