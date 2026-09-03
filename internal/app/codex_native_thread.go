package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	"github.com/crevissepartners/projmux/internal/version"
)

const codexNativeThreadTimeout = 25 * time.Second

type codexNativeThreadController interface {
	Current(context.Context) (codexNativeEndpointRoute, error)
	CatalogRoutes(context.Context) ([]codexNativeEndpointRoute, error)
	Resolve(context.Context, coremetadata.CodexEndpointRef) (codexNativeEndpointRoute, error)
	Create(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string, string) (codexappserver.ThreadBinding, error)
	Resume(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string) (codexappserver.ThreadBinding, error)
	CanFallback(error) bool
}

type defaultCodexNativeThreadController struct {
	current func(context.Context) (codexNativeEndpointRoute, error)
}

// rollingCodexNativeThreadController overlays the owner-private Phase 4
// admission journal on the attach-only default endpoint. An absent journal
// preserves the Phase 3 default behavior; a present journal is exact and never
// falls through to or adopts the ambient endpoint.
type rollingCodexNativeThreadController struct {
	journal   *codexupgrade.Store
	fallback  defaultCodexNativeThreadController
	activator codexManagedCurrentActivator
	observe   func(context.Context, codexupgrade.GenerationRoute) error
	create    func(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string, string) (codexappserver.ThreadBinding, error)
}

func (controller rollingCodexNativeThreadController) Current(ctx context.Context) (codexNativeEndpointRoute, error) {
	journal, exists, err := controller.load()
	if err != nil {
		return codexNativeEndpointRoute{}, err
	}
	if !exists {
		route, fallbackErr := controller.fallback.Current(ctx)
		if fallbackErr == nil {
			return route, nil
		}
		if controller.activator == nil {
			return codexNativeEndpointRoute{}, fallbackErr
		}
		if activationErr := controller.activator.Ensure(ctx); activationErr != nil {
			var refusal *managedCodexActivationError
			if errors.As(activationErr, &refusal) {
				return codexNativeEndpointRoute{}, &codexNativeRouteError{
					Reason: "managed-generation-activation-blocked", OperatorAction: refusal.Action, err: activationErr,
				}
			}
			return codexNativeEndpointRoute{}, &codexNativeRouteError{
				Reason:         "managed-generation-activation-blocked",
				OperatorAction: "run `projmux doctor --section integrations --json --verbose` and perform the exact generation action it reports before retrying",
				err:            activationErr,
			}
		}
		journal, exists, err = controller.load()
		if err != nil || !exists {
			return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable, err: err}
		}
	}
	route, ok := journal.CurrentRoute()
	if ok && controller.activator != nil && incompleteManagedActivation(journal) {
		if activationErr := controller.activator.Ensure(ctx); activationErr != nil {
			var refusal *managedCodexActivationError
			if errors.As(activationErr, &refusal) {
				return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: "managed-generation-activation-blocked", OperatorAction: refusal.Action, err: activationErr}
			}
			return codexNativeEndpointRoute{}, &codexNativeRouteError{
				Reason:         "managed-generation-activation-blocked",
				OperatorAction: "run `projmux doctor --section integrations --json --verbose` and perform the exact generation action it reports before retrying",
				err:            activationErr,
			}
		}
		journal, _, err = controller.load()
		if err != nil {
			return codexNativeEndpointRoute{}, err
		}
		route, ok = journal.CurrentRoute()
	}
	if !ok || controller.observeRoute(ctx, route) != nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return rollingNativeRoute(route), nil
}

func incompleteManagedActivation(journal codexupgrade.Journal) bool {
	if journal.Operation == nil || journal.Operation.Aborted || (journal.Operation.AdmissionCommitted && journal.Operation.DrainPublished) {
		return false
	}
	oldExternal, targetPrivate := false, false
	for _, route := range journal.Routes {
		switch route.Generation.Endpoint.EndpointGenerationID {
		case journal.Operation.OldGenerationID:
			oldExternal = route.Generation.Owner == codexgeneration.OwnerUnmanaged || route.Generation.Owner == codexgeneration.OwnerOfficialManaged
		case journal.Operation.TargetGenerationID:
			targetPrivate = route.Generation.Owner == codexgeneration.OwnerProjmuxPrivate
		}
	}
	return oldExternal && targetPrivate
}

