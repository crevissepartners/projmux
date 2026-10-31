package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
)

const (
	processDeleteRuntimeOffline = "offline"
	processDeleteRuntimeUnknown = "unknown"
	processDeleteRuntimeRunning = "running"
	processDeleteRuntimeStopped = "stopped"
)

// Refusal tokens. process-host-unavailable is the existing control token and
// is reused when a live owner cannot be reached.
const (
	processDeleteRefusedToken         = "process-delete-refused"
	processDeleteStopUnconfirmedToken = "process-delete-stop-unconfirmed"
	processDeleteHostUnavailableToken = "process-host-unavailable"
)

// ProcessDeleteError is the typed refusal of a process Agent delete. Its text
// is exactly what the CLI prints; Token and Runtime let an in-process caller
// branch without parsing it. A refusal leaves the Registry unchanged.
type ProcessDeleteError struct {
	Token   string
	Runtime string
	Cause   error
	message string
}

func (e *ProcessDeleteError) Error() string {
	if e.message != "" {
		return e.message
	}
	return e.Cause.Error()
}
func (e *ProcessDeleteError) Unwrap() error { return e.Cause }

func ProcessDeleteRefusal(token, runtime, format string, args ...any) *ProcessDeleteError {
	return &ProcessDeleteError{Token: token, Runtime: runtime, Cause: fmt.Errorf(token+": "+format, args...)}
}

type ProcessAgentDeleter struct {
	RegistryPath                   string
	Load                           func() (coremetadata.Registry, error)
	Mutate                         func(coremetadata.Kind, []string, func(*coremetadata.Registry, coremetadata.Mutator) error) error
	Alive                          func(coremetadata.ProcessIdentity) bool
	Stop                           func(context.Context, coremetadata.Registry, coremetadata.Agent) error
	WaitLimit, StopAdmission, Poll time.Duration
	ReadIdentity                   ProcessIdentityReader
	MapError                       func(error) error
}

func (d *ProcessAgentDeleter) mapError(err error) error {
	if d.MapError != nil {
		return d.MapError(err)
	}
	return err
}
func buildProcessDeletePlan(reg coremetadata.Registry, kind coremetadata.Kind, r selector.Resolution) DeletePlan {
	return DeletePlan(mutations.BuildDeletePlan(reg, kind, r))
}

// ProcessDeleteTarget is one classified Agent.
type ProcessDeleteTarget struct {
	Agent, Pane, Window string
	RootKind            coremetadata.Kind
	RootUID             string
	Runtime             string
	Evidence            string
	HostPID             int
	Binding             coremetadata.ProcessBinding
	Activation          *coremetadata.ProcessActivation
	Claim               bool
	ClaimNonce          string
	ClaimOwner          coremetadata.ProcessIdentity
}

// IsProcessAgentPlan reports whether every target is an Agent whose managed
// Panes are all process runtime Panes. A plan that touches any tmux Pane, or
// an Agent with no managed Pane, keeps the tmux delete path unchanged.
func IsProcessAgentPlan(registry coremetadata.Registry, plan DeletePlan) bool {
	if plan.Kind != coremetadata.KindAgent || len(plan.Targets) == 0 {
		return false
	}
	for _, target := range plan.Targets {
		panes := 0
		for _, descendant := range target.Descendants {
			if descendant.Kind != coremetadata.KindPane {
				continue
			}
			pane, ok := registry.Pane(descendant.UID)
			if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
				return false
			}
			panes++
		}
		if panes == 0 {
			return false
		}
	}
	return true
}

