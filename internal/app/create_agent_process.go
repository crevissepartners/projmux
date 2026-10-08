package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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

// Owner lifetime, actual Wait and plain user turns are shared; answer writers
// remain private to each provider's typed control bridge.
type processOwnedHandle interface {
	Observe(processhost.Binding) (processhost.Snapshot, error)
	Events(processhost.Binding, uint64) ([]processhost.Event, processhost.Snapshot, error)
	Wait(context.Context, processhost.Binding) (processhost.Snapshot, error)
	Stop(processhost.Binding) error
	Turn(context.Context, processhost.Authority, string, string) error
}

type processAgentCreateResult struct {
	Created       createResult
	Binding       processhost.Binding
	Handle        processOwnedHandle
	Provider      string
	codexEndpoint *codexProcessEndpoint
	registryPath  string
	waitReceipt   *coremetadata.TerminationEvidence
	waitRecorded  bool
	recorded      *processRecordedSnapshot
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

// Every newly planned headless Claude launch explicitly selects auto. Deferred
// launches freeze this argument with the rest of their immutable recipe.
const processClaudePermissionMode = "auto"

// Process launch uses the same provider grammar and resolved setting files as
// tmux. The initial task travels over stream input, never the provider argv.
// Headless permission policy is shared by create, resume and relaunch callers.
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
	resolved = append(resolved, "--permission-mode", processClaudePermissionMode)
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
	result.registryPath = path
	executable, err := os.Executable()
	if err != nil {
		return result, err
	}
	transactions := c.processCreateTransactions(path)
	host, err := processhost.NewHost(result.Binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: plan.command.Env}, transactions, processhost.DefaultLimits())
	if err != nil {
		return result, err
	}
	launch := processhost.Launch{Binding: result.Binding, Command: plan.command}
	if result.Provider == aiModeCodex {
		launch.Spawned = processCodexCreateSpawn(path, result.Binding)
		result.codexEndpoint, err = startProcessCodex(ctx, host, launch, processCodexCreateConfig(plan, result.Binding.Agent), path, nil)
		if result.codexEndpoint != nil && result.codexEndpoint.handle != nil {
			result.Handle = result.codexEndpoint.handle
		}
	} else {
		var handle *processhost.Handle
		handle, err = startProcessClaude(ctx, host, launch, path, nil)
		if handle != nil {
			result.Handle = handle
		}
	}
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
	if provider != aiModeClaude && provider != aiModeCodex {
		return plan, errors.New("process-provider-unsupported: this process creator requires Claude or Codex")
	}
	flags.provider = provider
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
	flags := resourceCreateFlags{host: "process", name: opts.Name, provider: opts.Provider, providerSet: opts.Provider != "", model: opts.Model, effort: opts.Effort, cwd: opts.CWD, cwdSet: opts.CWD != "", addDirs: opts.AddDirs, persona: opts.Persona, profile: opts.Profile, payload: opts.Payload}
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
	if provider == aiModeCodex {
		return c.prepareProcessCodexLaunch(plan)
	}
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
		ref, err := primaryWindowRef(reg, project, "create agent", "--host process")
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
		provider := plan.flags.provider
		if provider == "" {
			provider = aiModeClaude
		}
		agent, err := mutator.CreateAgent(reg, window.Metadata.UID, coremetadata.CreateAgentOptions{Name: opts.Name, Provider: provider, Labels: opts.Labels, Annotations: annotations, Workspace: plan.workspace, Activation: activationStateForPayload(opts.Payload), OperationID: operation})
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
		result = processAgentCreateResult{Created: createResult{kind: coremetadata.KindAgent, uid: agent.Metadata.UID, name: agent.Metadata.Name, projectName: project.Metadata.Name, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}, Binding: binding, Provider: provider}
		return nil
	})
	return result, MapMetadataError(err)
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
		err := updateProcessAgentSession(path, b, nil, false, true, nil, func(reg *coremetadata.Registry) error {
			activation, _, ok := reg.CurrentProcessActivation(metadataProcessBinding(b))
			if !ok {
				return processhost.ErrStale
			}
			return intmetadata.DefaultMutator().RecordProcessActivation(reg, activation, session)
		})
		return err
	}}
}

