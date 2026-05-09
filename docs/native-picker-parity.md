# Native Picker fzf Parity Map

This audit note reverse-engineers the subset of fzf behavior that projmux
currently uses and maps it to native picker evidence. It supports the
experimental native picker engine and is not a public dependency-policy change.

## App fzf Surface

| fzf surface | projmux usage | Native status | Evidence |
| --- | --- | --- | --- |
| `--prompt` | AI, settings, shell update, switch, sessions, notify | Covered | `renderNativeInteractive`, `renderNative`; `TestNativePromptLineIncludesInlineMatchCount` |
| `--height 100%` / `--border` | all interactive picker screens | Covered for fullscreen rounded border frame | `renderNativeFrame`; screen-height list budgeting; `TestNativeInteractiveRendersBorderFrame`; `TestNativeInteractiveUsesAvailableHeightForSimpleLists` |
| `--header` | AI, settings, shell update, notify | Covered | `renderNativeInteractive`, `renderNative`; settings native tests |
| `--footer` / `--footer-border line` | AI, settings, shell update, switch, sessions, notify | Covered for interactive native screens | `renderNativeInteractive` reserves bottom footer space and renders a separator line; `TestNativeInteractiveRendersFooterAtBottom` |
| `--ansi` | colored row labels from render package | Covered | native writes row labels directly, strips ANSI escapes from default search text, and restores selected-row styling after embedded ANSI resets; `TestFilterItemsIgnoresANSIEscapeSequences`; `TestNativeInteractiveUsesCurrentStyleForSimpleSelection`; Docker e2e shows ANSI rows |
| hidden value after tab delimiter | all picker selections and default fzf matching | Covered by `picker.Item.Value` and default search text | `fzf.PickerOptions`; `TestNativeRunnerFiltersAndSelectsByNumber`; `TestFilterItemsSearchesHiddenValueWhenNoSearchKey` |
| plain fzf candidates without structured entries | legacy runner call shape | Covered | `fzf.PickerOptions`; `TestPickerOptionsFromFZFMapsCandidatesWhenEntriesAreEmpty` |
| search key filtering (`--nth`/reload filter file) | switch/sessions/notify entries | Covered by `Item.SearchText` with fzf reload order preservation | `FilterItems`; `TestFilterItemsUsesSearchTextNotMetadata`; `TestFilterItemsPreservesSearchKeyOrder` |
| default `--smart-case` matching | all searchable picker rows | Covered | native filter keeps lower-case queries case-insensitive and uppercase queries case-sensitive; `TestFilterItemsUsesFZFSmartCase` |
| fzf match highlighting | searchable simple picker rows | Covered for non-search-key simple rows | native highlights matched visible label runes while preserving embedded ANSI style; search-key reload lists intentionally keep fzf disabled-filter rendering without match highlights; `TestNativeInteractiveHighlightsSimpleQueryMatches`; `TestNativeInteractiveDoesNotHighlightSearchKeyReloadLists` |
| `--disabled --no-input` | navigation-only notify sidebar | Covered | native suppresses prompt/query editing and ignores printable non-action input; `TestNativeInteractiveDisableSearchIgnoresPrintableInput`; Docker no-fzf e2e asserts notify prompt is hidden |
| fuzzy result ranking | simple non-search-key picker UX | Covered with fzf V2 dynamic scoring for normal app rows | `fuzzyScore`; `TestFilterItemsRanksBetterMatchesFirst`; `TestFilterItemsPrefersFZFBoundaryAndCamelCaseMatches`; `TestFuzzyScoreMatchesFZFV2ReferenceScores` |
| `--scrollbar █` | long switch/session/settings lists | Covered for app lists | `nativeListLinesWithScrollbar`; `TestNativeInteractiveUsesScrollbarForLongLists` |
| `--read0` multi-line rows | switch, sessions, notify | Covered | `Options.MultiLine`; `TestNativeInteractiveRendersFZFLikeMultilineSelection` |
| `--gap --gap-line ─` | switch, sessions, notify multi-line rows | Covered for app multiline rows | `nativeGapLine`, row-budgeted range; `TestNativeInteractiveRendersMultilineGapLine`, `TestNativeVisibleRangeCountsMultilineRenderedRows` |
| `--marker-multi-line` | selected switch/session/notify cards | Covered for app multiline rows | `nativeContinuation`; `TestNativeInteractiveRendersSelectedMultilineContinuationMarker` |
| fzf current row colors | simple and multi-line rows | Covered for app rows | `nativeCurrentStart`, `nativePointer`; `TestNativeSelectedContentKeepsCurrentStyleAfterReset`; `TestNativeInteractiveUsesCurrentStyleForSimpleSelection` |
| `--expect` keys | Enter/Ctrl-X/Alt-P/notify keys | Covered | `fzf.PickerOptions`; `TestNativeInteractiveSupportsCustomExpectKeys` |
| printable expect keys | notify sidebar `x` ack | Covered | `TestNativeInteractiveSupportsPrintableExpectKeys`; Docker no-fzf e2e |
| control expect keys | notify sidebar `Ctrl-X`, settings `Ctrl-Alt-S` close | Covered | `TestNativeInteractiveSupportsControlExpectKeys`; `TestNativeInteractiveSupportsControlAltCloseKeys` |
| close `--bind key:abort` | Esc, Ctrl-C, Alt-N, Ctrl-Alt-S variants | Covered | `CloseActions`; `TestNativeRunnerUsesSharedCloseActions` |
| terminal CSI-u key encoding | app keybind probe sequences, Ghostty/kitty-style modified keys | Covered | `TestNativeInteractiveSupportsCSIuAppKeyBindings` |
| `execute-silent(...)+refresh-preview` | switch/session preview cycling | Covered for command execution and rerender loop | `fzf.PickerOptions`/`fzf.OptionsFromPicker`; `TestNativeInteractiveRunsCustomActionCommandAndRefreshes`; `TestPickerOptionsMapsFZFBindingsToContractActions`; Docker no-fzf e2e sends `Right` and `Alt-Down` before selection |
| `focus:execute-silent(...)` | switch sidebar focus | Covered | `runNativeFocusAction`; `TestNativeInteractiveRunsFocusActionOnSelectionChange` |
| `start:pos(N)` | switch sidebar initial row | Covered | `fzf.PickerOptions`/`fzf.OptionsFromPicker`; `TestPickerOptionsFromFZFMapsStartPosToInitialIndex`; `TestPickerOptionsMapsFZFBindingsToContractActions` |
| `--preview` | switch, sessions | Covered by command output | `nativePreviewLines`; `TestNativeInteractiveRendersSelectedPreview` |
| `--preview-window right,60%,border-left` | switch popup, sessions popup | Covered for projmux option shape | `renderNativeSplitPreview` renders a single-column left border without a synthetic title row and uses fzf-measured percent sizing; `TestNativeInteractiveRendersWidePreviewBesideList`; `TestNativePreviewWidthUsesPreviewWindowPercent` |
| `--preview-window down,25%,border-top` | switch sidebar | Covered for projmux option shape | `renderNativeDownPreview` renders an immediate top border without a synthetic title row and uses fzf-measured percent sizing; `TestNativeInteractiveRendersDownPreviewBelowList`; `TestNativePreviewHeightUsesPreviewWindowPercent` |
| preview scrolling | long switch/session preview output | Covered for keyboard preview scroll | `previewOffset`; `TestNativeInteractiveRendersPreviewOffset` |
| `--query` | typed settings path defaults | Covered | `Options.InitialQuery`; settings tests |
| `--print-query` accept-query mode | typed settings path prompts | Covered | `Options.AcceptQuery`; `TestNativeRunnerAcceptsTypedQuery` |
| query cursor editing | typed settings path prompts | Covered | Left/Right, Ctrl-A/E, Delete, Backspace, Ctrl-U/W query editing plus visible prompt cursor; `TestNativeInteractiveEditsTypedQueryAtCursor`; `TestNativeInteractiveSupportsQueryLineEditingKeys`; `TestNativeInteractiveCtrlUDeletesBeforeCursor`; `TestNativePromptLineRendersQueryCursor` |
| terminal arrow key variants | interactive selection in tmux/docker | Covered | CSI, SS3/application cursor, modified CSI tests; app TTY `/dev/tty` fallback; raw TTY EOF polling keeps split ESC sequences from leaking into the query |
| fzf navigation keys | interactive selection in searchable lists | Covered | native maps `Ctrl-N` to down and `Ctrl-P`/`Ctrl-K` to up when not claimed by a custom action; `TestNativeInteractiveSupportsFZFNavigationKeys` |
| alternate-screen lifecycle | fzf fullscreen picker screen restore | Covered | `nativeScreenEnter`; `TestNativeInteractiveUsesAlternateScreen` |
| frame content width | fzf border inner width | Covered | `ContentLayout` uses the frame inner width so separators and rows reach the right border; `TestRendererContentLayoutUsesFrameInnerWidth` |
| tmux popup frame interaction | native picker popups launched through `popup-toggle` | Covered for native backend popups | `popup-toggle` passes tmux `display-popup -B` when `PROJMUX_PICKER_BACKEND=native`, so the native picker owns the visible frame instead of double-drawing with the tmux popup border; `TestAppRunTmuxPopupToggleUsesBorderlessPopupForNativeBackend` |
| redraw flicker/top clipping | keyboard navigation in exact-height tmux popup | Partially covered | native redraws use synchronized updates plus row diffs after the first frame, skip unchanged frames, and frame rendering avoids trailing bottom-border CRLF; `TestNativeInteractiveWrapsRedrawsInSynchronizedUpdates`; `TestFrameUpdateRendererSkipsUnchangedFrame`; `TestRendererRenderFrameUsesCRLFRowsForRawTTY` |

