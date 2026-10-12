package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"strings"
	"time"
)

const (
	AgentActivationStartupDeadline         = 35 * time.Second
	AgentActivationAcknowledgementDeadline = 12 * time.Second
)

type AgentActivationService struct {
	Store  *CreateStore
	Runner TmuxCommandRunner
	Await  func(context.Context, TmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error)
}

func (c *AgentActivationService) Confirm(targets []AgentActivationTarget) error {
	var diagnostics []error
	for _, target := range targets {
		acknowledged, _, err := c.Await(context.Background(), c.Runner, target.PaneID,
			AgentActivationStartupDeadline, AgentActivationAcknowledgementDeadline)
		source := string(coremetadata.InteractionSourceProviderHook)
		state := coremetadata.ActivationAcknowledged
		reason := ""
		if err != nil || !acknowledged {
			state = coremetadata.ActivationUnconfirmed
			if err != nil {
				reason = coremetadata.ActivationReasonFailed
			} else {
				reason = coremetadata.ActivationReasonTimedOut
			}
		}
		var committed coremetadata.Agent
		if _, updateErr := c.Store.Update(func(registry *coremetadata.Registry) error {
			check := CheckAgentActivationBinding(*registry, target.PaneUID, target.PaneID,
				&AgentActivationBindingExpectation{AgentUID: target.AgentUID, Generation: target.Generation})
			if !check.Bound() {
				return &AgentActivationBindingError{
					Command: "create agent", Stage: AgentBindingStageBeforeRecording,
					PaneUID: target.PaneUID, PaneID: target.PaneID, Check: check,
				}
			}
			updated, setErr := c.Store.Mutator().SetAgentActivation(registry, target.AgentUID, state, source, reason)
			committed = updated.Clone()
			return setErr
		}); updateErr != nil {
			diagnostics = append(diagnostics, MapMetadataError(updateErr))
			continue
		}
		// A provider hook may commit after Await's final read but before the
		// timeout writer takes the Registry lock. SetAgentActivation is monotonic,
		// so inspect the committed authority instead of the stale local decision.
		if committed.Status.Activation.State == coremetadata.ActivationUnconfirmed {
			diagnostics = append(diagnostics, errors.New(ActivationUnconfirmedDiagnostic(target, reason)))
		}
	}
	if err := errors.Join(diagnostics...); err != nil {
		return &ActivationConfirmationFailure{Err: err}
	}
	return nil
}

func ActivationUnconfirmedDiagnosticSteps(target AgentActivationTarget) []string {
	return []string{
		fmt.Sprintf("the Agent and its managed Pane %s were created and are still live; nothing was rolled back", target.PaneID),
		fmt.Sprintf("re-read the committed activation with `projmux describe agent uid:%s` first: a provider hook that arrived after the bound refines it to acknowledged", target.AgentUID),
		fmt.Sprintf("if it is still unconfirmed, look at Pane %s (`tmux capture-pane -p -t %s`) or the provider transcript for the initial task, and retry it through the provider when it never arrived", target.PaneID, target.PaneID),
		fmt.Sprintf("only when neither read shows activation evidence, clean up with `projmux delete agent uid:%s --yes`", target.AgentUID),
	}
}

func ActivationUnconfirmedDiagnostic(target AgentActivationTarget, reason string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "create agent: agent/%s uid:%s has live managed Pane %s but initial task activation was not confirmed: %s",
		target.AgentName, target.AgentUID, target.PaneID, reason)
	for _, step := range ActivationUnconfirmedDiagnosticSteps(target) {
		builder.WriteString("; ")
		builder.WriteString(step)
	}
	return builder.String()
}

type ActivationConfirmationFailure struct{ Err error }

func (e *ActivationConfirmationFailure) Error() string { return e.Err.Error() }
func (e *ActivationConfirmationFailure) Unwrap() error { return e.Err }
