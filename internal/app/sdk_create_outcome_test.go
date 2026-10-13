package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func publicOutcomeDiagnostics(o projmux.CreateOutcome) diagnostics.CreateOutcome {
	return diagnostics.CreateOutcome{Kind: diagnostics.CreateKind(o.Kind), Result: diagnostics.LifecycleResult(o.Result), Duration: o.Duration, LockHeld: o.LockHeld, Phases: diagnostics.CreatePhases(o.Phases), SpawnToRelease: o.SpawnToRelease}
}

// Exercise all four SDK transaction entrypoints on real isolated tmux, using a
// failing sink. At callback time the Store must be unlocked and compensation
// complete, including a split/new-window that succeeded before returning error.
func TestSDKCreateOutcomesAfterUnlockAndRollback(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []projmux.CreateKind{projmux.CreateKindWindow, projmux.CreateKindPane, projmux.CreateKindAgent, projmux.CreateKindResume} {
		for _, failed := range []bool{false, true} {
			t.Run(string(kind)+strconv.FormatBool(failed), func(t *testing.T) {
				root, state, config := t.TempDir(), filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, run := sdkCreateSocket(t)
				options := projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary}
				client, e := projmux.NewClient(options)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); e != nil {
					t.Fatal(e)
				}
				w, e := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "existing"})
				if e != nil {
					t.Fatal(e)
				}
				uid := ""
				if kind == projmux.CreateKindResume {
					uid = seedSDKResumeAgent(t, state, w.Window.Metadata.UID, "claude", root, metadata.PhaseOffline)
				}
				before := projectRegistryBytes(t, state)
				panes := run("list-panes", "-a", "-F", "#{pane_id}")
				journal := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
				recorder := diagnostics.NewLifecycleRecorder(journal, "sdk-outcomes", "test", "tmux").Create()
				calls := 0
				options.CreateRecorder = func(o projmux.CreateOutcome) error {
					calls++
					// A rejected no-op update acquires and releases the very same file lock,
					// without changing bytes. A callback under the lock would time out.
					stop := errors.New("lock probe")
					_, probeErr := store.NewStore(store.PathFor(state)).Update(func(*metadata.Registry) error { return stop })
					if !errors.Is(probeErr, stop) {
						t.Errorf("callback under lock: %v", probeErr)
					}
					if failed && (projectRegistryBytes(t, state) != before || run("list-panes", "-a", "-F", "#{pane_id}") != panes) {
						t.Error("callback ran before rollback")
					}
					if o.Kind != kind || (o.Result == projmux.CreateError) != failed || o.LockHeld == nil || *o.LockHeld > o.Duration {
						t.Errorf("wrong outcome %+v", o)
					}
					if !failed {
						for _, phase := range o.Phases {
							if phase == nil {
								t.Error("missing success phase")
							}
						}
						spawns := kind == projmux.CreateKindAgent || kind == projmux.CreateKindResume
						if (o.SpawnToRelease != nil) != spawns {
							t.Errorf("spawn timing for %s: %v", kind, o.SpawnToRelease)
						}
					}
					recorder.Record(publicOutcomeDiagnostics(o))
					return errors.New("sink failed after append")
				}
				client, e = projmux.NewClient(options)
				if e != nil {
					t.Fatal(e)
				}
				oldPath := os.Getenv("PATH")
				if failed {
					shim := t.TempDir()
					command := "split-window"
					if kind == projmux.CreateKindWindow {
						command = "new-window"
					}
					script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = " + command + " ]; then " + strconv.Quote(realTmux) + " \"$@\"; exit 12; fi\ndone\nexec " + strconv.Quote(realTmux) + " \"$@\"\n"
					if e = os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0700); e != nil {
						t.Fatal(e)
					}
					t.Setenv("PATH", shim+string(os.PathListSeparator)+oldPath)
				}
				switch kind {
				case projmux.CreateKindWindow:
					_, e = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "recorded"})
				case projmux.CreateKindPane:
					_, e = client.CreatePane(context.Background(), core.CreatePaneRequest{Project: "sample", Window: "existing", Name: "recorded"})
				case projmux.CreateKindAgent:
					_, e = client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "existing", Name: "recorded", Provider: "antigravity"})
				case projmux.CreateKindResume:
					_, e = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid})
				}
				t.Setenv("PATH", oldPath)
				if (e != nil) != failed || calls != 1 {
					t.Fatalf("recorder changed return/count: %v calls=%d", e, calls)
				}
				events, e := journal.Read()
				if e != nil || len(events) != 1 {
					t.Fatalf("journal: %+v %v", events, e)
				}
				if events[0].Operation != string(kind) || (events[0].Result == "error") != failed {
					t.Fatalf("journal meaning: %+v", events[0])
				}
			})
		}
	}
}

