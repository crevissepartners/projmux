package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// These fixtures have eligible Offline Agent anchors, with no live provider or tmux projection.
func primaryAgentDeleteFixture(t *testing.T, runtime metadata.RuntimeKind) (metadata.Registry, string, []string) {
	t.Helper()
	root := t.TempDir()
	m := store.DefaultMutator()
	reg := metadata.NewRegistry()
	registered, err := m.RegisterProject(&reg, metadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/sh", OperationID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	projectUID := registered.Project.Metadata.UID
	for _, window := range reg.WindowsOf(projectUID) {
		if err := m.DeleteWindow(&reg, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	var windows []string
	for i := range 3 {
		name := fmt.Sprintf("agent-window-%d", i)
		window, agent, pane, err := m.CreateProcessWindow(&reg, projectUID, metadata.BootstrapWindow{Name: name}, metadata.CreateAgentOptions{
			Name: name, Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: root}, OperationID: name,
		}, metadata.ProcessBinding{HostInstanceID: "fixture-host", Generation: name, OperationID: name})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := reg.Agent(agent.Metadata.UID)
		a.Status.Phase = metadata.PhaseOffline
		p, _ := reg.Pane(pane.Metadata.UID)
		p.Status.Activation = metadata.PaneActivation{}
		p.Status.ProcessSession = nil
		p.Spec.Runtime.Kind = runtime
		windows = append(windows, window.Metadata.UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return reg, projectUID, windows
}

func TestDeletePrimaryAgentWindowSDKCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	t.Setenv("HOME", t.TempDir())
	// Intentionally absent server: both adapters must commit registry-only
	// tmux Window deletion without starting a shell or provider.
	socketRoot, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "pa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "s")
	for _, runtime := range []metadata.RuntimeKind{metadata.RuntimeTmux, metadata.RuntimeProcess} {
		reg, projectUID, windows := primaryAgentDeleteFixture(t, runtime)
		for _, sdk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sdk=%t", runtime, sdk), func(t *testing.T) {
				state, _, client := renameState(t, reg)
				path := store.PathFor(state)
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				req := core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + windows[0], SocketPath: socket, DryRun: true}
				for _, commit := range []bool{false, true} {
					req.DryRun, req.Confirm = !commit, commit
					if sdk {
						result, err := client.Delete(context.Background(), req)
						if runtime == metadata.RuntimeProcess {
							// Keep the existing SDK boundary: direct process Window
							// deletion is unsupported, not a new feature of this fix.
							var refusal *projmux.Refusal
							if !errors.As(err, &refusal) || refusal.Code != projmux.CodeDeleteProcessUnsupported || result.Committed {
								t.Fatalf("process SDK boundary: %+v %v", result, err)
							}
							after, err := os.ReadFile(path)
							if err != nil || !bytes.Equal(before, after) {
								t.Fatalf("process refusal changed Registry: %v", err)
							}
							continue
						}
						if err != nil || result.Committed != commit || len(result.Plan.Targets) != 1 {
							t.Fatalf("SDK delete: %+v %v", result, err)
						}
					} else {
						out, stderr, err := sdkDeleteCLI(t, state, req)
						if err != nil || stderr != "" {
							t.Fatalf("CLI delete: %q %q %v", out, stderr, err)
						}
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !commit && !bytes.Equal(before, after) {
						t.Fatal("dry-run changed Registry bytes")
					}
					saved, err := store.NewStore(path).LoadReadOnly()
					if err != nil {
						t.Fatal(err)
					}
					if err := saved.Validate(); err != nil {
						t.Fatal(err)
					}
					want := windows[0]
					if commit {
						want = windows[1]
						if _, ok := saved.Window(windows[0]); ok {
							t.Fatal("committed target remains")
						}
					}
					project, _ := saved.Project(projectUID)
					if project.Spec.PrimaryWindowRef != want {
						t.Fatalf("primary = %q, want %q", project.Spec.PrimaryWindowRef, want)
					}
				}
			})
		}
	}
}

func TestDeletePrimaryAgentWindowMultipleTargets(t *testing.T) {
	liveguard.RequireActive(t)
	t.Setenv("HOME", t.TempDir())
	reg, projectUID, windows := primaryAgentDeleteFixture(t, metadata.RuntimeProcess)
	state, resources, _ := renameState(t, reg)
	path := store.PathFor(state)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := newDeleteCommand()
	c.store = resources
	c.lookupEnv = func(string) string { return "" }
	c.activeTarget = func() (activeTargetObserver, bool) { return activeTargetObserver{}, false }
	for _, commit := range []bool{false, true} {
		args := []string{"window", "uid:" + windows[0], "uid:" + windows[1]}
		if commit {
			args = append(args, "--yes")
		} else {
			args = append(args, "--dry-run")
		}
		var out, stderr bytes.Buffer
		if err := c.Run(args, &out, &stderr); err != nil {
			t.Fatalf("multiple targets: %s %s %v", &out, &stderr, err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !commit && !bytes.Equal(before, after) {
			t.Fatal("multiple-target dry-run changed Registry bytes")
		}
		saved, err := store.NewStore(path).LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		if err := saved.Validate(); err != nil {
			t.Fatal(err)
		}
		want, count := windows[0], 3
		if commit {
			want, count = windows[2], 1
		}
		project, _ := saved.Project(projectUID)
		if project.Spec.PrimaryWindowRef != want || len(saved.WindowsOf(projectUID)) != count {
			t.Fatalf("primary %q, Windows %d; want %q, %d", project.Spec.PrimaryWindowRef, len(saved.WindowsOf(projectUID)), want, count)
		}
	}
}
