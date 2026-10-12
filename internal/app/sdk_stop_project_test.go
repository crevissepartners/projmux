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
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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

// Refusal uses the same observed process predicate as CLI interruption filtering.
// In every refused case, both the Registry bytes and the live tmux session survive.
func TestStopProjectSDKRunningProcessAgentsZeroWrite(t *testing.T) {
	for _, evidence := range []string{"both", "activation-only", "session-only"} {
		t.Run(evidence, func(t *testing.T) {
			socket, run := stopAppSocket(t)
			fixture := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, fixture, "gen-tmux")
			agentUID, paneUID := addProjectStopProcessFixture(t, fixture)
			secondUID, _ := addSecondProjectStopProcessFixture(t, fixture)
			pane, _ := fixture.registry.Pane(paneUID)
			if evidence == "activation-only" {
				pane.Status.ProcessSession = nil
			}
			if evidence == "session-only" {
				pane.Status.Activation = metadata.PaneActivation{}
			}
			home := t.TempDir()
			session := sessions.NewNamer(home).SessionName(fixture.registry.Projects[0].Spec.Root)
			fixture.registry.Projects[0].Status.Session = &metadata.SessionProjection{Name: session, Live: true}
			run("new-session", "-d", "-s", session, "tail -f /dev/null")
			run("set-option", "-t", session, tmuxopts.ProjectUIDSession, "prj-alpha")
			state, _, _ := renameState(t, fixture.registry)
			client, err := projmux.NewClient(projmux.Options{StateDir: state, HomeDir: home, SocketPath: socket})
			if err != nil {
				t.Fatal(err)
			}
			before := projectRegistryBytes(t, state)
			// Refuse even when tmux routing would fail: process admission comes first.
			for _, route := range []string{socket, filepath.Join(filepath.Dir(socket), "missing")} {
				result, err := client.StopProject(context.Background(), core.StopProjectRequest{Ref: "uid:prj-alpha", SocketPath: route})
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectHasRunningProcessAgents {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				if sdkJSON(t, result) != sdkJSON(t, core.StopProjectResult{}) {
					t.Fatalf("refusal reported effects: %+v", result)
				}
				for _, uid := range []string{agentUID, secondUID} {
					agent, _ := fixture.registry.Agent(uid)
					if !strings.Contains(refusal.Error(), agent.Metadata.Name) || !strings.Contains(refusal.Error(), uid) {
						t.Fatalf("missing Agent name/UID: %v", refusal)
					}
				}
				if before != projectRegistryBytes(t, state) {
					t.Fatal("process refusal changed Registry bytes")
				}
				if got := run("list-sessions", "-F", "#{session_name}"); !strings.Contains(got, session) || !strings.Contains(got, "bystander") {
					t.Fatalf("process refusal changed tmux sessions: %s", got)
				}
			}
		})
	}
}

