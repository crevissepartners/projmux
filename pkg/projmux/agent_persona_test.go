package projmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAgentPersonaSDKFailureStateAndPartialResult(t *testing.T) {
	for _, mode := range []string{"alive", "error", "empty", "closed"} {
		t.Run(mode, func(t *testing.T) {
			c, before, p, runner := relaunchFailureFixture(t)
			runner.mode = mode
			loaded, err := persona.NewDefaultStore(c.paths).Write("reviewer", []byte("Review carefully.\n"))
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+p.Target.Metadata.UID, "", "", runner)
			var refusal *Refusal
			wantCode := CodeDeleteExecutionFailed
			if mode == "closed" {
				wantCode = CodePersonaUnavailable
			}
			if !errors.As(err, &refusal) || refusal.Code != wantCode {
				t.Fatalf("result %+v error %v", out, err)
			}
			if mode != "closed" && !errors.Is(err, runner.stopCause) {
				t.Fatalf("lost stop cause: %v", err)
			}
			if out.AgentUID != p.Target.Metadata.UID || out.NewPersonaDigest != loaded.Digest || out.NewPaneUID != "" {
				t.Fatalf("lost partial plan %+v", out)
			}
			after, err := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := after.Agent(p.Target.Metadata.UID)
			old, _ := before.Agent(p.Target.Metadata.UID)
			if mode == "closed" {
				if a.Metadata.Annotations[metadata.AnnotationAgentPersonaDigest] != loaded.Digest || out.Recovery == "" || len(out.Notices) < 2 {
					t.Fatalf("closed state %+v result %+v", a, out)
				}
			} else if !reflect.DeepEqual(metadata.PersonaAnnotationsOf(*a), metadata.PersonaAnnotationsOf(*old)) {
				t.Fatal("failed stop did not restore persona")
			}
			if (mode == "error" || mode == "empty") && out.Recovery == "" {
				t.Fatal("unknown stop has no rerun command")
			}
			if runner.stops != 1 || runner.resumes != 0 {
				t.Fatalf("replayed operation %+v", runner)
			}
		})
	}
}
func TestAgentPersonaSDKAdmission(t *testing.T) {
	c, _, p, runner := relaunchFailureFixture(t)
	for _, tc := range []struct {
		ref, name string
		code      Code
	}{
		{"", "reviewer", CodeSelectorInvalid}, {"uid:", "reviewer", CodeSelectorInvalid}, {"uid:missing", "reviewer", CodeResourceNotFound},
		{"uid:" + p.Target.Metadata.UID, "../bad", CodePersonaNameInvalid}, {"uid:" + p.Target.Metadata.UID, "missing", CodePersonaNotFound},
	} {
		_, err := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: tc.name, DryRun: true}, tc.ref, "", "", runner)
		var r *Refusal
		if !errors.As(err, &r) || r.Code != tc.code {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DetachAgentPersona(ctx, core.DetachAgentPersonaRequest{Ref: "uid:" + p.Target.Metadata.UID}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if runner.calls != 0 {
		t.Fatal("refusal invoked runtime")
	}
}

func TestAgentPersonaSDKBusyAndSnapshotFreeDryRun(t *testing.T) {
	c, _, p, runner := relaunchFailureFixture(t)
	storage := persona.NewDefaultStore(c.paths)
	loaded, e := storage.Write("reviewer", []byte("Review carefully.\n"))
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(store.PathFor(c.paths.StateDir))
	if e != nil {
		t.Fatal(e)
	}
	for _, dry := range []bool{true, false} {
		result, e := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", DryRun: dry}, "uid:"+p.Target.Metadata.UID, "", "", runner)
		if dry {
			if e != nil || result.Outcome != "would-restart" || !result.ConfirmationRequired {
				t.Fatalf("%+v %v", result, e)
			}
		} else {
			var r *Refusal
			if !errors.As(e, &r) || r.Code != CodePersonaAgentBusy {
				t.Fatal(e)
			}
		}
	}
	after, e := os.ReadFile(store.PathFor(c.paths.StateDir))
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) || runner.calls != 0 {
		t.Fatal("dry run or refusal caused effects")
	}
	path, e := storage.SnapshotPath(loaded.Digest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("snapshot written before confirmation")
	}
}

