package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/core/terminaltext"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	projectStartupKindTopology = "continue"
	projectStartupKindBack     = "back"

	projectStartupValueTopology = "continue"

	// projectTopologyStartupDescription is the one description shared by the
	// startup picker row and the Settings choice, so the two cannot drift. It
	// names Agents because the row restores them: a saved Agent comes back into
	// its own Pane and, when the Registry recorded a provider session ref, into
	// the conversation it already had.
	projectTopologyStartupDescription = "keep this Project identity; restore saved Windows, shell Panes, and Agents, or create a new Window and shell when none remain"
)

var errProjectStartupBack = errors.New("project startup back")
var errProjectTrustDenied = errors.New("project trust denied")

type errProjectTrustGate struct {
	err error
}

func (e errProjectTrustGate) Error() string {
	return "project trust gate: " + e.err.Error()
}

func (e errProjectTrustGate) Unwrap() error {
	return e.err
}

type switchProjectTrustAuthorizer interface {
	AuthorizeProjectHooks(ctx context.Context, cwd string) (bool, error)
}

type projectStartupCandidate struct {
	Kind        string
	Name        string
	Label       string
	Description string
}

// projectOpenRequest is the typed authority handoff for one Project open. The
// sidebar path fills Anchor from its required hidden-command operand; ordinary
// in-process opens leave it blank and retain their existing invocation route.
type projectOpenRequest struct {
	Target      string
	SessionName string
	Mode        projectStartupCandidate
	Anchor      string
}

type projectSessionRequest struct {
	SessionName string
	CWD         string
	Opened      openedProjectBootstrap
	Anchor      string
}

type projectTopologyMaterializeRequest struct {
	Root        string
	SessionName string
	Anchor      string
}

func (c *switchCommand) openProjectTarget(ctx context.Context, target, sessionName string) error {
	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		return c.openProjectSession(ctx, sessionName)
	}
	mode := projectStartupCandidate{Kind: projectStartupKindTopology}
	if sidebarStartupPickerEnabled(c.homeDir, c.lookupEnv) {
		mode = c.pickProjectStartupMode(sessionName, target)
	} else if cleanOptionalPath(target) != switchSettingsSentinel && cleanOptionalPath(target) != switchRuntimeSentinel &&
		!c.openedRootIsHome(target) {
		if starter, ok := c.projectFreshStart.(interface {
			ProjectRegistered(string) (bool, error)
		}); ok {
			registered, readErr := starter.ProjectRegistered(target)
			if readErr != nil {
				return readErr
			}
			if !registered {
				mode = projectStartupCandidate{Kind: projectStartupKindNew}
			}
		}
	}
	if mode.Kind == projectStartupKindBack {
		return errProjectStartupBack
	}
	return c.authorizeAndContinueProjectOpen(ctx, target, sessionName, mode)
}

func (c *switchCommand) authorizeAndContinueProjectOpen(ctx context.Context, target, sessionName string, mode projectStartupCandidate) (err error) {
	return c.authorizeAndContinueProjectOpenRequest(ctx, projectOpenRequest{Target: target, SessionName: sessionName, Mode: mode})
}

func (c *switchCommand) authorizeAndContinueProjectOpenRequest(ctx context.Context, request projectOpenRequest) (err error) {
	trusted, err := c.authorizeProjectOpen(ctx, request.Target)
	if err != nil {
		return errProjectTrustGate{err: err}
	}
	if !trusted {
		return errProjectTrustDenied
	}
	// The parser-level preflight happened before the sidebar popup was closed.
	// Reobserve the same typed anchor after trust and immediately before the
	// first preparation/fresh-prune path that can write Registry state. This
	// closes a stale/foreign authority change between those two boundaries.
	if strings.TrimSpace(request.Anchor) != "" {
		if c.validateProjectOpenRoute == nil {
			return errors.New("Project open route validator is not configured")
		}
		if err := c.validateProjectOpenRoute(ctx, request.Anchor); err != nil {
			return err
		}
	}
	if request.Mode.Kind == projectStartupKindNew && !c.openedRootIsHome(request.Target) {
		_, err = c.continueProjectOpenRequest(ctx, request, openedProjectBootstrap{})
		return err
	}
	opened, err := c.prepareProjectContinue(ctx, request.Target, request.SessionName)
	if err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "preparation",
			opened.project.Metadata.UID, opened.project.Metadata.UID, err)
	}
	_, err = c.continueProjectOpenRequest(ctx, request, opened)
	return err
}