// Unchanged snapshots skip projections; pending store answers still need consumption.
func processSnapshotSynchronizer(changed, unchanged func(processhost.Snapshot) error) func(processhost.Snapshot) error {
	var previous processhost.Snapshot
	seen := false
	return func(snapshot processhost.Snapshot) error {
		if seen && reflect.DeepEqual(previous, snapshot) {
			return unchanged(snapshot)
		}
		if err := changed(snapshot); err != nil {
			return err
		}
		previous, seen = snapshot, true
		return nil
	}
}

// processRecordedSnapshot is the part of a ready snapshot that
// recordProcessSnapshot persists: session identity, current turn, and pending
// control identities.
type processRecordedSnapshot struct {
	State, Session, Connection, Turn string
	Pending                          []coremetadata.ProcessRecordedControl
}

func processRecordedFields(snapshot processhost.Snapshot) processRecordedSnapshot {
	recorded := processRecordedSnapshot{State: snapshot.State, Session: snapshot.Session, Connection: snapshot.Connection, Turn: snapshot.Turn}
	for _, request := range snapshot.Pending {
		recorded.Pending = append(recorded.Pending, processRecordedControl(request))
	}
	return recorded
}

func processRecordedControl(request processhost.Request) coremetadata.ProcessRecordedControl {
	return coremetadata.ProcessRecordedControl{ID: request.ID, Kind: request.Kind, ConnectionID: request.Connection, SessionID: request.Session, TurnID: request.Turn}
}

// waitProcessAgent owns shutdown and persists only actual supervisor Wait.
// A short Wait deadline is a condition check, not a fixed sleep: child exit wins
// immediately. Cancellation closes only this Handle's dedicated lifetime.
func (r *processAgentCreateResult) waitProcessAgent(ctx context.Context, syncSnapshot func(processhost.Snapshot) error) (processhost.Snapshot, error) {
	if r.Handle == nil {
		return processhost.Snapshot{}, errors.New("process host was not started")
	}
	var failure error
	var stopDeadline time.Time
	for {
		if (ctx.Err() != nil || failure != nil) && stopDeadline.IsZero() {
			failure = errors.Join(failure, r.Handle.Stop(r.Binding))
			stopDeadline = time.Now().Add(3*processhost.DefaultLimits().Grace + 2*processhost.DefaultLimits().Write)
		}
		if !stopDeadline.IsZero() && !time.Now().Before(stopDeadline) {
			return processhost.Snapshot{}, errors.Join(failure, errors.New("owned process Wait cleanup exceeded its bound"))
		}
		poll, cancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
		snapshot, err := r.Handle.Wait(poll, r.Binding)
		cancel()
		if err == nil {
			return snapshot, errors.Join(failure, r.persistProcessWait(snapshot))
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return snapshot, errors.Join(failure, err)
		}
		snapshot, err = r.Handle.Observe(r.Binding)
		if err != nil {
			return snapshot, errors.Join(failure, err)
		}
		if failure == nil && stopDeadline.IsZero() && syncSnapshot != nil {
			failure = syncSnapshot(snapshot)
		}
	}
}

func (r *processAgentCreateResult) persistProcessWait(snapshot processhost.Snapshot) error {
	if snapshot.Binding != r.Binding {
		return processhost.ErrStale
	}
	if r.waitRecorded {
		return nil
	}
	receipt, ok := snapshot.Termination(time.Now().UTC())
	if !ok {
		return errors.New("owned supervisor Wait evidence is unavailable")
	}
	if r.waitReceipt == nil {
		r.waitReceipt = receipt.Clone()
	} else {
		receipt = *r.waitReceipt.Clone()
	}
	journal, err := terminationJournalForRegistryPath(r.registryPath)
	if err != nil {
		return err
	}
	// Durability precedes the Registry transaction. If the owner dies between
	// these operations, normal receipt convergence can still see the actual Wait.
	if err = journal.append(receipt); err != nil {
		return err
	}
	err = updateProcessAgentSession(r.registryPath, r.Binding, nil, true, snapshot.Session != "", nil, func(reg *coremetadata.Registry) error {
		activation, _, current := reg.CurrentProcessActivation(metadataProcessBinding(r.Binding))
		if !current {
			return processhost.ErrStale
		}
		// SessionStart can reserve a Claude id before stream init. Actual Wait
		// proves this child never initialized; do not turn that reservation into
		// a resumable conversation or a future backfill source.
		agent, _ := reg.Agent(r.Binding.Agent)
		pane, _ := reg.Pane(r.Binding.Pane)
		if r.Provider == aiModeClaude && snapshot.Session == "" && agent.Status.SessionRef == nil && pane.Status.ProcessSession.History == nil {
			pane.Status.ProcessSession.SessionID = ""
		}
		return intmetadata.DefaultMutator().RecordProcessWait(reg, activation, receipt)
	})
	r.waitRecorded = err == nil
	return err
}

