package codexbroker

import (
	"encoding/base64"
	"errors"
	"strings"
)

// EndpointKey is the durable identity of the one endpoint a Broker
// multiplexes. It is a closed token rather than a socket path, a pid, or a
// working directory, so an endpoint can be named in durable state and in
// diagnostics without carrying any machine-local location.
type EndpointKey string

// DefaultEndpointKey names the official default Codex app-server control
// endpoint. It remains unmanaged and attach-only. Private generation keys are
// accepted only with an explicit generation-scoped opener; this token never
// silently adopts the installed/default endpoint into that pool.
const DefaultEndpointKey EndpointKey = "codex-app-server:default"

const generationEndpointPrefix = "codex-app-server:g1:"

// EndpointIdentity is the durable, path-free identity of one private Codex
// app-server generation. Neither field is a socket path or a mutable version
// pointer: the state domain and endpoint generation are the two namespaces a
// routed thread must match before a provider write is possible.
type EndpointIdentity struct {
	StateDomainID        string `json:"stateDomainID"`
	EndpointGenerationID string `json:"endpointGenerationID"`
}

// NewEndpointKey constructs the canonical key for one private generation.
// The URL-safe encoding is length-delimited by its separator and cannot
// collide even when either opaque identity contains punctuation.
func NewEndpointKey(stateDomainID, endpointGenerationID string) (EndpointKey, error) {
	identity := EndpointIdentity{StateDomainID: stateDomainID, EndpointGenerationID: endpointGenerationID}
	if !identity.Valid() {
		return "", refuse(RefusalEndpointIdentityInvalid, nil)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return EndpointKey(generationEndpointPrefix + encode([]byte(stateDomainID)) + ":" + encode([]byte(endpointGenerationID))), nil
}

// Identity decodes a private generation key. The official default endpoint is
// deliberately not assigned a generation identity: it stays unmanaged and
// attach-only rather than being silently adopted into the managed pool.
func (key EndpointKey) Identity() (EndpointIdentity, bool) {
	value := string(key)
	if !strings.HasPrefix(value, generationEndpointPrefix) {
		return EndpointIdentity{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, generationEndpointPrefix), ":")
	if len(parts) != 2 {
		return EndpointIdentity{}, false
	}
	decode := base64.RawURLEncoding.DecodeString
	domain, domainErr := decode(parts[0])
	generation, generationErr := decode(parts[1])
	identity := EndpointIdentity{StateDomainID: string(domain), EndpointGenerationID: string(generation)}
	if domainErr != nil || generationErr != nil || !identity.Valid() {
		return EndpointIdentity{}, false
	}
	canonical, err := NewEndpointKey(identity.StateDomainID, identity.EndpointGenerationID)
	return identity, err == nil && canonical == key
}

// Valid reports whether both opaque endpoint identity components are bounded,
// non-empty, whitespace-free values. They are intentionally not interpreted
// as paths or version numbers.
func (identity EndpointIdentity) Valid() bool {
	return validEndpointIdentityPart(identity.StateDomainID) && validEndpointIdentityPart(identity.EndpointGenerationID)
}

func validEndpointIdentityPart(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validEndpointKey(key EndpointKey) bool {
	if key == DefaultEndpointKey {
		return true
	}
	_, ok := key.Identity()
	return ok
}

// Supported reports whether this build can host or reach a broker runtime.
//
// It is the one decision a product path makes before choosing the broker as
// its endpoint producer, because the runtime's singleton, credential, and
// stale-artifact contracts are stated in Unix socket and filesystem-ownership
// terms and this package accepts no weaker substitute.
func Supported() bool { return platformSupported }

// ConnectionEpoch counts the connections one Broker has opened to its
// endpoint. It only ever increases and a value is never reused, so a message
// or a mutation that carries an older epoch is provably from a connection the
// Broker has already revoked.
type ConnectionEpoch uint64

// BindingEpoch counts the bindings one Broker has created. It is independent
// of ConnectionEpoch because a binding survives a reconnect: the connection
// axis answers "is this the current wire", the binding axis answers "is this
// the current thread attachment".
type BindingEpoch uint64

// Fence is the two-axis authority token. Both axes must match the Broker's
// current epochs for a delivery or a mutation to be allowed. Fencing on the
// pair is what keeps a revived old connection from writing over the state a
// newer one already owns, and what keeps a re-created binding from inheriting
// the authority of the binding it replaced.
type Fence struct {
	Connection ConnectionEpoch
	Binding    BindingEpoch
}

// Refusal is the closed, content-free reason one broker operation was not
// performed. It never carries a socket path, a pid, a thread body, a prompt,
// or provider output, so every refusal is safe to log, persist, and render.
type Refusal string

const (
	// RefusalNone marks the absence of a refusal.
	RefusalNone Refusal = "none"
	// RefusalBrokerClosed marks work asked of a Broker that is shutting down.
	RefusalBrokerClosed Refusal = "broker-closed"
	// RefusalEndpointUnknown marks an endpoint key this phase cannot reach.
	RefusalEndpointUnknown Refusal = "endpoint-unknown"
	// RefusalEndpointIdentityInvalid marks a state-domain/generation key whose
	// opaque components are absent or malformed.
	RefusalEndpointIdentityInvalid Refusal = "endpoint-identity-invalid"
	// RefusalRouteMismatch marks a binding or control authority presented to a
	// different state domain, endpoint generation, or exact thread.
	RefusalRouteMismatch Refusal = "route-mismatch"
	// RefusalBrokerRuntimeStale marks authority granted by a broker process
	// that has since been replaced, even if its local epochs repeat.
	RefusalBrokerRuntimeStale Refusal = "broker-runtime-stale"
	// RefusalAdmissionClosed marks a fresh create admission request for a dark
	// Preparing generation. Readiness alone never opens admission.
	RefusalAdmissionClosed Refusal = "admission-closed"
	// RefusalBindingRestoreFailed marks a broker restart that could not restore
	// every exact endpoint/thread binding in its durable generation ledger.
	RefusalBindingRestoreFailed Refusal = "binding-restore-failed"
	// RefusalBrokerRestarting marks a bind or second restart that raced the
	// exact generation's restore fence. Sibling generations remain available.
	RefusalBrokerRestarting Refusal = "broker-restarting"
	// RefusalGenerationCapacityExceeded preserves the Phase 0 bounded-pool
	// invariant at the runtime construction boundary.
	RefusalGenerationCapacityExceeded Refusal = "generation-capacity-exceeded"
	// RefusalThreadRequired marks a bind attempt with no exact thread id.
	// Guessing one from a working directory or from the newest thread is the
	// exact "first match" binding this package refuses to invent.
	RefusalThreadRequired Refusal = "thread-required"
	// RefusalBindingExists marks a second bind of a thread that is already
	// bound. Two bindings for one thread would make delivery ambiguous.
	RefusalBindingExists Refusal = "binding-exists"
	// RefusalBindingClosed marks work asked of a binding the caller unbound.
	RefusalBindingClosed Refusal = "binding-closed"
	// RefusalControlNotOpen marks a mutation attempted before the current
	// connection's snapshot barrier closed for this binding.
	RefusalControlNotOpen Refusal = "control-not-open"
	// RefusalStaleConnectionEpoch marks a fence from a revoked connection.
	RefusalStaleConnectionEpoch Refusal = "stale-connection-epoch"
	// RefusalStaleBindingEpoch marks a fence from a superseded binding.
	RefusalStaleBindingEpoch Refusal = "stale-binding-epoch"
	// RefusalResyncRequired marks a binding whose bounded delivery queue
	// overflowed. Its stream has a hole, so the binding is revoked and the
	// caller must bind again rather than consume a silently gapped order.
	RefusalResyncRequired Refusal = "resync-required"
	// RefusalSnapshotUnavailable marks a binding whose reconnect snapshot was
	// refused by a live endpoint, which is a thread-scoped fault rather than a
	// connection-scoped one.
	RefusalSnapshotUnavailable Refusal = "snapshot-unavailable"
	// RefusalLeaseIdentityMismatch marks an approval answer whose raw request
	// id or thread id is not the one the lease was minted for.
	RefusalLeaseIdentityMismatch Refusal = "lease-identity-mismatch"
	// RefusalResponseAlreadyAnswered marks a second answer to one inbound
	// server request on the same connection.
	RefusalResponseAlreadyAnswered Refusal = "response-already-answered"
	// RefusalDisconnectBoundary marks a mutation that terminated at a
	// disconnect. Its result is indeterminate and it is never resent.
	RefusalDisconnectBoundary Refusal = "disconnect-boundary"

	// The codes below belong to the runtime host, its discovery, and its
	// authenticated local IPC. They stay in this one closed set because a
	// caller switches on Refusal without caring which layer produced it.

	// RefusalDomainRequired marks a discovery contract built without an
	// absolute state domain to scope the runtime singleton to.
	RefusalDomainRequired Refusal = "domain-required"
	// RefusalSocketPathTooLong marks a state domain whose derived socket path
	// exceeds the platform-safe bound. It is reported when the contract is
	// built, so nothing is ever created under a path that cannot be bound.
	RefusalSocketPathTooLong Refusal = "socket-path-too-long"
	// RefusalDiscoveryUntrusted marks a discovery directory, record, socket, or
	// lock that is not an owner-private object of the expected kind. Such an
	// artifact is left untouched rather than repaired: repairing it would be
	// indistinguishable from taking over something this process does not own.
	RefusalDiscoveryUntrusted Refusal = "discovery-untrusted"
	// RefusalUnsupportedPlatform marks a build whose filesystem and socket
	// semantics cannot carry the runtime's ownership contract.
	RefusalUnsupportedPlatform Refusal = "unsupported-platform"
	// RefusalHostUnavailable marks a discovery that found no runtime to reach.
	RefusalHostUnavailable Refusal = "host-unavailable"
	// RefusalHostLive marks a reclaim attempt against an artifact that still
	// answers. A live runtime is never replaced; it is reused.
	RefusalHostLive Refusal = "host-live"
	// RefusalRuntimeExists marks a host start whose socket is already bound.
	RefusalRuntimeExists Refusal = "runtime-exists"
	// RefusalRuntimeReplaced marks work presented to a runtime other than the
	// one that granted the authority being presented.
	RefusalRuntimeReplaced Refusal = "runtime-replaced"
	// RefusalHostClosed marks work asked of a runtime that is shutting down.
	RefusalHostClosed Refusal = "host-closed"
	// RefusalCredentialRejected marks a client whose local credential does not
	// match the one the running host published.
	// #nosec G101 -- this is a closed refusal code, not a credential value.
	RefusalCredentialRejected Refusal = "credential-rejected"
	// RefusalEndpointMismatch marks a client asking a host for an endpoint that
	// host does not serve.
	RefusalEndpointMismatch Refusal = "endpoint-mismatch"
	// RefusalProtocolIncompatible marks a negotiated protocol version outside
	// the range the receiving side accepts.
	RefusalProtocolIncompatible Refusal = "protocol-incompatible"
	// RefusalDrainRequired marks an incompatible client that arrived at a live
	// runtime. The running bindings are not severed and the runtime is not
	// forcibly replaced; the incompatible caller waits for the last binding to
	// drain, after which a new runtime starts under exact owner proof.
	RefusalDrainRequired Refusal = "drain-required"
	// RefusalFrameInvalid marks an IPC frame that was oversized, malformed, or
	// not the frame the protocol expected at that point.
	RefusalFrameInvalid Refusal = "frame-invalid"
	// RefusalRequestUnknown marks an IPC request kind this protocol version
	// does not implement.
	RefusalRequestUnknown Refusal = "request-unknown"
	// RefusalEndpointRefused marks a mutation the upstream endpoint answered
	// with an error. The error body is provider content, so it stops at the
	// runtime and the client is told the classification instead.
	RefusalEndpointRefused Refusal = "endpoint-refused"
)

// BrokerError is the typed, content-free refusal of one broker operation. Its
// rendered text is the closed code alone; a wrapped transport cause stays
// reachable through errors.As/Unwrap but is never part of the message.
type BrokerError struct {
	Refusal Refusal
	err     error
}

func (e *BrokerError) Error() string { return "codex broker refused: " + string(e.Refusal) }

func (e *BrokerError) Unwrap() error { return e.err }

// refuse builds one typed refusal. cause may be nil.
func refuse(reason Refusal, cause error) error {
	return &BrokerError{Refusal: reason, err: cause}
}

// RefusalOf returns the closed refusal code carried by err, or RefusalNone for
// a nil error and for any error this package did not classify. Callers switch
// on the code instead of matching error text.
func RefusalOf(err error) Refusal {
	var refusal *BrokerError
	if errors.As(err, &refusal) {
		return refusal.Refusal
	}
	return RefusalNone
}
