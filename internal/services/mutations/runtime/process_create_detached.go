package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const ProcessDetachedStartupTimeout = 30 * time.Second
const ProcessDetachedWireLimit = 2 << 20

type ProcessDetachedCreateRequest struct {
	Options      ProcessAgentCreateOptions
	Output       string
	Caller       coremetadata.ProcessIdentity
	RegistryPath string
	Context      *ProcessCreateContext `json:",omitempty"`

	// Reserve asks for a reservation only: Pending, frozen launch, no provider.
	Reserve bool `json:",omitempty"`

	// ReserveOperation is the caller's operation id, to find remains it never saw.
	ReserveOperation string `json:",omitempty"`
}

type ProcessCreateCommitted struct {
	MayBeTruncated bool `json:",omitempty"`
	Agent          coremetadata.Agent
	Pane           coremetadata.Pane
	Receipt        receipt.OperationReceipt
	Notices        []string
}

type ProcessDetachedReply struct {
	Preparation           *ProcessResumePreparation `json:",omitempty"`
	Relaunch              json.RawMessage           `json:",omitempty"`
	ErrorDetail           json.RawMessage           `json:",omitempty"`
	Owner                 coremetadata.ProcessIdentity
	Binding               processhost.Binding
	Stdout, Stderr, Error string
	Phase                 string
	Claim                 json.RawMessage         `json:",omitempty"`
	Result                *ProcessCreateCommitted `json:",omitempty"`
	ErrorCode             string                  `json:",omitempty"`
}

// ProcessDetachedLauncher is the shared typed startup/commit executor. The
// application supplies binding/control callbacks; SDK callers use the same
// owned internal launcher, never the public create command.
type ProcessDetachedLauncher struct {
	// Transient runs a no-provider preparation worker as a directly reaped child.
	Transient   bool
	Executable  string
	Environment []string
	// PayloadDir is frozen for SDK calls; empty preserves the CLI temp domain.
	PayloadDir string
	Verify     func(ProcessDetachedReply, coremetadata.ProcessIdentity) error
	Cleanup    func(coremetadata.ProcessIdentity, ProcessDetachedReply) error
	// Admit is used only by explicit SDK relaunch: a read-only plan precedes Stop.
	Admit                  func(ProcessDetachedReply) error
	CompleteAfterAdmission bool
}