// recordProcessSnapshot persists the ready session record. It opens a Registry
// transaction only when a recorded field differs from the last committed
// record, so streaming Sequence and diagnostic updates never do.
func (r *processAgentCreateResult) recordProcessSnapshot(snapshot processhost.Snapshot) error {
	if snapshot.State != "ready" || snapshot.Session == "" {
		return nil
	}
	fields := processRecordedFields(snapshot)
	if r.recorded != nil && reflect.DeepEqual(*r.recorded, fields) {
		return nil
	}
	record := coremetadata.ProcessSessionRecord{Provider: aiModeClaude, Binding: metadataProcessBinding(r.Binding), SessionID: snapshot.Session, ConnectionID: snapshot.Connection, TurnID: snapshot.Turn, ResumeState: coremetadata.ProcessResumeUnknown}
	if r.Provider == aiModeCodex {
		record.Provider, record.SessionID, record.ThreadID = aiModeCodex, "", snapshot.Session
	}
	for _, request := range snapshot.Pending {
		record.Pending = append(record.Pending, processRecordedControl(request))
	}
	err := updateProcessAgentSession(r.registryPath, r.Binding, nil, true, snapshot.Session != "", nil, func(reg *coremetadata.Registry) error {
		activation, _, current := reg.CurrentProcessActivation(record.Binding)
		if !current {
			return processhost.ErrStale
		}
		mutator := intmetadata.DefaultMutator()
		if err := mutator.RecordProcessSession(reg, activation, record); err != nil {
			return err
		}
		agent, _ := reg.Agent(r.Binding.Agent)
		if agent.Status.Activation.State == coremetadata.ActivationPending {
			_, err := mutator.SetAgentActivation(reg, r.Binding.Agent, coremetadata.ActivationAcknowledged, string(coremetadata.InteractionSourceProviderControl), "")
			return err
		}
		return nil
	})
	if err == nil {
		r.recorded = &fields
	}
	return err
}

func (c *createCommand) rollbackProcessAgent(result *processAgentCreateResult) error {
	if result.Handle != nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := result.waitProcessAgent(ctx, nil); err != nil {
			return processCreateCleanupError(*result, err)
		}
	}
	_, err := c.store.update(func(reg *coremetadata.Registry) error {
		pane, ok := reg.Pane(result.Binding.Pane)
		if !ok || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(result.Binding) {
			return processhost.ErrStale
		}
		if result.Handle != nil && (!pane.Status.Activation.IsZero() || pane.Status.LastTermination == nil) {
			return processhost.ErrStale
		}
		return c.store.mutator().DeleteAgent(reg, result.Binding.Agent)
	})
	if err != nil {
		return processCreateCleanupError(*result, err)
	}
	return nil
}

func processCreateCleanupError(result processAgentCreateResult, cause error) error {
	state := processCreateRuntimeUnknown
	if result.waitRecorded {
		state = processCreateRuntimeOffline
	}
	text := fmt.Errorf("runtime=%s; remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux delete agent uid:%s: %w", state, result.Binding.Agent, result.Binding.Pane, result.Binding.Agent, result.Binding.Agent, cause)
	return newProcessCreateError(text, state, processCreateRemainingRefs(result))
}

