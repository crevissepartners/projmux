# Native Picker fzf Parity Map

This is a POC audit note for `poc/native-picker-no-fzf`. It reverse-engineers
the subset of fzf behavior that projmux currently uses and maps it to native
picker evidence. It is not a production dependency-policy change.

## App fzf Surface

| fzf surface | projmux usage | Native status | Evidence |
| --- | --- | --- | --- |
| `--prompt` | AI, settings, shell update, switch, sessions, notify | Covered | `renderNativeInteractive`, `renderNative`; `TestNativePromptLineIncludesInlineMatchCount` |
| `--height 100%` / `--border` | all interactive picker screens | Covered approximately | `renderNativeFrame`; screen-height list budgeting; `TestNativeInteractiveRendersBorderFrame`; `TestNativeInteractiveUsesAvailableHeightForSimpleLists` |
| `--header` | AI, settings, shell update, notify | Covered | `renderNativeInteractive`, `renderNative`; settings native tests |
| `--footer` / `--footer-border line` | AI, settings, shell update, switch, sessions, notify | Covered for interactive native screens | `renderNativeInteractive` reserves bottom footer space and renders a separator line; `TestNativeInteractiveRendersFooterAtBottom` |
| `--ansi` | colored row labels from render package | Covered | native writes row labels directly and strips ANSI escapes from default search text; `TestFilterItemsIgnoresANSIEscapeSequences`; Docker e2e shows ANSI rows |
| hidden value after tab delimiter | all picker selections | Covered by `picker.Item.Value` | `pickerItemsFromFZFEntries`; `TestNativeRunnerFiltersAndSelectsByNumber` |
| plain fzf candidates without structured entries | legacy runner call shape | Covered | `pickerItemsFromFZF`; `TestPickerOptionsFromFZFMapsCandidatesWhenEntriesAreEmpty` |
| search key filtering (`--nth`/reload filter file) | switch/sessions/notify entries | Covered by `Item.SearchText` with fzf reload order preservation | `FilterItems`; `TestFilterItemsUsesSearchTextNotMetadata`; `TestFilterItemsPreservesSearchKeyOrder` |
| fuzzy result ranking | simple non-search-key picker UX | Covered approximately | `fuzzyScore`; `TestFilterItemsRanksBetterMatchesFirst` |
| `--scrollbar █` | long switch/session/settings lists | Covered approximately | `nativeListLinesWithScrollbar`; `TestNativeInteractiveUsesScrollbarForLongLists` |
| `--read0` multi-line rows | switch, sessions, notify | Covered | `Options.MultiLine`; `TestNativeInteractiveRendersFZFLikeMultilineSelection` |
| `--gap --gap-line ─` | switch, sessions, notify multi-line rows | Covered approximately | `nativeGapLine`, row-budgeted range; `TestNativeInteractiveRendersMultilineGapLine`, `TestNativeVisibleRangeCountsMultilineRenderedRows` |
| fzf current row colors | multi-line rows | Covered approximately | `nativeCurrentStart`, `nativePointer`; `TestNativeSelectedContentKeepsCurrentStyleAfterReset` |
| `--expect` keys | Enter/Ctrl-X/Alt-P/notify keys | Covered | `pickerActionsFromFZF`; `TestNativeInteractiveSupportsCustomExpectKeys` |
| printable expect keys | notify sidebar `a` ack | Covered | `TestNativeInteractiveSupportsPrintableExpectKeys`; Docker no-fzf e2e |
| control expect keys | notify sidebar `Ctrl-A`, settings `Ctrl-Alt-S` close | Covered | `TestNativeInteractiveSupportsControlExpectKeys`; `TestNativeInteractiveSupportsControlAltCloseKeys` |
| close `--bind key:abort` | Esc, Ctrl-C, Alt-N, Ctrl-Alt-S variants | Covered | `CloseActions`; `TestNativeRunnerUsesSharedCloseActions` |
| terminal CSI-u key encoding | app keybind probe sequences, Ghostty/kitty-style modified keys | Covered | `TestNativeInteractiveSupportsCSIuAppKeyBindings` |
| `execute-silent(...)+refresh-preview` | switch/session preview cycling | Covered for command execution and rerender loop | `pickerCommandFromFZFBinding`; `TestNativeInteractiveRunsCustomActionCommandAndRefreshes`; Docker no-fzf e2e sends `Right` and `Alt-Down` before selection |
| `focus:execute-silent(...)` | switch sidebar focus | Covered | `runNativeFocusAction`; `TestNativeInteractiveRunsFocusActionOnSelectionChange` |
| `start:pos(N)` | switch sidebar initial row | Covered | `pickerInitialIndexFromFZF`; `TestPickerOptionsFromFZFMapsStartPosToInitialIndex` |
| `--preview` | switch, sessions | Covered by command output | `nativePreviewLines`; `TestNativeInteractiveRendersSelectedPreview` |
| `--preview-window right,60%,border-left` | switch popup, sessions popup | Covered approximately | `renderNativeSplitPreview` renders a single-column left border without a synthetic title row; `TestNativeInteractiveRendersWidePreviewBesideList` |
| `--preview-window down,25%,border-top` | switch sidebar | Covered approximately | `renderNativeDownPreview` renders an immediate top border without a synthetic title row; `TestNativeInteractiveRendersDownPreviewBelowList` |
| preview scrolling | long switch/session preview output | Covered approximately with `Shift-Up`/`Shift-Down` | `previewOffset`; `TestNativeInteractiveRendersPreviewOffset` |
| `--query` | typed settings path defaults | Covered | `Options.InitialQuery`; settings tests |
| `--print-query` accept-query mode | typed settings path prompts | Covered | `Options.AcceptQuery`; `TestNativeRunnerAcceptsTypedQuery` |
| query cursor editing | typed settings path prompts | Covered | Left/Right, Ctrl-A/E, Delete, Backspace, Ctrl-U/W query editing plus visible prompt cursor; `TestNativeInteractiveEditsTypedQueryAtCursor`; `TestNativeInteractiveSupportsQueryLineEditingKeys`; `TestNativeInteractiveCtrlUDeletesBeforeCursor`; `TestNativePromptLineRendersQueryCursor` |
| terminal arrow key variants | interactive selection in tmux/docker | Covered | CSI, SS3/application cursor, modified CSI tests; app TTY `/dev/tty` fallback |
| alternate-screen lifecycle | fzf fullscreen picker screen restore | Covered | `nativeScreenEnter`; `TestNativeInteractiveUsesAlternateScreen` |

