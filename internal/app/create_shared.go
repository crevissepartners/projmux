package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type createOperation = mutationruntime.CreateOperation
type createPreReconcile = mutationruntime.CreatePreReconcile

var markSupervisedSpawn = mutationruntime.MarkSupervisedSpawn
var paneWindowUID = mutationruntime.PaneWindowUID
var projectTerminations = mutationruntime.ProjectTerminations

type liveSessionIdentity = mutationruntime.LiveSessionIdentity
type materializer = mutationruntime.Materializer
type sessionFirstPaneActivation = mutationruntime.SessionFirstPaneActivation
type IdentityPlanWrite = mutationruntime.IdentityPlanWrite

type runtimeOwnerSet = mutationruntime.RuntimeOwnerSet
type runtimeOwners = mutationruntime.RuntimeOwners

var attributeCreatedWindow = mutationruntime.AttributeCreatedWindow
var splitLayoutBatchFormat = mutationruntime.SplitLayoutBatchFormat

var strictTmuxRows = mutationruntime.StrictTmuxRows

type registryReconciler = mutationruntime.RegistryReconciler

type livePaneInventory = mutationruntime.LivePaneInventory

var returnAbsentProcessWindowProjection = mutationruntime.ReturnAbsentProcessWindowProjection

type runtimeMutationMetadataMirror = mutationruntime.RuntimeMutationMetadataMirror

type liveRuntimeInventory = mutationruntime.LiveRuntimeInventory

var observeRuntime = mutationruntime.ObserveRuntime

type aiPaneGeometry = mutationruntime.AiPaneGeometry
type plannedPaneResize = mutationruntime.PlannedPaneResize

var planEvenSplitResizes = mutationruntime.PlanEvenSplitResizes
var splitLayoutPeers = mutationruntime.SplitLayoutPeers
var resizePanesEvenly = mutationruntime.ResizePanesEvenly

type superviseSpec = mutationruntime.SuperviseSpec

const superviseServerDefaultCommandFlag = mutationruntime.SuperviseServerDefaultCommandFlag

var superviseArgv = mutationruntime.SuperviseArgv

type resolvedPaneCommand = mutationruntime.ResolvedPaneCommand

var resolvePaneCommand = mutationruntime.ResolvePaneCommand

var issuePaneActivation = mutationruntime.IssuePaneActivation
var observeActivationRuntime = mutationruntime.ObserveActivationRuntime

func lifecycleProjectionInput(event lifecycleDirtyEvent) mutationruntime.LifecycleProjectionEvent {
	return mutationruntime.LifecycleProjectionEvent{Processes: event.processes, Target: event.target, PaneUID: event.paneUID, RuntimePaneID: event.runtimePaneID, RuntimeSessionID: event.runtimeSessionID, RuntimeWindowID: event.runtimeWindowID, Generation: event.generation, TeardownKind: event.teardownKind}
}
func lifecycleProjectionTargets(reg coremetadata.Registry, live, windows map[string]bool, event lifecycleDirtyEvent) []coremetadata.TerminationProjectionInput {
	return mutationruntime.LifecycleProjectionTargets(reg, live, windows, lifecycleProjectionInput(event))
}
func lifecycleWindowUnlinkedWindow(reg coremetadata.Registry, event lifecycleDirtyEvent) (*coremetadata.Window, bool) {
	return mutationruntime.LifecycleWindowUnlinkedWindow(reg, lifecycleProjectionInput(event))
}

const registrySessionClaimControl = mutationruntime.RegistrySessionClaimControl
const registrySessionClaimRefused = mutationruntime.RegistrySessionClaimRefused