func TestAgentPersonaSDKResumeFailureRetainsAttachAndRecovery(t *testing.T) {
	c, _, p, runner := relaunchFailureFixture(t)
	// A successful exact Stop commits Offline. The fixture then refuses every
	// resume mutation, so the real transaction must retain the attached persona.
	runner.stopCause = nil
	runner.mode = "closed"
	runner.resumeReadFailure = true
	if _, e := persona.NewDefaultStore(c.paths).Write("reviewer", []byte("Review carefully.\n")); e != nil {
		t.Fatal(e)
	}
	result, e := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+p.Target.Metadata.UID, "", "", runner)
	if e == nil || result.Recovery == "" || result.NewPaneUID != "" || result.NewPersona != "reviewer" {
		t.Fatalf("%+v %v", result, e)
	}
	reg, loadErr := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	a, _ := reg.Agent(p.Target.Metadata.UID)
	if a.Status.Phase != metadata.PhaseOffline || a.Metadata.Annotations[metadata.AnnotationAgentInstructionsSource] != metadata.SettingSourceAttach || a.Metadata.Annotations[metadata.AnnotationAgentPersona] != "reviewer" {
		t.Fatalf("%+v", a)
	}
	if runner.stops != 1 {
		t.Fatal("stop replayed")
	}
	if !errors.Is(e, runner.observeCause) || !strings.HasPrefix(result.Recovery, "projmux agent resume uid:"+p.Target.Metadata.UID) || !strings.Contains(result.Recovery, " --socket-path "+runner.socket) {
		t.Fatalf("Offline recovery lost the cause or exact route: %+v %v", result, e)
	}
}