## Verified Flows

- `ai` picker/settings: native backend routing covered by app tests.
- `shell` update prompt: native backend routing covered by shared fzf-to-native
  adapter and settings-style typed prompt tests.
- `settings`: native backend exercised in unit tests and Docker no-fzf e2e
  using Enter plus arrow-key navigation under a PTY.
- `switch --ui=sidebar`: Docker no-fzf e2e creates sample projects, types
  `bravo`, selects `bravo-web`, and confirms the opened tmux shell path.
- `switch --ui=popup`: Docker no-fzf e2e creates existing tmux sessions using
  the app's session naming convention, sends `Right` and `Alt-Down` to exercise
  preview window/pane cycle, types `bravo`, selects `bravo-web`, and confirms
  the opened tmux shell path.
- `sessions --ui=popup`: Docker no-fzf e2e creates existing tmux sessions,
  sends `Right` and `Alt-Down` to exercise preview window/pane cycle, types
  `bravo`, selects `bravo-web`, and confirms the opened tmux shell path.
- `notify sidebar`: native routing is unit-covered; Docker no-fzf e2e pushes a
  notification, presses printable expect key `a`, and verifies the row is acked.

## Remaining Gaps Before Calling This Complete

- Exact fzf preview-window parity is not complete: native has approximate
  right/down layout and keyboard preview scrolling, but not the full fzf sizing
  algorithm.
- Exact fzf fuzzy scoring is not complete for non-search-key simple pickers:
  native ranking is deterministic and close enough for projmux search, but not
  an implementation of fzf's scorer. Search-keyed app pickers preserve fzf's
  `--disabled` reload order instead of score-sorting.
- Mouse support is not implemented. projmux does not currently expose mouse
  picker workflows, so this is outside the required app surface unless new
  workflows depend on it.
- The public doctor/docs dependency policy still says `fzf` is required. This is
  intentional for the POC branch.
- Draft PR: https://github.com/crevissepartners/projmux/pull/98 (`DO NOT MERGE
  / POC ONLY`).

## Commands

Automated no-fzf e2e:

```sh
wt run poc/native-picker-no-fzf -- scripts/poc-native-picker-no-fzf-e2e.sh
```

Interactive no-fzf sandbox:

```sh
bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
```

Do not run the interactive sandbox through `wt run`: current `wt run` captures
child stdio instead of forwarding the caller's TTY, so Docker cannot attach
`-it` and terminal picker input will not behave like a real session.
