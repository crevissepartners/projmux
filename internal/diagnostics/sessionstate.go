package diagnostics

// The `session-state.outcome` family was written by the removed Project
// snapshot commands. projmux no longer emits it, but existing logs still hold
// such records, so the closed vocabulary below stays readable: the validator
// accepts an old record and rejects any shape the removed writer could not have
// produced.
const (
	sessionStateOutcomeEvent = "session-state.outcome"

	OperationSessionStateSave     Operation = "session-state.save"
	OperationSessionStateAutosave Operation = "session-state.autosave"
	OperationSessionStateRestore  Operation = "session-state.restore"
	OperationSessionStateDelete   Operation = "session-state.delete"

	CodeSessionStateSaveFailed     Code = "session-state.save.failed"
	CodeSessionStateAutosaveFailed Code = "session-state.autosave.failed"
	CodeSessionStateRestoreFailed  Code = "session-state.restore.failed"
	CodeSessionStateDeleteFailed   Code = "session-state.delete.failed"
)

type SessionStateSource string

const (
	SessionStateSourceManual         SessionStateSource = "manual"
	SessionStateSourceSettingsLatest SessionStateSource = "settings-latest"
	SessionStateSourceSettingsNamed  SessionStateSource = "settings-named"
	SessionStateSourceAutosave       SessionStateSource = "autosave"
	SessionStateSourceStartupLatest  SessionStateSource = "startup-latest"
	SessionStateSourceStartupNamed   SessionStateSource = "startup-named"
	SessionStateSourcePrune          SessionStateSource = "prune"
)

func sessionStateOperation(operation Operation) bool {
	switch operation {
	case OperationSessionStateSave, OperationSessionStateAutosave, OperationSessionStateRestore, OperationSessionStateDelete:
		return true
	default:
		return false
	}
}

func sessionStateFailureCode(operation Operation) Code {
	switch operation {
	case OperationSessionStateSave:
		return CodeSessionStateSaveFailed
	case OperationSessionStateAutosave:
		return CodeSessionStateAutosaveFailed
	case OperationSessionStateRestore:
		return CodeSessionStateRestoreFailed
	case OperationSessionStateDelete:
		return CodeSessionStateDeleteFailed
	default:
		return ""
	}
}
