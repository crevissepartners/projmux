# Native Picker fzf Parity Map

This audit note reverse-engineers the subset of fzf behavior that projmux
currently uses and maps it to native picker evidence. It supports the default
native picker engine and is not a public dependency-policy change.

## App fzf Surface

| fzf surface | projmux usage | Native status | Evidence |
| --- | --- | --- | --- |
| `--prompt` | AI, settings, shell update, switch, sessions, notify | Covered | `renderNativeInteractive`, `renderNative`; `TestNativePromptLineIncludesInlineMatchCount` |
| prompt/list separation | native searchable picker chrome | Covered | native renders an explicit `Search` header label plus a separator under searchable prompt/count lines; `TestNativeInteractiveSeparatesSearchHeaderFromList`; Docker no-fzf e2e asserts the `Search` header in Settings, AI settings, mouse-click, switch sidebar, switch popup, sessions popup, and shell Alt-1 PTY logs |
| `--height 100%` / `--border` | all interactive picker screens | Covered for fullscreen rounded border frame | `renderNativeFrame`; screen-height list budgeting; conservative 80x24 fallback only when terminal size detection is unavailable; `TestNativeInteractiveRendersBorderFrame`; `TestNativeInteractiveUsesAvailableHeightForSimpleLists` |
| `--header` | AI, settings, shell update, notify | Covered | `renderNativeInteractive`, `renderNative`; settings native tests |
| `--footer` / `--footer-border line` | AI, settings, shell update, switch, sessions, notify | Covered for interactive native screens | `renderNativeInteractive` reserves bottom footer space and renders a separator line; `TestNativeInteractiveRendersFooterAtBottom` |
| `--ansi` | colored row labels from render package | Covered | native writes row labels directly, strips ANSI escapes from default search text, restores selected-row styling after embedded ANSI resets, and measures rendered cell width for Korean/CJK text, emoji, and combining marks; `TestFilterItemsIgnoresANSIEscapeSequences`; `TestNativeInteractiveUsesCurrentStyleForSimpleSelection`; `TestVisibleLenUsesTerminalCellWidth`; Docker e2e shows ANSI rows |
| hidden value after tab delimiter | all picker selections and default fzf matching | Covered by `picker.Item.Value` and default search text | `pickercompat.PickerOptions`; `TestNativeRunnerFiltersAndSelectsByNumber`; `TestFilterItemsSearchesHiddenValueWhenNoSearchKey` |
| plain fzf candidates without structured entries | compat option call shape | Covered | `pickercompat.PickerOptions`; `TestPickerOptionsFromCompatPickerMapsCandidatesWhenEntriesAreEmpty` |
| search key filtering (`--nth`/reload filter file) | switch/sessions/notify entries | Covered by `Item.SearchText` with fzf reload order preservation | `FilterItems`; `TestFilterItemsUsesSearchTextNotMetadata`; `TestFilterItemsPreservesSearchKeyOrder` |
| default `--smart-case` matching | all searchable picker rows | Covered | native filter keeps lower-case queries case-insensitive and uppercase queries case-sensitive; `TestFilterItemsUsesFZFSmartCase` |
| fzf match highlighting | searchable simple picker rows | Covered for non-search-key simple rows | native highlights matched visible label runes while preserving embedded ANSI style; search-key reload lists intentionally keep fzf disabled-filter rendering without match highlights; `TestNativeInteractiveHighlightsSimpleQueryMatches`; `TestNativeInteractiveDoesNotHighlightSearchKeyReloadLists` |
| `--disabled --no-input` | navigation-only notify sidebar | Covered | native suppresses prompt/query editing and ignores printable non-action input; `TestNativeInteractiveDisableSearchIgnoresPrintableInput`; Docker no-fzf e2e asserts notify prompt is hidden |
| fuzzy result ranking | simple non-search-key picker UX | Covered with fzf V2 dynamic scoring for normal app rows | `fuzzyScore`; `TestFilterItemsRanksBetterMatchesFirst`; `TestFilterItemsPrefersFZFBoundaryAndCamelCaseMatches`; `TestFuzzyScoreMatchesFZFV2ReferenceScores` |
| `--scrollbar █` | long switch/session/settings lists | Covered for app lists | `nativeListLinesWithScrollbarRows`; proportional multi-row thumb rendering in `projmuxpicker`; split-preview and sidebar list viewports keep a fixed row budget and scrollbar track; multi-line cards use rendered-row scroll units; `TestNativeInteractiveUsesScrollbarForLongLists`; `TestListLinesWithScrollbarUsesProportionalThumb`; `TestListLinesWithScrollbarMovesThumbGradually`; `TestListLinesWithScrollbarRowsKeepsViewportTrack`; `TestRenderSplitPreviewRowsKeepsRequestedViewport`; `TestNativeListScrollbarUnitsUseRenderedRowsForMultiline`; `TestNativeInteractiveKeepsMultilineScrollbarOnViewportTrack` |
| `--read0` multi-line rows | switch, sessions, notify | Covered | `Options.MultiLine`; `TestNativeInteractiveRendersFZFLikeMultilineSelection` |
| `--gap --gap-line ─` | switch, sessions, notify multi-line rows | Covered for app multiline rows | `nativeGapLine`, row-budgeted range; `TestNativeInteractiveRendersMultilineGapLine`, `TestNativeVisibleRangeCountsMultilineRenderedRows` |
| selected multi-line marker | selected switch/session/notify cards | Covered for app multiline rows | native uses the same compact pointer-width red `▌` gutter as the first selected project line, and metadata lines align to the project-name column without the old deeper indent; `nativeContinuation`; `TestNativeInteractiveRendersSelectedMultilineContinuationMarker`; `TestInteractiveRowLinesUsesCompactSelectedMetaIndent`; `TestInteractiveRowLinesAlignsUnselectedMetaWithProjectName` |
| fzf current row colors | simple and multi-line rows | Covered for app rows | `nativeCurrentStart`, `nativePointer`; pointer/continuation gutter tokens carry the current-row background; `TestNativeSelectedContentKeepsCurrentStyleAfterReset`; `TestNativeInteractiveUsesCurrentStyleForSimpleSelection` |
| `--expect` keys | Enter/Ctrl-X/Alt-P/notify keys | Covered | `pickercompat.PickerOptions`; `TestNativeInteractiveSupportsCustomExpectKeys` |
| printable expect keys | notify sidebar `a` ack and `x` non-critical clear | Covered | `TestNativeInteractiveSupportsPrintableExpectKeys`; Docker no-fzf e2e |
| control expect keys | notify sidebar `Ctrl-X`, settings `Ctrl-Alt-S` close | Covered | `TestNativeInteractiveSupportsControlExpectKeys`; `TestNativeInteractiveSupportsControlAltCloseKeys` |
| close `--bind key:abort` | Esc, Ctrl-C, Alt-N, Ctrl-Alt-S variants | Covered | `CloseActions`; `TestNativeRunnerUsesSharedCloseActions` |
| terminal modified-key encoding | legacy parser fixtures, Ghostty/kitty-style modified keys | Covered | native handles app-specific parser fixtures, generic modified letters/digits, and non-text keys such as Enter/Esc/Backspace/Tab; this is backend parity, not product fallback guidance; `TestNativeInteractiveSupportsCSIuAppKeyBindings` |
| `execute-silent(...)+refresh-preview` | switch/session preview cycling | Covered for command execution and rerender loop | `pickercompat.PickerOptions`/`pickercompat.OptionsFromPicker`; `TestNativeInteractiveRunsCustomActionCommandAndRefreshes`; `TestPickerOptionsMapsCompatBindingsToContractActions`; Docker no-fzf e2e sends `Right` and `Alt-Down` before selection |
| action-local/event-backed mutable refresh | notify sidebar `a` ack and `x` non-critical clear; notify sidebar queue-write event refresh; switch sidebar `Ctrl-X` kill | Covered for in-session row/live-state refresh without picker restart | `picker.Action.Mutate` returns a `DeferredUpdate`, notify queue-write events trigger the same `DeferredUpdate` path after an event arrives, and both reuse the native frame diff renderer plus value-then-clamp selection preservation; `TestNativeInteractiveCustomActionMutatesItemsAndRefreshes`; `TestNativeInteractiveCustomActionRefreshPreservesSelectedValue`; `TestNativeInteractiveDeferredUpdateTriggerRefreshesRepeatedly`; notify sidebar app tests assert one picker invocation with refreshed rows/live state and event subscription; switch sidebar kill app tests assert one native picker invocation with refreshed rows/preview and previous-live-session guard preservation |
| `focus:execute-silent(...)` | switch sidebar focus | Covered | native renders the selection frame diff before running sidebar focus commands so movement stays visible before tmux focus side effects; `runNativeFocusAction`; `TestNativeInteractiveRunsFocusActionOnSelectionChange` |
| `start:pos(N)` | switch sidebar initial row | Covered | `pickercompat.PickerOptions`/`pickercompat.OptionsFromPicker`; `TestPickerOptionsFromCompatPickerMapsStartPosToInitialIndex`; `TestPickerOptionsMapsCompatBindingsToContractActions` |
| `--preview` | switch, sessions | Covered by command output | `nativePreviewLines`; `TestNativeInteractiveRendersSelectedPreview` |
| `--preview-window right,60%,border-left` | switch popup, sessions popup | Covered for projmux option shape | `renderNativeSplitPreview` renders a single-column left border without a synthetic title row, uses fzf-measured percent sizing, normalizes preview tabs/control bytes before clipping, and pads both panes to fixed row widths so long preview rows do not wrap into extra vertical rows; `TestNativeInteractiveRendersWidePreviewBesideList`; `TestNativePreviewWidthUsesPreviewWindowPercent`; `TestRenderSplitPreviewRowsPadsBothPanes`; `TestRenderSplitPreviewRowsNormalizesPreviewTabsBeforeTruncating` |
| `--preview-window down,25%,border-top` | switch sidebar | Covered for projmux option shape | `renderNativeDownPreview` renders an immediate top border without a synthetic title row, uses fzf-measured percent sizing, and pads preview rows to the surface width; `TestNativeInteractiveRendersDownPreviewBelowList`; `TestNativePreviewHeightUsesPreviewWindowPercent`; `TestRenderDownPreviewPadsPreviewRows` |
| preview scrolling | long switch/session preview output | Covered for keyboard preview scroll | `previewOffset`; `TestNativeInteractiveRendersPreviewOffset` |
| `--query` | typed settings path defaults | Covered | `Options.InitialQuery`; settings tests |
| `--print-query` accept-query mode | typed settings path prompts | Covered | `Options.AcceptQuery`; `TestNativeRunnerAcceptsTypedQuery` |
| query cursor editing | typed settings path prompts | Covered | Left/Right, Ctrl-A/E, Delete, Backspace, Ctrl-U/W query editing plus visible prompt cursor; `TestNativeInteractiveEditsTypedQueryAtCursor`; `TestNativeInteractiveSupportsQueryLineEditingKeys`; `TestNativeInteractiveCtrlUDeletesBeforeCursor`; `TestNativePromptLineRendersQueryCursor` |
| terminal arrow key variants | interactive selection in tmux/docker | Covered | CSI, SS3/application cursor, modified CSI tests; up/down movement wraps at list boundaries while PageUp/PageDown and Home/End remain clamped or explicit jumps; app TTY `/dev/tty` fallback; raw TTY EOF polling keeps split ESC sequences from leaking into the query; `TestNativeInteractiveWrapsPreviousNavigationKeys`; `TestNativeInteractiveWrapsNextNavigationKeys`; `TestNativeInteractiveJumpNavigationRemainsClamped` |
| mouse support | optional fzf mouse picker interaction | Partially covered | native enables SGR mouse reporting in interactive alternate-screen mode, primary mouse down focuses the clicked row, primary mouse up applies it, and wheel input moves selection; drag gestures remain outside this POC; `TestNativeInteractiveSelectsOnMouseRelease`; `TestNativeInteractiveMouseDownOnlyFocuses`; `TestNativeInteractiveIgnoresMouseReleaseBeforeDown`; `TestNativeInteractiveSupportsMouseWheelSelection`; Docker no-fzf e2e clicks the AI settings row under a PTY |
| fzf navigation keys | interactive selection in searchable lists | Covered | native maps modified-key `Ctrl-J` plus `Ctrl-N` to down and `Ctrl-P`/`Ctrl-K` to up when not claimed by a custom action; up/down-family movement is safe on empty lists; raw LF remains Enter for PTY compatibility; `TestNativeInteractiveSupportsFZFNavigationKeys`; `TestNativeInteractiveNavigationKeysAreNoopForEmptyList` |
| alternate-screen lifecycle | fzf fullscreen picker screen restore | Covered | native frame updates and screen exit return to column 0 before terminal control sequences, screen exit resets styles plus clears the alternate buffer from the home cursor before restore, and real TTY restores get a short settle window before caller handoff; `nativeScreenEnter`; `TestNativeInteractiveUsesAlternateScreen`; `TestRenderFullFrameUpdateAlwaysHomesAndWritesFrame` |
| frame content width | fzf border inner width | Covered | `ContentLayout` uses the frame inner width so separators and rows reach the right border; `TestRendererContentLayoutUsesFrameInnerWidth` |
| picker-owned app background | native picker frame interior | Covered for renderer-owned cells | `ThemeFromEffective` applies built-in fallback and explicit effective `background` / `chrome_foreground` SGR to the native frame, and frame rows resume the app style after embedded resets so content padding, empty no-footer rows, footer rows, scrollbars, and preview gaps do not leak terminal default background; `TestThemeFromEffectiveFallbackPaintsFrameBackground`; `TestRendererFrameBackgroundResumesAfterContentResetBeforePadding`; `TestNativeInteractiveNoFooterBlankRowsUseThemeBackground`; `TestNativeInteractiveSplitPreviewGapsUseThemeBackground`; `TestNativeInteractiveSettingsAIBadgeStyleLongPreviewClampsFrameRows` |
| tmux popup frame interaction | native picker popups launched through `popup-toggle` | Covered for native backend popups | `popup-toggle` passes tmux `display-popup -B` when `PROJMUX_PICKER_BACKEND=native`, so the native picker owns the visible frame instead of double-drawing with the tmux popup border; tmux 3.4 per-command `display-popup -s` is used for the popup body style, setting `bg` from `surface` and `fg` from `chrome_foreground` so tmux's blank/pre-draw popup body matches the native picker surface before the app renderer paints; no global `popup-style`, `popup-border-style`, shell pane background, `default-style`, `window-style`, OSC background, or status/window palette options are changed; native Alt-1 uses a compact native-only minimum while fzf keeps the previous project sidebar minimum; Alt-1/Alt-2 sidebar heights reserve two bottom statusbar rows; Alt-2 notify sidebar keeps the fzf-like `24%` / min `64` baseline; `TestAppRunTmuxPopupToggleUsesBorderlessPopupForNativeBackend`; `TestAppRunTmuxPopupToggleUsesNativePopupBodyStyleFromEffectiveTheme`; `TestBuildPopupToggleWithPickerBackendStylesNativeOnly`; `TestBuildDisplayPopupArgsAddsBodyStyle`; `TestSessionizerSidebarWidthKeepsFZFMinimum`; `TestSidebarPopupHeightLeavesStatusbarRows`; `TestNotifySidebarWidthMatchesFZFBaseline`; `TestAppRunTmuxPopupToggleKeepsNotifySidebarFZFSizingForNative` |
| optional native titlebar | native picker popup frame | Covered for empty-title compatibility and opt-in titles | `picker.Options.Title`; `RenderFrameWithTitle`; `ContentLayoutWithTitle`; empty titles keep the prior frame unchanged, non-empty titles render in a picker-owned section below the top border, titlebar text/divider/chip gaps inherit the frame `background` / `chrome_foreground` instead of a separate titlebar overlay ANSI layer, and the native Alt-1 project sidebar opts into `Projects`; `TestRendererRenderFrameWithTitleKeepsDefaultWhenTitleEmpty`; `TestRendererRenderFrameWithTitleUsesTitlebarRow`; `TestRendererContentLayoutWithTitleReservesTitlebarRow`; `TestNativeInteractiveRendersOptionalTitlebar`; `TestSwitchCommandNativeSidebarSetsTitle` |
| redraw flicker/top clipping | keyboard navigation in exact-height tmux popup | Partially covered | native redraws use synchronized updates plus coalesced row diffs after the first frame, skip unchanged frames, render frame diffs before sidebar focus commands, frame rendering avoids trailing bottom-border CRLF, and screen exit clears the alternate buffer before restore; `TestNativeInteractiveWrapsRedrawsInSynchronizedUpdates`; `TestFrameUpdateRendererSkipsUnchangedFrame`; `TestFrameUpdateRendererCoalescesEachFrameUpdate`; `TestRendererRenderFrameUsesCRLFRowsForRawTTY`; `TestNativeInteractiveUsesAlternateScreen` |

