package codexbroker

import "time"

const (
	// baseReconnectDelay is the first reconnect wait. It is short enough that
	// an endpoint that bounced is picked up promptly.
	baseReconnectDelay = 250 * time.Millisecond
	// maxReconnectDelay caps the exponential growth. The supervisor keeps
	// retrying at this cadence for as long as at least one binding exists, so
	// the cap replaces a retry-count exhaustion that would strand a binding.
	maxReconnectDelay = 5 * time.Second
	// backoffShiftLimit bounds the shift used to grow the delay so the
	// exponent can never overflow on a long outage.
	backoffShiftLimit = 20
)

// Clock is the injected time source the reconnect supervisor waits on. Only
// the supervisor's backoff needs time, so the interface stays at the two
// operations that make a long outage testable without a real one.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// systemClock is the production Clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// backoffDelay returns the capped exponential reconnect wait for attempt,
// spread by jitter. attempt is 1-based.
//
// Jitter covers the lower half of the window, so the result is always inside
// [delay/2, delay] and therefore inside [baseReconnectDelay/2,
// maxReconnectDelay]. Keeping a floor matters as much as the cap: a zero wait
// would turn a refusing endpoint into a hot loop, and an unbounded wait would
// strand a binding that is still waiting to be served.
func backoffDelay(attempt int, jitter float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, backoffShiftLimit)
	delay := baseReconnectDelay << shift
	if delay > maxReconnectDelay || delay <= 0 {
		delay = maxReconnectDelay
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	half := delay / 2
	return half + time.Duration(jitter*float64(delay-half))
}
