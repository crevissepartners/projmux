package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// canonicalIntentScope is the typed identity carried from one UI origin into
// canonical create. It is deliberately not a path-shaped scope: rootKind and
// rootUID are the owner identity, while cwd is only launch workspace data.
type canonicalIntentScope struct {
	producer     canonicalCreateProducer
	rootKind     coremetadata.Kind
	rootUID      string
	rootName     string
	windowUID    string
	paneUID      string
	anchorPaneID string
	sessionName  string
	// sessionID is the stable runtime handle ($N) observed from anchorPaneID.
	// It routes the create-operation lease only; root authority still comes
	// exclusively from the Registry owner chain and the ControlSession markers.
	sessionID string
	cwd       string
	lookup    activeTargetLookup
}

// windowCreateIntent is the generated Window-create surface's complete input.
// The exact anchor Pane supplies runtime containment; owner UID and root kind
// are resolved from its mirrored Registry chain before allocation.
type windowCreateIntent struct {
	anchorPaneID string
	targetClient string
}

// createdWindowRuntime is the exact runtime placement of the Window a committed
// intent create made: the stable `$N` Session and `@N` Window handles the
// transaction bound into the Registry. It carries no identity authority; the
// generated route only uses it to address the pressing client's move.
type createdWindowRuntime struct {
	sessionID string
	windowID  string
}

// windowRenameIntent and paneRenameIntent are the generated rename surfaces'
// complete input: the exact anchor Pane the key or menu item targeted, the
// client that sees the result, and the raw prompt response. The response is
// deliberately unvalidated here; generatedRenameName owns the input rules.
type windowRenameIntent struct {
	anchorPaneID string
	targetClient string
	response     string
}

type paneRenameIntent struct {
	anchorPaneID string
	targetClient string
	response     string
}

func (c *createCommand) projectCanonicalOriginWindowBinding(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	scope canonicalIntentScope,
) error {
	owner, found, err := c.runtime.windowRuntimeForUID(ctx, scope.sessionName, scope.windowUID)
	if err != nil {
		return err
	}
	if !found || owner.SessionID != scope.sessionID {
		return usageError("canonical create: origin Window has no single current stable-UID runtime containment; nothing was created")
	}
	_, err = c.observeWindowRuntimeBinding(mutator, working, scope.windowUID, owner.SessionID, owner.WindowID)
	return err
}

func (c *createCommand) createWindowFromIntent(intent windowCreateIntent, stdout, stderr io.Writer) (createdWindowRuntime, error) {
	anchor := strings.TrimSpace(intent.anchorPaneID)
	if exactTmuxHandle(anchor, "%") == "" {
		return createdWindowRuntime{}, usageError("canonical Window create intent requires an exact anchor Pane; nothing was created")
	}
	scope, err := c.resolveCanonicalIntentScope(agentPaneIntent{
		producer: canonicalProducerWindowCreate, anchorPaneID: anchor, targetClient: intent.targetClient,
	})
	if err != nil {
		return createdWindowRuntime{}, visibleCanonicalCreateError(err)
	}
	var result createResult
	var placement createdWindowRuntime
	err = c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		if err := c.projectCanonicalOriginWindowBinding(ctx, working, mutator, scope); err != nil {
			return err
		}
		cwd := scope.cwd
		if scope.rootKind == coremetadata.KindControlSession {
			var cwdErr error
			cwd, cwdErr = canonicalExistingDir(cwd)
			if cwdErr != nil {
				return usageError(fmt.Sprintf("canonical create: ControlSession Window cwd %q: %v", scope.cwd, cwdErr))
			}
		}
		window, panes, addErr := mutator.AddWindowToManagedRoot(working, scope.rootKind, scope.rootUID,
			coremetadata.BootstrapWindow{Panes: []coremetadata.BootstrapPane{{CWD: cwd}}}, c.shell, cwd, operationID)
		if addErr != nil {
			return MapMetadataError(addErr)
		}
		activation, activationErr := c.issuePaneActivation(working, mutator, panes[0].Metadata.UID, "", operationID)
		if activationErr != nil {
			return activationErr
		}
		if err := c.runtime.markCreateOperation(ctx, scope.sessionID, ledger); err != nil {
			return err
		}
		created, createErr := c.runtime.newWindow(ctx, scope.sessionID, window.Metadata.Name, cwd,
			c.runtime.supervisedLaunch(ctx, activation, nil))
		if created.WindowID == "" {
			return createErr
		}
		if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, window.Metadata.UID, ledger); claimErr != nil {
			return errors.Join(createErr, claimErr)
		}
		if mirrorErr := c.runtime.mirrorWindow(ctx, created.WindowID, window); mirrorErr != nil {
			return errors.Join(createErr, mirrorErr)
		}
		projected, bindingErr := c.observeWindowRuntimeBinding(
			mutator, working, window.Metadata.UID, scope.sessionID, created.WindowID,
		)
		if bindingErr != nil {
			return errors.Join(createErr, bindingErr)
		}
		window = projected
		if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, panes[0].Metadata.UID, ledger); claimErr != nil {
			return errors.Join(createErr, claimErr)
		}
		if mirrorErr := c.runtime.mirrorPane(ctx, created.PaneID, panes[0]); mirrorErr != nil {
			return errors.Join(createErr, mirrorErr)
		}
		observeActivationRuntime(working, mutator, activation, created.PaneID, c.runtime.warn)
		result = createResult{kind: coremetadata.KindWindow, uid: window.Metadata.UID, name: window.Metadata.Name,
			paneID: created.PaneID, projectName: scope.rootName, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}
		placement = createdWindowRuntime{sessionID: scope.sessionID, windowID: created.WindowID}
		return createErr
	}, c.canonicalIntentGuards(scope)...)
	if err != nil {
		return createdWindowRuntime{}, visibleCanonicalCreateError(err)
	}
	return placement, c.writeResults(stdout, canonicalCreateWindow, cli.OutputModeDefault, coremetadata.KindWindow, []createResult{result})
}

