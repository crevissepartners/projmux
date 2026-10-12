package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func renameState(t *testing.T, reg metadata.Registry) (string, *resourceStore, *projmux.Client) {
	t.Helper()
	state := t.TempDir()
	path := store.PathFor(state)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	storage := store.NewStore(path)
	return state, &resourceStore{load: storage.LoadReadOnly, update: storage.Update, updateConvergent: storage.UpdateConvergent, mutator: store.DefaultMutator}, projectClient(t, state)
}
func renameCLI(t *testing.T, s *resourceStore, req core.RenameRequest) (string, string, error) {
	t.Helper()
	c := &renameCommand{store: s}
	args := []string{strings.ToLower(string(req.Kind)), req.Ref, "--name", req.Name}
	if req.Project != "" {
		args = append(args, "--project", req.Project)
	}
	if req.Window != "" {
		args = append(args, "--window", req.Window)
	}
	var out, stderr bytes.Buffer
	err := c.Run(args, &out, &stderr)
	return out.String(), stderr.String(), err
}
func TestRenameSDKCLIParity(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	for _, req := range []core.RenameRequest{
		{Kind: core.KindProject, Ref: "alpha", Name: "renamed"},
		{Kind: core.KindWindow, Ref: "main", Project: "alpha", Name: "renamed"},
		{Kind: core.KindPane, Ref: "uid:pan-alpha-zsh", Project: "alpha", Window: "main", Name: "renamed"},
		{Kind: core.KindAgent, Ref: "reader", Project: "alpha", Window: "main", Name: "renamed"},
	} {
		t.Run(string(req.Kind), func(t *testing.T) {
			sdkState, _, c := renameState(t, reg)
			cliState, s, _ := renameState(t, reg)
			// A poisoned inherited runtime must not affect SDK mutation or selection.
			t.Setenv("TMUX", "/bad/socket,999,1")
			t.Setenv("TMUX_PANE", "%999")
			got, err := c.Rename(context.Background(), req)
			if err != nil || !got.Committed {
				t.Fatalf("SDK: %+v %v", got, err)
			}
			text, notice, err := renameCLI(t, s, req)
			if err != nil {
				t.Fatal(err)
			}
			if notice != got.Notice+map[bool]string{true: "\n", false: ""}[got.Notice != ""] {
				t.Fatalf("notice %q != %q", notice, got.Notice)
			}
			sdkReg := projectRegistry(t, sdkState)
			cliReg := projectRegistry(t, cliState)
			a, _ := os.ReadFile(store.PathFor(sdkState))
			b, _ := os.ReadFile(store.PathFor(cliState))
			if normalizeProjectJSON(t, string(a), sdkReg) != normalizeProjectJSON(t, string(b), cliReg) {
				t.Fatalf("Registry bytes differ:\n%s\n%s", a, b)
			}
			uid := got.Receipt.Target.UID
			flags := resourceQueryFlags{kind: metadata.Kind(req.Kind)}
			flags.addPositionalRef(req.Ref)
			if req.Project != "" {
				flags.projects = append(flags.projects, req.Project)
			}
			if req.Window != "" {
				flags.windows = append(flags.windows, req.Window)
			}
			resolution, e := flags.resolve(selector.VerbRename, false, reg)
			if e != nil {
				t.Fatal(e)
			}
			match := resolution.Matches[0]
			match.Name = req.Name
			var encoded bytes.Buffer
			if err := writeResourceProjection(&encoded, "rename "+strings.ToLower(string(req.Kind)), cli.OutputModeJSON, metadata.Kind(req.Kind), []selector.Match{match}, cliReg, false, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if sdkJSON(t, got.Projection) != encoded.String() {
				t.Fatalf("projection bytes differ:\n%s\n%s", sdkJSON(t, got.Projection), encoded.String())
			}
			if !strings.HasPrefix(text, resourceSummary(match, metadata.Kind(req.Kind), sdkReg)+"\n") {
				t.Fatalf("default projection: %s", text)
			}
			receipt := renameReceipt(metadata.Kind(req.Kind), uid, req.Name)
			encoded.Reset()
			if err := receipt.WriteJSON(&encoded); err != nil {
				t.Fatal(err)
			}
			if sdkJSON(t, got.Receipt) != encoded.String() {
				t.Fatalf("receipt bytes differ")
			}
		})
	}
}
func TestRenameSDKRefusalAndCancellation(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	for _, tc := range []struct {
		req   core.RenameRequest
		code  projmux.Code
		usage bool
	}{
		{core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "beta"}, projmux.CodeResourceNameConflict, true},
		{core.RenameRequest{Kind: core.KindWindow, Ref: "main", Project: "alpha", Name: "bad name"}, projmux.CodeResourceNameInvalid, true},
		{core.RenameRequest{Kind: core.KindWindow, Ref: "missing", Project: "alpha", Name: "new"}, projmux.CodeResourceNotFound, true},
		{core.RenameRequest{Kind: core.KindWindow, Ref: "main", Name: "new"}, projmux.CodeSelectorAmbiguous, true},
	} {
		state, s, c := renameState(t, reg)
		before, _ := os.ReadFile(store.PathFor(state))
		result, err := c.Rename(context.Background(), tc.req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code || result.Committed {
			t.Fatalf("SDK refusal: %+v %v want %s", result, err, tc.code)
		}
		if (tc.code == projmux.CodeResourceNameConflict || tc.code == projmux.CodeResourceNameInvalid) && !strings.Contains(err.Error(), "RenameRequest.Name") {
			t.Fatalf("missing Go field: %v", err)
		}
		_, _, cliErr := renameCLI(t, s, tc.req)
		if cliErr == nil || IsUsageError(cliErr) != tc.usage {
			t.Fatalf("CLI %v usage want %v", cliErr, tc.usage)
		}
		after, _ := os.ReadFile(store.PathFor(state))
		if !bytes.Equal(before, after) {
			t.Fatal("refusal wrote Registry")
		}
	}
	state, _, c := renameState(t, reg)
	before, _ := os.ReadFile(store.PathFor(state))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Rename(ctx, core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "new"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(store.PathFor(state))
	if !bytes.Equal(before, after) {
		t.Fatal("canceled request wrote")
	}
	for _, tc := range []struct {
		req  core.RenameRequest
		code projmux.Code
	}{
		{core.RenameRequest{Kind: "unsupported", Ref: "alpha", Name: "new"}, projmux.CodeRenameKindUnsupported},
		{core.RenameRequest{Kind: core.KindProject, Name: "new"}, projmux.CodeSelectorInvalid},
		{core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "new", SocketPath: "relative"}, projmux.CodeRuntimeRequestInvalid},
	} {
		_, err := c.Rename(context.Background(), tc.req)
		var f *projmux.Refusal
		if !errors.As(err, &f) || f.Code != tc.code {
			t.Fatalf("%v want %s", err, tc.code)
		}
	}
}

func TestRenameSDKExplicitSocketCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	reg, _ := sdkResourceFixture(t)
	runner := tmux.ExecRunner{}
	for _, kind := range []core.Kind{core.KindProject, core.KindWindow, core.KindPane, core.KindAgent} {
		t.Run(string(kind), func(t *testing.T) {
			var snapshots []string
			for _, sdk := range []bool{false, true} {
				state, s, _ := renameState(t, reg)
				root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-rename-")
				if err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(root); err != nil || !info.IsDir() {
					t.Fatal("socket directory missing")
				}
				socket := filepath.Join(root, "tmux.sock")
				run := func(args ...string) string {
					t.Helper()
					out, e := runner.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
					if e != nil {
						t.Fatalf("%v: %v %s", args, e, out)
					}
					return strings.TrimSpace(string(out))
				}
				pane := run("-f", "/dev/null", "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "alpha", "-n", "main", "tail -f /dev/null")
				t.Cleanup(func() {
					if run("display-message", "-p", "#{socket_path}") != socket {
						t.Fatal("socket cleanup proof")
					}
					run("kill-server")
					_ = os.RemoveAll(root)
				})
				run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, "prj-alpha")
				run("set-option", "-t", "alpha", tmuxopts.ProjectNameSession, "alpha")
				run("set-option", "-w", "-t", "alpha:main", tmuxopts.WindowUID, "win-alpha-main")
				run("set-option", "-w", "-t", "alpha:main", tmuxopts.WindowName, "main")
				run("set-option", "-p", "-t", pane, tmuxopts.PaneUID, "pan-alpha-zsh")
				run("set-option", "-p", "-t", pane, tmuxopts.PaneName, "zsh")
				refs := map[core.Kind]string{core.KindProject: "uid:prj-alpha", core.KindWindow: "uid:win-alpha-main", core.KindPane: "uid:pan-alpha-zsh", core.KindAgent: "uid:agt-alpha"}
				req := core.RenameRequest{Kind: kind, Ref: refs[kind], Name: "-renamed", SocketPath: socket}
				if sdk {
					t.Setenv("TMUX", "/poison,999,1")
					t.Setenv("TMUX_PANE", "%999")
					// Exercise both request override and Options fallback.
					c, err := projmux.NewClient(projmux.Options{StateDir: state, SocketPath: filepath.Join(root, "wrong.sock")})
					if err != nil {
						t.Fatal(err)
					}
					if kind == core.KindPane {
						c, err = projmux.NewClient(projmux.Options{StateDir: state, SocketPath: socket})
						req.SocketPath = ""
						if err != nil {
							t.Fatal(err)
						}
					}
					result, e := c.Rename(context.Background(), req)
					if e != nil || !result.Committed || result.Notice != "" {
						t.Fatalf("SDK runtime: %+v %v", result, e)
					}
				} else {
					t.Setenv("TMUX", socket+","+run("display-message", "-p", "#{pid}")+",0")
					t.Setenv("TMUX_PANE", pane)
					cmd := &renameCommand{store: s}
					cmd.mirror = inheritedResourceMutationMirror(os.Getenv, runner)
					cmd.tmuxRunner = runner
					cmd.lookupEnv = os.Getenv
					var out, stderr bytes.Buffer
					if e := cmd.Run([]string{strings.ToLower(string(kind)), req.Ref, "--name", req.Name}, &out, &stderr); e != nil {
						t.Fatalf("CLI: %v %s", e, stderr.String())
					}
				}
				snapshots = append(snapshots, run("list-panes", "-a", "-F", tmuxRowFormat("#{session_name}", "#{"+tmuxopts.ProjectNameSession+"}", "#{window_name}", "#{"+tmuxopts.WindowName+"}", "#{pane_title}", "#{"+tmuxopts.PaneName+"}")))
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("tmux states differ:\nCLI %s\nSDK %s", snapshots[0], snapshots[1])
			}
		})
	}
}

