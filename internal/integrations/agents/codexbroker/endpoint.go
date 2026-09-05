package codexbroker

import (
	"context"
	"encoding/json"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// Endpoint is the exact slice of one initialized app-server connection this
// broker needs. It is an interface for one reason only: the broker must be
// driveable by an in-memory endpoint in tests without a process, a socket, or
// a wire. *codexappserver.Client satisfies it as written, so the production
// path adds no adapter and reimplements no JSON-RPC.
//
// A closed Notifications channel is the single disconnect signal. There is no
// separate liveness probe, because a broker that inferred liveness from
// anything other than the stream it is already reading would have two answers
// to one question.
type Endpoint interface {
	Notifications() <-chan codexappserver.Notification
	Request(ctx context.Context, method string, params, result any) error
	RespondServerRequest(ctx context.Context, rawID json.RawMessage, result any) error
	BootstrapThread(ctx context.Context, threadID, cwd string, roots []string) (codexappserver.ThreadSnapshot, error)
	Close() error
}

// witnessedEndpoint is implemented only by direct Unix app-server clients.
// Stdio proxy children intentionally cannot satisfy it: their process identity
// is not the upstream peer identity the fixed route needs to prove.
type witnessedEndpoint interface {
	PeerIdentity() codexappserver.PeerIdentity
}

// LifecycleOpener opens one request-owned connection to the exact fixed route
// and proves it reaches expected before any provider result is trusted.
type LifecycleOpener func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error)

// Opener opens exactly one endpoint connection. It is injected so the broker
// never names a transport, and so a test can fail, delay, or replace a
// connection without an OS process.
//
// An Opener must not start, stop, restart, or kill an upstream daemon. The
// production default below satisfies that by construction: the Phase 0 attach
// seam probes read-only and dials an endpoint that is already there.
type Opener func(ctx context.Context) (Endpoint, error)

// DefaultOpener is the production Opener. It attaches to the official default
// endpoint through the Phase 0 attach seam, which refuses before dialing
// unless endpoint attach authority allows it and which never widens daemon
// lifecycle authority.
//
// The returned Health is intentionally dropped: the broker's decisions are
// driven by epochs and by the stream, and retaining a readiness observation
// here would create a second, staler source of truth for the same question.
func DefaultOpener(projmuxVersion string, opts codexappserver.AttachOptions) Opener {
	return func(ctx context.Context) (Endpoint, error) {
		client, _, err := codexappserver.AttachDefaultEndpoint(ctx, projmuxVersion, opts)
		if err != nil {
			return nil, err
		}
		return client, nil
	}
}
