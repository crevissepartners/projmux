package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processDeleteFixture turns the session harness Agent into a live process
// runtime Agent with recorded host and child births.
func processDeleteFixture(t *testing.T, provider string) (*coremetadata.Registry, string, string) {
	t.Helper()
	h := newSessionRefHarness(t, provider)
	reg := h.registry.Clone()
	pane, _ := reg.Pane(h.paneUID)
	agent, _ := reg.Agent(h.agentUID)
	window, _ := reg.Window(agent.Metadata.OwnerUID())
	binding := coremetadata.ProcessBinding{HostInstanceID: "host-one", ProjectUID: window.Metadata.OwnerUID(), WindowUID: window.Metadata.UID,
		AgentUID: h.agentUID, PaneUID: h.paneUID, Generation: "gen-one", OperationID: "op-one"}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, Generation: binding.Generation, AgentUID: h.agentUID,
		OperationID: binding.OperationID, Process: &coremetadata.ProcessActivation{Binding: binding,
			HostProcess: coremetadata.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "host-start"},
			Child:       coremetadata.ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "child-start"}}}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: provider, Binding: binding, ResumeState: coremetadata.ProcessResumeUnknown}
	agent.Status.Phase = coremetadata.PhaseRunning
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &reg, h.agentUID, h.paneUID
}

// recordFixtureWait is what the owner does after its supervisor's Wait.
func recordFixtureWait(t *testing.T, reg *coremetadata.Registry, paneUID string) {
	t.Helper()
	pane, _ := reg.Pane(paneUID)
	activation := *pane.Status.Activation.Process
	code := 0
	receipt := coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal,
		ObservedAt: time.Now().UTC(), PaneUID: paneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation,
		OperationID: activation.Binding.OperationID, ExitCode: &code}
	if err := (coremetadata.Mutator{}).RecordProcessWait(reg, activation, receipt); err != nil {
		t.Fatal(err)
	}
}

func memoryProcessDeleteStore(reg *coremetadata.Registry) *resourceStore {
	return &resourceStore{
		load: func() (coremetadata.Registry, error) { return reg.Clone(), nil },
		update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			working := reg.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, err
			}
			*reg = working
			return working, nil
		},
		mutator: intmetadata.DefaultMutator,
	}
}

type processDeleteProbe struct {
	host, child bool
	stops       int
	onStop      func() error
}

func (p *processDeleteProbe) deleter(reg *coremetadata.Registry) *processAgentDeleter {
	return &processAgentDeleter{
		store: memoryProcessDeleteStore(reg),
		alive: func(birth coremetadata.ProcessIdentity) bool {
			if birth.Start == "host-start" {
				return p.host
			}
			return p.child
		},
		stop: func(context.Context, coremetadata.Registry, coremetadata.Agent) error {
			p.stops++
			if p.onStop != nil {
				return p.onStop()
			}
			return nil
		},
		waitLimit:     200 * time.Millisecond,
		stopAdmission: 20 * time.Millisecond,
		poll:          time.Millisecond,
		via:           deletionViaCLI,
	}
}

func runProcessDelete(reg *coremetadata.Registry, deleter *processAgentDeleter, args ...string) (string, error) {
	c := &deleteCommand{
		store:          deleter.store,
		confirm:        &confirmer{},
		resolveKinds:   deleteRegistryKinds,
		lookupEnv:      func(string) string { return "" },
		newOperationID: func() (string, error) { return "op-delete", nil },
		processDeleter: deleter,
	}
	var stdout, stderr bytes.Buffer
	err := c.Run(append([]string{"agent"}, args...), &stdout, &stderr)
	return stdout.String(), err
}

func requireProcessDeleteToken(t *testing.T, err error, token string) {
	t.Helper()
	var refusal *processDeleteError
	if !errors.As(err, &refusal) || refusal.Token != token || !strings.HasPrefix(err.Error(), token+": ") {
		t.Fatalf("err = %v, want typed %s", err, token)
	}
}