## Native Surface Architecture

- `internal/ui/picker` remains the backend-neutral contract and owns native
  routing, keyboard input, fuzzy filtering, action dispatch, preview command
  execution, and result handling.
- `internal/ui/projmuxpicker` owns projmux-native visual composition: frame,
  redraw updates, ANSI width/truncation, theme tokens, prompt/footer/list
  rendering, selected row styling, scrollbars/gap rows, and preview pane
  geometry/rendering.
- `internal/ui/pickercompat` remains as the internal compatibility option/result
  mapper from older app option shapes to `picker.Options` for the native
  backend. It is not a runtime backend. This keeps app code closer to a
  DI-style picker contract instead of embedding binding strings at each call
  site.
- Settings > Labs remains available, but picker backend selection has been
  retired. Deprecated saved/env backend values normalize to native.
- The split lets projmux grow a first-party picker design independently from
  the compatibility option/result mapper.

## Frame Chrome ANSI

Native picker frame chrome is normalized in `internal/ui/projmuxpicker`.
Titlebar text, title dividers, and chip-strip gaps inherit the frame
`background` / `chrome_foreground` instead of applying a second titlebar overlay ANSI layer;
chip bodies can still carry active/inactive/disabled tones. Search prompt and
footer separators fill the available frame width, and header, row, footer, and
preview lines close any active SGR style before padding or frame borders can
inherit it. This phase does not add popup modes or change the `popup-toggle`
contract; native popups still rely on the existing borderless tmux popup path.

