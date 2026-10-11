// Package agentcontrol shares exact native Codex approval transport and fencing.
package agentcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const ReadFencePoll = 2 * time.Millisecond
const FenceDir = "codex-authority-fences"
const ControlDir = "agent-control"

// AuthorityControlPlane marks an active exact native provider control binding.
const AuthorityControlPlane = "provider-control-plane"

var ErrNotSent = errors.New("exact Agent control request was not sent")

type notSentError struct{ err error }

func (e *notSentError) Error() string   { return e.err.Error() }
func (e *notSentError) Unwrap() []error { return []error{e.err, ErrNotSent} }
func notSent(err error) error           { return &notSentError{err: err} }

type Live struct {
	RuntimeID string
	PaneUID   string
	ThreadID  string
	Authority string
	Epoch     string
	Reason    string
}

type FrameError struct {
	Reason     string
	FieldCount int
}

func (e *FrameError) Error() string {
	if e.FieldCount > 0 {
		return fmt.Sprintf("live Codex control binding is malformed: %s (%d fields, want 6)", e.Reason, e.FieldCount)
	}
	return "live Codex control binding is malformed: " + e.Reason
}

type BindingError struct{ Reason string }

func (e *BindingError) Error() string {
	return "exact Agent native control unavailable: " + e.Reason
}

func ParseFrame(out []byte) (Live, error) {
	frame := string(out)
	if before, ok := strings.CutSuffix(frame, "\n"); ok {
		frame = before
		frame = strings.TrimSuffix(frame, "\r")
	}
	if strings.ContainsAny(frame, "\r\n") {
		return Live{}, &FrameError{Reason: "multiple output lines"}
	}
	hasEscaped := strings.Contains(frame, "\\037")
	hasRaw := strings.Contains(frame, "\x1f")
	if hasEscaped && hasRaw {
		return Live{}, &FrameError{Reason: "mixed separator spellings"}
	}
	separator := "\\037"
	if hasRaw {
		separator = "\x1f"
	} else if !hasEscaped {
		return Live{}, &FrameError{Reason: "separator is missing", FieldCount: 1}
	}
	fields := strings.Split(frame, separator)
	if len(fields) != 6 {
		return Live{}, &FrameError{Reason: "field count is not exact", FieldCount: len(fields)}
	}
	return Live{RuntimeID: fields[0], PaneUID: fields[1], ThreadID: fields[2], Authority: fields[3], Epoch: fields[4], Reason: fields[5]}, nil
}

