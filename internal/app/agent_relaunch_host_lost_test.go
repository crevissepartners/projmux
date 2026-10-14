package app

import (
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

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

func TestHostLostClaudeBirthAdmissionRejectsInconsistentIdentity(t *testing.T) {
	for _, change := range []string{"normal", "child-uid", "supervisor-uid", "host-birth", "parent", "late-parent", "supervisor-birth", "unreadable"} {
		t.Run(change, func(t *testing.T) {
			c, candidate, store := hostLostRelaunchCommand(t)
			b := processSchemaBinding(candidate.Record.Binding)
			b.Host, b.Generation, b.Operation = "birth", "birth", "birth"
			if err := c.reserveProcessRelaunch(context.Background(), candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, b); err != nil {
				t.Fatal(err)
			}
			before, _ := store.LoadReadOnly()
			host := coremetadata.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "host"}
			child := coremetadata.ProcessIdentity{PID: 13, OwnerUID: 1000, Start: "child"}
			if change == "child-uid" {
				child.OwnerUID = 0
			}
			service := claudeProcessService{registryPath: store.Path(), binding: b, ownedProcess: child, ownedHostProcess: host}
			reads := map[int]int{}
			read := func(pid int) (coremetadata.ProcessIdentity, int, error) {
				reads[pid]++
				if change == "unreadable" {
					return coremetadata.ProcessIdentity{}, 0, errors.New("unavailable")
				}
				switch pid {
				case 13:
					if change == "late-parent" && reads[pid] == 2 {
						return child, 99, nil
					}
					return child, 12, nil
				case 12:
					uid := uint32(1000)
					parent := 11
					if change == "supervisor-uid" {
						uid = 0
					}
					if change == "parent" {
						parent = 99
					}
					birth := "supervisor"
					if change == "supervisor-birth" && reads[pid] == 2 {
						birth = "replacement"
					}
					return coremetadata.ProcessIdentity{PID: 12, OwnerUID: uid, Start: birth}, parent, nil
				default:
					got := host
					if change == "host-birth" {
						got.Start = "replacement"
					}
					return got, 1, nil
				}
			}
			err := service.recordChildIdentity(read)
			after, readErr := store.LoadReadOnly()
			if change == "normal" {
				p, _ := after.Pane(b.Pane)
				if err != nil || readErr != nil || p.Status.Activation.Process == nil || p.Status.Activation.Process.Child != child || p.Status.Activation.Process.HostProcess != host {
					t.Fatalf("normal birth refused: %v %v", err, readErr)
				}
				return
			}
			if !errors.Is(err, processhost.ErrStale) || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("unsafe birth: %v %v", err, readErr)
			}
		})
	}
}

func TestHostLostUnpublishedBirthActualWaitRestoresRecipe(t *testing.T) {
	c, candidate, store := hostLostRelaunchCommand(t)
	settings := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolution{New: agentsettings.Settings{Model: agentsettings.Setting{Value: "rejected-model", Source: coremetadata.SettingSourceFlag}}}}
	b := processSchemaBinding(candidate.Record.Binding)
	b.Host, b.Generation, b.Operation = "unpublished", "unpublished", "unpublished"
	if err := c.reserveProcessRelaunch(context.Background(), candidate, settings, agentGuidanceLaunch{}, projectLinksLaunch{}, b); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "owner.test")
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + root, "XDG_CONFIG_HOME=" + root, "XDG_STATE_HOME=" + root, "PMX_TEST_PROCESS_CLAUDE_CHILD=1"}
	current := func(ctx context.Context, _ processhost.Binding) error { return ctx.Err() }
	host, err := processhost.NewHost(b.Host, processhost.Command{Path: binary, Args: []string{"internal", "processhost-test-supervisor"}, Env: env}, processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, _ string) error { return current(ctx, b) }}, processhost.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	published := false
	handle, err := host.Start(context.Background(), processhost.Launch{Binding: b, Command: processhost.Command{Path: "/bin/sh", Args: []string{"-c", "read line"}, Env: env}, Spawned: &processhost.SpawnCallback{Publish: func(_ context.Context, h *processhost.Handle) error {
		snap, err := h.Observe(b)
		if err != nil || snap.PID <= 0 {
			t.Errorf("missing actual birth: %+v %v", snap, err)
		}
		published = true
		return processhost.ErrStale
	}}})
	if !errors.Is(err, processhost.ErrStale) || handle == nil || !published {
		t.Fatalf("birth refusal %v", err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(b)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = handle.Wait(ctx, b)
	})
	result := processAgentResumeResult{Binding: b, Handle: handle, previousRecord: candidate.Record.Clone(), previousActivation: candidate.HostLost, owner: processAgentCreateResult{Binding: b, Handle: handle, registryPath: store.Path()}}
	if err := c.retireFailedProcessRelaunch(&result); err != nil {
		t.Fatal(err)
	}
	snap, err := handle.Observe(b)
	if err != nil || snap.Exit == nil || snap.PID <= 0 {
		t.Fatalf("no actual Wait: %+v %v", snap, err)
	}
	if err := c.restoreProcessRelaunchSettings(candidate, b, settings, agentGuidanceLaunch{}, projectLinksLaunch{}); err != nil {
		t.Fatal(err)
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent(candidate.Agent.Metadata.UID)
	p, _ := reg.Pane(candidate.Record.Binding.PaneUID)
	if !reflect.DeepEqual(a.Metadata.Annotations, candidate.Agent.Metadata.Annotations) || !reflect.DeepEqual(p.Status.Activation.Process, candidate.HostLost) || !processResumeRecordEqual(p.Status.ProcessSession, &candidate.Record) || coremetadata.MatchesProcessWait(candidate.Record.Binding, p.Status.LastTermination) {
		t.Fatal("unpublished actual Wait failed restoration or invented old Wait")
	}
}
