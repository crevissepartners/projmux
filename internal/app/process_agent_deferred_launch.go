package app

import (
	"context"
	"encoding/json"
	"errors"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"maps"
	"os"
	"path/filepath"
	"reflect"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

type deferredLaunchRecord = mutationruntime.DeferredLaunchRecord
type deferredLaunchAttempt = mutationruntime.DeferredLaunchAttempt

func deferredRefused(detail string) error { return mutationruntime.DeferredRefused(detail) }

func (c *agentCommand) deferredStatePath(kind, uid string) string {
	return mutationruntime.DeferredStatePath(c.messagePaths.registryPath, kind, uid)
}

func readDeferredState(path string, value any) (bool, error) {
	return mutationruntime.ReadDeferredState(path, value)
}

func (c *agentCommand) readDeferredLaunch(uid string) (*deferredLaunchRecord, error) {
	return mutationruntime.ReadDeferredLaunch(c.messagePaths.registryPath, uid)
}

func deferredLaunchDigest(record *deferredLaunchRecord) string {
	return mutationruntime.DeferredLaunchDigest(record)
}

func deferredLaunchFiles(args []string) (map[string]string, error) {
	return mutationruntime.DeferredLaunchFiles(args)
}

func validateDeferredClaudePermissionMode(args []string) error {
	return mutationruntime.ValidateDeferredClaudePermissionMode(args)
}

func writeDeferredState(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 65536 || path == "" {
		return deferredRefused("deferred state exceeds its bound or has no store")
	}
	if err = localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".deferred-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDeferredDirectory(path)
}

func syncDeferredDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- private state parent.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func removeDeferredState(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDeferredDirectory(path)
}

// Called under the existing claim guard. Replay can finish only the exact
// prepared old->new CAS, never overwrite an unrelated recipe or conversation.
func (c *agentCommand) reconcileDeferredLaunch(ctx context.Context, record *deferredLaunchRecord, allowHostLost bool) error {
	if record == nil {
		return nil
	}
	if record.HostLost != nil && !allowHostLost {
		return deferredRefused("host-lost prepared launch requires explicit opt-in")
	}
	if record.Previous != nil {
		return c.reconcilePromptRelaunch(record)
	}
	if err := c.reconcileDeferredAttempt(record); err != nil {
		return err
	}
	_, err := c.rebind.create.store.update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pane, ambiguous := processResumePane(*reg, record.Agent)
		agent, found := reg.Agent(record.Agent)
		if record.HostLost != nil {
			candidate, ok := hostLostResumeCandidate(*reg, record.Agent, c.readProcessIdentity())
			if !ok || !reflect.DeepEqual(candidate.HostLost, record.HostLost) || !processResumeRecordEqual(&candidate.Record, &record.Retired) {
				return deferredRefused("host-lost prepared activation changed")
			}
		} else if ambiguous || !found || pane == nil || agent.Status.Phase != coremetadata.PhaseOffline || !processResumeRecordEqual(pane.Status.ProcessSession, &record.Retired) || (!coremetadata.MatchesProcessWait(record.Retired.Binding, pane.Status.LastTermination) && !coremetadata.ProcessHostLostRetired(record.Retired.Binding, *pane, *agent)) {
			return deferredRefused("prepared relaunch conversation changed")
		}
		if reflect.DeepEqual(agent.Spec, record.NewSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
			return nil
		}
		if !reflect.DeepEqual(agent.Spec, record.OldSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.OldAnnotations) {
			return deferredRefused("prepared relaunch recipe changed")
		}
		agent.Spec, agent.Metadata.Annotations = record.NewSpec, maps.Clone(record.NewAnnotations)
		return nil
	})
	return err
}