func processCLIRequest(flags resourceCreateFlags) (processAgentCreateRequest, cli.OutputMode, error) {
	mode, err := resolveLifecycleProjection(canonicalCreateAgent, flags.output)
	if err != nil {
		return processAgentCreateRequest{}, mode, err
	}
	if mode == cli.OutputModePaneID || processCreateUnsupportedScope(flags) {
		return processAgentCreateRequest{}, mode, usageError("create agent --host process requires exactly one Agent and Window; pane-id, tmux placement/anchors, fan-out, and interactive modes are unavailable")
	}
	labels, err := labelMap(flags.labels)
	if err != nil {
		return processAgentCreateRequest{}, mode, MapMetadataError(err)
	}
	opts := processAgentCreateOptions{Provider: flags.provider, Name: flags.name, CWD: flags.cwd, Model: flags.model, Effort: flags.effort, Instructions: flags.persona, Profile: flags.profile, Creator: flags.creator, Payload: flags.payload, AddDirs: flags.addDirs, Labels: labels}
	if len(flags.projects) == 1 {
		opts.Project, err = selector.ParseRef(coremetadata.KindProject, flags.projects[0])
		if err != nil {
			return processAgentCreateRequest{}, mode, MapMetadataError(err)
		}
	}
	if len(flags.windows) == 1 {
		opts.Window, err = selector.ParseRef(coremetadata.KindWindow, flags.windows[0])
		if err != nil {
			return processAgentCreateRequest{}, mode, MapMetadataError(err)
		}
	}
	request, err := newProcessAgentCreateRequest(opts)
	return request, mode, err
}

func (c *createCommand) runProcessAgentCLI(flags resourceCreateFlags, stdout, stderr io.Writer) error {
	request, mode, err := processCLIRequest(flags)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	result, err := c.startProcessAgent(ctx, request)
	if err != nil {
		return c.failProcessCreate(&result, err)
	}
	if err = c.runProcessPostCreate(ctx, result, stderr); err != nil {
		return c.failProcessCreate(&result, fmt.Errorf("process-post-create-hook-failed: %w", err))
	}
	control, err := c.newProcessCreateControl(result)
	if err != nil {
		return c.failProcessCreate(&result, err)
	}
	if err = submitProcessInitialPrompt(ctx, result, flags.payload); err != nil {
		return c.failProcessCreate(&result, err)
	}
	if err = c.writeProcessCreateResult(stdout, stderr, mode, result); err != nil {
		return c.failProcessCreate(&result, err)
	}
	// EOF is owner shutdown. Provider content never uses the owner's stdout.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	syncSnapshot := processSnapshotSynchronizer(func(snapshot processhost.Snapshot) error {
		if err := result.recordProcessSnapshot(snapshot); err != nil {
			return err
		}
		return control.sync(context.WithoutCancel(ctx))
	}, func(snapshot processhost.Snapshot) error {
		if len(snapshot.Pending) > 0 {
			return control.syncControls(context.WithoutCancel(ctx))
		}
		return nil
	})
	snapshot, waitErr := result.waitProcessAgent(ctx, syncSnapshot)
	// A closed authority still closes answer records and projects termination.
	controlErr := control.syncControls(context.Background())
	if errors.Is(controlErr, processhost.ErrClosed) || errors.Is(controlErr, processhost.ErrStale) {
		controlErr = nil
	}
	waitErr = errors.Join(waitErr, controlErr, control.attention.sync(result.Handle, result.Binding))
	if waitErr != nil {
		if ended, ok := processOwnerEnded(result.registryPath, result.Binding, result.waitRecorded, snapshot, waitErr, stderr); ok {
			return ended
		}
		return processCreateCleanupError(result, waitErr)
	}
	return processWaitExit(snapshot)
}

func (c *createCommand) failProcessCreate(result *processAgentCreateResult, cause error) error {
	if result.Binding.Agent == "" {
		return newProcessCreateError(cause, processCreateRuntimeNone, processCreateRemaining{})
	}
	if err := c.rollbackProcessAgent(result); err != nil {
		var cleanup *processCreateError
		if errors.As(err, &cleanup) {
			return newProcessCreateError(errors.Join(cause, err), cleanup.Runtime, cleanup.Remaining)
		}
		return newProcessCreateError(errors.Join(cause, err), processCreateRuntimeUnknown, processCreateRemainingRefs(*result))
	}
	return newProcessCreateError(fmt.Errorf("%w; remaining: none", cause), processCreateRuntimeNone, processCreateRemaining{})
}

