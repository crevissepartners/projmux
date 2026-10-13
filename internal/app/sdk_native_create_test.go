package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	configpkg "github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	storepkg "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Two separate existing endpoint fixtures exercise the public SDK, including
// real observer/broker children. No fixture dispatch init or provider auth is used.
func TestSDKNativeCodexCreateExplicitRootsAndFrozenEnvironment(t *testing.T) {
	requireRealTmux(t)
	binary := sdkCreateBinary(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for protocol fixture")
	}
	ambientPath := os.Getenv("PATH")
	for _, lane := range []string{"cli", "sdk-a", "sdk-b"} {
		sdk := lane != "cli"
		t.Run(lane, func(t *testing.T) {
			root, err := os.MkdirTemp(os.TempDir(), "t3n-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			home, state, config := filepath.Join(root, "home"), filepath.Join(root, "state", "projmux"), filepath.Join(root, "config", "projmux")
			providerRoot := filepath.Join(root, "codex")
			bin := filepath.Join(home, ".local", "bin")
			for _, dir := range []string{home, bin, providerRoot} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := filepath.Join(root, "shared.py")
			if err := os.WriteFile(script, []byte(codexHostMoveServerFixture), 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$*\" \"$HOME\" \"$CODEX_HOME\" \"$PATH\" >> " + fmt.Sprintf("%q", filepath.Join(root, "provider-environment.log")) + "\ncase \"$1 $2\" in\n'app-server proxy' | 'app-server daemon') exec " + fmt.Sprintf("%q -u %q", python, script) + " \"$@\";;\nesac\nexec /bin/sleep 120\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":"+ambientPath)
			t.Setenv("CODEX_HOME", providerRoot)
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			control := filepath.Join(providerRoot, "app-server-control", "app-server-control.sock")
			server := exec.Command(python, "-u", script, "serve", control)
			var stderr bytes.Buffer
			server.Stderr = &stderr
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = server.Process.Kill()
				_ = server.Wait()
				if t.Failed() {
					t.Log(stderr.String())
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(control); return err == nil })
			socket, run := sdkCreateSocket(t)
			options := projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary}
			client, err := projmux.NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			project, err := client.CreateProject(ctx, core.CreateProjectRequest{Root: root, Name: "sample"})
			if err != nil {
				t.Fatal(err)
			}
			window, err := client.CreateWindow(ctx, core.CreateWindowRequest{Project: "sample", Name: "native"})
			if err != nil {
				t.Fatal(err)
			}
			parent := seedSDKResumeAgent(t, state, window.Window.Metadata.UID, "codex", root, metadata.PhaseOffline)
			if _, err := client.WriteInstructions(ctx, "reviewer", []byte("Read carefully.")); err != nil {
				t.Fatal(err)
			}
			if _, err := client.WriteProfile(ctx, "native", []byte("provider = \"codex\"\ninstructions = \"reviewer\"\nmodel = \"stub-model\"\neffort = \"low\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"on-request\"\n")); err != nil {
				t.Fatal(err)
			}
			// Capture was before the environment changes. The caller's new provider
			// root must not affect proxy, manager probe, TUI or observer recovery.
			if sdk {
				other := t.TempDir()
				trap := filepath.Join(other, "codex")
				trace := filepath.Join(root, "ambient-codex-calls")
				if err := os.WriteFile(trap, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+fmt.Sprintf("%q", trace)+"\nexit 91\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HOME", other)
				t.Setenv("CODEX_HOME", other)
				t.Setenv("PATH", other+":"+ambientPath)
				t.Cleanup(func() {
					if raw, err := os.ReadFile(trace); err == nil {
						t.Errorf("ambient Codex invoked: %s", raw)
					}
				})
			}
			t.Cleanup(func() { cleanupSDKNativePeers(t, state) })
			req := core.CreateAgentRequest{Project: "uid:" + project.Project.Metadata.UID, Window: "uid:" + window.Window.Metadata.UID, Provider: "codex", Name: "native-worker", Profile: "native", Creator: "uid:" + parent, Payload: []string{"hello-native"}}
			var result core.CreateAgentResult
			if sdk {
				result, err = client.CreateAgent(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if result.Agent.Status.SessionRef == nil || result.Agent.Status.SessionRef.Codex == nil || result.Agent.Status.SessionRef.Codex.ThreadID != "process-thread" || result.PaneID == "" || result.Receipt.Profile == nil {
					t.Fatalf("missing committed native result: %+v", result)
				}
				// Returned public resources must not alias the persisted native binding.
				result.Agent.Status.SessionRef.Codex.ThreadID = "changed"
			} else {
				c := sdkAgentCLI(t, state, config, socket, binary, home)
				c.codexNative = newCodexNativeThreadController(state)
				// Align CLI's explicit observer tuple with SDK's same-input tuple.
				c.resumes.(*aiCommand).nativeObserverPaths = &mutationruntime.NativeObserverPaths{Paths: configpkg.Paths{StateDir: state, ConfigDir: config}, RegistryPath: storepkg.PathFor(state), Executable: binary}
				var out, stderr bytes.Buffer
				err = c.Run([]string{"agent", "--host", "tmux", "--project", req.Project, "--window", req.Window, "--provider", "codex", "--name", "native-worker", "--profile", "native", "--creator", req.Creator, "-o", "receipt", "--", "hello-native"}, &out, &stderr)
				if err != nil {
					t.Fatalf("CLI: %v %s", err, stderr.String())
				}
			}
			reg := projectRegistry(t, state)
			var created metadata.Agent
			for _, a := range reg.Agents {
				if a.Metadata.Name == "native-worker" {
					created = a
				}
			}
			if created.Status.SessionRef == nil || created.Status.SessionRef.Codex.ThreadID != "process-thread" || created.Metadata.Annotations[metadata.AnnotationCreatorAgent] != parent || created.Status.Activation.State != metadata.ActivationAcknowledged {
				t.Fatalf("native state/creator mismatch: %+v", created)
			}
			var paneID string
			for _, p := range reg.Panes {
				if p.Metadata.UID == created.Status.PaneRef {
					paneID = p.Status.Activation.RuntimeID
				}
			}
			waitCodexCreate(t, ctx, func() bool {
				return run("show-options", "-pqv", "-t", paneID, aiPaneCodexAuthorityOption) == codexAuthorityControlPlane
			})
			rows, err := sessionhistory.List(state, created)
			if err != nil || len(rows.Sessions) != 1 {
				t.Fatalf("history: %+v %v", rows, err)
			}

			envlog, err := os.ReadFile(filepath.Join(root, "provider-environment.log"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("provider environment ledger: %s", envlog)
			for line := range strings.SplitSeq(strings.TrimSpace(string(envlog)), "\n") {
				fields := strings.SplitN(line, "|", 4)
				if len(fields) != 4 || fields[1] != home || fields[2] != providerRoot || !strings.HasPrefix(fields[3], bin+":") {
					t.Fatalf("provider child left frozen environment: %s", line)
				}
			}
			wire, err := os.ReadFile(filepath.Join(home, "shared-wire.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			start, turn := 0, 0
			for line := range bytes.SplitSeq(wire, []byte("\n")) {
				var frame struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal(line, &frame) != nil {
					continue
				}
				switch frame.Method {
				case "thread/start":
					start++
					if !strings.Contains(fmt.Sprint(frame.Params["developerInstructions"]), "Read carefully.") || !strings.Contains(fmt.Sprint(frame.Params["developerInstructions"]), created.Metadata.UID) || frame.Params["model"] != "stub-model" || frame.Params["approvalPolicy"] != "on-request" || frame.Params["sandbox"] != "read-only" {
						t.Fatalf("native payload/profile: %+v", frame.Params)
					}
				case "turn/start":
					turn++
					if !strings.Contains(fmt.Sprint(frame.Params), "hello-native") || frame.Params["model"] != "stub-model" {
						t.Fatalf("initial turn: %+v", frame.Params)
					}
				}
			}
			if start != 1 || turn != 1 {
				t.Fatalf("thread/turn cardinality %d/%d", start, turn)
			}
			// SDK resume stays deferred, including for an Agent this SDK created.
			_, err = client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:" + parent})
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResumeNativeCodexUnsupported {
				t.Fatalf("resume guard changed: %v", err)
			}
		})
	}
}

// Authenticate each owned peer through a private fixture socket, then recheck
// socket and process birth before stopping it. Retire readers before removing
// fake providers, so recovery cannot seek an executable beyond the fixture.
func cleanupSDKNativePeers(t *testing.T, state string) {
	t.Helper()
	for _, dir := range []string{"agent-control", "broker"} {
		sockets, _ := filepath.Glob(filepath.Join(state, dir, "*.sock"))
		for _, address := range sockets {
			socketIdentity, err := localipc.InspectOwnedSocket(address)
			if err != nil {
				continue
			}
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
			if err != nil {
				continue
			}
			process, _, err := localipc.PeerProcess(conn)
			_ = conn.Close()
			if err != nil || process.OwnerUID != uint32(os.Getuid()) {
				continue
			}
			current, _, err := localipc.Process(process.PID)
			observed, socketErr := localipc.InspectOwnedSocket(address)
			if err == nil && current == process && socketErr == nil && observed == socketIdentity {
				if err := syscall.Kill(process.PID, syscall.SIGTERM); err == nil {
					waitCodexFixturePeerExit(t, process)
				}
			}
		}
	}
}