func (l ProcessDetachedLauncher) Launch(ctx context.Context, request any, stdout, stderr io.Writer) (ready ProcessDetachedReply, returnErr error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return ready, err
	}
	if len(encoded) > ProcessDetachedWireLimit {
		return ready, processCreateUsage("process-detached-request-too-large: nothing was created")
	}
	payload, err := os.CreateTemp(l.PayloadDir, "pmx-detach-*")
	if err != nil {
		return ready, err
	}
	defer payload.Close()
	if err = os.Remove(payload.Name()); err != nil {
		return ready, err
	}
	if _, err = payload.Write(encoded); err != nil {
		return ready, err
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return ready, err
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return ready, err
	}
	syscall.CloseOnExec(pair[0])
	syscall.CloseOnExec(pair[1])
	own := os.NewFile(uintptr(pair[0]), "detach-caller")
	child := os.NewFile(uintptr(pair[1]), "detach-owner")
	defer child.Close()
	conn, err := net.FileConn(own)
	_ = own.Close()
	if err != nil {
		return ready, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return ready, err
	}
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer interrupt()
	route := "process-agent-launch"
	if l.Transient {
		route = "process-agent-owner"
	}
	// #nosec G204 -- this is the caller's own executable with fixed internal argv; private request data travels only through fd 4.
	launcher := exec.Command(l.Executable, "internal", route)
	launcher.Env = l.Environment
	launcher.ExtraFiles = []*os.File{child, payload}
	if err = launcher.Start(); err != nil {
		return ready, err
	}
	_ = child.Close()
	done := make(chan error, 1)
	go func() { done <- launcher.Wait() }()
	launcherReaped := false
	if l.Transient {
		defer func() {
			if !launcherReaped {
				_ = launcher.Process.Kill()
				<-done
			}
		}()
	}
	decoder := json.NewDecoder(io.LimitReader(conn, ProcessDetachedWireLimit))
	encoder := json.NewEncoder(conn)
	var identity ProcessDetachedReply
	if err = decoder.Decode(&identity); err != nil {
		_ = launcher.Process.Kill()
		<-done
		launcherReaped = true
		return ready, processDetachedIdentityFailure(ctx, deadline, err)
	}
	if !identity.Owner.Valid() || int64(identity.Owner.OwnerUID) != int64(os.Getuid()) {
		_ = launcher.Process.Kill()
		<-done
		launcherReaped = true
		return ready, errors.New("process-detached-owner-identity-invalid")
	}
	ready.Owner = identity.Owner
	// Once an exact owner is known, every failed startup asks only that kernel
	// identity to stop. A failed/closed handshake also makes the owner roll back.
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
			SignalDetachedProcessOwner(identity.Owner)
			if l.Cleanup != nil {
				returnErr = errors.Join(returnErr, l.Cleanup(identity.Owner, ready))
			}
		}
	}()
	kernelOwner, _, identityErr := localipc.Process(identity.Owner.PID)
	if identityErr != nil || kernelOwner != identity.Owner {
		_ = launcher.Process.Kill()
		<-done
		launcherReaped = true
		return ready, errors.New("process-detached-owner-identity-unconfirmed")
	}
	if err = decoder.Decode(&ready); err != nil {
		return ready, fmt.Errorf("process-detached-startup-handshake-failed: owner pid %d: %w", identity.Owner.PID, errors.Join(err, ctx.Err()))
	}
	if l.Transient {
		// This worker has no provider or claim to hand off. Reap it before returning,
		// including on malformed replies and cancellation.
		if ready.Error != "" {
			return ready, errors.New(ready.Error)
		}
		if ready.Phase != "prepared" || ready.Owner != identity.Owner || l.Verify == nil {
			return ready, errors.New("process-preparation-reply-invalid")
		}
		if err = l.Verify(ready, identity.Owner); err != nil {
			return ready, err
		}
		if err = ctx.Err(); err != nil {
			return ready, err
		}
		if err = encoder.Encode("commit"); err != nil {
			return ready, err
		}
		var completed string
		if err = decoder.Decode(&completed); err != nil {
			return ready, errors.Join(err, ctx.Err())
		}
		if completed != "completed" {
			return ready, errors.New("process-preparation-commit-invalid")
		}
		select {
		case err = <-done:
			launcherReaped = true
			if err != nil {
				return ready, err
			}
		case <-ctx.Done():
			return ready, ctx.Err()
		}
		failed = false
		return ready, nil
	}
	if ready.Phase == "planned" && ready.Error == "" {
		if ready.Owner != identity.Owner || l.Admit == nil {
			return ready, errors.New("process-detached-plan-invalid")
		}
		if err = l.Admit(ready); err != nil {
			return ready, err
		}
		if err = ctx.Err(); err != nil {
			return ready, err
		}
		if l.CompleteAfterAdmission {
			// A tmux destination has no owned process to roll back. Once Stop
			// is admitted its rebind/recovery finishes despite request cancellation.
			if !interrupt() {
				return ready, ctx.Err()
			}
			if err = conn.SetDeadline(time.Now().Add(ProcessDetachedStartupTimeout)); err != nil {
				return ready, err
			}
		}
		if err = encoder.Encode("admit"); err != nil {
			return ready, err
		}
		if err = decoder.Decode(&ready); err != nil {
			return ready, errors.Join(err, ctx.Err())
		}
	}
	if ready.Phase == "starting" && ready.Error == "" {
		started := ready
		if err = decoder.Decode(&ready); err != nil {
			ready = started
			return ready, fmt.Errorf("process-detached-startup-handshake-failed: %w", errors.Join(err, ctx.Err()))
		}
		if ready.Binding != started.Binding || ready.Owner != started.Owner {
			return ready, errors.New("process-detached-startup-binding-changed")
		}
	}
	if err = <-done; err != nil {
		return ready, err
	}
	if ready.Error != "" {
		return ready, errors.New(ready.Error)
	}
	if ready.Owner != identity.Owner || ready.Binding.Agent == "" {
		return ready, errors.New("process-detached-binding-invalid")
	}
	if l.Verify == nil {
		return ready, errors.New("process-detached-verifier-unavailable")
	}
	if err = l.Verify(ready, identity.Owner); err != nil {
		return ready, err
	}

	if _, err = io.WriteString(stderr, ready.Stderr); err != nil {
		return ready, err
	}
	if _, err = io.WriteString(stdout, ready.Stdout); err != nil {
		return ready, err
	}
	// Output failure and caller cancellation precede commit. The owner never
	// treats EOF as success and retains the deadline until this acknowledgment.
	if err = encoder.Encode("commit"); err != nil {
		return ready, err
	}
	var acknowledged string
	if err = decoder.Decode(&acknowledged); err != nil {
		return ready, err
	}
	if acknowledged != "owned" {
		return ready, errors.New("process-detached-commit-refused")
	}
	failed = false
	return ready, nil
}