func TestRenameSDKCommittedMirrorFailure(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	state, _, c := renameState(t, reg)
	// A never-created isolated socket cannot route to a live server.
	socket := filepath.Join(t.TempDir(), "missing.sock")
	result, err := c.Rename(context.Background(), core.RenameRequest{Kind: core.KindWindow, Ref: "uid:win-alpha-main", Name: "durable", SocketPath: socket})
	var refusal *projmux.Refusal
	if !result.Committed || !errors.As(err, &refusal) || refusal.Code != projmux.CodeMirrorNotConverged || errors.Unwrap(refusal) == nil {
		t.Fatalf("committed failure: %+v %v", result, err)
	}
	r := projectRegistry(t, state)
	w, _ := r.Window("win-alpha-main")
	if w.Metadata.Name != "durable" || result.Receipt.Target.Name != "durable" {
		t.Fatal("Registry commit lost")
	}
}

func TestRenameSDKIsolatedClientsAndInputFailures(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	aState, _, a := renameState(t, reg)
	bState, _, b := renameState(t, reg)
	before, _ := os.ReadFile(store.PathFor(bState))
	got, err := a.Rename(context.Background(), core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	value := got.Projection.(core.Project)
	value.Metadata.Name = "corrupted"
	value.Spec.Root = "corrupted"
	after, _ := os.ReadFile(store.PathFor(bState))
	if !bytes.Equal(before, after) {
		t.Fatal("other client changed")
	}
	again, err := a.Rename(context.Background(), core.RenameRequest{Kind: core.KindProject, Ref: "uid:prj-alpha", Name: "new"})
	if err != nil || again.Projection.(core.Project).Metadata.Name != "new" || again.Projection.(core.Project).Spec.Root == "corrupted" {
		t.Fatalf("public projection aliases Registry: %+v %v", again, err)
	}
	other, err := b.Rename(context.Background(), core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "other-client"})
	if err != nil || other.Projection.(core.Project).Metadata.Name != "other-client" {
		t.Fatalf("second client: %+v %v", other, err)
	}
	// Bad reference grammar and a corrupt Registry stay typed without mutations.
	before, _ = os.ReadFile(store.PathFor(aState))
	_, err = a.Rename(context.Background(), core.RenameRequest{Kind: core.KindPane, Ref: "uid:", Name: "new"})
	var f *projmux.Refusal
	if !errors.As(err, &f) || f.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("grammar: %v", err)
	}
	after, _ = os.ReadFile(store.PathFor(aState))
	if !bytes.Equal(before, after) {
		t.Fatal("bad grammar wrote")
	}
	if err = os.WriteFile(store.PathFor(aState), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.Rename(context.Background(), core.RenameRequest{Kind: core.KindProject, Ref: "alpha", Name: "new"})
	if !errors.As(err, &f) || f.Code != projmux.CodeRegistryUnavailable {
		t.Fatalf("corrupt Registry: %v", err)
	}
}

