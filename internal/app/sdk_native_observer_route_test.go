package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The public SDK create must start the native observer on the same physical
// tmux server it created the Pane on, however that server was selected. The
// existing TestSDKNativeCodexCreateExplicitRootsAndFrozenEnvironment only ever
// passes an explicit socket path, so it never exercises the logical-name route
// an SDK client gets when it does not name a socket.
//
// Every lane goes through the public SDK against the same fake Codex protocol
// server, a real tmux server under a private TMUX_TMPDIR and the real projmux
// binary as the observer child: client.CreateAgent after CreateWindow for the
// explicit and existing-server lanes, and one client.CreateWindowFromIntent
// that also creates the server for the fresh lane. A fixture-owned wrapper
// records the tmux route flag the SDK handed the observer, so each lane proves
// which route it took.
func TestSDKNativeCodexCreateObserverStaysOnItsTmuxServer(t *testing.T) {
	requireRealTmux(t)
	binary := sdkCreateBinary(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for protocol fixture")
	}
	ambientPath := os.Getenv("PATH")
	const secretStderr = "SECRET-OBSERVER-STDERR-TOKEN"
	for _, lane := range []string{"explicit-socket-path", "existing-app-server", "fresh-no-server", "explicit-child-exits"} {
		t.Run(lane, func(t *testing.T) {
			// Provider and broker Unix sockets must fit even under a long CI TMPDIR.
			root, err := os.MkdirTemp("/tmp", "pmxo")
			if err != nil {
				t.Fatal(err)
			}
			retired, protocolWaited := false, false
			t.Cleanup(func() {
				if retired && protocolWaited {
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				} else {
					t.Logf("preserved unresolved observer route fixture root=%s; owner review required", root)
				}
			})
			home, state, config := filepath.Join(root, "home"), filepath.Join(root, "state", "projmux"), filepath.Join(root, "config", "projmux")
			providerRoot, tmuxTmp := filepath.Join(root, "codex"), filepath.Join(root, "tmux")
			bin := filepath.Join(home, ".local", "bin")
			for _, dir := range []string{home, bin, providerRoot, tmuxTmp} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			observerLog, routeLog := filepath.Join(root, "observer-receipts.tsv"), filepath.Join(root, "observer-route.log")
			inner := filepath.Join(root, "projmux-inner")
			relaunchObserverExecutable(t, inner, binary, observerLog)
			executable := filepath.Join(root, "projmux")
			exits := lane == "explicit-child-exits"
			wrapper := fmt.Sprintf(`#!/bin/sh
if [ "$1 $2 $3 $4" = 'internal agent-hook ingest codex-broker-watch' ]; then
 %s
 previous=''
 for argument do
  case "$previous" in
   --tmux-socket-path|--tmux-socket-name) printf '%%s|%%s\n' "$previous" "$argument" >> %q;;
  esac
  previous="$argument"
 done
fi
exec %q "$@"
`, map[bool]string{true: "echo " + secretStderr + " >&2; exit 1", false: ""}[exits], routeLog, inner)
			if err := os.WriteFile(executable, []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "shared.py")
			if err := os.WriteFile(script, []byte(codexHostMoveServerFixture), 0600); err != nil {
				t.Fatal(err)
			}
			provider := "#!/bin/sh\ncase \"$1 $2\" in\n'app-server proxy' | 'app-server daemon') exec " + fmt.Sprintf("%q -u %q", python, script) + " \"$@\";;\nesac\nexec /bin/sleep 120\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(provider), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":"+ambientPath)
			t.Setenv("CODEX_HOME", providerRoot)
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			// The tmux server lives under this private directory only. The observer
			// environment is a fixed allow-list without TMUX_TMPDIR, so a route that
			// depends on it would reach a different server than the SDK's.
			t.Setenv("TMUX_TMPDIR", tmuxTmp)
			t.Setenv("TMUX", "")
			if err := os.Unsetenv("TMUX"); err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(providerRoot, "app-server-control", "app-server-control.sock")
			server := exec.Command(python, "-u", script, "serve", control)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = server.Process.Kill()
				waitErr := server.Wait()
				protocolWaited = true
				t.Logf("owned protocol server pid=%d Wait returned: %v", server.Process.Pid, waitErr)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(control); return err == nil })

			// target selects the one tmux server the fixture reads back from.
			var target []string
			options := projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", ProjmuxExecutable: executable}
			privateSocket := filepath.Join(tmuxTmp, fmt.Sprintf("tmux-%d", os.Getuid()), "projmux")
			switch lane {
			case "explicit-socket-path", "explicit-child-exits":
				socket, _ := sdkCreateSocket(t)
				options.SocketPath = socket
				target = []string{"-S", socket}
			case "existing-app-server":
				target = []string{"-L", "projmux"}
				t.Cleanup(func() { sdkCreateKillServer(t, privateSocket) })
				run := func(args ...string) {
					t.Helper()
					if out, err := (tmux.ExecRunner{}).Run(ctx, "tmux", append(slices.Clone(target), args...)...); err != nil {
						t.Fatalf("tmux %v: %v %s", args, err, out)
					}
				}
				run("-f", "/dev/null", "new-session", "-d", "-s", "control", "-n", "control", "/bin/sh")
				run("set-option", "-g", tmuxopts.AppGlobal, "1")
				run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
			default:
				target = []string{"-L", "projmux"}
				t.Cleanup(func() { sdkCreateKillServer(t, privateSocket) })
			}
			tmuxOut := func(args ...string) string {
				t.Helper()
				out, err := (tmux.ExecRunner{}).Run(ctx, "tmux", append(slices.Clone(target), args...)...)
				if err != nil {
					t.Fatalf("tmux %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}

			// A server-creating create reads the generated app tmux config, which
			// config apply normally writes; write the same minimal file the other
			// SDK create fixtures use instead of running an install.
			if err := os.MkdirAll(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "tmux.conf"), []byte("set -g "+tmuxopts.AppGlobal+" 1\nset -g default-shell /bin/sh\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if exits {
				// Installed exactly as an application invocation installs it.
				recorder := diagnostics.NewLifecycleRecorder(diagnostics.NewStore(filepath.Join(root, "invocation.jsonl")), "sdk-observer-fixture", "1.0.0", "tmux")
				t.Cleanup(observeCodexObserverFallbacks(recorder, []string{"agent", "create"}))
			}
			client, err := projmux.NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			project, err := client.CreateProject(ctx, core.CreateProjectRequest{Root: root, Name: "sample"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.WriteProfile(ctx, "native", []byte("provider = \"codex\"\nmodel = \"stub-model\"\neffort = \"low\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"on-request\"\n")); err != nil {
				t.Fatal(err)
			}
			var ownedUID, ownedWindow, paneID, physical string
			births := map[string]metadata.ProcessIdentity{}
			t.Cleanup(func() {
				cleaned := false
				if lane == "fresh-no-server" {
					cleaned = cleanupOwnedObserverRouteWindow(t, client, state, ownedUID, ownedWindow, physical)
				} else {
					cleaned = cleanupRelaunchBinding(t, client, state, ownedUID, ownedWindow, physical)
				}
				if ownedUID != "" && !cleaned {
					return
				}
				if ownedUID != "" && !exits && !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, true) {
					return
				}
				cleanupSDKNativePeers(t, state)
				retired = true
			})
			var agent *core.Agent
			var notices []string
			if lane == "fresh-no-server" {
				// No Window exists yet and no tmux server answers: one public intent
				// creates the server, the Window and the native Agent together, so the
				// no-server bind and the observer launch are the same operation.
				if out, err := (tmux.ExecRunner{}).Run(ctx, "tmux", append(slices.Clone(target), "list-sessions")...); err == nil {
					t.Fatalf("fresh lane needs no tmux server before the create, but one answered: %s", out)
				}
				intent := core.WindowCreateIntent{Project: "uid:" + project.Project.Metadata.UID, Name: "native", Agent: &core.WindowIntentAgent{Provider: "codex", Profile: "native", Payload: []string{"hello-native"}}}
				result, err := client.CreateWindowFromIntent(ctx, intent)
				if err != nil {
					t.Fatal(err)
				}
				agent, paneID, ownedWindow = result.Agent, result.PaneID, result.Window.Metadata.UID
			} else {
				window, err := client.CreateWindow(ctx, core.CreateWindowRequest{Project: "sample", Name: "native"})
				if err != nil {
					t.Fatal(err)
				}
				ownedWindow = window.Window.Metadata.UID
				req := core.CreateAgentRequest{Project: "uid:" + project.Project.Metadata.UID, Window: "uid:" + ownedWindow, Provider: "codex", Name: "native-worker", Profile: "native", Payload: []string{"hello-native"}}
				result, err := client.CreateAgent(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				agent, paneID, notices = &result.Agent, result.PaneID, result.Notices
			}
			if agent == nil || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID != "process-thread" || paneID == "" {
				t.Fatalf("missing committed native result: agent=%+v pane=%q", agent, paneID)
			}
			ownedUID = agent.Metadata.UID

			if exits {
				// The observer child is killed at once, writing a secret to stderr. The
				// public create still succeeds, the Pane stays on the provider hook, the
				// result carries one fixed notice, and the journal the app installs gets
				// exactly one bounded record -- none of them carries the child's output.
				physical = tmuxOut("display-message", "-p", "-t", paneID, "#{socket_path}")
				var authority, reason string
				deadline := time.Now().Add(30 * time.Second)
				for {
					authority = tmuxOut("show-options", "-pqv", "-t", paneID, aiPaneCodexAuthorityOption)
					reason = tmuxOut("show-options", "-pqv", "-t", paneID, aiPaneCodexReasonOption)
					if authority == codexAuthorityHook || time.Now().After(deadline) {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if authority != codexAuthorityHook || reason != "observer-exited" {
					t.Fatalf("pane authority = %q reason = %q, want the provider hook with observer-exited", authority, reason)
				}
				var observer []string
				for _, notice := range notices {
					if strings.Contains(notice, "Codex observer") {
						observer = append(observer, notice)
					}
				}
				if len(observer) != 1 || !strings.Contains(observer[0], "observer-exited") || !strings.Contains(observer[0], "provider hook") {
					t.Fatalf("create notices = %q, want exactly one fixed observer notice naming observer-exited", notices)
				}
				journalPath := filepath.Join(state, diagnostics.LogDirName, diagnostics.LogFileName)
				events, err := diagnostics.NewStore(journalPath).Read()
				if err != nil {
					t.Fatalf("operations journal unreadable: %v", err)
				}
				var fallbacks []diagnostics.Event
				for _, event := range events {
					if event.Event == "codex.observer.fallback" {
						fallbacks = append(fallbacks, event)
					}
				}
				if len(fallbacks) != 1 || fallbacks[0].Code != "observer-exited" || fallbacks[0].AgentUID != agent.Metadata.UID || fallbacks[0].PaneUID != agent.Status.PaneRef || fallbacks[0].Source != "sdk" || fallbacks[0].Operation != "observer-start" {
					t.Fatalf("journal fallback records = %+v, want exactly one for this Agent", fallbacks)
				}
				raw, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, leak := range []string{secretStderr, physical, "process-thread"} {
					if strings.Contains(string(raw), leak) || strings.Contains(strings.Join(notices, "\n"), leak) {
						t.Fatalf("journal or notices leak %q", leak)
					}
				}
				return
			}
			// The SDK must hand the observer the exact physical socket the Pane lives on.
			physical = tmuxOut("display-message", "-p", "-t", paneID, "#{socket_path}")
			if !filepath.IsAbs(physical) {
				t.Fatalf("parent tmux socket is not absolute: %q", physical)
			}
			routeRaw, err := os.ReadFile(routeLog)
			if err != nil {
				t.Fatalf("observer route not recorded (observer was never launched?): %v", err)
			}
			routes := strings.Fields(strings.TrimSpace(string(routeRaw)))
			if len(routes) != 1 || routes[0] != "--tmux-socket-path|"+physical {
				t.Fatalf("observer tmux route = %q, want the exact physical socket --tmux-socket-path|%s", routes, physical)
			}

			// The observer must stay alive and own the authority, not the hook fallback.
			var authority, reason string
			deadline := time.Now().Add(30 * time.Second)
			for {
				authority = tmuxOut("show-options", "-pqv", "-t", paneID, aiPaneCodexAuthorityOption)
				reason = tmuxOut("show-options", "-pqv", "-t", paneID, aiPaneCodexReasonOption)
				if authority == codexAuthorityControlPlane || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if authority != codexAuthorityControlPlane {
				t.Fatalf("pane authority = %q reason = %q, want %q: the observer did not keep control", authority, reason, codexAuthorityControlPlane)
			}
			if !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, false) {
				t.Fatal("observer did not stay alive on its tmux server")
			}

			wire, err := os.ReadFile(filepath.Join(home, "shared-wire.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			start, turn := 0, 0
			for line := range strings.SplitSeq(string(wire), "\n") {
				var frame struct {
					Method string `json:"method"`
				}
				if json.Unmarshal([]byte(line), &frame) != nil {
					continue
				}
				switch frame.Method {
				case "thread/start":
					start++
				case "turn/start":
					turn++
				}
			}
			if start != 1 || turn != 1 {
				t.Fatalf("thread/turn cardinality %d/%d", start, turn)
			}
		})
	}
}

// cleanupOwnedObserverRouteWindow deletes the Window the fresh lane's intent
// created, together with the one Agent and Pane in it. Unlike the other lanes,
// the fixture owns the whole Window, so the delete plan is checked to name only
// that Window, Agent and Pane before it is confirmed.
func cleanupOwnedObserverRouteWindow(t *testing.T, c *projmux.Client, state, agentUID, window, socket string) bool {
	t.Helper()
	if agentUID == "" || window == "" || socket == "" {
		t.Error("fixture Window ownership became uncertain")
		return false
	}
	reg := projectRegistry(t, state)
	a, ok := reg.Agent(agentUID)
	if !ok || a.Metadata.OwnerRef == nil || a.Metadata.OwnerRef.UID != window {
		t.Error("fixture Agent ownership became uncertain")
		return false
	}
	owned := map[string]core.Kind{agentUID: core.KindAgent, window: core.KindWindow}
	if a.Status.PaneRef != "" {
		owned[a.Status.PaneRef] = core.KindPane
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + window, SocketPath: socket, DryRun: true}
	preview, err := c.Delete(ctx, req)
	if err != nil || len(preview.Plan.Targets) != 1 || preview.Plan.Targets[0].UID != window || preview.Plan.Targets[0].Kind != core.KindWindow || len(preview.RegistryOnly) != 0 {
		t.Errorf("owned Window delete preview failed: %v", err)
		return false
	}
	for _, descendant := range preview.Plan.Targets[0].Descendants {
		if owned[descendant.UID] != descendant.Kind {
			t.Errorf("delete descendant %s escaped the fixture set", descendant.UID)
			return false
		}
	}
	for _, affected := range preview.Plan.Affected {
		kind, ok := owned[affected.UID]
		if !ok || !strings.EqualFold(string(kind), affected.Kind) {
			t.Errorf("delete affected %s escaped the fixture set", affected.UID)
			return false
		}
	}
	req.DryRun, req.Confirm = false, true
	result, err := c.Delete(ctx, req)
	if err != nil || !result.Committed {
		t.Errorf("owned Window cleanup failed: %v", err)
		return false
	}
	return true
}
