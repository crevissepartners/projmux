package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func seedSDKResumeAgent(t *testing.T, state, window, provider, root string, phase metadata.AgentPhase) string {
	t.Helper()
	var uid string
	_, err := store.NewStore(store.PathFor(state)).Update(func(reg *metadata.Registry) error {
		mut := store.DefaultMutator()
		agent, err := mut.CreateAgent(reg, window, metadata.CreateAgentOptions{Name: "worker", Provider: provider, Workspace: metadata.AgentWorkspace{CWD: root}})
		if err != nil {
			return err
		}
		uid = agent.Metadata.UID
		if _, err := mut.AttachAgentPane(reg, uid, metadata.BootstrapPane{CWD: root, Name: "worker-pane"}, "old-resume-fixture"); err != nil {
			return err
		}
		if _, err := mut.ReleaseAgentPane(reg, uid, metadata.AgentExitNormal, ""); err != nil {
			return err
		}
		if _, _, err := mut.RecordAgentSessionRef(reg, uid, metadata.AgentSessionObservation{Provider: provider, SessionID: "11111111-2222-4333-8444-555555555555", ThreadID: "11111111-2222-4333-8444-555555555555"}); err != nil {
			return err
		}
		_, err = mut.TransitionAgent(reg, uid, phase, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return uid
}
func sdkResumeCLI(t *testing.T, state, config, socket, binary, home string) *agentCommand {
	t.Helper()
	create := sdkAgentCLI(t, state, config, socket, binary, home)
	return &agentCommand{loadRegistry: create.store.load, store: create.store, resolveWorkspace: resolveAgentWorkspaceFor, rebind: newAgentRebinder(create, create.resumes)}
}
func waitSDKResumeBirth(t *testing.T, state, uid string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reg := projectRegistry(t, state)
		a, _ := reg.Agent(uid)
		p, ok := reg.Pane(a.Status.PaneRef)
		if ok && p.Status.Activation.RuntimeID != "" {
			if a.Spec.Provider != "claude" || p.Status.Activation.Claude != nil && p.Status.Activation.Claude.Process.PID > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("resume birth not recorded: %s", projectRegistryBytes(t, state))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestResumeAgentSDKCLIParity(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	for _, tc := range []struct {
		provider      string
		phase         metadata.AgentPhase
		model, effort string
		shared        bool
	}{
		{provider: "claude", phase: metadata.PhaseOffline},
		{provider: "antigravity", phase: metadata.PhaseOffline},
		{provider: "claude", phase: metadata.PhaseFailed, model: "sonnet", effort: "high"},
		{provider: "claude", phase: metadata.PhaseOffline, shared: true},
	} {
		t.Run(tc.provider+string(tc.phase)+tc.model+strconv.FormatBool(tc.shared), func(t *testing.T) {
			var snapshots, receipts, runtimes, disclosures []string
			for _, sdk := range []bool{false, true} {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, run := sdkCreateSocket(t)
				run("set-option", "-w", "-t", "control:0", "automatic-rename", "off")
				run("rename-window", "-t", "control:0", "control")
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				window, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"})
				if err != nil {
					t.Fatal(err)
				}
				uid := seedSDKResumeAgent(t, state, window.Window.Metadata.UID, tc.provider, root, tc.phase)
				if tc.shared {
					_, err := store.NewStore(store.PathFor(state)).Update(func(reg *metadata.Registry) error {
						mut := store.DefaultMutator()
						other, err := mut.CreateAgent(reg, window.Window.Metadata.UID, metadata.CreateAgentOptions{Name: "other", Provider: tc.provider, Workspace: metadata.AgentWorkspace{CWD: root}})
						if err != nil {
							return err
						}
						_, _, err = mut.RecordAgentSessionRef(reg, other.Metadata.UID, metadata.AgentSessionObservation{Provider: tc.provider, SessionID: "11111111-2222-4333-8444-555555555555"})
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				var receipt, notices string
				if sdk {
					// Captured Options route supervisor writes even when ambient roots differ.
					t.Setenv("XDG_STATE_HOME", t.TempDir())
					t.Setenv("XDG_CONFIG_HOME", t.TempDir())
					result, err := client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid, Project: "sample", Window: "extra", Model: tc.model, Effort: tc.effort})
					if err != nil {
						t.Fatal(err)
					}
					if result.Agent.Metadata.UID != uid || result.Agent.Status.PaneRef == "" || result.PaneID == "" || result.Receipt.Effects.Identity != core.IdentityReused {
						t.Fatalf("missing rebind result: %+v", result)
					}
					receipt = sdkJSON(t, result.Receipt)
					notices = strings.Join(result.Notices, "\n")
					if len(result.Notices) > 0 {
						notices += "\n"
					}
				} else {
					c := sdkResumeCLI(t, state, config, socket, binary, home)
					var out, stderr bytes.Buffer
					args := []string{"uid:" + uid, "--project", "sample", "--window", "extra"}
					if tc.model != "" {
						args = append(args, "--model", tc.model, "--effort", tc.effort)
					}
					if err := c.runResume(args, &out, &stderr); err != nil {
						t.Fatalf("CLI: %v %s", err, &stderr)
					}
					if out.String() != "agent/worker resumed\n" {
						t.Fatal(out.String())
					}
					notices = stderr.String()
					reg := projectRegistry(t, state)
					a, _ := reg.Agent(uid)
					receipt = sdkJSON(t, mutationruntime.ResumeReceipt(reg, uid, a.Status.PaneRef))
				}
				if tc.shared && !strings.Contains(notices, "also recorded on agent/other") {
					t.Fatalf("sdk=%v lost shared conversation disclosure: %q", sdk, notices)
				}
				waitSDKResumeBirth(t, state, uid)
				reg := projectRegistry(t, state)
				disclosures = append(disclosures, normalizeAgentCreateJSON(t, notices, reg, socket))
				snapshots = append(snapshots, normalizeAgentCreateJSON(t, projectRegistryBytes(t, state), reg, socket))
				receipts = append(receipts, normalizeAgentCreateJSON(t, receipt, reg, socket))
				runtimes = append(runtimes, normalizeAgentCreateJSON(t, run("list-panes", "-a", "-F", "#{window_name}|#{pane_current_path}|#{@projmux_pane_uid}|#{@projmux_ai_agent}|#{@projmux_ai_resume_id}"), reg, socket))
			}
			for i, pair := range [][]string{snapshots, receipts, runtimes, disclosures} {
				if pair[0] != pair[1] {
					t.Fatalf("pair %d mismatch\nCLI %s\nSDK %s", i, pair[0], pair[1])
				}
			}
		})
	}
}
func TestResumeAgentSDKCLIRollback(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			for _, sdk := range []bool{false, true} {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, run := sdkCreateSocket(t)
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				w, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"})
				if err != nil {
					t.Fatal(err)
				}
				uid := seedSDKResumeAgent(t, state, w.Window.Metadata.UID, "claude", root, metadata.PhaseOffline)
				before := projectRegistryBytes(t, state)
				panes := run("list-panes", "-a", "-F", "#{pane_id}")
				shim := t.TempDir()
				failure := "exit 12"
				if partial {
					failure = strconv.Quote(realTmux) + " \"$@\"; exit 12"
				}
				script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = split-window ]; then " + failure + "; fi\ndone\nexec " + strconv.Quote(realTmux) + " \"$@\"\n"
				if err := os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
				if sdk {
					_, err = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid})
					var refusal *projmux.Refusal
					if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResumeExecutionFailed {
						t.Fatalf("SDK refusal: %v", err)
					}
				} else {
					c := sdkResumeCLI(t, state, config, socket, binary, home)
					var out, stderr bytes.Buffer
					err = c.runResume([]string{"uid:" + uid}, &out, &stderr)
					if err == nil {
						t.Fatal("CLI accepted failed split")
					}
				}
				if projectRegistryBytes(t, state) != before || run("list-panes", "-a", "-F", "#{pane_id}") != panes {
					t.Fatal("failed resume left metadata or tmux effects")
				}
			}
		})
	}
}

