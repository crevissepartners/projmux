package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"slices"
	"strings"
)

// CreateExecution shares metadata allocation and tmux materialization across callers.
type CreateExecution struct {
	AdmitSplitAnchor    func(coremetadata.Registry, string) error
	ProcessInventory    func() resourcegraph.ProcessInventory
	BindWindowRuntime   func(coremetadata.Mutator, *coremetadata.Registry, string, string, string) (coremetadata.Window, error)
	Runtime             *Materializer
	Reconciler          *RegistryReconciler
	Shell               string
	EnsureRuntimeRoute  func(context.Context) error
	IssuePaneActivation func(*coremetadata.Registry, coremetadata.Mutator, string, string, string) (SuperviseSpec, error)
	AdmitAnchor         func(coremetadata.Registry, string) error
}
type CreateSelection struct {
	Name                      string
	Windows, Panes, Selectors []string
	CreateWindow              bool
	PrimarySpelling           string
}

func createUsage(message string) error { return &createUsageError{message: message} }
func IsExactNameRef(raw string) bool {
	return strings.TrimSpace(raw) != "" && !strings.HasPrefix(raw, selector.UIDPrefix)
}

// createScope is the resolved Project/Window/anchor scope of one create.
//
// `--project` is a scope flag, never a mode selector: the same argv means the
// same thing with and without it, and the only difference is where the scope
// comes from. See resolveCreateScope for the rule.
type CreateScope struct {
	// project is the exact-one Project every created resource lands under.
	Project selector.Ref
	// window and Pane are filled in only when the whole scope was omitted and
	// the active tmux target supplied it. They are never blended with an
	// explicit --window/--pane/--selector occurrence.
	Window *selector.Ref
	Pane   *selector.Ref
}

// projectQuery builds the exact-one Project scope query.
func (s CreateScope) ProjectQuery() selector.Query {
	ref := s.Project
	return selector.Query{Project: &ref}
}

// uidRef spells a resolved uid as the selector ref form, so downstream error
// text reports the identity that was actually selected rather than a name that
// was never typed.
func UIDRef(kind coremetadata.Kind, uid string) selector.Ref {
	return selector.Ref{Kind: kind, UID: uid, Raw: selector.UIDPrefix + uid}
}

// payloadCommand renders the `--` payload as the one-time name-derivation
// source stored on the created Pane.
func PayloadCommand(payload []string) string {
	return strings.TrimSpace(strings.Join(payload, " "))
}

// paneTarget is one preflighted (Window, anchor Pane) pair.
type PaneTarget struct {
	WindowUID    string
	AnchorUID    string
	StoredAnchor bool
	Virtual      bool
}

// panePlan is the preflight result of a `create pane` fan-out.
type PanePlan struct {
	Query              selector.Query
	Targets            []PaneTarget
	MissingWindowNames []string
}

// selectedWindowUIDs is the planner's target decision, in planner order.
//
// It is the authority the operation receipt quotes. Deriving the selected set
// from the created results instead would agree only on the happy path and only
// for the routes that create one resource per target Window, and it would make
// the same argv report a different selection per provider the moment a provider
// refuses. Quoting the planner keeps `create pane`, `create agent`, and the
// three provider shortcuts reporting one identical set for one identical argv.
func (p PanePlan) SelectedWindowUIDs() []string {
	uids := make([]string, 0, len(p.Targets))
	for _, target := range p.Targets {
		uids = append(uids, target.WindowUID)
	}
	return uids
}

// resolveSplitTargets is the shared preflight of the two routes that split an
// existing Window: `create pane --project` and `create agent --project`.
//
// It fixes every target Window and every anchor Pane, then promotes the missing
// exact-name --window occurrences into real Window allocations when
// --create-window asked for it. Both halves finish before the caller performs a
// single tmux call, so a stale stored role-agnostic anchor, an unsatisfiable cardinality, or
// an explicit --name collision leaves zero metadata writes and zero tmux objects
// behind.
//
// The returned targets are ordered by Window uid so a fan-out is deterministic
// regardless of selector spelling; the human-facing output order is applied
// separately by writeResults, which sorts by (project name, window name, uid).
func (c *CreateExecution) ResolveSplitTargets(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	scope CreateScope,
	flags CreateSelection,
	fanOut selector.Target,
	spelling, operationID string,
) (PanePlan, []WindowWork, error) {
	plan, err := c.PlanPaneTargets(*working, project, scope, flags, fanOut, spelling)
	if err != nil {
		return PanePlan{}, nil, err
	}
	if flags.Name != "" && len(plan.Targets)+len(plan.MissingWindowNames) > 1 {
		return PanePlan{}, nil, MapMetadataError(coremetadata.ExplicitNameCardinalityError(
			spelling, len(plan.Targets)+len(plan.MissingWindowNames)))
	}
	var Windows []WindowWork
	for _, missing := range plan.MissingWindowNames {
		work, err := c.AllocateWindow(working, mutator, project, WindowRequest{
			Name:        missing,
			OperationID: operationID,
		})
		if err != nil {
			return PanePlan{}, nil, err
		}
		Windows = append(Windows, work)
		// The Window this operation created owns a brand new initial Pane,
		// which is both its default shell ref and this operation's anchor.
		plan.Targets = append(plan.Targets, PaneTarget{
			WindowUID:    work.Window.Metadata.UID,
			AnchorUID:    work.Initial.Metadata.UID,
			StoredAnchor: true,
		})
	}
	slices.SortStableFunc(plan.Targets, func(a, b PaneTarget) int {
		return cmp.Compare(a.WindowUID, b.WindowUID)
	})
	return plan, Windows, nil
}

