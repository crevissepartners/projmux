package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processResumableFixture struct {
	root, path string
	store      *intmetadata.Store
	binding    processhost.Binding
	observe    func(*testing.T) processhost.Snapshot
	stop       func() error
	wait       func(context.Context) (processhost.Snapshot, error)
}

func newProcessResumableFixture(t *testing.T, provider string) processResumableFixture {
	t.Helper()
	var f processResumableFixture
	if provider == "claude" {
		c := newProcessClaudeFixture(t, nil)
		c.turn(t, "unfinished", "question")
		f = processResumableFixture{root: c.root, path: c.path, store: c.store, binding: c.binding, observe: func(t *testing.T) processhost.Snapshot {
			return c.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
		}, stop: func() error { return c.handle.Stop(c.binding) }, wait: func(ctx context.Context) (processhost.Snapshot, error) { return c.handle.Wait(ctx, c.binding) }}
	} else {
		c := newProcessCodexFixture(t, nil)
		c.turn(t, "unfinished", "controls")
		f = processResumableFixture{root: c.root, path: c.path, store: c.store, binding: c.endpoint.binding, observe: func(t *testing.T) processhost.Snapshot {
			return c.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		}, stop: func() error { return c.endpoint.handle.Stop(c.endpoint.binding) }, wait: func(ctx context.Context) (processhost.Snapshot, error) {
			return c.endpoint.handle.Wait(ctx, c.endpoint.binding)
		}}
	}
	// Persist identities while admission is open; Stop expires live controls.
	// The record deliberately retains the interrupted generation for resume.
	s := f.observe(t)
	_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, _ := reg.Pane(f.binding.Pane)
		r := p.Status.ProcessSession
		r.TurnID = s.Turn
		r.Pending = nil
		for _, q := range s.Pending {
			r.Pending = append(r.Pending, coremetadata.ProcessRecordedControl{ID: q.ID, Kind: q.Kind, ConnectionID: q.Connection, SessionID: q.Session, TurnID: q.Turn})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestProcessSupportedResumableProviderShutdownFixture(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newProcessResumableFixture(t, provider)
			before, err := f.store.LoadDegradedReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			p, _ := before.Pane(f.binding.Pane)
			activation := *p.Status.Activation.Process
			interrupted := p.Status.ProcessSession.Clone()
			if err := f.stop(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := f.wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			receipt, ok := s.Termination(time.Now())
			if !ok {
				t.Fatal("no actual child Wait", s)
			}
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				// The foreground termination writer is a separate consumer. Model
				// its committed postcondition here; resumable cannot perform it.
				pane, _ := reg.Pane(f.binding.Pane)
				agent, _ := reg.Agent(f.binding.Agent)
				pane.Status.Activation = coremetadata.PaneActivation{}
				pane.Status.LastTermination = receipt.Clone()
				agent.Status.Phase = coremetadata.PhaseOffline
				agent.Status.LastTermination = receipt.Clone()
				return intmetadata.DefaultMutator().RecordProcessResumable(reg, activation, &receipt)
			})
			if err != nil {
				t.Fatal(err)
			}
			reg, err := f.store.LoadDegradedReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			got := listResumableProcessAgents(reg, processResumeFilter{})
			if len(got) != 1 || got[0].Agent.Metadata.UID != f.binding.Agent || got[0].Previous.InterruptedTurn != interrupted.TurnID || !reflect.DeepEqual(got[0].Previous.Expired, interrupted.Pending) {
				t.Fatalf("durable candidate lost interrupted generation: %+v", got)
			}
			interrupted.ResumeState = coremetadata.ProcessResumable
			if !reflect.DeepEqual(&got[0].Record, interrupted) {
				t.Fatal("changed recorded identities or history")
			}
			if err := processResumeRefusal(reg, f.binding.Agent, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A separate copied test owner lets SIGKILL close the actual lifetime pipe.
// No finalizer or simulated Wait can write resumable after this owner dies.
func TestProcessSupportedResumableKilledOwnerHelper(t *testing.T) {
	provider := os.Getenv("PMX_TEST_RESUMABLE_OWNER")
	if provider == "" {
		return
	}
	f := newProcessResumableFixture(t, provider)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(f.binding.Pane)
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Root, Path string
		Activation coremetadata.ProcessActivation
		Snapshot   processhost.Snapshot
	}{f.root, f.path, *pane.Status.Activation.Process, f.observe(t)}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestProcessSupportedResumableOwnerKillRemainsUnknownFixture(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "owner.test")
			raw, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary, raw, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestProcessSupportedResumableKilledOwnerHelper$")
			for _, v := range os.Environ() {
				key, _, _ := strings.Cut(v, "=")
				if key == "HOME" || key == "CODEX_HOME" || key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "XDG_") {
					continue
				}
				cmd.Env = append(cmd.Env, v)
			}
			cmd.Env = append(cmd.Env, "HOME="+root, "CODEX_HOME="+root+"/codex", "PMX_TEST_RESUMABLE_OWNER="+provider)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = stdin.Close()
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			var ready struct {
				Root, Path string
				Activation coremetadata.ProcessActivation
				Snapshot   processhost.Snapshot
			}
			if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
				t.Fatal(err)
			}
			t.Logf("isolated killed-owner provider HOME=%s", ready.Root)
			defer os.RemoveAll(ready.Root)
			supervisor, _, err := localipc.Process(ready.Snapshot.SupervisorPID)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("owner KILL did not terminate")
			}
			waited = true
			store := intmetadata.NewStore(ready.Path)
			reg, err := store.LoadDegradedReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			p, _ := reg.Pane(ready.Activation.Binding.PaneUID)
			if p.Status.ProcessSession.ResumeState != coremetadata.ProcessResumeUnknown || len(listResumableProcessAgents(reg, processResumeFilter{})) != 0 {
				t.Fatal("owner death fabricated resumability")
			}
			before := reg.Clone()
			if err := (coremetadata.Mutator{}).RecordProcessResumable(&reg, ready.Activation, nil); !errors.Is(err, coremetadata.ErrInvalidRegistry) || !reflect.DeepEqual(reg, before) {
				t.Fatal("missing Wait promoted unknown", err)
			}
			for _, identity := range []coremetadata.ProcessIdentity{ready.Activation.Child, supervisor} {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					current, _, err := localipc.Process(identity.PID)
					if err != nil || current != identity {
						break
					}
					time.Sleep(time.Millisecond * 10)
				}
				if current, _, err := localipc.Process(identity.PID); err == nil && current == identity {
					t.Fatalf("owned process remains: %+v", identity)
				}
			}
		})
	}
}
