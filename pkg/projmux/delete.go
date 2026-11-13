package projmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeAgentProtected           Code = mutations.CodeAgentProtected
	CodeDeleteKindUnsupported    Code = "delete-kind-unsupported"
	CodeDeleteProcessUnsupported Code = "delete-process-unsupported"
	CodeDeleteTransportRequired  Code = "delete-transport-required"
	CodeDeleteRuntimeRefused     Code = "delete-runtime-refused"
	CodeDeleteExecutionFailed    Code = "delete-execution-failed"
)

// Delete removes tmux Windows/Panes/Agents, one exact process Agent, and
// process Windows or Panes (stopping their running providers first). An
// Offline or Failed Agent of a process Window that has no Pane is deleted from
// the Registry only, exactly as the CLI does, when addressed by exact uid.
//
// A Pane or Agent plan that mixes tmux and process resources is refused with
// CodeDeleteProcessUnsupported; the refusal text is the CLI's own, byte for
// byte, because the same live preflight produces it. A Window plan that picks a
// tmux Window together with a process Window, or a tmux Window that holds a
// process Pane, is deleted like the CLI does: the shared executor first stops
// every running process Agent through its owner host, and refuses with the
// existing process delete code when a stop cannot be confirmed, leaving tmux
// and the Registry unchanged. A plan of process Windows alone, or of process
// Panes alone, is deleted as described above.
//
// DryRun writes nothing. Confirm is required unless the CLI's leaf Pane rule
// exempts the plan. Cancellation is effective before admission only; admitted
// execution and compensation finish regardless of cancellation. Partial kill
// failure may leave Registry resources whose runtime is gone; Committed=false
// does not imply that no runtime effects occurred.
// The SDK has no current Pane and never queues a deferred self-kill.
func (c *Client) Delete(ctx context.Context, req core.DeleteRequest) (core.DeleteResult, error) {
	if req.Kind == core.KindAgent {
		return c.deleteWithRunner(ctx, req, deleteAgentTmuxRunner{frozenTurnRunner{c}})
	}
	return c.deleteWithRunner(ctx, req, tmux.ExecRunner{})
}

// deleteAgentTmuxRunner retains captured execution while preserving the typed
// failure evidence used by deletion's exact-absence observers. Other frozen
// runner consumers keep their existing error semantics.
type deleteAgentTmuxRunner struct{ frozenTurnRunner }

func (r deleteAgentTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := r.frozenTurnRunner.Run(ctx, name, args...)
	if err == nil {
		return output, nil
	}
	kind := tmux.CommandFailureRunner
	var exitErr *exec.ExitError
	var execErr *exec.Error
	switch {
	case errors.As(err, &exitErr):
		kind = tmux.CommandFailureExit
	case errors.As(err, &execErr):
		kind = tmux.CommandFailureNotFound
	case errors.Is(err, os.ErrPermission):
		kind = tmux.CommandFailurePermission
	}
	return output, &deleteAgentTmuxError{cause: err, failure: tmux.CommandFailure{
		Kind: kind, Stderr: strings.TrimSpace(string(output)),
	}}
}

type deleteAgentTmuxError struct {
	cause   error
	failure tmux.CommandFailure
}

func (e *deleteAgentTmuxError) Error() string {
	if e.failure.Stderr != "" {
		return fmt.Sprintf("%v: %s", e.cause, e.failure.Stderr)
	}
	return e.cause.Error()
}

func (e *deleteAgentTmuxError) Unwrap() error { return e.cause }

func (e *deleteAgentTmuxError) CommandFailure() tmux.CommandFailure { return e.failure }

func (c *Client) deleteWithRunner(ctx context.Context, req core.DeleteRequest, runner mutationruntime.TmuxCommandRunner) (core.DeleteResult, error) {
	return c.deleteOnTransport(ctx, req, "", runner)
}

