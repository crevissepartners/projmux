package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func projectStopFixture() m.Registry {
	reg, _, _ := stopFixture()
	reg.Windows = []m.Window{{Metadata: m.ObjectMeta{UID: "window", OwnerRef: &m.OwnerRef{Kind: m.KindProject, UID: "project"}}}}
	return reg
}

func TestProjectProcessStopBornClaimGenerationCancellationAndRetirement(t *testing.T) {
	for _, mode := range []string{"born", "claim", "stale", "replacement-after-stop", "ambiguous", "deadline", "no-wait", "child-live", "identity-error", "cancel-before", "cancel-load", "cancel-admitted"} {
		t.Run(mode, func(t *testing.T) {
			reg := projectStopFixture()
			if mode == "claim" {
				reg.Panes[0].Status.Activation = m.PaneActivation{}
			}
			if mode == "ambiguous" {
				p := reg.Panes[0].Clone()
				p.Metadata.UID = "other-pane"
				reg.Panes = append(reg.Panes, p)
			}
			before := reg.Clone()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Millisecond)
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			calls, reads := 0, 0
			var lock sync.Mutex
			s := ProjectProcessStopExecutor{Update: func(ctx context.Context, apply func(*m.Registry) error) (m.Registry, bool, error) {
				lock.Lock()
				defer lock.Unlock()
				if err := ctx.Err(); err != nil {
					return m.Registry{}, false, err
				}
				err := apply(&reg)
				return reg.Clone(), err == nil, err
			}, RegistryPath: filepath.Join(t.TempDir(), "registry.json"), Load: func() (m.Registry, error) {
				lock.Lock()
				defer lock.Unlock()
				if mode == "cancel-load" {
					cancel()
				}
				reads++
				if mode == "stale" && reads > 1 {
					reg.Panes[0].Status.ProcessSession.Binding.Generation = "replacement"
				}
				return reg.Clone(), nil
			}, ReadIdentity: func(pid int) (m.ProcessIdentity, int, error) {
				if mode == "identity-error" {
					return m.ProcessIdentity{}, 0, errors.New("injected unreadable proc")
				}
				if mode == "child-live" && pid == 101 {
					return before.Panes[0].Status.Activation.Process.Child, 0, nil
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}, CallStop: func(admitted context.Context, _ m.Registry, _ m.Agent) error {
				lock.Lock()
				defer lock.Unlock()
				calls++
				if mode == "cancel-admitted" {
					cancel()
					if admitted.Err() != nil {
						t.Error("cancellation reached Stop")
					}
				}
				if mode == "deadline" {
					<-admitted.Done()
					return admitted.Err()
				}
				if mode != "no-wait" {
					writeStopWait(&reg)
				}
				if mode == "replacement-after-stop" {
					reg.Panes[0].Status.ProcessSession.Binding.Generation = "replacement"
				}
				return nil
			}}
			stopped, err := s.Stop(ctx, "project")
			switch mode {
			case "born", "cancel-admitted":
				if err != nil || len(stopped) != 1 || calls != 1 {
					t.Fatalf("stopped%v calls%d err%v", stopped, calls, err)
				}
				if reg.Panes[0].Status.ProcessSession.ThreadID != "thread" || len(reg.Windows) != 1 {
					t.Fatal("topology/conversation lost")
				}
			case "cancel-before", "cancel-load":
				if !errors.Is(err, context.Canceled) || calls != 0 || !reflect.DeepEqual(before, reg) {
					t.Fatalf("cancel effect: %v", err)
				}
			default:
				if err == nil || len(stopped) != 0 {
					t.Fatalf("false success %v %v", stopped, err)
				}
				var f *ProjectProcessStopFailure
				if !errors.As(err, &f) || f.AgentUID != "agent" || f.PaneUID != "pane" || f.Generation != "generation" {
					t.Fatalf("refs: %v", err)
				}
				if mode == "claim" || mode == "ambiguous" || mode == "stale" || mode == "replacement-after-stop" {
					if !errors.Is(err, processhost.ErrStale) || (calls != 0 && mode != "replacement-after-stop") {
						t.Fatal(err)
					}
				} else if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				if mode == "no-wait" || mode == "identity-error" || mode == "child-live" {
					var cause *ProcessStopFailure
					if !errors.As(err, &cause) || cause.Code != "agent-stop-wait-unconfirmed" || err.Error() != "project-stop-agents-still-running: agent/agent uid:agent: durable Wait unconfirmed: context deadline exceeded; recover with: projmux stop project uid:project" {
						t.Fatalf("cause/display %v", err)
					}
				}
				if mode == "claim" && (!reflect.DeepEqual(before, reg) || reg.Panes[0].Status.LastTermination != nil) {
					t.Fatal("claim changed/fake Wait")
				}
			}
		})
	}
}

