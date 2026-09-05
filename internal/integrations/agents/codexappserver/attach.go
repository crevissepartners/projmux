package codexappserver

import (
	"context"
	"fmt"
	"time"
)

// EndpointAttach is the endpoint-connection axis. It answers only "may this
// process open an initialized connection to the endpoint that is already
// there", and deliberately says nothing about who may change that endpoint's
// process lifecycle.
type EndpointAttach string

const (
	EndpointAttachUnknown EndpointAttach = "unknown"
	EndpointAttachAllowed EndpointAttach = "allowed"
	EndpointAttachRefused EndpointAttach = "refused"
)

// AttachRefusal is the closed, content-free reason one endpoint may not be
// attached. It never carries a socket path, a pid, or process output.
type AttachRefusal string

const (
	AttachRefusalNone                  AttachRefusal = "none"
	AttachRefusalEndpointNotReady      AttachRefusal = "endpoint-not-ready"
	AttachRefusalProtocolMismatch      AttachRefusal = "protocol-mismatch"
	AttachRefusalVersionSkew           AttachRefusal = "version-skew"
	AttachRefusalRuntimeVersionUnknown AttachRefusal = "runtime-version-unknown"
	AttachRefusalOwnershipUnknown      AttachRefusal = "ownership-unknown"
	AttachRefusalConnectFailed         AttachRefusal = "connect-failed"
)

// DaemonLifecycleAuthority is the separate process-ownership axis. It is the
// only field that may authorize a daemon start, and it is never widened by a
// successful attach: a shared endpoint this process is allowed to talk to is
// still an endpoint this process must not start, stop, restart, or kill.
type DaemonLifecycleAuthority string

const (
	// DaemonLifecycleAuthorityNone forbids every daemon lifecycle mutation.
	DaemonLifecycleAuthorityNone DaemonLifecycleAuthority = "none"
	// DaemonLifecycleAuthorityColdStart allows only the official idempotent
	// start of an endpoint that is provably not running.
	DaemonLifecycleAuthorityColdStart DaemonLifecycleAuthority = "cold-start"
	// DaemonLifecycleAuthorityManaged marks an endpoint the official daemon
	// manager owns on this machine.
	DaemonLifecycleAuthorityManaged DaemonLifecycleAuthority = "managed"
)

// EndpointAuthority is the pure split of one readiness observation into the
// two independent authorities C-1 requires. It is derived on demand and is
// never projected into Doctor, Settings, or support diagnostics, so separating
// the axes changes no rendered surface.
type EndpointAuthority struct {
	Attach    EndpointAttach
	Refusal   AttachRefusal
	Lifecycle DaemonLifecycleAuthority
}

// AuthorityFor derives endpoint attach authority and daemon lifecycle
// authority from one readiness observation.
//
// Attach is allowed for a ready, protocol-compatible, exact-current endpoint
// whether the official daemon manager owns it or not, because reading and
// writing an already-running endpoint is not process ownership. It is refused
// for a protocol mismatch, for a version skew, for an unknown running version,
// and for unknown manager ownership, because none of those prove the endpoint
// is the exact current one.
//
// Lifecycle authority is granted only for an endpoint the official manager
// owns, plus the existing cold-start contract where there is no running shared
// endpoint to interrupt. Every other endpoint, including a ready unmanaged one
// this process may attach to, keeps DaemonLifecycleAuthorityNone.
func AuthorityFor(health Health) EndpointAuthority {
	authority := EndpointAuthority{Attach: EndpointAttachRefused, Lifecycle: DaemonLifecycleAuthorityNone}
	switch health.EndpointReadiness {
	case EndpointReady:
	case EndpointDead:
		// No running shared endpoint exists, so the exact cold-start contract
		// keeps its lifecycle authority while there is nothing to attach to.
		authority.Refusal = AttachRefusalEndpointNotReady
		authority.Lifecycle = DaemonLifecycleAuthorityColdStart
		return authority
	case EndpointUnsupported, EndpointProtocolError:
		authority.Refusal = AttachRefusalProtocolMismatch
		return authority
	default:
		authority.Refusal = AttachRefusalEndpointNotReady
		return authority
	}

	if health.ManagerOwnership == ManagerManaged {
		authority.Lifecycle = DaemonLifecycleAuthorityManaged
	}
	switch health.VersionRelation {
	case VersionSkew:
		authority.Refusal = AttachRefusalVersionSkew
		return authority
	case VersionUnknown:
		authority.Refusal = AttachRefusalRuntimeVersionUnknown
		return authority
	}
	if health.ManagerOwnership == ManagerUnknown {
		authority.Refusal = AttachRefusalOwnershipUnknown
		return authority
	}
	authority.Attach = EndpointAttachAllowed
	authority.Refusal = AttachRefusalNone
	return authority
}