func TestResumeAgentSDKRefusalsBeforeTransport(t *testing.T) {
	home := sdkAgentHome(t)
	state := filepath.Join(t.TempDir(), "projmux")
	config := filepath.Join(t.TempDir(), "projmux")
	root := t.TempDir()
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	reg := projectRegistry(t, state)
	window := reg.Windows[0].Metadata.UID
	uids := map[string]string{}
	for _, p := range []string{"claude", "codex"} {
		uid := seedSDKResumeAgent(t, state, window, p, root, metadata.PhaseOffline)
		uids[p] = uid
		if p == "claude" { // release the shared name for second fixture
			_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
				_, e := store.DefaultMutator().RenameAgent(r, uid, "claude-worker")
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	before := projectRegistryBytes(t, state)
	for _, tc := range []struct {
		req  core.ResumeAgentRequest
		code projmux.Code
	}{{core.ResumeAgentRequest{Ref: "absent"}, projmux.CodeResourceNotFound}, {core.ResumeAgentRequest{Ref: "uid:" + uids["codex"]}, projmux.CodeResumeNativeCodexUnavailable}, {core.ResumeAgentRequest{Ref: "uid:"}, projmux.CodeSelectorInvalid}, {core.ResumeAgentRequest{Ref: "uid:" + uids["claude"], Model: "bad\nmodel"}, projmux.CodeResumeRequestInvalid}} {
		_, err := client.ResumeAgent(context.Background(), tc.req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code {
			t.Fatalf("%+v: %v want %s", tc.req, err, tc.code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:" + uids["claude"]}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if before != projectRegistryBytes(t, state) {
		t.Fatal("refusals changed registry")
	}

}

func TestResumeAgentSDKProcessAndRunningRefusals(t *testing.T) {
	for _, scenario := range []string{"retired-process", "running-tmux"} {
		t.Run(scenario, func(t *testing.T) {
			home := sdkAgentHome(t)
			state := filepath.Join(t.TempDir(), "projmux")
			reg := processResumeQueryFixture(t)
			agent := reg.Agents[0]
			if scenario == "running-tmux" {
				for i := range reg.Panes {
					reg.Panes[i].Spec.Runtime.Kind = metadata.RuntimeTmux
					reg.Panes[i].Status.ProcessSession = nil
				}
				a, _ := reg.Agent(agent.Metadata.UID)
				a.Status.Phase = metadata.PhaseRunning
			}
			if _, err := store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error { *r = reg; return nil }); err != nil {
				t.Fatal(err)
			}
			before := projectRegistryBytes(t, state)
			marker := filepath.Join(t.TempDir(), "called")
			shim := t.TempDir()
			if err := os.WriteFile(filepath.Join(shim, "tmux"), []byte("#!/bin/sh\nprintf invoked > "+strconv.Quote(marker)+"\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: t.TempDir(), HomeDir: home})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + agent.Metadata.UID})
			var refusal *projmux.Refusal
			want := projmux.CodeResumeRequestInvalid
			if scenario == "running-tmux" {
				want = projmux.CodeResumePhaseNotResumable
			}
			if !errors.As(err, &refusal) || refusal.Code != want {
				t.Fatalf("%s: %v want %s", scenario, err, want)
			}
			if before != projectRegistryBytes(t, state) {
				t.Fatal("refusal wrote registry")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("refusal invoked tmux", err)
			}
		})
	}
}

func TestResumeAgentCLIPlanningFailureIsWrappedOnce(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	ref, ok := metadata.NewAgentSessionRef(metadata.AgentSessionObservation{Provider: aiModeClaude, SessionID: personaResumeConversation}, resourceFixtureClock)
	if !ok {
		t.Fatal("invalid fixture conversation")
	}
	target.Status.SessionRef = ref
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
	failure := errors.New("selected runner is not installed: claude")
	launcher.planErr = failure
	_, _, err := runRoute(t, command, "resume", "uid:"+target.Metadata.UID)
	want := "agent resume: agent/" + target.Metadata.Name + " cannot resume claude conversation " + personaResumeConversation + ": " + failure.Error()
	if err == nil || err.Error() != want || !errors.Is(err, failure) {
		t.Fatalf("planning failure = %v, want %q with original cause", err, want)
	}
}

func TestSDKProcessResumeProfileFailureCodeThroughSettingsAdapter(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			command := &aiCommand{processCreateContext: &mutationruntime.ProcessCreateContext{Paths: config.Paths{ConfigDir: root + "/config", StateDir: root + "/state"}}}
			annotations := map[string]string{metadata.AnnotationAgentProfile: "removed-profile"}
			_, shared := mutationruntime.ResolveAgentSettingsWithPaths(command.processCreatePaths, provider, annotations, mutationruntime.AgentSettingsRequest{})
			_, err := command.ResolveAgentSettingsRequest(provider, annotations, agentSettingsRequest{})
			var local *profileResumeError
			if shared == nil || !errors.As(err, &local) || err.Error() != shared.Error() {
				t.Fatalf("settings adapter lost profile refusal or changed CLI diagnostic: shared=%v local=%T %v", shared, err, err)
			}
			sentinel := errors.New("transport context")
			joined := errors.Join(sentinel, err)
			if !errors.Is(joined, sentinel) || processResumeErrorCode(joined) != "profile-resume-unavailable" {
				t.Fatalf("joined profile refusal classified as %q: %v", processResumeErrorCode(joined), joined)
			}
		})
	}
}
