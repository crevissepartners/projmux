package runtime

import (
	"context"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

type CreateKind string

const (
	CreateKindWindow CreateKind = "window"
	CreateKindPane   CreateKind = "pane"
	CreateKindAgent  CreateKind = "agent"
	CreateKindResume CreateKind = "resume"
)

type CreatePhase int

const (
	CreatePhaseGuard CreatePhase = iota
	CreatePhaseFirstReconcile
	CreatePhaseOperation
	CreatePhaseSecondReconcile
	CreatePhaseReprove
	CreatePhaseStoreWrite
	createPhaseCount
)

type CreatePhases [createPhaseCount]*time.Duration
type CreateOutcome struct {
	Kind           CreateKind
	Result         string
	Duration       time.Duration
	LockHeld       *time.Duration
	Phases         CreatePhases
	SpawnToRelease *time.Duration
}
type CreateRecorder interface{ Record(CreateOutcome) }
type CreateStore struct {
	Load     func() (coremetadata.Registry, error)
	Snapshot func() (coremetadata.Registry, error)
	Update   func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	Mutator  func() coremetadata.Mutator
}
type CreateTransaction struct {
	Store              *CreateStore
	Runtime            *Materializer
	Reconciler         *RegistryReconciler
	EnsureRuntimeRoute func(context.Context) error
	NewOperationID     func() (string, error)
	Now                func() time.Time
	OutcomeClock       func() time.Time
	Outcomes           CreateRecorder
}
type LifecycleProjectionEvent struct {
	Processes                                                             resourcegraph.ProcessInventory
	Target                                                                TmuxTransport
	PaneUID, RuntimePaneID, RuntimeSessionID, RuntimeWindowID, Generation string
	TeardownKind                                                          coremetadata.TeardownEventKind
}
type ProcessSplitAnchor interface{ AdmitSplitAnchor(string) error }

func (m *Materializer) SplitPane(ctx context.Context, anchorPaneID, placement, cwd string, command []string, processAnchor ProcessSplitAnchor) (string, error) {
	admit := func(string) error { return nil }
	if processAnchor != nil {
		admit = processAnchor.AdmitSplitAnchor
	}
	return m.MaterializeRuntime.SplitPane(ctx, anchorPaneID, placement, cwd, command, admit)
}

const DialogueReplyOnlyFlag = "dialogue-reply-only"
