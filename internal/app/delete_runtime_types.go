package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

// CLI spellings reference the shared execution owner; no implementation is kept here.
const runtimePane = mutationruntime.RuntimePane
const runtimeSession = mutationruntime.RuntimeSession
const runtimeWindow = mutationruntime.RuntimeWindow

type paneLiveDeletePlan = mutationruntime.PaneLiveDeletePlan
type paneLiveDeleteTarget = mutationruntime.PaneLiveDeleteTarget
type paneReplacementReceipt = mutationruntime.PaneReplacementReceipt
type paneReplacementShell = mutationruntime.PaneReplacementShell
type runtimeObject = mutationruntime.RuntimeObject