// planPaneTargets resolves every target Window and its anchor Pane without
// mutating anything.
func (c *CreateExecution) PlanPaneTargets(
	registry coremetadata.Registry,
	project coremetadata.Project,
	scope CreateScope,
	flags CreateSelection,
	fanOut selector.Target,
	spelling string,
) (PanePlan, error) {
	query := scope.ProjectQuery()
	// Both spellings of the exact-one primary selection resolve through this one
	// call, so the flag and the bare `--project P` scope cannot drift apart --
	// including on the empty and dangling refusals, which they share.
	if spelled := flags.PrimarySpelling; spelled != "" {
		ref, err := PrimaryWindowRef(registry, project, spelling, spelled)
		if err != nil {
			return PanePlan{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.Windows {
		ref, err := selector.ParseRef(coremetadata.KindWindow, raw)
		if err != nil {
			return PanePlan{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.Selectors {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return PanePlan{}, err
		}
		query.Labels = append(query.Labels, label)
	}
	if scope.Window != nil && len(query.Windows) == 0 && len(query.Labels) == 0 {
		query.Windows = append(query.Windows, *scope.Window)
	}

	plan := PanePlan{Query: query}
	resolver := selector.New(registry)
	resolution, err := resolver.ResolveWindows(query)
	if err != nil {
		return PanePlan{}, err
	}

	if flags.CreateWindow {
		// A missing exact name is promoted to a Window ensure. Everything else
		// keeps the default no-match behavior.
		plan.MissingWindowNames = UnresolvedWindowNames(registry, project, flags.Windows)
	}
	if len(resolution.Matches) == 0 && len(plan.MissingWindowNames) == 0 {
		// The fan-out cell belongs to the caller, not to this helper: `create
		// pane` fans out over the Windows it selected, and `create agent` over
		// the Agents it produces, one per selected Window.
		return PanePlan{}, selector.Enforce(fanOut, selector.DescribeSelector(query), resolution)
	}

	for _, match := range resolution.Matches {
		anchorUID, storedAnchor, err := c.ResolveAnchor(registry, project, scope, match.UID, flags, spelling)
		if err != nil {
			return PanePlan{}, err
		}
		virtual := registry.IsVirtualWindow(match.UID) && storedAnchor
		if !virtual && c.AdmitAnchor != nil {
			if err := c.AdmitAnchor(registry, anchorUID); err != nil {
				return PanePlan{}, err
			}
		}
		plan.Targets = append(plan.Targets, PaneTarget{WindowUID: match.UID, AnchorUID: anchorUID, StoredAnchor: storedAnchor, Virtual: virtual})
	}
	return plan, nil
}

// primaryWindowRef resolves the exact-one Window the Project's
// spec.primaryWindowRef names, for whichever spelling asked for it.
//
// The ref is validated here rather than left to the selector so the refusal
// names the actual defect. A Project with no Windows carries an empty ref, and
// a ref that survived into a registry whose Window is gone or reparented is a
// dangling or cross-root pointer; either way the operator is told to name the
// Window instead of being handed a bare no-match. The check is read-only and
// happens inside the preflight, so it costs zero Registry writes, zero tmux
// objects, and zero provider calls.
func PrimaryWindowRef(registry coremetadata.Registry, project coremetadata.Project, spelling, spelled string) (selector.Ref, error) {
	primary := strings.TrimSpace(project.Spec.PrimaryWindowRef)
	if primary == "" {
		return selector.Ref{}, createUsage(fmt.Sprintf(
			"%s %s: project/%s has no spec.primaryWindowRef; create a Window first or name one with --window <ref>",
			spelling, spelled, project.Metadata.Name))
	}
	window, ok := registry.Window(primary)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject ||
		window.Metadata.OwnerUID() != project.Metadata.UID {
		return selector.Ref{}, createUsage(fmt.Sprintf(
			"%s %s: project/%s spec.primaryWindowRef %q is dangling or owned by another Project; name the target with --window <ref>",
			spelling, spelled, project.Metadata.Name, primary))
	}
	return UIDRef(coremetadata.KindWindow, primary), nil
}

// unresolvedWindowNames returns the exact-name --window occurrences that match
// no Window in the Project, deduplicated and in argv order.
func UnresolvedWindowNames(registry coremetadata.Registry, project coremetadata.Project, refs []string) []string {
	existing := map[string]bool{}
	for _, window := range registry.WindowsOf(project.Metadata.UID) {
		existing[window.Metadata.Name] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, raw := range refs {
		if !IsExactNameRef(raw) || existing[raw] || seen[raw] {
			continue
		}
		seen[raw] = true
		missing = append(missing, raw)
	}
	return missing
}

// resolveAnchor fixes the split anchor of one target Window.
//
// An explicit --pane is resolved among that Window's direct and Agent-owned
// Panes and must be exactly one. With no --pane the persisted role-agnostic anchorPaneRef is
// the anchor: an explicitly scoped create has deliberately no fallback to the
// focused, last-used, or another live Pane, and a missing or stale ref is a
// usage error rather than a silent repair.
//
// The one implicit anchor is the whole-scope one, and it is not a fallback. When
// the argv named no scope at all, resolveCreateScope resolved the active Window
// *and* the active Pane together, so the anchor is a resolved target of the same
// invocation rather than a substitution for one. As soon as any --project,
// --window, --pane, or --selector is typed, that pair is gone: --project and
// --window fixed the target set, and substituting the focused pane would
// silently split somewhere the invocation never addressed, possibly in a
// different Window entirely.
func (c *CreateExecution) ResolveAnchor(registry coremetadata.Registry, project coremetadata.Project, scope CreateScope, windowUID string, flags CreateSelection, spelling string) (string, bool, error) {
	window, ok := registry.Window(windowUID)
	if !ok {
		return "", false, fmt.Errorf("%s: window %q disappeared during preflight", spelling, windowUID)
	}
	Panes := flags.Panes
	if len(Panes) == 0 && scope.Pane != nil {
		Panes = []string{scope.Pane.Raw}
	}
	if len(Panes) > 0 {
		query := scope.ProjectQuery()
		query.Windows = []selector.Ref{{Kind: coremetadata.KindWindow, UID: windowUID, Raw: window.Metadata.Name}}
		for _, raw := range Panes {
			ref, err := selector.ParseRef(coremetadata.KindPane, raw)
			if err != nil {
				return "", false, err
			}
			query.Panes = append(query.Panes, ref)
		}
		resolution, err := selector.New(registry).ResolvePanes(query)
		if err != nil {
			return "", false, err
		}
		target := selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindPane}
		if err := selector.Enforce(target, selector.DescribeSelector(query)+" in window/"+window.Metadata.Name, resolution); err != nil {
			return "", false, err
		}
		paneUID := resolution.Matches[0].UID
		if _, ok := registry.PaneInWindow(window.Metadata.UID, paneUID); !ok {
			return "", false, createUsage(fmt.Sprintf(
				"%s: explicit pane %q is not owned by window/%s; refusing cross-Window anchor",
				spelling, paneUID, window.Metadata.Name))
		}
		return paneUID, false, nil
	}

	anchor := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if anchor == "" {
		return "", true, createUsage(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef; pass an explicit --pane <ref>",
			spelling, window.Metadata.Name, project.Metadata.Name))
	}
	if resolved, ok := registry.WindowAnchor(window.Metadata.UID); !ok || resolved.Metadata.UID != anchor {
		return "", true, createUsage(fmt.Sprintf(
			"%s: window/%s (project/%s) anchorPaneRef %q is dangling or cross-Window; pass an explicit --pane <ref>",
			spelling, window.Metadata.Name, project.Metadata.Name, anchor))
	}
	return anchor, true, nil
}

// resolveProject resolves the exact-one Project scope through the declared
// <create, Project> cardinality cell.
//
// The cell is the same whether the ref was typed or derived: a create always
// lands under exactly one Project.
func (c *CreateExecution) ResolveProject(registry coremetadata.Registry, scope CreateScope) (coremetadata.Project, error) {
	query := scope.ProjectQuery()
	resolution, err := selector.New(registry).ResolveProjects(query)
	if err != nil {
		return coremetadata.Project{}, err
	}
	target := selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindProject}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return coremetadata.Project{}, err
	}
	project, ok := registry.Project(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Project{}, fmt.Errorf("create: project %q disappeared during preflight", resolution.Matches[0].UID)
	}
	return *project, nil
}

// refuseMissingRoot rejects a Project whose spec.root has disappeared.
//
// A MissingRoot Project is preserved rather than deleted, so it still resolves
// as a selector target; creating resources below a directory that is not there
// would materialize a runtime anchored at a path tmux cannot enter.
func (c *CreateExecution) RefuseMissingRoot(project coremetadata.Project) error {
	condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot)
	if !ok || condition.Status != coremetadata.ConditionTrue {
		return nil
	}
	return createUsage(fmt.Sprintf(
		"create: project/%s carries a MissingRoot condition for %q; rebind it before creating resources",
		project.Metadata.Name, project.Spec.Root))
}

// windowRequest describes one Window creation.
type WindowRequest struct {
	Name        string
	Labels      map[string]string
	Payload     []string
	OperationID string
}

// windowWork is one allocated Window plus the runtime handles it acquires.
type WindowWork struct {
	Window  coremetadata.Window
	Initial coremetadata.Pane
	Payload []string
	// activation is the generation the initial Pane's supervised launch was
	// issued.
	Activation SuperviseSpec
	// initialPaneID is the tmux pane id of the Window's initial Pane, filled in
	// by the runtime phase.
	InitialPaneID string
}

// allocateWindow creates one Window and its initial Pane in metadata only.
func (c *CreateExecution) AllocateWindow(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	req WindowRequest,
) (WindowWork, error) {
	command := PayloadCommand(req.Payload)
	window, Panes, err := mutator.AddWindow(working, project.Metadata.UID, coremetadata.BootstrapWindow{
		Name:    req.Name,
		Command: command,
		Labels:  req.Labels,
		Panes: []coremetadata.BootstrapPane{{
			Command: command,
			CWD:     project.Spec.Root,
			Labels:  req.Labels,
		}},
	}, c.Shell, req.OperationID)
	if err != nil {
		return WindowWork{}, MapMetadataError(err)
	}
	activation, err := c.IssuePaneActivation(working, mutator, Panes[0].Metadata.UID, "", req.OperationID)
	if err != nil {
		return WindowWork{}, err
	}
	return WindowWork{Window: window, Initial: Panes[0], Payload: req.Payload, Activation: activation}, nil
}

// materializeWindow creates the detached tmux window for an allocated Window and
// mirrors both identities onto it.
func (c *CreateExecution) MaterializeWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName string,
	work *WindowWork,
) error {
	// A Project whose runtime this operation just created adopts its first
	// stored Window onto the session's mandatory initial tmux window
	// (ensureProjectRuntime -> adoptInitialWindow). On a Project that owned no
	// Window at all, that first Window is the one this operation allocated a
	// moment earlier, so creating a second tmux window for it would leave two
	// tmux windows mirroring one stable UID with the adopted one stranded. The
	// adopted runtime already is this Window's materialization, so bind the
	// allocated initial Pane to it instead of creating anything.
	if owner, adopted := ledger.CurrentWindow(work.Window.Metadata.UID); adopted {
		return c.BindAdoptedWindow(ctx, working, mutator, work, owner)
	}
	launch := c.Runtime.SupervisedLaunch(ctx, work.Activation, work.Payload)
	created, err := c.Runtime.NewWindow(ctx, sessionName, work.Window.Metadata.Name, project.Spec.Root, launch)
	if created.WindowID == "" {
		return err
	}
	if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimeWindow, created.WindowID, work.Window.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	if mirrorErr := c.Runtime.MirrorWindowIdentity(ctx, created.WindowID, work.Window); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	if strings.TrimSpace(sessionName) != "" || strings.TrimSpace(created.WindowID) != "" {
		projected, bindingErr := mutator.ObserveWindowRuntimeBinding(
			working, work.Window.Metadata.UID, sessionName, created.WindowID,
		)
		if bindingErr != nil {
			return errors.Join(err, bindingErr)
		}
		work.Window = projected
		ledger.ObserveCurrentWindow(work.Window.Metadata.UID, RuntimeOwner{
			SessionID: projected.Status.RuntimeSessionID, WindowID: projected.Status.RuntimeID,
		})
	}
	if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, created.PaneID, work.Initial.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	work.InitialPaneID = created.PaneID
	if mirrorErr := c.Runtime.MirrorPaneIdentity(ctx, work.InitialPaneID, work.Initial); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	ObserveActivationRuntime(working, mutator, work.Activation, work.InitialPaneID, c.Runtime.Warn)
	return err
}

