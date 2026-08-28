package codexbroker

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	// ProtocolVersion is the local IPC version this build prefers.
	ProtocolVersion = 1
	// MinProtocolVersion is the oldest local IPC version this build still
	// speaks. Keeping a range rather than a single number is what lets an
	// installed binary be replaced while an older peer is still running: both
	// sides negotiate down to a version they share instead of refusing.
	MinProtocolVersion = 1

	// maxFrameBytes bounds one newline-delimited IPC frame.
	maxFrameBytes = 1 << 20
	// frameBufferBytes is the fixed per-connection read buffer. Frames larger
	// than it are accumulated in chunks up to maxFrameBytes.
	frameBufferBytes = 64 << 10
)

// ProtocolRange is one side's accepted local IPC version window.
type ProtocolRange struct {
	// Preferred is the newest version this side speaks.
	Preferred int
	// Minimum is the oldest version this side still accepts.
	Minimum int
}

// CurrentProtocol is the range this build speaks.
func CurrentProtocol() ProtocolRange {
	return ProtocolRange{Preferred: ProtocolVersion, Minimum: MinProtocolVersion}
}

// normalize fills a zero range with this build's own window.
func (r ProtocolRange) normalize() ProtocolRange {
	if r.Preferred <= 0 {
		r.Preferred = ProtocolVersion
	}
	if r.Minimum <= 0 {
		r.Minimum = MinProtocolVersion
	}
	if r.Minimum > r.Preferred {
		r.Minimum = r.Preferred
	}
	return r
}

// negotiate resolves the version two ranges share.
//
// The result is the newest version both sides speak, which is the highest
// version that is no greater than either preference and no less than either
// minimum. An empty intersection is not an error state to recover from: it
// means one side has been replaced by a binary the other cannot talk to, and
// the only safe outcome is a drain rather than a forced takeover.
func negotiate(client, host ProtocolRange) (int, bool) {
	client = client.normalize()
	host = host.normalize()
	version := min(client.Preferred, host.Preferred)
	floor := max(client.Minimum, host.Minimum)
	if version < floor {
		return 0, false
	}
	return version, true
}

// requestKind is the closed set of client-to-host operations.
type requestKind string

const (
	requestBind   requestKind = "bind"
	requestUnbind requestKind = "unbind"
	requestSubmit requestKind = "submit"
	requestAnswer requestKind = "answer"
)

// replyKind is the closed set of host-to-client frames.
type replyKind string

const (
	replyWelcome replyKind = "welcome"
	replyResult  replyKind = "result"
	replyRefused replyKind = "refused"
	replyEvent   replyKind = "event"
	replyRevoked replyKind = "revoked"
)

// hello is the first client frame. It carries the credential the running host
// published, the endpoint the client believes it is reaching, and the client's
// version window. Nothing else: a handshake that carried a working directory
// or a pid would invite the host to attribute a binding from it.
type hello struct {
	Preferred  int         `json:"protocol"`
	Minimum    int         `json:"minProtocol"`
	Endpoint   EndpointKey `json:"endpoint"`
	Credential string      `json:"credential"`
}

func (h hello) protocol() ProtocolRange {
	return ProtocolRange{Preferred: h.Preferred, Minimum: h.Minimum}
}

// wireRequest is one client operation.
//
// Runtime is the runtime id the client was welcomed by. It is the third
// authority axis after the connection and binding epochs, and it exists
// because those two epochs restart at one in every new host process: without
// it, a fence minted by a crashed runtime would numerically match a fresh
// one's.
type wireRequest struct {
	ID           uint64          `json:"id"`
	Kind         requestKind     `json:"kind"`
	Runtime      string          `json:"runtime"`
	Thread       string          `json:"thread,omitempty"`
	CWD          string          `json:"cwd,omitempty"`
	Roots        []string        `json:"roots,omitempty"`
	Fence        Fence           `json:"fence,omitzero"`
	Method       string          `json:"method,omitempty"`
	Params       json.RawMessage `json:"params,omitempty"`
	RawRequestID json.RawMessage `json:"rawRequestId,omitempty"`
}

// wireReply is one host frame. A frame with an ID answers that request; a
// frame without one is an unsolicited delivery for the thread it names.
type wireReply struct {
	ID       uint64          `json:"id,omitempty"`
	Kind     replyKind       `json:"kind"`
	Runtime  string          `json:"runtime,omitempty"`
	Protocol int             `json:"protocol,omitempty"`
	Refusal  Refusal         `json:"refusal,omitempty"`
	Outcome  MutationOutcome `json:"outcome,omitempty"`
	Thread   string          `json:"thread,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Event    *wireEvent      `json:"event,omitempty"`
}

// wireEvent is one delivered broker event.
//
// Params is opaque pass-through, exactly as it is in Event: the runtime
// forwards the provider's bytes and retains none of them.
type wireEvent struct {
	Fence        Fence                          `json:"fence"`
	Origin       EventOrigin                    `json:"origin"`
	Sequence     uint64                         `json:"sequence"`
	Method       string                         `json:"method,omitempty"`
	Params       json.RawMessage                `json:"params,omitempty"`
	RawRequestID json.RawMessage                `json:"rawRequestId,omitempty"`
	Snapshot     *codexappserver.ThreadSnapshot `json:"snapshot,omitempty"`
}

// readFrame reads one newline-delimited frame under the bounded size. The
// frame is accumulated from a small fixed buffer rather than read into one
// sized for the maximum, so a bound that exists to refuse an oversized frame
// does not allocate that frame for every idle connection.
func readFrame(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(frame)+len(chunk) > maxFrameBytes {
			return nil, refuse(RefusalFrameInvalid, nil)
		}
		frame = append(frame, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) == 0 {
				return nil, io.EOF
			}
			return nil, err
		}
		return frame[:len(frame)-1], nil
	}
}

// writeFrame writes one frame. json.Marshal escapes every newline inside a
// string, so the delimiter can never appear inside a payload.
func writeFrame(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return refuse(RefusalFrameInvalid, err)
	}
	if len(payload)+1 > maxFrameBytes {
		return refuse(RefusalFrameInvalid, nil)
	}
	if _, err := writer.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}
