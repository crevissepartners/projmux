package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkStopCLI(t *testing.T, s *resourceStore, session string, ref string) (string, error) {
	t.Helper()
	c := newSwitchCommand()
	c.identity = stubSwitchIdentityResolver{name: session}
	c.identityErr = nil
	c.lookupEnv = func(string) string { return "" }
	c.managedStopStore = s
	c.navigation.reader.reader.loadRegistry = s.load
	c.navigation.reader.reader.lookupEnv = func(string) string { return "" }
	cmd := &projectLifecycleCommand{verb: projectLifecycleStop, store: s, switcher: c, lookupEnv: func(string) string { return "" }}
	var out, stderr bytes.Buffer
	err := cmd.Run([]string{"project", ref, "-o", "receipt"}, &out, &stderr)
	return out.String(), err
}

func stopAppSocket(t *testing.T) (string, func(...string) string) {
	t.Helper()
	liveguard.RequireActive(t)
	requireRealTmux(t)
	root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "st-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", root)
	dir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("socket directory missing")
	}
	socket := filepath.Join(dir, "projmux")
	run := func(args ...string) string {
		t.Helper()
		out, err := (tmux.ExecRunner{}).Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("-f", "/dev/null", "new-session", "-d", "-s", "bystander", "tail -f /dev/null")
	run("set-option", "-g", tmuxopts.AppGlobal, "1")
	run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
	t.Cleanup(func() {
		// Kill only sessions on the socket this test created, leaving no daemon.
		out, _ := (tmux.ExecRunner{}).Run(context.Background(), "tmux", "-S", socket, "list-sessions", "-F", "#{session_id}")
		for id := range strings.FieldsSeq(string(out)) {
			_, _ = (tmux.ExecRunner{}).Run(context.Background(), "tmux", "-S", socket, "kill-session", "-t", id)
		}
		_ = os.RemoveAll(root)
	})
	return socket, run
}

func TestStopProjectSDKCLIParity(t *testing.T) {
	for _, mode := range []string{"app", "request", "options"} {
		t.Run(mode, func(t *testing.T) {
			socket, run := stopAppSocket(t)
			fixture := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, fixture, "gen-alpha")
			root := fixture.registry.Projects[0].Spec.Root
			home := t.TempDir()
			session := sessions.NewNamer(home).SessionName(root)
			fixture.registry.Projects[0].Status.Session = &metadata.SessionProjection{Name: session, Live: true}
			var snapshots, receipts []string
			for _, sdk := range []bool{false, true} {
				pane := run("new-session", "-d", "-s", session, "-P", "-F", "#{pane_id}", "tail -f /dev/null")
				run("set-option", "-t", session, tmuxopts.ProjectUIDSession, "prj-alpha")
				reg := fixture.registry.Clone()
				agentPane, _ := reg.Pane("pan-alpha-codex")
				agentPane.Status.Activation.RuntimeID = pane
				state, s, _ := renameState(t, reg)
				if sdk {
					options := projmux.Options{StateDir: state, HomeDir: home}
					req := core.StopProjectRequest{Ref: "uid:prj-alpha"}
					if mode == "request" {
						options.SocketPath = filepath.Join(filepath.Dir(socket), "wrong")
						req.SocketPath = socket
					}
					if mode == "options" {
						options.SocketPath = socket
					}
					c, err := projmux.NewClient(options)
					if err != nil {
						t.Fatal(err)
					}
					t.Setenv("TMUX", "/wrong,999,1")
					t.Setenv("TMUX_PANE", "%999")
					t.Setenv(runtimeMutationAnchorPaneEnv, "%999")
					result, err := c.StopProject(context.Background(), req)
					if err != nil || !result.Stopped || len(result.Interruptions) != 1 || result.OperationID == "" {
						t.Fatalf("SDK %+v %v", result, err)
					}
					receipts = append(receipts, sdkJSON(t, result.Receipt))
				} else {
					out, err := sdkStopCLI(t, s, session, "uid:prj-alpha")
					if err != nil {
						t.Fatal(err)
					}
					receipts = append(receipts, out)
				}
				if run("list-sessions", "-F", "#{session_name}") != "bystander" {
					t.Fatal("session survived or bystander changed")
				}
				after := projectRegistry(t, state)
				if after.Agents[0].Status.Phase != metadata.PhaseOffline || after.Agents[0].Status.LastTermination == nil {
					t.Fatal("interruption missing")
				}
				// The server allocates a fresh Pane handle on the second run.
				snapshot := normalizeProjectJSON(t, projectRegistryBytes(t, state), after)
				snapshot = regexp.MustCompile(`("(?:runtimeID|firstObservedAt)"\s*:\s*)"[^"]*"`).ReplaceAllString(snapshot, `${1}"NORMALIZED"`)
				snapshots = append(snapshots, snapshot)
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("Registry bytes differ:\n%s\n%s", snapshots[0], snapshots[1])
			}
			if receipts[0] != receipts[1] {
				t.Fatalf("receipt differs:\n%s\n%s", receipts[0], receipts[1])
			}
		})
	}
}

