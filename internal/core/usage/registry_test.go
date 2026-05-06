package usage

import (
	"context"
	"reflect"
	"testing"
)

type fakeAdapter struct {
	name   string
	events []TokenEvent
	err    error
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) Collect(ctx context.Context) ([]TokenEvent, error) {
	return f.events, f.err
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	c := &fakeAdapter{name: "claude"}
	x := &fakeAdapter{name: "codex"}

	if err := r.Register(c); err != nil {
		t.Fatalf("Register(claude) error = %v", err)
	}
	if err := r.Register(x); err != nil {
		t.Fatalf("Register(codex) error = %v", err)
	}

	got, ok := r.Lookup("claude")
	if !ok {
		t.Fatalf("Lookup(claude) ok=false, want true")
	}
	if got.Name() != "claude" {
		t.Fatalf("Lookup(claude).Name() = %q, want claude", got.Name())
	}

	if _, ok := r.Lookup("missing"); ok {
		t.Fatalf("Lookup(missing) ok=true, want false")
	}

	if got, want := r.Names(), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRegistryRegisterDuplicateRejected(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(&fakeAdapter{name: "claude"}); err != nil {
		t.Fatalf("first Register error = %v", err)
	}
	if err := r.Register(&fakeAdapter{name: "claude"}); err == nil {
		t.Fatalf("duplicate Register error = nil, want non-nil")
	}
}

func TestRegistryReplaceOverridesExisting(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	original := &fakeAdapter{name: "claude"}
	replacement := &fakeAdapter{name: "claude"}

	if err := r.Register(original); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := r.Replace(replacement); err != nil {
		t.Fatalf("Replace error = %v", err)
	}
	got, _ := r.Lookup("claude")
	if got != replacement {
		t.Fatalf("Lookup after Replace = %v, want %v", got, replacement)
	}
}

func TestRegistryRejectsEmptyName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(&fakeAdapter{name: ""}); err == nil {
		t.Fatalf("Register(empty name) error = nil, want non-nil")
	}
	if err := r.Register(nil); err == nil {
		t.Fatalf("Register(nil) error = nil, want non-nil")
	}
}

func TestRegistryAllReturnsSorted(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	_ = r.Register(&fakeAdapter{name: "codex"})
	_ = r.Register(&fakeAdapter{name: "claude"})

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
	if all[0].Name() != "claude" || all[1].Name() != "codex" {
		t.Fatalf("All() order = %s,%s; want claude,codex", all[0].Name(), all[1].Name())
	}
}
