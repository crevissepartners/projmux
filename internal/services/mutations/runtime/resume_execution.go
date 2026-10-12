package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type AgentResumeExecution struct {
	Execution           *CreateExecution
	ResolveWorkspace    func(string, coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
	Record              func(*coremetadata.Registry, coremetadata.Mutator, string) error
	Layered, Reserved   bool
	Retire              func(*coremetadata.Registry, coremetadata.Mutator) error
	ValidateReservation func(*coremetadata.Registry, *coremetadata.Agent) error
	Handoff             func(context.Context, *coremetadata.Registry, coremetadata.Mutator, coremetadata.Agent, AgentResumePlan) (string, string)
	Attach              func(*coremetadata.Registry, coremetadata.Mutator) (coremetadata.Pane, error)
	BeforeRuntime       func(*coremetadata.Registry, coremetadata.Mutator) error
	Activation          func() SuperviseSpec
	PrepareLaunch       func(context.Context, *coremetadata.Registry, coremetadata.Mutator, coremetadata.Pane, SuperviseSpec, coremetadata.AgentWorkspace, string, []string) (string, []string, error)
	MaterializeVirtual  func(context.Context, *coremetadata.Registry, coremetadata.Mutator, *RuntimeLedger, coremetadata.Project, string, coremetadata.Pane, SuperviseSpec, []string) (string, error)
	Bind                func(context.Context, *coremetadata.Registry, coremetadata.Pane, SuperviseSpec, string, string, string) error
}
type AgentResumeResult struct{ PaneID, PaneUID, Generation, NameReason string }

func (r *AgentResumeExecution) Operation(spelling string, plan AgentResumePlan, title string, launchArgv []string, out *AgentResumeResult) CreateOperation {
	contextDir := plan.Workspace.CWD
	if contextDir == "" {
		contextDir = plan.ProjectRoot
	}
	return func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		var err error
		out.NameReason = ""
		agent, ok := working.Agent(plan.AgentUID)
		if !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.AgentUID)
		}
		if r.Retire != nil {
			if err := r.Retire(working, mutator); err != nil {
				return err
			}
			agent, _ = working.Agent(plan.AgentUID)
		}
		// The preflight ran against a read-only snapshot and the reconciler has
		// since run inside this transaction. Re-checking the two facts the plan
		// rests on -- the Agent is still resumable, and it is still the same
		// conversation -- is what stops a concurrent hook or transition from
		// turning this into a rebind of something else.
		if r.ValidateReservation != nil {
			if err := r.ValidateReservation(working, agent); err != nil {
				return err
			}
		} else if err := RequireResumablePhase(spelling, agent); err != nil {
			return err
		}
		if !agent.Status.SessionRef.SameConversation(plan.Ref) {
			return fmt.Errorf(
				"%s: agent/%s now points at a different conversation than the one this resume planned; re-run it",
				spelling, plan.AgentName)
		}
		project, ok := working.Project(plan.ProjectUID)
		if !ok {
			return fmt.Errorf("%s: project %q disappeared before the rebind ran", spelling, plan.ProjectUID)
		}
		resolver := r.ResolveWorkspace
		if resolver == nil {
			resolver = ResolveAgentWorkspaceFor
		}
		workspace, err := resolver(spelling, *working, *project, plan.Provider, agent.Spec.Workspace.CWD, agent.Spec.Workspace.AdditionalWritableRoots)
		if err != nil {
			return err
		}
		if workspace.CWD != plan.Workspace.CWD || !slices.Equal(workspace.AdditionalWritableRoots, plan.Workspace.AdditionalWritableRoots) {
			return fmt.Errorf("%s: agent/%s workspace changed after preflight; re-run it", spelling, plan.AgentName)
		}
		// Persist the normalized effective workspace for legacy Agents whose
		// pre-Phase6 spec was empty, before any runtime object is created.
		agent.Spec.Workspace = workspace
		// The layered settings are recorded with their sources in the same
		// transaction, before any runtime object exists: the profile a
		// relaunch switched to, the effort, the model when it was passed, and
		// the instructions with the snapshot mode off when their content
		// changed. A failure anywhere later rolls the whole transaction back,
		// so a resume that does not launch records neither value nor source.
		if err := r.Record(working, mutator, plan.AgentUID); err != nil {
			return err
		}

		// The reply-only activation is recorded, so every later resume of
		// the Agent launches it again. An Agent that records it already is
		// left as it was.
		if plan.DialogueReplyOnly {
			if _, err := mutator.SetAgentDialogueReplyOnly(working, plan.AgentUID); err != nil {
				return MapMetadataError(err)
			}
		}
		// An unlayered launch records only its overrides, as before.
		if plan.EffortOverride != "" && !r.Layered {
			if _, err := mutator.SetAgentEffort(working, plan.AgentUID, plan.EffortOverride, plan.SettingSource()); err != nil {
				return MapMetadataError(err)
			}
		}
		if plan.ModelOverride != "" && !r.Layered {
			if _, err := mutator.SetAgentModel(working, plan.AgentUID, plan.ModelOverride, plan.SettingSource()); err != nil {
				return MapMetadataError(err)
			}
		}

		// Name handoff. The new managed Pane carries the non-automatic name of
		// the Agent's old Pane row that selectAgentPaneNameHandoff picks, and
		// that row is released first so the name has exactly one holder; with no
		// such row and no restart, it gets the `<agent>-pane` name `create agent`
		// gives. A name that cannot be carried is disclosed and never refuses
		// the resume.
		paneName, reason := "", ""
		if !r.Reserved {
			paneName, reason = r.Handoff(ctx, working, mutator, *agent, plan)
		}
		out.NameReason = reason
		if _, ok := working.Agent(plan.AgentUID); !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.AgentUID)
		}

		// Metadata phase. AttachAgentPane creates the managed Pane owned by this
		// existing Agent and moves it Offline/Failed -> Running through the
		// closed transition table. No Agent is created and no name is allocated
		// for one, so the uid and metadata.name are structurally untouchable
		// here.
		var pane coremetadata.Pane
		var attachReason string
		if r.Attach != nil {
			pane, err = r.Attach(working, mutator)
		} else {
			pane, attachReason, err = AttachAgentPaneWithName(working, mutator, plan.AgentUID, contextDir, paneName, operationID)
		}
		if err != nil {
			return MapMetadataError(err)
		}
		if attachReason != "" {
			out.NameReason = attachReason
		}

		virtual := plan.VirtualWindow
		if r.BeforeRuntime != nil {
			if err := r.BeforeRuntime(working, mutator); err != nil {
				return err
			}
		}
		// Runtime phase, on the create routes' own materializer.
		var sessionName, anchorPaneID string
		if !virtual {
			sessionName, err = r.Execution.EnsureProjectRuntime(ctx, working, mutator, *project, operationID, ledger)
			if err != nil {
				return err
			}
			anchorPaneID, err = r.Execution.EnsureAnchorPane(ctx, working, mutator, ledger, *project, sessionName, operationID, PaneTarget{
				WindowUID:    plan.WindowUID,
				AnchorUID:    plan.AnchorUID,
				StoredAnchor: true,
			})
			if err != nil {
				return err
			}
		}
		// A resume is a new materialization of the same Agent, so it issues a
		// fresh generation. That is what makes a late receipt from the process
		// this resume replaced recognizable as stale instead of being applied
		// to the Pane the operator is now looking at.
		var activation SuperviseSpec
		if r.Activation != nil {
			activation = r.Activation()
		} else {
			activation, err = r.Execution.IssuePaneActivation(working, mutator, pane.Metadata.UID, plan.AgentUID, operationID)
		}
		if err != nil {
			return err
		}
		activation.DialogueReplyOnly = plan.DialogueReplyOnly

		workTitle := title
		workLaunchArgv := launchArgv
		if r.PrepareLaunch != nil {
			workTitle, workLaunchArgv, err = r.PrepareLaunch(ctx, working, mutator, pane, activation, workspace, title, launchArgv)
			if err != nil {
				return err
			}
		}

		if virtual {
			pane.Spec.CWD = contextDir
		}
		var paneID string
		if virtual {
			paneID, err = r.MaterializeVirtual(ctx, working, mutator, ledger, *project, plan.WindowUID, pane, activation, workLaunchArgv)
		} else {
			paneID, err = r.Execution.Runtime.SplitPane(ctx, anchorPaneID, "right", contextDir, r.Execution.Runtime.SupervisedLaunch(ctx, activation, workLaunchArgv), nil)
		}
		if paneID != "" && !virtual {
			// The supervised child now runs and will want the Registry lock
			// this transaction holds; create.outcome measures the rest of the hold.
			MarkSupervisedSpawn(ctx)
			if claimErr := r.Execution.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := r.Execution.Runtime.Mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			ObserveActivationRuntime(working, mutator, activation, paneID, r.Execution.Runtime.Warn)
		}
		if err != nil {
			return err
		}
		if err := r.Bind(ctx, working, pane, activation, paneID, contextDir, workTitle); err != nil {
			return err
		}
		out.PaneID = paneID
		out.PaneUID = pane.Metadata.UID
		out.Generation = activation.Generation

		return nil

	}
}
