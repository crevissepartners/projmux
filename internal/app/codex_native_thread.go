package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/version"
)

const codexNativeThreadTimeout = 25 * time.Second

type codexNativeThreadController interface {
	Current(context.Context) (codexNativeEndpointRoute, error)
	CatalogRoutes(context.Context) ([]codexNativeEndpointRoute, error)
	Resolve(context.Context, coremetadata.CodexEndpointRef) (codexNativeEndpointRoute, error)
	Create(context.Context, codexNativeEndpointRoute, codexNativeCreateInput) (codexappserver.ThreadBinding, error)
	// Resume loads one stored thread and makes its later turns run with the
	// launch's settings: the model and effort the launch argv carries and the
	// thread policy the Agent's profile holds now. Zero settings send the
	// request every resume sent before a profile could carry a policy.
	Resume(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	// ProbeThreadSettings sends one thread/settings/update that changes
	// nothing, so a restart learns whether the endpoint can change the
	// thread's settings before it stops anything.
	ProbeThreadSettings(context.Context, codexNativeEndpointRoute, string) error
	CanFallback(error) bool
}

// defaultCodexNativeThreadController is the only native route owner: every
// create, catalog, resolve, and durable guard goes to the shared `codex
// app-server daemon` endpoint. The owner-private rolling-upgrade journal
// (`<stateDir>/codex-generations/rolling-upgrade.json`) is never read here, so
// its presence cannot decide a route.
//
// stateDir is used only to locate the deterministic socket of a retired
// private generation (managedCodexRuntimeLocation) so a resume can refuse
// while that host may still hold the thread. Empty skips that check.
type defaultCodexNativeThreadController struct {
	stateDir      string
	retiredListen func(context.Context, string) bool
	current       func(context.Context) (codexNativeEndpointRoute, error)
	probe         func(context.Context) codexappserver.Health
	open          func(context.Context, codexNativeEndpointRoute, bool) (codexNativeThreadClient, error)
	awaitDurable  func(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string) error
	guard         func(context.Context, codexNativeEndpointRoute) error
}

// newCodexNativeThreadController is the production native route owner that
// App wires for create, resume, and the resume catalog.
func newCodexNativeThreadController(stateDir string) defaultCodexNativeThreadController {
	return defaultCodexNativeThreadController{stateDir: stateDir}
}

// codexNativeCreateInput is everything one native fresh create sends to the
// shared endpoint. It is a struct rather than three trailing strings because
// DeveloperInstructions, Prompt and RequestKey are all free text and only
// their names tell them apart.
//
// DeveloperInstructions is what the thread is started with: the agent
// guidance and the persona snapshot content, in that order
// (agentGuidanceLaunch.developerInstructions). Empty means the create sends
// no such field at all, which is what every create did before a Codex Agent
// could have either. It is start-only: a thread keeps the instructions it was
// started with, and resume re-sends nothing (see
// (*aiCommand).resumePersonaSnapshot).
//
// Policy is the sandbox and approval of the Agent's profile, in the wire
// vocabulary (codexThreadPolicy). The zero value sends neither key, which is
// every create without a profile or with a profile that sets neither. Unlike
// the persona it is not start-only: every native resume re-sends the policy
// the profile holds then, and the answer is checked each time.
type codexNativeCreateInput = mutationruntime.NativeCodexCreateInput

type codexNativeThreadClient interface {
	StartThreadWithSettings(context.Context, string, []string, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	StartTurnWithOptions(context.Context, string, string, string, string, string) (string, error)
	BootstrapThread(context.Context, string, string, []string) (codexappserver.ThreadSnapshot, error)
	Close() error
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

func (route codexNativeEndpointRoute) shared() mutationruntime.NativeCodexRoute {
	return mutationruntime.NativeCodexRoute{Endpoint: route.Endpoint, State: route.State, SocketPath: route.SocketPath, TUIExecutable: route.TUIExecutable, Default: route.Default}
}
func (route codexNativeEndpointRoute) valid() bool { return route.shared().Valid() }

func (route codexNativeEndpointRoute) brokerRoute() codexBrokerEndpointRoute {
	return codexBrokerEndpointRoute{
		StateDomainID: route.Endpoint.StateDomainID, EndpointGenerationID: route.Endpoint.EndpointGenerationID,
		SocketPath: route.SocketPath, Default: route.Default,
	}
}

type codexNativeRouteError = mutationruntime.NativeCodexRouteError

// codexDaemonGuidance names the `codex app-server daemon` step that makes the
// default endpoint attachable, or nothing for a ready endpoint whose readiness
// decision needs no recovery. It is guidance only: Projmux never runs it from a
// create, and it never launches a private app-server in its place.
func codexDaemonGuidance(health codexappserver.Health) string {
	return mutationruntime.NativeCodexDaemonGuidance(health)
}

const (
	codexNativeReasonGenerationUnavailable = "generation-unavailable"
	codexNativeReasonLegacyEndpointMissing = "legacy-generation-unavailable"
	// codexNativeReasonRetiredStateDomain refuses a stored endpoint whose
	// thread lives under another CODEX_HOME than the default daemon serves.
	codexNativeReasonRetiredStateDomain = "retired-generation-state-domain-mismatch"
	// codexNativeReasonRetiredRunning refuses a switch while the retired
	// private generation's deterministic socket still accepts connections.
	codexNativeReasonRetiredRunning = "retired-generation-running"
)

func codexRetiredGenerationRefusal(reason, socketPath, agentRef string) *codexNativeRouteError {
	return mutationruntime.NativeCodexRetiredGenerationRefusal(reason, socketPath, agentRef)
}

func (controller defaultCodexNativeThreadController) Current(ctx context.Context) (codexNativeEndpointRoute, error) {
	if controller.current != nil {
		return controller.current(ctx)
	}
	probe := controller.probe
	if probe == nil {
		probe = func(ctx context.Context) codexappserver.Health {
			return codexappserver.ProbeDefaultProxy(ctx, codexNativeThreadTimeout, version.String(), true)
		}
	}
	route, err := mutationruntime.CurrentNativeCodexRoute(ctx, probe, exec.LookPath, os.Getenv, os.UserHomeDir)
	if err != nil {
		return codexNativeEndpointRoute{}, err
	}
	return codexNativeEndpointRoute{Endpoint: route.Endpoint, State: route.State, SocketPath: route.SocketPath, TUIExecutable: route.TUIExecutable, Default: route.Default}, nil
}

// defaultCodexStateDomainID derives a content-free durable identity from the
// exact canonical Codex state root. Two CODEX_HOME values cannot alias merely
// because they run the same Codex version, while symlink spellings of one
// physical root converge to one identity.
func defaultCodexStateDomainID(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	return mutationruntime.NativeCodexStateDomainID(lookupEnv, homeDir)
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
	route, err := mutationruntime.ResolveNativeCodexEndpoint(ctx, endpoint, controller.stateDir, func(ctx context.Context) (mutationruntime.NativeCodexRoute, error) {
		r, err := controller.Current(ctx)
		return r.shared(), err
	}, controller.retiredListen)
	return localNativeCodexRoute(route), err
}

const (
	managedCodexRuntimeKeyBytes    int = 16
	managedCodexSocketPathMaxBytes int = 100
)

func (controller defaultCodexNativeThreadController) Create(ctx context.Context, route codexNativeEndpointRoute, input codexNativeCreateInput) (codexappserver.ThreadBinding, error) {
	if !route.valid() || route.State != codexgeneration.StateCurrent {
		return codexappserver.ThreadBinding{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	return mutationruntime.CreateNativeCodexThread(ctx, input, func(ctx context.Context, experimental bool) (mutationruntime.NativeCodexThreadClient, error) {
		return controller.openRoute(ctx, route, experimental)
	}, func(ctx context.Context, w coremetadata.AgentWorkspace, id string) error {
		return controller.awaitDurableResume(ctx, route, w, id)
	})
}

func (controller defaultCodexNativeThreadController) awaitDurableResume(
	ctx context.Context,
	route codexNativeEndpointRoute,
	workspace coremetadata.AgentWorkspace,
	threadID string,
) error {
	if controller.awaitDurable != nil {
		return controller.awaitDurable(ctx, route, workspace, threadID)
	}
	_, err := (codexappserver.DurableResumeBarrier{Open: func(openCtx context.Context) (codexappserver.DurableResumeClient, error) {
		if controller.guard != nil {
			if err := controller.guard(openCtx, route); err != nil {
				return nil, err
			}
		} else if route.Default {
			current, err := controller.Current(openCtx)
			if err != nil || !current.Endpoint.Same(route.Endpoint) || !current.Default || current.State != codexgeneration.StateCurrent {
				return nil, codexappserver.ErrEndpointChanged
			}
		}
		return controller.openRoute(openCtx, route, true)
	}}).Await(ctx, threadID, workspace.CWD, workspace.AdditionalWritableRoots)
	return err
}

func (controller defaultCodexNativeThreadController) openRoute(ctx context.Context, route codexNativeEndpointRoute, experimental bool) (codexNativeThreadClient, error) {
	if controller.open != nil {
		return controller.open(ctx, route, experimental)
	}
	return openCodexNativeRoute(ctx, route, experimental)
}

func (defaultCodexNativeThreadController) Resume(ctx context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string, settings codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
	if !route.valid() {
		return codexappserver.ThreadBinding{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	client, err := openCodexNativeRoute(ctx, route, true)
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	defer client.Close()
	return client.ResumeThreadWithSettings(ctx, threadID, workspace.CWD, workspace.AdditionalWritableRoots, settings)
}

func (defaultCodexNativeThreadController) ProbeThreadSettings(ctx context.Context, route codexNativeEndpointRoute, threadID string) error {
	if !route.valid() {
		return &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	}
	client, err := openCodexNativeRoute(ctx, route, true)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.UpdateThreadSettings(ctx, threadID, codexappserver.ThreadSettings{})
}

func openCodexNativeRoute(ctx context.Context, route codexNativeEndpointRoute, experimental bool) (*codexappserver.Client, error) {
	return mutationruntime.OpenNativeCodexRoute(ctx, route.shared(), experimental)
}

func (defaultCodexNativeThreadController) CanFallback(err error) bool {
	return codexappserver.CanFallback(err)
}

type codexNativeAgentLauncher interface {
	PlanNativeCodexResume(codexNativeEndpointRoute, coremetadata.AgentWorkspace, string) (title string, argv []string, err error)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

type codexNativeOptionsLauncher interface {
	PlanNativeCodexResumeWithOptions(codexNativeEndpointRoute, coremetadata.AgentWorkspace, string, string, string) (title string, argv []string, err error)
}

func planNativeCodexResumeOptions(launcher codexNativeAgentLauncher, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID, model, effort string) (string, []string, error) {
	if model == "" && effort == "" {
		return launcher.PlanNativeCodexResume(route, workspace, threadID)
	}
	withOptions, ok := launcher.(codexNativeOptionsLauncher)
	if !ok {
		return "", nil, errors.New("native Codex options launcher is unavailable")
	}
	return withOptions.PlanNativeCodexResumeWithOptions(route, workspace, threadID, model, effort)
}

func resolveCodexNativeResumeRoute(ctx context.Context, controller codexNativeThreadController, ref *coremetadata.AgentSessionRef, agentRef string) (codexNativeEndpointRoute, error) {
	var resolve func(context.Context, coremetadata.CodexEndpointRef) (mutationruntime.NativeCodexRoute, error)
	if controller != nil {
		resolve = func(ctx context.Context, e coremetadata.CodexEndpointRef) (mutationruntime.NativeCodexRoute, error) {
			r, err := controller.Resolve(ctx, e)
			return r.shared(), err
		}
	}
	route, err := mutationruntime.ResolveNativeCodexResumeRoute(ctx, ref, agentRef, resolve)
	return localNativeCodexRoute(route), err
}
func localNativeCodexRoute(r mutationruntime.NativeCodexRoute) codexNativeEndpointRoute {
	return codexNativeEndpointRoute{Endpoint: r.Endpoint, State: r.State, SocketPath: r.SocketPath, TUIExecutable: r.TUIExecutable, Default: r.Default}
}

// validateStoredCodexNativeResumeRoute is the part of
// resolveCodexNativeResumeRoute that reads only the stored ref: a thread with
// no durable endpoint, or one whose recorded lifecycle does not admit it, is
// refused without asking any daemon.
func validateStoredCodexNativeResumeRoute(ref *coremetadata.AgentSessionRef) error {
	return mutationruntime.ValidateStoredCodexNativeResumeRoute(ref)
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
// Prompted fresh creates and app-server sourced resumes require native
// authority. A fresh create with no payload instead chooses the permanent
// plain fallback before any provider mutation; explicit interactive-only and a
// picker row sourced from rollout remain the other declared plain lanes. None
// of those declared lanes can satisfy native control authority.
var codexNativeLaunchOutcomeTable = []codexNativeLaunchOutcomeRow{
	{Action: "create", NativeResult: "thread+turn", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread/turn"},
	{Action: "resume", NativeResult: "same thread", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread"},
	{Action: "create", NativeResult: "no payload", Launch: "current CLI", Binding: "declared payload-free fallback; native turn control unavailable"},
	{Action: "create", NativeResult: "explicit --interactive-only", Launch: "current CLI", Binding: "no native binding; native turn control unavailable"},
	{Action: "resume", NativeResult: "rollout picker source", Launch: "current CLI resume", Binding: "no native endpoint authority"},
	{Action: "create", NativeResult: "unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse and name --interactive-only"},
	{Action: "create", NativeResult: "selector resolved several Windows", Launch: "none", Binding: "write zero before allocation; refuse and name --interactive-only"},
	{Action: "resume", NativeResult: "create-time picker row, unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse without a second lane"},
	{Action: "resume", NativeResult: "stored Agent rebind, unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse without a second lane"},
	{Action: "create", NativeResult: "indeterminate after thread creation", Launch: "none", Binding: "write zero; refuse duplicate lane"},
	{Action: "negative fixture", NativeResult: "payload-free thread not durably resumable", Launch: "none", Binding: "historical safety evidence only; never functional create success"},
}

func nativePrompt(payload []string) (string, bool) {
	return mutationruntime.NativePrompt(payload)
}

func prepareNativeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, codexNativeThreadTimeout)
}

func nativeFallbackAllowed(controller codexNativeThreadController, err error) bool {
	return err != nil && controller != nil && controller.CanFallback(err)
}

func nativeLaunchError(spelling string, err error) error {
	return mutationruntime.NativeLaunchError(spelling, err)
}

// interactiveOnlyFlag is the one public spelling that asks for a plain-CLI
// Codex Agent with no native thread binding. It is named in every refusal that
// has an explicit escape hatch, and in none that does not.
const interactiveOnlyFlag = "--interactive-only"

// nativeRootsUnsupported reports whether a native failure is the fail-closed
// additional-writable-roots classification.
//
// That row is not a safe fallback -- launching anyway would narrow the
// operator's writable workspace -- but it is still raised before any provider
// conversation exists, so it belongs with the pre-mutation refusals and not
// with the indeterminate one.
func nativeRootsUnsupported(err error) bool {
	return mutationruntime.NativeRootsUnsupported(err)
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
	return mutationruntime.NativeCreatePreparationRefusal(spelling, err, capability)
}

// nativeResumePreparationRefusal answers a stored-thread resume whose native
// authority could not be proven. A resume names an existing app-server
// conversation, so there is no launch mode to fall back to and no
// `--interactive-only` escape hatch to offer: the only honest answer is that
// the stored thread cannot be resumed natively right now.
func nativeResumePreparationRefusal(spelling string, err error) error {
	return mutationruntime.NativeResumePreparationRefusal(spelling, err)
}

// nativeFanOutRefusal answers a default native Codex create whose selector
// resolved several Windows.
//
// One create can own exactly one native thread, and a Registry rollback cannot
// delete app-server threads, so a fan-out has no atomic native shape. It is a
// usage error because the selector is what is wrong: narrowing it, or asking
// for the plain-CLI fan-out on purpose, both fix it.
func nativeFanOutRefusal(spelling string, targets int) error {
	return MapMetadataError(mutationruntime.NativeFanOutRefusal(spelling, targets))
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
