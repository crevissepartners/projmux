package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func hostLostRelaunchCommand(t *testing.T) (*agentCommand, processResumeCandidate, *intmetadata.Store) {
	t.Helper()
	reg, activation := hostLostFixture(t)
	candidate, ok := hostLostResumeCandidate(reg, activation.Binding.AgentUID, absentProcess)
	if !ok {
		t.Fatal("missing host-lost candidate")
	}
	state := t.TempDir()
	store := intmetadata.NewStore(intmetadata.PathFor(state))
	if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	c := &agentCommand{loadRegistry: store.LoadReadOnly, processIdentity: absentProcess, messagePaths: agentMessagePaths{registryPath: store.Path()}}
	c.rebind = &agentRebinder{create: &createCommand{store: &resourceStore{stateDir: func() (string, error) { return state, nil }, mutator: intmetadata.DefaultMutator, update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		r, _, err := store.UpdateConvergent(fn)
		return r, err
	}}}}
	return c, candidate, store
}

func TestHostLostRelaunchReservationRechecksSourceAndKernel(t *testing.T) {
	for _, change := range []string{"owner-live", "child-live", "unreadable", "activation", "record", "spec", "annotations", "pid-reused"} {
		t.Run(change, func(t *testing.T) {
			c, candidate, store := hostLostRelaunchCommand(t)
			a := *candidate.HostLost
			if change == "owner-live" || change == "child-live" || change == "unreadable" || change == "pid-reused" {
				c.processIdentity = func(pid int) (coremetadata.ProcessIdentity, int, error) {
					switch change {
					case "unreadable":
						return coremetadata.ProcessIdentity{}, 0, errors.New("unreadable")
					case "pid-reused":
						return coremetadata.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: "different-birth"}, 0, nil
					case "owner-live":
						if pid == a.HostProcess.PID {
							return a.HostProcess, 0, nil
						}
					case "child-live":
						if pid == a.Child.PID {
							return a.Child, 0, nil
						}
					}
					return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
				}
			} else {
				_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					p, _ := reg.Pane(a.Binding.PaneUID)
					agent, _ := reg.Agent(a.Binding.AgentUID)
					switch change {
					case "activation":
						p.Status.Activation.Process.Child.Start = "changed"
					case "record":
						p.Status.ProcessSession.TurnID = "changed"
						p.Status.ProcessSession.Pending = nil
					case "spec":
						agent.Spec.Workspace.CWD = "/changed"
					case "annotations":
						if agent.Metadata.Annotations == nil {
							agent.Metadata.Annotations = map[string]string{}
						}
						agent.Metadata.Annotations["test.projmux.io/changed"] = "true"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			binding := processSchemaBinding(candidate.Record.Binding)
			binding.Host = "replacement"
			binding.Operation = "replacement"
			binding.Generation = "replacement"
			err = c.reserveProcessRelaunch(context.Background(), candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, binding)
			after, readErr := store.LoadReadOnly()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if change == "pid-reused" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("unsafe reservation %v", err)
			}
		})
	}
}

func TestHostLostRelaunchUnspawnedReservationRestoresRecipe(t *testing.T) {
	c, candidate, store := hostLostRelaunchCommand(t)
	settings := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolution{New: agentsettings.Settings{Model: agentsettings.Setting{Value: "replacement-model", Source: coremetadata.SettingSourceFlag}}}}
	binding := processSchemaBinding(candidate.Record.Binding)
	binding.Host, binding.Generation, binding.Operation = "replacement", "replacement", "replacement"
	if err := c.reserveProcessRelaunch(context.Background(), candidate, settings, agentGuidanceLaunch{}, projectLinksLaunch{}, binding); err != nil {
		t.Fatal(err)
	}
	result := processAgentResumeResult{Binding: binding, previousRecord: candidate.Record.Clone(), previousActivation: candidate.HostLost, owner: processAgentCreateResult{registryPath: store.Path()}}
	if err := c.retireFailedProcessRelaunch(&result); err != nil {
		t.Fatal(err)
	}
	if err := c.restoreProcessRelaunchSettings(candidate, binding, settings, agentGuidanceLaunch{}, projectLinksLaunch{}); err != nil {
		t.Fatal(err)
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Pane(candidate.Record.Binding.PaneUID)
	a, _ := reg.Agent(candidate.Record.Binding.AgentUID)
	if a.Status.Phase != coremetadata.PhaseRunning || !reflect.DeepEqual(a.Metadata.Annotations, candidate.Agent.Metadata.Annotations) || !reflect.DeepEqual(p.Status.Activation.Process, candidate.HostLost) || !processResumeRecordEqual(p.Status.ProcessSession, &candidate.Record) || coremetadata.MatchesProcessWait(candidate.Record.Binding, p.Status.LastTermination) {
		t.Fatal("unspawned rollback lost source or invented Wait")
	}
}

