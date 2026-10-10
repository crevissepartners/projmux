package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

// Text is the exact provider input. Peer callers supply the existing serialized
// untrusted coordination content; resume never promotes it to operator input.
type processResumeFirstFrame struct{ Kind, Text string }
type processResumeScope struct{ Project, Window selector.Ref }
type processAgentResumeOptions struct {
	Agent               selector.Ref
	Scope               processResumeScope
	Prompt              processResumeFirstFrame
	Model, Effort       string
	promptPartsPrepared bool // relaunch already froze its prompt-part recipe.
	allowHostLost       bool // Explicit CLI resume and private consumers opt in.
	claim               *deferredProcessClaim
}
type processAgentResumeRequest struct{ options processAgentResumeOptions }
type processAgentResumeResult struct {
	Binding                 processhost.Binding
	Handle                  processOwnedHandle
	Previous                processResumePrevious
	owner                   processAgentCreateResult
	previousBinding         processhost.Binding
	previousRecord          *coremetadata.ProcessSessionRecord
	previousAttention       *processAttentionRecord
	attentionChecked        bool
	previousActivation      *coremetadata.ProcessActivation
	deferredSynchronization *processResumeSynchronization
}

func newProcessAgentResumeRequest(opts processAgentResumeOptions) (processAgentResumeRequest, error) {
	if opts.Agent == (selector.Ref{}) {
		return processAgentResumeRequest{}, usageError("agent resume requires one Agent reference")
	}
	for _, ref := range []struct {
		value selector.Ref
		kind  coremetadata.Kind
	}{{opts.Agent, coremetadata.KindAgent}, {opts.Scope.Project, coremetadata.KindProject}, {opts.Scope.Window, coremetadata.KindWindow}} {
		if err := validateProcessResumeRef(ref.value, ref.kind); err != nil {
			return processAgentResumeRequest{}, err
		}
	}
	switch opts.Prompt.Kind {
	case "":
		if opts.Prompt.Text != "" {
			return processAgentResumeRequest{}, fmt.Errorf("%s: %w: first frame has no kind", processResumeRefused, processhost.ErrResumeRefused)
		}
	case "user":
		if strings.TrimSpace(opts.Prompt.Text) == "" {
			return processAgentResumeRequest{}, usageError("agent resume: a user first frame requires a nonempty prompt")
		}
	case "peer":
		var content claudeProviderCoordinationContent
		if json.Unmarshal([]byte(opts.Prompt.Text), &content) != nil || content.Kind != "projmux-coordination" || content.Authority != "untrusted-coordination-only" || content.MessageRef == "" {
			return processAgentResumeRequest{}, fmt.Errorf("%s: %w: peer first frame requires existing untrusted coordination content", processResumeRefused, processhost.ErrResumeRefused)
		}
	default:
		return processAgentResumeRequest{}, fmt.Errorf("%s: %w: unsupported first frame kind", processResumeRefused, processhost.ErrResumeRefused)
	}
	return processAgentResumeRequest{options: opts}, nil
}

func (c *agentCommand) processResumeCandidate(request processAgentResumeRequest) (processResumeCandidate, error) {
	reg, err := c.loadRegistry()
	if err != nil {
		return processResumeCandidate{}, MapMetadataError(err)
	}
	opts := request.options
	query := selector.Query{Agents: []selector.Ref{opts.Agent}}
	if opts.Scope.Project != (selector.Ref{}) {
		query.Project = &opts.Scope.Project
	}
	if opts.Scope.Window != (selector.Ref{}) {
		query.Windows = []selector.Ref{opts.Scope.Window}
	}
	resolution, err := selector.New(reg).ResolveAgents(query)
	if err != nil {
		return processResumeCandidate{}, MapMetadataError(err)
	}
	if err := selector.Enforce(selector.Target{Verb: selector.VerbResume, Kind: coremetadata.KindAgent}, selector.DescribeSelector(query), resolution); err != nil {
		return processResumeCandidate{}, MapMetadataError(err)
	}
	uid := resolution.Matches[0].UID
	if err := c.checkDeferredClaim(uid, opts.claim); err != nil {
		return processResumeCandidate{}, err
	}
	pane, _ := processResumePane(reg, uid)
	alive := false
	if pane != nil && pane.Status.Activation.Process != nil {
		identity, _, e := c.readProcessIdentity()(pane.Status.Activation.Process.HostProcess.PID)
		alive = e == nil && identity == pane.Status.Activation.Process.HostProcess
	}
	if opts.allowHostLost && !alive && pane != nil && pane.Status.Activation.Process != nil {
		if candidate, ok := hostLostResumeCandidate(reg, uid, c.readProcessIdentity()); ok {
			return candidate, nil
		}
	}
	if err = processResumeRefusal(reg, uid, alive); err != nil {
		return processResumeCandidate{}, err
	}
	for _, candidate := range listResumableProcessAgents(reg, processResumeFilter{}) {
		if candidate.Agent.Metadata.UID == uid {
			return candidate, nil
		}
	}
	return processResumeCandidate{}, fmt.Errorf("%s: %w: selected durable candidate is unavailable", processResumeRefused, processhost.ErrResumeRefused)
}