// bindAdoptedWindow finishes materialization for an allocated Window the new
// Project session already adopted. The tmux window and its pane carry this
// Window's and Pane's mirrors already; what is still missing is the live pane
// handle the rest of the create route reads.
//
// The adopted tmux window is the session's own first window, which tmux always
// opens with a plain login shell. A Window adopted this way therefore never
// runs a stored command, exactly as an adopted pre-existing first Window does
// not; that is the adoption trade-off, not a new one.
func (c *CreateExecution) BindAdoptedWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	work *WindowWork,
	owner RuntimeOwner,
) error {
	if projected, ok := working.Window(work.Window.Metadata.UID); ok {
		work.Window = *projected
	}
	Panes, err := c.Runtime.PanesOf(ctx, owner.WindowID)
	if err != nil {
		return err
	}
	for _, row := range Panes {
		if row[0] != work.Initial.Metadata.UID {
			continue
		}
		work.InitialPaneID = row[1]
		ObserveActivationRuntime(working, mutator, work.Activation, work.InitialPaneID, c.Runtime.Warn)
		return nil
	}
	return fmt.Errorf(
		"create: window/%s was adopted onto tmux window %s, which has no live binding for its initial pane/%s; "+
			"stop the Project and retry the same create",
		work.Window.Metadata.Name, owner.WindowID, work.Initial.Metadata.Name)
}

