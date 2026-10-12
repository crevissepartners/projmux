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

// These fixtures use persistent idle shells, never real providers. Every tmux
// command, including cleanup, names the pre-created absolute socket path.
func TestDeletePaneAgentSDKCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name  string
		kind  core.Kind
		ref   string
		last  bool
		multi bool
	}{
		{"leaf", core.KindPane, "log", false, false},
		{"anchor", core.KindPane, "zsh", false, false},
		{"agent-owned-pane", core.KindPane, "codex-pane", false, false},
		{"agent", core.KindAgent, "codex", false, false},
		{"last-pane-replacement", core.KindPane, "codex-pane", true, false},
		{"last-agent-replacement", core.KindAgent, "codex", true, false},
		{"one-ref-many-panes", core.KindPane, "log", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := deleteSDKFixture(t)
			for i := range reg.Panes {
				reg.Panes[i].Spec.CWD = t.TempDir()
			}
			if tc.last {
				panes := reg.Panes[:0]
				for _, p := range reg.Panes {
					if p.Metadata.UID == "pan-alpha-codex" || p.Metadata.UID == "pan-alpha-review" {
						panes = append(panes, p)
					}
				}
				reg.Panes = panes
				reg.Windows[0].Spec.AnchorPaneRef = "pan-alpha-codex"
				reservations := reg.NameReservations[:0]
				for _, r := range reg.NameReservations {
					if r.UID != "pan-alpha-zsh" && r.UID != "pan-alpha-log" {
						reservations = append(reservations, r)
					}
				}
				reg.NameReservations = reservations
			}
			if tc.multi {
				project := reg.Projects[0].Clone()
				project.Metadata.UID = "prj-beta-name"
				project.Metadata.Name = "beta"
				project.Spec.Root = t.TempDir()
				project.Spec.PrimaryWindowRef = "win-beta-name"
				project.Status.Session.Name = "beta"
				window := reg.Windows[0].Clone()
				window.Metadata.UID = "win-beta-name"
				window.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindProject, UID: project.Metadata.UID}
				window.Spec.AnchorPaneRef = "pan-beta-name"
				pane := reg.Panes[1].Clone()
				pane.Metadata.UID = "pan-beta-name"
				pane.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: window.Metadata.UID}
				pane.Spec.CWD = project.Spec.Root
				reg.Projects = append(reg.Projects, project)
				reg.Windows = append(reg.Windows, window)
				reg.Panes = append(reg.Panes, pane)
				reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Kind: metadata.KindProject, Name: "beta", UID: project.Metadata.UID}, metadata.NameReservation{Kind: metadata.KindWindow, Scope: project.Metadata.UID, Name: window.Metadata.Name, UID: window.Metadata.UID}, metadata.NameReservation{Kind: metadata.KindPane, Scope: project.Metadata.UID, Name: pane.Metadata.Name, UID: pane.Metadata.UID})
			}
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			var snapshots, receipts []string
			for _, sdk := range []bool{false, true} {
				state, _, c := renameState(t, reg)
				root, rootErr := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-pane-")
				if rootErr != nil {
					t.Fatal(rootErr)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				})
				if st, e := os.Stat(root); e != nil || !st.IsDir() {
					t.Fatal("socket root absent")
				}
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
				if tc.multi {
					run("new-session", "-d", "-s", "beta", "-n", "main", "tail -f /dev/null")
					run("set-option", "-t", "beta", tmuxopts.ProjectUIDSession, "prj-beta-name")
					run("set-option", "-w", "-t", "beta:main", tmuxopts.WindowUID, "win-beta-name")
				}
				mainPane := run("display-message", "-p", "-t", "alpha:main", "#{pane_id}")
				for _, p := range reg.Panes {
					paneID := mainPane
					if p.Metadata.UID == "pan-beta-name" {
						paneID = run("display-message", "-p", "-t", "beta:main", "#{pane_id}")
					} else if p.Metadata.UID == "pan-alpha-review" {
						paneID = run("display-message", "-p", "-t", "alpha:review", "#{pane_id}")
					} else if (tc.last && p.Metadata.UID != "pan-alpha-codex") || (!tc.last && p.Metadata.UID != "pan-alpha-zsh") {
						paneID = run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "alpha:main", "tail -f /dev/null")
					}
					run("set-option", "-pq", "-t", paneID, tmuxopts.PaneUID, p.Metadata.UID)
				}
				t.Cleanup(func() {
					out, e := runner.Run(context.Background(), "tmux", "-S", socket, "display-message", "-p", "#{socket_path}")
					if e != nil || strings.TrimSpace(string(out)) != socket {
						t.Errorf("cleanup route proof: %v", e)
						return
					}
					_, e = runner.Run(context.Background(), "tmux", "-S", socket, "kill-server")
					if e != nil {
						t.Error(e)
					}
				})
				req := core.DeleteRequest{Kind: tc.kind, Ref: tc.ref, Project: "alpha", Window: "main", SocketPath: socket, DryRun: true}
				if tc.multi {
					req.Project = ""
					req.Window = ""
				}
				t.Setenv("TMUX", socket+",999,1")
				t.Setenv("TMUX_PANE", mainPane)
				t.Setenv(runtimeMutationAnchorPaneEnv, mainPane)
				before, _ := os.ReadFile(store.PathFor(state))
				preview, e := c.Delete(context.Background(), req)
				if e != nil {
					t.Fatal(e)
				}
				after, _ := os.ReadFile(store.PathFor(state))
				if !bytes.Equal(before, after) {
					t.Fatal("dry-run wrote")
				}
				wantTargets := 1
				if tc.multi {
					wantTargets = 2
				}
				if len(preview.Panes) != wantTargets || preview.Panes[0].Self {
					t.Fatalf("SDK caller or live plan: %+v", preview)
				}
				flags := resourceQueryFlags{kind: metadata.Kind(tc.kind)}
				if req.Project != "" {
					flags.projects = repeatedFlag{req.Project}
				}
				if req.Window != "" {
					flags.windows = repeatedFlag{req.Window}
				}
				if tc.kind == core.KindPane {
					flags.panes = repeatedFlag{tc.ref}
				} else {
					flags.agents = []string{tc.ref}
				}
				resolution, e := flags.resolve(selector.VerbDelete, false, reg)
				if e != nil {
					t.Fatal(e)
				}
				plan := buildDeletePlan(reg, metadata.Kind(tc.kind), resolution)
				if sdkJSON(t, preview.Plan) != sdkJSON(t, mutations.PreviewDelete(mutations.DeletePlan(plan))) {
					t.Fatal("SDK/CLI resource plan differs")
				}
				target, e := mutationruntime.ResolveDeleteTarget("delete "+strings.ToLower(string(tc.kind)), "", socket, nil)
				if e != nil {
					t.Fatal(e)
				}
				cliRuntime := &tmuxPaneDeleteRuntime{TmuxPaneDeleteRuntime: mutationruntime.TmuxPaneDeleteRuntime{Runner: runner, Getenv: func(string) string { return "" }}}
				cliRuntime.UseExactTarget(target)
				cliLive, e := cliRuntime.Preflight(context.Background(), reg, plan)
				if e != nil {
					t.Fatal(e)
				}
				if sdkJSON(t, preview.Panes) != sdkJSON(t, cliLive.Targets) {
					t.Fatal("SDK/CLI live plan differs")
				}
				cliOut, _, e := sdkDeleteCLI(t, state, req)
				if e != nil || !strings.Contains(cliOut, "dry-run") {
					t.Fatalf("CLI preview: %s %v", cliOut, e)
				}
				req.DryRun = false
				if tc.kind == core.KindAgent || tc.multi {
					denied, e := c.Delete(context.Background(), req)
					var r *projmux.Refusal
					if !errors.As(e, &r) || r.Code != projmux.CodeConfirmationRequired || denied.Committed {
						t.Fatalf("Confirm: %+v %v", denied, e)
					}
					after, _ = os.ReadFile(store.PathFor(state))
					if !bytes.Equal(before, after) {
						t.Fatal("confirmation refusal wrote")
					}
					req.Confirm = true
				}
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
				afterReg := projectRegistry(t, state)
				if err := afterReg.Validate(); err != nil {
					t.Fatal(err)
				}
				if tc.kind == core.KindPane && strings.Contains(tc.ref, "codex") {
					a, ok := afterReg.Agent("agt-alpha-codex")
					if !ok || a.Status.Phase != metadata.PhaseOffline || a.Status.PaneRef != "" {
						t.Fatalf("Agent Offline: %+v", a)
					}
				}
				if tc.last {
					w, _ := afterReg.Window("win-alpha-main")
					p, ok := afterReg.Pane(w.Spec.AnchorPaneRef)
					if !ok || p.Spec.Role != metadata.PaneRoleShell {
						t.Fatalf("replacement anchor: %+v", p)
					}
				}
				data, _ := os.ReadFile(store.PathFor(state))
				record, e := os.ReadFile(filepath.Join(state, mutations.DeletionRecordsFile))
				if e != nil {
					t.Fatal(e)
				}
				runtime := run("list-panes", "-a", "-F", "#{session_name}|#{window_name}|#{@projmux_window_uid}|#{@projmux_pane_uid}")
				snapshots = append(snapshots, normalizeProjectJSON(t, string(data)+runtime, afterReg)+normalizeDeletionRows(string(record)))
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("SDK/CLI differs:\n%s\n%s", snapshots[0], snapshots[1])
			}
			if len(receipts) != 2 || receipts[0] != receipts[1] {
				t.Fatal("SDK/CLI receipt differs")
			}
		})
	}
}
