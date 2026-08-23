package app

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
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
//     what preserves that topology while recording the observed Window display
//     names. Registering the same root from discovery first would create a
//     default one-Window topology, and the importer would then adopt *that*
//     bootstrap Window instead of representing the tmux windows the operator
//     actually has.
//  2. Register the remaining selectable workdirs with the bootstrap topology.
//  3. Observe roots and refresh the live session projection.
//  4. Reapply the tmux bindings of every live session the import step could not
//     resolve to a Project root. This runs after step 3 on purpose: it resolves
//     a session through the Project<->session name edge, and step 3 is what
//     settles that edge.
//  5. Observe live tmux and record why any Window or Pane lost its runtime,
//     then release every Agent whose managed Pane no longer exists in tmux.
//
// Nothing here renumbers or merges an existing Project: first registration wins
// and names stay stable. Nothing here re-identifies a Window or a Pane either:
// steps 1 and 4 may mint a new object, but no existing uid is ever changed,
// merged, or reassigned, and nothing is ever deleted or pruned.
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
	// refuseForeign is enabled only by the public repair planner. It keeps an
	// unknown live uid observable and untouched; lifecycle convergence retains
	// the historical mint-and-rebind behavior.
	refuseForeign bool
	// refusedSessions is populated only by the public repair planner after it
	// observes a foreign or ambiguous Project binding. Lifecycle reconciliation
	// leaves it nil and retains its existing behavior.
	refusedSessions map[string]bool
	// refusedSessionDivergence carries the classifier result that caused the
	// quarantine. Reporting reads this value instead of inferring a second D4
	// wrapper from a string key.
	refusedSessionDivergence map[string]resourcegraph.Divergence
	// exactProjects is populated only by the public repair planner. It carries
	// a known Registry Project UID already mirrored on a live session so a
	// stale project-path anchor after rebind can be repaired without treating
	// the old path as a new Project identity candidate.
	exactProjects map[string]string
	// targetLiveOnly keeps the public exact-socket repair scoped to resources
	// observed on that server.
	targetLiveOnly       bool
	approvedOrphanImport bool
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
		// Every production caller is an automatic/default recovery path unless
		// it explicitly opts into the approved D3 matcher. D2 import therefore
		// starts closed even for create-time convergence.
		refuseForeign: true,
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
// It reads exactly the sources the project picker reads -- candidate pins, the
// resolved repo root, and the configured managed roots -- through
// candidates.DiscoverProjectRoots, which deliberately excludes the home
// directory and the current path: those are picker conveniences, not evidence
// that a directory is a managed project.
//
// Only candidate pins are a discovery source. A managed pin names a Registry
// Project uid, and a Project's existence is the Registry's statement rather than
// something a filesystem scan has to rediscover; feeding managed pins back into
// discovery would make a presentation preference look like a scan root.
//
// The result is sorted by resolved (symlink-free) absolute path so candidate
// order never depends on filesystem scan order.
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
		if set, err := pins.NewStore(paths.PinFile()).Load(); err == nil {
			pinned = set.CandidatePaths()
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
	return r.reconcileGuarded(ctx, working, mutator, operationID, nil)
}

