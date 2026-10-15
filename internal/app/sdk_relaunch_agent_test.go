package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	configpkg "github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Planning compares the actual public SDK with the native CLI plan over the
// same Registry. It needs no provider or tmux effects, including for busy Agents.
func TestRelaunchAgentSDKCLIPlanParity(t *testing.T) {
	relaunchProviderFence(t)
	for _, tc := range []struct {
		name, model, effort string
		busy                bool
	}{
		{name: "unchanged", effort: "high"}, {name: "same-model", model: "sonnet"}, {name: "busy", model: "opus", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {

			home := sdkAgentHome(t)
			root := t.TempDir()
			state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
				t.Fatal(err)
			}
			reg := projectRegistry(t, state)
			uid := seedSDKResumeAgent(t, state, reg.Windows[0].Metadata.UID, "claude", root, metadata.PhaseOffline)
			_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
				m := store.DefaultMutator()
				if _, err := m.AttachAgentPane(r, uid, metadata.BootstrapPane{Name: "running-pane", CWD: root}, "fixture-running"); err != nil {
					return err
				}
				if _, err := m.SetAgentModel(r, uid, "sonnet", metadata.SettingSourceRelaunch); err != nil {
					return err
				}
				if _, err := m.SetAgentEffort(r, uid, "high", metadata.SettingSourceRelaunch); err != nil {
					return err
				}
				a, _ := r.Agent(uid)
				a.Status.Interaction = metadata.AgentInteraction{Kind: metadata.InteractionIdle, ObservedAt: time.Now(), Source: string(metadata.InteractionSourceProviderHook)}
				if tc.busy {
					a.Status.Interaction.Kind = metadata.InteractionUnknown
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cliCommand := sdkResumeCLI(t, state, config, "", "", home)
			reg = projectRegistry(t, state)
			a, _ := reg.Agent(uid)
			before := projectRegistryBytes(t, state)
			got, err := client.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + uid, Model: tc.model, Effort: tc.effort, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			err = cliCommand.runTmuxRelaunch(reg, *a, agentRelaunchRequest{model: tc.model, effort: tc.effort, dryRun: true, json: true}, &output, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			var cli agentRelaunchResult
			if err := json.Unmarshal(output.Bytes(), &cli); err != nil {
				t.Fatal(err)
			}
			if got.Outcome != cli.Outcome || got.Restart != cli.Restart || got.Unchanged != cli.Unchanged || got.ConfirmationRequired != cli.ConfirmationRequired || !reflect.DeepEqual(got.RelaunchReasons, cli.RelaunchReasons) {
				t.Fatalf("SDK %+v CLI %+v", got, cli)
			}
			if got.CurrentSettings.Model.Value != cli.CurrentSettings.Model.Value || got.NewSettings.Effort.Value != cli.NewSettings.Effort.Value {
				t.Fatalf("settings differ SDK %+v CLI %+v", got, cli)
			}
			if before != projectRegistryBytes(t, state) {
				t.Fatal("dry run changed state")
			}
			if tc.busy {
				_, err = client.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + uid, Model: "opus"})
				var refused *projmux.Refusal
				if !errors.As(err, &refused) || refused.Code != projmux.CodeRelaunchAgentBusy {
					t.Fatal(err)
				}
			}
		})
	}
}

func sdkRelaunchCLI(t *testing.T, state, config, socket, binary, home string) *agentCommand {
	t.Helper()
	c := sdkResumeCLI(t, state, config, socket, binary, home)
	storage := store.NewStore(store.PathFor(state))
	del := newDeleteCommand()
	del.store = &resourceStore{load: storage.LoadReadOnly, update: storage.Update, mutator: store.DefaultMutator, stateDir: func() (string, error) { return state, nil }}
	del.lookupEnv = func(string) string { return "" }
	del.windows = &tmuxWindowDeleteRuntime{Runner: tmux.ExecRunner{}}
	del.panes = &tmuxPaneDeleteRuntime{TmuxPaneDeleteRuntime: runtime.TmuxPaneDeleteRuntime{Runner: tmux.ExecRunner{}, Getenv: func(string) string { return "" }}}
	del.activeTarget = func() (activeTargetObserver, bool) { return activeTargetObserver{}, false }
	c.paneDelete = del
	return c
}