// ensureAnchorPane returns the live tmux pane id of the preflighted anchor,
// materializing the Window it belongs to when that Window is still offline.
func (c *CreateExecution) EnsureAnchorPane(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName, operationID string,
	target PaneTarget,
) (string, error) {
	registry := *working
	window, ok := registry.Window(target.WindowUID)
	if !ok {
		return "", fmt.Errorf("create pane: window %q disappeared before the mutation ran", target.WindowUID)
	}
	anchor, ok := registry.Pane(target.AnchorUID)
	if !ok {
		return "", fmt.Errorf("create pane: anchor pane %q disappeared before the mutation ran", target.AnchorUID)
	}
	if _, ok := registry.PaneInWindow(window.Metadata.UID, anchor.Metadata.UID); !ok {
		return "", fmt.Errorf("create pane: anchor pane/%s is not owned by window/%s", anchor.Metadata.Name, window.Metadata.Name)
	}
	if target.StoredAnchor {
		stored, ok := registry.WindowAnchor(window.Metadata.UID)
		if !ok || stored.Metadata.UID != anchor.Metadata.UID {
			return "", fmt.Errorf("create pane: window/%s stored anchor %q is dangling or cross-Window", window.Metadata.Name, target.AnchorUID)
		}
	}

	if c.AdmitSplitAnchor != nil {
		if err := c.AdmitSplitAnchor(registry, anchor.Metadata.UID); err != nil {
			return "", err
		}
	}

	owner, observedCurrent := ledger.CurrentWindow(target.WindowUID)
	if observedCurrent && owner.SessionID != sessionName {
		return "", fmt.Errorf("create pane: Window uid %q moved outside current Project session %s", target.WindowUID, sessionName)
	}
	windowID := owner.WindowID
	var err error
	if !observedCurrent {
		windowID, err = c.Runtime.WindowIDForUID(ctx, sessionName, target.WindowUID)
		if err != nil {
			return "", err
		}
	}
	if windowID == "" {
		// A whole offline Window has no Pane against which tmux can split. A stored
		// direct shell anchor can be its initial Pane. A stored Agent anchor instead
		// gets an explicit lazy default-shell bootstrap; the anchor identity itself
		// remains unchanged and is materialized only by its Agent launch path.
		bootstrap := *anchor
		if anchor.Spec.Role != coremetadata.PaneRoleShell || anchor.Metadata.OwnerRef == nil ||
			anchor.Metadata.OwnerRef.Kind != coremetadata.KindWindow || anchor.Metadata.OwnerRef.UID != window.Metadata.UID {
			if !target.StoredAnchor {
				return "", fmt.Errorf("create pane: explicit anchor pane/%s is offline; refusing to infer a different Pane", anchor.Metadata.Name)
			}
			bootstrap, _, err = mutator.EnsureWindowDefaultShell(working, window.Metadata.UID, c.Shell, operationID)
			if err != nil {
				return "", MapMetadataError(err)
			}
			window, _ = working.Window(window.Metadata.UID)
		}
		bootstrapActivation, activationErr := c.IssuePaneActivation(working, mutator, bootstrap.Metadata.UID, "", operationID)
		if activationErr != nil {
			return "", activationErr
		}
		created, createErr := c.Runtime.NewWindow(ctx, sessionName, window.Metadata.Name, project.Spec.Root,
			c.Runtime.SupervisedLaunch(ctx, bootstrapActivation, nil))
		windowID, err = created.WindowID, createErr
		if windowID != "" {
			if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimeWindow, windowID, window.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.Runtime.MirrorWindowIdentity(ctx, windowID, *window); mirrorErr != nil {
				return "", errors.Join(err, mirrorErr)
			}
			if strings.TrimSpace(sessionName) != "" || strings.TrimSpace(windowID) != "" {
				projected, bindingErr := mutator.ObserveWindowRuntimeBinding(
					working, window.Metadata.UID, sessionName, windowID,
				)
				if bindingErr != nil {
					return "", errors.Join(err, bindingErr)
				}
				ledger.ObserveCurrentWindow(window.Metadata.UID, RuntimeOwner{
					SessionID: projected.Status.RuntimeSessionID, WindowID: projected.Status.RuntimeID,
				})
			}
			if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, created.PaneID, bootstrap.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.Runtime.MirrorPaneIdentity(ctx, created.PaneID, bootstrap); mirrorErr != nil {
				return "", errors.Join(err, mirrorErr)
			}
			ObserveActivationRuntime(working, mutator, bootstrapActivation, created.PaneID, c.Runtime.Warn)
			return created.PaneID, err
		}
		if err != nil {
			return "", err
		}
	}

	Panes, err := c.Runtime.PanesOf(ctx, windowID)
	if err != nil {
		return "", err
	}
	for _, row := range Panes {
		if row[0] == target.AnchorUID {
			return row[1], nil
		}
	}
	return "", fmt.Errorf(
		"create pane: anchor pane/%s (window/%s) has no live tmux binding in window %s",
		anchor.Metadata.Name, window.Metadata.Name, windowID)
}

