package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// registryReconciler brings the resource registry up to date with the machine
// before a mutation route resolves its selectors.
//
// This exists because a `--project <name>` selector has nothing to resolve
// against until something registers Projects. Reconciliation is a mutation-route
// concern only: the read verbs keep LoadReadOnly and must never create the
// registry for an operator who has not run a mutation.
//
// Order is load bearing.
//
//  1. Import live tmux sessions first. A session that predates the resource
//     model carries the operator's real Windows and Panes, and importing it is
//     what seeds their stable names once. Registering the same root from
//     discovery first would create a default one-Window topology and the
//     importer would then skip the session, because a Project that already owns
//     Windows is never re-imported.
//  2. Register the remaining selectable workdirs with the bootstrap topology.
//  3. Observe roots and refresh the live session projection.
//  4. Release every Agent whose managed Pane no longer exists in tmux.
//
// Nothing here renumbers or merges an existing Project: first registration wins
// and names stay stable.
type registryReconciler struct {
	// discoverRoots returns the selectable workdirs, already absolute.
	discoverRoots func() ([]string, error)
	// liveSessions returns the live tmux session-name set. A machine with no
	// tmux server yields an empty set rather than an error.
	liveSessions func(ctx context.Context) (map[string]bool, error)
	// observeLegacy reads one live session's pre-v2 naming state together with
	// the tmux ids the migration must mirror its allocated uids onto.
	observeLegacy func(ctx context.Context, session string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error)
	// mirror writes allocated identity back onto live tmux objects.
	mirror intmetadata.Mirror
	// shell is the configured shell path; its basename seeds default Window and
	// Pane names.
	shell string
	// sessionNameFor maps a Project root onto its persistent tmux session name.
	sessionNameFor func(root string) string
}

func newRegistryReconciler(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
	mirror := intmetadata.NewMirror(runner)
	home, err := os.UserHomeDir()
	namer := coresessions.NewNamer(home)
	return &registryReconciler{
		discoverRoots: func() ([]string, error) {
			if err != nil {
				return nil, err
			}
			return discoverProjectRoots(home, os.Getenv)
		},
		liveSessions:  sessions.ExistingSessions,
		observeLegacy: mirror.ObserveLegacySessionTargets,
		mirror:        mirror,
		shell:         configuredShell(os.Getenv),
		sessionNameFor: func(root string) string {
			return namer.SessionName(root)
		},
	}
}

// sessionLister is the live tmux session inventory seam.
type sessionLister interface {
	ExistingSessions(ctx context.Context) (map[string]bool, error)
}

// configuredShell resolves the shell whose basename seeds default Window and
// Pane names. It is the same `$SHELL` tmux itself falls back to.
func configuredShell(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(lookupEnv("SHELL"))
}

// discoverProjectRoots returns the selectable workdirs in a deterministic order.
//
// It reads exactly the sources the project picker reads -- pins, the resolved
// repo root, and the configured managed roots -- through
// candidates.DiscoverProjectRoots, which deliberately excludes the home
// directory and the current path: those are picker conveniences, not evidence
// that a directory is a managed project.
//
// The result is sorted by resolved (symlink-free) absolute path so registration
// order, and therefore automatic name suffix allocation, never depends on
// filesystem scan order.
func discoverProjectRoots(homeDir string, lookupEnv func(string) string) ([]string, error) {
	homeDir = filepath.Clean(homeDir)
	repoRoot, _ := resolveProjdir(homeDir, lookupEnv, tmuxProjdirOption, config.LoadProjdir)

	var pinned []string
	homes := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}
	if paths, err := homes.Paths(); err == nil {
		if list, err := pins.NewStore(paths.PinFile()).List(); err == nil {
			pinned = list
		}
	}

	return candidates.DiscoverProjectRoots(candidates.Inputs{
		HomeDir:      homeDir,
		RepoRoot:     repoRoot,
		ManagedRoots: switchManagedRoots(homeDir, repoRoot, extraProjdirRoots(lookupEnv), lookupEnv, config.LoadWorkdirs),
		Pins:         pinned,
	})
}

// reconcile applies one reconciliation pass to the working registry copy.
//
// It runs inside the caller's registry transaction, so a later failure in the
// same operation discards everything it did: a pre-create hook refusal leaves
// the registry file byte-identical even though reconciliation ran first.
func (r *registryReconciler) reconcile(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string) error {
	live, err := r.liveSessions(ctx)
	if err != nil {
		return err
	}

	if err := r.importLiveSessions(ctx, working, mutator, operationID, live); err != nil {
		return err
	}
	if err := r.registerDiscoveredRoots(working, mutator, operationID); err != nil {
		return err
	}
	if err := mutator.ObserveProjectRoots(working); err != nil {
		return err
	}
	if err := r.refreshSessionProjections(working, mutator, live); err != nil {
		return err
	}
	// Last on purpose. importLiveSessions is what mirrors a freshly imported
	// Pane's uid onto its tmux pane, so sweeping before it ran would diff the
	// registry against an inventory that does not yet carry the uids this same
	// pass allocated, and would offline an Agent the instant it was imported.
	r.sweepDeadAgentPanes(ctx, working, mutator)
	return nil
}

