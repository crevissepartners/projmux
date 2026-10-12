package core

// StopProjectRequest addresses one Project by name, uid:<uid>, or absolute
// registered root. SocketPath overrides Options.SocketPath; empty uses the
// app-owned projmux server. No current Pane or client is inferred.
type StopProjectRequest struct {
	Ref        string
	SocketPath string
}

// ProjectStopInterruption identifies one exact activation carrying this stop's
// durable control-action/interrupted termination evidence.
type ProjectStopInterruption struct {
	PaneUID    string
	AgentUID   string
	Generation string
}

// StopProjectResult carries the CLI stop receipt with focus unchanged and the
// interruption evidence summary. On success, Interruptions contains only retained
// evidence. On failure, an OperationID and Interruptions
// describe attempted evidence; they may have been compensated or retained when
// runtime effects could not be proved. Stopped reports a successful operation,
// and false does not guarantee the runtime survived a failed kill.
type StopProjectResult struct {
	Receipt       OperationReceipt
	Interruptions []ProjectStopInterruption
	OperationID   string
	Stopped       bool
}
