package codexbroker

// MutationOutcome is the closed terminal classification of one control
// request. There is no fourth state on purpose: a mutation whose result cannot
// be proven either way must be surfaced as unknown rather than optimistically
// folded into applied or refused.
type MutationOutcome string

const (
	// MutationApplied means the endpoint answered the request.
	MutationApplied MutationOutcome = "applied"
	// MutationRefused means the endpoint answered with an error, or the broker
	// refused the request before it reached the wire. Either way it did not
	// take effect.
	MutationRefused MutationOutcome = "refused"
	// MutationIndeterminate means the request left the broker but its result
	// was lost at a disconnect boundary. It is never resent automatically:
	// re-sending a turn or an interrupt whose first attempt may have landed is
	// how a provider ends up with a duplicate.
	MutationIndeterminate MutationOutcome = "indeterminate"
)

// Mutation is one control request for a bound thread. Params and Result are
// opaque pass-through: the broker forwards them, decides nothing about them,
// and retains neither.
type Mutation struct {
	Method string
	Params any
	Result any
}

// WriteRecord is one mutation's durable, content-free trace. It keeps the
// fence it was authorized under, the bounded protocol discriminator, the
// terminal classification, and the attempt count. It deliberately keeps no
// payload, so the ledger can be persisted and rendered as-is.
//
// Attempts is always 1. It is recorded rather than assumed so the no-replay
// contract is observable instead of merely documented.
type WriteRecord struct {
	Fence    Fence
	Method   string
	Outcome  MutationOutcome
	Attempts int
}

// Diagnostics is the content-free telemetry projection of one broker. Every
// field is a closed token or a counter, so it is safe to log, persist, and
// render without redaction.
type Diagnostics struct {
	Endpoint         EndpointKey
	ConnectionEpoch  ConnectionEpoch
	OpenAttempts     int
	Connects         int
	Disconnects      int
	Bindings         int
	ReleasedBindings int
	RevokedBindings  int
	BufferedEvents   int
	DeliveredEvents  int
	ThreadlessEvents int
	UnboundEvents    int
	StaleEvents      int
	Applied          int
	Refused          int
	Indeterminate    int
	// Resends counts mutations this broker retried on its own. It exists to
	// stay zero: no code path increments it, and the ledger's Attempts column
	// is the second, independent witness of the same contract.
	Resends int
}
