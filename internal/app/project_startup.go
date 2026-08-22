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
	projectTopologyStartupDescription = "open every saved Window, shell Pane, and Agent"
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
	}
	if mode.Kind == projectStartupKindBack {
		return errProjectStartupBack
	}
	return c.authorizeAndContinueProjectOpen(ctx, target, sessionName, mode)
}

func (c *switchCommand) authorizeAndContinueProjectOpen(ctx context.Context, target, sessionName string, mode projectStartupCandidate) (err error) {
	trusted, err := c.authorizeProjectOpen(ctx, target)
	if err != nil {
		return errProjectTrustGate{err: err}
	}
	if !trusted {
		return errProjectTrustDenied
	}
	opened, err := c.registerOpenedProjectRoot(ctx, target)
	if err != nil {
		return err
	}
	_, err = c.continueProjectOpen(ctx, target, sessionName, mode, opened)
	return err
}

func (c *switchCommand) continueProjectOpen(ctx context.Context, target, sessionName string, mode projectStartupCandidate, opened openedProjectBootstrap) (diagnostics.SessionStateCounts, error) {
	switch mode.Kind {
	case projectStartupKindNew:
		return diagnostics.SessionStateCounts{}, c.startProjectFresh(ctx, sessionName, target, opened)
	default:
		return diagnostics.SessionStateCounts{}, c.materializeAndOpenProjectTopology(ctx, sessionName, target, opened)
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
//
// The loop exists for exactly one row: a cancelled `new` confirmation returns the
// operator to the rows they came from rather than to the Projects list, because
// declining to destroy saved state is not the same gesture as declining to open
// the Project. Every other row leaves on its first pass, and a picker that stops
// answering resolves to the topology start, so the loop cannot spin.
func (c *switchCommand) pickProjectStartupMode(sessionName, target string) projectStartupCandidate {
	for {
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
		if candidate.Kind != projectStartupKindNew {
			return candidate
		}
		approved, err := c.approveProjectFreshStart(sessionName, target)
		if err != nil {
			// A confirmation that could not be shown is not an approval. Falling
			// back to the non-destructive topology start is the only outcome that
			// cannot delete something nobody agreed to.
			format := localizeUIText(appLocale(c.homeDir, c.lookupEnv), "projmux: fresh start confirmation could not be shown: %s")
			c.reportProjectStartup(fmt.Sprintf(format, err.Error()))
			return projectStartupCandidate{Kind: projectStartupKindTopology}
		}
		if approved {
			return candidate
		}
		c.reportProjectStartup(localizeUIText(appLocale(c.homeDir, c.lookupEnv), projectStartupNewCanceledMessage))
	}
}

// approveProjectFreshStart plans the prune and asks for approval.
//
// Cancel is zero writes by construction: planning is the read-only snapshot read,
// the confirmation is a picker, and neither one reaches resourceStore.mutate,
// snapshot storage, or any tmux command that changes tmux state.
func (c *switchCommand) approveProjectFreshStart(sessionName, target string) (bool, error) {
	plan, err := c.planProjectFreshStart(sessionName, target)
	if err != nil {
		return false, err
	}
	return c.confirmProjectFreshStart(plan)
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
		// The destructive glyph and color are the same pair the notify clear-all
		// confirmation uses. This row starts a Project like the rows above it, but
		// it is the only one that deletes anything, and it has to read that way
		// before it is selected rather than only in the confirmation.
		return settingsLabel(settingsGlyphRemove, settingsColorRemove, candidate.Label, candidate.Description)
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

// ensureProjectSession is the shipped first-session start without client
// handoff: create the session if missing, then finish its Project identity.
func (c *switchCommand) ensureProjectSession(ctx context.Context, sessionName, target string, opened openedProjectBootstrap) error {
	if err := c.sessions.EnsureSession(ctx, sessionName, target); err != nil {
		return fmt.Errorf("ensure tmux session %q: %w", sessionName, err)
	}
	if err := c.mirrorBootstrappedProjectIdentity(ctx, sessionName, opened); err != nil {
		return err
	}
	return nil
}

// mirrorBootstrappedProjectIdentity writes the minted Project identity onto the
// session this open just created, and only then.
//
// The gate is `bootstrapped`, not "the mirror looks unset". An open of an
// already-registered Project converges through the topology engine, which already
// owns that session's identity, and a snapshot restore reaches a session the
// Registry did not declare; writing here in either case would put this flow in the
// business of repairing identity it did not mint. Repair has a route already --
// `projmux reconcile resources` plans exactly these two set-options -- and keeping
// it there is what makes a second pass of this flow write nothing at all.
func (c *switchCommand) mirrorBootstrappedProjectIdentity(ctx context.Context, sessionName string, opened openedProjectBootstrap) error {
	if !opened.bootstrapped || c.projectMirror == nil {
		return nil
	}
	if strings.TrimSpace(opened.project.Metadata.UID) == "" {
		return fmt.Errorf("mirror Project identity onto tmux session %q: the bootstrapped Project carries no uid", sessionName)
	}
	if err := c.projectMirror.MirrorProject(ctx, sessionName, opened.project); err != nil {
		return fmt.Errorf("mirror Project identity onto tmux session %q: %w", sessionName, err)
	}
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
	project      coremetadata.Project
	bootstrapped bool
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
// Window -- which is the ordinary case for a first open and keeps the historic
// `EnsureSession` behavior. Everything else is an error: a refusal, a failed
// preflight, and a rolled-back partial materialization all reach the caller as
// one, so the client is never moved into a session the topology never reached.
type switchProjectTopologyMaterializer interface {
	MaterializeProjectTopology(ctx context.Context, root, sessionName string) (bool, error)
}

// materializeAndOpenProjectTopology is the non-snapshot closed-Project start.
// The client move is strictly last: materialization either converges the whole
// declared shell topology or fails without an open.
//
// A bootstrapped open -- this open is what registered the Project -- takes the
// shipped ensure path rather than the topology engine. The two would build the
// same thing -- a Project registered a moment ago has exactly the one-Window,
// one-shell-Pane bootstrap topology EnsureSession produces -- but they build it on
// different servers: the topology engine binds to the app-owned socket by design,
// while a first open of a directory has always started its session on the transport
// the operator is actually in. Reusing the shipped path keeps a first open
// unchanged; every later open of the now-registered Project converges through the
// topology engine exactly as it already did.
//
// The Project itself travels with the flag because that ensure path is the only
// route that has to finish the identity mirror itself; see
// ensureProjectSession.
func (c *switchCommand) materializeAndOpenProjectTopology(ctx context.Context, sessionName, target string, opened openedProjectBootstrap) error {
	if err := c.materializeProjectTopology(ctx, sessionName, target, opened); err != nil {
		return err
	}
	return c.openProjectSession(ctx, sessionName)
}

func (c *switchCommand) materializeProjectTopology(ctx context.Context, sessionName, target string, opened openedProjectBootstrap) error {
	if c.projectTopology != nil && !opened.bootstrapped {
		materialized, err := c.projectTopology.MaterializeProjectTopology(ctx, target, sessionName)
		if err != nil {
			return fmt.Errorf("materialize Registry topology for session %q: %w", sessionName, err)
		}
		if materialized {
			return nil
		}
	}
	return c.ensureProjectSession(ctx, sessionName, target, opened)
}

// registryProjectTopologyMaterializer runs closed-Project activation through the
// same exact-socket engine `reconcile resources --materialize-project` uses, so
// the ownership, rollback, no-stored-command, and no-Agent-autostart contract has
// one implementation rather than a startup-flavored copy.
type registryProjectTopologyMaterializer struct {
	resources       *resourceStore
	runner          tmuxCommandRunner
	target          explicitTmuxTarget
	newReconciler   func(tmuxCommandRunner, sessionLister) *registryReconciler
	newOperationID  func() (string, error)
	newGeneration   func() (string, error)
	newMaterializer func(tmuxCommandRunner, io.Writer) *materializer
	warn            io.Writer
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
func newRegistryProjectTopologyMaterializer() *registryProjectTopologyMaterializer {
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		panic(err)
	}
	return &registryProjectTopologyMaterializer{
		resources: newResourceStore(),
		runner:    inttmux.ExecRunner{},
		target:    target,
		warn:      io.Discard,
		// The disclosure surface is the shared stderr/display-message tee this
		// Phase settled on for every closed-Project startup report. See
		// projectStartupNoticeSink: a popup guarantees the emit and denies the
		// read, so stderr alone was a disclosure nobody received.
		notices: newProjectStartupNoticeSink(inttmux.ExecRunner{}),
	}
}

func (m *registryProjectTopologyMaterializer) MaterializeProjectTopology(ctx context.Context, root, sessionName string) (bool, error) {
	root, sessionName = strings.TrimSpace(root), strings.TrimSpace(sessionName)
	if m == nil || m.resources == nil || root == "" || sessionName == "" {
		return false, nil
	}
	projectRef, ok, err := m.desiredTopologyRef(root)
	if err != nil || !ok {
		return false, err
	}
	if err := requireAutomaticRecoveryPaths("project-open-materialize", "project-open-skip-item"); err != nil {
		return false, err
	}
	if _, err := runLockedAutomaticMirrorRecovery(ctx, m.resources, m.runner, m.target, controller.RecoveryProjectOpen); err != nil {
		return false, err
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
		resources:       m.resources,
		runner:          m.runner,
		target:          m.target,
		newOperationID:  m.newOperationID,
		newGeneration:   m.newGeneration,
		newMaterializer: m.newMaterializer,
		agents:          m.agents,
		notices:         m.notices,
		recoveryTrigger: controller.RecoveryProjectOpen,
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