// processDetachedIdentityFailure retains the historical message while making
// the applied context deadline observable even if the socket timer wins the
// race with the context timer. Other I/O failures keep their original causes.
func processDetachedIdentityFailure(ctx context.Context, appliedDeadline time.Time, readErr error) error {
	contextErr := ctx.Err()
	cause := errors.Join(readErr, contextErr)
	deadline, hasDeadline := ctx.Deadline()
	if contextErr == nil && hasDeadline && !appliedDeadline.IsZero() && deadline.Equal(appliedDeadline) &&
		!time.Now().Before(deadline) && errors.Is(readErr, os.ErrDeadlineExceeded) {
		cause = &processDetachedDeadlineCause{cause: cause}
	}
	return fmt.Errorf("process-detached-identity-handshake-failed: %w", cause)
}

// Error preserves transport diagnostics; Unwrap adds only the missing cause.
type processDetachedDeadlineCause struct{ cause error }

func (e *processDetachedDeadlineCause) Error() string { return e.cause.Error() }
func (e *processDetachedDeadlineCause) Unwrap() []error {
	return []error{e.cause, context.DeadlineExceeded}
}

func SignalDetachedProcessOwner(identity coremetadata.ProcessIdentity) {
	current, _, err := localipc.Process(identity.PID)
	if err == nil && current == identity {
		if process, err := os.FindProcess(identity.PID); err == nil {
			_ = process.Signal(syscall.SIGTERM)
		}
	}
}

// ProcessCreateFailure retains the exact owner and generation even when
// rollback cannot be confirmed. Cause remains available through errors.Is/As.
type ProcessCreateFailure struct {
	Code    string
	Owner   coremetadata.ProcessIdentity
	Binding processhost.Binding
	Cause   error
}

func (e *ProcessCreateFailure) Error() string { return e.Cause.Error() }
func (e *ProcessCreateFailure) Unwrap() error { return e.Cause }