// reconcileGuarded uses two deliberately independent probes: liveSessions
// supplies the reconciler's name snapshot, then guard inventories full session
// identity and carries the selected stable $N when it exists. They are not one
// atomic tmux snapshot. Safety comes from removing the selected name from the
// earlier set before import: whether the name disappeared, appeared, or was
// replaced between either probe and a later write, it can feed neither
// importLiveSessions nor the unresolved set passed to reapplyUnresolvedBindings.
// The create operation subsequently re-inventories ownership and leases the
// exact $N itself.
func (r *registryReconciler) reconcileGuarded(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	guard createPreReconcile,
) error {
	live, err := r.liveSessions(ctx)
	if err != nil {
		return err
	}
	reconcileLive := live
	if guard != nil {
		selected, err := guard(ctx, working.Clone(), mutator, operationID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(selected.Name) != "" {
			reconcileLive = maps.Clone(live)
			delete(reconcileLive, selected.Name)
		}
	}

	// One pre-pass observation, shared by both binding steps. It answers exactly
	// one question -- which registry uids are already the binding of some live
	// tmux object -- so adoption can decline to steal one. It is read before
	// anything in this pass writes, which is what makes "already bound" mean
	// "bound before we got here".
	runtime := observeRuntime(ctx, r.mirror)
	binder := coremetadata.NewBindingMatcher(runtime)
	if r.refuseForeign {
		binder = coremetadata.NewRepairBindingMatcher(runtime)
	}
	if r.approvedOrphanImport {
		binder = coremetadata.NewApprovedOrphanBindingMatcher(runtime)
	}

	unresolved, err := r.importLiveSessions(ctx, working, mutator, operationID, reconcileLive, binder)
	if err != nil {
		return err
	}
	if err := mutator.ObserveProjectRoots(working); err != nil {
		return err
	}
	if err := r.refreshSessionProjections(working, mutator, live); err != nil {
		return err
	}
	r.reapplyUnresolvedBindings(ctx, working, mutator, operationID, unresolved, binder)
	// Last on purpose. The two binding steps are what mirror a Window's and a
	// Pane's uid onto their tmux objects, so observing before they ran would
	// diff the registry against an inventory that does not yet carry the uids
	// this same pass wrote: it would offline an Agent the instant it was
	// imported and stamp a MissingRuntime condition on a Window that is plainly
	// there -- and, now that binding reapply exists, on a Window this very pass
	// just reattached.
	r.observeRuntime(ctx, working, mutator)
	return nil
}

// observeRuntime is the runtime-observation step of one reconciliation pass.
//
// It reads the live tmux inventory once and puts it to two uses: projecting the
// lifecycle of every managed Pane whose runtime object died, and recording why a
// Window or Pane lost that object. Both are inventory diffs against the same
// snapshot, so the two can never disagree about which panes are still bound.
//
// The projection runs first and shares its whole transition body with the
// standalone one-shot in lifecycle_reconcile.go, so a mutation route and a
// pane-exit hook cannot classify the same death differently.
//
// This is what makes convergence hook-free. The pane-exit hooks are an
// optimization for the read verbs, which never reconcile; with every hook
// disabled the next mutation route still runs this pass and still reaches the
// same answer, because nothing here needs to be told what died -- it compares
// the registry to the machine.
//
// It fails closed, per inventory. A tmux query that errors -- no server
// running, a server that refuses the command -- records nothing and releases
// nothing and reports no error, which is the same tolerance reconcile already
// extends to an absent server. Rewriting the registry from an inventory we
// could not read would condition every Window and offline every managed Agent
// on a machine whose tmux server simply is not up.
func (r *registryReconciler) observeRuntime(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator) {
	panes, paneErr := r.mirror.LivePaneUIDs(ctx)
	if paneErr == nil {
		projectTerminations(working, mutator, lifecycleProjectionTargets(*working, panes, lifecycleDirtyEvent{}))
	}
	windows, windowErr := r.mirror.LiveWindowUIDs(ctx)
	if paneErr != nil || windowErr != nil {
		return
	}
	mutator.ObserveRuntimeBindings(working, coremetadata.RuntimeObservation{Windows: windows, Panes: panes})
}

// observedSession is one live tmux session the import step read but could not
// turn into a Project source, kept so the binding-repair step can reuse the
// observation instead of paying for it twice.
type observedSession struct {
	name       string
	projectUID string
	legacy     coremetadata.LegacySession
	targets    intmetadata.LegacyTargets
}

// importLiveSessions seeds resources from the tmux sessions that predate the
// resource model, in a deterministic session-name order, and reattaches the
// ones whose resources already exist.
//
// It returns the sessions it did not handle. Those are exactly the sessions
// with no usable `@projmux_project_path`: the anchor is written only at session
// creation, so every session that predates it -- which on a long-lived machine
// is most of them -- lands here with nothing to import from. They are the input
// to reapplyUnresolvedBindings, which resolves them by a different key. The two
// sets are disjoint by construction, so no session is bound twice in one pass.
func (r *registryReconciler) importLiveSessions(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	live map[string]bool,
	binder *coremetadata.BindingMatcher,
) ([]observedSession, error) {
	names := make([]string, 0, len(live))
	for name := range live {
		names = append(names, name)
	}
	slices.Sort(names)

	var unresolved []observedSession
	for _, name := range names {
		legacy, targets, err := r.observeLegacy(ctx, name)
		if err != nil {
			// A session that cannot be observed is not a Project source. Failing
			// the whole create because one unrelated session is in a strange
			// state would be worse than skipping it. There is nothing to repair
			// with either, so it is not handed on.
			continue
		}
		if r.refusedSessions[name] {
			continue
		}
		if projectUID := r.exactProjects[name]; projectUID != "" {
			if r.sessionBindingRefused(*working, projectUID, legacy) {
				r.refusedSessions[name] = true
				continue
			}
			unresolved = append(unresolved, observedSession{name: name, projectUID: projectUID, legacy: legacy, targets: targets})
			continue
		}
		if r.refuseForeign && strings.TrimSpace(legacy.Root) != "" {
			projectUID := ""
			if project, ok := working.ProjectByRoot(legacy.Root); ok {
				projectUID = project.Metadata.UID
			}
			if r.sessionBindingRefused(*working, projectUID, legacy) {
				r.refusedSessions[name] = true
				continue
			}
		}
		if strings.TrimSpace(legacy.Root) == "" {
			unresolved = append(unresolved, observedSession{name: name, legacy: legacy, targets: targets})
			continue
		}
		if r.refuseForeign {
			// D2 is L0 for every automatic/default trigger. Even a legacy
			// project-path anchor is not approval to create Registry identity;
			// automatic repair may only rebind rows the Registry already owns.
			_, exists := working.ProjectByRoot(legacy.Root)
			if !r.approvedOrphanImport || !exists {
				unresolved = append(unresolved, observedSession{name: name, legacy: legacy, targets: targets})
				continue
			}
		}
		result, err := mutator.ImportLegacySession(working, legacy, r.shell, operationID, binder)
		if err != nil {
			if coremetadata.IsUsageError(err) || errors.Is(err, coremetadata.ErrInvalidRoot) {
				// The recorded project path no longer exists, so this session is
				// not a registrable Project. MissingRoot tombstones belong to
				// Projects that were registered before, not to sessions that
				// never were. Its bindings may still be repairable through the
				// session-name edge, so it is handed on rather than dropped.
				unresolved = append(unresolved, observedSession{name: name, legacy: legacy, targets: targets})
				continue
			}
			return nil, err
		}
		if err := r.mirrorImported(ctx, *working, result, targets); err != nil {
			return nil, err
		}
	}
	return unresolved, nil
}

// reapplyUnresolvedBindings rewrites the tmux uid options of every live session
// that resolves to an existing Project through the Project<->session name edge.
//
// This step exists because the import path's anchor is not reachable on the
// machine state this phase repairs. `@projmux_project_path` is written once, by
// session creation; a session that predates it carries no anchor at all, so the
// import step bails on it and its windows and panes keep no binding whatsoever.
// The observable symptom is that `projmux delete pane` with no selector fails
// with "the active tmux pane carries no @projmux_pane_uid" in almost every pane
// on the machine.
//
// The edge it resolves through is the one the reconciler itself maintains and
// the create routes already read as preflight: a Project's session name is
// status.session.name when set, and otherwise the name sessionNameFor would
// give its root. That is used forward only -- compute the expected name from
// the Project and compare. A session name is never parsed back into a path;
// that direction would be the heuristic the contract forbids.
//
// It creates no Window. A live window with no eligible registry Window left is
// left exactly as it was found: creating registry topology for a session
// projmux cannot even resolve to a root is the import path's job, and the
// import path has the anchor it needs to do it safely. Panes are the single
// exception, and only underneath a Window this pass already paired -- see
// reapplySessionBindings for why that exception had to exist and why it stays
// closed inside one Project.
//
// It is tolerant, per session and per object. This is maintenance riding along
// inside somebody else's transaction, so one session whose tmux state changed
// mid-pass must not fail the create that happened to trigger the reconcile.
func (r *registryReconciler) reapplyUnresolvedBindings(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	sessions []observedSession,
	binder *coremetadata.BindingMatcher,
) {
	if len(sessions) == 0 {
		return
	}
	scope := r.projectsBySessionName(*working)
	for _, session := range sessions {
		if r.refusedSessions[session.name] {
			continue
		}
		projectUID := session.projectUID
		if projectUID == "" {
			projectUID = scope[session.name]
		}
		if projectUID == "" {
			continue
		}
		if r.refuseForeign && r.sessionBindingRefused(*working, projectUID, session.legacy) {
			r.refusedSessions[session.name] = true
			continue
		}
		r.reapplySessionBindings(ctx, working, mutator, operationID, projectUID, session, binder)
	}
}

func (r *registryReconciler) sessionBindingRefused(registry coremetadata.Registry, projectUID string, legacy coremetadata.LegacySession) bool {
	divergence := resourceSessionBindingDivergence(registry, projectUID, legacy)
	if divergence != "" {
		if r.refusedSessionDivergence == nil {
			r.refusedSessionDivergence = map[string]resourcegraph.Divergence{}
		}
		r.refusedSessionDivergence[legacy.Session] = divergence
	}
	return divergence != "" && !(r.approvedOrphanImport && divergence == resourcegraph.DivergenceOrphanMirror)
}

func resourceSessionBindingDivergence(registry coremetadata.Registry, projectUID string, legacy coremetadata.LegacySession) resourcegraph.Divergence {
	seen := map[string]bool{}
	orphan := false
	for _, liveWindow := range legacy.Windows {
		windowUID := strings.TrimSpace(liveWindow.UID)
		if windowUID != "" {
			if seen[windowUID] {
				return resourcegraph.DivergenceContaminated
			}
			seen[windowUID] = true
			window, ok := registry.Window(windowUID)
			if !ok {
				orphan = true
			} else if projectUID == "" || window.Metadata.OwnerUID() != projectUID {
				return resourcegraph.DivergenceContaminated
			}
		}
		for _, livePane := range liveWindow.Panes {
			paneUID := strings.TrimSpace(livePane.UID)
			if paneUID == "" {
				continue
			}
			if strings.HasPrefix(paneUID, coremetadata.DeletedPaneMirrorPrefix) || seen[paneUID] {
				return resourcegraph.DivergenceContaminated
			}
			seen[paneUID] = true
			pane, ok := registry.Pane(paneUID)
			if !ok {
				orphan = true
				continue
			}
			ownerUID := pane.Metadata.OwnerUID()
			if ownerUID == windowUID && windowUID != "" {
				continue
			}
			agent, ok := registry.Agent(ownerUID)
			if windowUID == "" || !ok || agent.Metadata.OwnerUID() != windowUID {
				return resourcegraph.DivergenceContaminated
			}
		}
	}
	if orphan {
		return resourcegraph.DivergenceOrphanMirror
	}
	return ""
}

// projectsBySessionName maps a live tmux session name onto the one Project that
// claims it.
//
// A name two Projects claim is ambiguous and is dropped rather than resolved to
// the first. Adoption's one unrecoverable mistake is pushing a live object into
// the wrong Project, so an ambiguous scope adopts nothing at all.
//
// Liveness is deliberately not read here. status.session.live is a stored bool,
// and a stored bool is exactly what the runtime-observation work replaced; the
// caller only ever passes session names that came out of the live inventory, so
// liveness is already established by construction.
func (r *registryReconciler) projectsBySessionName(registry coremetadata.Registry) map[string]string {
	byName := map[string]string{}
	ambiguous := map[string]bool{}
	for _, project := range registry.Projects {
		name := strings.TrimSpace(r.sessionNameFor(project.Spec.Root))
		if project.Status.Session != nil && strings.TrimSpace(project.Status.Session.Name) != "" {
			name = strings.TrimSpace(project.Status.Session.Name)
		}
		if name == "" {
			continue
		}
		if _, seen := byName[name]; seen {
			ambiguous[name] = true
			continue
		}
		byName[name] = project.Metadata.UID
	}
	for name := range ambiguous {
		delete(byName, name)
	}
	return byName
}

// reapplySessionBindings walks one resolved session and writes the binding of
// every window and pane it can pair, through the same mirror write path a
// freshly imported object uses.
//
// Missing bindings use the one existing write convention. A uid-only variant
// would drift from what MirrorWindow and MirrorPane do -- automatic-rename off,
// the name mirror, and the tmux window_name. An exact rebind, however, already
// carries that binding and is skipped so repeated lifecycle/apply passes issue
// no tmux writes.
//
// The Window layer only ever pairs. The Pane layer additionally mints, because
// pairing cannot reach the state this phase closes; paneBindingFor carries that
// argument and the boundary it stays inside.
//
// The Agent layer rides on the settled Pane rather than walking tmux again. An
// Agent owns no tmux object of its own, so there is nothing to pair it against
// directly; its runtime object is the managed Pane, and the pane this loop just
// resolved is exactly that. LinkAgentPane carries the evidence rule and the
// refusals.
func (r *registryReconciler) reapplySessionBindings(
	ctx context.Context,
	registry *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	projectUID string,
	session observedSession,
	binder *coremetadata.BindingMatcher,
) {
	for wi, legacyWindow := range session.legacy.Windows {
		if wi >= len(session.targets.Windows) {
			break
		}
		match := binder.MatchWindow(registry, projectUID, legacyWindow.UID)
		if !match.Matched() {
			// Refused or unmatched. Either way none of its panes are considered:
			// a pane is only ever paired inside a Window that was itself paired.
			continue
		}
		window, ok := registry.Window(match.UID)
		if !ok {
			continue
		}
		if strings.TrimSpace(legacyWindow.RuntimeSessionID) != "" || strings.TrimSpace(legacyWindow.RuntimeID) != "" {
			if _, err := mutator.ObserveWindowRuntimeBinding(registry, window.Metadata.UID,
				legacyWindow.RuntimeSessionID, legacyWindow.RuntimeID); err != nil {
				continue
			}
		}
		projected, err := mutator.ObserveWindowDisplayName(registry, window.Metadata.UID, legacyWindow.Name)
		if err != nil {
			continue
		}
		// Rebind means the exact registry uid is already on this live Window.
		// Reasserting the whole mirror would issue four writes (including a
		// rename) on every apply/lifecycle pass. Adoption is the missing-binding
		// case and still takes the existing whole MirrorWindow path.
		if match.Kind != coremetadata.AdoptionRebind {
			if err := r.mirror.MirrorWindow(ctx, session.targets.Windows[wi], projected); err != nil {
				continue
			}
		}
		if wi >= len(session.targets.Panes) {
			continue
		}
		row := session.targets.Panes[wi]
		for pi, legacyPane := range legacyWindow.Panes {
			if pi >= len(row) {
				break
			}
			paneUID, needsMirror, ok := r.paneBindingFor(registry, mutator, operationID, match.UID, legacyPane, binder)
			if !ok {
				continue
			}
			// Agent runtime linkage rides on the Pane the walk just settled on,
			// because an Agent's runtime object *is* that Pane. The error is
			// discarded rather than escalated *or* used to skip the binding
			// write below: the binding is what makes the pane addressable at
			// all, so a link that could not be made must not cost the pane the
			// work that already succeeded. LinkAgentPane writes nothing when it
			// fails, and the next pass sees the same pane and tries again.
			_, _ = mutator.LinkAgentPane(registry, match.UID, paneUID, legacyPane, binder, operationID)
			pane, ok := registry.Pane(paneUID)
			if !ok {
				continue
			}
			// A write that fails is skipped, not retried and not escalated. The
			// next pass observes the same drift and tries again.
			if needsMirror {
				_ = r.mirror.MirrorPane(ctx, row[pi], *pane)
			}
		}
	}
}

// paneBindingFor resolves the registry Pane one live tmux pane is the runtime
// of, minting one when the pane has never had a registry counterpart at all.
// The bool is false when the pane must be left exactly as it was found.
//
// Minting is here because pairing cannot close the measured gap. Phase 1 taught
// this path to adopt, and adoption needs an existing registry Pane to adopt
// *into*; panes from the non-resource `projmux create agent` bridge have none,
// because that route
// registers nothing. On the measured machine one live pane out of seven had a
// binding, and the operator's own active pane was not it -- which is what made
// the shipped "omit the selector, act on the active target" behavior
// unreachable for `delete pane`.
//
// The Project boundary is structural here, not checked. The Window this mints
// into was itself paired earlier in this same walk, and that Window is owned by
// the single Project the session name resolved to; a session two Projects claim
// resolves to none at all. There is no code path from a live pane to a Project
// it does not already sit under, so cross-project registration is not refused --
// it is unreachable.
//
// AdoptionForeign mints too, for the reason the import path gives: a uid nothing
// in the registry knows is never adopted, but projmux itself produces unknown
// uids -- a reconcile rolled back by a pre-create hook refusal has already
// written its allocated uids onto non-transactional tmux options -- and leaving
// those panes unmanageable forever is the worse answer. Minting allocates a new
// uid beside it and re-identifies nothing.
//
// AdoptionRefused still creates nothing, and that is the half of Phase 1 this
// phase must not undo. A refusal means a real registry object sits on the other
// side of the ambiguity, so minting there would leave two registry Panes for one
// tmux pane. Ambiguity resolved by guessing is the one mistake no later pass can
// undo.
func (r *registryReconciler) paneBindingFor(
	registry *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	windowUID string,
	legacyPane coremetadata.LegacyPane,
	binder *coremetadata.BindingMatcher,
) (string, bool, bool) {
	// A self-target delete commits the Registry result before queueing the exact
	// live kill. If that queue fails, the live Pane carries this transport
	// tombstone so the next reconciliation cannot mint it back as an orphan.
	if strings.HasPrefix(strings.TrimSpace(legacyPane.UID), coremetadata.DeletedPaneMirrorPrefix) {
		return "", false, false
	}
	match := binder.MatchPane(registry, windowUID, legacyPane.UID)
	if match.Matched() {
		return match.UID, match.Kind != coremetadata.AdoptionRebind, true
	}
	if match.Kind == coremetadata.AdoptionRefused {
		return "", false, false
	}
	// Automatic convergence never creates identity for a D2 unattributed live
	// pane. In repair mode, both an empty mirror and a D3 mirror just discarded
	// at L8 stay runtime-only until a separately approved import route is used.
	if r.refuseForeign && !(r.approvedOrphanImport && match.Kind == coremetadata.AdoptionForeign) {
		return "", false, false
	}
	pane, err := mutator.ImportOrphanPane(registry, windowUID, legacyPane, operationID)
	if err != nil {
		// Per object tolerant, exactly like the binding writes around it. One
		// pane that cannot be minted must not fail the create that happened to
		// trigger the reconcile; the next pass sees the same orphan and retries.
		return "", false, false
	}
	// Claimed for the rest of the pass, the way the import path claims what it
	// mints. Without it the next live pane of this Window would find a Pane that
	// is unbound only because it was created seconds ago, and adopt it.
	binder.Claim(pane.Metadata.UID)
	return pane.Metadata.UID, true, true
}

// mirrorImported writes the uids a legacy import settled on back onto exactly
// the tmux objects it bound.
//
// Without this the migrated Windows and Panes have registry identity but no
// transport binding, so an anchor lookup would find nothing live and every
// create against a pre-existing session would build a duplicate Window.
//
// Created and adopted objects are mirrored through the same existing calls. A
// rebound object already carries the exact registry uid and is skipped. This
// distinction preserves the full mirror contract on a missing binding while
// making a repeated lifecycle/apply pass a tmux-write-free no-op.
func (r *registryReconciler) mirrorImported(
	ctx context.Context,
	registry coremetadata.Registry,
	result coremetadata.ImportResult,
	targets intmetadata.LegacyTargets,
) error {
	for _, imported := range result.Windows {
		if imported.Origin == coremetadata.ImportRebound {
			continue
		}
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
		if imported.Origin == coremetadata.ImportRebound {
			continue
		}
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

// livePaneInventory is the mirrored-uid inventory the lifecycle projection
// diffs the registry against.
type livePaneInventory interface {
	LivePaneUIDs(ctx context.Context) (map[string]bool, error)
}