func TestRenameSDKAgentOwnedPane(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	reg := resourceFixtureRegistry(t)
	_, _, c := renameState(t, reg)
	got, err := c.Rename(context.Background(), core.RenameRequest{Kind: core.KindPane, Ref: "uid:pan-alpha-codex", Project: "alpha", Window: "main", Name: "agent-pane-new"})
	if err != nil || !got.Committed {
		t.Fatalf("Agent Pane: %+v %v", got, err)
	}
}

// Metadata rename does not own a provider lifetime, including on virtual Windows.
func TestRenameSDKProcessMetadataCLIParity(t *testing.T) {
	liveguard.RequireActive(t)
	requireRealTmux(t)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	reg := resourceFixtureRegistry(t)
	window, agent, pane, err := store.DefaultMutator().CreateProcessWindow(&reg, "prj-alpha", metadata.BootstrapWindow{Name: "process-window"}, metadata.CreateAgentOptions{Name: "process-agent", Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: "/tmp"}, OperationID: "rename-process"}, metadata.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "rename-process"})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IsVirtualWindow(window.Metadata.UID) {
		t.Fatal("fixture is not virtual")
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-process-rename-")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatal("socket directory missing")
	}
	socket := filepath.Join(root, "tmux.sock")
	runner := tmux.ExecRunner{}
	run := func(args ...string) string {
		t.Helper()
		out, err := runner.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runtimePane := run("-f", "/dev/null", "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "unrelated", "-n", "untouched", "tail -f /dev/null")
	runtimePID := run("display-message", "-p", "#{pid}")
	t.Cleanup(func() {
		if run("display-message", "-p", "#{socket_path}") != socket {
			t.Fatal("socket cleanup proof")
		}
		run("kill-server")
		_ = os.RemoveAll(root)
	})
	snapshot := func() string {
		return run("list-sessions", "-F", "#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_project_name}") + run("list-windows", "-a", "-F", "#{window_id}|#{window_name}|#{@projmux_window_uid}|#{@projmux_window_name}") + run("list-panes", "-a", "-F", "#{pane_id}|#{pane_title}|#{@projmux_pane_uid}|#{@projmux_pane_name}")
	}
	before := snapshot()
	for _, target := range []struct {
		kind core.Kind
		uid  string
	}{
		{core.KindWindow, window.Metadata.UID}, {core.KindPane, pane.Metadata.UID}, {core.KindAgent, agent.Metadata.UID},
	} {
		for _, mode := range []string{"no-socket", "socket", "missing-server"} {
			t.Run(string(target.kind)+"/"+mode, func(t *testing.T) {
				sdkState, _, c := renameState(t, reg)
				cliState, s, _ := renameState(t, reg)
				req := core.RenameRequest{Kind: target.kind, Ref: "uid:" + target.uid, Name: "process-renamed"}
				if mode == "socket" {
					req.SocketPath = socket
				}
				if mode == "missing-server" {
					req.SocketPath = filepath.Join(root, "missing.sock")
				}
				got, err := c.Rename(context.Background(), req)
				if !got.Committed {
					t.Fatalf("SDK did not commit: %+v %v", got, err)
				}
				if err != nil {
					t.Fatal(err)
				}
				cmd := &renameCommand{store: s}
				if mode != "no-socket" {
					lookup := func(key string) string {
						switch key {
						case "TMUX":
							return req.SocketPath + "," + runtimePID + ",0"
						case "TMUX_PANE":
							return runtimePane
						}
						return ""
					}
					cmd.mirror = inheritedResourceMutationMirror(lookup, runner)
					cmd.tmuxRunner = runner
					cmd.lookupEnv = lookup
				}
				var cliOut, cliNotice bytes.Buffer
				if err := cmd.Run([]string{strings.ToLower(string(target.kind)), req.Ref, "--name", req.Name}, &cliOut, &cliNotice); err != nil {
					t.Fatalf("CLI: %v %s", err, cliNotice.String())
				}
				notice := cliNotice.String()
				if strings.TrimSpace(notice) != got.Notice {
					t.Fatalf("notice mismatch %q != %q", notice, got.Notice)
				}
				// The shared service can observe inventory but must issue zero writes.
				counter := &renameCountingRuntimeRunner{delegate: runner}
				mirror := mutations.ExplicitSocketRenamer(mutations.Store{}, func() (metadata.Registry, error) { return reg, nil }, req.SocketPath, counter)
				mirrorNotice, mirrorErr := mirror.MirrorRenamed(context.Background(), metadata.Kind(target.kind), target.uid, req.Name)
				if mirrorErr != nil || mirrorNotice != got.Notice || counter.writes != 0 || (mode == "no-socket" && counter.calls != 0) || (mode != "no-socket" && target.kind != core.KindAgent && counter.calls == 0) {
					t.Fatalf("process mirror: notice=%q error=%v calls=%d writes=%d", mirrorNotice, mirrorErr, counter.calls, counter.writes)
				}
				a, err := os.ReadFile(store.PathFor(sdkState))
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(store.PathFor(cliState))
				if err != nil {
					t.Fatal(err)
				}
				if normalizeProjectJSON(t, string(a), projectRegistry(t, sdkState)) != normalizeProjectJSON(t, string(b), projectRegistry(t, cliState)) {
					t.Fatalf("process Registry bytes differ: %s\n%s", a, b)
				}
				if after := snapshot(); after != before {
					t.Fatalf("process metadata rename touched tmux: %q != %q", after, before)
				}
			})
		}
	}
}

type renameCountingRuntimeRunner struct {
	calls, writes int
	delegate      tmux.ExecRunner
}

func (r *renameCountingRuntimeRunner) Run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	r.calls++
	if len(args) >= 3 {
		switch args[2] {
		case "set-option", "rename-window", "rename-session", "select-pane":
			r.writes++
		}
	}
	return r.delegate.Run(ctx, executable, args...)
}