// resumeProcessAgent never creates a new Agent or replacement conversation.
// Kernel liveness and provider preparation stay outside the Registry lock; the
// reservation rechecks the exact old durable record before replacing generation.
func (c *agentCommand) resumeProcessAgent(ctx context.Context, request processAgentResumeRequest) (processAgentResumeResult, error) {
	var result processAgentResumeResult
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return result, err
	}
	if candidate.Record.Provider == aiModeClaude && request.options.Prompt.Kind == "" {
		return result, fmt.Errorf("%s: %w: Claude stream resume requires an explicit first frame", processResumeRefused, processhost.ErrResumeRefused)
	}
	if c.rebind == nil || c.rebind.create == nil {
		return result, errors.New("process-host-unavailable: process resume creator is not configured")
	}
	creator := c.rebind.create
	state, err := creator.store.stateDir()
	if err != nil {
		return result, err
	}
	path := intmetadata.PathFor(state)
	// Prove retirement before reserving or preparing a provider. A terminal
	// attention badge alone grants no authority to replace another binding.
	var previousAttention *processAttentionRecord
	if candidate.Record.Provider == aiModeCodex {
		previousAttention, err = readCodexResumeAttention(path, processSchemaBinding(candidate.Record.Binding))
		if err != nil {
			return result, err
		}
	}
	candidate, deferredLaunch, err := c.prepareDeferredLaunch(ctx, candidate, request.options)
	if err != nil {
		return result, err
	}
	var plan processhost.Command
	var config processhost.CodexConfig
	var settingsPlan agentSettingsLaunch
	if deferredLaunch != nil {
		plan, err = c.frozenDeferredCommand(candidate, deferredLaunch)
	} else {
		plan, config, settingsPlan, err = c.planProcessResume(candidate, request.options)
	}
	if err != nil {
		return result, err
	}
	operation, err := newCreateOperationID()
	if err != nil {
		return result, err
	}
	generation, err := creator.mintGeneration()
	if err != nil {
		return result, err
	}
	b := processSchemaBinding(candidate.Record.Binding)
	b.Host, b.Generation, b.Operation = operation, generation, operation
	if err = c.reserveProcessResume(ctx, candidate, settingsPlan, b, request.options.claim, deferredLaunch); err != nil {
		return result, err
	}
	if candidate.HostLost != nil {
		removeHostLostLease(path, *candidate.HostLost)
	}
	result = c.processResumeResult(candidate, b, path)
	if notice := settingsPlan.projectGuidance.notice(candidate.Agent.Metadata.Name); notice != "" {
		result.owner.Notices = append(result.owner.Notices, notice)
	}
	result.previousAttention = previousAttention
	result.attentionChecked = candidate.Record.Provider == aiModeCodex
	if err = result.startProcessResume(ctx, creator, plan, config, request.options.Prompt); err != nil {
		if result.hasNoChild() {
			err = errors.Join(err, result.restoreReservation())
		}
		if deferredLaunch != nil {
			err = result.fail(err)
			err = errors.Join(err, c.finishDeferredLaunch(deferredLaunch, result, false))
		}
		return result, fmt.Errorf("%s: %w: %w", processResumeRefused, processhost.ErrResumeRefused, err)
	}
	if deferredLaunch != nil {
		changed, controls, attention, syncErr := result.resumeSynchronization(creator)
		if syncErr != nil {
			err = result.fail(syncErr)
			return result, errors.Join(err, c.finishDeferredLaunch(deferredLaunch, result, false))
		}
		result.deferredSynchronization = &processResumeSynchronization{changed, controls, attention}
		if err = c.finishDeferredLaunch(deferredLaunch, result, true); err != nil {
			err = result.fail(err)
			return result, errors.Join(err, c.finishDeferredLaunch(deferredLaunch, result, false))
		}
	}
	return result, nil
}

