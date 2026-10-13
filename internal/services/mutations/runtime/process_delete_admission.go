package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

type ProcessDeleteAdmissionKey struct{}
type ProcessDeleteAdmission struct {
	Kind      coremetadata.Kind
	UIDs      []string
	Signature string
	// Admitted is carried only by the same in-process delete operation after its
	// first owner accepted Stop. It never comes from a user-facing bypass flag.
	Admitted bool
}

func AdmitProcessDeleteStop(path string, action, prompt string, authority processhost.Authority, apply func() error) error {
	if action != "delete-stop" {
		return apply()
	}
	var admission ProcessDeleteAdmission
	if err := json.Unmarshal([]byte(prompt), &admission); err != nil {
		return processhost.ErrStale
	}
	if len(admission.UIDs) == 0 {
		return processhost.ErrStale
	}
	return intmetadata.NewStore(path).WithAdmissionBarrier(func(reg coremetadata.Registry) error {
		binding := processMetadataBinding(authority.Binding)
		if _, _, ok := reg.CurrentProcessActivation(binding); !ok {
			// Starting hosts have a durable reservation before SessionStart
			// publishes a complete activation. Keep their existing Stop admission.
			pane, paneOK := reg.Pane(binding.PaneUID)
			agent, agentOK := reg.Agent(binding.AgentUID)
			if !paneOK || !agentOK || !pane.Status.Activation.IsZero() || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess ||
				pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != binding ||
				pane.Metadata.OwnerUID() != binding.AgentUID || agent.Status.PaneRef != binding.PaneUID ||
				agent.Metadata.OwnerUID() != binding.WindowUID || (agent.Status.Phase != coremetadata.PhaseRunning && agent.Status.Phase != coremetadata.PhaseOffline) {
				return processhost.ErrStale
			}
		}
		resolution := selector.Resolution{Kind: admission.Kind}
		for _, uid := range admission.UIDs {
			resolution.Matches = append(resolution.Matches, selector.Match{UID: uid})
		}
		plan := mutations.BuildDeletePlan(reg, admission.Kind, resolution)
		if err := mutations.ValidateDeleteProtection(reg, plan); !admission.Admitted && err != nil {
			return err
		}
		if plan.Signature() != admission.Signature {
			return processhost.ErrStale
		}
		// The requested host must be one of the Agents this exact plan removes.
		included := false
		for _, t := range plan.Targets {
			if plan.Kind == coremetadata.KindAgent && t.Match.UID == authority.Binding.Agent {
				included = true
			}
			for _, d := range t.Descendants {
				if d.Kind == coremetadata.KindAgent && d.UID == authority.Binding.Agent {
					included = true
				}
			}
		}
		if !included {
			return fmt.Errorf("process delete admission does not include this Agent: %w", processhost.ErrStale)
		}
		return apply()
	})
}

func processMetadataBinding(b processhost.Binding) coremetadata.ProcessBinding {
	return coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}
}

func processHostBinding(b coremetadata.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}
func mustProcessDeleteAdmission(a ProcessDeleteAdmission) string {
	b, _ := json.Marshal(a)
	return string(b)
}
func CallProcessAgentDeleteStop(ctx context.Context, path string, reg coremetadata.Registry, agent coremetadata.Agent, a ProcessDeleteAdmission) error {
	err := callProcessAgentStopAction(ctx, path, reg, agent, "delete-stop", mustProcessDeleteAdmission(a))
	if errors.Is(err, processhost.ErrClosed) {
		return nil
	}
	return err
}