// sweepDeadAgentPanes runs the dead-pane sweep as the last step of one
// reconciliation pass.
//
// It fails closed. A tmux query that errors -- no server running, a server that
// refuses the command -- releases nothing and reports no error, which is the
// same tolerance reconcile already extends to an absent server. Rewriting the
// registry from an inventory we could not read would offline every managed
// Agent on a machine whose tmux server simply is not up.
func (r *registryReconciler) sweepDeadAgentPanes(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator) {
	live, err := r.mirror.LivePaneUIDs(ctx)
	if err != nil {
		return
	}
	releaseDeadAgentPanes(working, mutator, live)
}

// importLiveSessions seeds resources from the tmux sessions that predate the
// resource model, in a deterministic session-name order.
func (r *registryReconciler) importLiveSessions(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	live map[string]bool,
) error {
	names := make([]string, 0, len(live))
	for name := range live {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		legacy, targets, err := r.observeLegacy(ctx, name)
		if err != nil {
			// A session that cannot be observed is not a Project source. Failing
			// the whole create because one unrelated session is in a strange
			// state would be worse than skipping it.
			continue
		}
		if strings.TrimSpace(legacy.Root) == "" {
			continue
		}
		result, err := mutator.ImportLegacySession(working, legacy, r.shell, operationID)
		if err != nil {
			if coremetadata.IsUsageError(err) || errors.Is(err, coremetadata.ErrInvalidRoot) {
				// The recorded project path no longer exists, so this session is
				// not a registrable Project. MissingRoot tombstones belong to
				// Projects that were registered before, not to sessions that
				// never were.
				continue
			}
			return err
		}
		if err := r.mirrorImported(ctx, *working, result, targets); err != nil {
			return err
		}
	}
	return nil
}

// mirrorImported writes the uids a legacy import allocated back onto exactly the
// tmux objects it imported.
//
// Without this the migrated Windows and Panes have registry identity but no
// transport binding, so an anchor lookup would find nothing live and every
// create against a pre-existing session would build a duplicate Window.
func (r *registryReconciler) mirrorImported(
	ctx context.Context,
	registry coremetadata.Registry,
	result coremetadata.ImportResult,
	targets intmetadata.LegacyTargets,
) error {
	for _, imported := range result.Windows {
		if imported.SourceIndex < 0 || imported.SourceIndex >= len(targets.Windows) {
			continue
		}
		window, ok := registry.Window(imported.UID)
		if !ok {
			continue
		}
		if err := r.mirror.MirrorWindow(ctx, targets.Windows[imported.SourceIndex], *window); err != nil {
			return err
		}
	}
	for _, imported := range result.Panes {
		if imported.WindowIndex < 0 || imported.WindowIndex >= len(targets.Panes) {
			continue
		}
		row := targets.Panes[imported.WindowIndex]
		if imported.PaneIndex < 0 || imported.PaneIndex >= len(row) {
			continue
		}
		pane, ok := registry.Pane(imported.UID)
		if !ok {
			continue
		}
		if err := r.mirror.MirrorPane(ctx, row[imported.PaneIndex], *pane); err != nil {
			return err
		}
	}
	return nil
}

// registerDiscoveredRoots registers every selectable workdir that is not a
// Project yet, with the bootstrap topology.
//
// Registration order is the sorted resolved (symlink-free) absolute path, not
// filesystem scan order, so the automatic name-suffix allocator produces the
// same names on every machine that sees the same set of workdirs. The sort is
// stable, so two spellings of one directory keep their discovery order and the
// first spelling wins.
//
// A root whose resolved path already belongs to a registered Project is skipped.
// That is the same canonical identity candidate discovery itself dedupes on, and
// it is not a heuristic uid merge: no existing Project is re-identified, a second
// Project is simply not created for a second spelling of one directory.
func (r *registryReconciler) registerDiscoveredRoots(working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string) error {
	discovered, err := r.discoverRoots()
	if err != nil {
		return err
	}
	roots := slices.Clone(discovered)
	slices.SortStableFunc(roots, func(a, b string) int {
		return strings.Compare(candidates.CanonicalPath(a), candidates.CanonicalPath(b))
	})
	registered := map[string]bool{}
	for _, project := range working.Projects {
		registered[candidates.CanonicalPath(project.Spec.Root)] = true
	}
	for _, root := range roots {
		key := candidates.CanonicalPath(root)
		if key == "" || registered[key] {
			continue
		}
		_, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{
			Root:         root,
			DefaultShell: r.shell,
			SessionName:  r.sessionNameFor(root),
			OperationID:  operationID,
		})
		if err != nil {
			if coremetadata.IsUsageError(err) {
				// A workdir that vanished between discovery and registration, or
				// whose basename cannot seed a name, is skipped rather than
				// failing an unrelated create.
				continue
			}
			return err
		}
		registered[key] = true
	}
	return nil
}