// IsVirtualProcessPlan reports whether every target is a virtual Window or a
// process Pane whose descendants are all process Panes. It admits plans that
// remove process Windows and Panes through their owner hosts, never tmux.
func IsVirtualProcessPlan(reg coremetadata.Registry, plan DeletePlan) bool {
	if len(plan.Targets) == 0 {
		return false
	}
	for _, target := range plan.Targets {
		switch plan.Kind {
		case coremetadata.KindWindow:
			if !reg.IsVirtualWindow(target.Match.UID) {
				return false
			}
			// Retained tmux rows of released Agents have no eligible anchor.
			// Their removal is Registry-only; living tmux Panes are never closed.
			continue
		case coremetadata.KindPane:
			pane, ok := reg.Pane(target.Match.UID)
			if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
				return false
			}
		default:
			return false
		}
		for _, child := range target.Descendants {
			if child.Kind == coremetadata.KindPane {
				pane, ok := reg.Pane(child.UID)
				if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
					return false
				}
			}
		}
	}
	return true
}

// Classify judges one Agent from the Registry and kernel birth evidence.
// It never probes under the Registry lock; callers revalidate the result with
// ProcessDeleteSignature inside the transaction.
func (d *ProcessAgentDeleter) Classify(registry coremetadata.Registry, agentUID string) (ProcessDeleteTarget, error) {
	agent, ok := registry.Agent(agentUID)
	if !ok {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "", "registry Agent uid %q disappeared; nothing was changed", agentUID)
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "", "registry Agent uid %q has no exact owning Window; nothing was changed", agentUID)
	}
	root, err := DeleteRootForWindow(registry, *window)
	if err != nil {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "", "%v; nothing was changed", err)
	}
	panes := registry.PanesOf(agentUID)
	if len(panes) != 1 || panes[0].Metadata.UID != agent.Status.PaneRef || panes[0].Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "",
			"registry Agent uid %q does not own exactly its current process Pane; nothing was changed", agentUID)
	}
	pane := panes[0]
	target := ProcessDeleteTarget{Agent: agentUID, Pane: pane.Metadata.UID, Window: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}
	if pane.Status.ProcessSession != nil {
		target.Binding = pane.Status.ProcessSession.Binding
	}
	activation := pane.Status.Activation
	if !activation.IsZero() && (activation.Process == nil || activation.Process.Binding.AgentUID != agentUID || activation.Process.Binding.PaneUID != pane.Metadata.UID || activation.Process.Binding.WindowUID != window.Metadata.UID || activation.Generation != activation.Process.Binding.Generation || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != activation.Process.Binding) {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "", "process Pane uid %q carries a non-process activation; nothing was changed", pane.Metadata.UID)
	}
	if d.RegistryPath != "" && activation.IsZero() && pane.Status.ProcessSession != nil {
		claim, err := ReadDeferredClaim(DeferredClaimPath(d.RegistryPath, agentUID))
		if err != nil {
			if errors.Is(err, processhost.ErrResumeRefused) || errors.Is(err, processhost.ErrStale) {
				return target, processDeleteEvidenceFailure(target, err)
			}
			return target, processDeleteUnobservedFailure(target, err)
		}
		if claim.Nonce != "" {
			if !DeferredClaimMatches(claim, registry, agentUID) || !claim.Detached {
				return target, processDeleteEvidenceFailure(target, fmt.Errorf("expected claim binding %+v, observed %+v: %w", pane.Status.ProcessSession.Binding, claim.Binding, processhost.ErrStale))
			}
			target.Claim = true
			target.ClaimNonce = claim.Nonce
			target.ClaimOwner = claim.Process
			target.Binding = claim.Binding
			target.HostPID = claim.Process.PID
		}
		witness, err := ReadProcessRetirement(d.RegistryPath, pane.Status.ProcessSession.Binding, pane.Status.LastTermination)
		if err == nil {
			if absent, readErr := d.activationRetired(witness); !absent {
				target.Runtime = processDeleteRuntimeOffline
				return target, processDeleteUnconfirmedFailure(target, fmt.Errorf("expected kernel retirement of owner %+v child %+v: %w", witness.HostProcess, witness.Child, readErr))
			}
			target.Activation = &witness
		} else if !errors.Is(err, os.ErrNotExist) {
			if errors.Is(err, processhost.ErrStale) {
				return target, processDeleteEvidenceFailure(target, fmt.Errorf("expected retirement binding %+v and Wait %+v: %w", pane.Status.ProcessSession.Binding, pane.Status.LastTermination, err))
			}
			return target, processDeleteUnobservedFailure(target, err)
		}
	}

	if activation.IsZero() {
		record := pane.Status.ProcessSession
		receipt := pane.Status.LastTermination
		if agent.Status.Phase == coremetadata.PhaseOffline && record != nil &&
			coremetadata.MatchesProcessWait(record.Binding, receipt) && coremetadata.SameProcessWait(receipt, agent.Status.LastTermination) {
			target.Runtime, target.Binding = processDeleteRuntimeOffline, record.Binding
			target.Evidence = ProcessWaitEvidence(*receipt)
			return target, nil
		}
		target.Runtime, target.Evidence = processDeleteRuntimeUnknown, "activation-absent"
		return target, nil
	}
	process := activation.Process
	if activation.Kind != coremetadata.RuntimeProcess || process == nil {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, "",
			"process Pane uid %q carries a non-process activation; nothing was changed", pane.Metadata.UID)
	}
	target.Binding = process.Binding
	target.Activation = process
	if d.Alive(process.HostProcess) {
		target.Runtime, target.HostPID = processDeleteRuntimeRunning, process.HostProcess.PID
		target.Evidence = fmt.Sprintf("owner-host-pid=%d", process.HostProcess.PID)
		return target, nil
	}
	if d.Alive(process.Child) {
		return ProcessDeleteTarget{}, ProcessDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeUnknown,
			"process Agent uid %q lost its owner host but child pid %d is still alive; its supervisor ends it within the stop grace, retry then; nothing was changed",
			agentUID, process.Child.PID)
	}
	if d.RegistryPath != "" {
		if absent, readErr := d.activationRetired(*process); !absent {
			return target, processDeleteUnconfirmedFailure(target, fmt.Errorf("expected kernel retirement of owner %+v child %+v: %w", process.HostProcess, process.Child, readErr))
		}
	}
	target.Runtime, target.Evidence = processDeleteRuntimeUnknown, "owner-host-and-child-absent"
	return target, nil
}

