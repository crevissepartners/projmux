package metadata

import (
	"errors"
	"maps"
	"testing"
)

// labelFixture registers one Project and returns the registry plus the uid of
// each kind SetLabels addresses.
func labelFixture(t *testing.T) (Mutator, *Registry, map[Kind]string) {
	t.Helper()
	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	result, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	agent, err := mutator.CreateAgent(&registry, result.Windows[0].Metadata.UID, CreateAgentOptions{
		Provider:    "codex",
		OperationID: "op-fixture-agent",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return mutator, &registry, map[Kind]string{
		KindProject: result.Project.Metadata.UID,
		KindWindow:  result.Windows[0].Metadata.UID,
		KindPane:    result.Panes[0].Metadata.UID,
		KindAgent:   agent.Metadata.UID,
	}
}

// TestSetLabelsWritesEveryAddressableKind is the kind half of the contract: the
// same write reaches all four kinds the CLI can address, and it moves nothing
// but metadata.labels.
func TestSetLabelsWritesEveryAddressableKind(t *testing.T) {
	t.Parallel()

	mutator, registry, uids := labelFixture(t)
	for _, kind := range []Kind{KindProject, KindWindow, KindPane, KindAgent} {
		uid := uids[kind]
		before, ok := registry.resourceMetadata(kind, uid)
		if !ok {
			t.Fatalf("%s %q missing from the fixture", kind, uid)
		}
		wantName := before.Name
		wantOwner := before.OwnerUID()

		meta, changed, err := mutator.SetLabels(registry, kind, uid, map[string]string{"role": "worker"}, nil)
		if err != nil {
			t.Fatalf("SetLabels(%s) error = %v", kind, err)
		}
		if !changed {
			t.Fatalf("SetLabels(%s) reported no change on a new key", kind)
		}
		if meta.Labels["role"] != "worker" {
			t.Fatalf("SetLabels(%s) labels = %v", kind, meta.Labels)
		}
		if meta.UID != uid || meta.Name != wantName || meta.OwnerUID() != wantOwner {
			t.Fatalf("SetLabels(%s) moved identity or ownership: %+v", kind, meta)
		}
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry invalid after labelling every kind: %v", err)
	}
}

// TestSetLabelsOverwritesRemovesAndIsIdempotent is the value half. Overwriting
// is the default because this route addresses exactly one resolved resource,
// and a removal is idempotent because "absent" is one end state, not two.
func TestSetLabelsOverwritesRemovesAndIsIdempotent(t *testing.T) {
	t.Parallel()

	mutator, registry, uids := labelFixture(t)
	uid := uids[KindPane]

	if _, _, err := mutator.SetLabels(registry, KindPane, uid, map[string]string{"role": "shell", "phase": "task-0"}, nil); err != nil {
		t.Fatalf("SetLabels error = %v", err)
	}

	meta, changed, err := mutator.SetLabels(registry, KindPane, uid, map[string]string{"role": "worker"}, nil)
	if err != nil || !changed {
		t.Fatalf("overwrite: changed = %v, err = %v", changed, err)
	}
	if !maps.Equal(meta.Labels, map[string]string{"role": "worker", "phase": "task-0"}) {
		t.Fatalf("overwrite left %v, want only role replaced", meta.Labels)
	}

	if _, changed, err = mutator.SetLabels(registry, KindPane, uid, map[string]string{"role": "worker"}, nil); err != nil || changed {
		t.Fatalf("rewriting the same value: changed = %v, err = %v; want no change", changed, err)
	}

	meta, changed, err = mutator.SetLabels(registry, KindPane, uid, nil, []string{"phase"})
	if err != nil || !changed {
		t.Fatalf("remove: changed = %v, err = %v", changed, err)
	}
	if !maps.Equal(meta.Labels, map[string]string{"role": "worker"}) {
		t.Fatalf("remove left %v", meta.Labels)
	}

	if _, changed, err = mutator.SetLabels(registry, KindPane, uid, nil, []string{"phase"}); err != nil || changed {
		t.Fatalf("removing an absent key: changed = %v, err = %v; want a no-op success", changed, err)
	}

	meta, changed, err = mutator.SetLabels(registry, KindPane, uid, nil, []string{"role"})
	if err != nil || !changed {
		t.Fatalf("removing the last key: changed = %v, err = %v", changed, err)
	}
	if meta.Labels != nil {
		t.Fatalf("an emptied label map stayed %v, want nil", meta.Labels)
	}
}

// TestSetLabelsStoresValuesWithoutAPolicy is the judgment this package makes on
// purpose: labels have no length or character-set rule, because `create
// --label` has never had one and a second, stricter writer would give one field
// two contracts.
func TestSetLabelsStoresValuesWithoutAPolicy(t *testing.T) {
	t.Parallel()

	mutator, registry, uids := labelFixture(t)
	values := map[string]string{
		"url":     "https://example.test/merge_requests/1?tab=diffs",
		"empty":   "",
		"spaced":  "two words",
		"unicode": "레이블",
		"upper":   "MixedCase",
		"long":    longValue(4096),
	}
	meta, changed, err := mutator.SetLabels(registry, KindAgent, uids[KindAgent], values, nil)
	if err != nil || !changed {
		t.Fatalf("SetLabels error = %v, changed = %v", err, changed)
	}
	if !maps.Equal(meta.Labels, values) {
		t.Fatalf("stored labels = %v, want them byte for byte", meta.Labels)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry rejected an unconstrained label value: %v", err)
	}
}

func longValue(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'x'
	}
	return string(buf)
}

// TestSetLabelsRefusesBeforeWriting covers the refusals that survive when a
// caller reaches the core directly instead of through the CLI parser.
func TestSetLabelsRefusesBeforeWriting(t *testing.T) {
	t.Parallel()

	mutator, registry, uids := labelFixture(t)
	uid := uids[KindWindow]
	before := registry.Clone()

	for _, test := range []struct {
		name   string
		kind   Kind
		uid    string
		set    map[string]string
		remove []string
		want   error
	}{
		{name: "absent uid", kind: KindWindow, uid: "window-does-not-exist", set: map[string]string{"role": "x"}, want: ErrNotFound},
		{name: "absent kind", kind: KindControlSession, uid: uid, set: map[string]string{"role": "x"}, want: ErrNotFound},
		{name: "empty set key", kind: KindWindow, uid: uid, set: map[string]string{"  ": "x"}, want: ErrInvalidLabel},
		{name: "empty remove key", kind: KindWindow, uid: uid, remove: []string{""}, want: ErrInvalidLabel},
		{name: "set and removed at once", kind: KindWindow, uid: uid, set: map[string]string{"role": "x"}, remove: []string{"role"}, want: ErrInvalidLabel},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, changed, err := mutator.SetLabels(registry, test.kind, test.uid, test.set, test.remove)
			if !errors.Is(err, test.want) {
				t.Fatalf("SetLabels error = %v, want %v", err, test.want)
			}
			if changed {
				t.Fatal("a refused SetLabels reported a change")
			}
		})
	}

	if !maps.Equal(registryLabels(t, registry, uid), registryLabels(t, &before, uid)) {
		t.Fatal("a refused SetLabels still wrote the Registry")
	}
}

func registryLabels(t *testing.T, registry *Registry, uid string) map[string]string {
	t.Helper()
	meta, ok := registry.resourceMetadata(KindWindow, uid)
	if !ok {
		t.Fatalf("window %q missing", uid)
	}
	return meta.Labels
}
