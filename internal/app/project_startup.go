package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/core/selector"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/core/terminaltext"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	projectStartupKindLatest   = "latest"
	projectStartupKindNamed    = "named"
	projectStartupKindTopology = "topology"
	projectStartupKindBack     = "back"

	projectStartupValueLatest   = "latest"
	projectStartupValueTopology = "topology"
	projectStartupValueNamed    = "named:"

	// projectStartupValueEmpty is the retired spelling of the non-snapshot start
	// row. It is still accepted on the internal sidebar-open transport so a
	// re-exec that straddles an upgrade keeps working; it resolves to the
	// topology start, which is what the row always claimed to be doing.
	projectStartupValueEmpty = "empty"

	// projectTopologyStartupDescription is the one description shared by the
	// startup picker row and the Settings choice, so the two cannot drift. It
	// names Agents because the row restores them: a saved Agent comes back into
	// its own Pane and, when the Registry recorded a provider session ref, into
	// the conversation it already had.
	projectTopologyStartupDescription = "restore every saved Window, shell Pane, and Agent"
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

type switchProjectLayoutTrustAuthorizer interface {
	AuthorizeProjectLayout(ctx context.Context, cwd string, artifact corelayout.Artifact) (bool, error)
}

type switchSessionSnapshotRestorer interface {
	RestoreSessionSnapshot(ctx context.Context, snap sessionstate.Snapshot, cwd, source string) error
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
	started := time.Now()
	var counts diagnostics.SessionStateCounts
	var diagnosticSource diagnostics.SessionStateSource
	switch mode.Kind {
	case projectStartupKindLatest:
		diagnosticSource = diagnostics.SessionStateSourceStartupLatest
	case projectStartupKindNamed:
		diagnosticSource = diagnostics.SessionStateSourceStartupNamed
	}
	if diagnosticSource != "" {
		defer func() {
			c.sessionStateDiagnostics.Record(diagnostics.OperationSessionStateRestore, diagnosticSource, started, counts, err)
		}()
	}
	var layoutArtifact *corelayout.Artifact
	if mode.Kind == projectStartupKindNamed {
		artifact, err := corelayout.NewStore(target).LoadArtifact(mode.Name)
		if err != nil {
			return errProjectTrustGate{err: err}
		}
		layoutArtifact = &artifact
	}
	trusted, err := c.authorizeProjectOpen(ctx, target)
	if err != nil {
		return errProjectTrustGate{err: err}
	}
	if !trusted {
		return errProjectTrustDenied
	}
	if layoutArtifact != nil && len(layoutArtifact.ExecutableCommands()) > 0 {
		trusted, err := c.authorizeProjectLayout(ctx, target, *layoutArtifact)
		if err != nil {
			return errProjectTrustGate{err: err}
		}
		if !trusted {
			return errProjectTrustDenied
		}
	}
	bootstrapped, err := c.registerOpenedProjectRoot(ctx, target)
	if err != nil {
		return err
	}
	counts, err = c.continueProjectOpen(ctx, target, sessionName, mode, layoutArtifact, bootstrapped)
	return err
}

func (c *switchCommand) continueProjectOpen(ctx context.Context, target, sessionName string, mode projectStartupCandidate, layoutArtifact *corelayout.Artifact, bootstrapped bool) (diagnostics.SessionStateCounts, error) {
	switch mode.Kind {
	case projectStartupKindLatest:
		return c.restoreProjectLatestSnapshot(ctx, sessionName, target)
	case projectStartupKindNamed:
		if layoutArtifact == nil {
			return diagnostics.SessionStateCounts{}, errors.New("named snapshot artifact is not prepared")
		}
		return c.restoreProjectNamedSnapshot(ctx, sessionName, target, *layoutArtifact)
	case projectStartupKindNew:
		return diagnostics.SessionStateCounts{}, c.startProjectFresh(ctx, sessionName, target, bootstrapped)
	default:
		return diagnostics.SessionStateCounts{}, c.materializeAndOpenProjectTopology(ctx, sessionName, target, bootstrapped)
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

func (c *switchCommand) authorizeProjectLayout(ctx context.Context, target string, artifact corelayout.Artifact) (bool, error) {
	authorizer, ok := c.sessions.(switchProjectLayoutTrustAuthorizer)
	if !ok || authorizer == nil {
		return false, errors.New("switch project layout trust authorizer is not configured")
	}
	return authorizer.AuthorizeProjectLayout(ctx, target, artifact)
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
			return projectStartupCandidate{Kind: projectStartupKindTopology}
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
			c.reportProjectStartup("projmux: fresh start confirmation could not be shown: " + err.Error())
			return projectStartupCandidate{Kind: projectStartupKindTopology}
		}
		if approved {
			return candidate
		}
		c.reportProjectStartup(projectStartupNewCanceledMessage)
	}
}