func ProcessWaitEvidence(receipt coremetadata.TerminationEvidence) string {
	evidence := "wait:" + string(receipt.Classification)
	if receipt.ExitCode != nil {
		evidence += fmt.Sprintf(" exit=%d", *receipt.ExitCode)
	}
	if receipt.Signal != "" {
		evidence += " signal=" + receipt.Signal
	}
	return evidence
}

// ProcessDeleteSignature signs the Registry facts a classification read. The
// locked transaction must reproduce it byte for byte before deleting.
func ProcessDeleteSignature(registry coremetadata.Registry, plan DeletePlan) string {
	var b strings.Builder
	b.WriteString(plan.Signature())
	if plan.Kind == coremetadata.KindWindow {
		for _, target := range plan.Targets {
			if window, ok := registry.Window(target.Match.UID); ok {
				fmt.Fprintf(&b, "|window=%q,anchor=%q,shell=%q,session=%q,runtime=%q", window.Metadata.UID,
					window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef, window.Status.RuntimeSessionID, window.Status.RuntimeID)
			}
		}
	}
	for _, uid := range ProcessDeleteAgentUIDs(registry, plan) {
		agent, ok := registry.Agent(uid)
		if !ok {
			fmt.Fprintf(&b, "|agent=%q:absent", uid)
			continue
		}
		fmt.Fprintf(&b, "|agent=%q,owner=%q,phase=%q,pane-ref=%q", agent.Metadata.UID, agent.Metadata.OwnerUID(), agent.Status.Phase, agent.Status.PaneRef)
		for _, pane := range registry.PanesOf(agent.Metadata.UID) {
			fmt.Fprintf(&b, ",pane=%q,runtime=%q,activation=%+v,termination=%s", pane.Metadata.UID, pane.Spec.Runtime.EffectiveKind(),
				pane.Status.Activation.Process, ProcessTerminationSignature(pane.Status.LastTermination))
			if pane.Status.ProcessSession != nil {
				fmt.Fprintf(&b, ",Binding=%+v", pane.Status.ProcessSession.Binding)
			}
		}
	}
	return b.String()
}