// validateSidebarProjectOpenRoute proves that the detached sidebar anchor is
// one exact Pane on the observed app-owned server. An unmanaged/control client
// Pane stays valid without being promoted into Registry ownership; if managed
// mirrors are present, they must resolve to one exact Registry owner chain
// rooted in a Project or ControlSession. The opened target Project may differ.
func validateSidebarProjectOpenRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	lookupEnv func(string) string,
	readRegistry func() (coremetadata.Registry, error),
	anchor string,
) error {
	route, err := resolveInvocationRuntimeMutationRouteWithAnchor(ctx, runner, lookupEnv, anchor)
	if err != nil {
		return err
	}
	anchor = strings.TrimSpace(anchor)
	if route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.PaneID != anchor ||
		exactTmuxHandle(route.authority.SessionID, "$") == "" || exactTmuxHandle(route.authority.WindowID, "@") == "" {
		return errors.New("runtime mutation route: sidebar anchor has no exact app-owned $/@/% authority")
	}
	if readRegistry == nil {
		return errors.New("runtime mutation route: sidebar anchor Registry reader is not configured")
	}
	registry, err := readRegistry()
	if err != nil {
		return fmt.Errorf("runtime mutation route: read sidebar anchor ownership: %w", err)
	}
	mirror := intmetadata.NewMirror(explicitTmuxRunner{runner: runner, target: route.target})
	paneUID, paneErr := mirror.ResolvePaneUID(ctx, anchor)
	windowUID, windowErr := mirror.ResolveWindowUID(ctx, anchor)
	if paneErr != nil || windowErr != nil {
		return errors.New("runtime mutation route: sidebar anchor managed ownership is unreadable")
	}
	paneUID, windowUID = strings.TrimSpace(paneUID), strings.TrimSpace(windowUID)
	// An exact app-owned but unmanaged/control client Pane is a valid transport
	// anchor and is deliberately not promoted into Project ownership. Once
	// either managed mirror exists, however, require and verify the complete
	// Pane -> Window -> Project/ControlSession Registry chain.
	if paneUID == "" && windowUID == "" {
		return nil
	}
	if paneUID == "" || windowUID == "" {
		return errors.New("runtime mutation route: sidebar anchor has incomplete managed ownership")
	}
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return errors.New("runtime mutation route: sidebar anchor Pane ownership is absent from the Registry")
	}
	window, ok := registry.Window(windowUID)
	if !ok {
		return errors.New("runtime mutation route: sidebar anchor Window ownership is absent from the Registry")
	}
	paneOwned := false
	if owner := pane.Metadata.OwnerRef; owner != nil {
		switch owner.Kind {
		case coremetadata.KindWindow:
			paneOwned = owner.UID == window.Metadata.UID
		case coremetadata.KindAgent:
			agent, exists := registry.Agent(owner.UID)
			paneOwned = exists && agent.Metadata.OwnerUID() == window.Metadata.UID && agent.Status.PaneRef == pane.Metadata.UID
		}
	}
	if !paneOwned {
		return errors.New("runtime mutation route: sidebar anchor Pane/Window ownership mismatch")
	}
	owner := window.Metadata.OwnerRef
	if owner == nil {
		return errors.New("runtime mutation route: sidebar anchor Window has no managed root owner")
	}
	switch owner.Kind {
	case coremetadata.KindProject:
		if _, ok := registry.Project(owner.UID); !ok {
			return errors.New("runtime mutation route: sidebar anchor Project ownership mismatch")
		}
	case coremetadata.KindControlSession:
		if _, ok := registry.ControlSession(owner.UID); !ok {
			return errors.New("runtime mutation route: sidebar anchor ControlSession ownership mismatch")
		}
	default:
		return errors.New("runtime mutation route: sidebar anchor has a foreign root owner")
	}
	if (window.Status.RuntimeSessionID != "" && window.Status.RuntimeSessionID != route.authority.SessionID) ||
		(window.Status.RuntimeID != "" && window.Status.RuntimeID != route.authority.WindowID) {
		return errors.New("runtime mutation route: sidebar anchor Registry/runtime ownership mismatch")
	}
	return nil
}