// approveProjectFreshStart plans the prune and asks for approval.
//
// Cancel is zero writes by construction: planning is the read-only snapshot read,
// the confirmation is a picker, and neither one reaches resourceStore.mutate,
// sessionstate.Store.Delete, or any tmux command that changes tmux state.
func (c *switchCommand) approveProjectFreshStart(sessionName, target string) (bool, error) {
	plan, err := c.planProjectFreshStart(sessionName, target)
	if err != nil {
		return false, err
	}
	return c.confirmProjectFreshStart(plan)
}

func (c *switchCommand) projectStartupCandidates(sessionName, target string) []projectStartupCandidate {
	var candidates []projectStartupCandidate
	if store, err := c.projectStartupSessionStateStore(); err == nil {
		if summary, err := store.Summary(sessionName); err == nil && summary.Source != sessionstate.SourceFresh {
			candidates = append(candidates, projectStartupCandidate{
				Kind:        projectStartupKindLatest,
				Label:       "Latest snapshot",
				Description: projectStartupDescription("auto-saved", summary.SavedAt, summary.WindowCount, summary.PaneCount),
			})
		}
	}
	if store := corelayout.NewStore(target); strings.TrimSpace(store.ProjectRoot) != "" {
		if entries, _, err := store.List(); err == nil {
			for _, entry := range entries {
				candidates = append(candidates, projectStartupCandidate{
					Kind:        projectStartupKindNamed,
					Name:        entry.Name,
					Label:       "Named snapshot",
					Description: namedSnapshotDescription(entry),
				})
			}
		}
	}
	// The fresh-start row is offered unconditionally, including when the Project
	// declares nothing to prune. "Start this Project clean" is a decision about
	// the start, not about how much saved state happens to exist, and a row that
	// appears and disappears with the Registry would be a row the operator cannot
	// learn. Its confirmation states the real counts, zeroes included.
	if len(candidates) == 0 {
		return []projectStartupCandidate{
			topologyProjectStartupCandidate(),
			newProjectStartupCandidate(),
			backProjectStartupCandidate(),
		}
	}
	candidates = append(candidates, topologyProjectStartupCandidate())
	candidates = append(candidates, newProjectStartupCandidate())
	candidates = append(candidates, backProjectStartupCandidate())
	return candidates
}

// topologyProjectStartupCandidate is the non-snapshot start row. It materializes
// the Project's own Registry Window and shell Pane topology, so it is a start
// action rather than the `Empty session` it used to advertise.
func topologyProjectStartupCandidate() projectStartupCandidate {
	return projectStartupCandidate{
		Kind:        projectStartupKindTopology,
		Label:       "Project topology",
		Description: projectTopologyStartupDescription,
	}
}

func namedSnapshotDescription(entry corelayout.Entry) string {
	parts := []string{terminaltext.EscapeControls(entry.Name)}
	if savedAt := namedSnapshotSavedAt(entry); !savedAt.IsZero() {
		parts = append(parts, projectStartupSavedAtText(savedAt))
	}
	if strings.TrimSpace(entry.Description) != "" {
		parts = append(parts, terminaltext.EscapeControls(strings.TrimSpace(entry.Description)))
	}
	parts = append(parts, sessionStateCount(entry.Windows, "window"), sessionStateCount(entry.Panes, "pane"))
	return strings.Join(parts, ", ")
}

