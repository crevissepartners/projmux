// Package processquestions implements the existing exact question host protocol.
package processquestions

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type Request struct {
	Binding    processhost.Binding
	QuestionID string
	Selections map[int]agentquestion.Selection
}
type Result struct {
	Accepted, InvalidAnswer bool
	Questions               []ExactQuestion
}
type envelope struct{ Questions *Request }
type observation struct {
	Binding         processhost.Binding
	Host, Child     coremetadata.ProcessIdentity
	Provider, State string
	Exit            *processhost.Exit
}

func hostSocket(provider, registryPath, pane, generation string) string {
	suffix := "/host.sock"
	if provider == "codex" {
		suffix = "/codex-host.sock"
	}
	return claudeadapter.ActivationLeaseDir(registryPath, pane, generation) + suffix
}

// ExactQuestion contains only question display data, never a writable
// provider token. Policy is captured at admission; Delivery names its transport.
type ExactQuestion struct {
	QuestionID string                   `json:"questionID"`
	Policy     string                   `json:"policy"`
	Delivery   string                   `json:"delivery"`
	CanAnswer  bool                     `json:"canAnswer"`
	Prompts    []agentquestion.Question `json:"prompts"`
	State      agentquestion.State      `json:"state"`
	Deadline   *time.Time               `json:"deadline,omitempty"`
}

// Dispatcher uses the same Registry and host sockets as commands.
// An empty path resolves production paths; fixtures supply their isolated path.
type Dispatcher struct{ RegistryPath string }

func ReadExactProcessQuestions(ctx context.Context, agentUID string) ([]ExactQuestion, error) {
	result, err := (Dispatcher{}).call(ctx, agentUID, "", nil)
	return result.Questions, err
}
func AnswerExactProcessQuestion(ctx context.Context, agentUID, questionID string, selections map[int]agentquestion.Selection) error {
	if questionID == "" || len(selections) == 0 {
		return agentquestion.ErrInvalidAnswer
	}
	_, err := (Dispatcher{}).call(ctx, agentUID, questionID, selections)
	return err
}
func (d Dispatcher) ReadExactProcessQuestions(ctx context.Context, uid string) ([]ExactQuestion, error) {
	if d.RegistryPath == "" {
		return ReadExactProcessQuestions(ctx, uid)
	}
	result, err := d.call(ctx, uid, "", nil)
	return result.Questions, err
}
func (d Dispatcher) AnswerExactProcessQuestion(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) error {
	if id == "" || len(selections) == 0 {
		return agentquestion.ErrInvalidAnswer
	}
	if d.RegistryPath == "" {
		return AnswerExactProcessQuestion(ctx, uid, id, selections)
	}
	_, err := d.call(ctx, uid, id, selections)
	return err
}
func (d Dispatcher) call(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	path := d.RegistryPath
	if path == "" {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return Result{}, err
		}
		path = intmetadata.PathFor(paths.StateDir)
	}
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return Result{}, err
	}
	agent, ok := reg.Agent(uid)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning {
		return Result{}, processhost.ErrStale
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Process == nil {
		return Result{}, processhost.ErrStale
	}
	activation := *pane.Status.Activation.Process
	binding := schemaBinding(activation.Binding)
	view := observation{Binding: binding, Provider: agent.Spec.Provider, Host: activation.HostProcess, Child: activation.Child, State: "ready"}
	record := pane.Status.ProcessSession
	session := ""
	connection := ""
	if record != nil {
		session = record.SessionID
		if agent.Spec.Provider == "codex" {
			session = record.ThreadID
		}
		connection = record.ConnectionID
	}
	if !observationMatches(reg, binding, view) || !SessionMatches(reg, binding, agent.Spec.Provider, session, connection) {
		return Result{}, processhost.ErrStale
	}
	socket := hostSocket(agent.Spec.Provider, path, binding.Pane, binding.Generation)
	inode, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return Result{}, err
	}
	request := &Request{Binding: binding, QuestionID: id, Selections: selections}
	wire := envelope{Questions: request}
	if agent.Spec.Provider != "codex" && agent.Spec.Provider != "claude" {
		return Result{}, processhost.ErrStale
	}
	result, err := call(ctx, socket, inode, activation.HostProcess, wire)
	if err != nil {
		return Result{}, err
	} // uncertain writes are never retried
	current, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	next, exists := current.Pane(binding.Pane)
	afterInode, inodeErr := localipc.InspectOwnedSocket(socket)
	if err != nil || !exists || next.Status.Activation.Process == nil || *next.Status.Activation.Process != activation || !observationMatches(current, binding, view) || !SessionMatches(current, binding, agent.Spec.Provider, session, connection) || inodeErr != nil || afterInode != inode {
		return Result{}, processhost.ErrStale
	}
	if !result.Accepted {
		if result.InvalidAnswer {
			return Result{}, agentquestion.ErrInvalidAnswer
		}
		return Result{}, processhost.ErrStale
	}
	return result, nil
}

