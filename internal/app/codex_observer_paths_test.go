package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// Run the real observer entry in a subprocess, with exactly the argv produced
// by the production launcher. An optional built/installed binary also exercises
// the full executable dispatcher, without a provider or a live socket.
const nativeObserverFixtureChildEnv = "PMX_TEST_NATIVE_OBSERVER_CHILD"

func init() {
	if os.Getenv(nativeObserverFixtureChildEnv) == "1" {
		if len(os.Args) > 3 && os.Args[1] == "internal" && os.Args[2] == "codex-broker" {
			os.Exit(1)
		}
		if len(os.Args) > 4 && strings.Join(os.Args[1:5], " ") == "internal agent-hook ingest codex-broker-watch" {
			err := newAICommand().Run(os.Args[3:], os.Stdout, os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	// A subprocess sentinel proves that unrelated argv reaches later dispatch.
	if os.Getenv("PMX_TEST_NATIVE_OBSERVER_DISPATCH_PROBE") == "1" {
		fmt.Fprintln(os.Stdout, "native observer fixture dispatch preserved")
		os.Exit(0)
	}
}

func TestNativeObserverFixtureDispatchBoundary(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range [][]string{
		{"internal", "codex-broker", "serve"},
		{"internal", "agent-hook", "ingest", "codex-broker-watch"},
	} {
		for _, marker := range []string{"", "unrelated", "1"} {
			t.Run(route[2]+"/"+marker, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), executable, route...)
				cmd.Env = append(cleanObserverEnv(), nativeObserverFixtureChildEnv+"="+marker, "PMX_TEST_NATIVE_OBSERVER_DISPATCH_PROBE=1")
				output, err := cmd.CombinedOutput()
				if marker == "1" {
					if err == nil || strings.Contains(string(output), "dispatch preserved") {
						t.Fatalf("owned child escaped dispatch: %v %s", err, output)
					}
				} else if err != nil || strings.TrimSpace(string(output)) != "native observer fixture dispatch preserved" {
					t.Fatalf("shared child intercepted: %v %s", err, output)
				}
			})
		}
	}
}

