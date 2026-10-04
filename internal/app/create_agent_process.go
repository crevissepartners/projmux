package app

import (
	"context"
	"errors"
	"fmt"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Typed callers and the CLI share this validated request. It contains no argv
// parser, output selection, terminal handle, or inferred runtime authority.
type processAgentCreateOptions struct {
	Project, Window                                                    selector.Ref
	Provider, Name, CWD, Model, Effort, Instructions, Profile, Persona string
	Creator                                                            string
	Payload, AddDirs                                                   []string
	Labels                                                             map[string]string
}

type processAgentCreateRequest struct{ options processAgentCreateOptions }

type processAgentCreateResult struct {
	Created createResult
	Binding processhost.Binding
	Handle  *processhost.Handle
}

func newProcessAgentCreateRequest(opts processAgentCreateOptions) (processAgentCreateRequest, error) {
	if err := validateProcessCreateRef(opts.Project, coremetadata.KindProject); err != nil {
		return processAgentCreateRequest{}, err
	}
	if err := validateProcessCreateRef(opts.Window, coremetadata.KindWindow); err != nil {
		return processAgentCreateRequest{}, err
	}
	if opts.Project == (selector.Ref{}) && opts.Window == (selector.Ref{}) {
		return processAgentCreateRequest{}, usageError("create agent --host process requires an exact Project or Window scope")
	}
	if opts.Name != "" {
		if err := coremetadata.ValidateName(opts.Name); err != nil {
			return processAgentCreateRequest{}, MapMetadataError(err)
		}
	}
	if opts.Instructions != "" && opts.Persona != "" {
		return processAgentCreateRequest{}, usageError("create agent accepts only one of --instructions and --persona")
	}
	if opts.Provider != "" && !slices.Contains([]string{aiModeClaude, aiModeCodex, aiModeAntigravity}, opts.Provider) {
		return processAgentCreateRequest{}, usageError("create agent: unknown Agent provider")
	}
	if opts.Creator != "" {
		creator, err := parseCreatorFlag(canonicalCreateAgent, opts.Creator)
		if err != nil {
			return processAgentCreateRequest{}, err
		}
		opts.Creator = creator
	}
	opts.Payload, opts.AddDirs, opts.Labels = slices.Clone(opts.Payload), slices.Clone(opts.AddDirs), maps.Clone(opts.Labels)
	return processAgentCreateRequest{options: opts}, nil
}

func validateProcessCreateRef(ref selector.Ref, kind coremetadata.Kind) error {
	if ref == (selector.Ref{}) {
		return nil
	}
	if ref.Kind != kind || (ref.UID == "") == (ref.Name == "") {
		return usageError(fmt.Sprintf("create agent: %s requires one typed name or UID reference", kind))
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
		return usageError(fmt.Sprintf("create agent: %s typed reference changes meaning when parsed", kind))
	}
	return nil
}

type processClaudeLaunchOptions struct{ Model, Effort, InstructionsFile, SettingsFile string }

// Process launch uses the same provider grammar and resolved setting files as
// tmux. The initial task travels over stream input, never the provider argv.
func (c *aiCommand) PlanProcessClaudeCommand(workspace coremetadata.AgentWorkspace, opts processClaudeLaunchOptions) (processhost.Command, error) {
	path := c.findAgentBinary(aiModeClaude)
	if path == "" {
		return processhost.Command{}, fmt.Errorf("%s", c.missingAgentRunnerMessage(aiModeClaude))
	}
	args, err := providerLaunchArgs(aiModeClaude, workspace, nil)
	if err != nil {
		return processhost.Command{}, err
	}
	resolved := append(claudeLaunchOptionArgs(opts.Model, opts.Effort, opts.InstructionsFile), claudeSettingsArgs(opts.SettingsFile)...)
	resolved = append(resolved, args...)
	env := append([]string{}, os.Environ()...)
	prepend := filepath.Dir(path)
	hasPath := false
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			hasPath = true
			env[i] = "PATH=" + prepend + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
		}
	}
	if !hasPath {
		env = append(env, "PATH="+prepend)
	}
	return processhost.ClaudeCommand(path, workspace.CWD, env, resolved)
}

type processAgentCreatePlan struct {
	project   coremetadata.Project
	window    coremetadata.Window
	workspace coremetadata.AgentWorkspace
	flags     resourceCreateFlags
	command   processhost.Command
}

type processClaudeCommandPlanner interface {
	PlanProcessClaudeCommand(coremetadata.AgentWorkspace, processClaudeLaunchOptions) (processhost.Command, error)
}

