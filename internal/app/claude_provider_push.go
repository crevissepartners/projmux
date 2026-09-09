package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
)

// claudeFrozenFrameProviderVersion is deliberately a single exact version,
// not a version family. A different running Claude must complete the isolated
// qualification canary before this constant can move.
const claudeFrozenFrameProviderVersion = "2.1.263"

type claudeProviderPostOutcome struct {
	FullFrameWritten bool
	WroteAny         bool
	Reason           string
}

func (o claudeProviderPostOutcome) Ambiguous() bool {
	return o.WroteAny && !o.FullFrameWritten
}

type claudeProviderPoster interface {
	Post(string, func() bool) (claudeProviderPostOutcome, error)
}

type claudeProviderAuthFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type claudeProviderUserMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeProviderUserFrame struct {
	Type    string                    `json:"type"`
	Message claudeProviderUserMessage `json:"message"`
}

// buildClaudeProviderPushFrame owns the only permitted post-auth vendor frame.
// Do not add a control, reply, status, or inferred frame to this boundary.
func buildClaudeProviderPushFrame(token, content string) ([]byte, error) {
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("provider-frame-invalid-auth")
	}
	if content == "" || !validClaudeAssistantReply(content) {
		return nil, errors.New("provider-frame-invalid-content")
	}
	auth, err := json.Marshal(claudeProviderAuthFrame{Type: "auth", Token: token})
	if err != nil {
		return nil, errors.New("provider-frame-build-failed")
	}
	message, err := json.Marshal(claudeProviderUserFrame{Type: "user", Message: claudeProviderUserMessage{Role: "user", Content: content}})
	if err != nil {
		return nil, errors.New("provider-frame-build-failed")
	}
	frame := make([]byte, 0, len(auth)+len(message)+2)
	frame = append(frame, auth...)
	frame = append(frame, '\n')
	frame = append(frame, message...)
	frame = append(frame, '\n')
	if len(frame) > claudeProviderFrameMaxBytes {
		return nil, errors.New(claudeProviderFrameSizeReason(len(frame)))
	}
	return frame, nil
}

// Reasons cross the helper/public boundary and are persisted in existing
// receipts. Only closed tokens and canonical numeric frame sizes belong here;
// never attach a marshal, socket, credential, content, or writer error.
func claudeProviderFrameSizeReason(size int) string {
	return fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=%d", size, claudeProviderFrameMaxBytes)
}

func isClaudeProviderFrameSizeReason(reason string) bool {
	value, ok := strings.CutPrefix(reason, "provider-frame-too-large: frameBytes=")
	if !ok {
		return false
	}
	sizeText, _, ok := strings.Cut(value, " ")
	size, err := strconv.Atoi(sizeText)
	return ok && err == nil && size > claudeProviderFrameMaxBytes && reason == claudeProviderFrameSizeReason(size)
}

func knownClaudeProviderFailureReason(reason string) bool {
	switch reason {
	case "provider-frame-invalid-auth", "provider-frame-invalid-content", "provider-frame-build-failed",
		"provider-prewrite-refused", "provider-write-zero":
		return true
	default:
		return isClaudeProviderFrameSizeReason(reason)
	}
}

// writeClaudeProviderPushFrame makes exactly one write. Retrying a partial
// provider write could duplicate a model-visible message and is forbidden.
func writeClaudeProviderPushFrame(writer io.Writer, frame []byte) claudeProviderPostOutcome {
	n, err := writer.Write(frame)
	if n < 0 || n > len(frame) {
		return claudeProviderPostOutcome{WroteAny: true, Reason: "provider-handoff-outcome-unknown"}
	}
	if n == 0 {
		return claudeProviderPostOutcome{Reason: "provider-write-zero"}
	}
	if n < len(frame) {
		return claudeProviderPostOutcome{WroteAny: true, Reason: "provider-write-partial"}
	}
	if err != nil {
		return claudeProviderPostOutcome{WroteAny: true, Reason: "provider-handoff-outcome-unknown"}
	}
	return claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}
}

type liveClaudeProviderPoster struct {
	socket         string
	token          string
	socketIdentity claudeSocketIdentity
	process        coremetadata.ProcessIdentity
	current        func() bool
}

func (p *liveClaudeProviderPoster) Post(content string, fence func() bool) (claudeProviderPostOutcome, error) {
	current := func() bool {
		return p.current != nil && p.current() && (fence == nil || fence())
	}
	frame, err := buildClaudeProviderPushFrame(p.token, content)
	if err != nil {
		return claudeProviderPostOutcome{Reason: err.Error()}, err
	}
	if !current() {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("claude provider push refused")
	}
	identity, err := inspectClaudeSocket(p.socket)
	if err != nil || identity != p.socketIdentity {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("claude provider push refused")
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: p.socket, Net: "unix"})
	if err != nil {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("claude provider push refused")
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	peer, err := claudeadapter.PeerProcess(connection)
	if err != nil || peer != p.process || !current() {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("claude provider push refused")
	}
	identity, err = inspectClaudeSocket(p.socket)
	if err != nil || identity != p.socketIdentity || !current() {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("claude provider push refused")
	}
	outcome := writeClaudeProviderPushFrame(connection, frame)
	if !outcome.FullFrameWritten {
		return outcome, errors.New("claude provider push failed")
	}
	return outcome, nil
}
