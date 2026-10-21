package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// ResolveNativeCodexResumeRoute admits only the stored conversation's domain.
func ResolveNativeCodexResumeRoute(ctx context.Context, ref *metadata.AgentSessionRef, agentRef string, resolve func(context.Context, metadata.CodexEndpointRef) (NativeCodexRoute, error)) (NativeCodexRoute, error) {
	if err := ValidateStoredCodexNativeResumeRoute(ref); err != nil {
		return NativeCodexRoute{}, err
	}
	if resolve == nil {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	route, err := resolve(ctx, *ref.Codex.Endpoint)
	if err != nil {
		return NativeCodexRoute{}, BindCodexResumeAgentRef(err, agentRef)
	}
	if !route.Valid() {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	if err := ValidateCodexNativeResumeRoute(ref, route); err != nil {
		return NativeCodexRoute{}, BindCodexResumeAgentRef(err, agentRef)
	}
	return route, nil
}

// ResolveNativeCodexEndpoint retains the retired private socket barrier.
func ResolveNativeCodexEndpoint(ctx context.Context, endpoint metadata.CodexEndpointRef, stateDir string, current func(context.Context) (NativeCodexRoute, error), listen func(context.Context, string) bool) (NativeCodexRoute, error) {
	if !endpoint.Valid() {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonLegacyEndpointMissing}
	}
	route, err := current(ctx)
	if err != nil {
		return NativeCodexRoute{}, err
	}
	if route.Endpoint.StateDomainID != endpoint.StateDomainID {
		return NativeCodexRoute{}, NativeCodexRetiredGenerationRefusal(NativeCodexReasonRetiredStateDomain, "", "")
	}
	version, ok := strings.CutPrefix(endpoint.EndpointGenerationID, "codex-")
	if strings.TrimSpace(stateDir) != "" && ok && codexappserver.IsSafeDiagnosticVersion(version) {
		_, socket, err := ManagedCodexRuntimeLocation(stateDir, endpoint.StateDomainID, version)
		if err == nil {
			if listen == nil {
				listen = DialCodexRetiredGenerationSocket
			}
			if listen(ctx, socket) {
				return NativeCodexRoute{}, NativeCodexRetiredGenerationRefusal(NativeCodexReasonRetiredRunning, socket, "")
			}
		}
	}
	return route, nil
}

func NativeResumePreparationRefusal(spelling string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: the stored Codex thread cannot be resumed natively right now (%s); refusing to rebind it onto a lane with no native turn control: %w", spelling, NativeThreadReason(err), err)
}

func NativeCodexResumeProfile(paths func() (config.Paths, error), annotations map[string]string, projectUID ...string) (AgentResumeLaunch, codexappserver.ThreadPolicy, error) {
	name := annotations[metadata.AnnotationAgentProfile]
	if name == "" {
		return AgentResumeLaunch{}, codexappserver.ThreadPolicy{}, nil
	}
	_, loaded, spec, err := ResolveRecordedProfileWithPaths(paths, name, projectUID...)
	if err != nil {
		return AgentResumeLaunch{}, codexappserver.ThreadPolicy{}, err
	}
	policy, err := CodexThreadPolicy(spec.Permissions)
	if err != nil {
		return AgentResumeLaunch{}, codexappserver.ThreadPolicy{}, &ProfileResumeError{Name: name, Reason: profile.ReasonValueInvalid, Detail: err.Error()}
	}
	return AgentResumeLaunch{ProfileName: loaded.Name, ProfileDigest: loaded.Digest}, policy, nil
}

// NativeCodexResumeExecution shares exact-thread admission and metadata writes.
// Resume is invoked once; any uncertain RPC result is returned without retry.
type NativeCodexResumeExecution struct {
	Spelling, AgentUID, ConversationID string
	Route                              NativeCodexRoute
	Settings                           codexappserver.ThreadSettings
	ManagedBase                        *ManagedCodexBase
	Resume                             func(context.Context, NativeCodexRoute, metadata.AgentWorkspace, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	Plan                               func(NativeCodexRoute, metadata.AgentWorkspace, string, string, string) (string, []string, error)
	SafeFallback                       func(error) bool
	ThreadID                           string
}

func (n *NativeCodexResumeExecution) PrepareLaunch(ctx context.Context, reg *metadata.Registry, mut metadata.Mutator, pane metadata.Pane, activation SuperviseSpec, w metadata.AgentWorkspace, _ string, _ []string) (string, []string, error) {
	agent, _ := reg.Agent(n.AgentUID)
	if err := ValidateCodexNativeResumeRoute(agent.Status.SessionRef, n.Route); err != nil {
		return "", nil, NativeResumePreparationRefusal(n.Spelling, BindCodexResumeAgentRef(err, "uid:"+n.AgentUID))
	}
	ctx, cancel := context.WithTimeout(ctx, nativeCodexTimeout)
	defer cancel()
	prepared, err := n.Resume(ctx, n.Route, w, n.ConversationID, n.Settings)
	if err != nil {
		fallback := n.SafeFallback
		if fallback == nil {
			fallback = codexappserver.CanFallback
		}
		if fallback(err) {
			return "", nil, NativeResumePreparationRefusal(n.Spelling, err)
		}
		return "", nil, NativeLaunchError(n.Spelling, err)
	}
	if strings.TrimSpace(prepared.ThreadID) != strings.TrimSpace(n.ConversationID) {
		return "", nil, NativeLaunchError(n.Spelling, fmt.Errorf("%w: native resume returned a different thread", codexappserver.ErrProtocol))
	}
	if n.ManagedBase != nil {
		n.ManagedBase.Instructions.Accepted = true
		if err := n.ManagedBase.Record(reg, n.AgentUID, false); err != nil {
			return "", nil, err
		}
	}
	title, argv, err := n.Plan(n.Route, w, prepared.ThreadID, n.Settings.Model, n.Settings.Effort)
	if err != nil {
		return "", nil, NativeLaunchError(n.Spelling, err)
	}
	if _, err := mut.AdoptCodexResumeEndpoint(reg, n.AgentUID, n.Route.Endpoint); err != nil {
		return "", nil, MapMetadataError(err)
	}
	if _, err := mut.BindCodexActivation(reg, metadata.CodexActivationObservation{AgentUID: n.AgentUID, PaneUID: pane.Metadata.UID, Generation: activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID, Endpoint: n.Route.Endpoint}); err != nil {
		return "", nil, MapMetadataError(err)
	}
	n.ThreadID = prepared.ThreadID
	return title, argv, nil
}

// PrepareNativeCodexResume resolves settings before probing any provider.
func PrepareNativeCodexResume(ctx context.Context, spelling string, plan AgentResumePlan, paths func() (config.Paths, error), resolve func(context.Context, *metadata.AgentSessionRef, string) (NativeCodexRoute, error), launch func(NativeCodexRoute, metadata.AgentWorkspace, string, string, string) (string, []string, error)) (PreparedAgentResume, NativeCodexRoute, codexappserver.ThreadSettings, error) {
	request := plan.SettingsRequest()
	request.ManagedCodexBase = true
	settings, err := ResolveAgentSettingsWithPaths(paths, plan.Provider, plan.Annotations, request)
	var resumed AgentResumeLaunch
	var policy codexappserver.ThreadPolicy
	if err == nil {
		resumed, policy, err = NativeCodexResumeProfile(paths, settings.LaunchAnnotations(plan.Annotations), plan.ProjectUID)
	}
	if err != nil {
		return PreparedAgentResume{}, NativeCodexRoute{}, codexappserver.ThreadSettings{}, fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w", spelling, plan.AgentName, plan.Provider, plan.ConversationID, err)
	}
	service := GuidanceService{Paths: paths}
	managed, baseErr := service.PlanManagedCodexBase(plan.Project, plan.AgentUID, plan.Annotations, settings)
	if baseErr != nil {
		return PreparedAgentResume{}, NativeCodexRoute{}, codexappserver.ThreadSettings{}, baseErr
	}
	managed.Instructions.Shared = true
	settings.ManagedBase = managed
	route, err := resolve(ctx, plan.Ref, "uid:"+plan.AgentUID)
	if err != nil {
		return PreparedAgentResume{}, route, codexappserver.ThreadSettings{}, NativeResumePreparationRefusal(spelling, err)
	}
	effort, invalid, skipped := ClaudeResumeEffort("codex", settings.LaunchAnnotations(plan.LaunchAnnotations()))
	resumed.EffortInvalid, resumed.EffortSkipped = invalid, skipped
	native := codexappserver.ThreadSettings{ManagedInstructions: managed.Instructions, Model: settings.Model(plan.ModelOverride), Effort: effort, Policy: policy}
	title, argv, err := launch(route, plan.Workspace, plan.ConversationID, native.Model, native.Effort)
	if err != nil {
		return PreparedAgentResume{}, route, native, fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w", spelling, plan.AgentName, plan.Provider, plan.ConversationID, err)
	}
	out := PreparedAgentResume{Title: title, Argv: argv, Resumed: resumed, Settings: settings}
	for _, notice := range []string{resumed.EffortNotice(plan.AgentName), settings.Notice(plan.AgentName)} {
		if notice != "" {
			out.Notices = append(out.Notices, notice)
		}
	}
	return out, route, native, nil
}