// refreshSessionProjections recomputes Project.status.session against the live
// tmux inventory. This is the `status.session` preflight the create routes read
// to decide whether a Project runtime needs materializing.
func (r *registryReconciler) refreshSessionProjections(working *coremetadata.Registry, mutator coremetadata.Mutator, live map[string]bool) error {
	for i := range working.Projects {
		project := working.Projects[i]
		name := r.sessionNameFor(project.Spec.Root)
		if project.Status.Session != nil && strings.TrimSpace(project.Status.Session.Name) != "" {
			name = project.Status.Session.Name
		}
		if _, err := mutator.BindProjectSession(working, project.Metadata.UID, name, live[name]); err != nil {
			return err
		}
	}
	return nil
}

// deadAgentPaneReason is the status.reason a swept Agent carries. It records
// what was observed and nothing about why, because nothing about why is
// observable: the pane-exit hooks fire after tmux has torn the pane struct
// down, so #{pane_dead_status} and #{pane_dead_signal} are empty for a clean
// exit, a non-zero exit, and a SIGKILL alike. They are populated only under
// `remain-on-exit on`, and that option changes pane lifecycle and layout.
const deadAgentPaneReason = "managed pane is no longer live"

// livePaneInventory is the mirrored-uid inventory the dead-pane sweep diffs the
// registry against.
type livePaneInventory interface {
	LivePaneUIDs(ctx context.Context) (map[string]bool, error)
}

// deadAgentPaneUIDs returns the uid of every Agent whose managed Pane is no
// longer bound to a live tmux pane, in registry order.
//
// This is an inventory diff rather than an event handler, and it has to be:
// `after-kill-pane` fires with an empty #{hook_pane}, so no hook can name the
// pane that died. Being a diff is also what makes a migration step unnecessary
// for paneRefs that have been dangling since before this code existed -- the
// first pass resolves them and the second finds nothing left to do.
func deadAgentPaneUIDs(registry coremetadata.Registry, live map[string]bool) []string {
	var uids []string
	for _, agent := range registry.Agents {
		paneUID := strings.TrimSpace(agent.Status.PaneRef)
		if paneUID == "" || live[paneUID] {
			continue
		}
		// The closed transition table is never widened here. An Agent that may
		// not reach Offline is skipped, not forced.
		if !coremetadata.CanTransitionAgent(agent.Status.Phase, coremetadata.PhaseOffline) {
			continue
		}
		uids = append(uids, agent.Metadata.UID)
	}
	return uids
}

// releaseDeadAgentPanes is the one sweep both trigger paths share. It moves
// every Agent whose managed Pane died to Offline and clears its paneRef,
// returning how many it released.
//
// Every swept death is classified AgentExitNormal, so the Agent lands in
// Offline and stays resumable. Failed is deliberately never inferred: tmux
// reports no exit status at hook time (see deadAgentPaneReason), so an
// abnormal classification here would be a guess, and a guessed Failed is worse
// than an accurate Offline for a phase the operator reads to decide whether to
// resume. status.sessionRef is untouched -- ReleaseAgentPane never clears it --
// so a released Agent still knows which provider conversation it belongs to.
func releaseDeadAgentPanes(working *coremetadata.Registry, mutator coremetadata.Mutator, live map[string]bool) int {
	released := 0
	for _, uid := range deadAgentPaneUIDs(*working, live) {
		if _, err := mutator.ReleaseAgentPane(working, uid, coremetadata.AgentExitNormal, deadAgentPaneReason); err != nil {
			// The sweep is maintenance riding along inside somebody else's
			// transaction. One Agent that cannot be released must not fail the
			// operation that happened to trigger it.
			continue
		}
		released++
	}
	return released
}

// runDeadAgentPaneSweep runs the sweep outside a reconciliation pass. It is what
// the two tmux pane-exit hooks invoke.
//
// The hook is not an optimization. registryReconciler.reconcile runs only on the
// mutation routes; the read verbs load read-only and never reconcile, so without
// a hook `projmux describe agent X` right after closing its pane would still
// print Phase: Running.
//
// Because that hook fires on every pane exit in every session, the common case
// -- nothing died that the registry owns -- must cost nothing. The inventory is
// read first, the release set is computed against a read-only snapshot, and the
// locked write transaction is opened only when there is something to release.
// The sweep then reruns inside the lock, so an Agent that changed between the
// two reads is judged against the state that is actually being written.
func runDeadAgentPaneSweep(ctx context.Context, inventory livePaneInventory, store *resourceStore) error {
	if inventory == nil {
		return errors.New("release dead agent panes: the tmux pane inventory is not configured")
	}
	if store == nil || store.load == nil || store.update == nil || store.mutator == nil {
		return errors.New("release dead agent panes: the resource registry store is not configured")
	}
	live, err := inventory.LivePaneUIDs(ctx)
	if err != nil {
		// Fail closed, exactly as the reconciler path does.
		return nil
	}
	registry, err := store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	if len(deadAgentPaneUIDs(registry, live)) == 0 {
		return nil
	}
	_, err = store.update(func(working *coremetadata.Registry) error {
		releaseDeadAgentPanes(working, store.mutator(), live)
		return nil
	})
	return MapMetadataError(err)
}
