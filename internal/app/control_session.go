package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The control-session convergence pass: the writer half of the control marker
// and of Home's Window/Pane identity mirror.
//
// The reading half already existed. resourcegraph.ControlSessionRole,
// Session.isControlSession, and the ClassControl branch of the resolver all read
// `@projmux_session_role`, and tmuxopts.SessionRole says in as many words that
// the marker's writer and lifecycle belong to the control-session surface. This
// is that surface. Until it existed the option had no producer at all: pane %0 of
// Home carried no `@projmux_window_uid`, so no owner chain could be derived from
// it and every route that resolves "the active target" refused inside Home.
//
// Four properties are contractual, and each is a decision rather than an
// implementation detail:
//
//  1. It runs from exact app lifecycle/config declarations and from existing
//     ControlSession identities only. A session whose ownership goes to a
//     Project target is not a control session and never gets the marker. No read
//     verb reaches this code, so no read verb can write the marker.
//  2. Every live ownership and identity claim is verified before anything is
//     written: the server carries `@projmux_app=1`, the exact session is not
//     ephemeral, and neither a Project uid nor a foreign role claims it. A
//     marker no reader will trust is just litter left on someone else's tmux;
//     contradictory identity is a refusal, never permission to rewrite it.
//  3. Every tmux call is routed through the one explicit `-L <socket>` target the
//     invocation was given. There is no unprefixed tmux call here, so this pass
//     can never mark a session on the default server or on a sibling socket, and
//     every write names one `-t <session>` target rather than a pattern or `-g`.
//  4. It converges. A brand-new Home and an already-live Home take the same path,
//     the registry commit goes through the store's convergent writer, and a
//     second pass over an already-converged Home performs zero Registry and tmux
//     writes.

// controlSessionSkip states why a convergence pass declined to do anything.
//
// A skip is not an execution error: shell must still attach and config apply's
// successful source-file reload must stay successful. The reason is carried so
// their public diagnostics say which refusal fired instead of reporting silence.
type controlSessionSkip string

const (
	// controlSessionSkipNotAppOwned is a server carrying no @projmux_app=1.
	controlSessionSkipNotAppOwned controlSessionSkip = "declared control target is not app-owned (@projmux_app is not 1)"
	// controlSessionSkipEphemeral is a session carrying @projmux_ephemeral=1.
	// Ephemeral grants nothing, and control plus ephemeral is the pair the
	// resolved graph fails closed on.
	controlSessionSkipEphemeral controlSessionSkip = "declared control target is ephemeral (@projmux_ephemeral=1)"
)

// controlSessionConvergence reports what one pass did.
type controlSessionConvergence struct {
	// skipped states why the pass declined, empty when it ran.
	skipped controlSessionSkip
	// controlUID is the ControlSession the pass settled on, empty when skipped.
	controlUID string
	// changed reports whether this pass executed any Registry or tmux plan
	// write. A converged repeat pass reports false, which is the idempotence
	// property.
	changed bool
	// writes counts declarative Registry/tmux plan items executed. It is zero on
	// a converged second pass and on every refusal.
	writes int
	// windows and panes count the objects the pass bound, created or not.
	windows int
	panes   int
}

// controlSessionConverger converges one app-owned control session.
type controlSessionConverger struct {
	// runner is the raw tmux subprocess seam. Every call it makes is wrapped in
	// the explicit-socket router below.
	runner tmuxCommandRunner
	// resources is the registry seam. Writes go through its convergent writer so
	// an already-converged pass leaves registry.json byte-identical.
	resources *resourceStore
	// shell is the configured shell path; its basename seeds a minted Pane name
	// exactly as it does for every other registry-backed creation path.
	shell string
	// newOperationID labels the transaction ledger. Injectable so a test can
	// pin the label.
	newOperationID func() (string, error)
}