// deleteOnTransport is deleteWithRunner with an internal logical tmux socket
// name. A non-empty socketName routes the delete the way the CLI's --socket
// <name> does and replaces the Client's explicit route; it is never part of
// DeleteRequest, so the public request type is unchanged.
func (c *Client) deleteOnTransport(ctx context.Context, req core.DeleteRequest, socketName string, runner mutationruntime.TmuxCommandRunner) (core.DeleteResult, error) {
	out := core.DeleteResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if req.Kind != core.KindWindow && req.Kind != core.KindPane && req.Kind != core.KindAgent {
		return out, &Refusal{Code: CodeDeleteKindUnsupported, message: fmt.Sprintf("DeleteRequest.Kind %q is unsupported", req.Kind)}
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "DeleteRequest.Ref must name one resource"}
	}
	query, err := deleteQuery(req)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.Kind(req.Kind), query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbDelete, Kind: metadata.Kind(req.Kind)}, query, resolution); err != nil {
		return out, resourceRefusal(err)
	}
	plan := mutationruntime.DeletePlan(mutations.BuildDeletePlan(reg, metadata.Kind(req.Kind), resolution))
	if err := mutations.ValidateDeleteProtection(reg, mutations.DeletePlan(plan)); err != nil {
		return out, deleteRefusal(CodeAgentProtected, err)
	}
	switch req.Kind {
	case core.KindWindow:
		plan.ExactUID = query.Windows[0].UID != ""
	case core.KindPane:
		plan.ExactUID = query.Panes[0].UID != ""
	case core.KindAgent:
		plan.ExactUID = query.Agents[0].UID != ""
	}
	if err := unregisterCopy(mutations.PreviewDelete(mutations.DeletePlan(plan)), &out.Plan); err != nil {
		return out, err
	}
	if mutationruntime.IsProcessAgentPlan(reg, plan) || mutationruntime.IsVirtualProcessPlan(reg, plan) {
		return c.deleteProcess(ctx, req, reg, plan, out)
	}
	// A Pane or Agent plan that mixes tmux and process resources is refused by
	// the live preflight below, with the CLI's own text. A Window plan is not:
	// the executor stops the running process Agents it holds first.
	socket := req.SocketPath
	if socket == "" && socketName == "" {
		socket = c.runtimeSocketPath
	}
	target, err := mutationruntime.ResolveDeleteTarget("delete "+strings.ToLower(string(req.Kind)), socketName, socket, nil)
	if err != nil {
		if mixed := deleteTmuxOnly(reg, plan, true); mixed != nil {
			return out, mixed
		}
		code := CodeDeleteTransportRequired
		var f *mutations.Failure
		if errors.As(err, &f) && f.Code == string(CodeRuntimeRequestInvalid) {
			code = CodeRuntimeRequestInvalid
		}
		message := err.Error()
		if code == CodeRuntimeRequestInvalid {
			message = "DeleteRequest.SocketPath must be absolute"
		}
		if code == CodeDeleteTransportRequired {
			message = "DeleteRequest.SocketPath or Options.SocketPath is required"
		}
		return out, &Refusal{Code: code, message: message, cause: err}
	}
	// Getenv is intentionally nil: no caller Pane or inherited transport is read.
	windows := &mutationruntime.TmuxWindowDeleteRuntime{Runner: runner}
	panes := &mutationruntime.TmuxPaneDeleteRuntime{Runner: runner}
	var live mutationruntime.WindowLiveDeletePlan
	var paneLive mutationruntime.PaneLiveDeletePlan
	if req.Kind == core.KindWindow {
		windows.UseExactTarget(target)
		live, err = windows.Preflight(ctx, reg, plan)
	} else {
		panes.UseExactTarget(target)
		paneLive, err = panes.Preflight(ctx, reg, plan)
	}
	if err != nil {
		if deleteTmuxOnly(reg, plan, true) != nil {
			// The CLI refuses this plan with exactly this text.
			return out, &Refusal{Code: CodeDeleteProcessUnsupported, message: err.Error(), cause: err}
		}
		return out, deleteRefusal(CodeDeleteRuntimeRefused, err)
	}
	if err := deleteTmuxOnly(reg, plan, false); err != nil {
		return out, err
	}
	if err := unregisterCopy(live.Targets, &out.Windows); err != nil {
		return out, err
	}
	if err := unregisterCopy(paneLive.Targets, &out.Panes); err != nil {
		return out, err
	}
	if err := unregisterCopy(paneLive.RegistryOnly, &out.RegistryOnly); err != nil {
		return out, err
	}
	if req.Kind == core.KindWindow {
		// Like the CLI dry run, name the process Agents the Window plan stops.
		// A classification that cannot be read is refused by the executor.
		classifier := mutationruntime.NewProcessAgentDeleter(store.PathFor(c.paths.StateDir))
		var held []mutationruntime.ProcessDeleteTarget
		for _, uid := range mutationruntime.ProcessDeleteAgentUIDs(reg, plan) {
			if t, err := classifier.Classify(reg, uid); err == nil {
				held = append(held, t)
			}
		}
		out.Processes = projectDeleteProcesses(held)
	}
	if req.DryRun {
		return out, nil
	}
	if mutationruntime.NeedsDeleteConfirmation(plan, paneLive) && !req.Confirm {
		return out, &Refusal{Code: CodeConfirmationRequired, message: "DeleteRequest.Confirm is required; use DryRun to review the deletion plan first"}
	}
	s := mutations.NewStore(store.PathFor(c.paths.StateDir))
	var warnings bytes.Buffer
	e := mutationruntime.DeleteExecutor{Windows: windows, Panes: mutationruntime.PaneDeleteRuntime{Preflight: panes.Preflight, PrepareReplacements: panes.PrepareReplacements, RollbackReplacements: panes.RollbackReplacements, Kill: panes.Kill, KillAll: panes.KillAll}, Update: s.Update, Mutator: s.Mutator, Via: mutations.DeletionViaSDK, StateDir: func() (string, error) { return c.paths.StateDir, nil }, Warnings: &warnings, Allow: func(r metadata.Registry, p mutationruntime.DeletePlan) error { return deleteTmuxOnly(r, p, false) }}
	if req.Kind == core.KindWindow {
		e.Process = mutationruntime.NewProcessAgentDeleter(store.PathFor(c.paths.StateDir))
	}
	e.Mutate = func(kind metadata.Kind, uids []string, apply func(*metadata.Registry, metadata.Mutator) error) error {
		_, err := s.Update(func(r *metadata.Registry) error {
			for _, uid := range uids {
				if _, _, ok := resources.ResourceFor(*r, kind, uid); !ok {
					return fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)
				}
			}
			return apply(r, s.Mutator())
		})
		return err
	}
	result, err := e.Execute(ctx, "delete "+strings.ToLower(string(req.Kind)), plan, resolution, live, paneLive)
	out.Committed, out.OperationID, out.Notice = result.Committed, result.OperationID, strings.TrimSuffix(warnings.String(), "\n")
	if result.Committed {
		if e := unregisterCopy(result.Affected, &out.Plan.Affected); e != nil {
			return out, e
		}
		if e := unregisterCopy(result.Receipt, &out.Receipt); e != nil {
			return out, e
		}
		out.Processes = projectDeleteProcesses(result.Process)
	}
	return out, deleteRefusal(CodeDeleteExecutionFailed, err)
}
func deleteRefusal(code Code, err error) error {
	if err == nil {
		return nil
	}
	// A refused admission returns the caller's context error directly. An
	// admitted process retirement failure keeps the public typed SDK refusal,
	// even when its preserved cause contains a deadline/cancellation error.
	var processFailure *mutationruntime.ProcessDeleteError
	if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && !errors.As(err, &processFailure) {
		return err
	}
	var failure *mutations.Failure
	var protectedAgentUID string
	if errors.As(err, &failure) && failure.Code == mutations.CodeAgentProtected {
		code = CodeAgentProtected
		protectedAgentUID = failure.AgentUID
	}
	var r *Refusal
	if errors.As(err, &r) {
		return err
	}
	return &Refusal{Code: code, AgentUID: protectedAgentUID, message: err.Error(), cause: err}
}