// renameWindowFromIntent is the generated Window rename (the catalog key and
// the Window menu Rename item). The Registry name is written by the same
// commitRename that owns public `rename window`, which also converges the
// `@projmux_window_name` mirror; only after that commit does this route rename
// the tmux display `window_name`, through the guarded exact-containment
// mutation, so the three agree live and a Continue that rebuilds the session
// from the Registry restores the new name. A refusal before the commit writes
// nothing to the Registry or tmux.
func (c *createCommand) renameWindowFromIntent(intent windowRenameIntent, renamer *renameCommand, stdout, stderr io.Writer) error {
	name, scope, err := c.commitRenameFromIntent(coremetadata.KindWindow, canonicalProducerWindowRename,
		intent.anchorPaneID, intent.targetClient, intent.response, renamer)
	if err != nil {
		return err
	}
	displayName := name
	err = c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, _ string, _ *runtimeLedger) error {
		_, ok := working.Window(scope.windowUID)
		if !ok {
			return usageError("canonical rename: origin Window disappeared; nothing was changed")
		}
		row, readErr := c.runtime.read(ctx, "display-message", "-p", "-t", scope.anchorPaneID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
		if readErr != nil {
			return readErr
		}
		rows := splitTmuxRows(row, 3)
		if len(rows) != 1 || rows[0][0] != scope.sessionID || exactTmuxHandle(rows[0][1], "@") == "" || rows[0][2] != scope.windowUID {
			return usageError("canonical rename: exact Window containment changed before planning; nothing was changed")
		}
		windowID := rows[0][1]
		stableTarget, bindErr := c.runtime.bindMaterializeIdentityTarget(ctx, "window", windowID, scope.windowUID)
		if bindErr != nil {
			return bindErr
		}
		action := materializeMutationAction(mutationRenameWindow,
			stableTarget,
			"exact root="+string(scope.rootKind)+"/"+scope.rootUID+";session="+scope.sessionID+";window="+windowID+"/"+scope.windowUID,
			"exact owned Window display name="+displayName,
			"-t", windowID, "--", displayName)
		observeContainment := func(ctx context.Context) (bool, error) {
			observed, err := c.runtime.read(ctx, "display-message", "-p", "-t", scope.anchorPaneID, "-F",
				tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
			if err != nil {
				return false, err
			}
			return observed == row, nil
		}
		if err := c.runtime.runMaterializeMutation(ctx, action, func() error {
			observed, err := observeContainment(ctx)
			if err != nil || !observed {
				return errors.New("exact Window containment drifted before rename")
			}
			return nil
		}, func() error {
			_, err := runRuntimeMutationCommand(ctx, c.runtime.runner, action)
			return err
		}, observeContainment); err != nil {
			return err
		}
		return nil
	}, c.canonicalIntentGuards(scope)...)
	if err != nil {
		return visibleCanonicalCreateError(fmt.Errorf(
			"rename window %q committed Registry name %q but its tmux display name did not change: %w", scope.windowUID, name, err))
	}
	_, err = fmt.Fprintf(stdout, "renamed: window/%s -> %s\n", scope.windowUID, displayName)
	return err
}

// renamePaneFromIntent is the generated Pane rename key. It resolves the exact
// anchor Pane to its Registry Pane UID with the same owner-chain and runtime
// session proof as the Window intent, then commits through commitRename, which
// writes the Registry name and its `@projmux_pane_label` mirror together. A Pane
// whose name a launcher chose, an Agent's Pane included, renames like any other.
func (c *createCommand) renamePaneFromIntent(intent paneRenameIntent, renamer *renameCommand, stdout, stderr io.Writer) error {
	name, scope, err := c.commitRenameFromIntent(coremetadata.KindPane, canonicalProducerPaneRename,
		intent.anchorPaneID, intent.targetClient, intent.response, renamer)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "renamed: pane/%s -> %s\n", scope.paneUID, name)
	return err
}