func TestNativeObserverExplicitPathsActualChildren(t *testing.T) {
	requireRealTmux(t)
	root := t.TempDir()
	// Keep the actual Unix socket/state root below the conservative 100-byte
	// broker and control limits, independently of the test's descriptive name.
	if own := os.Getenv("PROJMUX_TEST_OBSERVER_ROOT"); own != "" {
		if err := os.MkdirAll(own, 0700); err != nil {
			t.Fatal(err)
		}
		root = own
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("owned observer root must be empty: %v %v", entries, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
	if len(root) > 60 {
		root = shortTempDomain(t)
	}
	ambient := filepath.Join(root, "ambient")
	if err := os.MkdirAll(ambient, 0700); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("PROJMUX_TEST_OBSERVER_BINARY")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("HOME", ambient)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if binary != "" {
		executable = binary
	} else {
		source, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		executable = filepath.Join(root, "projmux-fixture")
		dest, err := os.OpenFile(executable, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(dest, source)
		closeErr := dest.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy fixture executable: %v %v", copyErr, closeErr)
		}
		// Only this copied app.test and its recovery children own the init routes.
		// Keep the parent and other shared fixture dispatchers unmarked.
		wrapper := filepath.Join(root, "native-observer-fixture")
		quoted := "'" + strings.ReplaceAll(executable, "'", `'\''`) + "'"
		script := "#!/bin/sh\nexport " + nativeObserverFixtureChildEnv + "=1\nexec " + quoted + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		executable = wrapper
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type fixture struct {
		target   codexLifecycleObserverTarget
		command  *aiCommand
		store    *intmetadata.Store
		endpoint *brokerTestEndpoint
		host     *codexbroker.Host
		tmux     func(...string) string
	}
	fixtures := []fixture{}
	for _, arm := range []string{"a", "b"} {
		dir := filepath.Join(root, arm)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(dir, "tmux")
		tmux := func(args ...string) string {
			t.Helper()
			p := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
			p.Env = cleanObserverEnv()
			out, err := p.CombinedOutput()
			if err != nil {
				t.Fatalf("tmux %v: %v %s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		pane := tmux("-f", "/dev/null", "new-session", "-d", "-s", "fixture", "-P", "-F", "#{pane_id}", "sleep 120")
		t.Cleanup(func() {
			p := exec.Command("tmux", "-S", socket, "kill-server")
			p.Env = cleanObserverEnv()
			if out, err := p.CombinedOutput(); err != nil {
				t.Errorf("owned tmux cleanup: %v %s", err, out)
			}
		})
		if got := tmux("display-message", "-p", "-t", pane, "#{socket_path}"); got != socket {
			t.Fatalf("socket=%s", got)
		}
		fake := newFakeResourceStore(t)
		identity := codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: pane, Generation: "generation-" + arm, ThreadID: "thread-" + arm}
		mutator := fake.mutator()
		if _, err := mutator.RecordPaneActivation(&fake.registry, identity.PaneUID, coremetadata.PaneActivationOptions{Generation: identity.Generation, RuntimeID: pane, AgentUID: identity.AgentUID, OperationID: "observer-" + arm}); err != nil {
			t.Fatal(err)
		}
		endpointRef := bindNativeCodexTestFixture(t, fake, mutator, coremetadata.CodexActivationObservation{AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation, ThreadID: identity.ThreadID})
		paths := mutationruntime.NativeObserverPaths{Paths: config.Paths{ConfigDir: filepath.Join(dir, "c"), StateDir: filepath.Join(dir, "s")}, RegistryPath: filepath.Join(dir, "registry.json"), Executable: executable}
		if err := os.MkdirAll(paths.Paths.ConfigDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := config.SaveAgentQuestionAnsweringFile(paths.Paths.AgentQuestionAnsweringFile(), config.AgentQuestionAnsweringProjmux); err != nil {
			t.Fatal(err)
		}
		if err := config.SaveDesktopNotifyModeFile(paths.Paths.DesktopNotifyModeFile(), config.DesktopNotifyModeOff); err != nil {
			t.Fatal(err)
		}
		// The opposite semantic settings prove that a child's config reader is
		// local, even when both roots contain exactly the same Agent/Pane UIDs.
		if arm == "b" {
			if err := os.WriteFile(paths.Paths.AISemanticPoliciesFile(), []byte(`{"events":{"response_complete":"quiet"}}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		store := intmetadata.NewStore(paths.RegistryPath)
		if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = fake.registry.Clone(); return nil }); err != nil {
			t.Fatal(err)
		}
		tmux("set-option", "-p", "-t", pane, tmuxopts.PaneUID, identity.PaneUID)
		tmux("set-option", "-p", "-t", pane, aiPaneAgentOption, aiModeCodex)
		key, err := codexbroker.NewEndpointKey(endpointRef.StateDomainID, endpointRef.EndpointGenerationID)
		if err != nil {
			t.Fatal(err)
		}
		discovery, err := codexbroker.NewImageDiscovery(paths.Paths.StateDir, key, codexBrokerImageOf(executable))
		if err != nil {
			t.Fatal(err)
		}
		endpoint := newBrokerTestEndpoint()
		var endpointMu sync.Mutex
		endpoint.respondWith("thread/read", `{"thread":{"id":"`+identity.ThreadID+`","status":{"type":"active"},"turns":[{"id":"turn-1","status":"inProgress","startedAt":1700000000}]}}`)
		broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) {
			endpointMu.Lock()
			defer endpointMu.Unlock()
			if endpoint.isClosed() {
				endpoint = newBrokerTestEndpoint()
				endpoint.respondWith("thread/read", `{"thread":{"id":"`+identity.ThreadID+`","status":{"type":"idle"},"turns":[]}}`)
			}
			return endpoint, nil
		}, Lifecycle: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			endpointMu.Lock()
			defer endpointMu.Unlock()
			return &brokerTestLifecycleEndpoint{shared: endpoint, peer: endpoint.peer}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = store.Update(func(reg *coremetadata.Registry) error {
				pane, _ := reg.Pane(identity.PaneUID)
				pane.Status.Activation.Generation = "cleanup"
				return nil
			})
			_ = host.Close()
			_ = broker.Close()
		})
		route, err := tmuxSocketPathTarget(socket)
		if err != nil {
			t.Fatal(err)
		}
		target := codexLifecycleObserverTarget{Identity: identity, Route: route, Paths: &paths, NativeRoute: codexNativeEndpointRoute{Endpoint: endpointRef, State: coremetadata.CodexGenerationCurrent, SocketPath: filepath.Join(dir, "provider"), TUIExecutable: executable}}
		command := newAICommand()
		result := command.startNativeCodexLifecycleObserver(target)
		if result.Status != codexObserverStartupReady {
			t.Fatalf("%s startup=%+v", arm, result)
		}
		fixtures = append(fixtures, fixture{target, command, store, endpoint, host, tmux})
		t.Logf("%s actual child ready: exact Agent/Pane/generation, State=%s Registry=%s broker image=%s", arm, paths.Paths.StateDir, paths.RegistryPath, discovery.Image())
	}
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s", label)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for _, f := range fixtures {
		paths, identity := f.target.Paths, f.target.Identity
		epoch := f.tmux("show-options", "-p", "-qv", "-t", identity.RuntimeID, aiPaneCodexEpochOption)
		if runtime.GOOS == "linux" {
			pidText, _, _ := strings.Cut(epoch, "-")
			data, err := os.ReadFile(filepath.Join("/proc", pidText, "cmdline"))
			if err != nil {
				t.Fatal(err)
			}
			actual := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			for index := 0; index < len(nativeObserverPathArgs(*paths)); index += 2 {
				expected := nativeObserverPathArgs(*paths)
				found := false
				for argIndex, arg := range actual {
					if arg == expected[index] && argIndex+1 < len(actual) && actual[argIndex+1] == expected[index+1] {
						found = true
					}
				}
				if !found {
					t.Fatalf("real child argv lost %s", expected[index])
				}
			}
		}

		response, err := callCodexControl(ctx, paths.Paths.StateDir, f.target.NativeRoute.Endpoint, identity, agentControlRequest{Operation: agentControlOpStatus, Identity: identity, Epoch: epoch})
		if err != nil || !response.OK {
			t.Fatalf("explicit control=%+v err=%v", response, err)
		}
		if _, err := callCodexControl(ctx, filepath.Join(ambient, "wrong"), f.target.NativeRoute.Endpoint, identity, agentControlRequest{Operation: agentControlOpStatus, Identity: identity, Epoch: epoch}); err == nil {
			t.Fatal("control escaped state domain")
		}
		fence, err := codexAuthorityFenceFileIn(paths.Paths.StateDir, identity.PaneUID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(fence); err != nil {
			t.Fatal(err)
		}
		f.endpoint.emit(codexappserver.Notification{Method: "turn/plan/updated", Params: json.RawMessage(`{"threadId":"` + identity.ThreadID + `","turnId":"turn-1","plan":[{"status":"inProgress","step":"PRIVATE"}]}`)})
		wait("progress", func() bool {
			reg, _ := f.store.Load()
			agent, _ := reg.Agent(identity.AgentUID)
			return agent.Status.Progress.PlanTotal == 1
		})
		f.endpoint.emit(codexappserver.Notification{Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`), Params: json.RawMessage(`{"threadId":"` + identity.ThreadID + `","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"pick","header":"Pick","question":"Pick one","options":[{"label":"A","description":"first"}]}]}`)})
		questionStore := agentquestion.NewStore(paths.Paths.StateDir)
		wait("question", func() bool { records, _ := questionStore.List(identity.AgentUID); return len(records) == 1 })
		f.endpoint.emit(codexappserver.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"` + identity.ThreadID + `","turn":{"id":"turn-1","status":"completed"}}`)})
		quiet := strings.HasSuffix(identity.ThreadID, "b")
		wantInteraction := coremetadata.InteractionResponseComplete
		if quiet {
			wantInteraction = coremetadata.InteractionUnknown
		}
		wait("completion", func() bool {
			reg, _ := f.store.Load()
			agent, _ := reg.Agent(identity.AgentUID)
			return agent.Status.Progress.IsZero() && agent.Status.Interaction.Kind == wantInteraction
		})
		wantAttention := "reply"
		if quiet {
			wantAttention = ""
		}
		wait("attention", func() bool {
			return f.tmux("show-options", "-p", "-qv", "-t", identity.RuntimeID, attentionStateOption) == wantAttention
		})
		notifyStore := notify.NewDefaultStore(paths.Paths)
		wait("notification policy", func() bool {
			records, _ := notifyStore.List()
			count := 0
			for _, record := range records {
				if record.Metadata[notify.MetaCategory] == "response_complete" {
					count++
				}
			}
			if quiet {
				return count == 0
			}
			return count == 1
		})
		// The creator/hook history writer consumes the identical explicit State.
		routed, err := f.command.withNativeObserverPaths(*paths)
		if err != nil {
			t.Fatal(err)
		}
		reg, _ := f.store.Load()
		agent, _ := reg.Agent(identity.AgentUID)
		record, ok := sessionhistory.RecordFor(identity.AgentUID, agent.Status.SessionRef, sessionhistory.SourceObserved)
		routed.recordClaudeSessionHistory(record, ok)
		if _, err := os.Stat(sessionhistory.Path(paths.Paths.StateDir)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(paths.Paths.StateDir, aiIngestLogName)); err != nil {
			t.Fatal(err)
		}
		// Replace the durable activation, then deliver another old event. The
		// observer exits its silent connection and the Registry stays untouched.
		if _, err := f.store.Update(func(reg *coremetadata.Registry) error {
			pane, _ := reg.Pane(identity.PaneUID)
			pane.Status.Activation.Generation = "replacement"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(paths.RegistryPath)
		if err != nil {
			t.Fatal(err)
		}
		f.endpoint.emit(codexappserver.Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"` + identity.ThreadID + `","status":{"type":"active"}}`)})
		wait("stale observer retired", func() bool {
			_, err := os.Stat(mustControlPath(t, *f.target.Paths, f.target.NativeRoute.Endpoint, identity))
			return os.IsNotExist(err)
		})
		after, _ := os.ReadFile(paths.RegistryPath)
		if string(before) != string(after) {
			t.Fatal("stale child changed Registry")
		}
		beforeState := f.tmux("show-options", "-p", "-qv", "-t", identity.RuntimeID, aiPaneStateOption)
		if result := f.command.startNativeCodexLifecycleObserver(f.target); result.Status != codexObserverStartupStale {
			t.Fatalf("stale parent=%+v", result)
		}
		if f.tmux("show-options", "-p", "-qv", "-t", identity.RuntimeID, aiPaneStateOption) != beforeState {
			t.Fatal("stale parent wrote Pane")
		}
		if _, err := f.store.Update(func(reg *coremetadata.Registry) error {
			pane, _ := reg.Pane(identity.PaneUID)
			pane.Status.Activation.Generation = identity.Generation
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		restarted := f.command.startNativeCodexLifecycleObserver(f.target)
		if restarted.Status != codexObserverStartupReady {
			t.Fatalf("crash fixture restart=%+v", restarted)
		}
		pidText, _, _ := strings.Cut(restarted.Epoch, "-")
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			t.Fatalf("fixture child PID=%s", pidText)
		}
		if err := syscall.Kill(pid, syscall.SIGQUIT); err != nil {
			t.Fatal(err)
		}
		crashPath, err := detachedCrashArtifactPath(paths.Paths.StateDir, detachedCrashRoleCodexBrokerWatch, pid)
		if err != nil {
			t.Fatal(err)
		}
		wait("explicit crash artifact", func() bool { _, err := os.Stat(crashPath); return err == nil })
		t.Logf("%s child: control/fence/progress/question/semantic/attention/history/journal/stale/crash PASS; ambient writes 0", identity.ThreadID)

	}
	if binary != "" {
		for _, args := range [][]string{
			{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--state-dir=" + root},
			{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--state-dir", root},
			{"internal", claudeQuestionPickerRoute, "--state-dir=" + root},
			{"internal", claudeQuestionPickerRoute, "-config-dir=" + root},
			{"internal", "codex-broker", "serve", "--state-domain=relative"},
			{"internal", "codex-broker", "serve", "--state-domain="},
			{"internal", "codex-broker", "serve", "--state-domain", "   "},
			{"internal", "codex-broker", "serve", "--state-domain"},
			{"internal", "codex-broker", "serve", "--state-domain=" + root, "--state-domain="},
		} {
			process := exec.CommandContext(ctx, binary, args...)
			process.Env = cleanObserverEnv()
			if output, err := process.CombinedOutput(); err == nil {
				t.Fatalf("partial actual child succeeded: %v %s", args, output)
			}
		}
	}
	entries, err := os.ReadDir(ambient)
	if err != nil || len(entries) != 0 {
		t.Fatalf("ambient HOME writes=%v err=%v", entries, err)
	}
}

func mustControlPath(t *testing.T, paths mutationruntime.NativeObserverPaths, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity) string {
	t.Helper()
	path, err := agentControlSocketPath(paths.Paths.StateDir, endpoint, identity)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func cleanObserverEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// Historical startup regression assertions now exercise service convergence.
func convergeCodexObserverStartupFallback(sink codexLifecycleSink, identity codexLifecycleIdentity, reason string) codexObserverStartupResult {
	starter := mutationruntime.NativeObserverStarter{BindingCurrent: func() bool { return sink.BindingCurrent(identity) }, SetAuthority: func(source, epoch, reason string) error { return sink.SetAuthority(identity, source, epoch, reason) }}
	result := starter.Converge(mutationruntime.NativeObserverStartup{Status: "fallback", Reason: reason})
	return codexObserverStartupResult{Status: codexObserverStartupStatus(result.Status), Epoch: result.Epoch, Reason: result.Reason, committed: result.Committed}
}
func convergeCodexObserverSlowStartup(sink aiCodexLifecycleSink, identity codexLifecycleIdentity, slow codexObserverStartupResult) codexObserverStartupResult {
	starter := mutationruntime.NativeObserverStarter{BindingCurrent: func() bool { return sink.BindingCurrent(identity) }, SetAuthorityFrom: func(from, source, epoch, reason string) (bool, error) {
		return sink.SetAuthorityFrom(identity, from, source, epoch, reason)
	}}
	result := starter.Converge(mutationruntime.NativeObserverStartup{Status: string(slow.Status), Epoch: slow.Epoch, Reason: slow.Reason, Committed: slow.committed})
	return codexObserverStartupResult{Status: codexObserverStartupStatus(result.Status), Epoch: result.Epoch, Reason: result.Reason, committed: result.Committed}
}

func TestNativeObserverRecoveryCapturesExplicitDomainAndExecutable(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "launcher")
	capture := filepath.Join(root, "argv")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+capture+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := mutationruntime.NativeObserverPaths{Paths: config.Paths{ConfigDir: root, StateDir: shortTempDomain(t)}, RegistryPath: filepath.Join(root, "registry.json"), Executable: executable}
	route := codexNativeEndpointRoute{Endpoint: coremetadata.CodexEndpointRef{StateDomainID: "codex-identity", EndpointGenerationID: "generation-1"}, State: coremetadata.CodexGenerationCurrent, TUIExecutable: executable, Default: true}
	session, err := newCodexBrokerObserverSessionForPaths(brokerTestIdentity("recovery"), "", nil, route, &paths)
	if err != nil {
		t.Fatal(err)
	}
	// Change ambient roots after constructing the session. Recovery must still
	// launch its captured executable and resolve the same original State domain.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	next := route
	next.Endpoint.EndpointGenerationID = "generation-2"
	discovery, launch, err := session.routeRuntime(next)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Domain() != paths.Paths.StateDir || discovery.Image() != codexBrokerImageOf(executable) {
		t.Fatalf("recovery discovery=%s image=%s", discovery.Domain(), discovery.Image())
	}
	if err := launch(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var data []byte
	for !strings.Contains(string(data), "generation-2") {
		data, _ = os.ReadFile(capture)
		if time.Now().After(deadline) {
			t.Fatalf("recovery argv=%s", data)
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(string(data), "--state-domain\n"+paths.Paths.StateDir+"\n") || !strings.Contains(string(data), "--endpoint-state-domain\ncodex-identity\n") {
		t.Fatalf("recovery escaped paths: %s", data)
	}
}

func TestNativeObservationOuterRoutingBoundary(t *testing.T) {
	root := t.TempDir()
	paths := mutationruntime.NativeObserverPaths{Paths: config.Paths{ConfigDir: root, StateDir: root}, RegistryPath: filepath.Join(root, "registry.json"), Executable: filepath.Join(root, "projmux")}
	args := append([]string{"internal", claudeQuestionPickerRoute}, nativeObserverPathArgs(paths)...)
	if state, explicit, err := NativeObservationStateDir(args); err != nil || !explicit || state != root {
		t.Fatalf("outer picker routing=%s/%t/%v", state, explicit, err)
	}
	if shouldRunLegacyHookMigrations(args) {
		t.Fatal("explicit picker migrated ambient config")
	}
	for _, args := range [][]string{
		{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--state-dir", root},
		{"internal", claudeQuestionPickerRoute, "--config-dir", root},
		{"internal", "codex-broker", "serve", "--state-domain", "relative"},
	} {
		if _, explicit, err := NativeObservationStateDir(args); !explicit || err == nil || shouldRunLegacyHookMigrations(args) {
			t.Fatalf("partial tuple fell back to ambient default: %v", args)
		}
	}
	if _, explicit, _ := NativeObservationStateDir([]string{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute}); explicit {
		t.Fatal("legacy omission changed")
	}
}

func TestNativeQuestionOuterAndPickerFlagFormsAgree(t *testing.T) {
	root := t.TempDir()
	paths := mutationruntime.NativeObserverPaths{Paths: config.Paths{ConfigDir: root, StateDir: root}, RegistryPath: filepath.Join(root, "registry.json"), Executable: filepath.Join(root, "projmux")}
	for _, dash := range []string{"--", "-"} {
		argv := []string{}
		routing := nativeObserverPathArgs(paths)
		for index := 0; index < len(routing); index += 2 {
			argv = append(argv, dash+strings.TrimLeft(routing[index], "-")+"="+routing[index+1])
		}
		// Go flag last-value wins is preserved by the outer journal adapter.
		argv = append(argv, dash+"state-dir="+filepath.Join(root, "last"))
		expected := paths
		expected.Paths.StateDir = filepath.Join(root, "last")
		input, err := parseClaudeQuestionPickerInput(argv, io.Discard)
		state, explicit, outerErr := NativeObservationStateDir(append([]string{"internal", claudeQuestionPickerRoute}, argv...))
		if err != nil || outerErr != nil || !explicit || state != expected.Paths.StateDir || input.Paths != expected {
			t.Fatalf("parser mismatch %v: %v %v %s", argv, err, outerErr, state)
		}
		partial := []string{dash + "state-dir=" + root}
		if _, explicit, err := NativeObservationStateDir(append([]string{"internal", claudeQuestionPickerRoute}, partial...)); !explicit || err == nil {
			t.Fatalf("partial equals tuple fell back: %v", partial)
		}
	}
}

func TestNativeBrokerOuterAndServeAdmissionAgree(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name            string
		args            []string
		valid, explicit bool
		domain          string
	}{
		{name: "omitted", valid: true},
		{name: "empty", args: []string{"--state-domain="}, explicit: true},
		{name: "space", args: []string{"--state-domain", "   "}, explicit: true},
		{name: "relative", args: []string{"-state-domain=relative"}, explicit: true},
		{name: "partial", args: []string{"--state-domain"}, explicit: true},
		{name: "absolute", args: []string{"--state-domain=" + root}, valid: true, explicit: true, domain: root},
		{name: "duplicate valid last", args: []string{"--state-domain=", "-state-domain", root}, valid: true, explicit: true, domain: root},
		{name: "duplicate empty last", args: []string{"--state-domain", root, "--state-domain="}, explicit: true},
		{name: "duplicate relative last", args: []string{"--state-domain", root, "--state-domain=relative"}, explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := parseCodexBrokerServeInput(test.args, io.Discard)
			state, explicit, outerErr := NativeObservationStateDir(append([]string{"internal", "codex-broker", "serve"}, test.args...))
			if (err == nil) != test.valid || (outerErr == nil) != test.valid || explicit != test.explicit {
				t.Fatalf("input=%+v/%v outer=%s/%t/%v", input, err, state, explicit, outerErr)
			}
			if test.valid {
				if input.Explicit != test.explicit || input.StateDomain != test.domain || state != test.domain {
					t.Fatalf("input=%+v outer=%s", input, state)
				}
				return
			}
			command := newCodexBrokerCommand()
			command.homeDir = func() (string, error) { t.Fatal("malformed admission read default HOME"); return "", nil }
			if err := command.runServe(test.args, io.Discard, io.Discard); err == nil {
				t.Fatal("malformed serve started")
			}
		})
	}
}
