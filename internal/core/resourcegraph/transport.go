package resourcegraph

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// TransportKind is the closed set of tmux routing shapes.
//
// There are exactly three, and the third one matters: TransportNone is a legal
// state, not an error. A read invoked outside tmux with no socket flag has no
// server to observe, and saying so is the honest answer. The alternative --
// falling back to bare `tmux`, which resolves to the default socket -- would
// answer a question about one server with another server's objects.
type TransportKind string

const (
	// TransportNone carries no tmux server. Inventory taken through it is
	// empty and costs zero tmux calls.
	TransportNone TransportKind = "none"
	// TransportSocketName routes through `tmux -L <name>`.
	TransportSocketName TransportKind = "socket-name"
	// TransportSocketPath routes through `tmux -S <absolute path>`.
	TransportSocketPath TransportKind = "socket-path"
)

// TransportSource records where the routing came from. It is reported for
// diagnostics and is never an authority input: an inherited socket routes
// exactly like an explicitly named one.
type TransportSource string

const (
	TransportSourceNone         TransportSource = "none"
	TransportSourceSocketName   TransportSource = "explicit-socket-name"
	TransportSourceSocketPath   TransportSource = "explicit-socket-path"
	TransportSourceInheritedEnv TransportSource = "inherited-tmux-env"
)

// AppOwnedMarker is the exact value of the server-global @projmux_app option on
// a server projmux started. Any other value, including absent, is a server
// projmux is a guest on.
const AppOwnedMarker = "1"

// ControlSessionRole is the exact @projmux_session_role value that marks an
// app-owned control session -- the Home session that is deliberately not a
// Registry Project.
//
// This package only ever reads the option. The marker's writer and its
// lifecycle belong to the control-session track; an unknown or absent value is
// simply not a control session here, which is the same fail-closed reading a
// spoofed value on a non-app-owned server gets.
const ControlSessionRole = "control"

// EphemeralMarker is the exact value of the session-scoped @projmux_ephemeral
// option on an auto-attach scratch session.
const EphemeralMarker = "1"

// Transport is the exact tmux routing of one Inventory.
type Transport struct {
	Kind   TransportKind   `json:"kind"`
	Value  string          `json:"value,omitempty"`
	Source TransportSource `json:"source"`
}

// Present reports whether this transport can reach a tmux server at all.
func (t Transport) Present() bool {
	return t.Kind != TransportNone && t.Kind != "" && t.Value != ""
}

// Args returns the argv prefix that pins every tmux call to this exact server.
// It returns nil for an absent transport, and a caller that gets nil must not
// run tmux: an unprefixed call is a default-server probe.
func (t Transport) Args() []string {
	switch {
	case !t.Present():
		return nil
	case t.Kind == TransportSocketName:
		return []string{"-L", t.Value}
	case t.Kind == TransportSocketPath:
		return []string{"-S", t.Value}
	default:
		return nil
	}
}

// String renders the transport the way an operator would type it.
func (t Transport) String() string {
	if !t.Present() {
		return "no tmux transport"
	}
	return "tmux " + strings.Join(t.Args(), " ")
}

// TransportRequest is the raw routing input of one invocation: the two mutually
// exclusive socket flags plus the inherited $TMUX value.
type TransportRequest struct {
	// SocketName is an explicit `--socket` value (tmux -L).
	SocketName string
	// SocketPath is an explicit `--socket-path` value (tmux -S), absolute.
	SocketPath string
	// InheritedTMUX is the raw $TMUX environment value, whose first
	// comma-separated field is the absolute socket path of the server the
	// current client is attached to.
	InheritedTMUX string
}

// ErrTransportConflict marks a request carrying both socket flags.
var ErrTransportConflict = errors.New("resourcegraph: --socket and --socket-path are mutually exclusive")

// ResolveTransport turns a raw request into exactly one routing decision.
//
// Precedence is explicit flags first, inherited $TMUX second, nothing third.
// An inherited value is trusted only when it is an absolute path, because that
// is the only shape tmux writes and the only shape that names a server without
// a search: a relative or empty value is discarded rather than guessed at.
//
// A request with no transport at all is not an error. It resolves to
// TransportNone, and the caller renders a Registry-only graph.
func ResolveTransport(req TransportRequest) (Transport, error) {
	name := strings.TrimSpace(req.SocketName)
	path := strings.TrimSpace(req.SocketPath)
	if name != "" && path != "" {
		return Transport{}, ErrTransportConflict
	}
	if name != "" {
		return Transport{Kind: TransportSocketName, Value: name, Source: TransportSourceSocketName}, nil
	}
	if path != "" {
		if !filepath.IsAbs(path) {
			return Transport{}, fmt.Errorf("resourcegraph: --socket-path must be absolute, got %q", path)
		}
		return Transport{Kind: TransportSocketPath, Value: filepath.Clean(path), Source: TransportSourceSocketPath}, nil
	}
	inherited, _, _ := strings.Cut(strings.TrimSpace(req.InheritedTMUX), ",")
	inherited = strings.TrimSpace(inherited)
	if inherited != "" && filepath.IsAbs(inherited) {
		return Transport{Kind: TransportSocketPath, Value: filepath.Clean(inherited), Source: TransportSourceInheritedEnv}, nil
	}
	return Transport{Kind: TransportNone, Source: TransportSourceNone}, nil
}

// HostMode is which of the two supported hosts the observed server is.
type HostMode string

const (
	// HostModeUnknown is a server whose ownership could not be observed: no
	// transport, or an option read that failed. It is never treated as
	// app-owned, so nothing in the graph gains authority from a failed read.
	HostModeUnknown HostMode = "unknown"
	// HostModeAppOwned is a server projmux started and owns.
	HostModeAppOwned HostMode = "app-owned"
	// HostModeStandalone is the operator's own tmux server, which projmux is a
	// guest on. Managed resources live here exactly as they do on an app-owned
	// server; what differs is that everything projmux did not create belongs to
	// the operator.
	HostModeStandalone HostMode = "standalone"
)

// HostModeFromAppMarker classifies an observed @projmux_app option value.
//
// Only the exact marker value is app ownership. Every other value -- absent,
// empty, "0", or anything an operator set by hand -- is a standalone host, so a
// mistyped option can never widen projmux's authority over someone else's
// server.
func HostModeFromAppMarker(value string) HostMode {
	if strings.TrimSpace(value) == AppOwnedMarker {
		return HostModeAppOwned
	}
	return HostModeStandalone
}