## Native Surface Architecture

- `internal/ui/picker` remains the backend-neutral contract and owns fzf/native
  routing, keyboard input, fuzzy filtering, action dispatch, preview command
  execution, and result handling.
- `internal/ui/projmuxpicker` owns projmux-native visual composition: frame,
  redraw updates, ANSI width/truncation, theme tokens, prompt/footer/list
  rendering, selected row styling, scrollbars/gap rows, and preview pane
  geometry/rendering.
- `internal/ui/fzf` owns the compatibility adapter in both directions:
  `picker.Options` becomes fzf flags/bindings for fallback, and legacy
  `fzf.Options` becomes `picker.Options` for the native backend. This keeps app
  code closer to a DI-style picker contract instead of embedding fzf binding
  strings at each call site.
- `intfzf.NewPickerRunner()` adapts fzf to the same `picker.Runner` interface
  as `picker.NativeRunner`, so follow-up branches can inject either backend at
  a narrower boundary without deleting the existing fzf runner.
- Settings > Labs > Picker Engine persists the selected backend in
  `~/.config/projmux/picker-backend` and updates the live tmux server
  environment through `PROJMUX_PICKER_BACKEND`, while direct environment
  overrides still win over saved config.
- The split lets projmux grow a first-party picker design independently from
  the fzf compatibility adapter.