func (c *switchCommand) prepareProjectContinue(ctx context.Context, target, sessionName string) (openedProjectBootstrap, error) {
	cleaned := cleanOptionalPath(target)
	if cleaned == "" || cleaned == switchSettingsSentinel || cleaned == switchRuntimeSentinel || c.openedRootIsHome(cleaned) {
		return c.registerOpenedProjectRoot(ctx, target)
	}
	if starter, ok := c.projectFreshStart.(interface {
		ContinueProject(context.Context, string, string) (openedProjectBootstrap, error)
	}); ok {
		return starter.ContinueProject(ctx, target, sessionName)
	}
	return c.registerOpenedProjectRoot(ctx, target)
}

func (c *switchCommand) continueProjectOpenRequest(ctx context.Context, request projectOpenRequest, opened openedProjectBootstrap) (diagnostics.SessionStateCounts, error) {
	switch request.Mode.Kind {
	case projectStartupKindNew:
		return diagnostics.SessionStateCounts{}, c.startProjectFresh(ctx, request.SessionName, request.Target, opened, request.Anchor)
	default:
		oldUID := opened.project.Metadata.UID
		if err := c.materializeProjectTopology(ctx, projectTopologyMaterializeRequest{
			Root: request.Target, SessionName: request.SessionName, Anchor: request.Anchor,
		}, opened); err != nil {
			return diagnostics.SessionStateCounts{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "topology-materialization", oldUID, oldUID, err)
		}
		if err := c.openProjectSession(ctx, request.SessionName); err != nil {
			return diagnostics.SessionStateCounts{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "client-handoff", oldUID, oldUID, err)
		}
		return diagnostics.SessionStateCounts{}, nil
	}
}

func (c *switchCommand) authorizeProjectOpen(ctx context.Context, target string) (bool, error) {
	authorizer, ok := c.sessions.(switchProjectTrustAuthorizer)
	if !ok || authorizer == nil {
		return true, nil
	}
	trusted, err := authorizer.AuthorizeProjectHooks(ctx, target)
	if err != nil {
		return false, err
	}
	return trusted, nil
}

// pickProjectStartupMode runs the startup screen and returns the approved start.
func (c *switchCommand) pickProjectStartupMode(sessionName, target string) projectStartupCandidate {
	candidates := c.projectStartupCandidates(sessionName, target)
	if len(candidates) == 0 {
		return projectStartupCandidate{Kind: projectStartupKindTopology}
	}
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, projectStartupPickerOptions(candidates))
	if err != nil {
		if isNoSelectionExit(err) {
			return projectStartupCandidate{Kind: projectStartupKindBack}
		}
		return projectStartupCandidate{Kind: projectStartupKindTopology}
	}
	if strings.TrimSpace(result.Value) == "" {
		return projectStartupCandidate{Kind: projectStartupKindBack}
	}
	candidate, ok := projectStartupCandidateFromValue(result.Value)
	if !ok {
		return projectStartupCandidate{Kind: projectStartupKindTopology}
	}
	return candidate
}

func (c *switchCommand) projectStartupCandidates(sessionName, target string) []projectStartupCandidate {
	locale := appLocale(c.homeDir, c.lookupEnv)
	return []projectStartupCandidate{topologyProjectStartupCandidate(locale), newProjectStartupCandidate(locale)}
}

