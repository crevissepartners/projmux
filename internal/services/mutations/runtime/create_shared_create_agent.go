package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	"maps"
)

type AgentCreateRequest struct {
	CreateSelection
	Provider, Cwd, CwdFrom, Placement string
	AddDirs, Payload                  []string
	CwdSet, DialogueReplyOnly         bool
	// NoEqualize skips evening the split against its anchor, for an anchor the
	// same operation retires.
	NoEqualize bool
	Labels     map[string]string
}
type AgentPreparation struct {
	Plan                  func(coremetadata.AgentWorkspace) (string, []string, error)
	Bind                  func(context.Context, string, coremetadata.AgentWorkspace, string) error
	Annotations           func(map[string]string) map[string]string
	Notices               func(string) []string
	DeveloperInstructions func(string) string
	ManagedBase           func(string) (*ManagedCodexBase, error)
	Policy                codexappserver.ThreadPolicy
	Model, Effort         string
	// Created, when set, records what the new Agent carries beyond its
	// annotations, in the same transaction, before its Pane is attached.
	Created func(*coremetadata.Registry, coremetadata.Mutator, string) error
}
type AgentCreatorRecord struct{ Annotations map[string]string }

func (r AgentCreatorRecord) ForAgent(reg *coremetadata.Registry, uid string) AgentCreatorRecord {
	if r.Annotations[coremetadata.AnnotationCreatorAgent] != uid {
		return r
	}
	if stored, ok := reg.Agent(uid); ok {
		for key := range r.Annotations {
			delete(stored.Metadata.Annotations, key)
		}
	}
	return AgentCreatorRecord{}
}
func (r AgentCreatorRecord) AnnotatePane(reg *coremetadata.Registry, pane coremetadata.Pane) coremetadata.Pane {
	if r.Annotations == nil || reg == nil {
		return pane
	}
	stored, ok := reg.Pane(pane.Metadata.UID)
	if !ok {
		return pane
	}
	if stored.Metadata.Annotations == nil {
		stored.Metadata.Annotations = map[string]string{}
	}
	maps.Copy(stored.Metadata.Annotations, r.Annotations)
	return stored.Clone()
}

