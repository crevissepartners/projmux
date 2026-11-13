package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// mixedDeleteRegistry is the tmux Project alpha of deleteSDKFixture plus a
// second Project beta with a process Window "main". The process Agent "codex"
// and its Pane "codex-pane" share their names with alpha's tmux Agent and Pane,
// so one name Ref reaches both runtimes. "bare" is a second Agent of that Window that
// owns no Pane.
func mixedDeleteRegistry(t *testing.T, barePhase metadata.AgentPhase) (metadata.Registry, string, string) {
	t.Helper()
	reg, _ := deleteSDKFixture(t)
	m := store.DefaultMutator()
	root := filepath.Join(t.TempDir(), "beta")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := m.RegisterProject(&reg, metadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/sh", OperationID: "op-beta"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := metadata.AgentWorkspace{CWD: root}
	window, _, processPane, err := m.CreateProcessWindow(&reg, project.Project.Metadata.UID, metadata.BootstrapWindow{Name: "main"},
		metadata.CreateAgentOptions{Name: "codex", Provider: "claude", Workspace: workspace, OperationID: "op-pw"},
		metadata.ProcessBinding{HostInstanceID: "host-mixed", Generation: "gen-mixed", OperationID: "op-pw"})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := m.CreateAgent(&reg, window.Metadata.UID, metadata.CreateAgentOptions{Name: "bare", Provider: "claude", Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for i := range reg.Agents {
		if reg.Agents[i].Metadata.UID == bare.Metadata.UID {
			reg.Agents[i].Status.Phase = barePhase
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, bare.Metadata.UID, processPane.Metadata.UID
}

// mixedDeleteTmux starts the real tmux server of Project alpha on a fresh
// absolute socket and mirrors alpha's tmux Panes. Process resources have no
// tmux mirror. It returns the socket and the tmux command runner.
func mixedDeleteTmux(t *testing.T, reg metadata.Registry) (string, func(args ...string) string) {
	t.Helper()
	root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-mix-")
	if err != nil {
		t.Fatal(err)
	}
	if st, e := os.Stat(root); e != nil || !st.IsDir() {
		t.Fatal("socket directory missing")
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "s")
	runner := tmux.ExecRunner{}
	run := func(args ...string) string {
		t.Helper()
		out, e := runner.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "-n", "main", "tail -f /dev/null")
	run("new-window", "-d", "-t", "alpha", "-n", "review", "tail -f /dev/null")
	run("set-option", "-g", tmuxopts.AppGlobal, "1")
	run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
	run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, "prj-alpha")
	run("set-option", "-w", "-t", "alpha:main", tmuxopts.WindowUID, "win-alpha-main")
	run("set-option", "-w", "-t", "alpha:review", tmuxopts.WindowUID, "win-alpha-review")
	t.Cleanup(func() {
		if run("display-message", "-p", "#{socket_path}") != socket {
			t.Error("cleanup route proof")
			return
		}
		run("kill-server")
	})
	mainPane := run("display-message", "-p", "-t", "alpha:main", "#{pane_id}")
	for _, p := range reg.Panes {
		if !strings.HasPrefix(p.Metadata.UID, "pan-alpha-") {
			continue
		}
		paneID := mainPane
		switch p.Metadata.UID {
		case "pan-alpha-zsh":
		case "pan-alpha-review":
			paneID = run("display-message", "-p", "-t", "alpha:review", "#{pane_id}")
		default:
			paneID = run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "alpha:main", "tail -f /dev/null")
		}
		run("set-option", "-pq", "-t", paneID, tmuxopts.PaneUID, p.Metadata.UID)
	}
	return socket, run
}

// Each form is judged against the CLI on the same Registry and the same real
// tmux server. A refusal must carry the CLI's text byte for byte, the code of
// the plan shape, and change nothing. An accepted form must leave the same
// Registry, the same deletion record, and the same receipt as the CLI.
func TestDeleteMixedPlanSDKCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name  string
		kind  core.Kind
		bare  bool // Ref is the exact uid of the Pane-less Agent
		ref   string
		phase metadata.AgentPhase
		// refused is the code the SDK must return; empty means the plan is accepted.
		refused projmux.Code
	}{
		{name: "tmux-pane-and-process-pane", kind: core.KindPane, ref: "codex-pane", phase: metadata.PhaseOffline, refused: projmux.CodeDeleteProcessUnsupported},
		{name: "tmux-agent-and-process-agent", kind: core.KindAgent, ref: "codex", phase: metadata.PhaseOffline, refused: projmux.CodeDeleteProcessUnsupported},
		{name: "offline-paneless-agent", kind: core.KindAgent, bare: true, phase: metadata.PhaseOffline},
		{name: "failed-paneless-agent", kind: core.KindAgent, bare: true, phase: metadata.PhaseFailed},
		{name: "pending-paneless-agent", kind: core.KindAgent, bare: true, phase: metadata.PhasePending, refused: projmux.CodeDeleteProcessUnsupported},
		{name: "offline-paneless-agent-by-name", kind: core.KindAgent, ref: "bare", phase: metadata.PhaseOffline, refused: projmux.CodeDeleteProcessUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, bareUID, processPane := mixedDeleteRegistry(t, tc.phase)
			req := core.DeleteRequest{Kind: tc.kind, Ref: tc.ref}
			if tc.bare {
				req.Ref = "uid:" + bareUID
			}
			if tc.refused != "" {
				state, _, c := renameState(t, reg)
				req.SocketPath, _ = mixedDeleteTmux(t, reg)
				before, _ := os.ReadFile(store.PathFor(state))
				for _, dry := range []bool{true, false} {
					req.DryRun, req.Confirm = dry, !dry
					_, _, cliErr := sdkDeleteCLI(t, state, req)
					result, sdkErr := c.Delete(context.Background(), req)
					var refusal *projmux.Refusal
					if cliErr == nil || !errors.As(sdkErr, &refusal) || refusal.Code != tc.refused || result.Committed {
						t.Fatalf("dry=%v cli=%v sdk=%+v %v", dry, cliErr, result, sdkErr)
					}
					if cliErr.Error() != sdkErr.Error() {
						t.Fatalf("dry=%v SDK refusal text differs from the CLI:\ncli: %q\nsdk: %q", dry, cliErr.Error(), sdkErr.Error())
					}
					if after, _ := os.ReadFile(store.PathFor(state)); !bytes.Equal(before, after) {
						t.Fatal("a refused delete changed the Registry")
					}
				}
				return
			}
			var snapshots, receipts []string
			for _, sdk := range []bool{false, true} {
				state, _, c := renameState(t, reg)
				socket, run := mixedDeleteTmux(t, reg)
				req.SocketPath, req.DryRun, req.Confirm = socket, true, false
				before, _ := os.ReadFile(store.PathFor(state))
				flags := resourceQueryFlags{kind: metadata.Kind(tc.kind), agents: []string{req.Ref}}
				resolution, e := flags.resolve(selector.VerbDelete, false, reg)
				if e != nil {
					t.Fatal(e)
				}
				plan := buildDeletePlan(reg, metadata.Kind(tc.kind), resolution)
				plan.ExactUID = tc.bare
				target, e := mutationruntime.ResolveDeleteTarget("delete agent", "", socket, nil)
				if e != nil {
					t.Fatal(e)
				}
				cliRuntime := &tmuxPaneDeleteRuntime{TmuxPaneDeleteRuntime: mutationruntime.TmuxPaneDeleteRuntime{Runner: tmux.ExecRunner{}, Getenv: func(string) string { return "" }}}
				cliRuntime.UseExactTarget(target)
				cliLive, e := cliRuntime.Preflight(context.Background(), reg, plan)
				if e != nil {
					t.Fatal(e)
				}
				preview, e := c.Delete(context.Background(), req)
				if e != nil {
					t.Fatalf("SDK dry-run: %v", e)
				}
				if after, _ := os.ReadFile(store.PathFor(state)); !bytes.Equal(before, after) {
					t.Fatal("dry-run wrote")
				}
				if len(cliLive.RegistryOnly) != 1 || sdkJSON(t, preview.RegistryOnly) != sdkJSON(t, cliLive.RegistryOnly) ||
					sdkJSON(t, preview.Plan) != sdkJSON(t, mutations.PreviewDelete(mutations.DeletePlan(plan))) {
					t.Fatalf("SDK/CLI dry-run plan differs: %s vs %s", sdkJSON(t, preview.RegistryOnly), sdkJSON(t, cliLive.RegistryOnly))
				}
				req.DryRun, req.Confirm = false, true
				if sdk {
					result, e := c.Delete(context.Background(), req)
					if e != nil || !result.Committed {
						t.Fatalf("SDK %+v %v", result, e)
					}
					receipts = append(receipts, sdkJSON(t, result.Receipt))
				} else {
					out, warnings, e := sdkDeleteCLI(t, state, req)
					receipts = append(receipts, sdkJSON(t, childDeleteReceipt(metadata.Kind(tc.kind), plan, windowLiveDeletePlan{}, cliLive, false)))
					if e != nil || warnings != "" {
						t.Fatalf("CLI: %s %s %v", out, warnings, e)
					}
				}
				after := projectRegistry(t, state)
				if err := after.Validate(); err != nil {
					t.Fatal(err)
				}
				if _, ok := after.Agent(bareUID); ok {
					t.Fatal("the Agent survived its delete")
				}
				if _, ok := after.Pane(processPane); !ok {
					t.Fatal("the sibling process Pane was removed")
				}
				data, _ := os.ReadFile(store.PathFor(state))
				record, e := os.ReadFile(filepath.Join(state, mutations.DeletionRecordsFile))
				if e != nil {
					t.Fatal(e)
				}
				snapshots = append(snapshots, normalizeProjectJSON(t, string(data)+run("list-panes", "-a", "-F", "#{session_name}|#{window_name}|#{@projmux_pane_uid}"), after)+normalizeDeletionRows(string(record)))
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("SDK/CLI differs:\n%s\n%s", snapshots[0], snapshots[1])
			}
			if len(receipts) != 2 || receipts[0] != receipts[1] {
				t.Fatalf("SDK/CLI receipt differs:\n%s\n%s", receipts[0], receipts[1])
			}
		})
	}
}