// startProcessAgent is the shared typed creation entry. Provider and filesystem
// preparation happens before the pure Registry reservation transaction.
func (c *createCommand) startProcessAgent(ctx context.Context, request processAgentCreateRequest) (processAgentCreateResult, error) {
	plan, err := c.planProcessAgent(request.options)
	if err != nil {
		return processAgentCreateResult{}, err
	}
	stateDir, err := c.store.stateDir()
	if err != nil {
		return processAgentCreateResult{}, err
	}
	operation, err := newCreateOperationID()
	if err != nil {
		return processAgentCreateResult{}, err
	}
	generation, err := c.mintGeneration()
	if err != nil {
		return processAgentCreateResult{}, err
	}
	result, err := c.reserveProcessAgent(ctx, plan, request.options, operation, generation)
	if err != nil {
		return result, err
	}
	path := intmetadata.PathFor(stateDir)
	executable, err := os.Executable()
	if err != nil {
		return result, err
	}
	host, err := processhost.NewHost(result.Binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: plan.command.Env}, c.processCreateTransactions(path), processhost.DefaultLimits())
	if err != nil {
		return result, err
	}
	result.Handle, err = startProcessClaude(ctx, host, processhost.Launch{Binding: result.Binding, Command: plan.command}, path)
	return result, err
}

func (c *createCommand) planProcessAgent(opts processAgentCreateOptions) (processAgentCreatePlan, error) {
	var plan processAgentCreatePlan
	if c.agents == nil || c.store == nil || c.store.stateDir == nil {
		return plan, errors.New("process creator is not configured")
	}
	flags := processCreateFlags(opts)
	provider, err := c.resolveCreateProvider(canonicalCreateAgent, "", flags)
	if err != nil {
		return plan, err
	}
	if provider != aiModeClaude {
		return plan, errors.New("process-provider-unsupported: this process creator requires Claude")
	}
	if err = c.agents.RequireAgentEnabled(provider); err != nil {
		return plan, err
	}
	if err = c.resolveCreateProfile(canonicalCreateAgent, provider, &flags); err != nil {
		return plan, err
	}
	registry, err := c.store.snapshot()
	if err != nil {
		return plan, err
	}
	plan.project, plan.window, err = resolveProcessCreateScope(registry, opts)
	if err != nil {
		return plan, err
	}
	if err = c.refuseMissingRoot(plan.project); err != nil {
		return plan, err
	}
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspace
	}
	plan.workspace, err = resolver(registry, plan.project, provider, flags.cwd, flags.addDirs)
	if err != nil {
		return plan, err
	}
	plan.flags = flags
	err = c.prepareProcessCreateLaunch(&plan, provider)
	return plan, err
}

func processCreateFlags(opts processAgentCreateOptions) resourceCreateFlags {
	flags := resourceCreateFlags{name: opts.Name, provider: opts.Provider, providerSet: opts.Provider != "", model: opts.Model, effort: opts.Effort, cwd: opts.CWD, cwdSet: opts.CWD != "", addDirs: opts.AddDirs, persona: opts.Persona, profile: opts.Profile, payload: opts.Payload}
	if opts.Instructions != "" {
		flags.persona = opts.Instructions
		flags.personaOption = "instructions"
	} else if opts.Persona != "" {
		flags.personaOption = "persona"
	}
	for key, value := range opts.Labels {
		flags.labels = append(flags.labels, key+"="+value)
	}
	return flags
}

func (c *createCommand) prepareProcessCreateLaunch(plan *processAgentCreatePlan, provider string) error {
	flags := &plan.flags
	if err := requireClaudeLaunchOptions(canonicalCreateAgent, provider, *flags); err != nil {
		return err
	}
	if flags.persona != "" {
		launch, err := c.preparePersonaLaunch(canonicalCreateAgent, flags.persona, flags.personaOption)
		if err != nil {
			return err
		}
		flags.personaLaunch = launch
	}
	if err := c.prepareProfileSettings(canonicalCreateAgent, provider, flags); err != nil {
		return err
	}
	c.prepareProjectLinks(provider, plan.project, flags)
	c.prepareProcessAgentGuidance(flags)
	planner, ok := c.agents.(processClaudeCommandPlanner)
	if !ok {
		return errors.New("process Claude launcher is not configured")
	}
	instructions := flags.personaLaunch.snapshot.Path
	if flags.projectLinks.systemPromptFile != "" {
		instructions = flags.projectLinks.systemPromptFile
	}
	if flags.agentGuidance.systemPromptFile != "" {
		instructions = flags.agentGuidance.systemPromptFile
	}
	command, err := planner.PlanProcessClaudeCommand(plan.workspace, processClaudeLaunchOptions{Model: flags.model, Effort: flags.effort, InstructionsFile: instructions, SettingsFile: flags.profileLaunch.settings})
	if err != nil {
		return err
	}
	plan.command = command
	return nil
}

