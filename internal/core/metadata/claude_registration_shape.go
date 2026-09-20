package metadata

// ClaudeRegistrationUnavailableReason is the byte-exact route reason every
// missing Claude registration produces. It is exported so the refusal and
// capability projections can recognize the one reason worth explaining further
// without re-spelling it, and so the string itself stays a single definition.
const ClaudeRegistrationUnavailableReason = "Claude registration lease is unavailable"

// ClaudeRegistrationShape names *why* an activation has no usable registration
// lease. The three failing shapes have three different causes and three
// different next actions, and until this type existed the product reported all
// of them with one sentence:
//
//   - ClaudeRegistrationNeverStarted: the session never registered. Nothing
//     cleared the lease, because nothing ever created one.
//   - ClaudeRegistrationLost: the session registered and then lost the lease
//     while its process kept running.
//   - ClaudeRegistrationSessionGone: the process that held the lease is gone,
//     so no hook can run to re-register it.
type ClaudeRegistrationShape string

const (
	// ClaudeRegistrationReady is a usable lease. It is also what the classifier
	// reports for a route that failed for some reason other than registration,
	// which is why callers must treat it as "nothing to explain here".
	ClaudeRegistrationReady ClaudeRegistrationShape = "ready"
	// ClaudeRegistrationNeverStarted is an activation whose provider session
	// never ran the registration hook: no session id, no generation, no lease.
	ClaudeRegistrationNeverStarted ClaudeRegistrationShape = "never-registered"
	// ClaudeRegistrationLost is an activation that admitted a registration and
	// no longer has one while its process is still alive. The session id and
	// generation that ClearClaudeRegistration deliberately leaves behind are
	// what separate it from an activation that never registered at all.
	ClaudeRegistrationLost ClaudeRegistrationShape = "registration-lost"
	// ClaudeRegistrationSessionGone is an activation whose bound provider
	// process is no longer on the host, or which carries a termination receipt.
	ClaudeRegistrationSessionGone ClaudeRegistrationShape = "session-gone"
)

// ClassifyClaudeRegistration reports why pane has no usable Claude registration.
//
// It is deliberately pure: every field it reads is one a caller already loaded
// to resolve the route, and the single piece of host truth it needs -- whether
// the bound provider process is still alive -- is passed in rather than probed
// here, so the whole table is unit-testable without a process or a tmux server.
//
// claudeProcessAlive must describe the *exact* process in the binding, not just
// its pid: a recycled pid that reads as alive would turn a gone session into a
// live one and send the operator after a session that no longer exists.
//
// Agent.Phase is deliberately not an input. A Pane can terminate without the
// Agent's phase converging, so a phase-based rule reports a dead session as a
// live one; process liveness is the observation that does not drift.
func ClassifyClaudeRegistration(pane Pane, claudeProcessAlive bool) ClaudeRegistrationShape {
	binding := pane.Status.Activation.Claude
	if binding != nil && binding.Registration != nil && binding.Registration.Ready {
		return ClaudeRegistrationReady
	}
	// A termination receipt is durable evidence that the generation that could
	// have registered is over, so it outranks liveness: it stays true even when
	// a pid has since been handed to some unrelated process.
	if pane.Status.LastTermination != nil {
		return ClaudeRegistrationSessionGone
	}
	if binding == nil {
		// No provider process was ever bound to this activation, so no hook can
		// have registered one. The operator's next action is the same as for a
		// session that started without the hook installed.
		return ClaudeRegistrationNeverStarted
	}
	if !claudeProcessAlive {
		return ClaudeRegistrationSessionGone
	}
	if binding.RegistrationSessionID != "" || binding.RegistrationGeneration != "" {
		return ClaudeRegistrationLost
	}
	return ClaudeRegistrationNeverStarted
}

// Diagnosis is the provider-neutral "why" of one shape. It names no command, so
// the CLI layer can pair it with next actions spelled in its own vocabulary.
func (s ClaudeRegistrationShape) Diagnosis() string {
	switch s {
	case ClaudeRegistrationNeverStarted:
		return "its Claude session never registered, so no lease was ever created for this activation"
	case ClaudeRegistrationLost:
		return "its Claude session registered and then lost the lease while the provider process kept running"
	case ClaudeRegistrationSessionGone:
		return "the Claude process bound to this activation is gone, so no hook can run to register it"
	default:
		return ""
	}
}