// commitRenameFromIntent is the shared front half of both generated rename
// routes: input rules, exact origin resolution, then the one Registry rename
// owner with the origin re-proved under its lock. It adds no rename logic of its
// own. An anchor that resolves to no exact managed Registry identity is refused
// here with zero writes.
func (c *createCommand) commitRenameFromIntent(kind coremetadata.Kind, producer canonicalCreateProducer, anchorPaneID, targetClient, response string, renamer *renameCommand) (string, canonicalIntentScope, error) {
	label := strings.ToLower(string(kind))
	anchor := exactTmuxHandle(strings.TrimSpace(anchorPaneID), "%")
	if anchor == "" {
		return "", canonicalIntentScope{}, usageError(fmt.Sprintf("canonical %s rename intent requires an exact anchor Pane; nothing was changed", label))
	}
	name, requested, err := generatedRenameName(response)
	if err != nil {
		return "", canonicalIntentScope{}, err
	}
	if !requested {
		return "", canonicalIntentScope{}, usageError(fmt.Sprintf("canonical %s rename intent requires a non-blank name; nothing was changed", label))
	}
	if renamer == nil {
		return "", canonicalIntentScope{}, fmt.Errorf("canonical %s rename: the Registry rename route is not configured; nothing was changed", label)
	}
	scope, err := c.resolveCanonicalIntentScope(agentPaneIntent{producer: producer, anchorPaneID: anchor, targetClient: targetClient})
	if err != nil {
		return "", canonicalIntentScope{}, visibleCanonicalCreateError(err)
	}
	uid := scope.windowUID
	if kind == coremetadata.KindPane {
		uid = scope.paneUID
	}
	// A key press or menu item always runs inside the runtime it renames, so
	// commitRename's no-route display notice cannot arise here; the tab is
	// converged by that route and, for a Window, again by this one.
	committed, _, err := renamer.commitRename(context.Background(), kind, uid, name, c.canonicalIntentRenameGuard(scope))
	if err != nil {
		if !committed && !strings.Contains(err.Error(), "nothing was") {
			err = fmt.Errorf("%w; nothing was changed", err)
		}
		return "", canonicalIntentScope{}, visibleCanonicalCreateError(err)
	}
	return name, scope, nil
}

// canonicalIntentRenameGuard runs the canonical intent guards a create runs
// before it writes against the working Registry of one rename. The origin
// Pane, its owner chain, its runtime session and, for a ControlSession, the
// root's identity markers must still be the ones resolved at key press.
func (c *createCommand) canonicalIntentRenameGuard(scope canonicalIntentScope) renameOriginGuard {
	guards := c.canonicalIntentGuards(scope)
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator) error {
		for _, guard := range guards {
			if _, err := guard(ctx, working, mutator, ""); err != nil {
				return err
			}
		}
		return nil
	}
}

// resolveCanonicalIntentScope resolves the exact mirrored origin chain before
// the Registry transaction opens. Unlike public create's omitted scope, this
// intent-only resolver accepts both Registry root kinds. It requires the Pane
// mirror as well as the Window mirror: a UI origin is an exact Pane, so falling
// back to the stored compatibility shell ref would silently move the requested split.
func (c *createCommand) resolveCanonicalIntentScope(intent agentPaneIntent) (canonicalIntentScope, error) {
	if c == nil || c.store == nil || c.store.load == nil {
		return canonicalIntentScope{}, errors.New("canonical create: the resource-backed create route is not configured")
	}
	lookup := c.activeTarget
	if anchor := strings.TrimSpace(intent.anchorPaneID); anchor != "" {
		if c.anchorTarget == nil {
			return canonicalIntentScope{}, errors.New("canonical create: exact Pane origin lookup is not configured")
		}
		lookup = c.anchorTarget(anchor)
	}
	if lookup == nil {
		return canonicalIntentScope{}, usageError("canonical create: the UI origin has no exact tmux Pane; nothing was created")
	}
	observer, inside := lookup()
	if !inside || strings.TrimSpace(observer.paneID) == "" {
		return canonicalIntentScope{}, usageError("canonical create: the UI origin has no exact tmux Pane on the inherited server; nothing was created")
	}
	registry, err := c.store.load()
	if err != nil {
		return canonicalIntentScope{}, MapMetadataError(err)
	}
	scope, err := resolveCanonicalIntentObserver(intent.producer, lookup, observer, registry)
	if err != nil {
		return canonicalIntentScope{}, err
	}
	if scope.rootKind == coremetadata.KindProject && scope.sessionName == "" {
		if c.sessionNameFor == nil {
			return canonicalIntentScope{}, errors.New("canonical create: Project session naming is not configured")
		}
		scope.sessionName = c.sessionNameFor(scope.cwd)
	}
	c.routeAnchor = scope.anchorPaneID
	if err := c.ensureRuntimeRoute(context.Background()); err != nil {
		return canonicalIntentScope{}, fmt.Errorf("canonical create: bind exact runtime route: %w", err)
	}
	runtimeSession, err := c.canonicalIntentRuntimeSession(context.Background(), scope.anchorPaneID)
	if err != nil {
		return canonicalIntentScope{}, err
	}
	if runtimeSession.Name != scope.sessionName {
		return canonicalIntentScope{}, usageError(fmt.Sprintf(
			"canonical create: origin Pane %s is in tmux session %q, not declared session %q; nothing was created",
			scope.anchorPaneID, runtimeSession.Name, scope.sessionName))
	}
	scope.sessionID = runtimeSession.ID
	return scope, nil
}