func ProcessTerminationSignature(receipt *coremetadata.TerminationEvidence) string {
	if receipt == nil {
		return "none"
	}
	code := "nil"
	if receipt.ExitCode != nil {
		code = fmt.Sprint(*receipt.ExitCode)
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", receipt.Source, receipt.Classification, receipt.Generation, code, receipt.Signal, receipt.ObservedAt.UTC().Format(time.RFC3339Nano))
}

func (d *ProcessAgentDeleter) ClassifyPlan(registry coremetadata.Registry, plan DeletePlan) ([]ProcessDeleteTarget, error) {
	if err := mutations.ValidateDeleteTargets(registry, plan.Kind, plan.Uids()); err != nil {
		return nil, err
	}
	return d.classifyAdmittedPlan(registry, plan)
}
func (d *ProcessAgentDeleter) classifyAdmittedPlan(registry coremetadata.Registry, plan DeletePlan) ([]ProcessDeleteTarget, error) {
	if plan.Kind == coremetadata.KindWindow {
		for _, target := range plan.Targets {
			if !registry.IsVirtualWindow(target.Match.UID) {
				return nil, ProcessDeleteRefusal(processDeleteRefusedToken, "", "Window uid %q is no longer virtual; nothing was deleted", target.Match.UID)
			}
		}
	}
	Targets := make([]ProcessDeleteTarget, 0, len(plan.Targets))
	for _, uid := range ProcessDeleteAgentUIDs(registry, plan) {
		classified, err := d.Classify(registry, uid)
		if err != nil {
			return nil, err
		}
		Targets = append(Targets, classified)
	}
	return Targets, nil
}

// ProcessDeleteOutcome is a committed delete.
type ProcessDeleteOutcome struct {
	Targets  []ProcessDeleteTarget
	Affected []mutations.DeletionAffected
	Actor    mutations.DeletionActor
	// Notices report cleanup that failed after the deletion committed.
	Notices []string
}

// Execute stops every running target through its owner, waits for the exact
// retirement, reclassifies outside the lock, and deletes the whole plan in one
// transaction that must reproduce the reclassified Registry evidence.
func (d *ProcessAgentDeleter) Execute(ctx context.Context, plan DeletePlan, Targets []ProcessDeleteTarget, Actor func(*coremetadata.Registry) mutations.DeletionActor) (ProcessDeleteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ProcessDeleteOutcome{}, err
	}
	registryBeforeStop, err := d.Load()
	if err != nil {
		return ProcessDeleteOutcome{}, err
	}
	if err := mutations.ValidateDeleteTargets(registryBeforeStop, plan.Kind, plan.Uids()); err != nil {
		return ProcessDeleteOutcome{}, err
	}
	ctx = context.WithValue(ctx, ProcessDeleteAdmissionKey{}, ProcessDeleteAdmission{Kind: plan.Kind, UIDs: plan.Uids(), Signature: plan.Signature()})
	expected := map[string]ProcessDeleteTarget{}
	for _, t := range Targets {
		expected[t.Agent] = t
	}
	stopped := map[string]bool{}
	claims := map[string]bool{}
	for _, target := range Targets {
		if target.Runtime != processDeleteRuntimeRunning && !target.Claim {
			continue
		}
		if err := d.StopAndWait(ctx, target); err != nil {
			return ProcessDeleteOutcome{}, err
		}
		stopped[target.Agent] = true
		claims[target.Agent] = target.Claim
		ctx = context.WithoutCancel(ctx)
		admission := ctx.Value(ProcessDeleteAdmissionKey{}).(ProcessDeleteAdmission)
		admission.Admitted = true
		ctx = context.WithValue(ctx, ProcessDeleteAdmissionKey{}, admission)
	}
	// Claim reservation and birth take this same guard before publishing their
	// Registry binding. Hold it across final classification and deletion so a
	// newly admitted claimant cannot escape an inactive Registry-only cleanup.
	guardContext := ctx
	if len(stopped) > 0 {
		var cancelGuards context.CancelFunc
		guardContext, cancelGuards = context.WithTimeout(context.WithoutCancel(ctx), d.WaitLimit)
		defer cancelGuards()
	}
	releaseGuards, err := d.lockDeleteClaims(guardContext, Targets)
	if err != nil {
		return ProcessDeleteOutcome{}, err
	}
	defer releaseGuards()
	registry, err := d.Load()
	if err != nil {
		return ProcessDeleteOutcome{}, d.mapError(err)
	}
	if current := buildProcessDeletePlan(registry, plan.Kind, processResolutionOf(plan)).Signature(); current != plan.Signature() {
		return ProcessDeleteOutcome{}, ProcessDeleteRefusal(processDeleteRefusedToken, "", "delete agent: the cascade plan changed before deletion; nothing was deleted")
	}
	final, err := d.classifyAdmittedPlan(registry, plan)
	if err != nil {
		return ProcessDeleteOutcome{}, err
	}
	for i := range final {
		original, found := expected[final[i].Agent]
		if !found || original.Pane != final[i].Pane || original.Window != final[i].Window || original.RootKind != final[i].RootKind || original.RootUID != final[i].RootUID || original.Binding != final[i].Binding {
			return ProcessDeleteOutcome{}, processDeleteEvidenceFailure(original, fmt.Errorf("expected binding %+v, observed %+v; prior delete-stop retired=%t: %w", original.Binding, final[i].Binding, stopped[final[i].Agent], processhost.ErrStale))
		}
		if final[i].Claim {
			return ProcessDeleteOutcome{}, processDeleteEvidenceFailure(final[i], fmt.Errorf("expected released claim before Registry deletion; an exact claim is still present: %w", processhost.ErrStale))
		}
		if final[i].Runtime == processDeleteRuntimeRunning {
			return ProcessDeleteOutcome{}, ProcessDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeRunning,
				"process Agent uid %q became live under owner host pid %d before deletion; nothing was deleted", final[i].Agent, final[i].HostPID)
		}
		if stopped[final[i].Agent] && !claims[final[i].Agent] {
			if final[i].Runtime != processDeleteRuntimeOffline && !claims[final[i].Agent] {
				return ProcessDeleteOutcome{}, ProcessDeleteRefusal(processDeleteStopUnconfirmedToken, final[i].Runtime,
					"process Agent uid %q did not keep its exact Wait receipt after Stop; nothing was deleted", final[i].Agent)
			}
			final[i].Runtime = processDeleteRuntimeStopped
		}
	}
	approved := ProcessDeleteSignature(registry, plan)
	outcome := ProcessDeleteOutcome{Targets: final}
	err = d.Mutate(plan.Kind, plan.Uids(), func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if err := mutations.ValidateDeleteTargets(*working, plan.Kind, plan.Uids()); len(stopped) == 0 && err != nil {
			return err
		}
		if err := ctx.Err(); len(stopped) == 0 && err != nil {
			return err
		}
		if current := buildProcessDeletePlan(*working, plan.Kind, processResolutionOf(plan)).Signature(); current != plan.Signature() {
			return ProcessDeleteRefusal(processDeleteRefusedToken, "", "the cascade plan changed between preflight and deletion; nothing was deleted")
		}
		if ProcessDeleteSignature(*working, plan) != approved {
			return ProcessDeleteRefusal(processDeleteRefusedToken, "", "delete agent: process Agent evidence changed between preflight and deletion; nothing was deleted")
		}
		if Actor != nil {
			outcome.Actor = Actor(working)
		}
		candidate := working.Clone()
		for _, uid := range plan.Uids() {
			if err := DeleteResource(&candidate, mutator, plan.Kind, uid); err != nil {
				return err
			}
		}
		if err := candidate.Validate(); err != nil {
			return err
		}
		outcome.Affected = mutations.DeletionAffectedBetween(*working, candidate)
		*working = candidate
		return nil
	})
	if err != nil {
		var refusal *ProcessDeleteError
		if errors.As(err, &refusal) {
			return ProcessDeleteOutcome{}, refusal
		}
		return ProcessDeleteOutcome{}, err
	}
	// The Registry no longer names these Agents. Their frozen reservation launch
	// goes with them, under the guard still held. A failed removal leaves a file
	// that names no Agent and cannot start anything; it is reported, not undone.
	for _, target := range final {
		if err := RemoveCreatedLaunch(d.RegistryPath, target.Agent); err != nil {
			outcome.Notices = append(outcome.Notices, fmt.Sprintf("reservation launch of agent uid:%s was not removed: %v", target.Agent, err))
		}
	}
	return outcome, nil
}

