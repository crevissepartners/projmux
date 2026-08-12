# Native Picker

The native picker is the product picker for every interactive selection flow.
There is no runtime backend selection, saved picker selector, or external
picker process. `internal/ui/picker` owns the interaction contract and
`internal/ui/projmuxpicker` owns its visual composition.

## Product Contract

| Area | Behavior | Primary evidence |
| --- | --- | --- |
| Items and results | Structured items keep display labels, stable values, optional search text, multi-line metadata, and action results separate. `internal/ui/pickercompat` maps older internal option/result shapes into this contract; it is not a runtime backend. | `TestPickerOptionsFromCompatPickerMapsCandidatesWhenEntriesAreEmpty`; `TestPickerOptionsFromCompatPickerPreservesTheme` |
| Search | Lower-case queries are case-insensitive, uppercase queries are case-sensitive, hidden values remain searchable when no explicit search key exists, explicit search-key lists preserve caller order, and simple rows receive fuzzy ranking and match highlighting. | `TestFilterItemsUsesSmartCase`; `TestFilterItemsSearchesHiddenValueWhenNoSearchKey`; `TestFilterItemsPreservesSearchKeyOrder`; `TestFilterItemsRanksBetterMatchesFirst` |
| Navigation | Up/Down, Ctrl-J/Ctrl-N, and Ctrl-K/Ctrl-P wrap selection; PageUp/PageDown and Home/End clamp or jump; empty lists remain safe. Ctrl-N and Ctrl-P remain real navigation keys unless a caller claims the key as a custom action. | `TestNativeInteractiveSupportsControlNavigationKeys`; `TestNativeInteractiveWrapsPreviousNavigationKeys`; `TestNativeInteractiveWrapsNextNavigationKeys`; `TestNativeInteractiveNavigationKeysAreNoopForEmptyList`; `TestNativeInteractiveJumpNavigationRemainsClamped` |
| Editing | Left/Right, Ctrl-A/E, Delete, Backspace, Ctrl-U/W, printable input, accept-query mode, and a visible query cursor are supported. | `TestNativeInteractiveEditsTypedQueryAtCursor`; `TestNativeInteractiveSupportsQueryLineEditingKeys`; `TestNativeInteractiveCtrlUDeletesBeforeCursor`; `TestNativeRunnerAcceptsTypedQuery` |
| Actions | Enter accepts, shared close actions abort, printable/control expect keys return stable action keys, command actions can refresh previews, and mutable actions can update items without restarting the picker. | `TestNativeRunnerUsesSharedCloseActions`; `TestNativeInteractiveSupportsPrintableExpectKeys`; `TestNativeInteractiveRunsCustomActionCommandAndRefreshes`; `TestNativeInteractiveCustomActionMutatesItemsAndRefreshes` |
| Deferred state | Deferred and event-triggered updates preserve query and selection by value, can repeat, and may explicitly choose a new focus value. | `TestNativeInteractiveDeferredUpdateTriggerRefreshesRepeatedly`; switch and notify sidebar mutable-refresh app tests |
| Preview | Popup previews use a right split, sidebar previews use a bottom split, control bytes and tabs are normalized before clipping, and preview scrolling/cycling rerenders in place. | `TestNativeInteractiveRendersWidePreviewBesideList`; `TestNativeInteractiveRendersDownPreviewBelowList`; `TestRenderSplitPreviewRowsNormalizesPreviewTabsBeforeTruncating`; `TestNativeInteractiveRendersPreviewOffset` |
| Mouse | SGR mouse input focuses on primary down, follows drag, accepts on matching release, and scrolls with the wheel. | `TestNativeInteractiveSelectsOnMouseRelease`; `TestNativeInteractiveMouseDragFollowsSelection`; `TestNativeInteractiveSupportsMouseWheelSelection` |
| Lifecycle | Interactive runs use the alternate screen, synchronized/coalesced frame updates, controlling-TTY fallback, and deterministic reader cleanup. | `TestNativeInteractiveUsesAlternateScreen`; `TestNativeInteractiveWrapsRedrawsInSynchronizedUpdates`; picker/setup lifecycle tests in `docs/agent-workflow.md` |

## Rendering And Popup Chrome

The renderer owns rounded frames, optional titlebars and chips, search/header
separators, footer placement, selected-row styling, proportional scrollbars,
multi-line gaps, preview geometry, ANSI restoration, and terminal-cell width for
CJK text, emoji, and combining marks. Frame-owned cells inherit the effective
theme's `surface` and `chrome_foreground` values, including padding after
embedded resets.

Picker popups are always borderless tmux popups (`display-popup -B`) so the
native renderer owns the visible frame. The popup body receives a per-command
style derived from the effective theme; no global popup, pane, window, shell,
or status style is mutated. Project sidebars use a `20%` width with a `40`
column minimum, notification sidebars use `24%` with a `64` column minimum,
and both reserve the two statusbar rows when client height is known.