// Persistent shell provider fixtures exercise the normal transaction and native
// CLI stop/rebind path. They never invoke an installed Claude/Codex binary.
func TestRelaunchAgentSDKCLIExecutionParity(t *testing.T) {
	relaunchProviderFence(t)
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	for _, phase := range []metadata.AgentPhase{metadata.PhaseOffline, metadata.PhaseFailed, metadata.PhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			var settings, notices []string
			for _, sdk := range []bool{false, true} {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, _ := sdkCreateSocket(t)
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				w, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"})
				if err != nil {
					t.Fatal(err)
				}
				uid := seedSDKResumeAgent(t, state, w.Window.Metadata.UID, "claude", root, metadata.PhaseOffline)
				if phase == metadata.PhaseFailed {
					_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
						_, err := store.DefaultMutator().TransitionAgent(r, uid, metadata.PhaseFailed, "fixture failure")
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				if phase == metadata.PhaseRunning {
					// A live pane is obtained by the existing shared resume transaction.
					_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
						a, _ := r.Agent(uid)
						a.Status.Phase = metadata.PhaseOffline
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid}); err != nil {
						t.Fatal(err)
					}
					waitSDKResumeBirth(t, state, uid)
				}
				reg := projectRegistry(t, state)
				old, _ := reg.Agent(uid)
				oldPane := old.Status.PaneRef
				var stderr bytes.Buffer
				if sdk {
					result, err := client.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + uid, Model: "sonnet", Effort: "high", Confirm: true})
					if err != nil {
						t.Fatal(err)
					}
					if result.AgentUID != uid || result.NewPaneUID == "" || result.NewPaneUID == oldPane || result.Resume.Receipt.Effects.Identity != core.IdentityReused {
						t.Fatalf("bad result %+v", result)
					}
					if phase == metadata.PhaseRunning && !result.Stop.Committed {
						t.Fatal("missing stop receipt")
					}
					notices = append(notices, strings.Join(result.Notices, "\n"))
				} else {
					c := sdkRelaunchCLI(t, state, config, socket, binary, home)
					var output bytes.Buffer
					err = c.runTmuxRelaunch(reg, *old, agentRelaunchRequest{model: "sonnet", effort: "high", yes: true, json: true, socket: deleteSocketFlags{socketPath: socket}}, &output, &stderr)
					if err != nil {
						t.Fatalf("%v %s", err, stderr.String())
					}
					notices = append(notices, stderr.String())
				}
				waitSDKResumeBirth(t, state, uid)
				after := projectRegistry(t, state)
				a, _ := after.Agent(uid)
				if a.Status.SessionRef.ConversationID() != old.Status.SessionRef.ConversationID() || a.Status.PaneRef == oldPane || a.Status.Phase != metadata.PhaseRunning {
					t.Fatal("identity/conversation/pane parity lost")
				}
				layers := map[string]string{}
				for _, k := range []string{metadata.AnnotationAgentModel, metadata.AnnotationAgentModelSource, metadata.AnnotationAgentEffort, metadata.AnnotationAgentEffortSource} {
					layers[k] = a.Metadata.Annotations[k]
				}
				raw, _ := json.Marshal(layers)
				settings = append(settings, string(raw))
			}
			if strings.TrimSpace(notices[0]) != strings.TrimSpace(notices[1]) {
				t.Fatalf("notices differ CLI %q SDK %q", notices[0], notices[1])
			}
			if settings[0] != settings[1] {
				t.Fatalf("launch annotations differ CLI %s SDK %s", settings[0], settings[1])
			}
		})
	}
}

func TestRelaunchAgentSDKPreStopRefusalsLeaveNoTrace(t *testing.T) {
	relaunchProviderFence(t)
	f := newRelaunchFixture(t)
	root := t.TempDir()
	for i := range f.store.registry.Projects {
		f.store.registry.Projects[i].Spec.Root = root
	}
	for i := range f.store.registry.Panes {
		f.store.registry.Panes[i].Spec.CWD = root
	}
	for i := range f.store.registry.Agents {
		f.store.registry.Agents[i].Spec.Workspace.CWD = root
	}

	a, _ := f.store.registry.Agent(personaAttachAgent)
	a.Status.Interaction.ObservedAt = time.Now()
	state, _, _ := renameState(t, f.store.registry)
	c, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: filepath.Join(t.TempDir(), "config"), HomeDir: t.TempDir(), SocketPath: filepath.Join(t.TempDir(), "missing"), ProjmuxExecutable: "/absent/projmux"})
	if err != nil {
		t.Fatal(err)
	}
	before := projectRegistryBytes(t, state)
	_, err = c.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + personaAttachAgent, Model: "opus", Confirm: true})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateExecutableInvalid {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: "uid:" + personaAttachAgent, Model: "opus", Confirm: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if before != projectRegistryBytes(t, state) {
		t.Fatal("preStop failure changed Registry")
	}
	// A malformed explicit socket must not consult an inherited transport.
	t.Setenv("TMUX", "/poison/live,1,0")
	_, err = c.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + personaAttachAgent, SocketPath: "relative", DryRun: true})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}
}