## Verified Flows

- `ai` picker/settings: native backend routing covered by app tests. Docker
  no-fzf e2e also types `Codex` into `projmux ai settings` and verifies the
  native simple picker writes the `codex` mode without fzf.
- `shell` update prompt: native backend routing covered by shared compat-to-native
  bridge and settings-style typed prompt tests.
- `settings`: native backend exercised in unit tests and Docker no-fzf e2e
  using Enter plus arrow-key navigation under a PTY. The Docker e2e also fails
  if the Settings flows write tmux no-server noise to stderr while running
  outside tmux.
- `settings > Labs`: unit-covered backend toggle writes
  `~/.config/projmux/picker-backend`, updates the tmux global
  `PROJMUX_PICKER_BACKEND`, and lets env override saved config.
- `switch --ui=sidebar`: Docker no-fzf e2e creates sample projects, types
  `bravo`, selects `bravo-web`, and confirms the opened tmux shell path.
- `switch --ui=popup`: Docker no-fzf e2e creates existing tmux sessions using
  the app's session naming convention, runs the picker under a 150x30 PTY,
  types `bravo`, sends `Right` and `Alt-Down` to exercise preview window/pane
  cycle, asserts the popup stays on the right-side preview layout instead of
  inline preview, asserts the stored preview cursor, selects `bravo-web`, and
  asserts tmux reports the selected session's active target on the expected
  window with the expected pane path.
- `sessions --ui=popup`: Docker no-fzf e2e creates existing tmux sessions,
  runs the picker under a 150x30 PTY, types `bravo`, sends `Right` and
  `Alt-Down` to exercise preview window/pane cycle, asserts the popup stays on
  the right-side preview layout instead of inline preview, asserts the stored
  preview cursor, selects `bravo-web`, and asserts tmux reports the selected
  session's active target on the expected window with the expected pane path.
- `notify sidebar`: native routing is unit-covered; app tests cover
  queue-write event subscription and picker tests cover repeated
  event-triggered deferred refresh. Docker no-fzf e2e pushes a notification,
  presses printable expect key `a`, and verifies the row is acked.

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
- Mouse support is intentionally narrow in this POC: primary mouse down focuses
  the clicked row, primary mouse up applies it, and wheel input moves selection.
  Drag gestures and the full fzf mouse grammar are follow-up work.
- The public doctor/docs dependency policy no longer includes an external
  picker binary.
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