// deleteTmuxOnly refuses what reaches the tmux path with a process resource in
// it. Process-only plans were routed to deleteProcess before this runs, so what
// is left mixes tmux and process resources, or is an Agent with no process Pane.
// Window plans are the exception below.
//
// strict also refuses an Agent of a process Window. The caller sets it to
// classify a plan, or to refuse before the live preflight can run. Unset, such
// an Agent is left to the live preflight, which as in the CLI deletes it from
// the Registry only when it is Offline or Failed and addressed by exact uid.
func deleteTmuxOnly(reg metadata.Registry, plan mutationruntime.DeletePlan, strict bool) error {
	// A Window plan may hold process Windows and process Panes below tmux
	// Windows. The shared executor stops their running providers through the
	// owner hosts before it kills or deletes anything, as the CLI does.
	if plan.Kind == metadata.KindWindow {
		return nil
	}
	for _, t := range plan.Targets {
		uid := t.Match.UID
		if plan.Kind == metadata.KindPane {
			if p, ok := reg.Pane(uid); ok && p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
				return &Refusal{Code: CodeDeleteProcessUnsupported, message: fmt.Sprintf("Delete cannot remove process Pane uid:%s: the plan mixes tmux and process resources", uid)}
			}
		}
		if plan.Kind == metadata.KindAgent && strict {
			if a, ok := reg.Agent(uid); ok && reg.IsVirtualWindow(a.Metadata.OwnerUID()) {
				return &Refusal{Code: CodeDeleteProcessUnsupported, message: fmt.Sprintf("Delete cannot remove Agent uid:%s of process Window uid:%s: it has no process-only Pane to stop", uid, a.Metadata.OwnerUID())}
			}
		}
		if reg.IsVirtualWindow(uid) {
			return &Refusal{Code: CodeDeleteProcessUnsupported, message: fmt.Sprintf("Delete cannot remove process Window uid:%s: the plan mixes tmux and process resources", uid)}
		}
		for _, d := range t.Descendants {
			if d.Kind == metadata.KindPane {
				if p, ok := reg.Pane(d.UID); ok && p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
					return &Refusal{Code: CodeDeleteProcessUnsupported, message: fmt.Sprintf("Delete cannot remove uid:%s: process descendant Pane uid:%s mixes tmux and process resources", uid, d.UID)}
				}
			}
		}
	}
	return nil
}
func deleteQuery(req core.DeleteRequest) (selector.Query, error) {
	return mutationQuery(req.Kind, req.Ref, req.Project, req.Window, "DeleteRequest")
}

