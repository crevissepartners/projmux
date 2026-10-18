package consumer_test

import (
	"context"
	"errors"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"testing"
)

func TestExternalAgentPersona(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{HomeDir: t.TempDir(), StateDir: t.TempDir(), ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var out core.AgentPersonaResult
	out, err = c.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:missing", Persona: "reviewer", DryRun: true})
	var r *projmux.Refusal
	if !errors.As(err, &r) || r.Code != projmux.CodeResourceNotFound || out.AgentUID != "" {
		t.Fatalf("%+v %v", out, err)
	}
	_, err = c.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{})
	if !errors.As(err, &r) || r.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.DetachAgentPersona(ctx, core.DetachAgentPersonaRequest{Ref: "uid:missing"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