func (c *agentCommand) reserveProcessResume(ctx context.Context, candidate processResumeCandidate, settings agentSettingsLaunch, binding processhost.Binding, claim *deferredProcessClaim, prepared ...*deferredLaunchRecord) error {
	unlock, lockErr := lockDeferredClaim(c.deferredClaimPath(binding.Agent))
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if err := c.checkDeferredClaim(binding.Agent, claim); err != nil {
		return err
	}
	if candidate.Record.Provider == aiModeClaude && (len(prepared) == 0 || prepared[0] == nil) {
		current, err := c.readDeferredLaunch(binding.Agent)
		if err != nil {
			return err
		}
		if current != nil {
			return deferredRefused("prepared launch changed")
		}
	}
	if err := c.admitDeferredInput(claim); err != nil {
		return err
	}
	if len(prepared) > 0 && prepared[0] != nil {
		if err := c.prepareDeferredAttempt(candidate, binding, prepared[0]); err != nil {
			return err
		}
	}
	state, err := c.rebind.create.store.stateDir()
	if err != nil {
		return err
	}
	update := func(fn func(*coremetadata.Registry) error) error {
		_, err := c.rebind.create.store.update(fn)
		return err
	}
	// Reservation has no live provider init evidence. A legacy missing ref is
	// bound by the next verified init/snapshot/Wait, never by reserved IDs.
	err = updateProcessAgentSession(intmetadata.PathFor(state), binding, nil, true, false, update, func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if candidate.HostLost == nil {
			if err := processResumeRefusal(*reg, binding.Agent, false); err != nil {
				return err
			}
		}
		pane, _ := processResumePane(*reg, binding.Agent)
		if pane == nil || !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) {
			return fmt.Errorf("%s: %w: recorded generation changed", processResumeRefused, processhost.ErrResumeRefused)
		}
		agent, _ := reg.Agent(binding.Agent)
		if !reflect.DeepEqual(agent.Spec, candidate.Agent.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, candidate.Agent.Metadata.Annotations) {
			return fmt.Errorf("%s: %w: resume recipe changed", processResumeRefused, processhost.ErrResumeRefused)
		}
		mutator := c.rebind.create.store.mutator()
		if candidate.HostLost != nil {
			if err := reserveHostLostResume(reg, candidate, metadataProcessBinding(binding), c.readProcessIdentity(), mutator); err != nil {
				return err
			}
		} else if err := mutator.ReserveProcessResume(reg, candidate.Record.Binding, metadataProcessBinding(binding)); err != nil {
			return err
		}
		return settings.record(reg, mutator, binding.Agent)
	})
	return err
}

func (c *agentCommand) processResumeResult(candidate processResumeCandidate, binding processhost.Binding, path string) processAgentResumeResult {
	owner := processAgentCreateResult{Binding: binding, Provider: candidate.Record.Provider, registryPath: path, Created: createResult{kind: coremetadata.KindAgent, uid: binding.Agent, name: candidate.Agent.Metadata.Name, windowUID: binding.Window}}
	if reg, err := c.rebind.create.store.snapshot(); err == nil {
		if project, ok := reg.Project(binding.Project); ok {
			owner.Created.projectName = project.Metadata.Name
		}
		if window, ok := reg.Window(binding.Window); ok {
			owner.Created.windowName = window.Metadata.Name
		}
	}
	return processAgentResumeResult{Binding: binding, Previous: candidate.Previous, previousActivation: candidate.HostLost, owner: owner, previousBinding: processSchemaBinding(candidate.Record.Binding), previousRecord: candidate.Record.Clone()}
}

func (r *processAgentResumeResult) startProcessResume(ctx context.Context, creator *createCommand, plan processhost.Command, config processhost.CodexConfig, frame processResumeFirstFrame) error {
	// Relaunch also uses this start path. Capture its retired writer before
	// child birth, while preserving an absent record checked by resume.
	if r.owner.Provider == aiModeCodex && !r.attentionChecked {
		var err error
		r.previousAttention, err = readCodexResumeAttention(r.owner.registryPath, r.previousBinding)
		if err != nil {
			return err
		}
		r.attentionChecked = true
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	host, err := processhost.NewHost(r.Binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: plan.Env}, creator.processCreateTransactions(r.owner.registryPath), processhost.DefaultLimits())
	if err != nil {
		return err
	}
	launch := processhost.Launch{Binding: r.Binding, Command: plan}
	old := processResumeSessionRecord(*r.previousRecord)
	if r.owner.Provider == aiModeClaude {
		err = r.startClaudeResume(ctx, host, launch, old, frame)
	} else {
		err = r.startCodexResume(ctx, host, launch, config, old, frame)
	}
	r.owner.Handle = r.Handle
	return err
}