func TestAgentPersonaSDKProviderConversationAndFileRefusals(t *testing.T) {
	for _, mode := range []string{"codex", "antigravity", "no-conversation", "too-large"} {
		t.Run(mode, func(t *testing.T) {
			c, _, p, runner := relaunchFailureFixture(t)
			storage := persona.NewDefaultStore(c.paths)
			if _, e := storage.Write("reviewer", []byte("review")); e != nil {
				t.Fatal(e)
			}
			code := CodePersonaNoConversation
			if mode == "too-large" {
				code = CodePersonaTooLarge
				if e := os.WriteFile(filepath.Join(c.paths.ConfigDir, persona.DirName, "reviewer.md"), []byte(strings.Repeat("x", persona.MaxSize+1)), 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *metadata.Registry) error {
					a, _ := reg.Agent(p.Target.Metadata.UID)
					switch mode {
					case "codex":
						a.Spec.Provider = "codex"
						a.Status.SessionRef = &metadata.AgentSessionRef{Provider: "codex", Codex: &metadata.CodexSessionRef{ThreadID: "thread"}}
						code = Code(runtime.PersonaReasonCodexInstructionsImmutable)
					case "antigravity":
						a.Spec.Provider = "antigravity"
						a.Status.SessionRef = &metadata.AgentSessionRef{Provider: "antigravity", Antigravity: &metadata.AntigravitySessionRef{ConversationID: "conversation"}}
						code = CodePersonaProviderUnsupported
					case "no-conversation":
						a.Status.SessionRef = nil
					}
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadFile(store.PathFor(c.paths.StateDir))
			if e != nil {
				t.Fatal(e)
			}
			_, e = c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+p.Target.Metadata.UID, "", "", runner)
			var refusal *Refusal
			if !errors.As(e, &refusal) || refusal.Code != code {
				t.Fatalf("want %s got %v", code, e)
			}
			after, e := os.ReadFile(store.PathFor(c.paths.StateDir))
			if e != nil {
				t.Fatal(e)
			}
			if string(before) != string(after) || runner.calls != 0 {
				t.Fatal("refusal caused effects")
			}
		})
	}
	c, _, uid, _ := sdkStopFixture(t)
	_, e := c.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:" + uid, Persona: "reviewer", DryRun: true})
	var refusal *Refusal
	if !errors.As(e, &refusal) || refusal.Code != CodeProcessRelaunchUnsupported {
		t.Fatal(e)
	}
}

// TestAgentPersonaSDKClosedStopRecoveryDescribesRunning pins the SDK boundary
// where the Pane is gone but a failed Stop did not commit the Registry update.
func TestAgentPersonaSDKClosedStopRecoveryDescribesRunning(t *testing.T) {
	c, _, p, runner := relaunchFailureFixture(t)
	runner.mode = "closed"
	loaded, err := persona.NewDefaultStore(c.paths).Write("reviewer", []byte("Review carefully.\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+p.Target.Metadata.UID, "", "", runner)
	if err == nil {
		t.Fatal("failed Stop followed by Running resume admission unexpectedly succeeded")
	}
	reg, loadErr := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	a, ok := reg.Agent(p.Target.Metadata.UID)
	if !ok || a.Status.Phase != metadata.PhaseRunning || a.Metadata.Annotations[metadata.AnnotationAgentPersonaDigest] != loaded.Digest {
		t.Fatalf("fixture must retain Running with the new persona: %+v", a)
	}
	if runner.stops != 1 || runner.resumes != 0 || out.NewPaneUID != "" {
		t.Fatalf("resume admission must not launch or replay: runner=%+v result=%+v", runner, out)
	}
	if strings.Contains(out.Recovery, "agent resume") {
		t.Errorf("Running Agent received an unusable plain resume recovery: %q", out.Recovery)
	}
	if strings.Contains(err.Error(), "is Offline") || strings.Contains(err.Error(), "needs `agent resume`") {
		t.Errorf("Running resume admission was misreported as Offline execution failure: %v", err)
	}
	for _, notice := range out.Notices {
		if strings.Contains(notice, "did not resume") || strings.Contains(notice, "the Agent is resumed") {
			t.Errorf("resume admission refusal received an unproven execution notice: %q", notice)
		}
	}
	if out.Recovery == "" || !strings.Contains(out.Recovery, p.Target.Metadata.UID) {
		t.Errorf("missing exact Agent state inspection recovery: %q", out.Recovery)
	}
}

// The supervisor and provider are inert shell fixtures. This proves a real SDK
// resume transaction commit, not provider birth, receipt I/O internals, or a reply.
func TestAgentPersonaSDKPostcommitRecovery(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "unobserved"}[unreadable], func(t *testing.T) {
			if _, err := exec.LookPath("tmux"); err != nil {
				t.Fatal("fixture requires tmux:", err)
			}
			root := t.TempDir()
			home, config, state := filepath.Join(root, "home"), filepath.Join(root, "config", "projmux"), filepath.Join(root, "state", "projmux")
			workspace, bin, socketDir := filepath.Join(root, "workspace"), filepath.Join(home, "bin"), filepath.Join(root, "socket")
			for _, path := range []string{workspace, bin, socketDir} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
					t.Setenv(key, "")
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
			t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			for _, name := range []string{"projmux", "claude"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexec /bin/sh\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			socket := filepath.Join(socketDir, "s")
			if info, err := os.Stat(socketDir); err != nil || !info.IsDir() || !filepath.IsAbs(socket) {
				t.Fatal("fixture socket directory must already exist")
			}
			run := func(args ...string) {
				t.Helper()
				out, err := (tmux.ExecRunner{}).Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
				if err != nil {
					t.Fatalf("fixture tmux %v: %v %s", args, err, out)
				}
			}
			t.Cleanup(func() {
				if out, err := (tmux.ExecRunner{}).Run(context.Background(), "tmux", "-S", socket, "kill-server"); err != nil {
					t.Errorf("fixture socket cleanup: %v %s", err, out)
				}
			})
			run("-f", "/dev/null", "new-session", "-d", "-s", "control", "-n", "control", "/bin/sh")
			run("set-option", "-g", "@projmux_app", "1")
			run("set-option", "-g", "@projmux_socket_name", "projmux")
			c, err := NewClient(Options{HomeDir: home, ConfigDir: config, StateDir: state, SocketPath: socket, DefaultShell: "/bin/sh", ProjmuxExecutable: filepath.Join(bin, "projmux")})
			if err != nil {
				t.Fatal(err)
			}
			c.agentEnvironment = map[string]string{"PATH": bin}
			project, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: workspace, Name: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			registryStore := store.NewStore(store.PathFor(state))
			var uid string
			if _, err := registryStore.Update(func(reg *metadata.Registry) error {
				windows := reg.WindowsOf(project.Project.Metadata.UID)
				if len(windows) == 0 {
					return errors.New("fixture project has no Window")
				}
				a, err := store.DefaultMutator().CreateAgent(reg, windows[0].Metadata.UID, metadata.CreateAgentOptions{Name: "worker", Provider: "claude", Workspace: metadata.AgentWorkspace{CWD: workspace}})
				if err != nil {
					return err
				}
				uid = a.Metadata.UID
				row, _ := reg.Agent(uid)
				row.Status.Phase = metadata.PhaseOffline // fixture seed, before the operation
				_, _, err = store.DefaultMutator().RecordAgentSessionRef(reg, uid, metadata.AgentSessionObservation{Provider: "claude", SessionID: "11111111-2222-4333-8444-555555555555"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := persona.NewDefaultStore(c.paths).Write("reviewer", []byte("Review carefully.\n")); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected receipt read failure after real resume commit")
			calls := 0
			var partial core.ResumeAgentResult
			var committedBytes []byte
			out, err := c.agentPersonaWithResume(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+uid, "", "", tmux.ExecRunner{}, func(ctx context.Context, plan runtime.AgentResumePlan, socket string, runner runtime.TmuxCommandRunner) (core.ResumeAgentResult, error) {
				calls++
				var err error
				partial, err = c.resumeAgentPlan(ctx, plan, socket, runner)
				if err != nil {
					t.Fatalf("real transaction must commit before error injection: %v", err)
				}
				if partial.Agent.Status.Phase != core.PhaseRunning || partial.Agent.Status.PaneRef == "" || partial.PaneID == "" {
					t.Fatalf("real commit missing from partial result: %+v", partial)
				}
				committedBytes, err = os.ReadFile(store.PathFor(state))
				if err != nil {
					t.Fatal(err)
				}
				if unreadable {
					if err := os.WriteFile(store.PathFor(state), []byte("invalid fixture receipt state"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				partial.Notices = append(partial.Notices, "fixture committed resume notice")
				return partial, cause
			})
			if !errors.Is(err, cause) || calls != 1 || out.NewPaneUID != partial.Agent.Status.PaneRef || out.NewPaneUID == "" {
				t.Fatalf("lost commit/cause or replayed: calls=%d result=%+v error=%v", calls, out, err)
			}
			if strings.Contains(out.Recovery, "agent resume") || !strings.Contains(out.Recovery, uid) || !strings.Contains(out.Recovery, out.NewPaneUID) || !strings.Contains(out.Recovery, socket) {
				t.Fatalf("partial commit needs exact state inspection: %+v", out)
			}
			if strings.Contains(err.Error(), "is Offline") || strings.Contains(strings.Join(out.Notices, "\n"), "did not resume") || !strings.Contains(strings.Join(out.Notices, "\n"), "fixture committed resume notice") {
				t.Fatalf("partial commit misclassified or notice lost: %+v %v", out, err)
			}
			if unreadable {
				if !strings.Contains(strings.Join(out.Notices, "\n"), "unobserved") {
					t.Fatal("unreadable fresh state was reported as known")
				}
				if err := os.WriteFile(store.PathFor(state), committedBytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reg, loadErr := registryStore.LoadReadOnly()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			a, _ := reg.Agent(uid)
			if a.Status.Phase != metadata.PhaseRunning || a.Status.PaneRef != out.NewPaneUID || a.Metadata.Annotations[metadata.AnnotationAgentPersona] != "reviewer" {
				t.Fatalf("error handling changed the committed state: %+v", a)
			}
		})
	}
}