func (controller rollingCodexNativeThreadController) CatalogRoutes(ctx context.Context) ([]codexNativeEndpointRoute, error) {
	journal, exists, err := controller.load()
	if err != nil {
		return nil, err
	}
	if !exists {
		return controller.fallback.CatalogRoutes(ctx)
	}
	routes := make([]codexNativeEndpointRoute, 0, len(journal.Routes))
	for _, route := range journal.Routes {
		if !route.Ready || route.Proof == nil {
			continue
		}
		if err := controller.observeRoute(ctx, route); err != nil {
			continue
		}
		routes = append(routes, rollingNativeRoute(route))
	}
	if len(routes) == 0 {
		return nil, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return routes, nil
}

func (controller rollingCodexNativeThreadController) Resolve(ctx context.Context, endpoint coremetadata.CodexEndpointRef) (codexNativeEndpointRoute, error) {
	if !endpoint.Valid() {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing}
	}
	journal, exists, err := controller.load()
	if err != nil {
		return codexNativeEndpointRoute{}, err
	}
	if !exists {
		return controller.fallback.Resolve(ctx, endpoint)
	}
	route, ok := journal.Route(endpoint)
	if !ok || !route.Ready || route.Proof == nil || controller.observeRoute(ctx, route) != nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return rollingNativeRoute(route), nil
}

func (controller rollingCodexNativeThreadController) Create(ctx context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, prompt, requestKey string) (codexappserver.ThreadBinding, error) {
	if controller.create != nil {
		return controller.create(ctx, route, workspace, prompt, requestKey)
	}
	return controller.fallback.Create(ctx, route, workspace, prompt, requestKey)
}

func (controller rollingCodexNativeThreadController) Resume(ctx context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	return controller.fallback.Resume(ctx, route, workspace, threadID)
}

func (controller rollingCodexNativeThreadController) CanFallback(err error) bool {
	return controller.fallback.CanFallback(err)
}