// ensureProjectRuntime makes the Project's persistent tmux session live and
// records the projection.
//
// This is the acceptance-criterion-1 path: a Project whose status.session is
// absent or not live is materialized in the background, detached, with no
// client movement at all.
func (c *CreateExecution) EnsureProjectRuntime(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	operationID string,
	ledger *RuntimeLedger,
) (string, error) {
	if err := c.EnsureRuntimeRoute(ctx); err != nil {
		return "", err
	}
	sessionName := c.Reconciler.ProjectPhysicalSessionName(*working, project)
	if sessionName == "" {
		return "", fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
	}
	exists, err := c.Runtime.Sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return "", fmt.Errorf("create: inspect Project runtime %q: %w", sessionName, err)
	}
	if !exists {
		Windows := TerminalWindows(*working, project.Metadata.UID)
		if len(Windows) > 0 {
			if _, ok := working.WindowDefaultShell(Windows[0].Metadata.UID); !ok {
				if _, _, err := mutator.EnsureWindowDefaultShell(working, Windows[0].Metadata.UID, c.Shell, operationID); err != nil {
					return "", MapMetadataError(err)
				}
			}
		}
	}
	created, initialActivation, err := c.Runtime.EnsureSessionLaunching(ctx, project, sessionName, project.Spec.Root,
		InitialWindowName(working, project.Metadata.UID), c.InitialPaneActivation(working, mutator, project.Metadata.UID, operationID), ledger)
	if err != nil {
		return "", err
	}
	if created.Created {
		// `new-session` always brings one window and one pane with it. Binding
		// them to the Project's first bootstrap Window is what keeps the runtime
		// a projection of the stored topology instead of an orphan window
		// sitting next to it.
		if err := c.AdoptInitialWindow(ctx, working, mutator, project, created, initialActivation, ledger); err != nil {
			return "", err
		}
	}
	// Re-project every already-live Window from its stable UID. The Registry's
	// cached $/@ pair is diagnostic only and may name a prior tmux generation;
	// this current pair and the positive MissingRuntime clear commit atomically
	// with the create operation.
	windowRuntimes, observeErr := c.Runtime.WindowRuntimeInventory(ctx, sessionName)
	if observeErr != nil {
		return "", observeErr
	}
	ledger.ResetCurrentWindows()
	for _, window := range working.WindowsOf(project.Metadata.UID) {
		owner, found, observeErr := ResolveWindowRuntimeUID(windowRuntimes, window.Metadata.UID)
		if observeErr != nil {
			return "", observeErr
		}
		if !found {
			continue
		}
		if owner.SessionID != created.SessionID {
			return "", fmt.Errorf("create: Window uid %q resolved outside current Project session %s", window.Metadata.UID, created.SessionID)
		}
		if _, bindingErr := c.BindWindowRuntime(mutator, working, window.Metadata.UID, owner.SessionID, owner.WindowID); bindingErr != nil {
			return "", bindingErr
		}
		ledger.ObserveCurrentWindow(window.Metadata.UID, owner)
	}
	if _, err := mutator.BindLiveProjectSession(working, project.Metadata.UID, sessionName, c.Runtime.ExpectedSocketPath); err != nil {
		return "", MapMetadataError(err)
	}
	if err := c.Runtime.FinalizeSessionStartup(ctx, created, sessionName, project.Spec.Root, ledger); err != nil {
		return "", err
	}
	return created.SessionID, nil
}