// topologyProjectStartupCandidate is the non-snapshot start row. It materializes
// the Project's own Registry Window and shell Pane topology, so it is a start
// action rather than the `Empty session` it used to advertise.
func topologyProjectStartupCandidate(locales ...i18n.Locale) projectStartupCandidate {
	locale := settingsLocale()
	if len(locales) > 0 {
		locale = locales[0]
	}
	return projectStartupCandidate{
		Kind:        projectStartupKindTopology,
		Label:       localizeUIText(locale, "Continue project"),
		Description: localizeUIText(locale, projectTopologyStartupDescription),
	}
}

func projectStartupPickerOptions(candidates []projectStartupCandidate) intpickercompat.Options {
	entries := make([]intpickercompat.Entry, 0, len(candidates))
	for _, candidate := range candidates {
		entries = append(entries, intpickercompat.Entry{
			Label:     projectStartupPickerLabel(candidate),
			Value:     projectStartupPickerValue(candidate),
			SearchKey: strings.TrimSpace(candidate.Label + " " + terminaltext.EscapeControls(candidate.Name) + " " + candidate.Description),
		})
	}
	return intpickercompat.Options{
		UI:            "project-startup",
		Prompt:        settingsCatalogText("Start project > "),
		Header:        settingsCatalogText("Start project"),
		Footer:        "Enter: open  |  Esc: projects",
		Entries:       entries,
		Bindings:      settingsCloseBindings(),
		DisableSearch: true,
	}
}

func projectStartupPickerLabel(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindTopology:
		return settingsLabel(settingsGlyphOpen, settingsColorType, candidate.Label, candidate.Description)
	case projectStartupKindNew:
		return settingsLabel(settingsGlyphOpen, settingsColorType, candidate.Label, candidate.Description)
	default:
		return settingsLabel(settingsGlyphInfo, settingsColorInfo, candidate.Label, candidate.Description)
	}
}

func projectStartupPickerValue(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindTopology:
		return projectStartupValueTopology
	case projectStartupKindNew:
		return projectStartupValueNew
	default:
		return ""
	}
}

func projectStartupCandidateFromValue(value string) (projectStartupCandidate, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == projectStartupValueTopology:
		return projectStartupCandidate{Kind: projectStartupKindTopology}, true
	case value == projectStartupValueNew:
		return projectStartupCandidate{Kind: projectStartupKindNew}, true
	default:
		return projectStartupCandidate{}, false
	}
}

// ensureProjectSession is the shipped first-session plan transaction without
// client handoff. It refuses reduced/unwired callers instead of retaining a raw
// EnsureSession escape hatch outside the managed mutation executor.
func (c *switchCommand) ensureProjectSession(ctx context.Context, request projectTopologyMaterializeRequest, opened openedProjectBootstrap) error {
	if strings.TrimSpace(opened.project.Metadata.UID) == "" {
		return errors.New("ensure Project session: exact Registry Project UID is unavailable; no runtime was created")
	}
	if c.tmuxRunner == nil {
		return errors.New("ensure Project session: tmux mutation runner is not configured; no runtime was created")
	}
	if c.projectSessionPlan == nil {
		return errors.New("ensure Project session: canonical materializer is not configured; no runtime was created")
	}
	return c.projectSessionPlan(ctx, projectSessionRequest{
		SessionName: request.SessionName, CWD: request.Root, Opened: opened, Anchor: request.Anchor,
	})
}

func (c *switchCommand) ensureBootstrappedProjectSessionPlanned(ctx context.Context, request projectSessionRequest) error {
	if c.tmuxRunner == nil {
		return errors.New("ensure bootstrapped Project session: tmux mutation runner is not configured")
	}
	route, err := resolveInvocationRuntimeMutationRouteWithAnchor(ctx, c.tmuxRunner, c.lookupEnv, request.Anchor)
	if err != nil {
		return err
	}
	return materializeProjectSessionCanonical(ctx, newResourceStore(), c.tmuxRunner, route, c.diagnostics, request.SessionName, request.CWD, request.Opened.project)
}