func observationMatches(reg coremetadata.Registry, binding processhost.Binding, view observation) bool {
	if !observationOwnership(reg, binding, view) {
		return false
	}
	host, _, err := localipc.Process(view.Host.PID)
	if err != nil || host != view.Host {
		return false
	}
	if view.Exit != nil {
		return view.State == "exited"
	}
	child, parent, err := localipc.Process(view.Child.PID)
	if err != nil || child != view.Child {
		return false
	}
	// The provider belongs to the host's dedicated supervisor, not to an
	// unrelated process that happens to have the same UID.
	_, hostPID, err := localipc.Process(parent)
	if err != nil || hostPID != view.Host.PID {
		return false
	}
	switch view.State {
	case "starting", "ready", "stopping", "unknown":
		return true
	default:
		return false
	}
}

func observationOwnership(reg coremetadata.Registry, binding processhost.Binding, view observation) bool {
	current := metadataBinding(binding)
	activation, provider, ok := reg.CurrentProcessActivation(current)
	return ok && view.Binding == binding && provider == view.Provider &&
		activation.HostProcess == view.Host && activation.Child == view.Child &&
		int64(view.Host.OwnerUID) == int64(os.Getuid()) && int64(view.Child.OwnerUID) == int64(os.Getuid())
}

func call(ctx context.Context, socket string, socketIdentity localipc.SocketIdentity, host coremetadata.ProcessIdentity, request any) (Result, error) {
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil || identity != socketIdentity {
		return Result{}, processhost.ErrStale
	}
	deadline := time.Now().Add(localipc.Deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(bounded, "unix", socket)
	if err != nil {
		return Result{}, err
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != host || ctx.Err() != nil {
		return Result{}, processhost.ErrStale
	}
	if err = localipc.WriteJSON(conn, request); err != nil {
		return Result{}, err
	}
	if err = conn.CloseWrite(); err != nil {
		return Result{}, err
	}
	var result Result
	err = localipc.ReadJSON(conn, &result)
	return result, err
}
func schemaBinding(b coremetadata.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}

func metadataBinding(b processhost.Binding) coremetadata.ProcessBinding {
	return coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}
}
func SessionMatches(reg coremetadata.Registry, b processhost.Binding, provider, session, connection string) bool {
	a, ok := reg.Agent(b.Agent)
	p, found := reg.Pane(b.Pane)
	if !ok || !found || a.Status.Phase != coremetadata.PhaseRunning || a.Status.SessionRef == nil || a.Status.SessionRef.Provider != provider || p.Status.ProcessSession == nil || session == "" || connection == "" {
		return false
	}
	record := p.Status.ProcessSession
	if record.Binding != metadataBinding(b) || record.Provider != provider || record.ConnectionID != connection {
		return false
	}
	if provider == "claude" {
		return a.Status.SessionRef.Claude != nil && a.Status.SessionRef.Claude.SessionID == session && record.SessionID == session
	}
	return provider == "codex" && a.Status.SessionRef.Codex != nil && a.Status.SessionRef.Codex.ThreadID == session && record.ThreadID == session
}