func newControlSessionConverger(runner tmuxCommandRunner, shell string) *controlSessionConverger {
	return &controlSessionConverger{
		runner:         runner,
		resources:      newResourceStore(),
		shell:          shell,
		newOperationID: newCreateOperationID,
	}
}

// converge brings one control session's Registry identity and tmux markers into
// agreement.
//
// The order is load bearing. The registry transaction commits first, and only
// then are the tmux options written: tmux options are not transactional, so a
// pass that wrote them first and then failed validation would leave uids on the
// machine that no resource backs. Committing first can leave the opposite
// mismatch -- a resource whose tmux object carries no uid yet -- and that one
// repairs itself, because the next pass sees an unmirrored live window and adopts
// the existing unbound Window instead of minting a second one.
func (c *controlSessionConverger) converge(ctx context.Context, socketName, sessionName string) (controlSessionConvergence, error) {
	if c == nil || c.runner == nil {
		return controlSessionConvergence{}, errors.New("control session convergence requires a tmux runner")
	}
	target, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	return c.convergeTarget(ctx, target, sessionName)
}

// convergeTarget is shared by shell lifecycle and config-apply. The caller has
// already declared both coordinates; there is no inherited/default socket
// fallback and no session-name inference inside this method.
func (c *controlSessionConverger) convergeTarget(ctx context.Context, target tmuxTransport, sessionName string) (controlSessionConvergence, error) {
	return c.convergeTargetWithEvidence(ctx, target, sessionName, true)
}

func (c *controlSessionConverger) convergeTargetWithEvidence(ctx context.Context, target tmuxTransport, sessionName string, declared bool) (controlSessionConvergence, error) {
	if c == nil || c.runner == nil {
		return controlSessionConvergence{}, errors.New("control session convergence requires a tmux runner")
	}
	if target.Flag() == "" || strings.TrimSpace(target.Value) == "" || strings.TrimSpace(sessionName) == "" {
		return controlSessionConvergence{}, errors.New("control session convergence requires an exact tmux target and session declaration")
	}
	mirror := intmetadata.NewMirror(explicitTmuxRunner{runner: c.runner, target: target})

	markers, err := mirror.ObserveControlSessionMarkers(ctx, sessionName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	observed, targets, err := mirror.ObserveControlSession(ctx, sessionName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	registryBefore, err := c.resources.load()
	if err != nil {
		return controlSessionConvergence{}, err
	}
	plan := controller.PlanControlTargetConvergence(controlTargetControllerState(target, sessionName, declared, markers, registryBefore, observed, targets))
	if plan.Refused() {
		return controlSessionConvergence{skipped: controlSessionSkip(plan.Reason)}, nil
	}
	if plan.Converged() {
		control, _ := registryBefore.ControlSessionBySession(sessionName)
		uid := ""
		if control != nil {
			uid = control.Metadata.UID
		}
		return controlSessionConvergence{controlUID: uid}, nil
	}

	operationID, err := c.operationID()
	if err != nil {
		return controlSessionConvergence{}, err
	}

	var binding coremetadata.ControlSessionBinding
	var registry coremetadata.Registry
	changed, err := c.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		// The preflight above avoids opening a Registry transaction for a refusal
		// or a converged target. Once a write is needed, none of its live evidence
		// authorizes the transaction: another actor can claim the exact session or
		// one of its descendants while this worker waits for the Registry lock.
		// Re-read every marker, object and mirror while holding that lock, then
		// derive both the plan and binding from that one guarded observation.
		lockedMarkers, err := mirror.ObserveControlSessionMarkers(ctx, sessionName)
		if err != nil {
			return err
		}
		lockedObserved, lockedTargets, err := mirror.ObserveControlSession(ctx, sessionName)
		if err != nil {
			return err
		}
		lockedRuntime, err := observeMirroredUIDs(ctx, mirror)
		if err != nil {
			return err
		}
		lockedPlan := controller.PlanControlTargetConvergence(controlTargetControllerState(target, sessionName, declared, lockedMarkers, *working, lockedObserved, lockedTargets))
		if lockedPlan.Refused() {
			return controlTargetRefusal{reason: lockedPlan.Reason}
		}
		plan = lockedPlan
		targets = lockedTargets
		if !controlPlanNeedsBinding(plan) {
			registry = working.Clone()
			if control, ok := working.ControlSessionBySession(sessionName); ok {
				binding.ControlSession = control.Clone()
			}
			return nil
		}
		// The matcher is built inside the callback because the convergent writer
		// may run it more than once, and a matcher carries the claims of exactly
		// one pass. Reusing one across attempts would leave every candidate
		// claimed and turn the second attempt into a mint-everything pass.
		binder := coremetadata.NewBindingMatcher(lockedRuntime)
		result, err := mutator.BindControlSession(working, lockedObserved, c.shell, operationID, binder)
		if err != nil {
			return err
		}
		binding = result
		registry = working.Clone()
		return nil
	})
	if err != nil {
		var refusal controlTargetRefusal
		if errors.As(err, &refusal) {
			return controlSessionConvergence{skipped: controlSessionSkip(refusal.reason)}, nil
		}
		return controlSessionConvergence{}, err
	}

	writes, err := executeControlSessionIdentityPlan(ctx, target, sessionName, mirror, registry, binding, targets,
		controlPlanHas(plan, controller.ControlEnsureRole))
	if err != nil {
		return controlSessionConvergence{}, err
	}
	if changed {
		writes++
	}
	return controlSessionConvergence{
		controlUID: binding.ControlSession.Metadata.UID,
		changed:    writes > 0,
		writes:     writes,
		windows:    len(binding.Windows),
		panes:      len(binding.Panes),
	}, nil
}