## Verified Flows

- `ai` picker/settings: native backend routing covered by app tests. Docker
  no-fzf e2e also types `Codex` into `projmux ai settings` and verifies the
  native simple picker writes the `codex` mode without fzf.
- `shell` update prompt: native backend routing covered by shared fzf-to-native
  adapter and settings-style typed prompt tests.
- `settings`: native backend exercised in unit tests and Docker no-fzf e2e
  using Enter plus arrow-key navigation under a PTY.
- `settings > Labs`: unit-covered backend toggle writes
  `~/.config/projmux/picker-backend`, updates the tmux global
  `PROJMUX_PICKER_BACKEND`, and lets env override saved config.
- `switch --ui=sidebar`: Docker no-fzf e2e creates sample projects, types
  `bravo`, selects `bravo-web`, and confirms the opened tmux shell path.
- `switch --ui=popup`: Docker no-fzf e2e creates existing tmux sessions using
  the app's session naming convention, types `bravo`, sends `Right` and
  `Alt-Down` to exercise preview window/pane cycle, asserts the stored preview
  cursor, selects `bravo-web`, and confirms the opened tmux shell path.
- `sessions --ui=popup`: Docker no-fzf e2e creates existing tmux sessions,
  types `bravo`, sends `Right` and `Alt-Down` to exercise preview window/pane
  cycle, asserts the stored preview cursor, selects `bravo-web`, and confirms
  the opened tmux shell path.
- `notify sidebar`: native routing is unit-covered; Docker no-fzf e2e pushes a
  notification, presses printable expect key `x`, and verifies the row is acked.

## Experimental Boundaries

- Preview-window parity is covered for the concrete projmux option shapes
  (`right,60%,border-left` and `down,25%,border-top`) with fzf-measured percent
  sizing. The full fzf preview-window grammar, threshold alternatives, sticky
  headers, and offset expressions are intentionally outside this POC surface.
- fzf V2 dynamic scoring is covered for normal app-length non-search-key
  rows, including reference scores for boundary, delimiter, camelCase/number,
  gap, and consecutive bonuses. Very large `query * row` matrices intentionally
  fall back to the greedy scorer to avoid pathological memory use. Search-keyed
  app pickers preserve fzf's `--disabled` reload order instead of score-sorting.
- Mouse support is not implemented. projmux does not currently expose mouse
  picker workflows, so this is outside the required app surface unless new
  workflows depend on it.
- The public doctor/docs dependency policy still says `fzf` is required. This
  is intentional while the native engine remains experimental.
- Draft PR: https://github.com/crevissepartners/projmux/pull/98.

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
