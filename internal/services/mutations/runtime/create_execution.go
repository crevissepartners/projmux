package runtime

// createScope is the resolved Project/Window/anchor scope of one create.
//
// `--project` is a scope flag, never a mode selector: the same argv means the
// same thing with and without it, and the only difference is where the scope
// comes from. See resolveCreateScope for the rule.
type createScope struct {
	// project is the exact-one Project every created resource lands under.
	project selector.Ref
	// window and pane are filled in only when the whole scope was omitted and
	// the active tmux target supplied it. They are never blended with an
	// explicit --window/--pane/--selector occurrence.
	window *selector.Ref
	pane   *selector.Ref
}


// projectQuery builds the exact-one Project scope query.
func (s createScope) projectQuery() selector.Query {
	ref := s.project
	return selector.Query{Project: &ref}
}


// uidRef spells a resolved uid as the selector ref form, so downstream error
// text reports the identity that was actually selected rather than a name that
// was never typed.
func uidRef(kind coremetadata.Kind, uid string) selector.Ref {
	return selector.Ref{Kind: kind, UID: uid, Raw: selector.UIDPrefix + uid}
}


// payloadCommand renders the `--` payload as the one-time name-derivation
// source stored on the created Pane.
func payloadCommand(payload []string) string {
	return strings.TrimSpace(strings.Join(payload, " "))
}


// paneTarget is one preflighted (Window, anchor Pane) pair.
type paneTarget struct {
	windowUID    string
	anchorUID    string
	storedAnchor bool
	virtual      bool
}


// panePlan is the preflight result of a `create pane` fan-out.
type panePlan struct {
	query              selector.Query
	targets            []paneTarget
	missingWindowNames []string
}