func (c *createCommand) canonicalIntentRuntimeSession(ctx context.Context, paneID string) (liveSessionIdentity, error) {
	if c == nil || c.runtime == nil {
		return liveSessionIdentity{}, errors.New("canonical create: runtime materializer is not configured")
	}
	out, err := c.runtime.read(ctx, "display-message", "-p", "-t", paneID, "-F",
		tmuxRowFormat("#{session_id}", "#{session_name}"))
	if err != nil {
		return liveSessionIdentity{}, usageError(fmt.Sprintf(
			"canonical create: read exact runtime session for origin Pane %s: %v; nothing was created", paneID, err))
	}
	rows, err := strictTmuxRows(out, 2)
	if err != nil || len(rows) != 1 {
		return liveSessionIdentity{}, usageError(fmt.Sprintf(
			"canonical create: origin Pane %s returned no single exact runtime session; nothing was created", paneID))
	}
	identity := liveSessionIdentity{ID: strings.TrimSpace(rows[0][0]), Name: strings.TrimSpace(rows[0][1])}
	if exactTmuxHandle(identity.ID, "$") == "" || identity.Name == "" {
		return liveSessionIdentity{}, usageError(fmt.Sprintf(
			"canonical create: origin Pane %s returned an invalid runtime session identity; nothing was created", paneID))
	}
	return identity, nil
}

func resolveCanonicalIntentObserver(producer canonicalCreateProducer, lookup activeTargetLookup, observer activeTargetObserver, registry coremetadata.Registry) (canonicalIntentScope, error) {
	pane, detail := observer.activePane(registry)
	if pane == nil {
		return canonicalIntentScope{}, usageError("canonical create: " + detail + "; the exact UI origin was lost, so nothing was created")
	}
	window, detail := observer.activeWindow(registry)
	if window == nil {
		return canonicalIntentScope{}, usageError("canonical create: " + detail + "; the exact UI origin was lost, so nothing was created")
	}
	ownerWindowUID, ok := paneWindowOwnerUID(registry, *pane)
	if !ok || ownerWindowUID != window.Metadata.UID {
		return canonicalIntentScope{}, usageError(fmt.Sprintf(
			"canonical create: the exact UI origin pane/%s does not belong to mirrored window/%s; identity evidence conflicts, so nothing was created",
			pane.Metadata.Name, window.Metadata.Name))
	}
	owner := window.Metadata.OwnerRef
	if owner == nil {
		return canonicalIntentScope{}, usageError(fmt.Sprintf(
			"canonical create: mirrored window/%s has no Registry root owner; identity evidence conflicts, so nothing was created", window.Metadata.Name))
	}
	scope := canonicalIntentScope{
		producer:     producer,
		rootKind:     owner.Kind,
		rootUID:      owner.UID,
		windowUID:    window.Metadata.UID,
		paneUID:      pane.Metadata.UID,
		anchorPaneID: observer.paneID,
		cwd:          pane.Spec.CWD,
		lookup:       lookup,
	}
	switch owner.Kind {
	case coremetadata.KindProject:
		project, ok := registry.Project(owner.UID)
		if !ok {
			return canonicalIntentScope{}, usageError(fmt.Sprintf(
				"canonical create: window/%s names missing Project uid %q; identity evidence conflicts, so nothing was created",
				window.Metadata.Name, owner.UID))
		}
		scope.rootName = project.Metadata.Name
		scope.cwd = project.Spec.Root
		if project.Status.Session != nil {
			scope.sessionName = strings.TrimSpace(project.Status.Session.Name)
		}
	case coremetadata.KindControlSession:
		control, ok := registry.ControlSession(owner.UID)
		if !ok {
			return canonicalIntentScope{}, usageError(fmt.Sprintf(
				"canonical create: window/%s names missing ControlSession uid %q; identity evidence conflicts, so nothing was created",
				window.Metadata.Name, owner.UID))
		}
		scope.rootName = control.Metadata.Name
		scope.sessionName = control.Spec.Session
	default:
		return canonicalIntentScope{}, usageError(fmt.Sprintf(
			"canonical create: window/%s owner kind %q is not Project or ControlSession; identity evidence conflicts, so nothing was created",
			window.Metadata.Name, owner.Kind))
	}
	return scope, nil
}