func TestStopProjectSDKWithoutRunningProcessAgents(t *testing.T) {
	for _, mode := range []string{"offline-process", "other-project-process"} {
		t.Run(mode, func(t *testing.T) {
			socket, run := stopAppSocket(t)
			fixture := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, fixture, "gen-tmux")
			agentUID, processPaneUID := addProjectStopProcessFixture(t, fixture)
			agent, _ := fixture.registry.Agent(agentUID)
			if mode == "offline-process" {
				agent.Status.Phase = metadata.PhaseOffline
			} else {
				// Keep process evidence valid while moving it outside this Project.
				agent.Metadata.OwnerRef.UID = "win-beta-main"
				processPane, _ := fixture.registry.Pane(processPaneUID)
				binding := &processPane.Status.ProcessSession.Binding
				binding.ProjectUID, binding.WindowUID = "prj-beta", "win-beta-main"
				processPane.Status.Activation.Process.Binding = *binding
				for i := range fixture.registry.NameReservations {
					reservation := &fixture.registry.NameReservations[i]
					if reservation.UID == agentUID || reservation.UID == processPaneUID {
						reservation.Scope = "prj-beta"
					}
				}
			}
			if err := fixture.registry.Validate(); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			session := sessions.NewNamer(home).SessionName(fixture.registry.Projects[0].Spec.Root)
			fixture.registry.Projects[0].Status.Session = &metadata.SessionProjection{Name: session, Live: true}
			paneID := run("new-session", "-d", "-s", session, "-P", "-F", "#{pane_id}", "tail -f /dev/null")
			run("set-option", "-t", session, tmuxopts.ProjectUIDSession, "prj-alpha")
			pane, _ := fixture.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.RuntimeID = paneID
			state, _, _ := renameState(t, fixture.registry)
			client, err := projmux.NewClient(projmux.Options{StateDir: state, HomeDir: home, SocketPath: socket})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.StopProject(context.Background(), core.StopProjectRequest{Ref: "uid:prj-alpha"})
			if err != nil || !result.Stopped || len(result.Interruptions) != 1 || result.OperationID == "" {
				t.Fatalf("SDK result=%+v error=%v", result, err)
			}
			if got := run("list-sessions", "-F", "#{session_name}"); got != "bystander" {
				t.Fatalf("tmux stop changed: %s", got)
			}
			after := projectRegistry(t, state)
			tmuxAgent, _ := after.Agent("agt-alpha-codex")
			if tmuxAgent.Status.Phase != metadata.PhaseOffline || tmuxAgent.Status.LastTermination == nil {
				t.Fatal("tmux interruption missing")
			}
			beforeAgent, _ := fixture.registry.Agent("agt-alpha-review")
			afterAgent, _ := after.Agent("agt-alpha-review")
			if sdkJSON(t, beforeAgent) != sdkJSON(t, afterAgent) {
				t.Fatal("unselected process Agent changed")
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

// A process descendant without a reachable owner cannot be confirmed stopped.
// The CLI still ends tmux, reports failure and preserves the process record.
func TestProjectStopUnavailableProcessDescendantStaysRunning(t *testing.T) {
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
	_, stopErr := sdkStopCLI(t, s, "alpha", "alpha")
	agent := projectRegistry(t, state).Agents[0]
	if exitCodeOf(stopErr) != 1 || !strings.Contains(stopErr.Error(), projectStopAgentsStillRunning) || !strings.Contains(stopErr.Error(), agent.Metadata.Name) || !strings.Contains(stopErr.Error(), agent.Metadata.UID) {
		t.Fatalf("stop failure=%v", stopErr)
	}
	if agent.Status.Phase != metadata.PhaseRunning || agent.Status.LastTermination != nil {
		t.Fatalf("process fixture %+v", agent.Status)
	}
	registry := projectRegistry(t, state)
	pane, _ := registry.Pane("pan-alpha-codex")
	if pane.Status.LastTermination != nil {
		t.Fatalf("process got tmux receipt: %+v", pane.Status.LastTermination)
	}
	// /proc is only the fixture process, not a provider discovery or live probe.
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("fixture process was stopped")
	}
	if strings.Contains(run("list-sessions", "-F", "#{session_name}"), "alpha") {
		t.Fatal("failed process stop prevented tmux stop")
	}
	t.Logf("CLI process descendant phase=%s; fixture PID remains alive after reported stop failure", agent.Status.Phase)
}

func TestSDKStopProjectInterruptionSummaryAfterCompensation(t *testing.T) {
	for _, converged := range []bool{true, false} {
		t.Run(fmt.Sprintf("converged=%t", converged), func(t *testing.T) {
			fixture := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, fixture, "generation-summary")
			if err := fixture.registry.Validate(); err != nil {
				t.Fatal(err)
			}
			runner := &exactManagedStopRunner{physical: "/fixture/sdk-stop/socket", logical: defaultAppSocket, rootUID: "prj-alpha"}
			if converged {
				runner.goneAfterRead = 1
			} else {
				runner.applyErr = errors.New("injected kill failure")
			}
			target := managedRuntimeStopTarget{SessionID: "$1", SessionName: "alpha", RootKind: metadata.KindProject, RootUID: "prj-alpha", Route: runtimeMutationRoute{Target: tmuxTransport{Kind: tmuxSocketPath, Value: runner.physical, Source: tmuxSocketPathSource}, SocketName: defaultAppSocket, ExpectedSocketPath: runner.physical, Authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}}}
			project, _ := fixture.registry.Project("prj-alpha")
			result, err := mutationruntime.StopProject(context.Background(), runner, target, stopServiceStore(fixture.store()), *project)
			if converged {
				if err != nil || !result.Stopped || len(result.Interruptions) != 0 {
					t.Fatalf("converged result: %+v, %v", result, err)
				}
				for _, call := range runner.calls {
					if len(call.args) > 2 && call.args[2] == "kill-session" {
						t.Fatal("converged stop attempted kill")
					}
				}
			} else {
				if err == nil || result.Stopped || len(result.Interruptions) != 1 {
					t.Fatalf("failed result must retain attempted summary: %+v, %v", result, err)
				}
			}
			if result.OperationID == "" {
				t.Fatal("missing admitted operation ID")
			}
			pane, _ := fixture.registry.Pane("pan-alpha-codex")
			agent, _ := fixture.registry.Agent("agt-alpha-codex")
			if pane.Status.LastTermination != nil || agent.Status.LastTermination != nil || agent.Status.Phase != metadata.PhaseRunning {
				t.Fatalf("compensation left evidence or changed phase: %+v %+v", pane.Status, agent.Status)
			}
			if err := fixture.registry.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