func materializeProjectSessionCanonical(ctx context.Context, store *resourceStore, runner tmuxCommandRunner, route runtimeMutationRoute, recorder *diagnostics.LifecycleRecorder, sessionName, cwd string, project coremetadata.Project) error {
	if store == nil || runner == nil {
		return errors.New("canonical Project session materializer is not configured")
	}
	if route.target.flag == "" || route.target.value == "" || strings.TrimSpace(route.socketName) == "" {
		return errors.New("canonical Project session materializer has no exact app-owned route")
	}
	routed := explicitTmuxRunner{runner: runner, target: route.target}
	client := defaultTmuxClientWithSocketRunner(routed, route.socketName, recorder)
	runtime := &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: client,
		target: route.target, expectedSocketPath: route.expectedSocketPath, warn: io.Discard,
		socketName: route.socketName, routeAuthority: route.authority,
		executable: os.Executable, lookupEnv: os.Getenv,
	}
	operationID, err := newCreateOperationID()
	if err != nil {
		return err
	}
	ledger := newRuntimeLedger(operationID)
	helper := &createCommand{runtime: runtime}
	_, err = store.update(func(working *coremetadata.Registry) error {
		current, ok := working.Project(project.Metadata.UID)
		if !ok || current.Spec.Root != project.Spec.Root {
			return errors.New("bootstrapped Project declaration drifted before canonical materialization")
		}
		created, err := runtime.ensureSession(ctx, *current, sessionName, ledger)
		if err != nil {
			return err
		}
		if created.Created {
			if err := helper.adoptInitialWindow(ctx, working, store.mutator(), *current, created, ledger); err != nil {
				return err
			}
		}
		if _, err := store.mutator().BindProjectSession(working, current.Metadata.UID, sessionName, true); err != nil {
			return MapMetadataError(err)
		}
		return runtime.finalizeSessionStartup(ctx, created, sessionName, cwd, ledger)
	})
	if err != nil {
		runtime.rollback(ctx, ledger)
		runtime.clearCreateOperations(ctx, ledger)
		return err
	}
	runtime.clearCreateOperations(ctx, ledger)
	return nil
}

func (c *switchCommand) openProjectSession(ctx context.Context, sessionName string) error {
	// A detached sidebar continuation carries an exact client but no inherited
	// TMUX routing. Its final handoff must address the same app socket the
	// ordinary Registry materializer just converged. Interactive/first-use opens
	// without that explicit authority retain the existing session executor.
	if client := c.lookupEnvValue(inttmux.SwitchTargetClientEnv); client != "" && c.tmuxRunner != nil {
		target, err := tmuxSocketNameTarget(defaultAppSocket)
		if err != nil {
			return err
		}
		exact := explicitTmuxRunner{runner: c.tmuxRunner, target: target}
		lookup := func(name string) string {
			if name == inttmux.SwitchTargetClientEnv {
				return client
			}
			return c.lookupEnvValue(name)
		}
		if err := inttmux.NewClient(exact, inttmux.WithLookupEnv(lookup)).OpenSession(ctx, sessionName); err != nil {
			return fmt.Errorf("open tmux session %q: %w", sessionName, err)
		}
		return nil
	}
	if err := c.sessions.OpenSession(ctx, sessionName); err != nil {
		return fmt.Errorf("open tmux session %q: %w", sessionName, err)
	}
	return nil
}

// switchProjectRegistrar is the explicit Project bootstrap seam of the open flow.
//
// Opening an unregistered directory from the sidebar is the gesture that makes it
// a Project. It used to be nothing of the kind: the Project appeared because some
// unrelated mutation had walked the discovery roots and registered everything it
// found, so the sidebar's "unregistered" section was a list of directories that
// were about to be registered whether or not anyone opened them.
//
// The seam registers exactly the path being opened and reports whether that was a
// new Project. A path an existing Project already claims writes nothing, which is
// what makes reopening free.
type switchProjectRegistrar interface {
	RegisterProjectRoot(ctx context.Context, root string) (project coremetadata.Project, created bool, err error)
}