type controlTargetRefusal struct{ reason string }

func (e controlTargetRefusal) Error() string { return e.reason }

func controlPlanHas(plan controller.ControlTargetPlan, step controller.ControlTargetStep) bool {
	return slices.ContainsFunc(plan.Actions, func(action controller.ControlTargetAction) bool { return action.Step == step })
}

func controlPlanNeedsBinding(plan controller.ControlTargetPlan) bool {
	return controlPlanHas(plan, controller.ControlEnsureRoot) ||
		controlPlanHas(plan, controller.ControlEnsureWindowMirror) ||
		controlPlanHas(plan, controller.ControlEnsurePaneMirror)
}

func controlTargetControllerState(target tmuxTransport, sessionName string, declared bool, markers intmetadata.ControlSessionMarkers,
	registry coremetadata.Registry, observed coremetadata.ControlSessionObservation, targets intmetadata.LegacyTargets,
) controller.ControlTargetState {
	state := controller.ControlTargetState{
		Declaration: controller.ControlTargetDeclaration{Transport: target.ExplicitProjection(), Session: sessionName, Declared: declared},
		AppOwned:    markers.AppOwned, Ephemeral: markers.Ephemeral, Role: markers.Role, ProjectUID: markers.ProjectUID,
	}
	for _, project := range registry.Projects {
		if project.Metadata.UID == markers.ProjectUID {
			state.ProjectKnown = true
			break
		}
	}
	for _, control := range registry.ControlSessions {
		if control.Spec.Session == sessionName {
			state.RootUIDs = append(state.RootUIDs, control.Metadata.UID)
		}
	}
	for wi, window := range observed.Windows {
		handle := ""
		if wi < len(targets.Windows) {
			handle = targets.Windows[wi]
		}
		claim := controller.ControlWindowClaim{ControlMirrorClaim: controlWindowClaim(registry, handle, window.UID)}
		for pi, pane := range window.Panes {
			paneHandle := ""
			if wi < len(targets.Panes) && pi < len(targets.Panes[wi]) {
				paneHandle = targets.Panes[wi][pi]
			}
			claim.Panes = append(claim.Panes, controlPaneClaim(registry, paneHandle, pane.UID))
		}
		state.Windows = append(state.Windows, claim)
	}
	return state
}