func TestHostLostRelaunchResumeCASHasOneWinner(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "relaunch-relaunch", true: "resume-relaunch"}[mixed], func(t *testing.T) {
			c, candidate, store := hostLostRelaunchCommand(t)
			start := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Go(func() {
					<-start
					b := processSchemaBinding(candidate.Record.Binding)
					b.Generation = []string{"first", "second"}[i]
					b.Host = b.Generation
					b.Operation = b.Generation
					if mixed && i == 0 {
						errs <- c.reserveProcessResume(context.Background(), candidate, agentSettingsLaunch{}, b, nil)
					} else {
						errs <- c.reserveProcessRelaunch(context.Background(), candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, b)
					}
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			winners := 0
			for err := range errs {
				if err == nil {
					winners++
				}
			}
			if winners != 1 {
				t.Fatalf("winners=%d", winners)
			}
			reg, err := store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			p, _ := reg.Pane(candidate.Pane.Metadata.UID)
			if p.Status.ProcessSession.History == nil || p.Status.ProcessSession.History.Binding != candidate.Record.Binding || p.Status.LastTermination.ExitCode != nil || p.Status.LastTermination.Source != coremetadata.TerminationSourceReconcile {
				t.Fatal("lost history or invented Wait")
			}
		})
	}
}

func TestHostLostRelaunchRequiresCallerOptIn(t *testing.T) {
	c, _, store, activation := hostLostPreparedFixture(t)
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(activation.Binding.AgentUID)
	if _, err = c.runOwnedProcessRelaunch(context.Background(), reg, agent.Clone(), agentRelaunchRequest{}); err == nil {
		t.Fatal("default private relaunch admitted lost host")
	}
	after, err := store.LoadReadOnly()
	if err != nil || !reflect.DeepEqual(reg, after) {
		t.Fatal("default refusal changed source", err)
	}
	preview, err := c.runOwnedProcessRelaunch(context.Background(), reg, agent.Clone(), agentRelaunchRequest{allowHostLost: true, dryRun: true, prompt: []string{"recover"}})
	if err != nil || preview.State != agentProcessPreview || preview.Result.Restart || preview.Result.PromptRequired || preview.Result.RelaunchCommand != "" || preview.Owned != nil || preview.Prepared != nil {
		t.Fatalf("explicit caller proof %+v: %v", preview, err)
	}
	for _, prompt := range [][]string{nil, {"   "}} {
		preview, err = c.runOwnedProcessRelaunch(context.Background(), reg, agent.Clone(), agentRelaunchRequest{allowHostLost: true, dryRun: true, prompt: prompt})
		command := "projmux agent relaunch uid:" + agent.Metadata.UID + " --detach --yes -- <prompt>"
		if err != nil || !preview.Result.PromptRequired || preview.Result.RelaunchCommand != command || preview.Result.ConfirmationRequired || preview.Owned != nil || preview.Prepared != nil {
			t.Fatalf("promptless preview %+v: %v", preview, err)
		}
	}
	after, err = store.LoadReadOnly()
	if err != nil || !reflect.DeepEqual(reg, after) {
		t.Fatal("preview changed source", err)
	}
}