// switchProjectIdentityMirror is the identity-writer seam of the open flow.
//
// Its one method is spelled exactly as the shipped writer spells it, so
// `intmetadata.Mirror` satisfies it with no adapter. That is the point: the first
// open must not grow a second mirror implementation, and no tmux `set-option` for
// Project identity is ever assembled outside that writer.
type switchProjectIdentityMirror interface {
	MirrorProject(ctx context.Context, sessionName string, project coremetadata.Project) error
}

// openedProjectBootstrap is what one explicit open bootstrapped.
//
// It travels as one value rather than a bare `bootstrapped bool` because the two
// facts are only useful together: the flag decides whether this open owns the new
// session's identity, and the Project is that identity. A zero value is the honest
// answer for every open that minted nothing -- an unwired registrar, a sentinel
// target, Home -- so nothing downstream can mistake a withheld registration for a
// half-filled one.
type openedProjectBootstrap struct {
	project             coremetadata.Project
	bootstrapped        bool
	materializeTopology bool
}

// registerOpenedProjectRoot is the bootstrap step of one explicit open.
//
// It runs after the trust gate and before any materialization, in that order on
// purpose. A denied trust prompt must leave the Registry exactly as it was -- the
// operator declined to open the directory, which is not the moment to record that
// it is a Project -- and materialization needs the Registry topology this step
// declares.
// It reports whether this open is what created the Project and which Project that
// is. Together those decide how the runtime is brought up and whose identity the
// new session carries; see materializeAndOpenProjectTopology and
// ensureProjectSession.
func (c *switchCommand) registerOpenedProjectRoot(ctx context.Context, target string) (openedProjectBootstrap, error) {
	if c.projectRegistrar == nil {
		return openedProjectBootstrap{}, nil
	}
	target = cleanOptionalPath(target)
	if target == "" || target == switchSettingsSentinel || target == switchRuntimeSentinel {
		return openedProjectBootstrap{}, nil
	}
	if c.openedRootIsHome(target) {
		// Home is chrome. It leads the Projects list because it is where the
		// surface starts from, not because it is a member of what the surface
		// orders, and `$HOME` alone is never evidence of a Project. Opening it
		// still opens a session; it just does not mint managed identity, so it
		// answers with a zero Project and nothing downstream has an identity to
		// mirror.
		return openedProjectBootstrap{}, nil
	}
	project, created, err := c.projectRegistrar.RegisterProjectRoot(ctx, target)
	if err != nil {
		return openedProjectBootstrap{}, fmt.Errorf("register Project for %q: %w", target, err)
	}
	return openedProjectBootstrap{project: project, bootstrapped: created}, nil
}

// openedRootIsHome reports whether target is the operator's own home directory.
//
// A home directory that cannot be resolved answers false: the guard exists to
// refuse a specific known path, not to refuse everything when the environment is
// unreadable, and the registration itself is still bounded by the exact path.
func (c *switchCommand) openedRootIsHome(target string) bool {
	homeDir, err := c.resolveHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return false
	}
	return candidates.MatchKey(target) == candidates.MatchKey(homeDir)
}

// defaultSwitchProjectRegistrar registers through the same transaction and the
// same idempotence `projmux create project` uses.
type defaultSwitchProjectRegistrar struct {
	store          *resourceStore
	shell          string
	sessionNameFor func(string) string
}

func newDefaultSwitchProjectRegistrar() *defaultSwitchProjectRegistrar {
	home, _ := os.UserHomeDir()
	namer := coresessions.NewNamer(home)
	return &defaultSwitchProjectRegistrar{
		store:          newResourceStore(),
		shell:          configuredShell(os.Getenv),
		sessionNameFor: namer.SessionName,
	}
}

func (r *defaultSwitchProjectRegistrar) RegisterProjectRoot(ctx context.Context, root string) (coremetadata.Project, bool, error) {
	if r == nil {
		return coremetadata.Project{}, false, nil
	}
	return registerProjectRoot(ctx, r.store, r.shell, r.sessionNameFor, root)
}