// The same existing websocket/JSON-RPC fixture used by native SDK create and
// resume proves the relaunch boundary. It cannot contact a real provider.
func TestRelaunchAgentSDKNativeCodexAndPostStopCancellation(t *testing.T) {
	relaunchProviderFence(t)
	requireRealTmux(t)
	binary := sdkCreateBinary(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 required for native relaunch fixture")
	}
	ambient := os.Getenv("PATH")
	for _, lane := range []string{"cli", "sdk", "sdk-cancel-success", "sdk-cancel-failure"} {
		t.Run(lane, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "t6n-")
			if err != nil {
				t.Fatal(err)
			}
			retired := false
			t.Cleanup(func() {
				if retired {
					if err := os.RemoveAll(root); err != nil {
						t.Error(err)
					}
				} else {
					t.Logf("preserved unresolved native fixture root=%s; owner review required", root)
				}
			})
			home, state, config := filepath.Join(root, "home"), filepath.Join(root, "state", "projmux"), filepath.Join(root, "config", "projmux")
			providerRoot, bin := filepath.Join(root, "codex"), filepath.Join(home, ".local", "bin")
			t.Logf("native fixture root=%s", root)
			for _, d := range []string{home, bin, providerRoot} {
				if err := os.MkdirAll(d, 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := filepath.Join(root, "shared.py")
			fixture := strings.Replace(relaunchNativeLedgerFixture(t), "if (method=='thread/resume' and", "if (method=='thread/settings/update' and os.path.exists(os.path.join(os.environ['HOME'],'fail-settings'))) or (method=='thread/resume' and", 1)
			fixture = strings.Replace(fixture, "    if method=='thread/resume' and", "    if method=='thread/settings/update' and os.path.exists(os.path.join(os.environ['HOME'],'hold-settings')):\n     open(os.path.join(os.environ['HOME'],'settings-entered'),'w').write('entered')\n     while os.path.exists(os.path.join(os.environ['HOME'],'hold-settings')):time.sleep(.01)\n    if method=='thread/resume' and", 1)
			if err := os.WriteFile(script, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := "#!/bin/sh\ncase \"$1 $2\" in\n'app-server proxy' | 'app-server daemon') exec " + fmt.Sprintf("%q -u %q", python, script) + " \"$@\";;\nesac\nexec /bin/sleep 120\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			observerLog := filepath.Join(root, "observer-receipts.tsv")
			executable := filepath.Join(root, "projmux")
			relaunchObserverExecutable(t, executable, binary, observerLog)
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":"+ambient)
			t.Setenv("CODEX_HOME", providerRoot)
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			control := filepath.Join(providerRoot, "app-server-control", "app-server-control.sock")
			server := exec.Command(python, "-u", script, "serve", control)
			var serverErr bytes.Buffer
			server.Stderr = &serverErr
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = server.Process.Kill()
				waitErr := server.Wait()
				t.Logf("owned protocol server pid=%d Wait returned: %v", server.Process.Pid, waitErr)
				if t.Failed() {
					t.Log(serverErr.String())
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			waitCodexCreate(t, ctx, func() bool { _, e := os.Stat(control); return e == nil })
			socket, _ := sdkCreateSocket(t)
			t.Logf("native fixture socket=%s", socket)
			client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: executable})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateProject(ctx, core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
				t.Fatal(err)
			}
			w, err := client.CreateWindow(ctx, core.CreateWindowRequest{Project: "sample", Name: "native"})
			if err != nil {
				t.Fatal(err)
			}
			var uid string
			births := map[string]metadata.ProcessIdentity{}
			t.Cleanup(func() {
				if uid != "" && !cleanupRelaunchBinding(t, client, state, uid, w.Window.Metadata.UID, socket) {
					return
				}
				if !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, true) {
					return
				}
				cleanupSDKNativePeers(t, state)
				retired = true
			})
			created, err := client.CreateAgent(ctx, core.CreateAgentRequest{Project: "sample", Window: "native", Provider: "codex", Name: "worker", Payload: []string{"native-fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			uid = created.Agent.Metadata.UID
			if !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, false) {
				t.Fatal("initial observer birth capture uncertain")
			}
			waitSDKResumeBirth(t, state, uid)
			req := core.RelaunchAgentRequest{Ref: "uid:" + uid, Window: "uid:" + w.Window.Metadata.UID, Model: "relaunch-model", Effort: "high", Confirm: true}
			call := func(callCtx context.Context) (core.RelaunchAgentResult, error) {
				if lane != "cli" {
					out, err := client.RelaunchAgent(callCtx, req)
					if err == nil && !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, false) {
						return out, errors.New("observer birth capture uncertain")
					}
					return out, err
				}
				c := sdkRelaunchCLI(t, state, config, socket, executable, home)
				c.rebind.create.codexNative = newCodexNativeThreadController(state)
				c.rebind.create.resumes.(*aiCommand).nativeObserverPaths = &runtime.NativeObserverPaths{Paths: configpkg.Paths{StateDir: state, ConfigDir: config}, RegistryPath: store.PathFor(state), Executable: executable}
				reg := projectRegistry(t, state)
				a, _ := reg.Agent(uid)
				var output, stderr bytes.Buffer
				err := c.runTmuxRelaunch(reg, *a, agentRelaunchRequest{model: req.Model, effort: req.Effort, yes: true, json: true, socket: deleteSocketFlags{socketPath: socket}}, &output, &stderr)
				if err == nil && !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, false) {
					return core.RelaunchAgentResult{}, errors.New("observer birth capture uncertain")
				}
				return core.RelaunchAgentResult{}, err
			}
			// A settings capability refusal is before Stop, with no record or snapshot.
			if err := os.WriteFile(filepath.Join(home, "fail-settings"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			before := projectRegistryBytes(t, state)
			_, err = call(ctx)
			if err == nil || !strings.Contains(err.Error(), "relaunch-codex-settings-unsupported") || projectRegistryBytes(t, state) != before {
				t.Fatalf("preStop settings guard %v", err)
			}
			if err := os.Remove(filepath.Join(home, "fail-settings")); err != nil {
				t.Fatal(err)
			}
			if lane == "sdk" {
				t.Run("preStop-cancel", func(t *testing.T) {
					hold := filepath.Join(home, "hold-settings")
					entered := filepath.Join(home, "settings-entered")
					if err := os.WriteFile(hold, nil, 0600); err != nil {
						t.Fatal(err)
					}
					defer os.Remove(hold)
					before := projectRegistryBytes(t, state)
					callCtx, cancelCall := context.WithCancel(ctx)
					defer cancelCall()
					type response struct {
						out core.RelaunchAgentResult
						err error
					}
					done := make(chan response, 1)
					go func() { out, err := call(callCtx); done <- response{out, err} }()
					waitCodexCreate(t, ctx, func() bool { _, e := os.Stat(entered); return e == nil })
					cancelCall()
					select {
					case r := <-done:
						if !errors.Is(r.err, context.Canceled) || r.out.Stop.Committed || projectRegistryBytes(t, state) != before {
							t.Errorf("preStop cancellation lost cause or changed state: %+v %v", r.out, r.err)
						}
					case <-ctx.Done():
						t.Fatal("preStop cancellation did not return")
					}
					if err := os.Remove(hold); err != nil {
						t.Fatal(err)
					}
				})
			}
			reg := projectRegistry(t, state)
			old, _ := reg.Agent(uid)
			oldPane, _ := reg.Pane(old.Status.PaneRef)
			oldGeneration := oldPane.Status.Activation.Generation
			wirePath := filepath.Join(home, "shared-wire.jsonl")
			wireBefore, _ := os.ReadFile(wirePath)
			var result core.RelaunchAgentResult
			if strings.HasPrefix(lane, "sdk-cancel") {
				hold := filepath.Join(home, "hold-shared-init")
				entered := filepath.Join(home, "shared-init-entered")
				if err := os.WriteFile(hold, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if lane == "sdk-cancel-failure" {
					if err := os.WriteFile(filepath.Join(home, "fail-shared-init"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				callCtx, cancelCall := context.WithCancel(ctx)
				type response struct {
					out core.RelaunchAgentResult
					err error
				}
				done := make(chan response, 1)
				go func() { o, e := call(callCtx); done <- response{o, e} }()
				waitCodexCreate(t, ctx, func() bool { _, e := os.Stat(entered); return e == nil })
				// The native resume RPC begins only after the old Pane was stopped.
				during := projectRegistry(t, state)
				a, _ := during.Agent(uid)
				if a.Status.Phase != metadata.PhaseOffline {
					t.Fatalf("cancel boundary was before Stop: %s", a.Status.Phase)
				}
				cancelCall()
				if err := os.Remove(hold); err != nil {
					t.Fatal(err)
				}
				select {
				case r := <-done:
					result, err = r.out, r.err
				case <-ctx.Done():
					t.Fatal("postStop operation did not finish")
				}
			} else {
				result, err = call(ctx)
			}
			wireAfter, _ := os.ReadFile(wirePath)
			assertRelaunchNativeSequence(t, wireAfter[len(wireBefore):], old.Status.SessionRef.ConversationID(), req.Model, req.Effort, lane == "sdk-cancel-failure")
			for _, method := range []string{"thread/start", "turn/start"} {
				needle := []byte(`"method": "` + method + `"`)
				if bytes.Count(wireAfter, needle) != bytes.Count(wireBefore, needle) {
					t.Fatalf("relaunch started new thread or turn: %s", method)
				}
			}
			after := projectRegistry(t, state)
			a, _ := after.Agent(uid)
			if a.Status.SessionRef.ConversationID() != old.Status.SessionRef.ConversationID() {
				t.Fatal("conversation changed")
			}
			if lane == "sdk-cancel-failure" {
				var refused *projmux.Refusal
				if !errors.As(err, &refused) || refused.Code != projmux.CodeRelaunchExecutionFailed || !result.Stop.Committed || (result.Recovery == "" || !strings.Contains(result.Recovery, socket)) || a.Status.Phase != metadata.PhaseOffline || result.Outcome != "resume-failed" {
					t.Fatalf("lost partial failure %+v %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			waitSDKResumeBirth(t, state, uid)
			after = projectRegistry(t, state)
			a, _ = after.Agent(uid)
			newPane, _ := after.Pane(a.Status.PaneRef)
			if a.Status.PaneRef == old.Status.PaneRef || newPane.Status.Activation.Generation == oldGeneration || a.Status.Phase != metadata.PhaseRunning {
				t.Fatal("relaunch did not replace Pane/generation")
			}
			if lane != "cli" && (!result.Stop.Committed || result.Resume.Receipt.Effects.Identity != core.IdentityReused) {
				t.Fatalf("lost receipt %+v", result)
			}
			// Offline and Failed relaunches reuse this native path with no Stop.
			if lane == "sdk" {
				for _, phase := range []metadata.AgentPhase{metadata.PhaseOffline, metadata.PhaseFailed} {
					if _, err := client.Delete(ctx, core.DeleteRequest{Kind: core.KindPane, Ref: "uid:" + a.Status.PaneRef, Confirm: true}); err != nil {
						t.Fatal(err)
					}
					if phase == metadata.PhaseFailed {
						_, err := store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
							_, e := store.DefaultMutator().TransitionAgent(r, uid, phase, "fixture failure")
							return e
						})
						if err != nil {
							t.Fatal(err)
						}
					}
					result, err := call(ctx)
					if err != nil || result.Stop.Committed || result.Outcome != "resumed" {
						t.Fatalf("cold native %+v %v", result, err)
					}
					waitSDKResumeBirth(t, state, uid)
					r := projectRegistry(t, state)
					a, _ = r.Agent(uid)
				}
			}
		})
	}
}

// Keep the global provider guard active. This additional failing fence names
// which test-owned process accidentally fell past its protocol fixture.
func relaunchProviderFence(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "unexpected-provider.log")
	for _, name := range []string{"codex", "claude"} {
		body := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2\" in\n'app-server proxy') verb='app-server proxy';;\n'app-server daemon') case \"$3\" in version) verb='app-server daemon version';; *) verb='unknown daemon verb';; esac;;\n*) verb='unknown';;\nesac\nprintf '%%s|ppid=%%s|verb=%%s|parent=' %q \"$PPID\" \"$verb\" >> %q\nreadlink \"/proc/$PPID/exe\" >> %q\nexit 1\n", name, record, record)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		b, err := os.ReadFile(record)
		if err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		if len(b) > 0 {
			t.Errorf("test-owned fail-closed provider fence: %s", b)
		}
	})
}

func relaunchNativeLedgerFixture(t *testing.T) string {
	t.Helper()
	s := codexHostMoveServerFixture
	replacements := [][2]string{
		{" global model,effort,sandbox,approval,started\n try:", " global model,effort,sandbox,approval,started\n connection=str(time.monotonic_ns())\n try:"},
		{"f.write(json.dumps(frame)+'\\n')", "f.write(json.dumps(dict(id=frame.get('id'),method=method,params={k:v for k,v in p.items() if k in ('threadId','model','effort','sandboxPolicy','approvalPolicy')},_fixtureConnection=connection,_fixtureEvent='request',_fixtureTime=time.monotonic_ns()))+'\\n')"},
		{"   data=json.dumps({'id':frame['id'],'result':r}).encode();", "   with lock:\n    with open(os.path.join(os.environ['HOME'],'shared-wire.jsonl'),'a') as f:f.write(json.dumps(dict(id=frame['id'],result=r,_fixtureConnection=connection,_fixtureEvent='response',_fixtureTime=time.monotonic_ns()))+'\\n')\n   data=json.dumps({'id':frame['id'],'result':r}).encode();"},
		{"     data=json.dumps({'id':frame['id'],'error':", "     with open(os.path.join(os.environ['HOME'],'shared-wire.jsonl'),'a') as f:f.write(json.dumps(dict(id=frame['id'],error='injected refusal',_fixtureConnection=connection,_fixtureEvent='response',_fixtureTime=time.monotonic_ns()))+'\\n')\n     data=json.dumps({'id':frame['id'],'error':"},
	}
	for _, r := range replacements {
		if strings.Count(s, r[0]) != 1 {
			t.Fatalf("native ledger anchor count differs: %q", r[0])
		}
		s = strings.Replace(s, r[0], r[1], 1)
	}
	return s
}

func assertRelaunchNativeSequence(t *testing.T, wire []byte, thread, model, effort string, failed bool) {
	t.Helper()
	type frame struct {
		Method     string          `json:"method"`
		ID         json.RawMessage `json:"id"`
		Params     map[string]any  `json:"params"`
		Result     map[string]any  `json:"result"`
		Error      any             `json:"error"`
		Connection string          `json:"_fixtureConnection"`
		Event      string          `json:"_fixtureEvent"`
		Time       int64           `json:"_fixtureTime"`
	}
	var frames []frame
	connections := map[string]bool{}
	for line := range bytes.SplitSeq(wire, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
		if f.Event == "request" && f.Method == "thread/resume" {
			if f.Params["threadId"] != thread {
				t.Fatal("resume changed thread")
			}
			connections[f.Connection] = true
		}
	}
	if len(connections) != 1 {
		t.Fatalf("logical resume used %d connections", len(connections))
	}
	var connection string
	for c := range connections {
		connection = c
	}
	var requests []frame
	responses := map[string]frame{}
	for _, f := range frames {
		if f.Connection != connection {
			continue
		}
		if f.Event == "response" {
			responses[string(f.ID)] = f
		}
		if f.Event == "request" && (f.Method == "thread/resume" || f.Method == "thread/settings/update") {
			requests = append(requests, f)
		}
	}
	want := []string{"thread/resume", "thread/settings/update", "thread/resume"}
	if failed {
		want = want[:1]
	}
	if len(requests) != len(want) {
		t.Fatalf("initial/update/verify count=%d want=%d wire=%s", len(requests), len(want), wire)
	}
	ids := map[string]bool{}
	for i, q := range requests {
		if q.Method != want[i] || q.Params["threadId"] != thread || ids[string(q.ID)] {
			t.Fatalf("noncanonical same-thread request %+v", q)
		}
		ids[string(q.ID)] = true
		r, ok := responses[string(q.ID)]
		if !ok || r.Time < q.Time {
			t.Fatalf("missing/early response %+v", q)
		}
		if i+1 < len(requests) && r.Time >= requests[i+1].Time {
			t.Fatal("next request preceded the previous response")
		}
		if failed {
			if r.Error == nil {
				t.Fatal("missing native refusal")
			}
			return
		}
		if r.Error != nil {
			t.Fatalf("unexpected native error %+v", r)
		}
		if i == 1 {
			if q.Params["model"] != model || q.Params["effort"] != effort {
				t.Fatalf("settings update lost requested fields %+v", q.Params)
			}
		}
		if i == 0 {
			if r.Result["model"] == model && r.Result["reasoningEffort"] == effort {
				t.Fatal("fixture did not require the settings update")
			}
		}
		if i == 2 {
			if r.Result["model"] != model || r.Result["reasoningEffort"] != effort {
				t.Fatalf("verify response did not report applied settings %+v", r.Result)
			}
		}
	}
}

// This launcher records only the exact observation children it execs. The
// supervisor and every product route still run the real built binary unchanged.
func relaunchObserverExecutable(t *testing.T, path, binary, record string) {
	t.Helper()
	body := fmt.Sprintf(`#!/bin/sh
if [ "$1 $2 $3 $4" = 'internal agent-hook ingest codex-broker-watch' ]; then
 agent=''; pane=''; generation=''; state=''; pending=''
 for argument do
  case "$pending" in
   agent) agent="$argument";; pane) pane="$argument";; generation) generation="$argument";; state) state="$argument";;
  esac
  pending=''
  case "$argument" in
   --agent-uid) pending=agent;; --pane-uid) pending=pane;; --generation) pending=generation;; --state-dir) pending=state;;
  esac
 done
 created=$(date -u '+%%Y-%%m-%%dT%%H:%%M:%%SZ')
 printf '%%s|%%s|%%s|%%s|%%s|%%s\n' "$$" "$agent" "$pane" "$generation" "$state" "$created" >> %q
fi
exec %q "$@"
`, record, binary)
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Logf("observer wrapper path=%s sha256=%x record=%s; argv forwarded unchanged via exec", path, sha256.Sum256([]byte(body)), record)
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	t.Logf("observer actual binary=%s sha256=%x", binary, h.Sum(nil))
}

func cleanupRelaunchBinding(t *testing.T, c *projmux.Client, state, uid, window, socket string) bool {
	t.Helper()
	reg := projectRegistry(t, state)
	a, ok := reg.Agent(uid)
	if !ok || a.Metadata.OwnerRef == nil || a.Metadata.OwnerRef.UID != window {
		t.Error("fixture Agent ownership became uncertain")
		return false
	}
	if a.Status.Phase != metadata.PhaseRunning {
		return true
	}
	p, ok := reg.Pane(a.Status.PaneRef)
	if !ok || p.Metadata.OwnerRef == nil || p.Metadata.OwnerRef.Kind != metadata.KindAgent || p.Metadata.OwnerRef.UID != uid || p.Spec.Runtime.EffectiveKind() != metadata.RuntimeTmux || p.Status.Activation.RuntimeID == "" {
		t.Error("fixture Pane ownership/runtime became uncertain")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := core.DeleteRequest{Kind: core.KindPane, Ref: "uid:" + p.Metadata.UID, Window: "uid:" + window, SocketPath: socket, DryRun: true}
	preview, err := c.Delete(ctx, req)
	if err != nil || len(preview.Panes) != 1 || preview.Panes[0].PaneUID != p.Metadata.UID || (preview.Panes[0].AgentUID != "" && preview.Panes[0].AgentUID != uid) || preview.Panes[0].WindowUID != window || preview.Panes[0].PaneID != p.Status.Activation.RuntimeID {
		t.Errorf("owned Pane delete preview failed: %v", err)
		return false
	}

	if len(preview.Plan.Targets) != 1 || preview.Plan.Targets[0].UID != p.Metadata.UID || len(preview.Windows) != 0 || len(preview.RegistryOnly) != 0 || preview.Panes[0].EndsWindow || preview.Panes[0].EndsSession {
		t.Error("delete preview extends beyond owned leaf Pane")
		return false
	}
	owned := map[string]core.Kind{uid: core.KindAgent, p.Metadata.UID: core.KindPane, window: core.KindWindow}
	for _, target := range preview.Plan.Targets {
		if owned[target.UID] != target.Kind {
			t.Error("delete target is not in exact fixture set")
			return false
		}
		for _, descendant := range target.Descendants {
			if owned[descendant.UID] != descendant.Kind {
				t.Error("delete descendant escaped fixture set")
				return false
			}
		}
	}
	for _, affected := range preview.Plan.Affected {
		kind, ok := owned[affected.UID]
		if !ok || !strings.EqualFold(string(kind), affected.Kind) {
			t.Error("delete affected set escaped fixture set")
			return false
		}
	}
	req.DryRun = false
	req.Confirm = true
	result, err := c.Delete(ctx, req)
	if err != nil || !result.Committed {
		t.Errorf("owned Pane cleanup failed: %v", err)
		return false
	}
	return true
}

func observeRelaunchObserverReceipts(t *testing.T, record, binary, state string, births map[string]metadata.ProcessIdentity, wait bool) bool {
	t.Helper()
	info, statErr := os.Stat(record)
	if statErr != nil {
		t.Errorf("observer receipt file stat unavailable: %v", statErr)
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		t.Error("observer receipt file owner is uncertain")
		return false
	}
	t.Logf("observer receipt file=%s owner=%d modified=%s", record, owner.Uid, info.ModTime().UTC().Format(time.RFC3339Nano))
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Errorf("observer receipts unavailable: %v", err)
		return false
	}
	if len(raw) == 0 {
		t.Error("observer receipts empty")
		return false
	}
	var witnesses []metadata.ProcessIdentity
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 6 || fields[4] != state || fields[5] == "" || fields[1] == "" || fields[2] == "" || fields[3] == "" {
			t.Error("observer receipt identity differs from fixture")
			return false
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			t.Error("invalid observer PID receipt")
			return false
		}
		key := line
		if old, known := births[key]; known {
			witnesses = append(witnesses, old)
			continue
		}
		current, _, err := localipc.Process(pid)
		if errors.Is(err, localipc.ErrProcessAbsent) {
			t.Errorf("owned observer exited before birth observation pid=%d; preserve unresolved receipt", pid)
			return false
		}
		if err != nil || current.OwnerUID != uint32(os.Getuid()) {
			t.Errorf("observer birth unreadable pid=%d: %v", pid, err)
			return false
		}
		var executable string
		if goruntime.GOOS == "linux" {
			executable, err = os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
		} else {
			var out []byte
			out, err = exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
			executable = strings.TrimSpace(string(out))
		}
		if err != nil || executable != binary {
			t.Errorf("owned observer exe uncertain pid=%d: %v", pid, err)
			return false
		}

		var argv []string
		if goruntime.GOOS == "linux" {
			raw, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			if readErr != nil {
				t.Errorf("observer selected tuple unreadable pid=%d", pid)
				return false
			}
			for _, part := range bytes.Split(raw, []byte{0}) {
				if len(part) > 0 {
					argv = append(argv, string(part))
				}
			}
		} else {
			raw, readErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
			if readErr != nil {
				t.Errorf("observer selected tuple unreadable pid=%d", pid)
				return false
			}
			argv = strings.Fields(string(raw))
		}
		if len(argv) < 5 || strings.Join(argv[1:5], " ") != "internal agent-hook ingest codex-broker-watch" {
			t.Errorf("observer role mismatch pid=%d", pid)
			return false
		}
		selected := map[string]string{}
		for i := 5; i+1 < len(argv); i++ {
			if slices.Contains([]string{"--state-dir", "--agent-uid", "--pane-uid", "--generation"}, argv[i]) {
				selected[argv[i]] = argv[i+1]
			}
		}
		if selected["--state-dir"] != state || selected["--agent-uid"] != fields[1] || selected["--pane-uid"] != fields[2] || selected["--generation"] != fields[3] {
			t.Errorf("observer selected tuple mismatch pid=%d", pid)
			return false
		}
		again, _, err := localipc.Process(pid)
		if err != nil || again != current {
			t.Errorf("observer birth changed during selected read pid=%d", pid)
			return false
		}
		t.Logf("owned observer pid=%d birth=%s agent=%s pane=%s generation=%s", pid, current.Start, fields[1], fields[2], fields[3])
		births[key] = current
		witnesses = append(witnesses, current)
	}
	if !wait {
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(witnesses) > 0 {
		remaining := witnesses[:0]
		for _, old := range witnesses {
			now, _, err := localipc.Process(old.PID)
			if errors.Is(err, localipc.ErrProcessAbsent) {
				continue
			}
			if err != nil {
				t.Errorf("observer exit unreadable pid=%d: %v", old.PID, err)
				return false
			}
			if now == old {
				remaining = append(remaining, old)
			}
		}
		witnesses = remaining
		if len(witnesses) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			t.Errorf("owned observers did not retire naturally: %+v", witnesses)
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func TestRelaunchAgentSDKCLISettingsParity(t *testing.T) {
	relaunchProviderFence(t)
	for _, name := range []string{"plain", "switch-profile", "same-profile-reset", "no-profile", "attach-instructions", "no-instructions"} {
		t.Run(name, func(t *testing.T) {
			home, root := sdkAgentHome(t), t.TempDir()
			state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
				t.Fatal(err)
			}
			instructions, err := client.WriteInstructions(context.Background(), "base", []byte("base instructions"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.WriteInstructions(context.Background(), "other", []byte("other instructions")); err != nil {
				t.Fatal(err)
			}
			profile, err := client.WriteProfile(context.Background(), "base", []byte("provider = \"claude\"\ninstructions = \"base\"\nmodel = \"sonnet\"\neffort = \"high\"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.WriteProfile(context.Background(), "other", []byte("provider = \"claude\"\ninstructions = \"other\"\nmodel = \"opus\"\neffort = \"low\"\n")); err != nil {
				t.Fatal(err)
			}
			reg := projectRegistry(t, state)
			uid := seedSDKResumeAgent(t, state, reg.Windows[0].Metadata.UID, "claude", root, metadata.PhaseOffline)
			_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
				m := store.DefaultMutator()
				if _, err := m.SetAgentProfile(r, uid, "base", profile.Digest, metadata.SettingSourceRelaunch); err != nil {
					return err
				}
				if _, err := m.SetAgentModel(r, uid, "haiku", metadata.SettingSourceRelaunch); err != nil {
					return err
				}
				if _, err := m.SetAgentEffort(r, uid, "max", metadata.SettingSourceRelaunch); err != nil {
					return err
				}
				_, err := m.SetAgentPersona(r, uid, metadata.AgentPersonaAnnotations{Persona: "base", PersonaDigest: instructions.Digest, InstructionsSource: metadata.SettingSourceProfile})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			none, base, other := "", "base", "other"
			q := core.RelaunchAgentRequest{Ref: "uid:" + uid, DryRun: true}
			switch name {
			case "switch-profile":
				q.Profile = &other
				q.Model = "sonnet"
			case "same-profile-reset":
				q.Profile = &base
				q.Reset = []string{"model", "effort"}
			case "no-profile":
				q.Profile = &none
			case "attach-instructions":
				q.Instructions = &other
			case "no-instructions":
				q.Instructions = &none
			}
			before := projectRegistryBytes(t, state)
			got, err := client.RelaunchAgent(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			c := sdkResumeCLI(t, state, config, "", "", home)
			reg = projectRegistry(t, state)
			a, _ := reg.Agent(uid)
			var output, stderr bytes.Buffer
			err = c.runTmuxRelaunch(reg, *a, agentRelaunchRequest{profile: q.Profile, instructions: q.Instructions, model: q.Model, reset: q.Reset, dryRun: true, json: true}, &output, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			var cli agentRelaunchResult
			if err := json.Unmarshal(output.Bytes(), &cli); err != nil {
				t.Fatal(err)
			}
			var current, next core.RelaunchAgentSettings
			raw, _ := json.Marshal(cli.CurrentSettings)
			if err := json.Unmarshal(raw, &current); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(cli.NewSettings)
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.CurrentSettings, current) || !reflect.DeepEqual(got.NewSettings, next) || !reflect.DeepEqual(got.RelaunchReasons, cli.RelaunchReasons) || got.Outcome != cli.Outcome {
				t.Fatalf("layer plan differs SDK %+v CLI %+v", got, cli)
			}
			if before != projectRegistryBytes(t, state) {
				t.Fatal("dry-run changed Registry")
			}
			snapshots, err := filepath.Glob(filepath.Join(state, "personas", "*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshots) > 0 {
				t.Fatal("dry-run materialized an instructions snapshot")
			}
		})
	}
}