// ExecuteDetachedProcessCreate admits one typed SDK request into the same
// internal owner route used by the CLI. The caller owns startup only.
func ExecuteDetachedProcessCreate(ctx context.Context, frozen ProcessCreateContext, options ProcessAgentCreateOptions) (reply ProcessDetachedReply, err error) {
	if err = ctx.Err(); err != nil {
		return reply, err
	}
	if err = ValidateProcessCreateContext(frozen); err != nil {
		return reply, err
	}
	request, err := NewProcessAgentCreateRequest(options)
	if err != nil {
		return reply, err
	}
	storage := intmetadata.NewStore(intmetadata.PathFor(frozen.Paths.StateDir))
	reg, err := storage.LoadReadOnly()
	if err != nil {
		return reply, err
	}
	if _, _, err = ResolveProcessCreateScope(reg, request.Options); err != nil {
		return reply, err
	}
	if _, err = DeclaredAgentCreator(&reg, request.Options.Creator, "create agent", "CreateAgentRequest.Creator"); err != nil {
		return reply, err
	}
	if err = ctx.Err(); err != nil {
		return reply, err
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	wire := ProcessDetachedCreateRequest{Options: request.Options, Output: "none", Caller: caller, RegistryPath: storage.Path(), Context: &frozen}
	launcher := ProcessDetachedLauncher{Executable: frozen.Executable, Environment: frozen.Environment, PayloadDir: frozen.Paths.StateDir, Verify: func(ready ProcessDetachedReply, owner coremetadata.ProcessIdentity) error {
		return VerifyDetachedProcessCreate(storage.LoadReadOnly, ready, owner)
	}, Cleanup: func(owner coremetadata.ProcessIdentity, ready ProcessDetachedReply) error {
		return AwaitDetachedProcessCreateCleanup(storage.LoadReadOnly, owner, ready.Binding)
	}}
	reply, err = launcher.Launch(ctx, wire, io.Discard, io.Discard)
	if err != nil {
		code := "create-execution-failed"
		if reply.ErrorCode != "" {
			code = reply.ErrorCode
		}
		err = &ProcessCreateFailure{Code: code, Owner: reply.Owner, Binding: reply.Binding, Cause: errors.Join(err, ctx.Err())}
	}
	return reply, err
}

func processCreateSchemaBinding(binding processhost.Binding) coremetadata.ProcessBinding {
	return coremetadata.ProcessBinding{HostInstanceID: binding.Host, ProjectUID: binding.Project, WindowUID: binding.Window, AgentUID: binding.Agent, PaneUID: binding.Pane, Generation: binding.Generation, OperationID: binding.Operation}
}

// VerifyDetachedProcessCreateBinding owns the shared caller-side admission
// proof. A stale session or a second process Pane cannot borrow an activation.
func VerifyDetachedProcessCreateBinding(reg coremetadata.Registry, binding processhost.Binding, owner coremetadata.ProcessIdentity) error {
	b := processCreateSchemaBinding(binding)
	activation, _, current := reg.CurrentProcessActivation(b)
	agent, exists := reg.Agent(b.AgentUID)
	pane, present := reg.Pane(b.PaneUID)
	exact, ambiguous := ProcessAgentPane(reg, b.AgentUID)
	if !current || activation.HostProcess != owner || !exists || !present || ambiguous || exact == nil || exact.Metadata.UID != b.PaneUID || agent.Status.PaneRef != b.PaneUID || pane.Metadata.OwnerUID() != b.AgentUID || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != b {
		return processhost.ErrStale
	}
	return nil
}
func VerifyDetachedProcessCreate(load func() (coremetadata.Registry, error), ready ProcessDetachedReply, owner coremetadata.ProcessIdentity) error {
	if len(ready.Claim) != 0 || ready.Result == nil {
		return processhost.ErrStale
	}
	reg, err := load()
	if err != nil {
		return err
	}
	if err := VerifyDetachedProcessCreateBinding(reg, ready.Binding, owner); err != nil {
		return err
	}
	b := processCreateSchemaBinding(ready.Binding)
	if ready.Result.Agent.Metadata.UID != b.AgentUID || ready.Result.Pane.Metadata.UID != b.PaneUID || ready.Result.Pane.Status.ProcessSession == nil || ready.Result.Pane.Status.ProcessSession.Binding != b {
		return processhost.ErrStale
	}
	return nil
}

// Cleanup observes actual owner retirement and durable Wait or exact rollback.
// Sending TERM alone never establishes success and unrelated generations are
// never changed by the caller-side recovery observation.
func AwaitDetachedProcessCreateCleanup(load func() (coremetadata.Registry, error), owner coremetadata.ProcessIdentity, binding processhost.Binding) error {
	ctx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
	defer cancel()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for {
		reg, loadErr := load()
		if binding.Agent == "" && loadErr == nil {
			for _, pane := range reg.Panes {
				if activation := pane.Status.Activation.Process; activation != nil && activation.HostProcess == owner && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == activation.Binding {
					b := activation.Binding
					binding = processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
					break
				}
			}
		}
		gone := ProcessIdentityAbsent(owner, localipc.Process)
		settled := loadErr == nil && binding.Agent == ""
		if binding.Agent != "" && loadErr == nil {
			agent, exists := reg.Agent(binding.Agent)
			pane, present := reg.Pane(binding.Pane)
			settled = !exists && !present
			if exists && present && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == processCreateSchemaBinding(binding) {
				settled = agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
			}
		}
		if gone && settled {
			return nil
		}
		select {
		case <-ctx.Done():
			if binding.Agent != "" {
				return fmt.Errorf("process-detached-cleanup-unconfirmed: remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", binding.Agent, binding.Pane, binding.Agent, binding.Agent, ctx.Err())
			}
			return fmt.Errorf("process-detached-cleanup-unconfirmed: inspect exact owner pid %d start %s: %w", owner.PID, owner.Start, ctx.Err())
		case <-ticker.C:
		}
	}
}

// ProcessCreateErrorCode transports the existing typed refusal reason across
// the owned startup handshake without parsing free-form diagnostic text.
func ProcessCreateErrorCode(err error) string {
	var refusal *AgentCreateRefusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	if coremetadata.IsUsageError(err) {
		return "create-request-invalid"
	}
	return "create-execution-failed"
}