// Caller holds the claim guard. The witness is intent, never live authority.
func (c *agentCommand) prepareDeferredAttempt(candidate processResumeCandidate, binding processhost.Binding, record *deferredLaunchRecord) error {
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	sourceRecipe := record
	if record.Previous != nil {
		sourceRecipe = record.Previous
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(sourceRecipe) {
		return deferredRefused("launch changed before reservation")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	retired := found && !ambiguous && pane != nil && agent.Status.Phase == coremetadata.PhaseOffline && pane.Status.Activation.IsZero() &&
		((coremetadata.MatchesProcessWait(candidate.Record.Binding, pane.Status.LastTermination) && coremetadata.MatchesProcessWait(candidate.Record.Binding, agent.Status.LastTermination)) || coremetadata.ProcessHostLostRetired(candidate.Record.Binding, *pane, *agent))
	if candidate.HostLost != nil {
		retired = found && !ambiguous && pane != nil && reflect.DeepEqual(pane.Status.Activation.Process, candidate.HostLost)
	}
	if !retired || !sourceRecipe.Matches(candidate) || !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) || !reflect.DeepEqual(agent.Spec, sourceRecipe.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, sourceRecipe.NewAnnotations) {
		return deferredRefused("attempt source changed")
	}
	target, source := metadataProcessBinding(binding), candidate.Record.Binding
	if target.AgentUID != source.AgentUID || target.PaneUID != source.PaneUID || target.ProjectUID != source.ProjectUID || target.WindowUID != source.WindowUID || target.HostInstanceID == source.HostInstanceID || target.Generation == source.Generation || target.OperationID == source.OperationID {
		return deferredRefused("attempt requires exact owner chain and fresh target")
	}
	record.Attempt = &deferredLaunchAttempt{Source: *candidate.Record.Clone(), Target: target}
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), record)
}

// A failed attempt can readdress only its exact Wait. Registry locks are
// released before the attention CAS; no old cursor/control becomes authority.
func (c *agentCommand) reconcileDeferredAttempt(record *deferredLaunchRecord) error {
	a := record.Attempt
	if a == nil {
		return nil
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || !reflect.DeepEqual(agent.Spec, record.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
		return deferredRefused("attempt recipe changed")
	}
	s := pane.Status.ProcessSession
	if a.Target.HostInstanceID == a.Source.Binding.HostInstanceID || a.Target.Generation == a.Source.Binding.Generation || a.Target.OperationID == a.Source.Binding.OperationID || (!processResumeRecordEqual(&record.Retired, &a.Source) && !processResumeRecordEqual(&record.Retired, s)) {
		return deferredRefused("attempt source/target differs")
	}
	if processResumeRecordEqual(s, &a.Source) {
		return nil
	}
	history := a.Source.History
	// Host-lost reservation always starts fresh history for the lost binding.
	if record.HostLost != nil || history == nil || a.Source.TurnID != "" || len(a.Source.Pending) > 0 {
		history = &coremetadata.ProcessResumeHistory{Binding: a.Source.Binding, SessionID: a.Source.SessionID, InterruptedTurnID: a.Source.TurnID, Expired: a.Source.Pending}
	}
	if a.Source.Provider != aiModeClaude || a.Source.Binding.AgentUID != record.Agent || a.Target.AgentUID != record.Agent || a.Target.PaneUID != a.Source.Binding.PaneUID || a.Target.ProjectUID != a.Source.Binding.ProjectUID || a.Target.WindowUID != a.Source.Binding.WindowUID || s.Binding != a.Target || s.Provider != aiModeClaude || s.SessionID != a.Source.SessionID || s.SessionID != record.Retired.SessionID || s.ConnectionID != a.Target.OperationID || !reflect.DeepEqual(s.History, history) || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || s.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(s.Binding, pane.Status.LastTermination) || !coremetadata.MatchesProcessWait(s.Binding, agent.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return deferredRefused("attempt lacks exact failed writer/History/Wait proof")
	}
	attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(c.messagePaths.registryPath)))
	err = attention.update(func(records map[string]processAttentionRecord) error {
		old, exists := records[a.Target.PaneUID]
		if !exists {
			return nil
		}
		if old.Provider != aiModeClaude || (old.Binding != processSchemaBinding(a.Source.Binding) && old.Binding != processSchemaBinding(a.Target)) {
			return deferredRefused("attempt attention source differs")
		}
		records[a.Target.PaneUID] = processAttentionRecord{Binding: processSchemaBinding(a.Target), Provider: aiModeClaude, Terminal: true, Pending: map[string]processAttentionPending{}}
		return nil
	})
	if err != nil {
		return err
	}
	record.Retired = *s.Clone()
	// The failed host-lost attempt is fully retired. Future replay uses this
	// writer's actual Wait rather than the prior host-lost history witness.
	if record.HostLost != nil {
		record.Attempt = nil
	}
	record.HostLost = nil
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), record)
}

