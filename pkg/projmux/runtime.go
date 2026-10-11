package projmux

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodeRuntimeRequestInvalid denotes a malformed runtime observation request.
const CodeRuntimeRequestInvalid Code = "runtime-request-invalid"

// RuntimeRequest selects one object kind and at most one exact tmux server.
// SocketName corresponds to --socket; SocketPath must be absolute. Empty route
// fields use an explicitly configured Options.SocketPath, otherwise no server.
type RuntimeRequest struct {
	Kind       core.RuntimeObjectKind
	SocketName string
	SocketPath string
}

// ObserveRuntime reads one server without writing tmux or Registry state. It
// never infers a server from TMUX or falls back to the app/default socket.
// Results own their nested slices and pointers; concurrent calls are supported.
func (c *Client) ObserveRuntime(ctx context.Context, input RuntimeRequest) (core.RuntimeReport, error) {
	if err := ctx.Err(); err != nil {
		return core.RuntimeReport{}, err
	}
	var kind resourcegraph.ObjectKind
	switch input.Kind {
	case "":
		return core.RuntimeReport{}, &Refusal{Code: CodeRuntimeRequestInvalid, message: "get runtime requires an object kind: sessions, windows, panes"}
	case core.RuntimeSessions:
		kind = resourcegraph.ObjectSession
	case core.RuntimeWindows:
		kind = resourcegraph.ObjectWindow
	case core.RuntimePanes:
		kind = resourcegraph.ObjectPane
	default:
		return core.RuntimeReport{}, &Refusal{Code: CodeRuntimeRequestInvalid, message: fmt.Sprintf("get runtime %s is not available; this release implements: sessions, windows, panes", input.Kind)}
	}
	request := resourcegraph.TransportRequest{SocketName: input.SocketName, SocketPath: input.SocketPath}
	// Validate request fields before applying the Client's explicit route. An
	// explicit name overrides Options.SocketPath rather than conflicting with it.
	transport, err := runtimeservice.ResolveTransport(request)
	if err != nil {
		message := "RuntimeRequest.SocketName and SocketPath are mutually exclusive"
		if strings.TrimSpace(input.SocketName) == "" || strings.TrimSpace(input.SocketPath) == "" {
			message = fmt.Sprintf("RuntimeRequest.SocketPath must be absolute, got %q", strings.TrimSpace(input.SocketPath))
		}
		return core.RuntimeReport{}, &Refusal{Code: CodeRuntimeRequestInvalid, message: message}
	}
	if transport.Kind == resourcegraph.TransportNone && c.runtimeSocketPath != "" {
		transport, err = runtimeservice.ResolveTransport(resourcegraph.TransportRequest{SocketPath: c.runtimeSocketPath})
		if err != nil {
			return core.RuntimeReport{}, err
		}
	}
	if c.pathsErr != nil {
		return core.RuntimeReport{}, resourceRefusal(c.pathsErr)
	}
	reader := runtimeservice.NewReader(c.paths)
	graph, err := reader.Resolve(ctx, transport)
	if err != nil {
		if ctx.Err() != nil {
			return core.RuntimeReport{}, ctx.Err()
		}
		return core.RuntimeReport{}, resourceRefusal(err)
	}
	raw, err := json.Marshal(runtimeservice.Project(graph, kind))
	if err != nil {
		return core.RuntimeReport{}, err
	}
	var report core.RuntimeReport
	err = json.Unmarshal(raw, &report)
	return report, err
}