func TestStopProjectSDKNoWriteRefusals(t *testing.T) {
	socket, run := stopAppSocket(t)
	reg := freshStartFixtureStore(t).registry
	home := t.TempDir()
	session := sessions.NewNamer(home).SessionName(reg.Projects[0].Spec.Root)
	reg.Projects[0].Status.Session.Name = session
	state, s, _ := renameState(t, reg)
	c, err := projmux.NewClient(projmux.Options{StateDir: state, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	before := projectRegistryBytes(t, state)
	for _, ref := range []string{"alpha", "missing", "/absolute/unclaimed"} {
		_, err := c.StopProject(context.Background(), core.StopProjectRequest{Ref: ref})
		_, cliErr := sdkStopCLI(t, s, session, ref)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || cliErr == nil {
			t.Fatalf("ref %s: %v / %v", ref, err, cliErr)
		}
		if err.Error() != cliErr.Error() {
			t.Fatalf("selector/not-live text differs: %v / %v", err, cliErr)
		}
		if ref == "alpha" && refusal.Code != projmux.CodeProjectNotLive {
			t.Fatal(refusal)
		}
		if before != projectRegistryBytes(t, state) {
			t.Fatal("refusal wrote")
		}
	}
	run("new-session", "-d", "-s", session, "tail -f /dev/null")
	run("set-option", "-t", session, tmuxopts.ProjectUIDSession, "wrong-project")
	_, err = c.StopProject(context.Background(), core.StopProjectRequest{Ref: "alpha", SocketPath: socket})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectStopExecutionFailed {
		t.Fatal(err)
	}
	if !strings.Contains(run("list-sessions", "-F", "#{session_name}"), session) || before != projectRegistryBytes(t, state) {
		t.Fatal("UID refusal changed state")
	}
	run("set-option", "-t", session, tmuxopts.ProjectUIDSession, "prj-alpha")
	if _, e := s.update(func(r *metadata.Registry) error {
		p, _ := r.Project("prj-alpha")
		p.Status.Session.Name = "stale"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	before = projectRegistryBytes(t, state)
	_, err = c.StopProject(context.Background(), core.StopProjectRequest{Ref: "alpha", SocketPath: socket})
	_, staleCLI := sdkStopCLI(t, s, session, "alpha")
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectStopRouteRefused || staleCLI == nil || err.Error() != staleCLI.Error() {
		t.Fatalf("stale session projection parity: %v / %v", err, staleCLI)
	}
	if before != projectRegistryBytes(t, state) {
		t.Fatal("stale projection wrote")
	}

	run("set-option", "-g", tmuxopts.AppGlobal, "foreign")
	_, err = c.StopProject(context.Background(), core.StopProjectRequest{Ref: "alpha"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectStopRouteRefused {
		t.Fatal(err)
	}
	_, cliErr := sdkStopCLI(t, s, session, "alpha")
	var usage *UsageError
	if !errors.As(cliErr, &usage) || before != projectRegistryBytes(t, state) {
		t.Fatalf("unowned CLI/SDK wrote: %v", cliErr)
	}
}

// Existing stop semantics record Running process descendants as interrupted
// without controlling their provider lifetime. This fixture documents that
// behavior; changing it belongs to the separate lifecycle follow-up.
func TestProjectStopProcessDescendantObservation(t *testing.T) {
	_, run := stopAppSocket(t)
	fixture := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, fixture, "gen-process")
	processPane, _ := fixture.registry.Pane("pan-alpha-codex")
	processPane.Spec.Runtime.Kind = metadata.RuntimeProcess
	binding := metadata.ProcessBinding{HostInstanceID: "fixture", ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Generation: "gen-process", OperationID: "op-create"}
	processPane.Status.Activation = metadata.PaneActivation{Kind: metadata.RuntimeProcess, Generation: binding.Generation, AgentUID: binding.AgentUID, OperationID: binding.OperationID, Process: &metadata.ProcessActivation{Binding: binding, HostProcess: metadata.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "fixture-host"}, Child: metadata.ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "fixture-child"}}}
	state, s, _ := renameState(t, fixture.registry)
	run("new-session", "-d", "-s", "alpha", "tail -f /dev/null")
	run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, "prj-alpha")
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	if _, err := sdkStopCLI(t, s, "alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	agent := projectRegistry(t, state).Agents[0]
	if agent.Status.Phase != metadata.PhaseOffline || agent.Status.LastTermination == nil || agent.Status.LastTermination.Classification != metadata.TerminationInterrupted {
		t.Fatalf("process fixture %+v", agent.Status)
	}
	// /proc is only the fixture process, not a provider discovery or live probe.
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("fixture process was stopped")
	}
	t.Logf("CLI process descendant phase=%s termination=%s; fixture PID remains alive", agent.Status.Phase, agent.Status.LastTermination.Classification)
}