// StopAndWait asks the owner to Stop and waits, by condition, for the exact
// Wait receipt of the same Binding and the retired activation.
func (d *ProcessAgentDeleter) StopAndWait(ctx context.Context, target ProcessDeleteTarget) error {
	ctx, cancel := context.WithTimeout(ctx, d.WaitLimit)
	defer cancel()
	// Cancellation is checked at admission. An admitted owner request and its
	// retirement must finish even if the caller disappears before the reply.
	execution, cancelExecution := context.WithTimeout(context.WithoutCancel(ctx), d.WaitLimit)
	defer cancelExecution()
	admissionDeadline := time.Now().Add(d.StopAdmission)
	var err error
	for {
		registry, loadErr := d.Load()
		if loadErr != nil {
			return d.mapError(loadErr)
		}
		agent, ok := registry.Agent(target.Agent)
		if !ok {
			return ProcessDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeRunning, "registry Agent uid %q disappeared before Stop; nothing was changed", target.Agent)
		}
		pane, paneOK := registry.Pane(target.Pane)
		if !paneOK || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != target.Binding || agent.Status.PaneRef != target.Pane {
			return processDeleteEvidenceFailure(target, fmt.Errorf("expected binding %+v no longer current: %w", target.Binding, processhost.ErrStale))
		}
		admission, ok := ctx.Value(ProcessDeleteAdmissionKey{}).(ProcessDeleteAdmission)
		if !ok {
			plan := buildProcessDeletePlan(registry, coremetadata.KindAgent, selector.Resolution{Kind: coremetadata.KindAgent, Matches: []selector.Match{{Kind: coremetadata.KindAgent, UID: target.Agent}}})
			admission = ProcessDeleteAdmission{Kind: plan.Kind, UIDs: plan.Uids(), Signature: plan.Signature()}
			ctx = context.WithValue(ctx, ProcessDeleteAdmissionKey{}, admission)
		}
		if err := mutations.ValidateDeleteTargets(registry, admission.Kind, admission.UIDs); !admission.Admitted && err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if target.Activation != nil && pane.Status.Activation.Process != nil && !reflect.DeepEqual(target.Activation, pane.Status.Activation.Process) {
			return processDeleteEvidenceFailure(target, fmt.Errorf("expected activation %+v differs from current %+v: %w", target.Activation, pane.Status.Activation.Process, processhost.ErrStale))
		}
		if target.Activation == nil && pane.Status.Activation.Process != nil {
			target.Activation = pane.Status.Activation.Process
		}
		if target.Claim {
			stopper := ProcessAgentStopper{RegistryPath: d.RegistryPath, Load: d.Load, ExpectedClaimNonce: target.ClaimNonce, ExpectedClaimOwner: target.ClaimOwner, ClaimStopAdmission: func(_ coremetadata.Registry, apply func() error) error {
				return AdmitProcessDeleteStop(d.RegistryPath, "delete-stop", mustProcessDeleteAdmission(admission), processhost.Authority{Binding: processHostBinding(target.Binding)}, apply)
			}}
			err = stopper.StopExact(ctx, registry, agent.Clone(), pane.Clone())
			if err == nil && !stopper.ClaimReleased {
				return processDeleteEvidenceFailure(target, processhost.ErrStale)
			}
			if err != nil {
				var protected *mutations.Failure
				if errors.As(err, &protected) && protected.Code == mutations.CodeAgentProtected {
					return err
				}
				var retirement *ProcessStopFailure
				if !errors.As(err, &retirement) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					return err
				}
				return processDeleteEvidenceFailure(target, err)
			}
			return nil
		}
		if d.Stop != nil {
			err = d.Stop(context.WithValue(execution, ProcessDeleteAdmissionKey{}, admission), registry, *agent)
		} else {
			err = CallProcessAgentDeleteStop(execution, d.RegistryPath, registry, *agent, admission)
		}
		if err == nil || !errors.Is(err, processhost.ErrStale) || !time.Now().Before(admissionDeadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.Poll):
		}
	}
	if err != nil {
		var protection *mutations.Failure
		if errors.As(err, &protection) && protection.Code == mutations.CodeAgentProtected {
			return err
		}
		if strings.HasPrefix(err.Error(), processDeleteHostUnavailableToken+":") {
			return &ProcessDeleteError{Token: processDeleteHostUnavailableToken, Runtime: processDeleteRuntimeRunning, Cause: err}
		}
		return ProcessDeleteRefusal(processDeleteHostUnavailableToken, processDeleteRuntimeRunning,
			"owner host pid %d of process Agent uid %q did not accept Stop: %w; nothing was changed", target.HostPID, target.Agent, err)
	}
	ctx = execution
	ticker := time.NewTicker(d.Poll)
	defer ticker.Stop()
	var retirementError error
	for {
		registry, err := d.Load()
		if err == nil && ProcessWaitRetired(registry, target) && target.Activation != nil {
			var absent bool
			absent, retirementError = d.activationRetired(*target.Activation)
			if absent {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			refused := ProcessDeleteRefusal(processDeleteStopUnconfirmedToken, processDeleteRuntimeRunning,
				"owner host pid %d accepted Stop for process Agent uid %q but no exact Wait receipt was recorded within %s; the Registry is unchanged, retry once the owner exits",
				target.HostPID, target.Agent, d.WaitLimit)
			refused.message = refused.Error()
			refused.Cause = errors.Join(refused.Cause, ctx.Err(), retirementError)
			return refused
		case <-ticker.C:
		}
	}
}