func (controller rollingCodexNativeThreadController) load() (codexupgrade.Journal, bool, error) {
	if controller.journal == nil {
		return codexupgrade.Journal{}, false, nil
	}
	journal, exists, err := controller.journal.Load()
	if err != nil {
		return codexupgrade.Journal{}, false, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return journal, exists, nil
}

func (controller rollingCodexNativeThreadController) observeRoute(ctx context.Context, route codexupgrade.GenerationRoute) error {
	if controller.observe != nil {
		return controller.observe(ctx, route)
	}
	return codexupgrade.ObserveRoute(ctx, route)
}

func rollingNativeRoute(route codexupgrade.GenerationRoute) codexNativeEndpointRoute {
	return codexNativeEndpointRoute{Endpoint: route.Generation.Endpoint, State: route.Generation.State, SocketPath: route.Config.SocketPath, TUIExecutable: route.TUIPath}
}

// codexNativeEndpointRoute is process-local routing material for one durable
// endpoint identity. Paths never enter Registry metadata; the exact endpoint
// ref does. Current selection is an injected read-only fact in Phase 3.
type codexNativeEndpointRoute struct {
	Endpoint      coremetadata.CodexEndpointRef
	State         codexgeneration.GenerationState
	SocketPath    string
	TUIExecutable string
	Default       bool
}

func (route codexNativeEndpointRoute) valid() bool {
	socketPath := strings.TrimSpace(route.SocketPath)
	transportValid := (route.Default && socketPath == "") || (!route.Default && filepath.IsAbs(socketPath))
	return route.Endpoint.Valid() && route.State != "" && filepath.IsAbs(strings.TrimSpace(route.TUIExecutable)) && transportValid
}

func (route codexNativeEndpointRoute) brokerRoute() codexBrokerEndpointRoute {
	return codexBrokerEndpointRoute{
		StateDomainID: route.Endpoint.StateDomainID, EndpointGenerationID: route.Endpoint.EndpointGenerationID,
		SocketPath: route.SocketPath, Default: route.Default,
	}
}

type codexNativeRouteError struct {
	Reason         string
	OperatorAction string
	err            error
}

func (e *codexNativeRouteError) Error() string {
	message := "Codex generation route: " + e.Reason
	if strings.TrimSpace(e.OperatorAction) != "" {
		message += "; operator action: " + e.OperatorAction
	}
	return message
}

func (e *codexNativeRouteError) Unwrap() error { return e.err }

const (
	codexNativeReasonGenerationUnavailable = "generation-unavailable"
	codexNativeReasonLegacyEndpointMissing = "legacy-generation-unavailable"
	codexNativeReasonHandoverRequired      = "handover-required"
)

func (controller defaultCodexNativeThreadController) Current(ctx context.Context) (codexNativeEndpointRoute, error) {
	if controller.current != nil {
		return controller.current(ctx)
	}
	health := codexappserver.ProbeDefaultProxy(ctx, codexNativeThreadTimeout, version.String(), true)
	if codexappserver.AuthorityFor(health).Attach != codexappserver.EndpointAttachAllowed {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	runningVersion := strings.TrimSpace(health.RunningVersion)
	if !codexappserver.IsSafeDiagnosticVersion(runningVersion) {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(executable) {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	stateDomainID, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return codexNativeEndpointRoute{
		Endpoint: coremetadata.CodexEndpointRef{
			StateDomainID:        stateDomainID,
			EndpointGenerationID: "codex-" + runningVersion,
		},
		State: coremetadata.CodexGenerationCurrent, TUIExecutable: executable, Default: true,
	}, nil
}

// defaultCodexStateDomainID derives a content-free durable identity from the
// exact canonical Codex state root. Two CODEX_HOME values cannot alias merely
// because they run the same Codex version, while symlink spellings of one
// physical root converge to one identity.
func defaultCodexStateDomainID(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	_, id, err := defaultCodexStateDomain(lookupEnv, homeDir)
	return id, err
}

func defaultCodexStateDomain(lookupEnv func(string) string, homeDir func() (string, error)) (string, string, error) {
	root := strings.TrimSpace(lookupEnv("CODEX_HOME"))
	if root == "" {
		home, err := homeDir()
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(root) {
		return "", "", errors.New("codex state domain must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("codex state domain must be an existing directory")
	}
	sum := sha256.Sum256([]byte(canonical))
	return canonical, "codex-state-" + hex.EncodeToString(sum[:16]), nil
}

// CatalogRoutes is a read-only inventory projection. The bootstrap/default
// controller has exactly one attach-authorized unmanaged generation; injected
// pool controllers may return current plus draining generations without
// changing admission-current or acquiring lifecycle authority.
func (controller defaultCodexNativeThreadController) CatalogRoutes(ctx context.Context) ([]codexNativeEndpointRoute, error) {
	route, err := controller.Current(ctx)
	if err != nil {
		return nil, err
	}
	return []codexNativeEndpointRoute{route}, nil
}

func (controller defaultCodexNativeThreadController) Resolve(ctx context.Context, endpoint coremetadata.CodexEndpointRef) (codexNativeEndpointRoute, error) {
	if !endpoint.Valid() {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing}
	}
	route, err := controller.Current(ctx)
	if err != nil || !route.Endpoint.Same(endpoint) {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return route, nil
}

func (defaultCodexNativeThreadController) Create(ctx context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, prompt, requestKey string) (codexappserver.ThreadBinding, error) {
	if !route.valid() || route.State != codexgeneration.StateCurrent {
		return codexappserver.ThreadBinding{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	client, err := openCodexNativeRoute(ctx, route, len(workspace.AdditionalWritableRoots) > 0)
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	defer client.Close()
	binding, err := client.StartThread(ctx, workspace.CWD, workspace.AdditionalWritableRoots)
	if err != nil || prompt == "" {
		return binding, err
	}
	binding.TurnID, err = client.StartTurn(ctx, binding.ThreadID, prompt, requestKey)
	return binding, err
}

func (defaultCodexNativeThreadController) Resume(ctx context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	if !route.valid() {
		return codexappserver.ThreadBinding{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	client, err := openCodexNativeRoute(ctx, route, true)
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	defer client.Close()
	return client.ResumeThread(ctx, threadID, workspace.CWD, workspace.AdditionalWritableRoots)
}

func openCodexNativeRoute(ctx context.Context, route codexNativeEndpointRoute, experimental bool) (*codexappserver.Client, error) {
	if route.Default {
		client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(), codexappserver.AttachOptions{
			Timeout: codexNativeThreadTimeout, ExperimentalAPI: experimental,
		})
		return client, err
	}
	return codexappserver.OpenPrivateUnix(ctx, route.SocketPath, codexNativeThreadTimeout, version.String(), experimental)
}

func (defaultCodexNativeThreadController) CanFallback(err error) bool {
	return codexappserver.CanFallback(err)
}

type codexNativeAgentLauncher interface {
	PlanNativeCodexResume(codexNativeEndpointRoute, coremetadata.AgentWorkspace, string) (title string, argv []string, err error)
	BindNativeCodexPane(paneID, contextDir, title, threadID string)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

func resolveCodexNativeResumeRoute(ctx context.Context, controller codexNativeThreadController, ref *coremetadata.AgentSessionRef) (codexNativeEndpointRoute, error) {
	if ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing}
	}
	endpoint := *ref.Codex.Endpoint
	if err := validateCodexNativeResumeRoute(ref, codexNativeEndpointRoute{Endpoint: endpoint, State: coremetadata.CodexGenerationCurrent}); err != nil {
		return codexNativeEndpointRoute{}, err
	}
	if controller == nil {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	route, err := controller.Resolve(ctx, endpoint)
	if err != nil {
		return codexNativeEndpointRoute{}, err
	}
	if !route.valid() || !route.Endpoint.Same(endpoint) {
		return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	if err := validateCodexNativeResumeRoute(ref, route); err != nil {
		return codexNativeEndpointRoute{}, err
	}
	return route, nil
}

func validateCodexNativeResumeRoute(ref *coremetadata.AgentSessionRef, route codexNativeEndpointRoute) error {
	if ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() {
		return &codexNativeRouteError{Reason: codexNativeReasonLegacyEndpointMissing}
	}
	endpoint := *ref.Codex.Endpoint
	lifecycle := ref.Codex.Lifecycle
	if lifecycle == nil || !lifecycle.ValidFor(&endpoint) || !route.Endpoint.Same(endpoint) {
		return &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	if lifecycle.State == coremetadata.CodexGenerationDraining || lifecycle.State == coremetadata.CodexGenerationHandoverPending ||
		route.State == codexgeneration.StateDraining || route.State == codexgeneration.StateHandoverPending {
		return &codexNativeRouteError{Reason: codexNativeReasonHandoverRequired}
	}
	if lifecycle.State != coremetadata.CodexGenerationCurrent || route.State != codexgeneration.StateCurrent {
		return &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return nil
}

func bindNativeCodexPaneOnRoute(
	ctx context.Context,
	launcher codexNativeAgentLauncher,
	runner tmuxCommandRunner,
	paneID, contextDir, title, topic, threadID string,
) error {
	return launcher.BindAgentPaneOnRoute(ctx, runner, agentPaneBinding{
		PaneID: paneID, Provider: aiModeCodex, ContextDir: contextDir, Title: title,
		Topic: topic, TopicManual: strings.TrimSpace(topic) != "",
		ConversationID: threadID, ThreadID: threadID, NativeCodex: true,
	})
}

type codexNativeLaunchOutcomeRow struct {
	Action       string
	NativeResult string
	Launch       string
	Binding      string
}

// codexNativeLaunchOutcomeTable is the closed native launch outcome contract.
//
// Native authority is required exactly where an Agent is created. Only the
// explicit interactive-only lane and a picker row sourced from rollout reach
// the plain CLI. Neither can satisfy native control authority.
var codexNativeLaunchOutcomeTable = []codexNativeLaunchOutcomeRow{
	{Action: "create", NativeResult: "thread+turn", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread/turn"},
	{Action: "create", NativeResult: "thread without turn", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread/endpoint; no-turn obligation"},
	{Action: "resume", NativeResult: "same thread", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread"},
	{Action: "create", NativeResult: "explicit --interactive-only", Launch: "current CLI", Binding: "no native binding; native turn control unavailable"},
	{Action: "resume", NativeResult: "rollout picker source", Launch: "current CLI resume", Binding: "no native endpoint authority"},
	{Action: "create", NativeResult: "unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse and name --interactive-only"},
	{Action: "create", NativeResult: "selector resolved several Windows", Launch: "none", Binding: "write zero before allocation; refuse and name --interactive-only"},
	{Action: "resume", NativeResult: "create-time picker row, unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse without a second lane"},
	{Action: "resume", NativeResult: "stored Agent rebind, unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse without a second lane"},
	{Action: "create", NativeResult: "indeterminate after thread creation", Launch: "none", Binding: "write zero; refuse duplicate lane"},
}

func nativePrompt(payload []string) (string, bool) {
	switch len(payload) {
	case 0:
		return "", true
	case 1:
		return payload[0], true
	default:
		// The legacy CLI owns provider parsing for a multi-operand payload. Native
		// turn/start accepts one text item, so joining operands here would silently
		// invent prompt semantics.
		return "", false
	}
}

func prepareNativeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, codexNativeThreadTimeout)
}

func nativeFallbackAllowed(controller codexNativeThreadController, err error) bool {
	return err != nil && controller != nil && controller.CanFallback(err)
}

func nativeLaunchError(spelling string, err error) error {
	if err == nil {
		return nil
	}
	// Provider identity is already indeterminate here, so this refusal must not
	// offer any second lane -- `--interactive-only` included. Starting another
	// Codex process now could submit the same prompt twice.
	return errors.New(spelling + ": native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: " + err.Error())
}

// interactiveOnlyFlag is the one public spelling that asks for a plain-CLI
// Codex Agent with no native thread binding. It is named in every refusal that
// has an explicit escape hatch, and in none that does not.
const interactiveOnlyFlag = "--interactive-only"

// nativeThreadReason renders the typed classification of a native preparation
// failure. A refusal has to say why native authority could not be proven, not
// only that it could not, or the operator cannot tell an unreachable endpoint
// from an endpoint that is missing a capability.
func nativeThreadReason(err error) string {
	var route *codexNativeRouteError
	if errors.As(err, &route) && strings.TrimSpace(route.Reason) != "" {
		return route.Reason
	}
	var action *codexappserver.ThreadActionError
	if errors.As(err, &action) {
		if reason := strings.TrimSpace(action.Reason); reason != "" {
			return reason
		}
	}
	return "unclassified"
}

// nativeRootsUnsupported reports whether a native failure is the fail-closed
// additional-writable-roots classification.
//
// That row is not a safe fallback -- launching anyway would narrow the
// operator's writable workspace -- but it is still raised before any provider
// conversation exists, so it belongs with the pre-mutation refusals and not
// with the indeterminate one.
func nativeRootsUnsupported(err error) bool {
	return nativeThreadReason(err) == codexappserver.ReasonAdditionalRootsUnsupported
}

// nativeCreatePreparationRefusal answers a prompted native Codex create whose
// native authority could not be proven before any provider conversation was
// mutated.
//
// Nothing is committed and nothing is launched. The old behaviour -- quietly
// creating a managed Agent on the hook/plain lane -- produced an Agent that
// looked native, carried no thread binding, and answered no native turn
// control, so the degradation is now a refusal that names the explicit opt-out.
func nativeCreatePreparationRefusal(spelling string, err error) error {
	return nativeCreatePreparationRefusalForCapability(spelling, err, codexappserver.ObserveDefaultInstallCapability())
}

func nativeCreatePreparationRefusalForCapability(spelling string, err error, capability codexappserver.InstallCapability) error {
	if err == nil {
		return nil
	}
	guidance := codexInstallCapabilityGuidance(capability)
	return errors.New(spelling + ": native Codex thread preparation is unavailable (" + nativeThreadReason(err) +
		") and no provider conversation was mutated; refusing to create a managed Agent with no native thread binding. " +
		"Re-run with " + interactiveOnlyFlag + " for a plain interactive Codex Agent with no native turn control, " +
		"or make the Codex app-server endpoint available. Install capability " + string(guidance.Capability) + ": " +
		guidance.Text() + ". Native error: " + err.Error())
}

// nativeResumePreparationRefusal answers a stored-thread resume whose native
// authority could not be proven. A resume names an existing app-server
// conversation, so there is no launch mode to fall back to and no
// `--interactive-only` escape hatch to offer: the only honest answer is that
// the stored thread cannot be resumed natively right now.
func nativeResumePreparationRefusal(spelling string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(spelling + ": the stored Codex thread cannot be resumed natively right now (" + nativeThreadReason(err) +
		"); refusing to rebind it onto a lane with no native turn control: " + err.Error())
}

// nativeFanOutRefusal answers a default native Codex create whose selector
// resolved several Windows.
//
// One create can own exactly one native thread, and a Registry rollback cannot
// delete app-server threads, so a fan-out has no atomic native shape. It is a
// usage error because the selector is what is wrong: narrowing it, or asking
// for the plain-CLI fan-out on purpose, both fix it.
func nativeFanOutRefusal(spelling string, targets int) error {
	return usageError(fmt.Sprintf(
		"%s creates exactly one native Codex thread, but this selector resolved %d Windows; "+
			"narrow the selector to one Window, or pass %s to keep the plain-CLI fan-out of one Agent per resolved Window",
		spelling, targets, interactiveOnlyFlag))
}

// requireInteractiveOnlyProvider refuses `--interactive-only` on a provider
// that has no native lane to opt out of. Accepting it as a silent no-op would
// tell the operator their Claude or Antigravity Agent was launched under a mode
// that never existed.
func requireInteractiveOnlyProvider(spelling, provider string, flags resourceCreateFlags) error {
	if !flags.interactiveOnly || provider == aiModeCodex {
		return nil
	}
	return usageError(fmt.Sprintf(
		"%s %s applies only to --provider %s; %s has no native thread binding to opt out of",
		spelling, interactiveOnlyFlag, aiModeCodex, provider))
}