// The CLI deletes only the Registry records of a plan that picks a tmux Window
// together with a process Window, or of a tmux Window that holds a process Pane,
// and leaves the running provider behind. The SDK refuses these plans before
// any effect, with the code and text it always had. A plan of process Windows
// alone is not mixed and is not covered here.
func TestSDKDeleteStillRefusesWindowsHoldingProcessResources(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	t.Setenv("HOME", t.TempDir())
	reg, _, _ := mixedDeleteRegistry(t, metadata.PhaseOffline)
	state, _, c := renameState(t, reg)
	socket, _ := mixedDeleteTmux(t, reg)
	before, _ := os.ReadFile(store.PathFor(state))
	for _, dry := range []bool{true, false} {
		// Ref "main" names alpha's tmux Window and beta's process Window.
		result, err := c.Delete(context.Background(), core.DeleteRequest{Kind: core.KindWindow, Ref: "main", SocketPath: socket, DryRun: dry, Confirm: !dry})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeDeleteProcessUnsupported || result.Committed ||
			!strings.Contains(err.Error(), "Delete cannot remove process Window uid:") || !strings.Contains(err.Error(), "mixes tmux and process resources") {
			t.Fatalf("dry=%v: %+v %v", dry, result, err)
		}
		if after, _ := os.ReadFile(store.PathFor(state)); !bytes.Equal(before, after) {
			t.Fatal("a refused delete changed the Registry")
		}
	}
}