func ProcessWaitRetired(registry coremetadata.Registry, target ProcessDeleteTarget) bool {
	pane, ok := registry.Pane(target.Pane)
	agent, agentOK := registry.Agent(target.Agent)
	if !ok || !agentOK || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != target.Binding {
		return false
	}
	return agent.Status.Phase == coremetadata.PhaseOffline &&
		coremetadata.MatchesProcessWait(target.Binding, pane.Status.LastTermination) &&
		coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
}

func ProcessDeleteAgentUIDs(reg coremetadata.Registry, plan DeletePlan) []string {
	var out []string
	add := func(uid string) {
		if !slices.Contains(out, uid) {
			out = append(out, uid)
		}
	}
	for _, target := range plan.Targets {
		switch plan.Kind {
		case coremetadata.KindAgent:
			add(target.Match.UID)
		case coremetadata.KindPane:
			pane, ok := reg.Pane(target.Match.UID)
			if ok && pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
				add(pane.Metadata.OwnerUID())
			}
		case coremetadata.KindWindow:
			for _, agent := range reg.AgentsOf(target.Match.UID) {
				for _, pane := range reg.PanesOf(agent.Metadata.UID) {
					if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
						add(agent.Metadata.UID)
						break
					}
				}
			}
		}
	}
	return out
}