func TestProcessDeleteRegistryOnlyRuntimesNeedNoTmux(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T, *coremetadata.Registry, string, *processDeleteProbe) string{
		"offline": func(t *testing.T, reg *coremetadata.Registry, pane string, _ *processDeleteProbe) string {
			recordFixtureWait(t, reg, pane)
			return "runtime=offline evidence=wait:normal exit=0"
		},
		"unknown-reserved": func(t *testing.T, reg *coremetadata.Registry, pane string, _ *processDeleteProbe) string {
			p, _ := reg.Pane(pane)
			p.Status.Activation = coremetadata.PaneActivation{}
			agent, _ := reg.Agent(p.Metadata.OwnerUID())
			agent.Status.Phase = coremetadata.PhasePending
			return "runtime=unknown evidence=activation-absent"
		},
		"unknown-owner-killed": func(*testing.T, *coremetadata.Registry, string, *processDeleteProbe) string {
			return "runtime=unknown evidence=owner-host-and-child-absent"
		},
	} {
		t.Run(name, func(t *testing.T) {
			reg, agent, pane := processDeleteFixture(t, aiModeClaude)
			probe := &processDeleteProbe{}
			want := prepare(t, reg, pane, probe)
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			before := reg.Clone()
			deleter := probe.deleter(reg)
			out, err := runProcessDelete(reg, deleter, "uid:"+agent, "--dry-run", "--socket", "projmux")
			if err != nil || !strings.Contains(out, "registry-only would delete this Agent") || !strings.Contains(out, want) || !reflect.DeepEqual(*reg, before) {
				t.Fatalf("dry-run out=%q err=%v", out, err)
			}
			out, err = runProcessDelete(reg, deleter, "uid:"+agent, "--yes")
			if err != nil || !strings.Contains(out, "registry-only deleted this Agent") || !strings.Contains(out, want) ||
				!strings.Contains(out, "runtime=unchanged") {
				t.Fatalf("delete out=%q err=%v", out, err)
			}
			if _, ok := reg.Agent(agent); ok {
				t.Fatal("Agent survived")
			}
			if _, ok := reg.Pane(pane); ok {
				t.Fatal("Pane survived")
			}
			if probe.stops != 0 {
				t.Fatalf("registry-only delete sent %d Stop requests", probe.stops)
			}
		})
	}
}

func TestProcessDeleteRunningStopsThroughOwnerThenDeletes(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			reg, agent, pane := processDeleteFixture(t, provider)
			probe := &processDeleteProbe{host: true, child: true}
			probe.onStop = func() error {
				if probe.stops == 1 {
					// A just-started session can briefly refuse as stale.
					return processhost.ErrStale
				}
				recordFixtureWait(t, reg, pane)
				probe.host, probe.child = false, false
				return nil
			}
			deleter := probe.deleter(reg)
			out, err := runProcessDelete(reg, deleter, "uid:"+agent, "--dry-run")
			if err != nil || !strings.Contains(out, "would stop through owner host pid=11") || probe.stops != 0 {
				t.Fatalf("dry-run out=%q err=%v stops=%d", out, err, probe.stops)
			}
			out, err = runProcessDelete(reg, deleter, "uid:"+agent, "--yes")
			if err != nil || probe.stops != 2 || !strings.Contains(out, "runtime=stopped evidence=wait:normal exit=0") || !strings.Contains(out, " runtime=stopped focus=") {
				t.Fatalf("delete out=%q err=%v stops=%d", out, err, probe.stops)
			}
			if _, ok := reg.Agent(agent); ok {
				t.Fatal("Agent survived")
			}
		})
	}
}

func TestProcessDeleteRefusalsLeaveRegistryUnchanged(t *testing.T) {
	for name, tc := range map[string]struct {
		probe processDeleteProbe
		args  func(agent string) []string
		token string
	}{
		"child outlived host": {probe: processDeleteProbe{child: true}, token: processDeleteRefusedToken},
		"stop unconfirmed":    {probe: processDeleteProbe{host: true, child: true}, token: processDeleteStopUnconfirmedToken},
		"host unreachable": {probe: processDeleteProbe{host: true, child: true, onStop: func() error {
			return errors.New("process-host-unavailable: dial unix: connection refused")
		}}, token: processDeleteHostUnavailableToken},
		"host stays stale past admission": {probe: processDeleteProbe{host: true, child: true, onStop: func() error {
			return processhost.ErrStale
		}}, token: processDeleteHostUnavailableToken},
		"host refused stop": {probe: processDeleteProbe{host: true, child: true, onStop: func() error {
			return errors.New("stale")
		}}, token: processDeleteHostUnavailableToken},
		"name selector": {args: func(string) []string { return nil }, token: processDeleteRefusedToken},
	} {
		t.Run(name, func(t *testing.T) {
			reg, agent, _ := processDeleteFixture(t, aiModeClaude)
			before := reg.Clone()
			probe := tc.probe
			args := []string{"uid:" + agent, "--yes"}
			if tc.args != nil {
				named, _ := reg.Agent(agent)
				args = []string{named.Metadata.Name, "--yes"}
			}
			_, err := runProcessDelete(reg, probe.deleter(reg), args...)
			requireProcessDeleteToken(t, err, tc.token)
			if !reflect.DeepEqual(*reg, before) {
				t.Fatal("refusal changed the Registry")
			}
		})
	}
}