Primary evidence includes `TestAppRunTmuxPopupToggleUsesBorderlessNativePopup`,
`TestAppRunTmuxPopupToggleUsesNativePopupBodyStyleFromGlobalTheme`,
`TestBuildPopupToggleAppliesNativeBodyStyle`,
`TestSessionizerSidebarWidthUsesCompactMinimum`,
`TestNotifySidebarWidthUsesProductContract`, and
`TestSidebarPopupHeightLeavesStatusbarRows`.

## Fuzzy Scoring Provenance

The native fuzzy scorer intentionally follows the fzf V2 dynamic-scoring
algorithm for the maintained reference cases in `internal/ui/picker/backend_test.go`.
`TestFuzzyScoreMatchesFZFV2ReferenceScores` and
`TestFuzzyScoreRejectsFZFV2ReferenceNonMatches` are provenance fixtures; keep
their expected scores stable when changing filtering internals.

## Retired Artifact Contract

The retired picker selector environment variable and saved selector filename
are not runtime inputs. Their presence must not cause lookup, file reads,
warnings, deletion, rewriting, propagation to child popups, or any change to
the native path. There is deliberately no alias, migration, or stale-value
cleanup behavior.

Coverage is split by boundary:

- `TestRunNativePickerOptionDoesNotObserveRetiredBackendArtifacts` guards the
  direct picker path, fails if the retired env name is queried, and verifies a
  stale file is neither deleted nor replaced.
- `TestAppRunSwitchUsesNativePickerWithoutBackendLookup` guards the ordinary
  switch flow.
- `TestAppRunTmuxPopupToggleIgnoresRetiredBackendArtifacts` and
  `TestBuildPopupTogglePropagatesNativePickerEnvironmentWithoutRetiredBackend`
  guard popup chrome and child-environment construction.
- `test/e2e/linux-smoke.sh` launches the ordinary attached-client Settings
  popup with stale env/file values, completes the recorder flow, verifies the
  file inode/content are unchanged, and rejects visible compatibility output.

## Removed PoC Coverage Mapping

The retired standalone dependency sandbox and focused smoke duplicated product
coverage or tested a dependency policy that no longer exists. Its valid
behavior assertions remain covered as follows:

| Removed focused assertion | Maintained coverage |
| --- | --- |
| Settings and Labs render through the native picker | `TestSettingsUsesNativePicker`; `TestSettingsHubKeepsLabsSectionWithoutRetiredPickerChoices`; ordinary attached-client Settings recorder in `test/e2e/linux-smoke.sh` |
| Search chrome, smart-case filtering, native frame ownership, and full-height rendering | `TestNativeInteractiveSeparatesSearchHeaderFromList`; `TestFilterItemsUsesSmartCase`; `TestNativeInteractiveRendersBorderFrame`; `TestNativeInteractiveUsesAvailableHeightForSimpleLists` |
| AI Settings maps a typed selection to the saved mode | `TestAISettingsPickerSetsSelectedMode`; native picker option/result mapping tests |
| Switch popup/sidebar filtering, selection, preview cycling, and initial focus | `TestAppRunSwitchDefaultsToPopupAndOpensSelectedSession`; `TestSwitchCommandSupportsSidebarUI`; preview cycle and initial-position tests in `internal/app/switch_test.go` |
| Sessions popup filtering, selection, and preview cycling | `TestAppRunSessionsDefaultsToPopupAndOpensSelectedSession`; session popup preview/cycle tests in `internal/app/session_popup_test.go` |
| Notification sidebar navigation and mutable actions | `TestNotifySidebarUsesNativePicker`; notify sidebar ack/clear/deferred-refresh app tests |
| Launch-key close behavior and Ctrl-N/Ctrl-P navigation | `TestNativeInteractiveClosesOnMatchingLaunchCloseKey`; `TestNativeInteractiveSupportsControlNavigationKeys`; empty-list and wrap tests |
| Mouse selection and alternate-screen restoration | native mouse and lifecycle tests listed above |
| Generated shell config and the Alt-1 project-sidebar popup path | `TestShellWritesAppConfigAndRunsIsolatedTmux`; `TestAppRunTmuxPopupToggleOpensStandaloneSidebar`; popup frame/body-style tests listed above |
| Stale selector values do not affect the product | retained and strengthened in the unit/app/e2e negative coverage listed in the previous section |

The dependency-absence assertion is now a source-residue property rather than
a separate container scenario: production code contains no selector, resolver,
saved-selector access, propagation, or external picker launch path.

## Maintenance

Update this document and the maintained list in
[`docs/agent-workflow.md`](agent-workflow.md) whenever picker behavior changes
coverage level, gains a new product flow, or changes input/render/action
semantics.