func TestProjectProcessStopParallelPartialFailureAndOtherProject(t *testing.T) {
	reg := projectStopFixture()
	for _, uid := range []string{"failed", "bystander"} {
		a := reg.Agents[0].Clone()
		p := reg.Panes[0].Clone()
		a.Metadata.UID = uid
		a.Metadata.Name = uid
		a.Status.PaneRef = uid + "-pane"
		p.Metadata.UID = a.Status.PaneRef
		p.Metadata.OwnerRef.UID = uid
		b := &p.Status.ProcessSession.Binding
		b.AgentUID = uid
		b.PaneUID = p.Metadata.UID
		p.Status.Activation.Process.Binding = *b
		if uid == "bystander" {
			a.Metadata.OwnerRef.UID = "other-window"
			b.ProjectUID = "other-project"
			b.WindowUID = "other-window"
			p.Status.Activation.Process.Binding = *b
		}
		reg.Agents = append(reg.Agents, a)
		reg.Panes = append(reg.Panes, p)
	}
	before := reg.Clone()
	var lock sync.Mutex
	admitted := make(chan string, 2)
	both := make(chan struct{})
	calls := 0
	var deadlines []time.Time
	s := ProjectProcessStopExecutor{Update: func(ctx context.Context, apply func(*m.Registry) error) (m.Registry, bool, error) {
		lock.Lock()
		defer lock.Unlock()
		if err := ctx.Err(); err != nil {
			return m.Registry{}, false, err
		}
		err := apply(&reg)
		return reg.Clone(), err == nil, err
	}, RegistryPath: filepath.Join(t.TempDir(), "registry.json"), Timeout: time.Second, Load: func() (m.Registry, error) { lock.Lock(); defer lock.Unlock(); return reg.Clone(), nil }, ReadIdentity: func(int) (m.ProcessIdentity, int, error) { return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent }, CallStop: func(ctx context.Context, _ m.Registry, a m.Agent) error {
		lock.Lock()
		calls++
		d, _ := ctx.Deadline()
		deadlines = append(deadlines, d)
		if calls == 2 {
			close(both)
		}
		lock.Unlock()
		admitted <- a.Metadata.UID
		select {
		case <-both:
		case <-ctx.Done():
			return ctx.Err()
		}
		if a.Metadata.UID == "failed" {
			return processhost.ErrBusy
		}
		lock.Lock()
		defer lock.Unlock()
		writeStopWait(&reg)
		return nil
	}}
	stopped, err := s.Stop(context.Background(), "project")
	if len(stopped) != 1 || stopped[0].UID != "agent" || !errors.Is(err, processhost.ErrBusy) || len(admitted) != 2 || deadlines[0] != deadlines[1] {
		t.Fatalf("stopped%v err%v deadlines%v", stopped, err, deadlines)
	}
	if !reflect.DeepEqual(reg.Agents[1:], before.Agents[1:]) || !reflect.DeepEqual(reg.Panes[1:], before.Panes[1:]) {
		t.Fatal("failed/bystander changed")
	}
	if reg.Panes[0].Status.ProcessSession.ThreadID != before.Panes[0].Status.ProcessSession.ThreadID {
		t.Fatal("conversation lost")
	}
}

func TestExecuteProjectStopCancellationAndPartialTmux(t *testing.T) {
	for _, mode := range []string{"cancel-before", "cancel-process", "tmux-fail", "process-fail"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			processCalls, tmuxCalls := 0, 0
			stopped, tmuxStopped, err := ExecuteProjectStop(ctx, func(exec context.Context) ([]receipt.ReceiptResource, error) {
				processCalls++
				if mode == "cancel-process" {
					cancel()
				}
				if exec.Err() != nil {
					t.Fatal("cancel reached admitted process")
				}
				if mode == "process-fail" {
					return nil, processhost.ErrBusy
				}
				return []receipt.ReceiptResource{{Kind: "Agent", UID: "agent", Name: "agent", Action: receipt.ActionStopped}}, nil
			}, func(exec context.Context) error {
				tmuxCalls++
				if exec.Err() != nil {
					t.Fatal("cancel reached tmux/compensation")
				}
				if mode == "tmux-fail" {
					return os.ErrPermission
				}
				return nil
			})
			if mode == "cancel-before" {
				if processCalls+tmuxCalls != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if processCalls != 1 || tmuxCalls != 1 || tmuxStopped == (mode == "tmux-fail") {
				t.Fatal("batch policy changed")
			}
			if mode == "tmux-fail" && (!errors.Is(err, os.ErrPermission) || len(stopped) != 1) {
				t.Fatal(err)
			}
			if mode == "process-fail" && !errors.Is(err, processhost.ErrBusy) {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectProcessStopInactiveNoEffects(t *testing.T) {
	reg := projectStopFixture()
	reg.Agents[0].Status.Phase = m.PhaseOffline
	root := t.TempDir()
	before := reg.Clone()
	s := ProjectProcessStopExecutor{RegistryPath: filepath.Join(root, "registry.json"), Load: func() (m.Registry, error) { return reg.Clone(), nil }, CallStop: func(context.Context, m.Registry, m.Agent) error { t.Fatal("inactive controlled"); return nil }}
	stopped, err := s.Stop(context.Background(), "project")
	files, _ := os.ReadDir(root)
	if err != nil || len(stopped) != 0 || len(files) != 0 || !reflect.DeepEqual(before, reg) {
		t.Fatal("inactive changed")
	}
}

func TestProjectProcessStopPathRequiredOnlyForSelectedOwners(t *testing.T) {
	reg := projectStopFixture()
	s := ProjectProcessStopExecutor{Load: func() (m.Registry, error) { return reg.Clone(), nil }}
	if _, err := s.Stop(context.Background(), "other-project"); err != nil {
		t.Fatal("empty snapshot requires process path", err)
	}
	if _, err := s.Stop(context.Background(), "project"); err == nil {
		t.Fatal("selected process accepts implicit path")
	}
}