type AgentWork struct {
	Target     PaneTarget
	WindowName string
	Agent      coremetadata.Agent
	Pane       coremetadata.Pane
	Activation SuperviseSpec
}
type AgentActivationTarget struct{ AgentUID, AgentName, PaneUID, PaneID, Generation string }
type NativeAgentBinding struct {
	Title            string
	Argv             []string
	ThreadID, TurnID string
	History          []sessionhistory.Record
}
type NativeAgentMaterialization struct {
	Work      AgentWork
	Workspace coremetadata.AgentWorkspace
	PaneID    string
	Binding   NativeAgentBinding
}
type NativeAgentCreation interface {
	Admit(context.Context, int) error
	Create(context.Context, *coremetadata.Registry, coremetadata.Mutator, AgentWork, coremetadata.AgentWorkspace, AgentPreparation) (NativeAgentBinding, error)
	Bind(context.Context, AgentWork, coremetadata.AgentWorkspace, string, NativeAgentBinding) error
}
type AgentCreateResult struct {
	ShellCreateResult
	ActivationTargets []AgentActivationTarget
	NativeHistory     []sessionhistory.Record
	NativeBindings    []NativeAgentMaterialization
}
type AgentCreation struct {
	Execution        *CreateExecution
	ResolveWorkspace func(coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
	Prepare          func(coremetadata.Registry, coremetadata.Project, coremetadata.AgentWorkspace) (AgentPreparation, error)
	Creator          func(context.Context, *coremetadata.Registry) (AgentCreatorRecord, error)
	Native           NativeAgentCreation
}

func (c *AgentCreation) Operation(scope CreateScope, flags AgentCreateRequest, spelling string, out *AgentCreateResult) CreateOperation {
	provider, labels := flags.Provider, flags.Labels
	return func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		out.NativeHistory = nil
		project, err := c.Execution.ResolveProject(*working, scope)
		if err != nil {
			return err
		}
		if err := c.Execution.RefuseMissingRoot(project); err != nil {
			return err
		}

		resolver := c.ResolveWorkspace
		if resolver == nil {
			return errors.New("create agent: workspace resolver is not configured")
		}
		workspace, err := resolver(*working, project, provider, flags.Cwd, flags.AddDirs)
		if err != nil {
			return err
		}

		prepared, err := c.Prepare(*working, project, workspace)
		if err != nil {
			return err
		}
		title, launchArgv, err := prepared.Plan(workspace)
		if err != nil {
			return err
		}

		// An explicit --cwd names the working directory outright, so it wins
		// over --cwd-from. Otherwise this CLI route follows its own argv:
		// only --cwd-from decides, and no config file is opened.
		source := "project"
		if !flags.CwdSet {
			source = flags.CwdFrom
		}

		// The declared <create, Agent> cell is this route's fan-out cardinality:
		// one Agent per resolved target Window, at least one overall. It is the
		// Agent row rather than the Window row because this route never resolves
		// an existing Agent -- rebinding a conversation is `agent resume` -- so
		// the only Agent count it can fix is the one it produces.
		plan, windows, err := c.Execution.ResolveSplitTargets(working, mutator, project, scope, flags.CreateSelection,
			selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindAgent}, spelling, operationID)
		if err != nil {
			return err
		}
		// The receipt quotes the planner, not the Agents this route produced,
		// so `create agent` and the three provider shortcuts report the same
		// selected set as `create pane` for the same argv -- including on the
		// native refusal below, which is decided on this very set.
		out.SelectedWindowUIDs = plan.SelectedWindowUIDs()
		// A Registry transaction can roll back several target Panes, but it
		// cannot delete app-server threads. Native identity therefore stays
		// atomic by being used only for the exact-one create shape.
		//
		// A default native create whose selector resolved several Windows is
		// refused here rather than fanned out onto the plain CLI lane: dropping
		// every target to a lane with no native turn control is exactly the
		// silent degradation this route no longer performs. The refusal lands
		// before the first Agent or Pane is allocated, so it costs zero threads,
		// zero Panes, and zero Registry mutations. `--interactive-only` remains
		// the way to ask for the plain-CLI fan-out on purpose.
		if c.Native != nil {
			if err := c.Native.Admit(ctx, len(plan.Targets)); err != nil {
				return err
			}
		}

		// Metadata phase. Every Agent and every managed Pane is allocated before
		// the first tmux call, so an explicit --name that collides in the target
		// root refuses with zero runtime objects created. The creator is
		// decided once, before the first allocation, for the whole fan-out.
		creator, err := c.Creator(ctx, working)
		if err != nil {
			return err
		}
		agents := make([]AgentWork, 0, len(plan.Targets))
		for _, target := range plan.Targets {
			window, ok := working.Window(target.WindowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, target.WindowUID)
			}
			agent, err := mutator.CreateAgent(working, target.WindowUID, coremetadata.CreateAgentOptions{
				// The explicit --name names only the Agent. The managed Pane
				// takes its name from the Agent's, one derivation later.
				Name:        flags.Name,
				Provider:    provider,
				Labels:      labels,
				Annotations: prepared.Annotations(creator.Annotations),
				Workspace:   workspace,
				Activation:  ActivationStateForPayload(flags.Payload),
				OperationID: operationID,
			})
			if err != nil {
				return MapMetadataError(err)
			}
			agentCreator := creator.ForAgent(working, agent.Metadata.UID)
			if prepared.Created != nil {
				if err := prepared.Created(working, mutator, agent.Metadata.UID); err != nil {
					return err
				}
			}
			out.Notices = append(out.Notices, prepared.Notices(agent.Metadata.Name)...)

			pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
				Name:   DerivedAgentPaneName(agent.Metadata.Name),
				CWD:    workspace.CWD,
				Labels: labels,
			}, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			pane = agentCreator.AnnotatePane(working, pane)
			activation, err := c.Execution.IssuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
			if err != nil {
				return err
			}
			activation.DialogueReplyOnly = flags.DialogueReplyOnly
			agents = append(agents, AgentWork{
				Target:     target,
				WindowName: window.Metadata.Name,
				Agent:      agent,
				Pane:       pane,
				Activation: activation,
			})
		}

		// Runtime phase.
		var sessionName string
		if len(windows) > 0 {
			sessionName, err = c.Execution.EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
			if err != nil {
				return err
			}
		}
		for i := range windows {
			if err := c.Execution.MaterializeWindow(ctx, working, mutator, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range agents {
			var anchorPaneID string
			if !work.Target.Virtual {
				if sessionName == "" {
					sessionName, err = c.Execution.EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
					if err != nil {
						return err
					}
				}
				anchorPaneID, err = c.Execution.EnsureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, work.Target)
				if err != nil {
					return err
				}
			}
			workWorkspace := workspace
			workTitle := title
			workLaunchArgv := launchArgv
			if source == "pane" && !work.Target.Virtual {
				dir, notice := c.Execution.SplitPaneLaunchDir(ctx, anchorPaneID, project.Spec.Root)
				if notice != "" {
					out.Notices = append(out.Notices, fmt.Sprintf("%s: window/%s: %s", spelling, work.WindowName, notice))
				}
				if dir != project.Spec.Root {
					// The Pane directory becomes this Agent's working directory
					// through the unchanged workspace validation, and its launch is
					// planned again for that workspace.
					workWorkspace, err = resolver(*working, project, provider, dir, flags.AddDirs)
					if err != nil {
						return err
					}
					workTitle, workLaunchArgv, err = prepared.Plan(workWorkspace)
					if err != nil {
						return err
					}
					if stored, ok := working.Agent(work.Agent.Metadata.UID); ok {
						stored.Spec.Workspace = workWorkspace
					}
					if stored, ok := working.Pane(work.Pane.Metadata.UID); ok {
						stored.Spec.CWD = workWorkspace.CWD
					}
					work.Pane.Spec.CWD = workWorkspace.CWD
				}
			}
			var nativeBinding NativeAgentBinding
			usedNative := c.Native != nil
			if usedNative {
				nativeBinding, err = c.Native.Create(ctx, working, mutator, work, workWorkspace, prepared)
				if err != nil {
					return err
				}
				workTitle, workLaunchArgv = nativeBinding.Title, nativeBinding.Argv
				out.NativeHistory = append(out.NativeHistory, nativeBinding.History...)
			}

			var paneID string
			if work.Target.Virtual {
				paneID, err = c.Execution.MaterializeVirtualPane(ctx, working, mutator, ledger, project, work.Target.WindowUID, work.Pane, work.Activation, workLaunchArgv)
			} else {
				paneID, err = c.Execution.Runtime.SplitPane(ctx, anchorPaneID, flags.Placement, workWorkspace.CWD, c.Execution.Runtime.SupervisedLaunch(ctx, work.Activation, workLaunchArgv), nil)
			}
			if paneID != "" && !work.Target.Virtual {
				// The supervised child now runs and will want the Registry lock
				// this transaction holds; create.outcome measures the rest of the hold.
				MarkSupervisedSpawn(ctx)
				if claimErr := c.Execution.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, paneID, work.Pane.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(err, claimErr)
				}
				if mirrorErr := c.Execution.Runtime.Mirror.MirrorPane(ctx, paneID, work.Pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
				ObserveActivationRuntime(working, mutator, work.Activation, paneID, c.Execution.Runtime.Warn)
			}
			if err != nil {
				return err
			}
			if !work.Target.Virtual && !flags.NoEqualize {
				c.Execution.Runtime.EqualizeSplitLayout(ctx, anchorPaneID, flags.Placement)
			}
			// The managed-pane options are what make this pane an agent pane to
			// the statusbar, the attention tracker, and the notification
			// pipeline. They are applied after the pane exists and before the
			// result is reported.
			if usedNative {
				if err := c.Native.Bind(ctx, work, workWorkspace, paneID, nativeBinding); err != nil {
					return err
				}
				out.NativeBindings = append(out.NativeBindings, NativeAgentMaterialization{Work: work, Workspace: workWorkspace, PaneID: paneID, Binding: nativeBinding})
			} else if err := prepared.Bind(ctx, paneID, workWorkspace, workTitle); err != nil {
				return fmt.Errorf("%s: bind Agent Pane %s presentation metadata: %w", spelling, paneID, err)
			}

			if len(flags.Payload) > 0 && !usedNative {
				out.ActivationTargets = append(out.ActivationTargets, AgentActivationTarget{
					AgentUID:   work.Agent.Metadata.UID,
					AgentName:  work.Agent.Metadata.Name,
					PaneUID:    work.Pane.Metadata.UID,
					PaneID:     paneID,
					Generation: work.Activation.Generation,
				})
			}
			out.Results = append(out.Results, ShellCreateResource{
				Kind: coremetadata.KindAgent,
				UID:  work.Agent.Metadata.UID,
				Name: work.Agent.Metadata.Name,
				// `-o pane-id` is the managed Pane's raw transport handle, which
				// is what the existing pane launchers and skill bridges consume.
				PaneID:      paneID,
				ProjectName: project.Metadata.Name,
				WindowName:  work.WindowName,
				WindowUID:   work.Target.WindowUID,
			})
		}
		return nil
	}
}

func DerivedAgentPaneName(agentName string) string {
	derived := agentName + "-pane"
	if coremetadata.ValidateName(derived) != nil {
		return ""
	}
	return derived
}

func ActivationStateForPayload(payload []string) coremetadata.AgentActivationState {
	if len(payload) == 0 {
		return coremetadata.ActivationNotRequested
	}
	return coremetadata.ActivationPending
}

func DeclaredAgentCreator(reg *coremetadata.Registry, uid, spelling, field string) (AgentCreatorRecord, error) {
	if uid == "" {
		return AgentCreatorRecord{}, nil
	}
	if _, ok := reg.Agent(uid); !ok {
		return AgentCreatorRecord{}, createUsage(fmt.Sprintf("%s %s uid:%s names no Agent in the Registry; nothing was created", spelling, field, uid))
	}
	return AgentCreatorRecord{Annotations: coremetadata.ExplicitCreatorAnnotations(uid)}, nil
}