// initialWindowName is the Registry name of the Window adoptInitialWindow binds
// a freshly created session's own first Window to, so new-session can create
// that Window under its Registry name. A Project with no Window has nothing to
// adopt, and the blank name keeps tmux's default.
func InitialWindowName(registry *coremetadata.Registry, projectUID string) string {
	Windows := TerminalWindows(*registry, projectUID)
	if len(Windows) == 0 {
		return ""
	}
	return Windows[0].Metadata.Name
}

// initialPaneActivation issues the activation of the default shell Pane
// adoptInitialWindow will bind to a new session's first Pane, so that Pane is
// launched under its own generation like any other Pane this create starts.
func (c *CreateExecution) InitialPaneActivation(registry *coremetadata.Registry, mutator coremetadata.Mutator, projectUID, operationID string) SessionFirstPaneActivation {
	return func() (SuperviseSpec, error) {
		Windows := TerminalWindows(*registry, projectUID)
		if len(Windows) == 0 {
			return SuperviseSpec{}, nil
		}
		primary, ok := registry.WindowDefaultShell(Windows[0].Metadata.UID)
		if !ok {
			return SuperviseSpec{}, nil
		}
		return c.IssuePaneActivation(registry, mutator, primary.Metadata.UID, "", operationID)
	}
}

