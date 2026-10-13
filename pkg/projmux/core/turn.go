package core

// TurnRequest requires one explicit Agent. Via is an explicit caller declaration
// required only for Claude interrupt. Binding and Generation optionally pin the
// observed process or tmux activation; no ambient actor or active Pane is used.
type TurnRequest struct {
	Ref, Project, Window string
	Text, Via            string
	Binding              ProcessBinding
	Generation           string
}

// TurnResult preserves receipts on post-effect failures. Accepted records an
// observed acceptance, not provider completion. OutcomeUnknown forbids replay
// without inspection. Steer acceptance=provider retains delivery=unconfirmed.
type TurnResult struct {
	AgentUID, PaneUID, Generation, ThreadID, TurnID, Operation string
	Mode, Acceptance, Delivery, RunningTurn, Origin            string
	Process                                                    bool
	Accepted, OutcomeUnknown                                   bool
}
