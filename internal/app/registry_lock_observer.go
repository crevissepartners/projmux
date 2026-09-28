package app

import (
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// ObserveRegistryLock installs the process-wide Registry lock observer for one
// CLI invocation: every lock acquisition this process makes is handed to the
// invocation's registry.lock.acquisition recorder, which journals the ones that
// waited or held for at least a second, or timed out. The record is attributed
// to the invocation's catalog command, classified from args; no argv text
// reaches the journal. A nil recorder installs nothing, and neither does an
// invocation that must never append to the journal -- Doctor, the support
// report, and the retired no-write argv -- so a slow locked Registry read
// inside them cannot break their no-write contract. The returned function
// restores the previous observer.
func ObserveRegistryLock(lifecycle *diagnostics.LifecycleRecorder, args []string) (restore func()) {
	if lifecycle == nil || diagnostics.JournalForbidden(args) {
		return func() {}
	}
	return intmetadata.SetLockObserver(newRegistryLockObserver(lifecycle.RegistryLock(diagnostics.Classify(args))))
}

// newRegistryLockObserver maps the Store's own observation onto the journal's
// closed vocabulary. The observer runs after the lease is released, so its
// append never lengthens another writer's wait; the Store also recovers from
// any panic here, so a journal failure never reaches the Registry operation.
func newRegistryLockObserver(recorder *diagnostics.RegistryLockRecorder) intmetadata.LockObserver {
	if recorder == nil {
		return nil
	}
	return func(observation intmetadata.LockObservation) {
		acquisition := diagnostics.RegistryLockAcquisition{
			Operation: registryLockOperation(observation.Operation),
			Wait:      observation.Wait,
		}
		switch observation.Outcome {
		case intmetadata.LockOutcomeReleased:
			held := observation.Held
			acquisition.Held = &held
			acquisition.Result = diagnostics.RegistryLockSuccess
			if observation.Failed {
				acquisition.Result = diagnostics.RegistryLockMutationFailed
			}
		case intmetadata.LockOutcomeTimeout:
			acquisition.Result = diagnostics.RegistryLockTimeout
		case intmetadata.LockOutcomeAcquireFailed:
			acquisition.Result = diagnostics.RegistryLockAcquireFailed
		default:
			return
		}
		recorder.Record(acquisition)
	}
}

// registryLockOperation translates the Store's entry point name. An unknown
// name maps to the empty operation, which the recorder drops.
func registryLockOperation(operation intmetadata.LockOperation) diagnostics.RegistryLockOperation {
	switch operation {
	case intmetadata.LockOperationUpdate:
		return diagnostics.RegistryLockUpdate
	case intmetadata.LockOperationUpdateConvergent:
		return diagnostics.RegistryLockUpdateConvergent
	case intmetadata.LockOperationLoad:
		return diagnostics.RegistryLockLoad
	case intmetadata.LockOperationMigrate:
		return diagnostics.RegistryLockMigrate
	case intmetadata.LockOperationAdmissionBarrier:
		return diagnostics.RegistryLockAdmissionBarrier
	default:
		return ""
	}
}