func AcquireReadFence(ctx context.Context, stateDir, paneUID string) (func(), error) {
	path, err := FencePath(stateDir, paneUID)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- FencePath returns the private state
	// directory plus a digest of the Registry-authenticated Pane uid.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open Codex authority fence: %w", err)
	}
	localstate.RepairPrivateFile(path)
	for {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock Codex authority fence: %w", lockErr)
		}
		timer := time.NewTimer(ReadFencePoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, fmt.Errorf("wait for a settled Codex authority transition: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

type Binding struct {
	Identity   Identity
	Endpoint   coremetadata.CodexEndpointRef
	Epoch      string
	StateDir   string
	ProjectUID string
	WindowUID  string
	Fence      string
}

func Resolve(registry coremetadata.Registry, agent coremetadata.Agent, live Live, observed bool, stateDir string) (Binding, error) {
	refusal := func(reason string) (Binding, error) {
		return Binding{}, &BindingError{Reason: reason}
	}
	if agent.Spec.Provider != "codex" {
		return refusal("the selected Agent is not Codex")
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return refusal("the selected Agent has no current Running Pane")
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Status.Activation.AgentUID != agent.Metadata.UID {
		return refusal("Agent to Pane ownership is not exact")
	}
	threadID := ""
	if pane.Status.Activation.Codex != nil {
		threadID = strings.TrimSpace(pane.Status.Activation.Codex.ThreadID)
	}
	if threadID == "" || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID == "" {
		return refusal("activation generation or Codex thread identity is missing")
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Valid() {
		return refusal("legacy-generation-unavailable")
	}
	durableThreadID := strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID)
	if durableThreadID == "" || durableThreadID != threadID {
		return refusal("durable Codex thread does not match the Pane activation")
	}
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	if agent.Status.SessionRef.Codex.Lifecycle != nil &&
		!agent.Status.SessionRef.Codex.Lifecycle.ValidFor(&endpoint) {
		return refusal("durable endpoint lifecycle is invalid")
	}
	if pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() ||
		!pane.Status.Activation.Codex.Authority.Endpoint().Same(endpoint) {
		return refusal("live activation authority does not match the durable endpoint generation")
	}
	if !observed || live.RuntimeID != pane.Status.Activation.RuntimeID || live.PaneUID != pane.Metadata.UID || live.ThreadID != threadID {
		return refusal("live Pane identity no longer matches the activation")
	}
	if live.Authority != AuthorityControlPlane || strings.TrimSpace(live.Epoch) == "" {
		reason := strings.TrimSpace(live.Reason)
		if reason == "" {
			reason = "not-ready"
		}
		return refusal("the native connection epoch is unavailable (" + reason + "); Open Codex")
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Window is missing")
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Project is missing")
	}
	lifecycle, durable, authorized := LifecycleInput(
		registry, agent, pane.Metadata.UID, live.RuntimeID, agent.EffectiveInteraction(time.Now()).Kind,
	)
	consumer := codexgeneration.ProjectConsumers(lifecycle, codexgeneration.RuntimeMutationInput{
		DurableEndpoint: agent.Status.SessionRef.Codex.Endpoint,
		StoredAuthority: pane.Status.Activation.Codex.Authority, PresentedAuthority: pane.Status.Activation.Codex.Authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: live.RuntimeID,
	}, true)
	if !durable || !authorized || consumer.Effect != codexgeneration.MutationSemanticEffect ||
		!consumer.Endpoint.Same(endpoint) || consumer.Fence == "" {
		return refusal("canonical generation consumer fence is unavailable")
	}
	return Binding{
		Identity: Identity{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, RuntimeID: pane.Status.Activation.RuntimeID, Generation: pane.Status.Activation.Generation, ThreadID: threadID},
		Endpoint: endpoint, Epoch: live.Epoch, StateDir: stateDir, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID,
		Fence: consumer.Fence,
	}, nil
}

type Request struct {
	Operation  string   `json:"operation"`
	Identity   Identity `json:"identity"`
	Epoch      string   `json:"epoch"`
	Text       string   `json:"text,omitempty"`
	RequestKey string   `json:"requestKey,omitempty"`
	Decision   string   `json:"decision,omitempty"`
}

type Response struct {
	OK           bool              `json:"ok"`
	Code         string            `json:"code,omitempty"`
	Message      string            `json:"message,omitempty"`
	Acceptance   string            `json:"acceptance,omitempty"`
	Delivery     string            `json:"delivery,omitempty"`
	Availability Availability      `json:"availability"`
	Approvals    []PendingApproval `json:"approvals,omitempty"`
	ThreadID     string            `json:"threadId,omitempty"`
	TurnID       string            `json:"turnId,omitempty"`
}

type Availability struct {
	Start     bool `json:"start"`
	Steer     bool `json:"steer"`
	Interrupt bool `json:"interrupt"`
	Review    bool `json:"review"`
}

type PendingApproval struct {
	RequestID       string                            `json:"requestId"`
	Kind            codexappserver.ApprovalKind       `json:"kind"`
	ThreadID        string                            `json:"threadId"`
	TurnID          string                            `json:"turnId"`
	ItemID          string                            `json:"itemId"`
	ApprovalID      *string                           `json:"approvalId,omitempty"`
	Command         string                            `json:"command,omitempty"`
	CWD             string                            `json:"cwd,omitempty"`
	NetworkHost     string                            `json:"networkHost,omitempty"`
	NetworkProtocol string                            `json:"networkProtocol,omitempty"`
	RequestCWD      string                            `json:"requestCwd,omitempty"`
	Reason          string                            `json:"reason,omitempty"`
	GrantRoot       *string                           `json:"grantRoot,omitempty"`
	Permissions     json.RawMessage                   `json:"permissions,omitempty"`
	Decisions       []codexappserver.ApprovalDecision `json:"decisions"`
}

func (r Response) Error() error {
	if r.OK {
		return nil
	}
	return &Refusal{Code: r.Code, Message: r.Message}
}

type Refusal struct {
	Code    string
	Message string
}

func (e *Refusal) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("native Codex control unavailable (%s)", e.Code)
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

type Identity struct {
	AgentUID   string
	PaneUID    string
	RuntimeID  string
	Generation string
	ThreadID   string
}

func (i Identity) Valid() bool {
	return strings.TrimSpace(i.AgentUID) != "" && strings.TrimSpace(i.PaneUID) != "" &&
		strings.TrimSpace(i.RuntimeID) != "" && strings.TrimSpace(i.Generation) != "" &&
		strings.TrimSpace(i.ThreadID) != ""
}

func FencePath(stateDir, paneUID string) (string, error) {
	path, err := FenceFile(stateDir, paneUID)
	if err != nil {
		return "", err
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return "", fmt.Errorf("create Codex authority fence directory: %w", err)
	}
	return path, nil
}

func FenceFile(stateDir, paneUID string) (string, error) {
	paneUID = strings.TrimSpace(paneUID)
	if paneUID == "" {
		return "", fmt.Errorf("codex authority fence requires pane uid")
	}
	digest := sha256.Sum256([]byte(paneUID))
	return filepath.Join(stateDir, FenceDir, fmt.Sprintf("%x.lock", digest)), nil
}

func LifecycleInput(registry coremetadata.Registry, agent coremetadata.Agent, paneUID, target string, interaction coremetadata.AgentInteractionKind) (codexgeneration.LifecycleProjectionInput, bool, bool) {
	legacy := codexgeneration.LifecycleProjectionInput{Interaction: interaction}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Lifecycle == nil {
		return legacy, false, true
	}
	codex := agent.Status.SessionRef.Codex
	lifecycle := codex.Lifecycle
	input := codexgeneration.LifecycleProjectionInput{
		Interaction: interaction, Endpoint: codex.Endpoint,
		GenerationState: lifecycle.State, Operation: lifecycle.Operation,
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID ||
		agent.Spec.Provider != "codex" || !input.Authoritative() {
		return input, true, false
	}
	pane, ok := registry.Pane(paneUID)
	if !ok || pane.Metadata.OwnerUID() != agent.Metadata.UID ||
		pane.Status.Activation.AgentUID != agent.Metadata.UID ||
		pane.Status.Activation.RuntimeID != target || pane.Status.Activation.Codex == nil ||
		pane.Status.Activation.Codex.ThreadID != codex.ThreadID ||
		pane.Status.Activation.Codex.Authority == nil {
		return input, true, false
	}
	authority := pane.Status.Activation.Codex.Authority
	decision := codexgeneration.DecideRuntimeMutation(codexgeneration.RuntimeMutationInput{
		DurableEndpoint: codex.Endpoint,
		StoredAuthority: authority, PresentedAuthority: authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: target,
	})
	return input, true, decision.Class.Effect == codexgeneration.MutationSemanticEffect
}

// Call sends one bounded native control frame. The optional validator keeps
// the CLI's framing adapter on its execution path; otherwise the same default
// framing rule is used by the SDK.
func Call(ctx context.Context, stateDir string, endpoint coremetadata.CodexEndpointRef, identity Identity, request Request, validators ...func(*json.Decoder) bool) (Response, error) {
	trailing := hasTrailingJSON
	if len(validators) > 0 {
		trailing = validators[0]
	}

	path, err := SocketPath(stateDir, endpoint, identity)
	if err != nil {
		return Response{}, notSent(err)
	}
	dialer := net.Dialer{Timeout: localipc.DialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, notSent(fmt.Errorf("native Codex control endpoint unavailable: %w", err))
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
	}
	var requestPayload bytes.Buffer
	if err := json.NewEncoder(&requestPayload).Encode(request); err != nil || requestPayload.Len() > localipc.MaxFrameBytes {
		return Response{}, notSent(errors.New("native Codex control request exceeds the bounded frame"))
	}
	if _, err := conn.Write(requestPayload.Bytes()); err != nil {
		return Response{}, fmt.Errorf("write native Codex control request: %w", err)
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}
	payload, err := io.ReadAll(io.LimitReader(conn, localipc.MaxFrameBytes+1))
	if err != nil || len(payload) > localipc.MaxFrameBytes {
		return Response{}, errors.New("native Codex control response exceeds the bounded frame")
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("read native Codex control response: %w", err)
	}
	if trailing(decoder) {
		return Response{}, errors.New("read native Codex control response: trailing frame data")
	}
	return response, nil
}

func hasTrailingJSON(decoder *json.Decoder) bool {
	var extra any
	err := decoder.Decode(&extra)
	return !errors.Is(err, io.EOF)
}

func SocketPath(stateDir string, endpoint coremetadata.CodexEndpointRef, identity Identity) (string, error) {
	stateDir = filepath.Clean(stateDir)
	if stateDir == "." || !filepath.IsAbs(stateDir) || !endpoint.Valid() || !identity.Valid() {
		return "", errors.New("exact Agent control requires an absolute state directory and durable endpoint/thread identity")
	}
	// The local control address is stable for one durable endpoint/thread and
	// deliberately excludes PaneActivation.Generation. That activation epoch
	// fences writes inside the request, but it is not app-server route identity
	// and cannot retarget an Agent when admission-current changes.
	sum := sha256.Sum256([]byte(endpoint.StateDomainID + "\x00" + endpoint.EndpointGenerationID + "\x00" +
		identity.AgentUID + "\x00" + identity.PaneUID + "\x00" + identity.ThreadID))
	name := "control-" + hex.EncodeToString(sum[:12]) + ".sock"
	dir := filepath.Join(stateDir, ControlDir)
	path := filepath.Join(dir, name)
	if len(path) > localipc.MaxSocketPath {
		return "", errors.New("exact Agent control socket path exceeds the platform-safe bound")
	}
	return path, nil
}
