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
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

// Text is the exact provider input. Peer callers supply the existing serialized
// untrusted coordination content; resume never promotes it to operator input.
type processResumeFirstFrame struct{ Kind, Text string }
type processResumeScope struct{ Project, Window selector.Ref }
type processAgentResumeOptions struct {
	Agent         selector.Ref
	Scope         processResumeScope
	Prompt        processResumeFirstFrame
	Model, Effort string
}
type processAgentResumeRequest struct{ options processAgentResumeOptions }
type processAgentResumeResult struct {
	Binding         processhost.Binding
	Handle          processOwnedHandle
	Previous        processResumePrevious
	owner           processAgentCreateResult
	previousBinding processhost.Binding
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
	pane, _ := processResumePane(reg, uid)
	alive := false
	if pane != nil && pane.Status.Activation.Process != nil {
		identity, _, e := localipc.Process(pane.Status.Activation.Process.HostProcess.PID)
		alive = e == nil && identity == pane.Status.Activation.Process.HostProcess
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
	plan, config, settingsPlan, err := c.planProcessResume(candidate, request.options)
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
	_, err = creator.store.update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := processResumeRefusal(*reg, b.Agent, false); err != nil {
			return err
		}
		pane, _ := processResumePane(*reg, b.Agent)
		if !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) {
			return fmt.Errorf("%s: %w: recorded generation changed", processResumeRefused, processhost.ErrResumeRefused)
		}
		agent, _ := reg.Agent(b.Agent)
		if !reflect.DeepEqual(agent.Spec, candidate.Agent.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, candidate.Agent.Metadata.Annotations) {
			return fmt.Errorf("%s: %w: resume recipe changed", processResumeRefused, processhost.ErrResumeRefused)
		}
		mutator := creator.store.mutator()
		if err := mutator.ReserveProcessResume(reg, candidate.Record.Binding, metadataProcessBinding(b)); err != nil {
			return err
		}
		return settingsPlan.record(reg, mutator, b.Agent)
	})
	if err != nil {
		return result, err
	}
	owner := processAgentCreateResult{Binding: b, Provider: candidate.Record.Provider, registryPath: path, Created: createResult{kind: coremetadata.KindAgent, uid: b.Agent, name: candidate.Agent.Metadata.Name, windowUID: b.Window}}
	if reg, e := creator.store.snapshot(); e == nil {
		if project, ok := reg.Project(b.Project); ok {
			owner.Created.projectName = project.Metadata.Name
		}
		if window, ok := reg.Window(b.Window); ok {
			owner.Created.windowName = window.Metadata.Name
		}
	}
	result = processAgentResumeResult{Binding: b, Previous: candidate.Previous, owner: owner, previousBinding: processSchemaBinding(candidate.Record.Binding)}
	executable, err := os.Executable()
	if err != nil {
		return result, err
	}
	host, err := processhost.NewHost(b.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: plan.Env}, creator.processCreateTransactions(path), processhost.DefaultLimits())
	if err != nil {
		return result, err
	}
	launch := processhost.Launch{Binding: b, Command: plan}
	old := processResumeSessionRecord(candidate.Record)
	if candidate.Record.Provider == aiModeClaude {
		handle, startErr := startProcessClaude(ctx, host, launch, path, processClaudeResumeLaunch{Record: old, Turn: operation + "-resume", Prompt: request.options.Prompt.Text})
		err = startErr
		if handle != nil {
			result.Handle = handle
		}
	} else {
		launch.Spawned = processCodexCreateSpawn(path, b)
		var endpoint *codexProcessEndpoint
		endpoint, err = startProcessCodex(ctx, host, launch, config, path, old)
		result.owner.codexEndpoint = endpoint
		if endpoint != nil {
			result.Handle = endpoint.handle
		}
	}
	result.owner.Handle = result.Handle
	if err != nil {
		return result, fmt.Errorf("%s: %w: %w", processResumeRefused, processhost.ErrResumeRefused, err)
	}
	if candidate.Record.Provider == aiModeCodex && request.options.Prompt.Kind != "" {
		snapshot, e := result.Handle.Observe(b)
		if e != nil {
			return result, e
		}
		err = result.Handle.Turn(ctx, processhost.Authority{Binding: b, Connection: snapshot.Connection, Session: snapshot.Session}, operation+"-resume", request.options.Prompt.Text)
	}
	return result, err
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
	settingsPlan, err = ai.ResolveAgentSettingsRequest(provider, candidate.Agent.Metadata.Annotations, agentSettingsRequest{model: opts.Model, effort: opts.Effort, source: "resume"})
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
	annotations := settingsPlan.launchAnnotations(candidate.Agent.Metadata.Annotations)
	model := settingsPlan.resolution.New.Model.Value
	effort := annotations[coremetadata.AnnotationAgentEffort]
	if err := requireLaunchOptions("agent resume", provider, model, effort, false, "nothing was changed"); err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	var settings string
	var policy codexappserver.ThreadPolicy
	if provider == aiModeCodex {
		_, policy, err = c.rebind.create.codexResumeProfile(annotations)
	} else {
		_, _, settings, policy, err = ai.resumeProfileSettings(provider, annotations)
	}
	if err != nil {
		return processhost.Command{}, config, settingsPlan, err
	}
	if provider == aiModeClaude {
		persona, unavailable := ai.resumePersonaSnapshot(provider, annotations)
		if unavailable != nil {
			return processhost.Command{}, config, settingsPlan, unavailable
		}
		instructions, err := ai.resumeSystemPromptFile(provider, annotations, persona)
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