func controlWindowClaim(registry coremetadata.Registry, handle, uid string) controller.ControlMirrorClaim {
	claim := controller.ControlMirrorClaim{Handle: handle, UID: strings.TrimSpace(uid)}
	window, ok := registry.Window(claim.UID)
	if !ok {
		return claim
	}
	claim.Known = true
	if owner := window.Metadata.OwnerRef; owner != nil {
		claim.RootKind, claim.RootUID = string(owner.Kind), owner.UID
	}
	return claim
}

func controlPaneClaim(registry coremetadata.Registry, handle, uid string) controller.ControlMirrorClaim {
	claim := controller.ControlMirrorClaim{Handle: handle, UID: strings.TrimSpace(uid)}
	pane, ok := registry.Pane(claim.UID)
	if !ok {
		return claim
	}
	claim.Known = true
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return claim
	}
	var window *coremetadata.Window
	switch owner.Kind {
	case coremetadata.KindWindow:
		window, _ = registry.Window(owner.UID)
	case coremetadata.KindAgent:
		if agent, exists := registry.Agent(owner.UID); exists && agent.Metadata.OwnerRef != nil {
			window, _ = registry.Window(agent.Metadata.OwnerRef.UID)
		}
	}
	if window == nil {
		return claim
	}
	claim.WindowUID = window.Metadata.UID
	if root := window.Metadata.OwnerRef; root != nil {
		claim.RootKind, claim.RootUID = string(root.Kind), root.UID
	}
	return claim
}

func (c *controlSessionConverger) operationID() (string, error) {
	if c.newOperationID == nil {
		return "op-control-session", nil
	}
	return c.newOperationID()
}

// observeMirroredUIDs reads the live Window and Pane uid inventory the adoption
// matcher needs.
//
// It is taken before anything is written, which is what lets adoption refuse to
// steal a uid that is already the binding of some other live tmux object. An
// empty observation is the fail-closed reading: it can never invent a binding,
// only decline to protect one that no longer exists.
func observeMirroredUIDs(ctx context.Context, mirror intmetadata.Mirror) (coremetadata.RuntimeObservation, error) {
	windows, err := mirror.LiveWindowUIDs(ctx)
	if err != nil {
		return coremetadata.RuntimeObservation{}, err
	}
	panes, err := mirror.LivePaneUIDs(ctx)
	if err != nil {
		return coremetadata.RuntimeObservation{}, err
	}
	return coremetadata.RuntimeObservation{Windows: windows, Panes: panes}, nil
}