func (r *processAgentResumeResult) startClaudeResume(ctx context.Context, host *processhost.Host, launch processhost.Launch, old processhost.SessionRecord, frame processResumeFirstFrame) error {
	handle, err := startProcessClaude(ctx, host, launch, r.owner.registryPath, &processClaudeResumeLaunch{Record: old, Turn: r.Binding.Operation + "-resume", Prompt: frame.Text})
	if handle != nil {
		r.Handle = handle
	}
	return err
}

func (r *processAgentResumeResult) startCodexResume(ctx context.Context, host *processhost.Host, launch processhost.Launch, config processhost.CodexConfig, old processhost.SessionRecord, frame processResumeFirstFrame) error {
	launch.Spawned = processCodexCreateSpawn(r.owner.registryPath, r.Binding)
	publish := launch.Spawned.Publish
	launch.Spawned.Publish = func(ctx context.Context, handle *processhost.Handle) error {
		if err := publish(ctx, handle); err != nil {
			return err
		}
		// Birth is now durable, so even initialization failure and its actual
		// Wait leave attention on the same generation as the Registry.
		return r.activateCodexResumeAttention()
	}
	endpoint, err := startProcessCodex(ctx, host, launch, config, r.owner.registryPath, &old)
	r.owner.codexEndpoint = endpoint
	if endpoint != nil && endpoint.handle != nil {
		r.Handle = endpoint.handle
	}
	if err != nil || frame.Kind == "" {
		return err
	}
	snapshot, err := r.Handle.Observe(r.Binding)
	if err != nil {
		return err
	}
	// Resume can already have opened a goal continuation on the bound thread.
	// Deliver the first frame to that exact turn, or start one while idle.
	_, err = endpoint.handle.DeliverUserTurn(ctx, processhost.Authority{Binding: r.Binding, Connection: snapshot.Connection, Session: snapshot.Session}, r.Binding.Operation+"-resume", frame.Text)
	return err
}

// A failed Start can return a handle before any child exists. Only exact
// observation with PID zero admits restoration; the writer also fences birth.
func (r *processAgentResumeResult) hasNoChild() bool {
	if r.Handle == nil {
		return true
	}
	snapshot, err := r.Handle.Observe(r.Binding)
	return err == nil && snapshot.PID == 0
}

func (r *processAgentResumeResult) restoreReservation() error {
	if r.previousRecord == nil {
		return nil
	}
	_, _, err := intmetadata.NewStore(r.owner.registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		if r.previousActivation != nil {
			return intmetadata.DefaultMutator().RestoreProcessHostLostResume(reg, metadataProcessBinding(r.Binding), *r.previousRecord, *r.previousActivation)
		}
		return intmetadata.DefaultMutator().RestoreProcessResume(reg, metadataProcessBinding(r.Binding), *r.previousRecord)
	})
	if err == nil {
		r.previousRecord = nil
	}
	return err
}

func processResumeSessionRecord(record coremetadata.ProcessSessionRecord) processhost.SessionRecord {
	session := record.SessionID
	if record.Provider == aiModeCodex {
		session = record.ThreadID
	}
	out := processhost.SessionRecord{Provider: record.Provider, Binding: processSchemaBinding(record.Binding), Session: session, Connection: record.ConnectionID, Turn: record.TurnID}
	for _, q := range record.Pending {
		out.Pending = append(out.Pending, processhost.RecordedControl{ID: q.ID, Kind: q.Kind, Connection: q.ConnectionID, Session: q.SessionID, Turn: q.TurnID})
	}
	return out
}

func processResumeRecordEqual(a, b *coremetadata.ProcessSessionRecord) bool {
	// Includes previous history, termination-independent identities and controls.
	return reflect.DeepEqual(a, b)
}