// adoptInitialWindow binds the window and pane a freshly created session came
// with to the Project's first stored Window and that Window's default shell.
//
// Adoption is deliberately narrow: it only ever runs for a session this
// operation just created, so it can never claim a window that belonged to
// someone else. The exact initial Window and Pane are claimed and recorded as
// separate ledger entries before their full mirrors, so rollback can verify and
// remove each owned handle even when a hook moved it out of the new Session.
// activation is the generation the Pane was launched under; its %N is recorded
// once the Pane is claimed.
func (c *CreateExecution) AdoptInitialWindow(ctx context.Context, registry *coremetadata.Registry, mutator coremetadata.Mutator, project coremetadata.Project, created intmux.NewSessionResult, activation SuperviseSpec, ledger *RuntimeLedger) error {
	Windows := TerminalWindows(*registry, project.Metadata.UID)
	if len(Windows) == 0 {
		return nil
	}
	first := Windows[0]
	if err := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimeWindow, created.WindowID, first.Metadata.UID, ledger); err != nil {
		return err
	}
	if err := c.Runtime.MirrorWindowIdentity(ctx, created.WindowID, first); err != nil {
		return err
	}
	if strings.TrimSpace(created.SessionID) != "" || strings.TrimSpace(created.WindowID) != "" {
		projected, err := mutator.ObserveWindowRuntimeBinding(
			registry, first.Metadata.UID, created.SessionID, created.WindowID,
		)
		if err != nil {
			return err
		}
		first = projected
		ledger.ObserveCurrentWindow(first.Metadata.UID, RuntimeOwner{
			SessionID: projected.Status.RuntimeSessionID, WindowID: projected.Status.RuntimeID,
		})
	}
	primary, ok := registry.WindowDefaultShell(first.Metadata.UID)
	if !ok {
		return nil
	}
	if err := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, created.PaneID, primary.Metadata.UID, ledger); err != nil {
		return err
	}
	if err := c.Runtime.MirrorPaneIdentity(ctx, created.PaneID, *primary); err != nil {
		return err
	}
	if activation.PaneUID == primary.Metadata.UID {
		ObserveActivationRuntime(registry, mutator, activation, created.PaneID, c.Runtime.Warn)
	}
	return nil
}

// projectOwnershipGuard contains the selected session before lifecycle
// reconciliation can import, adopt, or re-mirror anything in it. Existing
// Projects require the complete UID/root/duplicate proof. A Project visible
// only through discovery has no durable UID proof yet; if its would-be session
// name is already live, create refuses rather than importing that blank or
// foreign runtime under a freshly allocated identity.
func (c *CreateExecution) ProjectOwnershipGuard(scope CreateScope) CreatePreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (LiveSessionIdentity, error) {
		project, err := c.ResolveProject(working, scope)
		if err == nil {
			sessionName := c.Reconciler.ProjectPhysicalSessionName(working, project)
			if sessionName == "" {
				return LiveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
			}
			identity, _, err := c.Runtime.PreflightSessionOwnership(ctx, project, sessionName)
			if identity.Name == "" {
				identity.Name = sessionName
			}
			return identity, err
		}
		query := scope.ProjectQuery()
		var selectorErr *selector.SelectorError
		if query.Project == nil || query.Project.IsUID() || !errors.As(err, &selectorErr) || !selectorErr.IsNoMatch() {
			return LiveSessionIdentity{}, err
		}
		// Even when the name is neither registered nor discovered, a live
		// same-name session must not become the selector's identity source via
		// lifecycle import.
		if err := c.Runtime.RefuseUnregisteredSessionClaims(ctx, query.Project.Name, ""); err != nil {
			return LiveSessionIdentity{}, err
		}

		// A name that matches no Project used to be a name that was about to
		// become one: the reconcile prelude registered every discovered workdir,
		// so this guard only had to predict the name that registration would
		// allocate. Registration is explicit now, so the honest answer is a
		// refusal -- and the useful refusal names the exact path and the route
		// that would register it, which is why discovery is still read here.
		return LiveSessionIdentity{}, c.UnregisteredProjectRefusal(query.Project.Name, err)
	}
}