func TestSDKCreateOutcomeJournalMatchesCLIAdapter(t *testing.T) {
	public := []projmux.CreatePhase{projmux.CreatePhaseGuard, projmux.CreatePhaseFirstReconcile, projmux.CreatePhaseOperation, projmux.CreatePhaseSecondReconcile, projmux.CreatePhaseReprove, projmux.CreatePhaseStoreWrite}
	cli := []mutationruntime.CreatePhase{mutationruntime.CreatePhaseGuard, mutationruntime.CreatePhaseFirstReconcile, mutationruntime.CreatePhaseOperation, mutationruntime.CreatePhaseSecondReconcile, mutationruntime.CreatePhaseReprove, mutationruntime.CreatePhaseStoreWrite}
	for i := range public {
		if int(public[i]) != int(cli[i]) {
			t.Fatal("phase enum drift")
		}
	}
	for _, kind := range []projmux.CreateKind{projmux.CreateKindWindow, projmux.CreateKindPane, projmux.CreateKindAgent, projmux.CreateKindResume} {
		for _, result := range []projmux.CreateResult{projmux.CreateSuccess, projmux.CreateError} {
			held, spawn := 21*time.Millisecond, 7*time.Millisecond
			o := projmux.CreateOutcome{Kind: kind, Result: result, Duration: 30 * time.Millisecond, LockHeld: &held}
			for i := range o.Phases {
				d := time.Duration(i+1) * time.Millisecond
				o.Phases[i] = &d
			}
			if kind == projmux.CreateKindAgent || kind == projmux.CreateKindResume {
				o.SpawnToRelease = &spawn
			}
			a, b := diagnostics.NewStore(filepath.Join(t.TempDir(), "a.jsonl")), diagnostics.NewStore(filepath.Join(t.TempDir(), "b.jsonl"))
			diagnostics.NewLifecycleRecorder(a, "parity", "test", "tmux").Create().Record(publicOutcomeDiagnostics(o))
			createOutcomeRecorder{diagnostics.NewLifecycleRecorder(b, "parity", "test", "tmux").Create()}.Record(mutationruntime.CreateOutcome{Kind: mutationruntime.CreateKind(kind), Result: string(result), Duration: o.Duration, LockHeld: o.LockHeld, Phases: mutationruntime.CreatePhases(o.Phases), SpawnToRelease: o.SpawnToRelease})
			x, e := a.Read()
			if e != nil || len(x) != 1 {
				t.Fatal(e, x)
			}
			y, e := b.Read()
			if e != nil || len(y) != 1 {
				t.Fatal(e, y)
			}
			x[0].At = ""
			y[0].At = ""
			if !reflect.DeepEqual(x, y) {
				t.Fatalf("CLI/public journal drift: %+v / %+v", x, y)
			}
		}
	}
	// Public phase names must remain aligned with the diagnostic field vocabulary.
	if strings.Join([]string{string(projmux.CreateKindWindow), string(projmux.CreateKindPane), string(projmux.CreateKindAgent), string(projmux.CreateKindResume)}, ",") != "window,pane,agent,resume" {
		t.Fatal("kind drift")
	}
}