func paneWindowOwnerUID(registry coremetadata.Registry, pane coremetadata.Pane) (string, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", false
	}
	switch owner.Kind {
	case coremetadata.KindWindow:
		_, ok := registry.Window(owner.UID)
		return owner.UID, ok
	case coremetadata.KindAgent:
		agent, ok := registry.Agent(owner.UID)
		if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
			return "", false
		}
		return agent.Metadata.OwnerRef.UID, true
	default:
		return "", false
	}
}

// canonicalOriginGuard re-proves the exact origin under the Registry lock and
// before reconciliation can write. For a ControlSession it additionally proves
// the Phase 11 app marker + control role + exact spec.session chain; neither cwd
// nor a session-name resemblance participates.
func (c *createCommand) canonicalOriginGuard(scope canonicalIntentScope) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, _ coremetadata.Mutator, _ string) (liveSessionIdentity, error) {
		observer, inside := scope.lookup()
		if !inside || observer.paneID != scope.anchorPaneID {
			return liveSessionIdentity{}, usageError("canonical create: the exact UI origin Pane was lost before commit; nothing was created")
		}
		resolved, err := resolveCanonicalIntentObserver(scope.producer, scope.lookup, observer, working)
		if err != nil {
			return liveSessionIdentity{}, err
		}
		if resolved.rootKind != scope.rootKind || resolved.rootUID != scope.rootUID ||
			resolved.windowUID != scope.windowUID || resolved.paneUID != scope.paneUID {
			return liveSessionIdentity{}, usageError("canonical create: the exact UI origin owner chain changed before commit; identity evidence conflicts, so nothing was created")
		}
		liveSession, err := c.canonicalIntentRuntimeSession(ctx, scope.anchorPaneID)
		if err != nil || liveSession.ID != scope.sessionID || liveSession.Name != scope.sessionName {
			return liveSessionIdentity{}, usageError("canonical create: the exact UI origin runtime session changed before commit; nothing was created")
		}
		if scope.rootKind == coremetadata.KindProject {
			return liveSession, nil
		}
		control, ok := working.ControlSession(scope.rootUID)
		if !ok || control.Spec.Session != scope.sessionName {
			return liveSessionIdentity{}, usageError("canonical create: the exact ControlSession declaration changed before commit; nothing was created")
		}
		markers, err := c.runtime.mirror.ObserveControlSessionMarkers(ctx, liveSession.Name)
		if err != nil {
			return liveSessionIdentity{}, fmt.Errorf("canonical create: prove ControlSession %q identity: %v", liveSession.Name, err)
		}
		if !markers.AppOwned || markers.Ephemeral || markers.Role != resourcegraph.ControlSessionRole || strings.TrimSpace(markers.ProjectUID) != "" {
			return liveSessionIdentity{}, usageError(fmt.Sprintf(
				"canonical create: session %q lacks exact ControlSession identity (app-owned=%t role=%q ephemeral=%t project-uid=%q); nothing was created",
				liveSession.Name, markers.AppOwned, markers.Role, markers.Ephemeral, markers.ProjectUID))
		}
		return liveSession, nil
	}
}

func (c *createCommand) canonicalIntentGuards(scope canonicalIntentScope) []createPreReconcile {
	guards := []createPreReconcile{c.canonicalOriginGuard(scope)}
	if scope.rootKind == coremetadata.KindProject {
		guards = append(guards, c.exactProjectOwnershipGuard(scope.rootUID))
	}
	return guards
}

