package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

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

// paneTarget is one preflighted (Window, anchor Pane) pair.
type paneTarget struct {
	windowUID    string
	anchorUID    string
	storedAnchor bool
	virtual      bool
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

func (s createScope) shared() mutationruntime.CreateScope {
	return mutationruntime.CreateScope{Project: s.project, Window: s.window, Pane: s.pane}
}
func (f resourceCreateFlags) sharedSelection() mutationruntime.CreateSelection {
	return mutationruntime.CreateSelection{Name: f.name, Windows: f.windows, Panes: f.panes, Selectors: f.selectors, CreateWindow: f.createWindow, PrimarySpelling: f.primaryWindowSpelling()}
}
func (p paneTarget) shared() mutationruntime.PaneTarget {
	return mutationruntime.PaneTarget{WindowUID: p.windowUID, AnchorUID: p.anchorUID, StoredAnchor: p.storedAnchor, Virtual: p.virtual}
}

func (w windowWork) shared() mutationruntime.WindowWork {
	return mutationruntime.WindowWork{Window: w.window, Initial: w.initial, Payload: w.payload, Activation: w.activation, InitialPaneID: w.initialPaneID}
}
func localWindowWork(w mutationruntime.WindowWork) windowWork {
	return windowWork{w.Window, w.Initial, w.Payload, w.Activation, w.InitialPaneID}
}
func (c *createCommand) createExecution() *mutationruntime.CreateExecution {
	execution := &mutationruntime.CreateExecution{Runtime: c.runtime, BindWindowRuntime: c.observeWindowRuntimeBinding, Reconciler: c.reconciler, Shell: c.shell, EnsureRuntimeRoute: c.ensureRuntimeRoute, IssuePaneActivation: c.issuePaneActivation, AdmitAnchor: func(reg coremetadata.Registry, uid string) error {
		_, _, err := c.processRuntime.admit(reg, uid, resourcegraph.ProcessCreatePane)
		return err
	}}
	if c.processRuntime != nil {
		execution.ProcessInventory = c.processRuntime.inventory
		execution.AdmitSplitAnchor = func(reg coremetadata.Registry, uid string) error {
			pane, ok := reg.Pane(uid)
			if !ok || !c.processRuntime.inventory().Declares(*pane) {
				return nil
			}
			_, _, err := c.processRuntime.admit(reg, uid, resourcegraph.ProcessSplit)
			return err
		}
	}
	return execution
}

func uidRef(kind coremetadata.Kind, uid string) selector.Ref {
	return mutationruntime.UIDRef(kind, uid)
}

func (c *createCommand) resolveProject(registry coremetadata.Registry, scope createScope) (coremetadata.Project, error) {
	return c.createExecution().ResolveProject(registry, scope.shared())
}

func (c *createCommand) refuseMissingRoot(project coremetadata.Project) error {
	return c.createExecution().RefuseMissingRoot(project)
}

func (c *createCommand) allocateWindow(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	req windowRequest,
) (windowWork, error) {
	v, err := c.createExecution().AllocateWindow(working, mutator, project, mutationruntime.WindowRequest{Name: req.name, Labels: req.labels, Payload: req.payload, OperationID: req.operationID})
	return localWindowWork(v), err
}

func (c *createCommand) materializeWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName string,
	work *windowWork,
) error {
	v := work.shared()
	err := c.createExecution().MaterializeWindow(ctx, working, mutator, ledger, project, sessionName, &v)
	*work = localWindowWork(v)
	return err
}

func (c *createCommand) ensureAnchorPane(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	project coremetadata.Project,
	sessionName, operationID string,
	target paneTarget,
) (string, error) {
	return c.createExecution().EnsureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, target.shared())
}

func (c *createCommand) ensureProjectRuntime(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	operationID string,
	ledger *RuntimeLedger,
) (string, error) {
	return c.createExecution().EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
}

func initialWindowName(registry *coremetadata.Registry, projectUID string) string {
	return mutationruntime.InitialWindowName(registry, projectUID)
}

func (c *createCommand) initialPaneActivation(registry *coremetadata.Registry, mutator coremetadata.Mutator, projectUID, operationID string) sessionFirstPaneActivation {
	return c.createExecution().InitialPaneActivation(registry, mutator, projectUID, operationID)
}

func (c *createCommand) adoptInitialWindow(ctx context.Context, registry *coremetadata.Registry, mutator coremetadata.Mutator, project coremetadata.Project, created intmux.NewSessionResult, activation superviseSpec, ledger *RuntimeLedger) error {
	return c.createExecution().AdoptInitialWindow(ctx, registry, mutator, project, created, activation, ledger)
}

func (c *createCommand) projectOwnershipGuard(scope createScope) createPreReconcile {
	return func(ctx context.Context, reg coremetadata.Registry, mut coremetadata.Mutator, op string) (liveSessionIdentity, error) {
		return c.createExecution().ProjectOwnershipGuard(scope.shared())(ctx, reg, mut, op)
	}
}

func (c *createCommand) exactProjectOwnershipGuard(projectUID string) createPreReconcile {
	return func(ctx context.Context, reg coremetadata.Registry, mut coremetadata.Mutator, op string) (liveSessionIdentity, error) {
		return c.createExecution().ExactProjectOwnershipGuard(projectUID)(ctx, reg, mut, op)
	}
}

func localShellResults(rows []mutationruntime.ShellCreateResource) []createResult {
	var results []createResult
	for _, r := range rows {
		results = append(results, createResult{kind: r.Kind, uid: r.UID, name: r.Name, paneID: r.PaneID, projectName: r.ProjectName, windowName: r.WindowName, windowUID: r.WindowUID})
	}
	return results
}