func (c *agentCommand) planProcessResume(candidate processResumeCandidate, opts processAgentResumeOptions) (processhost.Command, processhost.CodexConfig, agentSettingsLaunch, error) {
	var config processhost.CodexConfig
	var settingsPlan agentSettingsLaunch
	ai, ok := c.ai.(*aiCommand)
	if !ok {
		return processhost.Command{}, config, settingsPlan, errors.New("process-host-unavailable: process resume launcher is not configured")
	}
	provider := candidate.Record.Provider
	if err := ai.RequireAgentEnabled(provider); err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	workspace := candidate.Agent.Spec.Workspace
	if workspace.CWD == "" {
		return processhost.Command{}, config, settingsPlan, fmt.Errorf("%s: %w: recorded workspace is unavailable", processResumeRefused, processhost.ErrResumeRefused)
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return processhost.Command{}, config, settingsPlan, MapMetadataError(err)
	}
	project, ok := registry.Project(candidate.Record.Binding.ProjectUID)
	if !ok {
		return processhost.Command{}, config, settingsPlan, processhost.ErrStale
	}
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspaceFor
	}
	workspace, err = resolver("agent resume", registry, *project, provider, workspace.CWD, workspace.AdditionalWritableRoots)
	if err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	var projectGuidance projectGuidanceLaunch
	if provider == aiModeClaude && !opts.promptPartsPrepared {
		projectGuidance = ai.loadProjectGuidance(*project, candidate.Agent.Metadata.Annotations)
	}
	request := agentSettingsRequest{projectUID: project.Metadata.UID, model: opts.Model, effort: opts.Effort, source: "resume"}
	if projectGuidance.active && projectGuidance.unavailable == nil {
		request.projectGuidance = &projectGuidance.digest
	}
	settingsPlan, err = ai.ResolveAgentSettingsRequest(provider, candidate.Agent.Metadata.Annotations, request)
	settingsPlan.projectGuidance = projectGuidance
	if err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	if provider == aiModeClaude && settingsPlan.instructionsErr != nil {
		return processhost.Command{}, config, settingsPlan, settingsPlan.instructionsErr
	}
	settingsPlan = settingsPlan.writeSnapshot()
	if settingsPlan.snapshotErr != nil {
		return processhost.Command{}, config, settingsPlan, settingsPlan.snapshotErr
	}
	annotations := projectGuidance.resumeLaunchAnnotations(settingsPlan.launchAnnotations(candidate.Agent.Metadata.Annotations))
	model := settingsPlan.resolution.New.Model.Value
	effort := annotations[coremetadata.AnnotationAgentEffort]
	if err := requireLaunchOptions("agent resume", provider, model, effort, false, "nothing was changed"); err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	var settings string
	var policy codexappserver.ThreadPolicy
	if provider == aiModeCodex {
		_, policy, err = c.rebind.create.codexResumeProfile(annotations, project.Metadata.UID)
	} else {
		_, _, settings, policy, err = ai.resumeProfileSettings(provider, annotations, project.Metadata.UID)
	}
	if err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	if provider == aiModeClaude {
		persona, unavailable := ai.resumePersonaSnapshot(provider, annotations)
		if unavailable != nil {
			return processhost.Command{}, config, settingsPlan, unavailable
		}
		instructions, err := ai.resumeProjectSystemPromptFile(provider, annotations, persona)
		if err != nil {
			return processhost.Command{}, config, settingsPlan, err
		}
		instructions, err = ai.resumeGuidanceSystemPromptFile(provider, annotations, instructions)
		if err != nil {
			return processhost.Command{}, config, settingsPlan, err
		}
		command, err := ai.PlanProcessClaudeCommand(workspace, processClaudeLaunchOptions{Model: model, Effort: effort, SettingsFile: settings, InstructionsFile: instructions})
		if err == nil {
			command.Args = append(claudeResumeSnapshotArgs(provider, annotations), command.Args...)
		}
		return command, config, settingsPlan, err
	}
	path := ai.findAgentBinary(provider)
	if path == "" {
		return processhost.Command{}, config, settingsPlan, errors.New(ai.missingAgentRunnerMessage(provider))
	}
	if provider != aiModeCodex {
		return processhost.Command{}, config, settingsPlan, errors.New("process-provider-unsupported: process resume requires Claude or Codex")
	}
	command, err := processhost.CodexCommand(path, workspace.CWD, os.Environ(), nil)
	config = processhost.CodexConfig{Version: version.String(), Roots: slices.Clone(workspace.AdditionalWritableRoots), Settings: codexappserver.ThreadSettings{Model: model, Effort: effort, Policy: policy}}
	return command, config, settingsPlan, err
}

func validateProcessResumeRef(ref selector.Ref, kind coremetadata.Kind) error {
	if ref == (selector.Ref{}) {
		return nil
	}
	if ref.Kind != kind || (ref.UID == "") == (ref.Name == "") {
		return usageError(fmt.Sprintf("agent resume: %s requires one typed name or UID reference", kind))
	}
	raw := ref.Name
	if ref.UID != "" {
		raw = selector.UIDPrefix + ref.UID
	}
	parsed, err := selector.ParseRef(kind, raw)
	if err != nil {
		return MapMetadataError(err)
	}
	if parsed.UID != ref.UID || parsed.Name != ref.Name {
		return usageError(fmt.Sprintf("agent resume: %s typed reference changes meaning when parsed", kind))
	}
	return nil
}
