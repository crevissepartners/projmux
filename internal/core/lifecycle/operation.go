package lifecycle

// Operation is the closed set of state-changing runtime lifecycles. Adapters
// mark these operations; diagnostics owns their recording and failure codes.
type Operation string

const (
	OperationSessionCreate Operation = "session.create"
	OperationSessionAttach Operation = "session.attach"
	OperationSessionSwitch Operation = "session.switch"
	OperationSessionKill   Operation = "session.kill"
	OperationTmuxApply     Operation = "tmux.apply"
)
