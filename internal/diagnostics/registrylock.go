package diagnostics

import (
	"fmt"
	"time"
)

// registryLockAcquisitionEvent is the record of one Registry lock acquisition
// that waited or held long enough to matter, or that timed out.
const registryLockAcquisitionEvent = "registry.lock.acquisition"

const (
	// RegistryLockRecordThreshold is the wait or hold at which an acquisition
	// is recorded. Below it on both counts, and not timed out, an acquisition
	// writes nothing: the lock is taken on every Registry read and mutation,
	// and a record per acquisition would bury the ones worth reading.
	RegistryLockRecordThreshold = time.Second
	// RegistryLockWarnThreshold is the hold at which a successful acquisition
	// is recorded at level warn: one sixth of the 30s deadline every waiter
	// behind it is spending.
	RegistryLockWarnThreshold = 5 * time.Second
)

// RegistryLockOperation is the closed name of the Registry Store entry point
// that took the lock. It is carried in the `operation` field.
type RegistryLockOperation string

const (
	RegistryLockUpdate           RegistryLockOperation = "update"
	RegistryLockUpdateConvergent RegistryLockOperation = "update-convergent"
	RegistryLockLoad             RegistryLockOperation = "load"
	RegistryLockMigrate          RegistryLockOperation = "migrate"
	RegistryLockAdmissionBarrier RegistryLockOperation = "admission-barrier"
)

var registryLockOperations = [...]RegistryLockOperation{
	RegistryLockUpdate, RegistryLockUpdateConvergent, RegistryLockLoad, RegistryLockMigrate, RegistryLockAdmissionBarrier,
}

func validRegistryLockOperation(operation RegistryLockOperation) bool {
	for _, candidate := range registryLockOperations {
		if operation == candidate {
			return true
		}
	}
	return false
}

// RegistryLockResult is how one acquisition ended, as the record states it.
//
//   - success: the lease was held, the locked work succeeded, and the lease
//     was released.
//   - mutation-failed: the lease was held and released, but the locked work
//     returned an error -- a refused callback, a failed validation or write.
//   - timeout: the acquisition gave up on the lock deadline.
//   - acquire-failed: the acquisition failed for any other reason.
type RegistryLockResult string

const (
	RegistryLockSuccess        RegistryLockResult = "success"
	RegistryLockMutationFailed RegistryLockResult = "mutation-failed"
	RegistryLockTimeout        RegistryLockResult = "timeout"
	RegistryLockAcquireFailed  RegistryLockResult = "acquire-failed"
)

// Closed codes of the error results. A success carries no code.
const (
	codeRegistryLockTimeout       Code = "registry.lock.timeout"
	codeRegistryLockAcquireFailed Code = "registry.lock.acquire-failed"
	codeRegistryMutationFailed    Code = "registry.mutation.failed"
)

// RegistryLockAcquisition is one acquisition as the Registry Store measured
// it. Wait runs from before the acquisition to the grant or the give-up. Held
// runs from the grant to after the release, and is nil when no lease was held.
type RegistryLockAcquisition struct {
	Operation RegistryLockOperation
	Result    RegistryLockResult
	Wait      time.Duration
	Held      *time.Duration
}

// RegistryLockRecorder appends `registry.lock.acquisition` records under the
// invocation run ID, attributed to the invocation's catalog command. Like the
// create recorder it owns no top-level outcome and no once: one process takes
// the lock many times, and each acquisition over the threshold records itself.
// Appends are best-effort and never flow back into the Registry operation.
type RegistryLockRecorder struct {
	lifecycle  *LifecycleRecorder
	command    string
	subcommand string
}

// RegistryLock binds the recorder to the invocation's command class. Only the
// catalog Command and Subcommand of class are kept; an unclassified invocation
// records neither.
func (r *LifecycleRecorder) RegistryLock(class CommandClass) *RegistryLockRecorder {
	if r == nil {
		return nil
	}
	return &RegistryLockRecorder{lifecycle: r, command: class.Command, subcommand: class.Subcommand}
}