func TestProcessDeleteKeepsSocketFlagSyntax(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	recordFixtureWait(t, reg, pane)
	for _, args := range [][]string{{"--socket", "a", "--socket-path", "/b"}, {"--socket-path", "relative"}} {
		_, err := runProcessDelete(reg, (&processDeleteProbe{}).deleter(reg), append([]string{"uid:" + agent, "--yes"}, args...)...)
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("%v: err=%v", args, err)
		}
	}
	if _, ok := reg.Agent(agent); !ok {
		t.Fatal("usage error deleted the Agent")
	}
}

// A tmux Agent, alone or beside a process Agent, keeps the existing route and
// its exact outside-tmux refusal.
func TestProcessDeleteLeavesTmuxAgentRouteUnchanged(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	reg := h.registry.Clone()
	probe := &processDeleteProbe{host: true}
	_, err := runProcessDelete(&reg, probe.deleter(&reg), "uid:"+h.agentUID, "--yes")
	if err == nil || err.Error() != "delete agent requires --socket <name> or --socket-path <absolute> outside tmux" || probe.stops != 0 {
		t.Fatalf("tmux Agent err=%v stops=%d", err, probe.stops)
	}

	process, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := process.Agent(agent)
	window, _ := process.Window(a.Metadata.OwnerUID())
	tmuxPane := window.Spec.AnchorPaneRef
	only := deletePlan{Kind: coremetadata.KindAgent, ExactUID: true, Targets: []deleteTarget{{Match: selector.Match{Kind: coremetadata.KindAgent, UID: agent},
		Descendants: []deleteDescendant{{Kind: coremetadata.KindPane, UID: pane}}}}}
	if !isProcessAgentPlan(*process, only) {
		t.Fatal("a process-only plan kept the tmux route")
	}
	mixed := only
	mixed.Targets = []deleteTarget{{Match: only.Targets[0].Match, Descendants: append(slices.Clone(only.Targets[0].Descendants),
		deleteDescendant{Kind: coremetadata.KindPane, UID: tmuxPane})}}
	paneless := deletePlan{Kind: coremetadata.KindAgent, ExactUID: true, Targets: []deleteTarget{{Match: only.Targets[0].Match}}}
	if tmuxPane == "" || tmuxPane == pane || isProcessAgentPlan(*process, mixed) || isProcessAgentPlan(*process, paneless) {
		t.Fatalf("a plan with a tmux or no Pane took the process route (anchor %q)", tmuxPane)
	}
}

func TestDeleteProcessAgentGoEntry(t *testing.T) {
	if _, err := newProcessAgentDeleteRequest(processAgentDeleteOptions{Agent: selector.Ref{Name: "agent"}}); err == nil {
		t.Fatal("name reference accepted")
	} else {
		requireProcessDeleteToken(t, err, processDeleteRefusedToken)
	}
	reg, agent, pane := processDeleteFixture(t, aiModeCodex)
	recordFixtureWait(t, reg, pane)
	deleter := (&processDeleteProbe{}).deleter(reg)
	window, _ := reg.Agent(agent)
	req, err := newProcessAgentDeleteRequest(processAgentDeleteOptions{Agent: selector.Ref{UID: agent, Raw: "uid:" + agent},
		Scope: processDeleteScope{Window: selector.Ref{Kind: coremetadata.KindWindow, UID: window.Metadata.OwnerUID()}}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	before := reg.Clone()
	got, err := deleter.deleteOne(context.Background(), req)
	want := processAgentDeleteResult{Agent: agent, Pane: pane, Runtime: processDeleteRuntimeOffline, Evidence: "wait:normal exit=0", DryRun: true}
	got.target = processDeleteTarget{}
	if err != nil || got != want || !reflect.DeepEqual(*reg, before) {
		t.Fatalf("dry run = %+v, %v", got, err)
	}
	req.options.DryRun = false
	got, err = deleter.deleteOne(context.Background(), req)
	want.DryRun = false
	got.target = processDeleteTarget{}
	if err != nil || got != want {
		t.Fatalf("delete = %+v, %v", got, err)
	}
	if _, ok := reg.Agent(agent); ok {
		t.Fatal("Agent survived")
	}

	h := newSessionRefHarness(t, aiModeClaude)
	tmuxReg := h.registry.Clone()
	req, _ = newProcessAgentDeleteRequest(processAgentDeleteOptions{Agent: selector.Ref{UID: h.agentUID, Raw: "uid:" + h.agentUID}})
	_, err = (&processDeleteProbe{}).deleter(&tmuxReg).deleteOne(context.Background(), req)
	requireProcessDeleteToken(t, err, processDeleteRefusedToken)
	if !strings.Contains(err.Error(), fmt.Sprintf("Agent uid %q is not a process runtime Agent", h.agentUID)) {
		t.Fatalf("tmux Agent err=%v", err)
	}
}
