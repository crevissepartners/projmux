package mutations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestMarkProcessOwnerDecidesAndWritesInOneUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	s := NewStore(path)
	var binding metadata.ProcessBinding
	if _, err := s.Update(func(reg *metadata.Registry) error {
		mut := store.DefaultMutator()
		created, e := mut.RegisterProject(reg, metadata.RegisterProjectOptions{Root: t.TempDir(), Name: "owner-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		_, _, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, metadata.BootstrapWindow{Name: "owner-window"}, metadata.CreateAgentOptions{Name: "owner-agent", Provider: "codex", OperationID: "create"}, metadata.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "op"})
		if e != nil {
			return e
		}
		binding = p.Status.ProcessSession.Binding
		return mut.RecordProcessChild(reg, metadata.ProcessActivation{Binding: binding, HostProcess: metadata.ProcessIdentity{PID: 2000000000, OwnerUID: 1000, Start: "host-start"}, Child: metadata.ProcessIdentity{PID: 2000000001, OwnerUID: 1000, Start: "child-start"}})
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := MarkProcessOwner(ctx, s, binding, "web", false)
	if err != nil || !res.Changed || res.Previous != "" {
		t.Fatalf("%+v %v", res, err)
	}
	stale := binding
	stale.Generation = "other-generation"
	_, err = MarkProcessOwner(ctx, s, stale, "web", true)
	var f *Failure
	if !errors.As(err, &f) || f.Code != ProcessOwnerMarkStale || f.AgentUID != binding.AgentUID {
		t.Fatalf("stale: %#v %v", f, err)
	}
	_, err = MarkProcessOwner(ctx, s, binding, "other", false)
	if !errors.As(err, &f) || f.Code != ProcessOwnerMarkForeign {
		t.Fatalf("foreign: %#v %v", f, err)
	}
	res, err = MarkProcessOwner(ctx, s, binding, "other", true)
	if err != nil || !res.Changed || res.Previous != "web" {
		t.Fatalf("%+v %v", res, err)
	}
}