// unregisteredProjectRefusal turns a Project selector no-match into an actionable
// bootstrap instruction.
//
// Discovery is consulted for the message and for nothing else. Finding a candidate
// directory whose basename matches the requested name does not register it, does
// not reserve the name, and does not change the refusal -- it only lets the error
// name the exact `--root` an operator would pass next.
func (c *CreateExecution) UnregisteredProjectRefusal(name string, cause error) error {
	name = strings.TrimSpace(name)
	if root := c.DiscoveredCandidateRootFor(name); root != "" {
		return createUsage(fmt.Sprintf(
			"create: no Registry Project is named %q; %q is a discovered directory that no Project claims. Register it with `projmux create project --root %s`, or open it once from the Project sidebar",
			name, root, root))
	}
	return cause
}

// discoveredCandidateRootFor returns the discovered, unregistered directory whose
// automatic Project name would be name, or empty when there is none.
func (c *CreateExecution) DiscoveredCandidateRootFor(name string) string {
	if name == "" || c.Reconciler == nil || c.Reconciler.DiscoverRoots == nil {
		return ""
	}
	roots, err := c.Reconciler.DiscoverRoots()
	if err != nil {
		return ""
	}
	roots = slices.Clone(roots)
	slices.SortStableFunc(roots, func(a, b string) int {
		return strings.Compare(candidates.CanonicalPath(a), candidates.CanonicalPath(b))
	})
	for _, root := range roots {
		if coremetadata.ProjectNameBase(root) == name {
			return root
		}
	}
	return ""
}

func (c *CreateExecution) ExactProjectOwnershipGuard(projectUID string) CreatePreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, _ coremetadata.Mutator, _ string) (LiveSessionIdentity, error) {
		project, ok := working.Project(projectUID)
		if !ok {
			return LiveSessionIdentity{}, fmt.Errorf("create: project %q disappeared before ownership preflight", projectUID)
		}
		sessionName := c.Reconciler.ProjectPhysicalSessionName(working, *project)
		if sessionName == "" {
			return LiveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
		}
		identity, _, err := c.Runtime.PreflightSessionOwnership(ctx, *project, sessionName)
		if identity.Name == "" {
			identity.Name = sessionName
		}
		return identity, err
	}
}

func TerminalWindows(reg coremetadata.Registry, projectUID string) []coremetadata.Window {
	windows := reg.WindowsOf(projectUID)
	var out []coremetadata.Window
	for _, w := range windows {
		if !reg.IsVirtualWindow(w.Metadata.UID) {
			out = append(out, w)
		}
	}
	return out
}

func (c *CreateExecution) ProcessAnchorAdmission(scope CreateScope, flags CreateSelection, spelling string) func(coremetadata.Registry) error {
	if c.ProcessInventory == nil {
		return nil
	}
	var virtualAnchors map[string]string
	return func(registry coremetadata.Registry) error {
		if virtualAnchors == nil {
			virtualAnchors = make(map[string]string)
			for _, window := range registry.Windows {
				if registry.IsVirtualWindow(window.Metadata.UID) {
					virtualAnchors[window.Metadata.UID] = window.Spec.AnchorPaneRef
				}
			}
		}
		if len(ProcessPaneUIDs(registry, c.ProcessInventory())) == 0 {
			return nil
		}
		project, err := c.ResolveProject(registry, scope)
		if err != nil {
			return nil
		}
		plan, err := c.PlanPaneTargets(registry, project, scope, flags, selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindWindow}, spelling)
		var capability resourcegraph.ProcessCapabilityError
		if errors.As(err, &capability) {
			return err
		}
		for _, target := range plan.Targets {
			// A Window admitted for a split cannot turn into a process anchor
			// while waiting for the Registry lock and gain creation authority.
			if target.Virtual && virtualAnchors[target.WindowUID] != target.AnchorUID {
				return resourcegraph.ProcessCapabilityError{Action: resourcegraph.ProcessCreatePane, Anchor: true}
			}
		}
		// Preserve ordinary tmux validation order and its route errors. Only the
		// process capability refusal belongs to this earlier admission stage.
		return nil
	}
}