func processResolutionOf(plan DeletePlan) selector.Resolution {
	r := selector.Resolution{Kind: plan.Kind}
	for _, t := range plan.Targets {
		r.Matches = append(r.Matches, t.Match)
	}
	return r
}

func (d *ProcessAgentDeleter) identityReader() ProcessIdentityReader {
	if d.ReadIdentity != nil {
		return d.ReadIdentity
	}
	return localipc.Process
}
func NewProcessAgentDeleter(path string) *ProcessAgentDeleter {
	storage := intmetadata.NewStore(path)
	m := mutations.NewStore(path)
	limits := processhost.DefaultLimits()
	return &ProcessAgentDeleter{RegistryPath: path, Load: storage.LoadReadOnly, Alive: func(b coremetadata.ProcessIdentity) bool {
		r, _, e := localipc.Process(b.PID)
		return b.Valid() && e == nil && r == b
	}, WaitLimit: 3*limits.Grace + 2*limits.Write, StopAdmission: 2 * time.Second, Poll: 25 * time.Millisecond, Mutate: func(kind coremetadata.Kind, uids []string, apply func(*coremetadata.Registry, coremetadata.Mutator) error) error {
		_, err := m.Update(func(r *coremetadata.Registry) error { return apply(r, m.Mutator()) })
		return err
	}}
}

