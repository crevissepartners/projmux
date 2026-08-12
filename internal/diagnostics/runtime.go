package diagnostics

// RuntimeState is a stable, read-only health projection for future Doctor
// consumers. It is derived from safe event enums, never from raw messages.
type RuntimeState string

const (
	RuntimeUnknown     RuntimeState = "unknown"
	RuntimeHealthy     RuntimeState = "healthy"
	RuntimeUnreachable RuntimeState = "unreachable"
	RuntimeError       RuntimeState = "error"
	RuntimeSkipped     RuntimeState = "skipped"
)

const recentFailureLimit = 20

// RuntimeHealth is the typed operational seam owned by diagnostics. Doctor may
// consume it in its later phase without writing, repairing, probing, or
// decoding the JSONL schema itself.
type RuntimeHealth struct {
	MuxBackend         string
	Socket             RuntimeState
	Apply              RuntimeState
	RecentFailureCodes []Code
	// RecentErrorCount is the size of the newest bounded window, never a
	// lifetime total. RecentErrorsBounded is true when older errors were
	// omitted from that window.
	RecentErrorCount    int
	RecentErrorsBounded bool
	Missing             bool
	Malformed           int
	Truncated           bool
}

// ReadOnlyStore is the strict no-write event source used by runtime consumers.
type ReadOnlyStore interface {
	ReadOnly() (ReadResult, error)
}

// ReadRuntimeHealth reads and projects the bounded journal without creating,
// chmodding, locking, truncating, probing, or repairing anything.
func ReadRuntimeHealth(store ReadOnlyStore) (RuntimeHealth, error) {
	health := RuntimeHealth{MuxBackend: MuxBackend(), Socket: RuntimeUnknown, Apply: RuntimeUnknown}
	if store == nil {
		return health, nil
	}
	result, err := store.ReadOnly()
	if err != nil {
		return health, err
	}
	health.Missing = result.Missing
	health.Malformed = result.Malformed
	health.Truncated = result.Truncated

	recentErrors := make([]Event, 0, recentFailureLimit)
	for _, event := range result.Events {
		if event.Level == "error" || event.Result == "error" {
			recentErrors = append(recentErrors, event)
			if len(recentErrors) > recentFailureLimit {
				recentErrors = recentErrors[len(recentErrors)-recentFailureLimit:]
				health.RecentErrorsBounded = true
			}
		}
		if event.Event != "lifecycle.outcome" {
			continue
		}
		switch Operation(event.Operation) {
		case OperationSessionCreate, OperationSessionAttach, OperationSessionSwitch, OperationSessionKill:
			if event.Result == "success" {
				health.Socket = RuntimeHealthy
			} else {
				health.Socket = RuntimeError
			}
		case OperationTmuxApply:
			switch Code(event.Code) {
			case CodeTmuxApplySocketUnreachable:
				health.Socket = RuntimeUnreachable
				health.Apply = RuntimeError
			case CodeTmuxApplyReloadFailed:
				health.Socket = RuntimeHealthy
				health.Apply = RuntimeError
			case CodeTmuxApplyReloadSkipped:
				health.Apply = RuntimeSkipped
			default:
				if event.Result == "success" {
					health.Socket = RuntimeHealthy
					health.Apply = RuntimeHealthy
				} else {
					health.Apply = RuntimeError
				}
			}
		}
	}
	health.RecentErrorCount = len(recentErrors)
	for _, event := range recentErrors {
		if event.Code == "" {
			continue
		}
		health.RecentFailureCodes = append(health.RecentFailureCodes, Code(event.Code))
	}
	return health, nil
}
