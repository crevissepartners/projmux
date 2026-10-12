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

func sdkDeleteCLI(t *testing.T, state string, req core.DeleteRequest) (string, string, error) {
	t.Helper()
	storage := store.NewStore(store.PathFor(state))
	c := newDeleteCommand()
	c.store = &resourceStore{load: storage.LoadReadOnly, update: storage.Update, mutator: store.DefaultMutator, stateDir: func() (string, error) { return state, nil }}
	c.lookupEnv = func(string) string { return "" }
	c.windows = &tmuxWindowDeleteRuntime{Runner: tmux.ExecRunner{}}
	c.activeTarget = func() (activeTargetObserver, bool) { return activeTargetObserver{}, false }
	args := []string{"window", req.Ref}
	if req.Project != "" {
		args = append(args, "--project", req.Project)
	}
	if req.SocketPath != "" {
		args = append(args, "--socket-path", req.SocketPath)
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Confirm {
		args = append(args, "--yes")
	}
	var out, stderr bytes.Buffer
	err := c.Run(args, &out, &stderr)
	return out.String(), stderr.String(), err
}
func deleteSDKFixture(t *testing.T) (metadata.Registry, string) {
	t.Helper()
	reg := resourceFixtureRegistry(t)
	// One Project with two ordinary Windows; no provider is launched.
	reg.Projects = reg.Projects[:1]
	reg.Windows = reg.Windows[:2]
	reg.Agents = reg.Agents[:1]
	var panes []metadata.Pane
	for _, p := range reg.Panes {
		if p.Metadata.OwnerUID() == "win-alpha-main" || p.Metadata.OwnerUID() == "win-alpha-review" || p.Metadata.OwnerUID() == "agt-alpha-codex" {
			panes = append(panes, p)
		}
	}
	reg.Panes = panes
	var reservations []metadata.NameReservation
	for _, r := range reg.NameReservations {
		switch r.UID {
		case "prj-alpha", "win-alpha-main", "win-alpha-review", "pan-alpha-zsh", "pan-alpha-log", "pan-alpha-review", "pan-alpha-codex", "agt-alpha-codex":
			reservations = append(reservations, r)
		}
	}
	reg.NameReservations = reservations
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, "win-alpha-main"
}
func TestDeleteWindowSDKCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "absent-server"}[offline], func(t *testing.T) {
			reg, uid := deleteSDKFixture(t)
			var snapshots []string
			var receipts []string
			for _, sdk := range []bool{false, true} {
				state, _, _ := renameState(t, reg)
				root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-del-")
				if err != nil {
					t.Fatal(err)
				}
				if st, e := os.Stat(root); e != nil || !st.IsDir() {
					t.Fatal("socket directory missing")
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
				if !offline {
					run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "-n", "main", "tail -f /dev/null")
					run("new-window", "-d", "-t", "alpha", "-n", "review", "tail -f /dev/null")
					run("set-option", "-g", tmuxopts.AppGlobal, "1")
					run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
					run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, "prj-alpha")
					run("set-option", "-w", "-t", "alpha:main", tmuxopts.WindowUID, uid)
					run("set-option", "-w", "-t", "alpha:review", tmuxopts.WindowUID, "win-alpha-review")
					t.Cleanup(func() {
						if run("display-message", "-p", "#{socket_path}") != socket {
							t.Fatal("cleanup proof")
						}
						run("kill-server")
						_ = os.RemoveAll(root)
					})
				}
				req := core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + uid, SocketPath: socket, DryRun: true}
				c, e := projmux.NewClient(projmux.Options{StateDir: state, SocketPath: filepath.Join(root, "wrong")})
				if e != nil {
					t.Fatal(e)
				}
				// Poison runtime context, including the private anchor. SDK must ignore all.
				t.Setenv("TMUX", socket+",999,1")
				t.Setenv("TMUX_PANE", "%0")
				t.Setenv(runtimeMutationAnchorPaneEnv, "%0")
				before, _ := os.ReadFile(store.PathFor(state))
				preview, e := c.Delete(context.Background(), req)
				if e != nil {
					t.Fatal(e)
				}
				after, _ := os.ReadFile(store.PathFor(state))
				if !bytes.Equal(before, after) {
					t.Fatal("dry run wrote")
				}
				flags := resourceQueryFlags{kind: metadata.KindWindow, windows: repeatedFlag{req.Ref}}
				res, e := flags.resolve(selector.VerbDelete, false, reg)
				if e != nil {
					t.Fatal(e)
				}
				plan := buildDeletePlan(reg, metadata.KindWindow, res)
				plan.ExactUID = true
				if sdkJSON(t, preview.Plan) != sdkJSON(t, mutations.PreviewDelete(mutations.DeletePlan(plan))) {
					t.Fatal("preview differs")
				}
				req.DryRun = false
				denied, e := c.Delete(context.Background(), req)
				var refusal *projmux.Refusal
				if !errors.As(e, &refusal) || refusal.Code != projmux.CodeConfirmationRequired || len(denied.Plan.Targets) != 1 {
					t.Fatalf("confirm: %+v %v", denied, e)
				}
				after, _ = os.ReadFile(store.PathFor(state))
				if !bytes.Equal(before, after) {
					t.Fatal("confirm refusal wrote")
				}
				req.Confirm = true
				if sdk {
					// Options fallback, after request override was already exercised.
					c, e = projmux.NewClient(projmux.Options{StateDir: state, SocketPath: socket})
					if e != nil {
						t.Fatal(e)
					}
					req.SocketPath = ""
					result, e := c.Delete(context.Background(), req)
					if e != nil || !result.Committed {
						t.Fatalf("SDK %+v %v", result, e)
					}
					if len(result.Windows) > 0 && result.Windows[0].Self {
						t.Fatal("SDK queued self kill")
					}
					receipts = append(receipts, sdkJSON(t, result.Receipt))
				} else {
					_, warnings, e := sdkDeleteCLI(t, state, req)
					if e != nil || warnings != "" {
						t.Fatalf("CLI %v %s", e, warnings)
					}
					live := windowLiveDeletePlan{}
					if !offline {
						live.Targets = []windowLiveDeleteTarget{{UID: uid}}
					}
					receipts = append(receipts, sdkJSON(t, mutationruntime.ChildDeleteReceipt(metadata.KindWindow, plan, live, paneLiveDeletePlan{}, false)))
				}
				bytesReg, _ := os.ReadFile(store.PathFor(state))
				record, e := os.ReadFile(filepath.Join(state, mutations.DeletionRecordsFile))
				if e != nil {
					t.Fatal(e)
				}
				runtimeSnapshot := "absent"
				if !offline {
					runtimeSnapshot = run("list-windows", "-a", "-F", "#{window_name}|#{@projmux_window_uid}")
				}
				snapshots = append(snapshots, normalizeProjectJSON(t, string(bytesReg), projectRegistry(t, state))+normalizeDeletionRows(string(record))+runtimeSnapshot)
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("SDK/CLI snapshot differs:\n%s\n%s", snapshots[0], snapshots[1])
			}
			if receipts[0] != receipts[1] {
				t.Fatal("receipts differ")
			}
		})
	}
}
func TestDeleteWindowSDKRefusals(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	state, _, c := renameState(t, reg)
	before, _ := os.ReadFile(store.PathFor(state))
	for _, tc := range []struct {
		req      core.DeleteRequest
		code     projmux.Code
		equalCLI bool
	}{
		{core.DeleteRequest{Kind: core.KindWindow, Ref: "missing"}, projmux.CodeResourceNotFound, true},

		{core.DeleteRequest{Kind: core.KindWindow}, projmux.CodeSelectorInvalid, false},
		{core.DeleteRequest{Kind: core.KindPane, Ref: "zsh"}, projmux.CodeDeleteKindUnsupported, false},
		{core.DeleteRequest{Kind: core.KindAgent, Ref: "reader"}, projmux.CodeDeleteKindUnsupported, false},
		{core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:win-alpha-main"}, projmux.CodeDeleteTransportRequired, false},
		{core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:win-alpha-main", SocketPath: "relative"}, projmux.CodeRuntimeRequestInvalid, false},
	} {
		result, err := c.Delete(context.Background(), tc.req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code || result.Committed {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.equalCLI {
			_, _, cliErr := sdkDeleteCLI(t, state, tc.req)
			if cliErr == nil || cliErr.Error() != err.Error() {
				t.Fatalf("CLI=%v SDK=%v", cliErr, err)
			}
		}
		after, _ := os.ReadFile(store.PathFor(state))
		if !bytes.Equal(before, after) {
			t.Fatal("refusal wrote")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Delete(ctx, core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:win-alpha-main", Confirm: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDeleteWindowSDKSingleRefKeepsCLISetSemantics(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-del-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Fatal("socket directory missing")
	}
	reg := resourceFixtureRegistry(t)
	m := store.DefaultMutator()
	if _, err := m.RenameWindow(&reg, "win-gone-main", "other"); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "s")
	var snapshots []string
	for _, sdk := range []bool{false, true} {
		state, _, c := renameState(t, reg)
		before, _ := os.ReadFile(store.PathFor(state))
		req := core.DeleteRequest{Kind: core.KindWindow, Ref: "main", SocketPath: socket, DryRun: true}
		got, err := c.Delete(context.Background(), req)
		if err != nil || len(got.Plan.Targets) != 2 {
			t.Fatalf("%+v %v", got, err)
		}
		flags := resourceQueryFlags{kind: metadata.KindWindow, windows: repeatedFlag{"main"}}
		resolution, err := flags.resolve(selector.VerbDelete, false, reg)
		if err != nil {
			t.Fatal(err)
		}
		plan := buildDeletePlan(reg, metadata.KindWindow, resolution)
		if sdkJSON(t, got.Plan) != sdkJSON(t, mutations.PreviewDelete(mutations.DeletePlan(plan))) {
			t.Fatal("single-ref CLI set differs")
		}
		_, _, err = sdkDeleteCLI(t, state, req)
		if err != nil {
			t.Fatal(err)
		}
		req.DryRun = false
		refused, err := c.Delete(context.Background(), req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfirmationRequired || len(refused.Plan.Targets) != 2 {
			t.Fatalf("%+v %v", refused, err)
		}
		after, _ := os.ReadFile(store.PathFor(state))
		if !bytes.Equal(before, after) {
			t.Fatal("missing Confirm wrote")
		}
		req.Confirm = true
		if sdk {
			result, err := c.Delete(context.Background(), req)
			if err != nil || !result.Committed {
				t.Fatalf("%+v %v", result, err)
			}
		} else {
			_, warnings, err := sdkDeleteCLI(t, state, req)
			if err != nil || warnings != "" {
				t.Fatalf("%s %v", warnings, err)
			}
		}
		data, _ := os.ReadFile(store.PathFor(state))
		record, err := os.ReadFile(filepath.Join(state, mutations.DeletionRecordsFile))
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, normalizeProjectJSON(t, string(data), projectRegistry(t, state))+normalizeDeletionRows(string(record)))
	}
	if snapshots[0] != snapshots[1] {
		t.Fatal("Confirm set result differs from CLI --yes")
	}
}