// mirrorControlSessionIdentity writes the uids a control-session bind settled on
// back onto exactly the tmux objects it bound.
//
// It is the mirrorImported contract applied to the control session, including its
// skip: a rebound object already carries the exact registry uid, so re-writing it
// would spend tmux calls to change nothing. Created and adopted objects go
// through the same MirrorWindow / MirrorPane calls every other managed path uses,
// so a control-session Window gets the same `automatic-rename off` and the same
// name projections a Project's Window does -- there is deliberately no second
// writer with weaker guarantees.
func executeControlSessionIdentityPlan(
	ctx context.Context,
	target tmuxTransport,
	sessionName string,
	mirror intmetadata.Mirror,
	registry coremetadata.Registry,
	binding coremetadata.ControlSessionBinding,
	targets intmetadata.LegacyTargets,
	ensureRole bool,
) (int, error) {
	runner, ok := mirror.Runner.(tmuxCommandRunner)
	if !ok || runner == nil {
		return 0, errors.New("ControlSession identity plan requires a tmux runner")
	}
	transport := runner
	var boundPaths []string
	for {
		switch explicit := transport.(type) {
		case explicitTmuxRunner:
			if explicit.target.Flag() == "-L" && !explicit.target.SameRoute(target) {
				return 0, errors.New("ControlSession identity plan route disagrees with its mirror")
			}
			if explicit.target.Flag() == "-S" {
				boundPaths = append(boundPaths, filepath.Clean(explicit.target.Value))
			}
			transport = explicit.runner
			continue
		case *explicitTmuxRunner:
			if explicit.target.Flag() == "-L" && !explicit.target.SameRoute(target) {
				return 0, errors.New("ControlSession identity plan route disagrees with its mirror")
			}
			if explicit.target.Flag() == "-S" {
				boundPaths = append(boundPaths, filepath.Clean(explicit.target.Value))
			}
			transport = explicit.runner
			continue
		}
		break
	}
	route, err := resolveExistingRuntimeMutationRoute(ctx, transport, target, nil)
	if err != nil || route.authority == nil || route.authority.Class != runtimeMutationRouteApp {
		return 0, fmt.Errorf("ControlSession identity plan: bind exact app route: %w", err)
	}
	target = route.target
	expectedSocket := route.expectedSocketPath
	for _, boundPath := range boundPaths {
		if !filepath.IsAbs(boundPath) || boundPath != expectedSocket {
			return 0, errors.New("ControlSession identity plan physical route disagrees with its mirror")
		}
	}
	routed := explicitTmuxRunner{runner: transport, target: tmuxTransport{Kind: tmuxSocketPath, Value: expectedSocket, Source: tmuxSocketPathSource}}
	sessionOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "-F", "#{session_id}")
	if err != nil || exactTmuxHandle(strings.TrimSpace(string(sessionOut)), "$") == "" {
		return 0, errors.New("ControlSession identity plan: exact session handle is unavailable")
	}
	sessionID := strings.TrimSpace(string(sessionOut))
	socket := target.Flag() + "=" + target.Value
	var steps []runtimeMutationStep
	logicalWrites := 0
	appendWrite := func(verb runtimeMutationVerb, stable runtimeMutationTarget, expect string, operands []string,
		observeStable func(context.Context) (bool, error), guard func(context.Context) error) {
		action := newRuntimeMutation(len(steps)+1, verb, stable)
		bindRuntimeMutationGuard(&action, expect)
		action.Operands = slices.Clone(operands)
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return guardPrintedRuntimeMutationRoute(ctx, transport, route, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				currentSocket, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
				if err != nil || strings.TrimSpace(string(currentSocket)) != action.Target.PhysicalSocket {
					return false, errors.New("ControlSession identity plan socket drifted while reobserving effect")
				}
				if err := guardPrintedRuntimeMutationRoute(ctx, transport, route, action); err != nil {
					return false, err
				}
				stable, err := observeStable(ctx)
				if err != nil || !stable {
					return false, err
				}
				if len(action.Operands) < 2 {
					return false, errors.New("ControlSession identity effect operands are incomplete")
				}
				field := action.Operands[len(action.Operands)-2]
				want := action.Operands[len(action.Operands)-1]
				format := "#{" + field + "}"
				if action.Verb == mutationRenameWindow {
					format = "#{window_name}"
				}
				out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", action.Target.ID, "-F", format)
				return err == nil && controllerRuntimeMutationEffectValue(field, string(out)) == controllerRuntimeMutationEffectValue(field, want), err
			},
			Guard: func(ctx context.Context) error {
				currentSocket, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
				if err != nil || strings.TrimSpace(string(currentSocket)) != expectedSocket {
					return errors.New("ControlSession identity plan socket drifted before write")
				}
				if err := guardPrintedRuntimeMutationRoute(ctx, transport, route, action); err != nil {
					return err
				}
				return guard(ctx)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, transport, action)
				return err
			},
		})
	}
	if ensureRole {
		stable := runtimeMutationTarget{Socket: socket, PhysicalSocket: expectedSocket, RouteAuthority: route.authority.printable(), Kind: "control-session", ID: sessionID, UID: binding.ControlSession.Metadata.UID}
		observeStable := func(ctx context.Context) (bool, error) {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", sessionID, "-F",
				tmuxRowFormat("#{session_id}", "#{"+tmuxopts.SessionRole+"}", "#{"+tmuxopts.ProjectUIDSession+"}"))
			rows := splitTmuxRows(string(out), 3)
			if err != nil || len(rows) != 1 || rows[0][0] != sessionID || rows[0][2] != "" {
				return false, errors.New("ControlSession role target containment is unavailable")
			}
			if rows[0][1] != "" && rows[0][1] != resourcegraph.ControlSessionRole {
				return false, errors.New("ControlSession role target is foreign")
			}
			return rows[0][1] == resourcegraph.ControlSessionRole, nil
		}
		appendWrite(mutationWriteIdentity, stable, "exact app-owned ControlSession role is blank and session containment is stable",
			[]string{"-t", sessionID, "-q", tmuxopts.SessionRole, resourcegraph.ControlSessionRole}, observeStable, func(ctx context.Context) error {
				out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", sessionID, "-F",
					tmuxRowFormat("#{session_id}", "#{"+tmuxopts.SessionRole+"}", "#{"+tmuxopts.ProjectUIDSession+"}"))
				rows := splitTmuxRows(string(out), 3)
				if err != nil || len(rows) != 1 || rows[0][0] != sessionID || (rows[0][1] != "" && rows[0][1] != resourcegraph.ControlSessionRole) || rows[0][2] != "" {
					return errors.New("ControlSession role or mutually exclusive Project identity drifted")
				}
				return nil
			})
		logicalWrites++
	}
	for _, bound := range binding.Windows {
		if bound.Origin == coremetadata.ImportRebound {
			continue
		}
		if bound.SourceIndex < 0 || bound.SourceIndex >= len(targets.Windows) {
			continue
		}
		window, ok := registry.Window(bound.UID)
		if !ok {
			continue
		}
		windowID := targets.Windows[bound.SourceIndex]
		stable := runtimeMutationTarget{Socket: socket, PhysicalSocket: expectedSocket, RouteAuthority: route.authority.printable(), Kind: "window", ID: windowID, UID: window.Metadata.UID, Parent: sessionID}
		initialOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", windowID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
		initialRows := splitTmuxRows(string(initialOut), 3)
		if err != nil || len(initialRows) != 1 || initialRows[0][0] != sessionID || initialRows[0][1] != windowID {
			return 0, errors.New("ControlSession Window containment is unavailable before planning")
		}
		initialWindowUID := initialRows[0][2]
		if initialWindowUID != "" && initialWindowUID != window.Metadata.UID {
			return 0, errors.New("ControlSession Window carries a foreign UID")
		}
		guard := func(ctx context.Context) error {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", windowID, "-F",
				tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
			rows := splitTmuxRows(string(out), 3)
			if err != nil || len(rows) != 1 || rows[0][0] != sessionID || rows[0][1] != windowID || rows[0][2] != initialWindowUID {
				return errors.New("ControlSession Window containment or UID drifted")
			}
			return nil
		}
		observeStable := func(ctx context.Context) (bool, error) {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", windowID, "-F",
				tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
			rows := splitTmuxRows(string(out), 3)
			if err != nil || len(rows) != 1 || rows[0][0] != sessionID || rows[0][1] != windowID {
				return false, errors.New("ControlSession Window effect containment is unavailable")
			}
			if rows[0][2] != "" && rows[0][2] != window.Metadata.UID {
				return false, errors.New("ControlSession Window effect target carries a foreign UID")
			}
			return rows[0][2] == window.Metadata.UID, nil
		}
		appendWrite(mutationWriteIdentity, stable, "exact ControlSession Window containment before automatic-rename projection", []string{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"}, observeStable, guard)
		appendWrite(mutationWriteIdentity, stable, "exact ControlSession Window containment before UID projection", []string{"-w", "-t", windowID, "-q", tmuxopts.WindowUID, window.Metadata.UID}, observeStable, guard)
		appendWrite(mutationWriteIdentity, stable, "exact ControlSession Window containment before stable-name projection", []string{"-w", "-t", windowID, "-q", tmuxopts.WindowName, window.Metadata.Name}, observeStable, guard)
		logicalWrites++
	}
	for _, bound := range binding.Panes {
		if bound.Origin == coremetadata.ImportRebound {
			continue
		}
		if bound.WindowIndex < 0 || bound.WindowIndex >= len(targets.Panes) {
			continue
		}
		row := targets.Panes[bound.WindowIndex]
		if bound.PaneIndex < 0 || bound.PaneIndex >= len(row) {
			continue
		}
		pane, ok := registry.Pane(bound.UID)
		if !ok {
			continue
		}
		paneID := row[bound.PaneIndex]
		windowUID, ok := paneWindowOwnerUID(registry, *pane)
		if !ok {
			continue
		}
		if _, ok := registry.Window(windowUID); !ok {
			continue
		}
		windowID := ""
		for _, candidate := range binding.Windows {
			if candidate.UID == windowUID && candidate.SourceIndex >= 0 && candidate.SourceIndex < len(targets.Windows) {
				windowID = targets.Windows[candidate.SourceIndex]
				break
			}
		}
		stable := runtimeMutationTarget{Socket: socket, PhysicalSocket: expectedSocket, RouteAuthority: route.authority.printable(), Kind: "pane", ID: paneID, UID: pane.Metadata.UID, Parent: sessionID + "/" + windowID}
		initialOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
		initialRows := splitTmuxRows(string(initialOut), 4)
		if err != nil || len(initialRows) != 1 || initialRows[0][0] != sessionID || initialRows[0][1] != windowID || initialRows[0][2] != paneID {
			return 0, errors.New("ControlSession Pane containment is unavailable before planning")
		}
		initialPaneUID := initialRows[0][3]
		if initialPaneUID != "" && initialPaneUID != pane.Metadata.UID {
			return 0, errors.New("ControlSession Pane carries a foreign UID")
		}
		guard := func(ctx context.Context) error {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
				tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
			rows := splitTmuxRows(string(out), 4)
			if err != nil || len(rows) != 1 || rows[0][0] != sessionID || rows[0][1] != windowID || rows[0][2] != paneID || rows[0][3] != initialPaneUID {
				return errors.New("ControlSession Pane containment or UID drifted")
			}
			return nil
		}
		observeStable := func(ctx context.Context) (bool, error) {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
				tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
			rows := splitTmuxRows(string(out), 4)
			if err != nil || len(rows) != 1 || rows[0][0] != sessionID || rows[0][1] != windowID || rows[0][2] != paneID {
				return false, errors.New("ControlSession Pane effect containment is unavailable")
			}
			if rows[0][3] != "" && rows[0][3] != pane.Metadata.UID {
				return false, errors.New("ControlSession Pane effect target carries a foreign UID")
			}
			return rows[0][3] == pane.Metadata.UID, nil
		}
		appendWrite(mutationWriteIdentity, stable, "exact ControlSession Pane containment before UID projection", []string{"-p", "-t", paneID, "-q", tmuxopts.PaneUID, pane.Metadata.UID}, observeStable, guard)
		appendWrite(mutationWriteIdentity, stable, "exact ControlSession Pane containment before stable-name projection", []string{"-p", "-t", paneID, "-q", tmuxopts.PaneName, pane.Metadata.Name}, observeStable, guard)
		logicalWrites++
	}
	if len(steps) == 0 {
		return 0, nil
	}
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		return 0, err
	}
	return logicalWrites, nil
}

// controlSessionWarning renders the one-line diagnostic a failed convergence
// prints. The lifecycle entry continues either way: `projmux shell` owes the
// operator a shell, and an unmarked control session degrades to exactly the
// pre-Phase-0 behavior rather than to a broken terminal.
func controlSessionWarning(sessionName string, err error) string {
	return fmt.Sprintf("warning: converge control session %q: %v\n", sessionName, err)
}