// Digest reads are outside the lock. The lock then checks the exact sidecar,
// claim authority and recipe CAS again before the resume planner can use it.
func (c *agentCommand) prepareDeferredLaunch(ctx context.Context, candidate processResumeCandidate, opts processAgentResumeOptions) (processResumeCandidate, *deferredLaunchRecord, error) {
	return c.prepareDeferredLaunchMode(ctx, candidate, opts, false)
}

// Explicit replacement validates its new planner files, never reuses old Args.
func (c *agentCommand) prepareDeferredLaunchMode(ctx context.Context, candidate processResumeCandidate, opts processAgentResumeOptions, replacement bool) (processResumeCandidate, *deferredLaunchRecord, error) {
	record, err := c.readDeferredLaunch(candidate.Agent.Metadata.UID)
	if err != nil || record == nil {
		return candidate, record, err
	}
	if !replacement {
		// A frozen launch preserves its settings, but the recorded profile's
		// current Project scope still applies. Refuse before recovery can write
		// the Registry or the prepared launch sidecar.
		annotationsToCheck := []map[string]string{candidate.Agent.Metadata.Annotations, record.NewAnnotations}
		if record.Previous != nil {
			// Prompt recovery can restore the prior recipe before first input.
			annotationsToCheck = append(annotationsToCheck, record.OldAnnotations, record.Previous.NewAnnotations)
		}
		for _, annotations := range annotationsToCheck {
			if name := annotations[coremetadata.AnnotationAgentProfile]; name != "" {
				creator := c.rebind.create
				_, _, _, scopeErr := mutationruntime.ResolveRecordedProfileWithPaths(creator.processCreatePaths, name, candidate.Record.Binding.ProjectUID)
				if profile.ReasonOf(scopeErr) == profile.ReasonOutOfScope {
					return candidate, nil, scopeErr
				}
			}
		}
		if err = record.ValidatePermissionMode(); err != nil {
			return candidate, nil, err
		}
		if err = record.ValidateFiles(); err != nil {
			return candidate, nil, err
		}
	}
	if opts.Model != "" && opts.Model != record.Model || opts.Effort != "" && opts.Effort != record.Effort {
		return candidate, nil, deferredRefused("pending relaunch configuration is fixed; use agent relaunch to change it")
	}
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return candidate, nil, err
	}
	defer func() { unlock() }()
	if err = c.checkDeferredClaim(record.Agent, opts.claim); err != nil {
		return candidate, nil, err
	}
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return candidate, nil, deferredRefused("prepared launch changed")
	}
	if err = c.reconcileDeferredLaunch(ctx, record, opts.allowHostLost); err != nil {
		return candidate, nil, err
	}
	request, err := newProcessAgentResumeRequest(opts)
	if err != nil {
		return candidate, nil, err
	}
	record, err = c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	unlock()
	unlock = func() {}
	if !replacement {
		if err = record.ValidatePermissionMode(); err != nil {
			return candidate, nil, err
		}
		if err = record.ValidateFiles(); err != nil {
			return candidate, nil, err
		}
	}
	if opts.Model != "" && opts.Model != record.Model || opts.Effort != "" && opts.Effort != record.Effort {
		return candidate, nil, deferredRefused("recovered launch override differs")
	}
	nextUnlock, lockErr := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if lockErr != nil {
		return candidate, nil, lockErr
	}
	unlock = nextUnlock
	if err = c.checkDeferredClaim(record.Agent, opts.claim); err != nil {
		return candidate, nil, err
	}
	current, err = c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return candidate, nil, deferredRefused("launch changed during snapshot validation")
	}
	candidate, err = c.processResumeCandidate(request)
	if err == nil && !record.Matches(candidate) {
		err = deferredRefused("prepared launch proof changed")
	}
	return candidate, record, err
}

func (c *agentCommand) frozenDeferredCommand(candidate processResumeCandidate, record *deferredLaunchRecord) (processhost.Command, error) {
	if err := validateDeferredClaudePermissionMode(record.Command.Args); err != nil {
		return processhost.Command{}, err
	}
	ai, ok := c.ai.(*aiCommand)
	if !ok {
		return processhost.Command{}, deferredRefused("process launcher unavailable")
	}
	if err := ai.RequireAgentEnabled(aiModeClaude); err != nil {
		return processhost.Command{}, err
	}
	fresh, err := ai.PlanProcessClaudeCommand(candidate.Agent.Spec.Workspace, processClaudeLaunchOptions{})
	if err != nil {
		return fresh, err
	}
	if fresh.Path != record.Command.Path {
		return fresh, deferredRefused("resolved provider path changed")
	}
	command := record.Command
	command.Env = fresh.Env
	return command, nil
}