// switchProjectTopologyMaterializer is the closed-Project activation seam.
//
// A `false, nil` result means the exact Project root declares no Registry
// desired topology -- an unregistered directory, or a Project with no Registry
// Window. The caller then enters the same canonical typed first-session
// transaction; it never falls back to Client.EnsureSession. Everything else is
// an error: a refusal, a failed preflight, and a rolled-back partial
// materialization all reach the caller as one, so the client is never moved
// into a session the topology never reached.
type switchProjectTopologyMaterializer interface {
	MaterializeProjectTopology(ctx context.Context, request projectTopologyMaterializeRequest) (bool, error)
}

func (c *switchCommand) materializeProjectTopology(ctx context.Context, request projectTopologyMaterializeRequest, opened openedProjectBootstrap) error {
	if c.projectTopology != nil && (!opened.bootstrapped || opened.materializeTopology) {
		materialized, err := c.projectTopology.MaterializeProjectTopology(ctx, request)
		if err != nil {
			return fmt.Errorf("materialize Registry topology for session %q: %w", request.SessionName, err)
		}
		if materialized {
			return nil
		}
	}
	return c.ensureProjectSession(ctx, request, opened)
}

// registryProjectTopologyMaterializer runs closed-Project activation through the
// same exact-socket engine `reconcile resources --materialize-project` uses, so
// the ownership, rollback, no-stored-command, and no-Agent-autostart contract has
// one implementation rather than a startup-flavored copy.
type registryProjectTopologyMaterializer struct {
	resources          *resourceStore
	runner             tmuxCommandRunner
	target             explicitTmuxTarget
	expectedSocketPath string
	socketName         string
	routeAuthority     *runtimeMutationRouteAuthority
	resolveRoute       func(context.Context, string) (runtimeMutationRoute, error)
	diagnostics        *diagnostics.LifecycleRecorder
	newReconciler      func(tmuxCommandRunner, sessionLister) *registryReconciler
	newOperationID     func() (string, error)
	newGeneration      func() (string, error)
	newMaterializer    func(tmuxCommandRunner, io.Writer) *materializer
	warn               io.Writer
	// agents is the provider-launch seam replayed Agents are launched through.
	// It is injected from the process wiring rather than constructed here so
	// startup, `create agent`, and `agent resume` share one object.
	agents topologyAgentLauncher
	// notices is where the operator is told which stored Agents did not rejoin
	// their recorded conversation. It is not the discarded rollback stream:
	// silently substituting a new conversation is exactly the failure Phase 0
	// existed to prevent.
	//
	// Production wires a projectStartupNoticeSink, which mirrors every line to
	// stderr and flushes the batch to `tmux display-message`. The field stays a
	// plain io.Writer so a fixture can keep passing a bytes.Buffer; the flush is
	// opt-in through projectStartupNoticeFlusher.
	notices io.Writer
}

// newRegistryProjectTopologyMaterializer binds activation to the app's own
// `-L projmux` socket. Startup never infers a socket from an inherited client,
// which is the same exact-target rule the reconcile and delete routes follow.
func newRegistryProjectTopologyMaterializer(recorders ...*diagnostics.LifecycleRecorder) *registryProjectTopologyMaterializer {
	runner := inttmux.ExecRunner{}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		panic(err)
	}
	materializer := &registryProjectTopologyMaterializer{
		resources:   newResourceStore(),
		runner:      runner,
		target:      target,
		diagnostics: recorderFrom(recorders),
		warn:        io.Discard,
		// The disclosure surface is the shared stderr/display-message tee this
		// Phase settled on for every closed-Project startup report. See
		// projectStartupNoticeSink: a popup guarantees the emit and denies the
		// read, so stderr alone was a disclosure nobody received.
		notices: newProjectStartupNoticeSink(runner),
	}
	materializer.resolveRoute = func(ctx context.Context, anchor string) (runtimeMutationRoute, error) {
		return resolveInvocationRuntimeMutationRouteWithAnchor(ctx, runner, os.Getenv, anchor)
	}
	return materializer
}

