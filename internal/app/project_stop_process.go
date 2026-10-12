package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const projectStopAgentsStillRunning = "project-stop-agents-still-running"

type projectProcessStopper func(context.Context, *resourceStore, string) ([]cli.ReceiptResource, error)

type projectProcessStopTarget struct {
	agent coremetadata.Agent
	pane  coremetadata.Pane
}

func runningProjectProcessAgents(reg coremetadata.Registry, projectUID string) []projectProcessStopTarget {
	windows := make(map[string]bool)
	for _, window := range reg.WindowsOf(projectUID) {
		windows[window.Metadata.UID] = true
	}
	var targets []projectProcessStopTarget
	for _, agent := range reg.Agents {
		if agent.Status.Phase != coremetadata.PhaseRunning || !windows[agent.Metadata.OwnerUID()] {
			continue
		}
		pane, ok := reg.Pane(agent.Status.PaneRef)
		if ok && (pane.Status.ProcessSession != nil || pane.Status.Activation.Kind == coremetadata.RuntimeProcess) {
			targets = append(targets, projectProcessStopTarget{agent.Clone(), pane.Clone()})
		}
	}
	return targets
}

// Project stop uses the existing host control. Only the host writes termination
// evidence; this observer never projects a refused or unconfirmed stop Offline.
func stopProjectProcessAgents(ctx context.Context, store *resourceStore, projectUID string) ([]cli.ReceiptResource, error) {
	command := newAgentCommand()
	return stopProjectProcessAgentsWith(ctx, store, projectUID, command.callProcessTurn, command.readProcessIdentity(), processRelaunchRetirementTimeout)
}

func stopProjectProcessAgentsWith(ctx context.Context, store *resourceStore, projectUID string,
	stop func(coremetadata.Registry, coremetadata.Agent, string, string, string) (string, error),
	identity processIdentityReader, timeout time.Duration) ([]cli.ReceiptResource, error) {
	reg, err := store.load()
	if err != nil {
		return nil, err
	}
	targets := runningProjectProcessAgents(reg, projectUID)
	failures := make([]error, len(targets))
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var requests sync.WaitGroup
	// Admit every Stop before waiting for any durable Wait. Control calls may
	// block independently; dispatch them together rather than multiplying latency.
	for i, target := range targets {
		requests.Go(func() {
			if target.pane.Status.ProcessSession == nil {
				failures[i] = processhost.ErrStale
				return
			}
			_, failures[i] = stop(reg, target.agent, target.pane.Status.ProcessSession.Provider, "stop", "")
		})
	}
	requests.Wait()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	pending := make(map[int]bool)
	for i := range targets {
		if failures[i] == nil {
			pending[i] = true
		}
	}
	confirmed := make([]bool, len(targets))
	for len(pending) > 0 {
		latest, loadErr := store.load()
		if loadErr != nil {
			for i := range pending {
				failures[i] = loadErr
				delete(pending, i)
			}
			break
		}
		for i := range pending {
			target := targets[i]
			pane, ok := latest.Pane(target.pane.Metadata.UID)
			agent, found := latest.Agent(target.agent.Metadata.UID)
			if !ok || !found || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != target.pane.Status.ProcessSession.Binding {
				failures[i] = processhost.ErrStale
				delete(pending, i)
				continue
			}
			if agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
				continue
			}
			activation := target.pane.Status.Activation.Process
			if activation == nil {
				failures[i] = processhost.ErrStale
				delete(pending, i)
				continue
			}
			actual, _, identityErr := identity(activation.HostProcess.PID)
			if identityErr == nil && actual == activation.HostProcess {
				continue
			}
			confirmed[i] = true
			delete(pending, i)
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-bounded.Done():
			for i := range pending {
				failures[i] = fmt.Errorf("durable Wait unconfirmed: %w", bounded.Err())
				delete(pending, i)
			}
		case <-ticker.C:
		}
	}
	var stopped []cli.ReceiptResource
	for i, done := range confirmed {
		if done {
			agent := targets[i].agent
			stopped = append(stopped, cli.ReceiptResource{Kind: "Agent", UID: agent.Metadata.UID, Name: agent.Metadata.Name, Action: cli.ActionStopped})
		}
	}
	var errs []error
	for i, failure := range failures {
		if failure != nil {
			errs = append(errs, fmt.Errorf("%s: agent/%s uid:%s: %w; recover with: projmux stop project uid:%s", projectStopAgentsStillRunning, targets[i].agent.Metadata.Name, targets[i].agent.Metadata.UID, failure, projectUID))
		}
	}
	return stopped, errors.Join(errs...)
}
