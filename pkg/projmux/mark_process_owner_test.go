package projmux

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ownerMarkFixture registers a process Agent whose child is recorded, so its
// binding is the current activation. Names differ per caller.
func ownerMarkFixture(t *testing.T, name string, active bool) (*Client, core.ProcessBinding, string) {
	t.Helper()
	state := t.TempDir()
	storage := store.NewStore(store.PathFor(state))
	var binding m.ProcessBinding
	_, err := storage.Update(func(reg *m.Registry) error {
		mut := store.DefaultMutator()
		created, e := mut.RegisterProject(reg, m.RegisterProjectOptions{Root: t.TempDir(), Name: name + "-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		_, _, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, m.BootstrapWindow{Name: name + "-window"}, m.CreateAgentOptions{Name: name + "-agent", Provider: "codex", OperationID: "create"}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "op"})
		if e != nil {
			return e
		}
		binding = p.Status.ProcessSession.Binding
		if !active {
			return nil
		}
		return mut.RecordProcessChild(reg, m.ProcessActivation{Binding: binding, HostProcess: m.ProcessIdentity{PID: 2000000000, OwnerUID: 1000, Start: "host-start"}, Child: m.ProcessIdentity{PID: 2000000001, OwnerUID: 1000, Start: "child-start"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Options{StateDir: state, ConfigDir: state + "/config", HomeDir: state})
	if err != nil {
		t.Fatal(err)
	}
	var out core.ProcessBinding
	if err = unregisterCopy(binding, &out); err != nil {
		t.Fatal(err)
	}
	return c, out, store.PathFor(state)
}

func registryBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wantRefusal(t *testing.T, err error, code Code, agent string) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Code != code || r.AgentUID != agent {
		t.Fatalf("want %s for %q, got %#v (%v)", code, agent, r, err)
	}
	return r
}

func TestMarkProcessOwnerWritesAndRepeatIsUnchanged(t *testing.T) {
	c, b, path := ownerMarkFixture(t, "mark-write", true)
	ctx := context.Background()
	res, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "web"})
	if err != nil || !res.Changed || res.Previous != "" || res.Agent.Metadata.Labels["process-owner"] != "web" {
		t.Fatalf("%+v %v", res, err)
	}
	before := registryBytes(t, path)
	res, err = c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "web"})
	if err != nil || res.Changed || res.Previous != "web" || res.Agent.Metadata.Labels["process-owner"] != "web" {
		t.Fatalf("%+v %v", res, err)
	}
	if !bytes.Equal(before, registryBytes(t, path)) {
		t.Fatal("unchanged repeat rewrote the Registry")
	}
}

func TestMarkProcessOwnerForeignRefusedUnlessReplaced(t *testing.T) {
	c, b, path := ownerMarkFixture(t, "mark-foreign", true)
	ctx := context.Background()
	if _, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "other-host"}); err != nil {
		t.Fatal(err)
	}
	before := registryBytes(t, path)
	_, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "web"})
	wantRefusal(t, err, CodeProcessOwnerMarkForeign, b.AgentUID)
	if !bytes.Equal(before, registryBytes(t, path)) {
		t.Fatal("foreign refusal changed the Registry")
	}
	res, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "web", ReplaceForeign: true})
	if err != nil || !res.Changed || res.Previous != "other-host" || res.Agent.Metadata.Labels["process-owner"] != "web" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMarkProcessOwnerStaleBindingTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*core.ProcessBinding)
		code   Code
	}{
		{"host", func(b *core.ProcessBinding) { b.HostInstanceID = "other-host" }, CodeProcessOwnerMarkStale},
		{"project", func(b *core.ProcessBinding) { b.ProjectUID = "proj-other" }, CodeProcessOwnerMarkStale},
		{"window", func(b *core.ProcessBinding) { b.WindowUID = "win-other" }, CodeProcessOwnerMarkStale},
		{"pane", func(b *core.ProcessBinding) { b.PaneUID = "pane-other" }, CodeProcessOwnerMarkStale},
		{"generation", func(b *core.ProcessBinding) { b.Generation = "other-generation" }, CodeProcessOwnerMarkStale},
		{"operation", func(b *core.ProcessBinding) { b.OperationID = "other-op" }, CodeProcessOwnerMarkStale},
		{"agent", func(b *core.ProcessBinding) { b.AgentUID = "agent-missing" }, CodeResourceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, b, path := ownerMarkFixture(t, "mark-stale-"+tc.name, true)
			agent := b.AgentUID
			tc.mutate(&b)
			if tc.code == CodeResourceNotFound {
				agent = b.AgentUID
			}
			before := registryBytes(t, path)
			_, err := c.MarkProcessOwner(context.Background(), core.MarkProcessOwnerRequest{Binding: b, Owner: "web", ReplaceForeign: true})
			wantRefusal(t, err, tc.code, agent)
			if !bytes.Equal(before, registryBytes(t, path)) {
				t.Fatal("refusal changed the Registry")
			}
		})
	}
}

func TestMarkProcessOwnerInactiveReservationIsStale(t *testing.T) {
	c, b, path := ownerMarkFixture(t, "mark-inactive", false)
	before := registryBytes(t, path)
	_, err := c.MarkProcessOwner(context.Background(), core.MarkProcessOwnerRequest{Binding: b, Owner: "web"})
	wantRefusal(t, err, CodeProcessOwnerMarkStale, b.AgentUID)
	if !bytes.Equal(before, registryBytes(t, path)) {
		t.Fatal("refusal changed the Registry")
	}
}

func TestMarkProcessOwnerInputRefusals(t *testing.T) {
	c, b, _ := ownerMarkFixture(t, "mark-input", true)
	ctx := context.Background()
	for _, owner := range []string{"", "  "} {
		_, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: owner})
		if r := wantRefusal(t, err, CodeSelectorInvalid, ""); r.Field != "MarkProcessOwnerRequest.Owner" {
			t.Fatalf("field %q", r.Field)
		}
	}
	empty := b
	empty.OperationID = ""
	_, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: empty, Owner: "web"})
	if r := wantRefusal(t, err, CodeSelectorInvalid, ""); r.Field != "MarkProcessOwnerRequest.Binding.OperationID" {
		t.Fatalf("field %q", r.Field)
	}
}

func TestMarkProcessOwnerCancelledBeforeWrite(t *testing.T) {
	c, b, path := ownerMarkFixture(t, "mark-cancel", true)
	before := registryBytes(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.MarkProcessOwner(ctx, core.MarkProcessOwnerRequest{Binding: b, Owner: "web"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !bytes.Equal(before, registryBytes(t, path)) {
		t.Fatal("cancelled call changed the Registry")
	}
}