// Record appends one acquisition when it reached the threshold: a wait or a
// hold of at least RegistryLockRecordThreshold, or a timeout, which is always
// recorded because it is the failure the threshold exists to explain. An
// unknown operation or result drops the record. Negative durations are clamped
// to zero.
func (r *RegistryLockRecorder) Record(acquisition RegistryLockAcquisition) {
	if r == nil || r.lifecycle == nil {
		return
	}
	wait := max(acquisition.Wait, 0)
	var held *time.Duration
	if acquisition.Held != nil {
		clamped := max(*acquisition.Held, 0)
		held = &clamped
	}
	if wait < RegistryLockRecordThreshold && (held == nil || *held < RegistryLockRecordThreshold) &&
		acquisition.Result != RegistryLockTimeout {
		return
	}
	owner := r.lifecycle
	waitMS := wait.Milliseconds()
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "registry",
		Event: registryLockAcquisitionEvent, Result: "success", DurationMS: waitMS,
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Command: r.command, Subcommand: r.subcommand, Operation: string(acquisition.Operation),
		WaitMS: &waitMS,
	}
	if held != nil {
		heldMS := held.Milliseconds()
		event.LockHeldMS = &heldMS
		event.DurationMS += heldMS
	}
	switch acquisition.Result {
	case RegistryLockSuccess:
		if held != nil && *held >= RegistryLockWarnThreshold {
			event.Level = "warn"
		}
	case RegistryLockMutationFailed:
		event.Level, event.Result, event.Kind, event.Code = "error", "error", "runtime", string(codeRegistryMutationFailed)
	case RegistryLockTimeout:
		event.Level, event.Result, event.Kind, event.Code = "error", "error", "runtime", string(codeRegistryLockTimeout)
	case RegistryLockAcquireFailed:
		event.Level, event.Result, event.Kind, event.Code = "error", "error", "runtime", string(codeRegistryLockAcquireFailed)
	default:
		return
	}
	if validateRegistryLockEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

// validateRegistryLockEvent admits exactly the shape Record writes. The
// command pair is checked against the catalog by sanitizeEvent like every
// other family's.
func validateRegistryLockEvent(event Event) error {
	if event.Component != "registry" || event.Message != "" || event.Source != "" || event.hasCounts() ||
		event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() || event.hasTeardownFields() ||
		event.AgentUID != "" || event.hasCreatePhaseFields() {
		return fmt.Errorf("invalid registry lock shape")
	}
	if !validRegistryLockOperation(RegistryLockOperation(event.Operation)) {
		return fmt.Errorf("invalid registry lock operation")
	}
	if event.WaitMS == nil || *event.WaitMS < 0 {
		return fmt.Errorf("invalid registry lock wait")
	}
	var held int64
	if event.LockHeldMS != nil {
		held = *event.LockHeldMS
		if held < 0 {
			return fmt.Errorf("invalid registry lock hold")
		}
	}
	if event.DurationMS != *event.WaitMS+held {
		return fmt.Errorf("invalid registry lock duration")
	}
	leaseHeld := event.LockHeldMS != nil
	switch event.Result {
	case "success":
		want := "info"
		if held >= RegistryLockWarnThreshold.Milliseconds() {
			want = "warn"
		}
		if !leaseHeld || event.Level != want || event.Kind != "" || event.Code != "" {
			return fmt.Errorf("invalid registry lock success shape")
		}
	case "error":
		if event.Level != "error" || event.Kind != "runtime" {
			return fmt.Errorf("invalid registry lock error shape")
		}
		switch Code(event.Code) {
		case codeRegistryMutationFailed:
			if !leaseHeld {
				return fmt.Errorf("registry mutation failure without a lock hold")
			}
		case codeRegistryLockTimeout, codeRegistryLockAcquireFailed:
			if leaseHeld {
				return fmt.Errorf("registry lock acquisition failure with a lock hold")
			}
		default:
			return fmt.Errorf("invalid registry lock code")
		}
	default:
		return fmt.Errorf("invalid registry lock result")
	}
	return nil
}
