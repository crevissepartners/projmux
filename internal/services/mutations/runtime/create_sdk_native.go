package runtime

import (
	"context"
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
	"github.com/crevissepartners/projmux/internal/version"
	"maps"
	"os/exec"
	"time"
)

const nativeCodexTimeout = 25 * time.Second

type SDKNativeCreation struct {
	Engine *NativeCreationEngine
	*SDKNativeSession
}

// SDKNativeSession carries frozen routing for create and resume, without starting a thread.
type SDKNativeSession struct {
	Route       NativeCodexRoute
	Planner     *SDKAgentPlanner
	Environment *codexappserver.ProxyEnvironment
	Current     func(context.Context) (NativeCodexRoute, error)
	Open        func(context.Context, bool) (*codexappserver.Client, error)
}

func (p *SDKAgentPlanner) PrepareNative(ctx context.Context) (*SDKNativeCreation, error) {
	if !NativeCodexFreshCreateRequired(p.Provider, p.Settings.InteractiveOnly, p.Settings.Payload, "", "") {
		return nil, nil
	}
	base, err := p.NativeSession()
	refusal := func(err error) error {
		env := &codexappserver.ProxyEnvironment{Executable: p.Command.LookupBinary("codex"), HomeDir: p.HomeDir, Values: maps.Clone(p.Environment)}
		return &AgentCreateRefusal{Reason: "create-native-codex-unavailable", Cause: err, Message: NativeCreatePreparationRefusal("create agent", err, env.InstallCapability()).Error()}
	}
	if err != nil {
		return nil, refusal(err)
	}
	prompt, exact := NativePrompt(p.Settings.Payload)
	if !exact {
		return nil, refusal(&NativeCodexRouteError{Reason: "unsupported-create-shape"})
	}
	nativeCtx, cancel := context.WithTimeout(ctx, nativeCodexTimeout)
	route, err := base.Current(nativeCtx)
	cancel()
	if err != nil {
		return nil, refusal(err)
	}
	base.Route = route
	n := &SDKNativeCreation{SDKNativeSession: base}
	current, open := base.Current, base.Open
	n.Engine = &NativeCreationEngine{Spelling: "create agent", Endpoint: route.Endpoint, Prompt: prompt, PreparationRefusal: refusal, LaunchError: func(err error) error { return NativeLaunchError("create agent", err) }, FanOutRefusal: func(count int) error { return NativeFanOutRefusal("create agent", count) }, SafeFallback: codexappserver.CanFallback,
		AdmitEndpoint: func(ctx context.Context) (metadata.CodexEndpointRef, error) {
			ctx, cancel := context.WithTimeout(ctx, nativeCodexTimeout)
			defer cancel()
			r, err := current(ctx)
			return r.Endpoint, err
		},
		CreateThread: func(ctx context.Context, input NativeCodexCreateInput) (codexappserver.ThreadBinding, error) {
			ctx, cancel := context.WithTimeout(ctx, nativeCodexTimeout)
			defer cancel()
			return CreateNativeCodexThread(ctx, input, func(ctx context.Context, experimental bool) (NativeCodexThreadClient, error) {
				return open(ctx, experimental)
			}, func(ctx context.Context, w metadata.AgentWorkspace, id string) error {
				_, err := (codexappserver.DurableResumeBarrier{Open: func(ctx context.Context) (codexappserver.DurableResumeClient, error) { return open(ctx, true) }}).Await(ctx, id, w.CWD, w.AdditionalWritableRoots)
				return err
			})
		},
		Plan: func(w metadata.AgentWorkspace, id, model, effort string) (string, []string, error) {
			return base.Plan(route, w, id, model, effort)
		}, BindPane: func(ctx context.Context, pane string, w metadata.AgentWorkspace, b NativeAgentBinding) error {
			return p.Launch.BindAgentPaneOnRoute(ctx, p.Runtime.Runner, AgentPaneBinding{PaneID: pane, Provider: "codex", ContextDir: w.CWD, Title: b.Title, ConversationID: b.ThreadID, ResumeSource: "app-server", ThreadID: b.ThreadID, NativeCodex: true})
		}}
	return n, nil
}
func (n *SDKNativeCreation) Observe(out AgentCreateResult) {
	n.SDKNativeSession.Observe(out)
}
func (n *SDKNativeSession) Observe(out AgentCreateResult) {
	p := n.Planner
	for _, row := range out.NativeHistory {
		_ = sessionhistory.Append(p.Paths.StateDir, row)
	}
	for _, target := range out.NativeBindings {
		n.observeIdentity(NativeObserverIdentity{AgentUID: target.Work.Agent.Metadata.UID, PaneUID: target.Work.Pane.Metadata.UID, RuntimeID: target.PaneID, Generation: target.Work.Activation.Generation, ThreadID: target.Binding.ThreadID})
	}
}
func (n *SDKNativeSession) observeIdentity(id NativeObserverIdentity) {
	p := n.Planner
	authority := NativeObserverAuthority{Identity: id, Runner: p.Runtime.Runner, Load: p.Store.Load, Acquire: func() (func(), error) {
		path, err := agentcontrol.FencePath(p.Paths.StateDir, id.PaneUID)
		if err != nil {
			return nil, err
		}
		return AcquireNativeAuthorityFenceAt(path)
	}}
	starter := NativeObserverStarter{BindingCurrent: authority.BindingCurrent, SetAuthority: func(source, epoch, reason string) error { return authority.Set("", source, epoch, reason) }, SetAuthorityFrom: func(from, source, epoch, reason string) (bool, error) {
		err := authority.Set(from, source, epoch, reason)
		if errors.Is(err, ErrNativeAuthorityNotFrom) {
			return false, nil
		}
		return err == nil, err
	}, Launch: func() NativeObserverStartup {
		args := []string{"internal", "agent-hook", "ingest", "codex-broker-watch", "--agent-uid", id.AgentUID, "--pane-uid", id.PaneUID, "--pane", id.RuntimeID, "--generation", id.Generation, "--thread", id.ThreadID, "--state-domain", n.Route.Endpoint.StateDomainID, "--endpoint-generation", n.Route.Endpoint.EndpointGenerationID, "--endpoint-state", string(n.Route.State), "--tui-executable", n.Route.TUIExecutable, "--endpoint-default", "true", "--tmux-socket-path", p.Runtime.Target.Value, "--config-dir", p.Paths.ConfigDir, "--state-dir", p.Paths.StateDir, "--registry-path", store.PathFor(p.Paths.StateDir), "--projmux-executable", p.Executable}
		// #nosec G204 -- PrepareNative validates this explicit executable; argv is the fixed hidden observer route and exact binding.
		cmd := tmuxexec.Command(p.Executable, args...)
		values := maps.Clone(n.Environment.Values)
		values["HOME"] = p.HomeDir
		values["PROJMUX_INTERNAL_CODEX_OBSERVER_STARTUP"] = "1"
		for k, v := range values {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return LaunchNativeObserverProcess(cmd, NativeObserverTimeout, false)
	}}
	starter.Start(context.Background())
}

func (p *SDKAgentPlanner) NativeSession() (*SDKNativeSession, error) {
	env := &codexappserver.ProxyEnvironment{Executable: p.Command.LookupBinary("codex"), HomeDir: p.HomeDir, Values: maps.Clone(p.Environment)}
	n := &SDKNativeSession{Planner: p, Environment: env}
	paths := NativeObserverPaths{Paths: p.Paths, RegistryPath: store.PathFor(p.Paths.StateDir), Executable: p.Executable}
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if _, err := ValidateNativeObserverExecutable(p.Executable); err != nil {
		return nil, err
	}
	n.Current = func(ctx context.Context) (NativeCodexRoute, error) {
		return CurrentNativeCodexRoute(ctx, func(ctx context.Context) codexappserver.Health {
			return codexappserver.ProbeDefaultProxyWithEnvironment(ctx, nativeCodexTimeout, version.String(), true, env)
		}, func(string) (string, error) {
			if env.Executable == "" {
				return "", exec.ErrNotFound
			}
			return env.Executable, nil
		}, func(k string) string { return env.Values[k] }, func() (string, error) { return env.HomeDir, nil })
	}
	n.Open = func(ctx context.Context, experimental bool) (*codexappserver.Client, error) {
		client, _, err := codexappserver.AttachDefaultEndpointWithEnvironment(ctx, version.String(), codexappserver.AttachOptions{Timeout: nativeCodexTimeout, ExperimentalAPI: experimental}, env)
		if err != nil {
			return nil, err
		}
		return client, nil
	}
	return n, nil
}

func (n *SDKNativeSession) Plan(route NativeCodexRoute, w metadata.AgentWorkspace, id, model, effort string) (string, []string, error) {
	p := n.Planner
	return PlanNativeCodexAttachment(route, w, id, model, effort, func(cwd string, argv []string, prepend string) (string, []string, error) {
		plan, err := p.Command.PlanLaunch("codex", cwd, nil, argv, prepend)
		if err != nil {
			return "", nil, err
		}
		command := "export HOME=" + ShellQuote(p.HomeDir) + " PATH=" + ShellQuote(n.Environment.Values["PATH"]) + " CODEX_HOME=" + ShellQuote(n.Environment.Values["CODEX_HOME"]) + "; " + plan.CommandArgs[2]
		return plan.Title, []string{plan.CommandArgs[0], plan.CommandArgs[1], command}, nil
	})
}
func (n *SDKNativeSession) Resolve(ctx context.Context, endpoint metadata.CodexEndpointRef) (NativeCodexRoute, error) {
	return ResolveNativeCodexEndpoint(ctx, endpoint, n.Planner.Paths.StateDir, n.Current, nil)
}
func (n *SDKNativeSession) Resume(ctx context.Context, route NativeCodexRoute, w metadata.AgentWorkspace, id string, settings codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
	if !route.Valid() {
		return codexappserver.ThreadBinding{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	client, err := n.Open(ctx, true)
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	defer client.Close()
	return client.ResumeThreadWithSettings(ctx, id, w.CWD, w.AdditionalWritableRoots, settings)
}

func (n *SDKNativeSession) ObserveResume(agentUID string, out AgentResumeResult, threadID string) {
	n.observeIdentity(NativeObserverIdentity{AgentUID: agentUID, PaneUID: out.PaneUID, RuntimeID: out.PaneID, Generation: out.Generation, ThreadID: threadID})
}