// Success consumes only this prepared launch after verified same-session init.
// Failure retains the new recipe and readdresses only an exactly reaped writer.
func (c *agentCommand) finishDeferredLaunch(record *deferredLaunchRecord, result processAgentResumeResult, success bool) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return deferredRefused("launch changed during provider start")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || !reflect.DeepEqual(agent.Spec, record.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
		return deferredRefused("new launch recipe changed")
	}
	session := pane.Status.ProcessSession
	if session.SessionID != record.Retired.SessionID || session.Provider != aiModeClaude {
		return deferredRefused("new launch conversation differs")
	}
	if success {
		if session.Binding != metadataProcessBinding(result.Binding) || pane.Status.Activation.Process == nil {
			return deferredRefused("new launch has no exact init")
		}
		return removeDeferredState(c.deferredStatePath("deferred-launches", record.Agent))
	}
	if processResumeRecordEqual(session, &record.Retired) {
		return nil
	}
	if session.Binding != metadataProcessBinding(result.Binding) || agent.Status.Phase != coremetadata.PhaseOffline || session.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(session.Binding, pane.Status.LastTermination) {
		return deferredRefused("failed launch has no exact Wait; inspect Agent before reclaiming")
	}
	return c.reconcileDeferredAttempt(record)
}

// Prompt intents never grant writer authority. Exact failed reservation proof
// precedes configuration rollback and restoration of the immediate prior L.
func (c *agentCommand) reconcilePromptRelaunch(record *deferredLaunchRecord) error {
	prior := record.Previous
	a := record.Attempt
	if a == nil || a.Source.Provider != aiModeClaude || a.Source.Binding.AgentUID != record.Agent || a.Target.AgentUID != record.Agent || a.Target.PaneUID != a.Source.Binding.PaneUID || a.Target.ProjectUID != a.Source.Binding.ProjectUID || a.Target.WindowUID != a.Source.Binding.WindowUID || a.Target.HostInstanceID == a.Source.Binding.HostInstanceID || a.Target.Generation == a.Source.Binding.Generation || a.Target.OperationID == a.Source.Binding.OperationID {
		return deferredRefused("prompt planned target differs")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || prior.Previous != nil || !processResumeRecordEqual(&prior.Retired, &record.Attempt.Source) || !reflect.DeepEqual(prior.NewSpec, record.OldSpec) || !reflect.DeepEqual(prior.NewAnnotations, record.OldAnnotations) {
		return deferredRefused("prompt intent source differs")
	}
	old := reflect.DeepEqual(agent.Spec, record.OldSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.OldAnnotations)
	if processResumeRecordEqual(pane.Status.ProcessSession, &record.Attempt.Source) {
		if !old || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || !coremetadata.MatchesProcessWait(prior.Retired.Binding, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
			return deferredRefused("prompt source rollback lacks exact Wait")
		}
		return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), prior)
	} else {
		proof := *record
		if old {
			proof.NewSpec, proof.NewAnnotations = record.OldSpec, record.OldAnnotations
		}
		// Reuse the complete owner65 History, full binding, both Wait and attention CAS.
		if err = c.reconcileDeferredAttempt(&proof); err != nil {
			return err
		}
		record.Retired = proof.Retired
		_, err = c.rebind.create.store.update(func(current *coremetadata.Registry) error {
			a, ok := current.Agent(record.Agent)
			p, multiple := processResumePane(*current, record.Agent)
			if !ok || multiple || p == nil || !processResumeRecordEqual(p.Status.ProcessSession, &proof.Retired) || !reflect.DeepEqual(a.Spec, proof.NewSpec) || !reflect.DeepEqual(a.Metadata.Annotations, proof.NewAnnotations) {
				return deferredRefused("prompt rollback source changed")
			}
			a.Spec, a.Metadata.Annotations = record.OldSpec, maps.Clone(record.OldAnnotations)
			return nil
		})
		if err != nil {
			return err
		}
	}
	prior.Retired, prior.Attempt = record.Retired, record.Attempt
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), prior)
}

func (c *agentCommand) restorePromptRelaunch(record *deferredLaunchRecord) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return deferredRefused("prompt intent changed during start")
	}
	return c.reconcilePromptRelaunch(record)
}
