package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	if _, e := persona.NewDefaultStore(c.paths).Write("reviewer", []byte("Review carefully.\n")); e != nil {
		t.Fatal(e)
	}
	result, e := c.agentPersona(context.Background(), runtime.AgentPersonaRequest{Action: "attach", Persona: "reviewer", Confirm: true}, "uid:"+p.Target.Metadata.UID, "", "", runner)
	if e == nil || result.Recovery == "" || result.NewPaneUID != "" || result.NewPersona != "reviewer" {
		t.Fatalf("%+v %v", result, e)
	}
	reg, e := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
	if e != nil {
		t.Fatal(e)
	}
	a, _ := reg.Agent(p.Target.Metadata.UID)
	if a.Status.Phase != metadata.PhaseOffline || a.Metadata.Annotations[metadata.AnnotationAgentInstructionsSource] != metadata.SettingSourceAttach || a.Metadata.Annotations[metadata.AnnotationAgentPersona] != "reviewer" {
		t.Fatalf("%+v", a)
	}
	if runner.stops != 1 {
		t.Fatal("stop replayed")
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
