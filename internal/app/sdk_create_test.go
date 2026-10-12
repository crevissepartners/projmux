package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkCreateBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projmux")
	cmd := exec.Command("go", "build", "-o", path, "../../cmd/projmux")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build supervisor: %v %s", err, out)
	}
	return path
}
func sdkCreateSocket(t *testing.T) (string, func(...string) string) {
	t.Helper()
	requireRealTmux(t)
	dir, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-c-")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("socket directory missing")
	}
	socket := filepath.Join(dir, "s")
	run := func(args ...string) string {
		t.Helper()
		out, err := tmux.ExecRunner{}.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("-f", "/dev/null", "new-session", "-d", "-s", "control", "/bin/sh")
	run("set-option", "-g", tmuxopts.AppGlobal, "1")
	run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
	t.Cleanup(func() {
		sdkCreateKillServer(t, socket)
		_ = os.RemoveAll(dir)
	})
	return socket, run
}
func sdkCreateCLI(t *testing.T, state, config, socket, binary string, args ...string) (string, error) {
	t.Helper()
	storage := store.NewStore(store.PathFor(state))
	c, err := virtualWindowCreator(tmux.ExecRunner{}, func(string) string { return "" }, &resourceStore{load: storage.LoadReadOnly, snapshot: storage.LoadReadOnly, update: storage.Update, mutator: store.DefaultMutator}, socket)
	if err != nil {
		t.Fatal(err)
	}
	c.shell = "/bin/sh"
	c.runtime.Executable = func() (string, error) { return binary, nil }
	c.runtime.ConfigPath = filepath.Join(config, "tmux.conf")
	var out, stderr bytes.Buffer
	err = c.Run(args, &out, &stderr)
	return out.String(), err
}
func normalizeCreateJSON(t *testing.T, raw string, reg metadata.Registry, socket string) string {
	t.Helper()
	raw = normalizeProjectJSON(t, raw, reg)
	raw = strings.ReplaceAll(raw, socket, "SOCKET")
	// Generation tokens are minted for shell activation, independently of UIDs.
	clocks := regexp.MustCompile(`("(?:generation|startedAt|lastAppliedOperationID|lastOperationID)"\s*:\s*)"[^"]*"`)
	return clocks.ReplaceAllString(raw, `${1}"NORMALIZED"`)
}
func TestCreateShellSDKCLIParity(t *testing.T) {
	binary := sdkCreateBinary(t)
	root := t.TempDir()
	home := t.TempDir()
	subdir := filepath.Join(root, "nested")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	for _, kind := range []string{"window", "pane-right-project", "pane-down-pane", "pane-right-explicit"} {
		t.Run(kind, func(t *testing.T) {
			var snapshots, receipts, projections, runtimes []string
			for _, sdk := range []bool{false, true} {
				state := filepath.Join(t.TempDir(), "projmux")
				config := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				socket, run := sdkCreateSocket(t)
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"})
				if err != nil {
					t.Fatal(err)
				}
				createWindow := []string{"window", "--project", "sample", "--name", "extra", "--label", "team=one"}
				if kind != "window" {
					setup := []string{"window", "--project", "sample", "--name", "extra"}
					if kind == "pane-down-pane" {
						setup = append(setup, "--", "/bin/sh", "-c", "cd "+strconv.Quote(subdir)+"; exec /bin/sh")
					}
					if _, err = sdkCreateCLI(t, state, config, socket, binary, setup...); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "pane-down-pane" {
					deadline := time.Now().Add(2 * time.Second)
					for {
						found := false
						for path := range strings.SplitSeq(run("list-panes", "-a", "-F", "#{pane_current_path}"), "\n") {
							found = found || path == subdir
						}
						if found {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("anchor never entered nested cwd")
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				anchor := ""
				for _, w := range projectRegistry(t, state).Windows {
					if w.Metadata.Name == "extra" {
						anchor = "uid:" + w.Spec.AnchorPaneRef
					}
				}
				var receipt, projection string
				if sdk {
					if kind == "window" {
						res, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra", Labels: []string{"team=one"}})
						if err != nil {
							t.Fatal(err)
						}
						receipt = sdkJSON(t, res.Receipt)
						projection = sdkJSON(t, res.Projection)
					} else {
						req := core.CreatePaneRequest{Project: "sample", Window: "extra", Name: "child", Labels: []string{"team=one"}, Placement: "right", CwdFrom: "project"}
						if kind == "pane-down-pane" {
							req.Placement = "down"
							req.CwdFrom = " pane "
						}
						if kind == "pane-right-explicit" {
							req.Pane = anchor
						}
						res, err := client.CreatePane(context.Background(), req)
						if err != nil {
							t.Fatal(err)
						}
						if kind == "pane-down-pane" && res.Pane.Spec.CWD != subdir {
							t.Fatalf("pane cwd %q", res.Pane.Spec.CWD)
						}
						receipt = sdkJSON(t, res.Receipt)
						projection = sdkJSON(t, res.Projection)
					}
				} else {
					args := createWindow
					if kind != "window" {
						placement, cwd := "right", "project"
						if kind == "pane-down-pane" {
							placement, cwd = "down", " pane "
						}
						args = []string{"pane", "--project", "sample", "--window", "extra", "--name", "child", "--label", "team=one", "--placement", placement, "--cwd-from", cwd}
					}
					if kind == "pane-right-explicit" {
						args = append(args, "--pane", anchor)
					}
					receipt, err = sdkCreateCLI(t, state, config, socket, binary, append(args, "-o", "receipt")...)
					if err != nil {
						t.Fatal(err)
					}
					// Produce the JSON projection through the real CLI renderer from the
					// committed graph; a second create would allocate another identity.
					reg := projectRegistry(t, state)
					resourceKind := metadata.KindWindow
					uid := ""
					name := "extra"
					if kind != "window" {
						resourceKind = metadata.KindPane
						name = "child"
					}
					for _, w := range reg.Windows {
						if resourceKind == metadata.KindWindow && w.Metadata.Name == name {
							uid = w.Metadata.UID
						}
					}
					for _, p := range reg.Panes {
						if resourceKind == metadata.KindPane && p.Metadata.Name == name {
							uid = p.Metadata.UID
						}
					}
					c, err := virtualWindowCreator(tmux.ExecRunner{}, func(string) string { return "" }, &resourceStore{load: store.NewStore(store.PathFor(state)).LoadReadOnly}, socket)
					if err != nil {
						t.Fatal(err)
					}
					var jsonOut bytes.Buffer
					err = c.writeResults(&jsonOut, "create "+string(resourceKind), "json", resourceKind, []createResult{{kind: resourceKind, uid: uid, name: name}})
					if err != nil {
						t.Fatal(err)
					}
					projection = jsonOut.String()
				}
				reg := projectRegistry(t, state)
				snapshots = append(snapshots, normalizeCreateJSON(t, projectRegistryBytes(t, state), reg, socket))
				receipts = append(receipts, normalizeCreateJSON(t, receipt, reg, socket))
				projections = append(projections, normalizeCreateJSON(t, projection, reg, socket))
				runtimes = append(runtimes, normalizeCreateJSON(t, run("list-panes", "-a", "-F", "#{window_name}|#{pane_current_path}|#{@projmux_pane_uid}"), reg, socket))
			}
			for _, pair := range [][]string{snapshots, receipts, projections, runtimes} {
				if pair[0] != pair[1] {
					t.Fatalf("SDK/CLI mismatch\nCLI %s\nSDK %s", pair[0], pair[1])
				}
			}
		})
	}
}

func TestCreateShellSDKRefusalsAndCancellation(t *testing.T) {
	state, config := t.TempDir(), t.TempDir()
	socket := filepath.Join(t.TempDir(), "absent")
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.CreateWindow(canceled, core.CreateWindowRequest{Project: "sample"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, req := range []core.CreateWindowRequest{{}, {Project: "sample", Labels: []string{"bad"}}, {Project: "sample", Name: "bad/name"}, {Project: "sample"}} {
		_, err = client.CreateWindow(context.Background(), req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("%+v: %T %v", req, err, err)
		}
	}
	if _, err = os.Stat(store.PathFor(state)); !os.IsNotExist(err) {
		t.Fatal("refusal wrote Registry")
	}
	client, err = projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, SocketPath: socket, ProjmuxExecutable: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateRouteRefused {
		t.Fatalf("absent socket %T %v", err, err)
	}
	if _, err = os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("refusal started tmux")
	}
}

func TestCreatePhaseDiagnosticsMapping(t *testing.T) {
	// Conversion in createOutcomeRecorder must preserve the closed enum and size.
	service := []mutationruntime.CreatePhase{mutationruntime.CreatePhaseGuard, mutationruntime.CreatePhaseFirstReconcile, mutationruntime.CreatePhaseOperation, mutationruntime.CreatePhaseSecondReconcile, mutationruntime.CreatePhaseReprove, mutationruntime.CreatePhaseStoreWrite}
	diagnostic := []diagnostics.CreatePhase{diagnostics.CreatePhaseGuard, diagnostics.CreatePhaseFirstReconcile, diagnostics.CreatePhaseOperation, diagnostics.CreatePhaseSecondReconcile, diagnostics.CreatePhaseReprove, diagnostics.CreatePhaseStoreWrite}
	if len(mutationruntime.CreatePhases{}) != len(service) {
		t.Fatal("phase count drift")
	}
	if len(diagnostics.CreatePhases{}) != len(diagnostic) {
		t.Fatal("diagnostics phase count drift")
	}
	for i := range service {
		if int(service[i]) != int(diagnostic[i]) || int(service[i]) != i {
			t.Fatalf("phase mapping %d", i)
		}
	}
}

func TestCreateRecorderPreservesPhaseFields(t *testing.T) {
	journal := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := createOutcomeRecorder{diagnostics.NewLifecycleRecorder(journal, "sdk-create-test", "test", "tmux").Create()}
	var phases mutationruntime.CreatePhases
	for i := range phases {
		d := time.Duration(i+1) * time.Millisecond
		phases[i] = &d
	}
	held := 21 * time.Millisecond
	recorder.Record(mutationruntime.CreateOutcome{Kind: mutationruntime.CreateKindWindow, Result: "success", Duration: held, LockHeld: &held, Phases: phases})
	events, err := journal.Read()
	if err != nil || len(events) != 1 {
		t.Fatalf("events %+v %v", events, err)
	}
	event := events[0]
	fields := []*int64{event.PhaseGuardMS, event.PhaseFirstReconcileMS, event.PhaseOperationMS, event.PhaseSecondReconcileMS, event.PhaseReproveMS, event.PhaseStoreWriteMS}
	for i, field := range fields {
		if field == nil || *field != int64(i+1) {
			t.Fatalf("phase %d: %v", i, field)
		}
	}
}

func TestCreateShellHooksFreshServerAndRollback(t *testing.T) {
	binary := sdkCreateBinary(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	root, home := t.TempDir(), t.TempDir()
	originalTmuxTmp := os.Getenv("TMUX_TMPDIR")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	for _, scenario := range []string{"fresh-server", "pre-hook-failure", "post-hook-failure", "new-window-failure", "new-window-partial-failure"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots, receipts, messages []string
			for _, sdk := range []bool{false, true} {
				state := filepath.Join(t.TempDir(), "projmux")
				config := filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				if err := os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(config, "tmux.conf"), []byte("set -g "+tmuxopts.AppGlobal+" 1\nset -g default-shell /bin/sh\n"), 0600); err != nil {
					t.Fatal(err)
				}
				hook := ""
				if scenario == "pre-hook-failure" {
					hook = "[hooks.pre-create]\nrun = \"exit 7\"\n"
				}
				if scenario == "post-hook-failure" {
					hook = "[hooks.post-create]\nrun = \"exit 9\"\n"
				}
				if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte(hook), 0600); err != nil {
					t.Fatal(err)
				}
				var socket string
				var run func(...string) string
				if strings.HasPrefix(scenario, "new-window") {
					socket, run = sdkCreateSocket(t)
				} else {
					dir, err := os.MkdirTemp(originalTmuxTmp, "sdk-new-")
					if err != nil {
						t.Fatal(err)
					}
					if info, err := os.Stat(dir); err != nil || !info.IsDir() {
						t.Fatal("socket directory missing")
					}
					t.Setenv("TMUX_TMPDIR", dir)
					t.Cleanup(func() { _ = os.RemoveAll(dir) })
				}
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				before := projectRegistryBytes(t, state)
				oldPath := os.Getenv("PATH")
				if strings.HasPrefix(scenario, "new-window") {
					shim := t.TempDir()
					script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = new-window ]; then exit 12; fi\ndone\nexec " + strconv.Quote(realTmux) + " \"$@\"\n"
					if scenario == "new-window-partial-failure" {
						script = strings.Replace(script, "then exit 12;", "then "+strconv.Quote(realTmux)+" \"$@\"; exit 12;", 1)
					}
					if err := os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", shim+string(os.PathListSeparator)+oldPath)
				}
				var output string
				if sdk {
					res, e := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"})
					err = e
					output = sdkJSON(t, res.Receipt)
				} else {
					output, err = sdkCreateCLI(t, state, config, socket, binary, "window", "--project", "sample", "--name", "extra", "-o", "receipt")
				}
				t.Setenv("PATH", oldPath)
				after := projectRegistry(t, state)
				failed := scenario == "pre-hook-failure" || strings.HasPrefix(scenario, "new-window")
				if failed {
					if err == nil {
						t.Fatal("failure injection succeeded")
					}
					if projectRegistryBytes(t, state) != before {
						t.Fatal("failure wrote Registry")
					}
					if socket != "" {
						if got := run("list-sessions", "-F", "#{session_name}"); got != "control" {
							t.Fatalf("rollback residual: %s", got)
						}
					}
					message := err.Error()
					var refusal *projmux.Refusal
					if sdk && !errors.As(err, &refusal) {
						t.Fatal("untyped create failure")
					}
					message = strings.ReplaceAll(strings.ReplaceAll(message, socket, "SOCKET"), config, "CONFIG")
					minted := regexp.MustCompile(`(--(?:pane-uid|generation|operation-id) )\S+`)
					message = minted.ReplaceAllString(message, `${1}NORMALIZED`)
					message = regexp.MustCompile(` --registry-path \S+`).ReplaceAllString(message, "")
					messages = append(messages, message)
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if socket == "" {
						socket = after.Projects[0].Status.Session.SocketPath
						if !filepath.IsAbs(socket) || !strings.HasPrefix(socket, os.Getenv("TMUX_TMPDIR")+string(os.PathSeparator)) {
							t.Fatalf("unexpected socket %q", socket)
						}
						sdkCreateKillServer(t, socket)
					}
					receipts = append(receipts, normalizeCreateJSON(t, output, after, socket))
				}
				snapshots = append(snapshots, normalizeCreateJSON(t, projectRegistryBytes(t, state), after, socket))
				if scenario == "pre-hook-failure" {
					if _, e := os.Stat(filepath.Join(os.Getenv("TMUX_TMPDIR"), "tmux-"+strconv.Itoa(os.Getuid()), "projmux")); !os.IsNotExist(e) {
						t.Fatal("pre-hook failure started server")
					}
				}
			}
			for _, pair := range [][]string{snapshots, receipts, messages} {
				if len(pair) == 2 && pair[0] != pair[1] {
					t.Fatalf("%s mismatch\nCLI %s\nSDK %s", scenario, pair[0], pair[1])
				}
			}
		})
	}
}

// Wait for every owned pane supervisor to stop writing its termination receipt
// before TempDir cleanup removes the explicitly selected state directory.
func sdkCreateKillServer(t *testing.T, socket string) {
	t.Helper()
	runner := tmux.ExecRunner{}
	out, err := runner.Run(context.Background(), "tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_pid}")
	if err != nil {
		return
	}
	if _, err = runner.Run(context.Background(), "tmux", "-S", socket, "kill-server"); err != nil {
		t.Fatal(err)
	}
	for pid := range strings.FieldsSeq(string(out)) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, err := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
			text := strings.TrimSpace(string(status))
			if err != nil || text == "" || strings.HasPrefix(text, "Z") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("owned pane process %s still running: %s", pid, text)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCreatePaneProcessAnchorSDKCLIRefusalBeforeRoute(t *testing.T) {
	reg := resourceFixtureRegistry(t)
	for i := range reg.Panes {
		if reg.Panes[i].Metadata.UID == "pan-alpha-codex" {
			reg.Panes[i].Spec.Runtime.Kind = metadata.RuntimeProcess
			reg.Panes[i].Status.Activation = metadata.PaneActivation{}
		}
	}
	state, _, _ := renameState(t, reg)
	before := projectRegistryBytes(t, state)
	socket := filepath.Join(t.TempDir(), "missing")
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: t.TempDir(), SocketPath: socket, ProjmuxExecutable: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreatePane(context.Background(), core.CreatePaneRequest{Project: "alpha", Window: "main", Pane: "uid:pan-alpha-codex"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProcessAnchorUnsupported {
		t.Fatalf("SDK %T %v", err, err)
	}
	_, cliErr := sdkCreateCLI(t, state, t.TempDir(), socket, "/bin/sh", "pane", "--project", "alpha", "--window", "main", "--pane", "uid:pan-alpha-codex")
	if cliErr == nil || cliErr.Error() != err.Error() {
		t.Fatalf("CLI %v SDK %v", cliErr, err)
	}
	if projectRegistryBytes(t, state) != before {
		t.Fatal("process refusal wrote Registry")
	}
	if _, err = os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("process refusal created socket")
	}
}

func TestSharedCreateCWDHelpersPreserveErrorsAndValues(t *testing.T) {
	for _, path := range []string{"", "relative"} {
		_, cli := canonicalExistingDir(path)
		_, shared := mutationruntime.CanonicalExistingDir(path)
		if cli.Error() != "must be an absolute existing directory" || shared.Error() != cli.Error() || !errors.Is(cli, errAgentWorkspaceNotAbsolute) || !errors.Is(shared, errAgentWorkspaceNotAbsolute) {
			t.Fatalf("path %q CLI %v shared %v", path, cli, shared)
		}
	}
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, child, filepath.Join(root, "missing")} {
		a, x := canonicalExistingDir(path)
		b, y := mutationruntime.CanonicalExistingDir(path)
		if a != b || (x == nil) != (y == nil) {
			t.Fatalf("canonical %q: %q/%q %v/%v", path, a, b, x, y)
		}
		if pathWithinTree(root, path) != mutationruntime.PathWithinTree(root, path) {
			t.Fatal("containment drift")
		}
	}
}

func TestCreateShellSDKCLIRefusalCodes(t *testing.T) {
	binary := sdkCreateBinary(t)
	state, config, root, home := filepath.Join(t.TempDir(), "projmux"), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	socket, run := sdkCreateSocket(t)
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		window core.CreateWindowRequest
		pane   *core.CreatePaneRequest
		args   []string
		code   projmux.Code
		field  string
	}{
		{name: "missing Project", window: core.CreateWindowRequest{Project: "missing"}, args: []string{"window", "--project", "missing"}, code: projmux.CodeResourceNotFound},
		{name: "missing Window", pane: &core.CreatePaneRequest{Project: "sample", Window: "missing"}, args: []string{"pane", "--project", "sample", "--window", "missing"}, code: projmux.CodeResourceNotFound},
		{name: "missing Pane", pane: &core.CreatePaneRequest{Project: "sample", Window: "existing", Pane: "missing"}, args: []string{"pane", "--project", "sample", "--window", "existing", "--pane", "missing"}, code: projmux.CodeResourceNotFound},
		{name: "invalid Name", window: core.CreateWindowRequest{Project: "sample", Name: "bad/name"}, args: []string{"window", "--project", "sample", "--name", "bad/name"}, code: projmux.CodeResourceNameInvalid, field: "CreateWindowRequest.Name"},
		{name: "invalid Labels", window: core.CreateWindowRequest{Project: "sample", Labels: []string{"bad"}}, args: []string{"window", "--project", "sample", "--label", "bad"}, code: projmux.CodeResourceLabelInvalid, field: "CreateWindowRequest.Labels"},
		{name: "duplicate Name", window: core.CreateWindowRequest{Project: "sample", Name: "existing"}, args: []string{"window", "--project", "sample", "--name", "existing"}, code: projmux.CodeResourceNameConflict, field: "CreateWindowRequest.Name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := projectRegistryBytes(t, state)
			runtime := run("list-panes", "-a", "-F", "#{pane_id}|#{@projmux_pane_uid}")
			var sdkErr error
			if tc.pane != nil {
				_, sdkErr = client.CreatePane(context.Background(), *tc.pane)
			} else {
				_, sdkErr = client.CreateWindow(context.Background(), tc.window)
			}
			var refusal *projmux.Refusal
			if !errors.As(sdkErr, &refusal) || refusal.Code != tc.code {
				t.Fatalf("SDK code: %T %v, want %s", sdkErr, sdkErr, tc.code)
			}
			_, cliErr := sdkCreateCLI(t, state, config, socket, binary, tc.args...)
			if cliErr == nil {
				t.Fatal("CLI accepted refusal case")
			}
			if tc.field == "" && sdkErr.Error() != cliErr.Error() {
				t.Fatalf("selection text CLI %v SDK %v", cliErr, sdkErr)
			}
			if tc.field != "" && !strings.Contains(sdkErr.Error(), tc.field) {
				t.Fatalf("SDK input field absent: %v", sdkErr)
			}
			if projectRegistryBytes(t, state) != before || run("list-panes", "-a", "-F", "#{pane_id}|#{@projmux_pane_uid}") != runtime {
				t.Fatal("refusal changed Registry or tmux")
			}
		})
	}
}
