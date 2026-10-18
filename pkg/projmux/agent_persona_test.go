package projmux

import (
	"context"
	"errors"
	"reflect"
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
			if !errors.As(err, &refusal) || refusal.Code != CodePersonaUnavailable {
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