func (c *createCommand) createCanonicalIntentPane(scope canonicalIntentScope, intent agentPaneIntent, launchDir string, stdout io.Writer) error {
	var result createResult
	err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		if err := c.projectCanonicalOriginWindowBinding(ctx, working, mutator, scope); err != nil {
			return err
		}
		window, ok := working.Window(scope.windowUID)
		if !ok {
			return usageError("canonical create: origin Window disappeared before allocation; nothing was created")
		}
		cwd := scope.cwd
		if scope.rootKind == coremetadata.KindControlSession {
			var err error
			cwd, err = canonicalExistingDir(cwd)
			if err != nil {
				return usageError(fmt.Sprintf("canonical create: ControlSession origin Pane launch cwd %q: %v", scope.cwd, err))
			}
		}
		// launchDir is the resolved split start directory. scope.cwd keeps
		// naming the Project root the session was derived from.
		if launchDir != "" {
			cwd = launchDir
		}
		pane, err := mutator.AddPane(working, scope.windowUID, coremetadata.BootstrapPane{CWD: cwd}, c.shell, operationID)
		if err != nil {
			return MapMetadataError(err)
		}
		activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, "", operationID)
		if err != nil {
			return err
		}
		if err := c.runtime.markCreateOperation(ctx, scope.sessionID, ledger); err != nil {
			return err
		}
		paneID, err := c.runtime.splitPane(ctx, scope.anchorPaneID, intent.placement, cwd,
			c.runtime.supervisedLaunch(ctx, activation, nil))
		if paneID != "" {
			if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.mirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, c.runtime.warn)
		}
		if err != nil {
			return err
		}
		if _, _, err := mutator.AdoptWindowDefaultShell(working, scope.windowUID, pane.Metadata.UID); err != nil {
			return MapMetadataError(err)
		}
		c.runtime.equalizeSplitLayout(ctx, scope.anchorPaneID, intent.placement)
		result = createResult{kind: coremetadata.KindPane, uid: pane.Metadata.UID, name: pane.Metadata.Name,
			paneID: paneID, projectName: scope.rootName, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}
		return nil
	}, c.canonicalIntentGuards(scope)...)
	if err != nil {
		return err
	}
	return c.writeResults(stdout, canonicalCreatePane, cli.OutputModeDefault, coremetadata.KindPane, []createResult{result})
}