// AttachOptions bounds one attach attempt. It carries no endpoint identity:
// the effective endpoint is the official default control socket.
type AttachOptions struct {
	// Timeout bounds the dial and initialize handshake.
	Timeout time.Duration
	// ExperimentalAPI negotiates the upstream experimental API capability
	// during initialize. Additional writable roots require it.
	ExperimentalAPI bool
}

// AttachError is the typed, content-free refusal of one attach attempt.
type AttachError struct {
	Refusal   AttachRefusal
	Authority EndpointAuthority
	err       error
}

func (e *AttachError) Error() string {
	return fmt.Sprintf("codex app-server attach refused: %s", e.Refusal)
}

func (e *AttachError) Unwrap() error { return e.err }

// AttachDefaultEndpoint opens one initialized connection to the default
// endpoint when, and only when, endpoint attach authority allows it. It runs a
// read-only probe first and then dials through the official stdio proxy, so it
// never starts, stops, restarts, kills, configures, or logs in to the daemon.
// An exact-current endpoint the official manager does not own is therefore
// attachable with a daemon lifecycle argv count of zero.
func AttachDefaultEndpoint(ctx context.Context, projmuxVersion string, opts AttachOptions) (*Client, Health, error) {
	policy := attachPolicy{
		probe: func(probeCtx context.Context) Health {
			return ProbeDefaultProxy(probeCtx, attachTimeout(opts.Timeout), projmuxVersion, true)
		},
		open: func(openCtx context.Context, experimental bool) (*Client, error) {
			return openDefaultProxy(openCtx, attachTimeout(opts.Timeout), projmuxVersion, experimental)
		},
	}
	return policy.attach(ctx, opts)
}

// AttachDefaultUnixAt applies the existing default readiness/version/
// ownership gate, then opens the already-resolved control socket directly so
// the broker can retain a kernel peer-birth witness. The path is fixed by the
// route factory and is not treated as identity.
func AttachDefaultUnixAt(ctx context.Context, socketPath, projmuxVersion string, opts AttachOptions) (*Client, Health, error) {
	policy := attachPolicy{
		probe: func(probeCtx context.Context) Health {
			return ProbeDefaultProxy(probeCtx, attachTimeout(opts.Timeout), projmuxVersion, true)
		},
		open: func(openCtx context.Context, experimental bool) (*Client, error) {
			return OpenPrivateUnix(openCtx, socketPath, attachTimeout(opts.Timeout), projmuxVersion, experimental)
		},
	}
	return policy.attach(ctx, opts)
}

type attachPolicy struct {
	probe func(context.Context) Health
	open  func(context.Context, bool) (*Client, error)
}

func (p attachPolicy) attach(ctx context.Context, opts AttachOptions) (*Client, Health, error) {
	if err := ctx.Err(); err != nil {
		return nil, Health{}, err
	}
	health := p.probe(ctx)
	authority := AuthorityFor(health)
	if authority.Attach != EndpointAttachAllowed {
		return nil, health, &AttachError{Refusal: authority.Refusal, Authority: authority}
	}
	client, err := p.open(ctx, opts.ExperimentalAPI)
	if err != nil {
		return nil, health, &AttachError{Refusal: AttachRefusalConnectFailed, Authority: authority, err: err}
	}
	return client, health, nil
}

func attachTimeout(timeout time.Duration) time.Duration {
	return positiveDuration(timeout, DefaultProbeTimeout)
}