func resolveProcessCreateScope(reg coremetadata.Registry, opts processAgentCreateOptions) (coremetadata.Project, coremetadata.Window, error) {
	var project coremetadata.Project
	query := selector.Query{}
	if opts.Project != (selector.Ref{}) {
		query.Project = &opts.Project
	}
	if opts.Window != (selector.Ref{}) {
		query.Windows = []selector.Ref{opts.Window}
	} else {
		matches, err := selector.New(reg).ResolveProjects(query)
		if err != nil {
			return project, coremetadata.Window{}, err
		}
		if len(matches.Matches) != 1 {
			return project, coremetadata.Window{}, usageError("process creation requires exactly one Project")
		}
		found, _ := reg.Project(matches.Matches[0].UID)
		project = *found
		ref, err := primaryWindowRef(reg, project, canonicalCreateAgent, "--host process")
		if err != nil {
			return project, coremetadata.Window{}, err
		}
		query.Windows = []selector.Ref{ref}
	}
	matches, err := selector.New(reg).ResolveWindows(query)
	if err != nil {
		return project, coremetadata.Window{}, err
	}
	if len(matches.Matches) != 1 {
		return project, coremetadata.Window{}, usageError("process creation requires exactly one Window")
	}
	window, _ := reg.Window(matches.Matches[0].UID)
	owner, ok := reg.Project(window.Metadata.OwnerUID())
	if !ok {
		return project, coremetadata.Window{}, errors.New("process Window owner is unavailable")
	}
	return *owner, *window, nil
}

func (c *createCommand) reserveProcessAgent(ctx context.Context, plan processAgentCreatePlan, opts processAgentCreateOptions, operation, generation string) (processAgentCreateResult, error) {
	var result processAgentCreateResult
	_, err := c.store.update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		project, window, err := resolveProcessCreateScope(*reg, opts)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(project.Spec, plan.project.Spec) || window.Metadata.UID != plan.window.Metadata.UID {
			return errors.New("process creation scope changed during preparation")
		}
		mutator := c.store.mutator()
		creator, err := c.decideCreator(ctx, canonicalCreateAgent, reg, opts.Creator)
		if err != nil {
			return err
		}
		annotations := plan.flags.agentGuidance.withCreateAnnotation(plan.flags.projectLinks.withCreateAnnotation(withCreateSettingSources(plan.flags, plan.flags.profileLaunch.withAnnotations(withModelAnnotation(plan.flags.model, withEffortAnnotation(plan.flags.effort, plan.flags.personaLaunch.withAnnotations(creator.annotations())))))))
		agent, err := mutator.CreateAgent(reg, window.Metadata.UID, coremetadata.CreateAgentOptions{Name: opts.Name, Provider: aiModeClaude, Labels: opts.Labels, Annotations: annotations, Workspace: plan.workspace, Activation: activationStateForPayload(opts.Payload), OperationID: operation})
		if err != nil {
			return err
		}
		pane, err := mutator.AttachAgentPane(reg, agent.Metadata.UID, coremetadata.BootstrapPane{Name: derivedAgentPaneName(agent.Metadata.Name), CWD: plan.workspace.CWD, Labels: opts.Labels}, operation)
		if err != nil {
			return err
		}
		creator.forAgent(reg, agent.Metadata.UID).annotatePane(reg, pane)
		binding := processhost.Binding{Host: operation, Project: project.Metadata.UID, Window: window.Metadata.UID, Agent: agent.Metadata.UID, Pane: pane.Metadata.UID, Generation: generation, Operation: operation}
		if err := mutator.ReserveProcessBinding(reg, metadataProcessBinding(binding)); err != nil {
			return err
		}
		result = processAgentCreateResult{Created: createResult{kind: coremetadata.KindAgent, uid: agent.Metadata.UID, name: agent.Metadata.Name, projectName: project.Metadata.Name, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}, Binding: binding}
		return nil
	})
	return result, MapMetadataError(err)
}

func metadataProcessBinding(b processhost.Binding) coremetadata.ProcessBinding {
	return coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}
}

func (c *createCommand) processCreateTransactions(path string) processhost.Transactions {
	store := intmetadata.NewStore(path)
	current := func(ctx context.Context, b processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		reg, err := store.LoadReadOnly()
		if err != nil {
			return err
		}
		pane, ok := reg.Pane(b.Pane)
		if !ok || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(b) {
			return processhost.ErrStale
		}
		agent, ok := reg.Agent(b.Agent)
		if !ok || agent.Status.PaneRef != b.Pane || (agent.Status.Phase != coremetadata.PhasePending && agent.Status.Phase != coremetadata.PhaseRunning) {
			return processhost.ErrStale
		}
		return nil
	}
	return processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, session string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			activation, _, ok := reg.CurrentProcessActivation(metadataProcessBinding(b))
			if !ok {
				return processhost.ErrStale
			}
			return intmetadata.DefaultMutator().RecordProcessActivation(reg, activation, session)
		})
		return err
	}}
}
