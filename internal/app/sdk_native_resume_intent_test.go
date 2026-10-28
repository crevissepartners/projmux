package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configpkg "github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	storepkg "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestSDKNativeCatalogResumeMatchesCLI holds the public Resume of a Codex
// app-server conversation to the CLI picker's native catalog create, on one
// existing endpoint fixture each: the thread is resumed (never started), the
// new Agent records it on the resolved endpoint with a current lifecycle, its
// activation is bound to the thread, its Pane is a native Codex Pane and one
// session history row is written. No real provider runs.
func TestSDKNativeCatalogResumeMatchesCLI(t *testing.T) {
	requireRealTmux(t)
	binary := sdkCreateBinary(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for protocol fixture")
	}
	ambientPath := os.Getenv("PATH")
	outcomes := map[string]string{}
	for _, lane := range []string{"cli", "sdk"} {
		sdk := lane == "sdk"
		t.Run(lane, func(t *testing.T) {
			// Provider and broker Unix sockets must fit even under a long CI TMPDIR.
			root, err := os.MkdirTemp("/tmp", "pmxr")
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
					t.Logf("preserved unresolved native fixture root=%s; owner review required", root)
				}
			})
			home, state, config := filepath.Join(root, "home"), filepath.Join(root, "state", "projmux"), filepath.Join(root, "config", "projmux")
			providerRoot := filepath.Join(root, "codex")
			bin := filepath.Join(home, ".local", "bin")
			observerLog := filepath.Join(root, "observer-receipts.tsv")
			executable := filepath.Join(root, "projmux")
			relaunchObserverExecutable(t, executable, binary, observerLog)
			for _, dir := range []string{home, bin, providerRoot} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := filepath.Join(root, "shared.py")
			if err := os.WriteFile(script, []byte(codexHostMoveServerFixture), 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := "#!/bin/sh\ncase \"$1 $2\" in\n'app-server proxy' | 'app-server daemon') exec " + fmt.Sprintf("%q -u %q", python, script) + " \"$@\";;\nesac\nexec /bin/sleep 120\n"
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
				waitErr := server.Wait()
				protocolWaited = true
				t.Logf("owned protocol server pid=%d Wait returned: %v", server.Process.Pid, waitErr)
				if t.Failed() {
					t.Log(stderr.String())
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(control); return err == nil })
			socket, run := sdkCreateSocket(t)
			client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: executable})
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
			// The fixture's catalog lists its threads in the provider HOME.
			candidates, err := client.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project: "uid:" + project.Project.Metadata.UID, Provider: "codex", Cwd: home})
			if err != nil {
				t.Fatal(err)
			}
			var picked *core.ResumeCandidate
			for i := range candidates {
				if candidates[i].Conversation == "process-thread" && candidates[i].Source == aisessions.SourceCodexAppServer {
					picked = &candidates[i]
				}
			}
			if picked == nil {
				t.Fatalf("catalog candidates %+v", candidates)
			}
			var ownedUID, ownedWindow string
			births := map[string]metadata.ProcessIdentity{}
			t.Cleanup(func() {
				if ownedUID != "" && sdk {
					if _, err := client.Delete(context.Background(), core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + ownedWindow, SocketPath: socket, Confirm: true}); err != nil {
						t.Errorf("resumed Window cleanup: %v", err)
						return
					}
				} else if ownedUID != "" && !cleanupRelaunchBinding(t, client, state, ownedUID, ownedWindow, socket) {
					return
				}
				if ownedUID != "" && !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, true) {
					return
				}
				cleanupSDKNativePeers(t, state)
				retired = true
			})

			if sdk {
				selection := &core.ResumeSelection{Provider: picked.Provider, Conversation: picked.Conversation, Source: picked.Source, Cwd: picked.Cwd}
				intent := core.WindowCreateIntent{Project: "uid:" + project.Project.Metadata.UID, Name: "resumed", Agent: &core.WindowIntentAgent{Resume: selection}}
				if preview, err := client.PreflightWindowIntent(ctx, intent); err != nil || preview.Provider != "codex" {
					t.Fatalf("preview %+v %v", preview, err)
				}
				result, err := client.CreateWindowFromIntent(ctx, intent)
				if err != nil {
					t.Fatal(err)
				}
				ownedWindow = result.Window.Metadata.UID
			} else {
				c := sdkAgentCLI(t, state, config, socket, executable, home)
				c.codexNative = newCodexNativeThreadController(state)
				c.resumes.(*aiCommand).nativeObserverPaths = &mutationruntime.NativeObserverPaths{Paths: configpkg.Paths{StateDir: state, ConfigDir: config}, RegistryPath: storepkg.PathFor(state), Executable: executable}
				route, err := c.codexNative.Current(ctx)
				if err != nil {
					t.Fatal(err)
				}
				shell, ok := projectRegistry(t, state).WindowDefaultShell(window.Window.Metadata.UID)
				if !ok {
					t.Fatal("fixture Window has no shell")
				}
				if _, err := c.createFromIntent(agentPaneIntent{
					producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
					conversationID: picked.Conversation, resumeSource: picked.Source, anchorPaneID: shell.Status.Activation.RuntimeID,
					resumeEndpoint: route.Endpoint, resumeGenerationState: route.State,
				}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
				ownedWindow = window.Window.Metadata.UID
			}

			reg := projectRegistry(t, state)
			var created metadata.Agent
			for _, a := range reg.Agents {
				if a.Status.SessionRef != nil && a.Status.SessionRef.Codex != nil && a.Status.SessionRef.Codex.ThreadID == "process-thread" {
					created = a
				}
			}
			ownedUID = created.Metadata.UID
			ref := created.Status.SessionRef
			if ownedUID == "" || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() || ref.Codex.Lifecycle == nil {
				t.Fatalf("resumed Agent %+v", created)
			}
			if !observeRelaunchObserverReceipts(t, observerLog, binary, state, births, false) {
				t.Fatal("observer birth capture uncertain")
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
			if err != nil {
				t.Fatal(err)
			}
			wire, err := os.ReadFile(filepath.Join(home, "shared-wire.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			starts, resumes := 0, []string{}
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
					starts++
				case "thread/resume":
					resumes = append(resumes, fmt.Sprint(frame.Params["threadId"]))
				}
			}
			outcomes[lane] = fmt.Sprintf("provider=%s endpoint-default-domain=%t lifecycle=%s activation=%s history=%d starts=%d resumes=%s",
				created.Spec.Provider, ref.Codex.Endpoint.StateDomainID != "", ref.Codex.Lifecycle.State, created.Status.Activation.State,
				len(rows.Sessions), starts, strings.Join(resumes, ","))
		})
	}
	want := "provider=codex endpoint-default-domain=true lifecycle=" + string(metadata.CodexGenerationCurrent) + " activation=" + string(metadata.ActivationAcknowledged) + " history=1 starts=0 resumes=process-thread"
	if outcomes["cli"] != want || outcomes["sdk"] != want {
		t.Fatalf("native catalog resume\ncli %s\nsdk %s\nwant %s", outcomes["cli"], outcomes["sdk"], want)
	}
}
