package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const ProjectStopAgentsStillRunning = "project-stop-agents-still-running"

type ProjectProcessStopTarget struct {
	Agent metadata.Agent
	Pane  metadata.Pane
}

func RunningProjectProcessAgents(reg metadata.Registry, projectUID string) []ProjectProcessStopTarget {
	windows := make(map[string]bool)
	for _, window := range reg.WindowsOf(projectUID) {
		windows[window.Metadata.UID] = true
	}
	var targets []ProjectProcessStopTarget
	for _, agent := range reg.Agents {
		if agent.Status.Phase != metadata.PhaseRunning || !windows[agent.Metadata.OwnerUID()] {
			continue
		}
		pane, ok := reg.Pane(agent.Status.PaneRef)
		if ok && IsNonTmuxRuntimePane(*pane) {
			targets = append(targets, ProjectProcessStopTarget{agent.Clone(), pane.Clone()})
		}
	}
	return targets
}

// ProjectProcessStopFailure keeps the CLI display stable while exposing the
// exact target and the C1 typed cause to embedding callers through errors.As.
type ProjectProcessStopFailure struct {
	AgentUID, PaneUID, Generation string
	Cause                         error
	message                       string
}

func (e *ProjectProcessStopFailure) Error() string { return e.message }
func (e *ProjectProcessStopFailure) Unwrap() error { return e.Cause }

type ProjectProcessStopExecutor struct {
	Update       func(func(*metadata.Registry) error) (metadata.Registry, bool, error)
	RegistryPath string
	Load         func() (metadata.Registry, error)
	ReadIdentity ProcessIdentityReader
	CallStop     func(context.Context, metadata.Registry, metadata.Agent) error
	Timeout      time.Duration
}

// Stop preserves the CLI batch policy: dispatch all owners concurrently, share
// one deadline, report only confirmed stops, and never project failed owners Offline.
// Child-free claims retain Project stop's refusal policy, unlike StopAgent.
func (s ProjectProcessStopExecutor) Stop(ctx context.Context, projectUID string) ([]receipt.ReceiptResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Load == nil {
		return nil, errors.New("project stop Registry store is not configured")
	}
	reg, err := s.Load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets := RunningProjectProcessAgents(reg, projectUID)
	if len(targets) == 0 {
		return nil, nil
	}
	if s.RegistryPath == "" {
		return nil, errors.New("project stop Registry store is not configured")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = ProcessAgentStopTimeout
	}
	deadline := time.Now().Add(timeout)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	bounded, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	failures := make([]error, len(targets))
	confirmed := make([]bool, len(targets))
	var requests sync.WaitGroup
	for i, target := range targets {
		requests.Go(func() {
			if target.Pane.Status.ProcessSession == nil || target.Pane.Status.Activation.Process == nil {
				failures[i] = processhost.ErrStale
				return
			}
			call := s.CallStop
			if call == nil {
				call = func(ctx context.Context, reg metadata.Registry, agent metadata.Agent) error {
					return CallProcessAgentStop(ctx, s.RegistryPath, reg, agent)
				}
			}
			original := call
			call = func(_ context.Context, reg metadata.Registry, agent metadata.Agent) error {
				return original(bounded, reg, agent)
			}

			stopper := ProcessAgentStopper{Update: s.Update, RegistryPath: s.RegistryPath, Load: s.Load, ReadIdentity: s.ReadIdentity, CallStop: call,
				ClaimStopAdmission: func(metadata.Registry, func() error) error { return processhost.ErrStale }}
			failures[i] = stopper.StopExplicit(bounded, reg, target.Agent, target.Pane)
			confirmed[i] = failures[i] == nil && !stopper.ClaimReleased
		})
	}
	requests.Wait()
	var stopped []receipt.ReceiptResource
	var errs []error
	for i, target := range targets {
		if confirmed[i] {
			stopped = append(stopped, receipt.ReceiptResource{Kind: "Agent", UID: target.Agent.Metadata.UID, Name: target.Agent.Metadata.Name, Action: receipt.ActionStopped})
		}
		if failure := failures[i]; failure != nil {
			display := failure.Error()
			var typed *ProcessStopFailure
			if errors.As(failure, &typed) {
				display = processhost.ErrStale.Error()
				if errors.Is(failure, context.DeadlineExceeded) {
					display = "durable Wait unconfirmed: " + context.DeadlineExceeded.Error()
				}
			}
			generation := ""
			if target.Pane.Status.ProcessSession != nil {
				generation = target.Pane.Status.ProcessSession.Binding.Generation
			}
			errs = append(errs, &ProjectProcessStopFailure{AgentUID: target.Agent.Metadata.UID, PaneUID: target.Pane.Metadata.UID, Generation: generation, Cause: failure,
				message: fmt.Sprintf("%s: agent/%s uid:%s: %s; recover with: projmux stop project uid:%s", ProjectStopAgentsStillRunning, target.Agent.Metadata.Name, target.Agent.Metadata.UID, display, projectUID)})
		}
	}
	return stopped, errors.Join(errs...)
}