func (c *createCommand) runProcessPostCreate(ctx context.Context, result processAgentCreateResult, stderr io.Writer) error {
	runner := defaultLifecycleHookRunner()
	if runner == nil {
		return nil
	}
	runner.ProjectHookPrompt = nil
	runner.Logger, runner.PromptWriter = stderr, stderr
	reg, err := c.store.snapshot()
	if err != nil {
		return err
	}
	agent, ok := reg.Agent(result.Binding.Agent)
	if !ok {
		return processhost.ErrStale
	}
	_, err = runner.Run(ctx, hooks.EventPostCreate, hooks.Context{Runtime: hooks.RuntimeProcess, CWD: agent.Spec.Workspace.CWD, Socket: defaultAppSocket})
	return err
}

func (c *createCommand) newProcessClaudeControl(result processAgentCreateResult) (*claudeProcessControl, error) {
	handle, ok := result.Handle.(*processhost.Handle)
	if !ok {
		return nil, errors.New("process Claude handle is unavailable")
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return nil, err
	}
	paths.StateDir = filepath.Dir(filepath.Dir(result.registryPath))
	attention := newProcessAttentionStore(paths.StateDir)
	if err = attention.activate(result.Binding, aiModeClaude, ""); err != nil {
		return nil, err
	}
	control := &claudeProcessControl{questionAnswering: func() config.AgentQuestionAnswering { return questionAnsweringFromPaths(paths) }, handle: handle, binding: result.Binding, questions: agentquestion.NewStore(paths.StateDir), approvals: agentapproval.NewStore(paths.StateDir), now: time.Now,
		questionWindow: time.Duration(loadCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second, approvalWindow: time.Duration(loadCentralAgentApprovalWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second,
		attention: &processAttentionProjection{store: attention, queue: notify.NewDefaultStore(paths)}}
	processQuestionCallbacks.Store(result.Binding, processQuestionCallback(control.exactQuestions))
	return control, nil
}

func submitProcessInitialPrompt(ctx context.Context, result processAgentCreateResult, payload []string) error {
	if len(payload) == 0 {
		return nil
	}
	snapshot, err := result.Handle.Observe(result.Binding)
	if err != nil {
		return err
	}
	return result.Handle.Turn(ctx, processhost.Authority{Binding: result.Binding, Connection: snapshot.Connection, Session: snapshot.Session}, result.Binding.Operation+"-initial", strings.Join(payload, " "))
}

func (c *createCommand) writeProcessCreateResult(stdout, stderr io.Writer, mode cli.OutputMode, result processAgentCreateResult) error {
	if mode != cli.OutputModeNone {
		ownership := stderr
		if mode == cli.OutputModeDefault {
			ownership = stdout
		}
		if _, err := fmt.Fprintf(ownership, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", result.Binding.Agent, result.Binding.Pane); err != nil {
			return err
		}
	}
	return c.writeResults(stdout, canonicalCreateAgent, mode, coremetadata.KindAgent, []createResult{result.Created})
}

func processWaitExit(snapshot processhost.Snapshot) error {
	if snapshot.Exit == nil {
		return errors.New("owned process has no actual Wait exit")
	}
	code := snapshot.Exit.Code
	if snapshot.Exit.Signal != "" {
		names := map[string]syscall.Signal{"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL}
		sig, ok := names[snapshot.Exit.Signal]
		if !ok {
			for candidate := syscall.Signal(1); candidate < 128; candidate++ {
				if candidate.String() == snapshot.Exit.Signal {
					sig, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			return errors.New("owned process Wait signal is invalid")
		}
		code = 128 + int(sig)
	}
	if code == 0 {
		return nil
	}
	return superviseExitError{code: code}
}

func processCreateUnsupportedScope(flags resourceCreateFlags) bool {
	return len(flags.projects) > 1 || len(flags.windows) > 1 || len(flags.panes) > 0 || len(flags.selectors) > 0 || flags.createWindow || flags.allWindows || flags.placementSet || flags.cwdFrom != "" || flags.dialogueReplyOnly || flags.interactiveOnly
}