func namedSnapshotSavedAt(entry corelayout.Entry) time.Time {
	if strings.TrimSpace(entry.Path) == "" {
		return time.Time{}
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func projectStartupDescription(source string, savedAt time.Time, windows, panes int) string {
	parts := []string{}
	if savedText := projectStartupSavedAtText(savedAt); savedText != "" {
		parts = append(parts, savedText)
	}
	if strings.TrimSpace(source) != "" {
		parts = append(parts, strings.TrimSpace(source))
	}
	parts = append(parts, sessionStateCount(windows, "window"), sessionStateCount(panes, "pane"))
	return strings.Join(parts, ", ")
}

func projectStartupSavedAtText(savedAt time.Time) string {
	if savedAt.IsZero() {
		return ""
	}
	return "saved " + savedAt.UTC().Format("2006-01-02 15:04:05 MST")
}

func backProjectStartupCandidate() projectStartupCandidate {
	return projectStartupCandidate{
		Kind:        projectStartupKindBack,
		Label:       "Back",
		Description: "return to projects",
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
		Footer:        projmuxFooter("Enter: start  |  New row: discards saved state  |  Back row: projects  |  Esc: Project topology"),
		Entries:       entries,
		Bindings:      settingsCloseBindings(),
		DisableSearch: true,
	}
}

func projectStartupPickerLabel(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindLatest:
		return settingsLabel(settingsGlyphOpen, settingsColorType, "Latest snapshot", candidate.Description)
	case projectStartupKindNamed:
		return settingsLabel(settingsGlyphOpen, settingsColorType, "Named snapshot", candidate.Description)
	case projectStartupKindTopology:
		return settingsLabel(settingsGlyphOpen, settingsColorType, "Project topology", candidate.Description)
	case projectStartupKindNew:
		// The destructive glyph and color are the same pair the notify clear-all
		// confirmation uses. This row starts a Project like the rows above it, but
		// it is the only one that deletes anything, and it has to read that way
		// before it is selected rather than only in the confirmation.
		return settingsLabel(settingsGlyphRemove, settingsColorRemove, projectStartupNewLabel, candidate.Description)
	case projectStartupKindBack:
		return settingsLabel(settingsGlyphBack, settingsColorBack, "Back", candidate.Description)
	default:
		return settingsLabel(settingsGlyphInfo, settingsColorInfo, candidate.Label, candidate.Description)
	}
}

func projectStartupPickerValue(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindLatest:
		return projectStartupValueLatest
	case projectStartupKindNamed:
		return projectStartupValueNamed + candidate.Name
	case projectStartupKindTopology:
		return projectStartupValueTopology
	case projectStartupKindNew:
		return projectStartupValueNew
	case projectStartupKindBack:
		return settingsBackValue
	default:
		return ""
	}
}

func projectStartupCandidateFromValue(value string) (projectStartupCandidate, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == projectStartupValueLatest:
		return projectStartupCandidate{Kind: projectStartupKindLatest}, true
	case value == projectStartupValueTopology, value == projectStartupValueEmpty:
		return projectStartupCandidate{Kind: projectStartupKindTopology}, true
	case value == projectStartupValueNew:
		return projectStartupCandidate{Kind: projectStartupKindNew}, true
	case value == settingsBackValue:
		return projectStartupCandidate{Kind: projectStartupKindBack}, true
	case strings.HasPrefix(value, projectStartupValueNamed):
		name := strings.TrimSpace(strings.TrimPrefix(value, projectStartupValueNamed))
		if name == "" {
			return projectStartupCandidate{}, false
		}
		return projectStartupCandidate{Kind: projectStartupKindNamed, Name: name}, true
	default:
		return projectStartupCandidate{}, false
	}
}

func (c *switchCommand) restoreProjectLatestSnapshot(ctx context.Context, sessionName, target string) (diagnostics.SessionStateCounts, error) {
	store, err := c.projectStartupSessionStateStore()
	if err != nil {
		return diagnostics.SessionStateCounts{}, err
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		return diagnostics.SessionStateCounts{}, err
	}
	counts := sessionStateDiagnosticCounts(snap)
	return counts, c.restoreProjectSnapshot(ctx, snap, target, sessionstate.SourceAutosave)
}

func (c *switchCommand) restoreProjectNamedSnapshot(ctx context.Context, sessionName, target string, artifact corelayout.Artifact) (diagnostics.SessionStateCounts, error) {
	snap, err := corelayout.ToSnapshot(artifact.Preset, sessionName, target, c.projectStartupNow())
	if err != nil {
		return diagnostics.SessionStateCounts{}, err
	}
	counts := sessionStateDiagnosticCounts(snap)
	source := layoutPresetSource(artifact.Name, artifact.Preset)
	if err := c.restoreProjectSnapshot(ctx, snap, target, source); err != nil {
		return diagnostics.SessionStateCounts{}, err
	}
	if source == sessionstate.SourceFresh {
		if store, err := c.projectStartupSessionStateStore(); err == nil {
			_ = store.Delete(sessionName)
		}
	}
	return counts, nil
}

