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

// resolveCanonicalIntentScope resolves the exact mirrored origin chain before
// the Registry transaction opens. Unlike public create's omitted scope, this
// intent-only resolver accepts both Registry root kinds. It requires the Pane
// mirror as well as the Window mirror: a UI origin is an exact Pane, so falling
// back to spec.primaryPaneRef would silently move the requested split.
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

func (c *createCommand) createCanonicalIntentPane(scope canonicalIntentScope, intent agentPaneIntent, stdout io.Writer) error {
	var result createResult
	err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
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
			if mirrorErr := c.runtime.mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, c.runtime.warn)
		}
		if err != nil {
			return err
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

func (c *createCommand) createCanonicalIntentAgent(scope canonicalIntentScope, intent agentPaneIntent, provider string, flags resourceCreateFlags, stdout io.Writer) error {
	if c.agents == nil {
		return errors.New("create agent: the provider launcher is not configured")
	}
	var result createResult
	err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
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
			workspace, err = resolver(*working, *project, provider, flags.cwd, flags.addDirs)
		} else {
			workspace.CWD, err = canonicalExistingDir(scope.cwd)
			if err != nil {
				err = usageError(fmt.Sprintf("canonical create: ControlSession origin Pane launch cwd %q: %v", scope.cwd, err))
			}
		}
		if err != nil {
			return err
		}
		title, launchArgv, err := c.planAgentPaneLaunch(provider, workspace, flags)
		if err != nil {
			return err
		}
		agent, err := mutator.CreateAgent(working, scope.windowUID, coremetadata.CreateAgentOptions{
			Provider: provider, Workspace: workspace, Activation: coremetadata.ActivationNotRequested, OperationID: operationID,
		})
		if err != nil {
			return MapMetadataError(err)
		}
		pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{CWD: workspace.CWD}, operationID)
		if err != nil {
			return MapMetadataError(err)
		}
		activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
		if err != nil {
			return err
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
			if mirrorErr := c.runtime.mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, c.runtime.warn)
		}
		if err != nil {
			return err
		}
		c.runtime.equalizeSplitLayout(ctx, scope.anchorPaneID, intent.placement)
		c.bindAgentPane(paneID, provider, workspace.CWD, title, flags)
		for _, option := range []string{aiPaneTopicOption, aiPaneTopicManualOption} {
			if _, err := c.runtime.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", paneID, option); err != nil {
				return tmuxError("%s: clear compatibility topic projection %s on Pane %s: %v", canonicalCreateAgent, option, paneID, err)
			}
		}
		result = createResult{kind: coremetadata.KindAgent, uid: agent.Metadata.UID, name: agent.Metadata.Name,
			paneID: paneID, projectName: scope.rootName, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}
		return nil
	}, c.canonicalIntentGuards(scope)...)
	if err != nil {
		return err
	}
	return c.writeResults(stdout, canonicalCreateAgent, cli.OutputModeDefault, coremetadata.KindAgent, []createResult{result})
}