// selectedWindowUIDs is the planner's target decision, in planner order.
//
// It is the authority the operation receipt quotes. Deriving the selected set
// from the created results instead would agree only on the happy path and only
// for the routes that create one resource per target Window, and it would make
// the same argv report a different selection per provider the moment a provider
// refuses. Quoting the planner keeps `create pane`, `create agent`, and the
// three provider shortcuts reporting one identical set for one identical argv.
func (p panePlan) selectedWindowUIDs() []string {
	uids := make([]string, 0, len(p.targets))
	for _, target := range p.targets {
		uids = append(uids, target.windowUID)
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
func (c *createCommand) resolveSplitTargets(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	scope createScope,
	flags resourceCreateFlags,
	fanOut selector.Target,
	spelling, operationID string,
) (panePlan, []windowWork, error) {
	plan, err := c.planPaneTargets(*working, project, scope, flags, fanOut, spelling)
	if err != nil {
		return panePlan{}, nil, err
	}
	if flags.name != "" && len(plan.targets)+len(plan.missingWindowNames) > 1 {
		return panePlan{}, nil, MapMetadataError(coremetadata.ExplicitNameCardinalityError(
			spelling, len(plan.targets)+len(plan.missingWindowNames)))
	}
	var windows []windowWork
	for _, missing := range plan.missingWindowNames {
		work, err := c.allocateWindow(working, mutator, project, windowRequest{
			name:        missing,
			operationID: operationID,
		})
		if err != nil {
			return panePlan{}, nil, err
		}
		windows = append(windows, work)
		// The Window this operation created owns a brand new initial Pane,
		// which is both its default shell ref and this operation's anchor.
		plan.targets = append(plan.targets, paneTarget{
			windowUID:    work.window.Metadata.UID,
			anchorUID:    work.initial.Metadata.UID,
			storedAnchor: true,
		})
	}
	slices.SortStableFunc(plan.targets, func(a, b paneTarget) int {
		return cmp.Compare(a.windowUID, b.windowUID)
	})
	return plan, windows, nil
}


// planPaneTargets resolves every target Window and its anchor Pane without
// mutating anything.
func (c *createCommand) planPaneTargets(
	registry coremetadata.Registry,
	project coremetadata.Project,
	scope createScope,
	flags resourceCreateFlags,
	fanOut selector.Target,
	spelling string,
) (panePlan, error) {
	query := scope.projectQuery()
	// Both spellings of the exact-one primary selection resolve through this one
	// call, so the flag and the bare `--project P` scope cannot drift apart --
	// including on the empty and dangling refusals, which they share.
	if spelled := flags.primaryWindowSpelling(); spelled != "" {
		ref, err := primaryWindowRef(registry, project, spelling, spelled)
		if err != nil {
			return panePlan{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.windows {
		ref, err := selector.ParseRef(coremetadata.KindWindow, raw)
		if err != nil {
			return panePlan{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.selectors {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return panePlan{}, err
		}
		query.Labels = append(query.Labels, label)
	}
	if scope.window != nil && len(query.Windows) == 0 && len(query.Labels) == 0 {
		query.Windows = append(query.Windows, *scope.window)
	}

	plan := panePlan{query: query}
	resolver := selector.New(registry)
	resolution, err := resolver.ResolveWindows(query)
	if err != nil {
		return panePlan{}, err
	}

	if flags.createWindow {
		// A missing exact name is promoted to a Window ensure. Everything else
		// keeps the default no-match behavior.
		plan.missingWindowNames = unresolvedWindowNames(registry, project, flags.windows)
	}
	if len(resolution.Matches) == 0 && len(plan.missingWindowNames) == 0 {
		// The fan-out cell belongs to the caller, not to this helper: `create
		// pane` fans out over the Windows it selected, and `create agent` over
		// the Agents it produces, one per selected Window.
		return panePlan{}, selector.Enforce(fanOut, selector.DescribeSelector(query), resolution)
	}

	for _, match := range resolution.Matches {
		anchorUID, storedAnchor, err := c.resolveAnchor(registry, project, scope, match.UID, flags, spelling)
		if err != nil {
			return panePlan{}, err
		}
		virtual := registry.IsVirtualWindow(match.UID) && storedAnchor
		if !virtual {
			if _, _, err := c.processRuntime.admit(registry, anchorUID, resourcegraph.ProcessCreatePane); err != nil {
				return panePlan{}, err
			}
		}
		plan.targets = append(plan.targets, paneTarget{windowUID: match.UID, anchorUID: anchorUID, storedAnchor: storedAnchor, virtual: virtual})
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
func primaryWindowRef(registry coremetadata.Registry, project coremetadata.Project, spelling, spelled string) (selector.Ref, error) {
	primary := strings.TrimSpace(project.Spec.PrimaryWindowRef)
	if primary == "" {
		return selector.Ref{}, usageError(fmt.Sprintf(
			"%s %s: project/%s has no spec.primaryWindowRef; create a Window first or name one with --window <ref>",
			spelling, spelled, project.Metadata.Name))
	}
	window, ok := registry.Window(primary)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject ||
		window.Metadata.OwnerUID() != project.Metadata.UID {
		return selector.Ref{}, usageError(fmt.Sprintf(
			"%s %s: project/%s spec.primaryWindowRef %q is dangling or owned by another Project; name the target with --window <ref>",
			spelling, spelled, project.Metadata.Name, primary))
	}
	return uidRef(coremetadata.KindWindow, primary), nil
}


// unresolvedWindowNames returns the exact-name --window occurrences that match
// no Window in the Project, deduplicated and in argv order.
func unresolvedWindowNames(registry coremetadata.Registry, project coremetadata.Project, refs []string) []string {
	existing := map[string]bool{}
	for _, window := range registry.WindowsOf(project.Metadata.UID) {
		existing[window.Metadata.Name] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, raw := range refs {
		if !isExactNameRef(raw) || existing[raw] || seen[raw] {
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
func (c *createCommand) resolveAnchor(registry coremetadata.Registry, project coremetadata.Project, scope createScope, windowUID string, flags resourceCreateFlags, spelling string) (string, bool, error) {
	window, ok := registry.Window(windowUID)
	if !ok {
		return "", false, fmt.Errorf("%s: window %q disappeared during preflight", spelling, windowUID)
	}
	panes := flags.panes
	if len(panes) == 0 && scope.pane != nil {
		panes = repeatedFlag{scope.pane.Raw}
	}
	if len(panes) > 0 {
		query := scope.projectQuery()
		query.Windows = []selector.Ref{{Kind: coremetadata.KindWindow, UID: windowUID, Raw: window.Metadata.Name}}
		for _, raw := range panes {
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
			return "", false, usageError(fmt.Sprintf(
				"%s: explicit pane %q is not owned by window/%s; refusing cross-Window anchor",
				spelling, paneUID, window.Metadata.Name))
		}
		return paneUID, false, nil
	}

	anchor := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if anchor == "" {
		return "", true, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef; pass an explicit --pane <ref>",
			spelling, window.Metadata.Name, project.Metadata.Name))
	}
	if resolved, ok := registry.WindowAnchor(window.Metadata.UID); !ok || resolved.Metadata.UID != anchor {
		return "", true, usageError(fmt.Sprintf(
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
func (c *createCommand) resolveProject(registry coremetadata.Registry, scope createScope) (coremetadata.Project, error) {
	query := scope.projectQuery()
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
func (c *createCommand) refuseMissingRoot(project coremetadata.Project) error {
	condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot)
	if !ok || condition.Status != coremetadata.ConditionTrue {
		return nil
	}
	return usageError(fmt.Sprintf(
		"create: project/%s carries a MissingRoot condition for %q; rebind it before creating resources",
		project.Metadata.Name, project.Spec.Root))
}


// windowRequest describes one Window creation.
type windowRequest struct {
	name        string
	labels      map[string]string
	payload     []string
	operationID string
}


// windowWork is one allocated Window plus the runtime handles it acquires.
type windowWork struct {
	window  coremetadata.Window
	initial coremetadata.Pane
	payload []string
	// activation is the generation the initial Pane's supervised launch was
	// issued.
	activation superviseSpec
	// initialPaneID is the tmux pane id of the Window's initial Pane, filled in
	// by the runtime phase.
	initialPaneID string
}


// allocateWindow creates one Window and its initial Pane in metadata only.
func (c *createCommand) allocateWindow(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	req windowRequest,
) (windowWork, error) {
	command := payloadCommand(req.payload)
	window, panes, err := mutator.AddWindow(working, project.Metadata.UID, coremetadata.BootstrapWindow{
		Name:    req.name,
		Command: command,
		Labels:  req.labels,
		Panes: []coremetadata.BootstrapPane{{
			Command: command,
			CWD:     project.Spec.Root,
			Labels:  req.labels,
		}},
	}, c.shell, req.operationID)
	if err != nil {
		return windowWork{}, MapMetadataError(err)
	}
	activation, err := c.issuePaneActivation(working, mutator, panes[0].Metadata.UID, "", req.operationID)
	if err != nil {
		return windowWork{}, err
	}
	return windowWork{window: window, initial: panes[0], payload: req.payload, activation: activation}, nil
}


// materializeWindow creates the detached tmux window for an allocated Window and
// mirrors both identities onto it.
func (c *createCommand) materializeWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName string,
	work *windowWork,
) error {
	// A Project whose runtime this operation just created adopts its first
	// stored Window onto the session's mandatory initial tmux window
	// (ensureProjectRuntime -> adoptInitialWindow). On a Project that owned no
	// Window at all, that first Window is the one this operation allocated a
	// moment earlier, so creating a second tmux window for it would leave two
	// tmux windows mirroring one stable UID with the adopted one stranded. The
	// adopted runtime already is this Window's materialization, so bind the
	// allocated initial Pane to it instead of creating anything.
	if owner, adopted := ledger.CurrentWindow(work.window.Metadata.UID); adopted {
		return c.bindAdoptedWindow(ctx, working, mutator, work, owner)
	}
	launch := c.runtime.SupervisedLaunch(ctx, work.activation, work.payload)
	created, err := c.runtime.NewWindow(ctx, sessionName, work.window.Metadata.Name, project.Spec.Root, launch)
	if created.WindowID == "" {
		return err
	}
	if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, work.window.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	if mirrorErr := c.runtime.MirrorWindowIdentity(ctx, created.WindowID, work.window); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	if strings.TrimSpace(sessionName) != "" || strings.TrimSpace(created.WindowID) != "" {
		projected, bindingErr := mutator.ObserveWindowRuntimeBinding(
			working, work.window.Metadata.UID, sessionName, created.WindowID,
		)
		if bindingErr != nil {
			return errors.Join(err, bindingErr)
		}
		work.window = projected
		ledger.ObserveCurrentWindow(work.window.Metadata.UID, RuntimeOwner{
			SessionID: projected.Status.RuntimeSessionID, WindowID: projected.Status.RuntimeID,
		})
	}
	if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, work.initial.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	work.initialPaneID = created.PaneID
	if mirrorErr := c.runtime.MirrorPaneIdentity(ctx, work.initialPaneID, work.initial); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	observeActivationRuntime(working, mutator, work.activation, work.initialPaneID, c.runtime.Warn)
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
func (c *createCommand) bindAdoptedWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	work *windowWork,
	owner RuntimeOwner,
) error {
	if projected, ok := working.Window(work.window.Metadata.UID); ok {
		work.window = *projected
	}
	panes, err := c.runtime.PanesOf(ctx, owner.WindowID)
	if err != nil {
		return err
	}
	for _, row := range panes {
		if row[0] != work.initial.Metadata.UID {
			continue
		}
		work.initialPaneID = row[1]
		observeActivationRuntime(working, mutator, work.activation, work.initialPaneID, c.runtime.Warn)
		return nil
	}
	return fmt.Errorf(
		"create: window/%s was adopted onto tmux window %s, which has no live binding for its initial pane/%s; "+
			"stop the Project and retry the same create",
		work.window.Metadata.Name, owner.WindowID, work.initial.Metadata.Name)
}


// ensureAnchorPane returns the live tmux pane id of the preflighted anchor,
// materializing the Window it belongs to when that Window is still offline.
func (c *createCommand) ensureAnchorPane(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName, operationID string,
	target paneTarget,
) (string, error) {
	registry := *working
	window, ok := registry.Window(target.windowUID)
	if !ok {
		return "", fmt.Errorf("create pane: window %q disappeared before the mutation ran", target.windowUID)
	}
	anchor, ok := registry.Pane(target.anchorUID)
	if !ok {
		return "", fmt.Errorf("create pane: anchor pane %q disappeared before the mutation ran", target.anchorUID)
	}
	if _, ok := registry.PaneInWindow(window.Metadata.UID, anchor.Metadata.UID); !ok {
		return "", fmt.Errorf("create pane: anchor pane/%s is not owned by window/%s", anchor.Metadata.Name, window.Metadata.Name)
	}
	if target.storedAnchor {
		stored, ok := registry.WindowAnchor(window.Metadata.UID)
		if !ok || stored.Metadata.UID != anchor.Metadata.UID {
			return "", fmt.Errorf("create pane: window/%s stored anchor %q is dangling or cross-Window", window.Metadata.Name, target.anchorUID)
		}
	}

	if c.processRuntime != nil {
		if c.processRuntime.inventory().Declares(*anchor) {
			processAnchor := &processTerminalTarget{runtime: c.processRuntime, registry: registry.Clone(), paneUID: anchor.Metadata.UID}
			if err := processAnchor.AdmitSplitAnchor(anchor.Status.Activation.RuntimeID); err != nil {
				return "", err
			}
		}
	}

	owner, observedCurrent := ledger.CurrentWindow(target.windowUID)
	if observedCurrent && owner.SessionID != sessionName {
		return "", fmt.Errorf("create pane: Window uid %q moved outside current Project session %s", target.windowUID, sessionName)
	}
	windowID := owner.WindowID
	var err error
	if !observedCurrent {
		windowID, err = c.runtime.WindowIDForUID(ctx, sessionName, target.windowUID)
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
			if !target.storedAnchor {
				return "", fmt.Errorf("create pane: explicit anchor pane/%s is offline; refusing to infer a different Pane", anchor.Metadata.Name)
			}
			bootstrap, _, err = mutator.EnsureWindowDefaultShell(working, window.Metadata.UID, c.shell, operationID)
			if err != nil {
				return "", MapMetadataError(err)
			}
			window, _ = working.Window(window.Metadata.UID)
		}
		bootstrapActivation, activationErr := c.issuePaneActivation(working, mutator, bootstrap.Metadata.UID, "", operationID)
		if activationErr != nil {
			return "", activationErr
		}
		created, createErr := c.runtime.NewWindow(ctx, sessionName, window.Metadata.Name, project.Spec.Root,
			c.runtime.SupervisedLaunch(ctx, bootstrapActivation, nil))
		windowID, err = created.WindowID, createErr
		if windowID != "" {
			if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimeWindow, windowID, window.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.MirrorWindowIdentity(ctx, windowID, *window); mirrorErr != nil {
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
			if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, bootstrap.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.MirrorPaneIdentity(ctx, created.PaneID, bootstrap); mirrorErr != nil {
				return "", errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, bootstrapActivation, created.PaneID, c.runtime.Warn)
			return created.PaneID, err
		}
		if err != nil {
			return "", err
		}
	}

	panes, err := c.runtime.PanesOf(ctx, windowID)
	if err != nil {
		return "", err
	}
	for _, row := range panes {
		if row[0] == target.anchorUID {
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
func (c *createCommand) ensureProjectRuntime(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	operationID string,
	ledger *RuntimeLedger,
) (string, error) {
	if err := c.ensureRuntimeRoute(ctx); err != nil {
		return "", err
	}
	sessionName := c.reconciler.ProjectPhysicalSessionName(*working, project)
	if sessionName == "" {
		return "", fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
	}
	exists, err := c.runtime.Sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return "", fmt.Errorf("create: inspect Project runtime %q: %w", sessionName, err)
	}
	if !exists {
		windows := terminalWindows(*working, project.Metadata.UID)
		if len(windows) > 0 {
			if _, ok := working.WindowDefaultShell(windows[0].Metadata.UID); !ok {
				if _, _, err := mutator.EnsureWindowDefaultShell(working, windows[0].Metadata.UID, c.shell, operationID); err != nil {
					return "", MapMetadataError(err)
				}
			}
		}
	}
	created, initialActivation, err := c.runtime.EnsureSessionLaunching(ctx, project, sessionName, project.Spec.Root,
		initialWindowName(working, project.Metadata.UID), c.initialPaneActivation(working, mutator, project.Metadata.UID, operationID), ledger)
	if err != nil {
		return "", err
	}
	if created.Created {
		// `new-session` always brings one window and one pane with it. Binding
		// them to the Project's first bootstrap Window is what keeps the runtime
		// a projection of the stored topology instead of an orphan window
		// sitting next to it.
		if err := c.adoptInitialWindow(ctx, working, mutator, project, created, initialActivation, ledger); err != nil {
			return "", err
		}
	}
	// Re-project every already-live Window from its stable UID. The Registry's
	// cached $/@ pair is diagnostic only and may name a prior tmux generation;
	// this current pair and the positive MissingRuntime clear commit atomically
	// with the create operation.
	windowRuntimes, observeErr := c.runtime.WindowRuntimeInventory(ctx, sessionName)
	if observeErr != nil {
		return "", observeErr
	}
	ledger.ResetCurrentWindows()
	for _, window := range working.WindowsOf(project.Metadata.UID) {
		owner, found, observeErr := resolveWindowRuntimeUID(windowRuntimes, window.Metadata.UID)
		if observeErr != nil {
			return "", observeErr
		}
		if !found {
			continue
		}
		if owner.SessionID != created.SessionID {
			return "", fmt.Errorf("create: Window uid %q resolved outside current Project session %s", window.Metadata.UID, created.SessionID)
		}
		if _, bindingErr := c.observeWindowRuntimeBinding(mutator, working, window.Metadata.UID, owner.SessionID, owner.WindowID); bindingErr != nil {
			return "", bindingErr
		}
		ledger.ObserveCurrentWindow(window.Metadata.UID, owner)
	}
	if _, err := mutator.BindLiveProjectSession(working, project.Metadata.UID, sessionName, c.runtime.ExpectedSocketPath); err != nil {
		return "", MapMetadataError(err)
	}
	if err := c.runtime.FinalizeSessionStartup(ctx, created, sessionName, project.Spec.Root, ledger); err != nil {
		return "", err
	}
	return created.SessionID, nil
}


// initialWindowName is the Registry name of the Window adoptInitialWindow binds
// a freshly created session's own first Window to, so new-session can create
// that Window under its Registry name. A Project with no Window has nothing to
// adopt, and the blank name keeps tmux's default.
func initialWindowName(registry *coremetadata.Registry, projectUID string) string {
	windows := terminalWindows(*registry, projectUID)
	if len(windows) == 0 {
		return ""
	}
	return windows[0].Metadata.Name
}


// initialPaneActivation issues the activation of the default shell Pane
// adoptInitialWindow will bind to a new session's first Pane, so that Pane is
// launched under its own generation like any other Pane this create starts.
func (c *createCommand) initialPaneActivation(registry *coremetadata.Registry, mutator coremetadata.Mutator, projectUID, operationID string) sessionFirstPaneActivation {
	return func() (superviseSpec, error) {
		windows := terminalWindows(*registry, projectUID)
		if len(windows) == 0 {
			return superviseSpec{}, nil
		}
		primary, ok := registry.WindowDefaultShell(windows[0].Metadata.UID)
		if !ok {
			return superviseSpec{}, nil
		}
		return c.issuePaneActivation(registry, mutator, primary.Metadata.UID, "", operationID)
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
func (c *createCommand) adoptInitialWindow(ctx context.Context, registry *coremetadata.Registry, mutator coremetadata.Mutator, project coremetadata.Project, created intmux.NewSessionResult, activation superviseSpec, ledger *RuntimeLedger) error {
	windows := terminalWindows(*registry, project.Metadata.UID)
	if len(windows) == 0 {
		return nil
	}
	first := windows[0]
	if err := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, first.Metadata.UID, ledger); err != nil {
		return err
	}
	if err := c.runtime.MirrorWindowIdentity(ctx, created.WindowID, first); err != nil {
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
	if err := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, primary.Metadata.UID, ledger); err != nil {
		return err
	}
	if err := c.runtime.MirrorPaneIdentity(ctx, created.PaneID, *primary); err != nil {
		return err
	}
	if activation.PaneUID == primary.Metadata.UID {
		observeActivationRuntime(registry, mutator, activation, created.PaneID, c.runtime.Warn)
	}
	return nil
}


// projectOwnershipGuard contains the selected session before lifecycle
// reconciliation can import, adopt, or re-mirror anything in it. Existing
// Projects require the complete UID/root/duplicate proof. A Project visible
// only through discovery has no durable UID proof yet; if its would-be session
// name is already live, create refuses rather than importing that blank or
// foreign runtime under a freshly allocated identity.
func (c *createCommand) projectOwnershipGuard(scope createScope) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (liveSessionIdentity, error) {
		project, err := c.resolveProject(working, scope)
		if err == nil {
			sessionName := c.reconciler.ProjectPhysicalSessionName(working, project)
			if sessionName == "" {
				return liveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
			}
			identity, _, err := c.runtime.PreflightSessionOwnership(ctx, project, sessionName)
			if identity.Name == "" {
				identity.Name = sessionName
			}
			return identity, err
		}
		query := scope.projectQuery()
		var selectorErr *selector.SelectorError
		if query.Project == nil || query.Project.IsUID() || !errors.As(err, &selectorErr) || !selectorErr.IsNoMatch() {
			return liveSessionIdentity{}, err
		}
		// Even when the name is neither registered nor discovered, a live
		// same-name session must not become the selector's identity source via
		// lifecycle import.
		if err := c.runtime.RefuseUnregisteredSessionClaims(ctx, query.Project.Name, ""); err != nil {
			return liveSessionIdentity{}, err
		}

		// A name that matches no Project used to be a name that was about to
		// become one: the reconcile prelude registered every discovered workdir,
		// so this guard only had to predict the name that registration would
		// allocate. Registration is explicit now, so the honest answer is a
		// refusal -- and the useful refusal names the exact path and the route
		// that would register it, which is why discovery is still read here.
		return liveSessionIdentity{}, c.unregisteredProjectRefusal(query.Project.Name, err)
	}
}


// unregisteredProjectRefusal turns a Project selector no-match into an actionable
// bootstrap instruction.
//
// Discovery is consulted for the message and for nothing else. Finding a candidate
// directory whose basename matches the requested name does not register it, does
// not reserve the name, and does not change the refusal -- it only lets the error
// name the exact `--root` an operator would pass next.
func (c *createCommand) unregisteredProjectRefusal(name string, cause error) error {
	name = strings.TrimSpace(name)
	if root := c.discoveredCandidateRootFor(name); root != "" {
		return usageError(fmt.Sprintf(
			"create: no Registry Project is named %q; %q is a discovered directory that no Project claims. Register it with `projmux create project --root %s`, or open it once from the Project sidebar",
			name, root, root))
	}
	return cause
}


// discoveredCandidateRootFor returns the discovered, unregistered directory whose
// automatic Project name would be name, or empty when there is none.
func (c *createCommand) discoveredCandidateRootFor(name string) string {
	if name == "" || c.reconciler == nil || c.reconciler.DiscoverRoots == nil {
		return ""
	}
	roots, err := c.reconciler.DiscoverRoots()
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


func (c *createCommand) exactProjectOwnershipGuard(projectUID string) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, _ coremetadata.Mutator, _ string) (liveSessionIdentity, error) {
		project, ok := working.Project(projectUID)
		if !ok {
			return liveSessionIdentity{}, fmt.Errorf("create: project %q disappeared before ownership preflight", projectUID)
		}
		sessionName := c.reconciler.ProjectPhysicalSessionName(working, *project)
		if sessionName == "" {
			return liveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
		}
		identity, _, err := c.runtime.PreflightSessionOwnership(ctx, *project, sessionName)
		if identity.Name == "" {
			identity.Name = sessionName
		}
		return identity, err
	}
}