func (c *createCommand) createCanonicalIntentAgent(scope canonicalIntentScope, intent agentPaneIntent, provider, launchDir string, flags resourceCreateFlags, stdout io.Writer) error {
	if c.agents == nil {
		return errors.New("create agent: the provider launcher is not configured")
	}
	nativeLauncher, nativeLaunchCapable := c.resumes.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := c.resumes.(codexNativeLifecycleStarter)
	freshNativeCreate := nativeCodexFreshCreateRequired(provider, flags)
	nativeCatalogResume := provider == aiModeCodex && strings.TrimSpace(flags.resumeConversation) != "" &&
		strings.TrimSpace(flags.resumeSource) == aisessions.SourceCodexAppServer
	rolloutResume := provider == aiModeCodex && strings.TrimSpace(flags.resumeConversation) != "" &&
		strings.TrimSpace(flags.resumeSource) == aisessions.SourceCodexRollout
	if provider == aiModeCodex && strings.TrimSpace(flags.resumeConversation) != "" && !nativeCatalogResume && !rolloutResume {
		return nativeResumePreparationRefusal(canonicalCreateAgent, &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing})
	}
	var nativeRoute codexNativeEndpointRoute
	if freshNativeCreate {
		_, exactPrompt := nativePrompt(flags.payload)
		if flags.codexCapability != nil || !exactPrompt {
			return nativeCreatePreparationRefusal(canonicalCreateAgent, &codexNativeRouteError{Reason: "unsupported-create-shape"})
		}
		if !nativeLaunchCapable || c.codexNative == nil {
			return nativeCreatePreparationRefusal(canonicalCreateAgent, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable})
		}
		nativeCtx, cancel := prepareNativeContext(context.Background())
		var routeErr error
		nativeRoute, routeErr = c.codexNative.Current(nativeCtx)
		cancel()
		if routeErr != nil || !nativeRoute.valid() || nativeRoute.State != coremetadata.CodexGenerationCurrent {
			if routeErr == nil {
				routeErr = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
			}
			return nativeCreatePreparationRefusal(canonicalCreateAgent, routeErr)
		}
	} else if nativeCatalogResume {
		if !nativeLaunchCapable || c.codexNative == nil || !flags.resumeEndpoint.Valid() {
			return nativeResumePreparationRefusal(canonicalCreateAgent, &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing})
		}
		// Draining and handover-pending rows are leftovers of the retired
		// private generation pool; they resolve like current rows, switching
		// onto the default endpoint of the same Codex state domain or refusing.
		switch flags.resumeGenerationState {
		case coremetadata.CodexGenerationCurrent, coremetadata.CodexGenerationDraining, coremetadata.CodexGenerationHandoverPending:
		default:
			return nativeResumePreparationRefusal(canonicalCreateAgent, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable})
		}
		nativeCtx, cancel := prepareNativeContext(context.Background())
		var routeErr error
		nativeRoute, routeErr = c.codexNative.Resolve(nativeCtx, flags.resumeEndpoint)
		cancel()
		if routeErr == nil && nativeRoute.valid() && !nativeRoute.Endpoint.Same(flags.resumeEndpoint) &&
			(!nativeRoute.Default || nativeRoute.Endpoint.StateDomainID != flags.resumeEndpoint.StateDomainID) {
			routeErr = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}
		if routeErr != nil || !nativeRoute.valid() || nativeRoute.State != coremetadata.CodexGenerationCurrent {
			if routeErr == nil {
				routeErr = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
			}
			return nativeResumePreparationRefusal(canonicalCreateAgent, routeErr)
		}
	}
	var result createResult
	var nativeLifecycleTarget codexLifecycleObserverTarget
	err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		if err := c.projectCanonicalOriginWindowBinding(ctx, working, mutator, scope); err != nil {
			return err
		}
		window, ok := working.Window(scope.windowUID)
		if !ok {
			return usageError("canonical create: origin Window disappeared before allocation; nothing was created")
		}
		var workspace coremetadata.AgentWorkspace
		var err error
		if scope.rootKind == coremetadata.KindProject {
			project, ok := working.Project(scope.rootUID)
			if !ok {
				return usageError("canonical create: origin Project disappeared before workspace planning; nothing was created")
			}
			resolver := c.resolveWorkspace
			if resolver == nil {
				resolver = resolveAgentWorkspace
			}
			cwd := flags.cwd
			if launchDir != "" {
				cwd = launchDir
			}
			workspace, err = resolver(*working, *project, provider, cwd, flags.addDirs)
		} else {
			workspace.CWD, err = canonicalExistingDir(scope.cwd)
			if err != nil {
				err = usageError(fmt.Sprintf("canonical create: ControlSession origin Pane launch cwd %q: %v", scope.cwd, err))
			}
		}
		if err != nil {
			return err
		}
		var title string
		var launchArgv []string
		if !freshNativeCreate && !nativeCatalogResume {
			title, launchArgv, err = c.planAgentPaneLaunch(provider, workspace, flags)
			if err != nil {
				return err
			}
		}
		agent, err := mutator.CreateAgent(working, scope.windowUID, coremetadata.CreateAgentOptions{
			Provider: provider, Workspace: workspace, Activation: coremetadata.ActivationNotRequested, OperationID: operationID,
		})
		if err != nil {
			return MapMetadataError(err)
		}
		// A resume-picker selection already carries provider-owned conversation
		// identity before the provider starts. Persist that exact normalized
		// identity now, in the same transaction that owns the Agent and Pane,
		// instead of waiting for a hook that may arrive only after the Pane has
		// stopped. Ordinary fresh creates leave the pointer nil.
		if conversation := strings.TrimSpace(flags.resumeConversation); conversation != "" {
			observation := pickerResumeSessionObservation(provider, conversation)
			if nativeCatalogResume {
				// The resolved route, not the row's endpoint: a switched row
				// binds the new Agent to the default endpoint.
				endpoint := nativeRoute.Endpoint
				observation.Endpoint = &endpoint
			}
			if _, _, err := mutator.RecordAgentSessionRef(working, agent.Metadata.UID, observation); err != nil {
				return MapMetadataError(err)
			}
			if nativeCatalogResume {
				storedAgent, _ := working.Agent(agent.Metadata.UID)
				storedAgent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationCurrent}
			}
		}
		pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{CWD: workspace.CWD}, operationID)
		if err != nil {
			return MapMetadataError(err)
		}
		activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
		if err != nil {
			return err
		}
		usedNative := false
		nativeThreadID := ""
		bindFlags := flags
		if freshNativeCreate {
			if err := mutator.StageCodexEndpoint(working, agent.Metadata.UID, nativeRoute.Endpoint); err != nil {
				return MapMetadataError(err)
			}
			prompt, _ := nativePrompt(flags.payload)
			nativeCtx, cancel := prepareNativeContext(ctx)
			prepared, nativeErr := c.codexNative.Create(nativeCtx, nativeRoute, workspace, prompt, activation.Generation)
			cancel()
			switch {
			case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) != "":
				title, launchArgv, err = nativeLauncher.PlanNativeCodexResume(nativeRoute, workspace, prepared.ThreadID)
				if err != nil {
					return nativeLaunchError(canonicalCreateAgent, err)
				}
				if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
					AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID,
					Generation: activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID,
					Endpoint: nativeRoute.Endpoint,
				}); err != nil {
					return MapMetadataError(err)
				}
				nativeThreadID = prepared.ThreadID
				usedNative = true
			case nativeErr == nil:
				return nativeLaunchError(canonicalCreateAgent, fmt.Errorf("%w: native create returned an empty thread", codexappserver.ErrProtocol))
			case nativeFallbackAllowed(c.codexNative, nativeErr), nativeRootsUnsupported(nativeErr):
				return nativeCreatePreparationRefusal(canonicalCreateAgent, nativeErr)
			default:
				return nativeLaunchError(canonicalCreateAgent, nativeErr)
			}
		} else if nativeCatalogResume {
			nativeCtx, cancel := prepareNativeContext(ctx)
			prepared, nativeErr := c.codexNative.Resume(nativeCtx, nativeRoute, workspace, flags.resumeConversation)
			cancel()
			switch {
			case nativeErr == nil:
				if strings.TrimSpace(prepared.ThreadID) != strings.TrimSpace(flags.resumeConversation) {
					return nativeLaunchError(canonicalCreateAgent, fmt.Errorf("%w: native resume returned a different thread", codexappserver.ErrProtocol))
				}
				title, launchArgv, err = nativeLauncher.PlanNativeCodexResume(nativeRoute, workspace, prepared.ThreadID)
				if err != nil {
					return nativeLaunchError(canonicalCreateAgent, err)
				}
				if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
					AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID,
					Generation: activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID,
					Endpoint: nativeRoute.Endpoint,
				}); err != nil {
					return MapMetadataError(err)
				}
				nativeThreadID = prepared.ThreadID
				usedNative = true
			case nativeFallbackAllowed(c.codexNative, nativeErr):
				// The picker row names a thread the app-server owns. Rebinding it
				// onto the rollout CLI lane looks like a resume but carries no
				// native turn control, so the refusal is typed instead. There is
				// no `--interactive-only` escape hatch on a resume: the operator
				// picked an existing conversation, not a launch mode.
				return nativeResumePreparationRefusal(canonicalCreateAgent, nativeErr)
			default:
				return nativeLaunchError(canonicalCreateAgent, nativeErr)
			}
		}
		if err := c.runtime.markCreateOperation(ctx, scope.sessionID, ledger); err != nil {
			return err
		}
		paneID, err := c.runtime.splitPane(ctx, scope.anchorPaneID, intent.placement, workspace.CWD,
			c.runtime.supervisedLaunch(ctx, activation, launchArgv))
		if paneID != "" {
			if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.mirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, c.runtime.warn)
		}
		if err != nil {
			return err
		}
		c.runtime.equalizeSplitLayout(ctx, scope.anchorPaneID, intent.placement)
		if usedNative {
			if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, c.runtime.runner, paneID, workspace.CWD, title, "", nativeThreadID); err != nil {
				return tmuxError("%s: bind native Codex Pane %s presentation metadata: %v", canonicalCreateAgent, paneID, err)
			}
			if nativeLifecycleCapable {
				nativeLifecycleTarget = codexLifecycleObserverTarget{
					Identity: codexLifecycleIdentity{
						AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, RuntimeID: paneID,
						Generation: activation.Generation, ThreadID: nativeThreadID,
					},
					Route: c.runtime.target, NativeRoute: nativeRoute,
				}
			}
		} else if err := c.bindAgentPane(ctx, paneID, provider, workspace.CWD, title,
			declaredPlainCodexLane(provider, bindFlags, ""), bindFlags); err != nil {
			return tmuxError("%s: bind Agent Pane %s presentation metadata: %v", canonicalCreateAgent, paneID, err)
		}
		if err := c.runtime.runIdentityWrites(ctx, "pane", paneID, pane.Metadata.UID, []identityPlanWrite{
			{operands: []string{"-p", "-u", "-t", paneID, aiPaneTopicOption}, effect: "legacy AI topic projection absent"},
			{operands: []string{"-p", "-u", "-t", paneID, aiPaneTopicManualOption}, effect: "legacy manual-topic projection absent"},
		}); err != nil {
			return tmuxError("%s: clear compatibility topic projections on Pane %s: %v", canonicalCreateAgent, paneID, err)
		}
		result = createResult{kind: coremetadata.KindAgent, uid: agent.Metadata.UID, name: agent.Metadata.Name,
			paneID: paneID, projectName: scope.rootName, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}
		return nil
	}, c.canonicalIntentGuards(scope)...)
	if err != nil {
		return err
	}
	if nativeLifecycleTarget.valid() {
		nativeLifecycle.startNativeCodexLifecycleObserver(nativeLifecycleTarget)
	}
	return c.writeResults(stdout, canonicalCreateAgent, cli.OutputModeDefault, coremetadata.KindAgent, []createResult{result})
}

// pickerResumeSessionObservation projects the provider-discriminated picker
// identity onto the existing durable sessionRef input shape. The picker owns
// only the exact resume id: it never reads or persists transcripts, caches, or
// provider-private turn identity.
func pickerResumeSessionObservation(provider, conversation string) coremetadata.AgentSessionObservation {
	observation := coremetadata.AgentSessionObservation{Provider: provider}
	switch provider {
	case aiModeCodex, aiModeAntigravity:
		observation.ThreadID = conversation
	default:
		observation.SessionID = conversation
	}
	return observation
}