func (c *switchCommand) restoreProjectSnapshot(ctx context.Context, snap sessionstate.Snapshot, target, source string) error {
	restorer, ok := c.sessions.(switchSessionSnapshotRestorer)
	if !ok || restorer == nil {
		return errors.New("switch session snapshot restorer is not configured")
	}
	if err := restorer.RestoreSessionSnapshot(ctx, snap, target, source); err != nil {
		return err
	}
	return c.openProjectSession(ctx, snap.Session)
}

func (c *switchCommand) ensureAndOpenProjectSession(ctx context.Context, sessionName, target string) error {
	if err := c.sessions.EnsureSession(ctx, sessionName, target); err != nil {
		return fmt.Errorf("ensure tmux session %q: %w", sessionName, err)
	}
	return c.openProjectSession(ctx, sessionName)
}

func (c *switchCommand) openProjectSession(ctx context.Context, sessionName string) error {
	if err := c.sessions.OpenSession(ctx, sessionName); err != nil {
		return fmt.Errorf("open tmux session %q: %w", sessionName, err)
	}
	return nil
}

func (c *switchCommand) projectStartupSessionStateStore() (sessionstate.Store, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionstate.Store{}, err
	}
	return sessionstate.NewStore(paths.SessionStateDir()), nil
}

func (c *switchCommand) projectStartupNow() time.Time {
	return time.Now()
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
	RegisterProjectRoot(ctx context.Context, root string) (uid string, created bool, err error)
}

// registerOpenedProjectRoot is the bootstrap step of one explicit open.
//
// It runs after the trust gate and before any materialization, in that order on
// purpose. A denied trust prompt must leave the Registry exactly as it was -- the
// operator declined to open the directory, which is not the moment to record that
// it is a Project -- and materialization needs the Registry topology this step
// declares.
// It reports whether this open is what created the Project, which decides how the
// runtime is brought up; see materializeAndOpenProjectTopology.
func (c *switchCommand) registerOpenedProjectRoot(ctx context.Context, target string) (bool, error) {
	if c.projectRegistrar == nil {
		return false, nil
	}
	target = cleanOptionalPath(target)
	if target == "" || target == switchSettingsSentinel || target == switchRuntimeSentinel {
		return false, nil
	}
	if c.openedRootIsHome(target) {
		// Home is chrome. It leads the Projects list because it is where the
		// surface starts from, not because it is a member of what the surface
		// orders, and `$HOME` alone is never evidence of a Project. Opening it
		// still opens a session; it just does not mint managed identity.
		return false, nil
	}
	_, created, err := c.projectRegistrar.RegisterProjectRoot(ctx, target)
	if err != nil {
		return false, fmt.Errorf("register Project for %q: %w", target, err)
	}
	return created, nil
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

func (r *defaultSwitchProjectRegistrar) RegisterProjectRoot(ctx context.Context, root string) (string, bool, error) {
	if r == nil {
		return "", false, nil
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
// bootstrapped means this open is what registered the Project, and it takes the
// shipped ensure path rather than the topology engine. The two would build the
// same thing -- a Project registered a moment ago has exactly the one-Window,
// one-shell-Pane bootstrap topology EnsureSession produces -- but they build it on
// different servers: the topology engine binds to the app-owned socket by design,
// while a first open of a directory has always started its session on the transport
// the operator is actually in. Reusing the shipped path keeps a first open
// unchanged; every later open of the now-registered Project converges through the
// topology engine exactly as it already did.
func (c *switchCommand) materializeAndOpenProjectTopology(ctx context.Context, sessionName, target string, bootstrapped bool) error {
	if c.projectTopology != nil && !bootstrapped {
		materialized, err := c.projectTopology.MaterializeProjectTopology(ctx, target, sessionName)
		if err != nil {
			return fmt.Errorf("materialize Registry topology for session %q: %w", sessionName, err)
		}
		if materialized {
			return c.openProjectSession(ctx, sessionName)
		}
	}
	return c.ensureAndOpenProjectSession(ctx, sessionName, target)
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