func (m *registryProjectTopologyMaterializer) MaterializeProjectTopology(ctx context.Context, request projectTopologyMaterializeRequest) (bool, error) {
	root, sessionName := strings.TrimSpace(request.Root), strings.TrimSpace(request.SessionName)
	if m == nil || m.resources == nil || root == "" || sessionName == "" {
		return false, nil
	}
	projectRef, ok, err := m.desiredTopologyRef(root)
	if err != nil || !ok {
		return false, err
	}
	if m.resolveRoute != nil {
		route, routeErr := m.resolveRoute(ctx, request.Anchor)
		if routeErr != nil {
			return false, routeErr
		}
		m.target = route.target
		m.expectedSocketPath = route.expectedSocketPath
		m.socketName = route.socketName
		m.routeAuthority = route.authority
	}
	if err := requireAutomaticRecoveryPaths("project-open-materialize", "project-open-skip-item"); err != nil {
		return false, err
	}
	route := runtimeMutationRoute{
		target: m.target, expectedSocketPath: m.expectedSocketPath,
		socketName: m.socketName, authority: m.routeAuthority,
	}
	var recoveryErr error
	if route.expectedSocketPath != "" && route.authority != nil {
		_, recoveryErr = runLockedAutomaticMirrorRecovery(ctx, m.resources, m.runner, m.target, controller.RecoveryProjectOpen, route)
	} else {
		_, recoveryErr = runLockedAutomaticMirrorRecovery(ctx, m.resources, m.runner, m.target, controller.RecoveryProjectOpen)
	}
	if recoveryErr != nil {
		return false, recoveryErr
	}
	planner := resourceReconcilePlanner{
		reader:             explicitTmuxRunner{runner: m.runner, target: m.target},
		store:              m.resources,
		newReconciler:      m.newReconciler,
		materializeProject: projectRef,
		materializeSession: sessionName,
		exactTarget:        m.target,
		agents:             m.agents,
	}
	run := topologyMaterializeRun{
		resources:          m.resources,
		runner:             m.runner,
		target:             m.target,
		expectedSocketPath: m.expectedSocketPath,
		diagnostics:        m.diagnostics,
		socketName:         m.socketName,
		routeAuthority:     m.routeAuthority,
		newOperationID:     m.newOperationID,
		newGeneration:      m.newGeneration,
		newMaterializer:    m.newMaterializer,
		agents:             m.agents,
		notices:            m.notices,
		recoveryTrigger:    controller.RecoveryProjectOpen,
	}
	warn := m.warn
	if warn == nil {
		warn = io.Discard
	}
	outcome, err := run.execute(ctx, planner, warn)
	// The plan writes its Agent disclosures line by line during the transaction;
	// the flush is what turns them into the one message the operator actually
	// sees. It runs on both outcomes: a failed activation is exactly when the
	// "this Agent did not rejoin its conversation" line matters most.
	flushProjectStartupNotices(m.notices)
	if err != nil {
		stage := outcome.failedStage
		if stage == "" {
			stage = "topology activation"
		}
		return false, fmt.Errorf("%s: %w", stage, MapMetadataError(err))
	}
	return true, nil
}

// desiredTopologyRef resolves the exact Project selector for a root that declares
// Registry topology.
//
// The read is the zero-write snapshot read on purpose: opening a directory that
// was never registered must not create the Registry state directory, so an
// absent Registry and a Project with no Registry Window both answer "nothing to
// materialize" rather than failing the open.
func (m *registryProjectTopologyMaterializer) desiredTopologyRef(root string) (string, bool, error) {
	read := m.resources.snapshot
	if read == nil {
		read = m.resources.load
	}
	if read == nil {
		return "", false, nil
	}
	registry, err := read()
	if err != nil {
		return "", false, MapMetadataError(err)
	}
	project, ok := registry.ProjectByRoot(root)
	if !ok {
		return "", false, nil
	}
	if len(registry.WindowsOf(project.Metadata.UID)) == 0 {
		return "", false, nil
	}
	return selector.UIDPrefix + project.Metadata.UID, true, nil
}