// Keep the existing human refusal template while preserving exact private
// evidence and the original typed cause for in-process recovery inspection.
func processDeleteEvidenceFailure(t ProcessDeleteTarget, cause error) *ProcessDeleteError {
	return &ProcessDeleteError{Token: processDeleteRefusedToken, Runtime: t.Runtime, message: processDeleteRefusedToken + ": delete agent: process Agent evidence changed between preflight and deletion; nothing was deleted", Cause: fmt.Errorf("expected Agent uid:%s Pane uid:%s binding %+v: %w", t.Agent, t.Pane, t.Binding, cause)}
}
func processDeleteUnconfirmedFailure(t ProcessDeleteTarget, cause error) *ProcessDeleteError {
	return &ProcessDeleteError{Token: processDeleteStopUnconfirmedToken, Runtime: t.Runtime, message: fmt.Sprintf("%s: process Agent uid %q did not keep its exact Wait receipt after Stop; nothing was deleted", processDeleteStopUnconfirmedToken, t.Agent), Cause: cause}
}

func processDeleteUnobservedFailure(t ProcessDeleteTarget, cause error) *ProcessDeleteError {
	// A failed read is unavailable evidence, not proof of a changed or dead owner.
	return &ProcessDeleteError{Token: processDeleteHostUnavailableToken, Runtime: t.Runtime, Cause: fmt.Errorf("process-host-unavailable: %w", cause)}
}

func (d *ProcessAgentDeleter) activationRetired(a coremetadata.ProcessActivation) (bool, error) {
	reader := d.identityReader()
	var readError error
	absent := ProcessActivationAbsent(a, func(pid int) (coremetadata.ProcessIdentity, int, error) {
		identity, parent, err := reader(pid)
		if err != nil && !errors.Is(err, localipc.ErrProcessAbsent) {
			readError = errors.Join(readError, err)
		}
		return identity, parent, err
	})
	if !absent && readError == nil {
		readError = processhost.ErrBusy
	}
	return absent, readError
}

func (d *ProcessAgentDeleter) lockDeleteClaims(ctx context.Context, targets []ProcessDeleteTarget) (func(), error) {
	if d.RegistryPath == "" {
		return func() {}, nil
	}
	uids := make([]string, 0, len(targets))
	for _, t := range targets {
		if !slices.Contains(uids, t.Agent) {
			uids = append(uids, t.Agent)
		}
	}
	slices.Sort(uids)
	var release []func()
	unlock := func() {
		for i := len(release) - 1; i >= 0; i-- {
			release[i]()
		}
	}
	for _, uid := range uids {
		guard, err := LockDeferredClaimContext(ctx, DeferredClaimPath(d.RegistryPath, uid))
		if err != nil {
			unlock()
			return nil, err
		}
		release = append(release, guard)
	}
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