func (c *Client) deleteProcess(ctx context.Context, req core.DeleteRequest, reg metadata.Registry, plan mutationruntime.DeletePlan, out core.DeleteResult) (core.DeleteResult, error) {
	// Like the CLI, only an Agent plan is limited to one exact uid; a process
	// Window or Pane is admitted by its virtual-Window shape.
	if plan.Kind == metadata.KindAgent && (!plan.ExactUID || len(plan.Targets) != 1) {
		return out, deleteRefusal(CodeDeleteRuntimeRefused, mutationruntime.ProcessDeleteRefusal("process-delete-refused", "", "delete agent requires one exact uid: Agent reference"))
	}
	if req.SocketPath != "" && !filepath.IsAbs(req.SocketPath) {
		return out, &Refusal{Code: CodeRuntimeRequestInvalid, message: "DeleteRequest.SocketPath must be absolute"}
	}
	executor := mutationruntime.NewProcessAgentDeleter(store.PathFor(c.paths.StateDir))
	targets, err := executor.ClassifyPlan(reg, plan)
	if err != nil {
		return out, deleteRefusal(CodeDeleteRuntimeRefused, err)
	}
	out.Processes = projectDeleteProcesses(targets)
	if req.DryRun {
		return out, nil
	}
	if plan.NeedsConfirmation() && !req.Confirm {
		return out, &Refusal{Code: CodeConfirmationRequired, message: "DeleteRequest.Confirm is required; use DryRun to review the deletion plan first"}
	}
	operation, err := (&mutationruntime.DeleteExecutor{}).MintOperationID()
	if err != nil {
		return out, err
	}
	outcome, err := executor.Execute(ctx, plan, targets, nil)
	if err != nil {
		return out, deleteRefusal(CodeDeleteExecutionFailed, err)
	}
	out.Committed = true
	out.Processes = projectDeleteProcesses(outcome.Targets)
	out.OperationID = operation
	if err = unregisterCopy(outcome.Affected, &out.Plan.Affected); err != nil {
		return out, err
	}
	r := mutationruntime.ChildDeleteReceipt(plan.Kind, plan, mutationruntime.WindowLiveDeletePlan{}, mutationruntime.PaneLiveDeletePlan{}, false)
	for _, t := range outcome.Targets {
		if t.Runtime == "stopped" {
			r.Effects.Runtime = receipt.RuntimeStopped
		}
	}
	if err = unregisterCopy(r, &out.Receipt); err != nil {
		return out, err
	}
	var warnings bytes.Buffer
	mutations.RecordDeletion(func() (string, error) { return c.paths.StateDir, nil }, mutations.NewDeletionRecord(mutationruntime.ChildDeletionOperation(plan.Kind), operation, mutations.DeletionViaSDK, outcome.Actor, mutations.DeletionTargetsOf(mutations.DeletePlan(plan)), outcome.Affected), &warnings)
	for _, notice := range outcome.Notices {
		warnings.WriteString(notice + "\n")
	}
	out.Notice = strings.TrimSuffix(warnings.String(), "\n")
	return out, nil
}

// projectDeleteProcesses copies only the CLI-visible fields of the classified
// process Agents. Classification also contains private owner credentials, so
// the internal target is never serialized wholesale.
func projectDeleteProcesses(targets []mutationruntime.ProcessDeleteTarget) []core.DeleteProcess {
	processes := make([]core.DeleteProcess, len(targets))
	for i, t := range targets {
		processes[i] = core.DeleteProcess{AgentUID: t.Agent, WindowUID: t.Window,
			RootKind: core.Kind(t.RootKind), RootUID: t.RootUID,
			Runtime: t.Runtime, Evidence: t.Evidence}
		// The CLI only prints a PID for a running provider owner, never
		// for the owner of a child-free deferred claim.
		if t.Runtime == "running" {
			processes[i].HostPID = t.HostPID
		}
	}
	return processes
}
