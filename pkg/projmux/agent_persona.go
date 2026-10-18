package projmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodePersonaNameInvalid         Code = "persona-name-invalid"
	CodePersonaNotFound            Code = "persona-not-found"
	CodePersonaTooLarge            Code = "persona-too-large"
	CodePersonaProviderUnsupported Code = "persona-provider-unsupported"
	CodePersonaAgentBusy           Code = "persona-agent-busy"
	CodePersonaNoConversation      Code = "persona-no-conversation"
	CodePersonaUnavailable         Code = "persona-unavailable"
	CodeProcessRelaunchUnsupported Code = "process-relaunch-unsupported"
)

// AttachAgentPersona records a named persona with attach provenance and resumes
// the exact Claude Agent on its existing conversation. It supports tmux Agents.
func (c *Client) AttachAgentPersona(ctx context.Context, req core.AttachAgentPersonaRequest) (core.AgentPersonaResult, error) {
	return c.agentPersona(ctx, runtime.AgentPersonaRequest{Action: "attach", Persona: req.Persona, DryRun: req.DryRun, Confirm: req.Confirm, SocketPath: req.SocketPath}, req.Ref, req.Project, req.Window, tmux.ExecRunner{})
}

// DetachAgentPersona clears the persona with attach provenance, disables the
// system prompt snapshot, and resumes the same Claude conversation.
func (c *Client) DetachAgentPersona(ctx context.Context, req core.DetachAgentPersonaRequest) (core.AgentPersonaResult, error) {
	return c.agentPersona(ctx, runtime.AgentPersonaRequest{Action: "detach", DryRun: req.DryRun, Confirm: req.Confirm, SocketPath: req.SocketPath}, req.Ref, req.Project, req.Window, tmux.ExecRunner{})
}
func (c *Client) agentPersona(ctx context.Context, q runtime.AgentPersonaRequest, ref, project, window string, runner runtime.TmuxCommandRunner) (core.AgentPersonaResult, error) {
	var out core.AgentPersonaResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "persona request Ref must name one Agent"}
	}
	if q.Action == "attach" {
		if err := persona.ValidateName(q.Persona); err != nil {
			return out, personaSDKRefusal(err)
		}
	}
	query, err := mutationQuery(core.KindAgent, ref, project, window, "AgentPersonaRequest")
	if err != nil {
		return out, resourceRefusal(err)
	}
	if query.Agents[0].UID == "" && project == "" && window == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "persona request Ref requires uid: or explicit Project/Window scope"}
	}
	if q.SocketPath == "" {
		q.SocketPath = c.runtimeSocketPath
	}
	if q.SocketPath != "" && !filepath.IsAbs(q.SocketPath) {
		return out, &Refusal{Code: CodeRuntimeRequestInvalid, message: "persona request SocketPath must be absolute"}
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbResume, Kind: metadata.KindAgent}, query, resolution); err != nil {
		return out, resourceRefusal(err)
	}
	target, _ := reg.Agent(resolution.Matches[0].UID)
	// The web persona route has never admitted a process relaunch.
	for _, p := range reg.PanesOf(target.Metadata.UID) {
		if p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
			return out, &Refusal{Code: CodeProcessRelaunchUnsupported, message: "persona attach/detach does not support process Agents"}
		}
	}
	q.Spelling = "agent persona " + q.Action
	q.Noun = "persona"
	var planned runtime.AgentRestartPlan
	var resumed core.ResumeAgentResult
	admitted := context.WithoutCancel(ctx)
	result, err := runtime.RunAgentPersona(runtime.AgentPersonaDependencies{
		Request: q, Registry: reg, Target: target.Clone(), Interaction: target.EffectiveInteraction(time.Now()).Kind,
		OpenStore: func() (persona.Store, error) { return persona.NewDefaultStore(c.paths), nil },
		Refuse: func(reason, message string, cause error) error {
			return &Refusal{Code: Code(reason), message: message, cause: cause}
		},
		Plan: func(p *runtime.AgentRestartPlan, settings runtime.AgentSettingsRequest, refuse func(string, string) error) error {
			err := p.Plan(settings, runtime.AgentRestartPlanning{Registry: reg, Tokens: runtime.PersonaTokens("persona"), Refuse: refuse,
				Predict: func(pane string) (runtime.AgentResumePlan, error) {
					predicted := reg.Clone()
					if pane != "" {
						if err := mutations.NewStore("").Mutator().DeletePane(&predicted, pane); err != nil {
							return runtime.AgentResumePlan{}, err
						}
					}
					a, ok := predicted.Agent(target.Metadata.UID)
					if !ok {
						return runtime.AgentResumePlan{}, errors.New("target Agent disappeared")
					}
					return personaResumePlan(predicted, a)
				},
				ResolveSettings: func(_ runtime.AgentResumePlan, q runtime.AgentSettingsRequest) (runtime.AgentSettingsLaunch, error) {
					return runtime.ResolveAgentSettings(c.paths, p.Provider, target.Metadata.Annotations, q)
				},
			})
			planned = *p
			return err
		},
		SetPersona: func(a metadata.Agent, want metadata.AgentPersonaAnnotations) error {
			_, err := mutations.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *metadata.Registry) error {
				return runtime.SetPlannedAgentPersona(q.Spelling, reg, mutations.NewStore("").Mutator(), a, want)
			})
			return mutations.Classify(err)
		},
		Execution: runtime.AgentRestartExecution{
			ValidateTransport: func() error { _, err := runtime.ResolveDeleteTarget(q.Spelling, "", q.SocketPath, nil); return err },
			BeforeStop: func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !filepath.IsAbs(c.projmuxExecutable) {
					return &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
				}
				info, err := os.Stat(c.projmuxExecutable)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
					return &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: err}
				}
				return nil
			},
			Stop: func() error {
				_, err := c.deleteWithRunner(admitted, core.DeleteRequest{Kind: core.KindPane, Ref: "uid:" + planned.PaneUID, SocketPath: q.SocketPath, Confirm: true}, runner)
				return err
			},
			Observe: func() (runtime.AgentRestartLiveness, error) { return c.relaunchLiveness(planned, q.SocketPath, runner) },
			Resume: func(stopped runtime.StoppedAgentPane) error {
				fresh, err := resources.Load(c.paths)
				if err != nil {
					return err
				}
				a, ok := fresh.Agent(target.Metadata.UID)
				if !ok {
					return errors.New("target Agent disappeared before resume")
				}
				if err := runtime.RequireResumablePhase("agent resume", a); err != nil {
					return err
				}
				plan, err := personaResumePlan(fresh, a)
				if err != nil {
					return err
				}
				plan.StoppedPane = stopped
				resumed, err = c.resumeAgentPlan(admitted, plan, q.SocketPath, runner)
				return err
			},
			NewPane: func() string { return resumed.Agent.Status.PaneRef },
		},
	})
	if copyErr := unregisterCopy(result, &out); copyErr != nil {
		return out, copyErr
	}
	out.Notices = append(result.Notices, resumed.Notices...)
	out.Recovery = result.Recovery
	if err != nil {
		return out, personaSDKRefusal(err)
	}
	return out, nil
}
func personaResumePlan(reg metadata.Registry, a *metadata.Agent) (runtime.AgentResumePlan, error) {
	plan, err := runtime.BuildAgentResumePlan("agent resume", reg, a)
	if err != nil {
		return plan, err
	}
	project, _ := reg.Project(plan.ProjectUID)
	plan.Workspace, err = runtime.ResolveAgentWorkspaceFor("agent resume", reg, *project, plan.Provider, plan.Workspace.CWD, plan.Workspace.AdditionalWritableRoots)
	return plan, err
}
func personaSDKRefusal(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var r *Refusal
	if errors.As(err, &r) {
		return err
	}
	if reason := persona.ReasonOf(err); reason != "" {
		return &Refusal{Code: Code(reason), message: err.Error(), cause: err}
	}
	var f *mutations.Failure
	if errors.As(err, &f) {
		return &Refusal{Code: Code(f.Code), message: err.Error(), cause: err}
	}
	return &Refusal{Code: CodePersonaUnavailable, message: fmt.Sprint(err), cause: err}
}
