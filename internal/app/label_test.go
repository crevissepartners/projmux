package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func newTestLabelCommand(store *fakeResourceStore) *labelCommand {
	return &labelCommand{store: store.store(), runtime: liveAlphaRuntime()}
}

// selectorMatches runs the plural read the acceptance criteria are stated in,
// so the assertions below observe the label through `get --selector` rather
// than through the Registry the write just produced.
func selectorMatches(t *testing.T, store *fakeResourceStore, plural, label string, scope ...string) []string {
	t.Helper()
	args := append([]string{plural, "--selector", label, "-o", "uid"}, scope...)
	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), args...)
	if err != nil {
		t.Fatalf("get %v error = %v (stderr=%s)", args, err, stderr)
	}
	return strings.Fields(stdout)
}

// TestLabelIsSelectableImmediatelyAfterTheWrite is acceptance 1 and 2 for every
// kind the route addresses: a label attached after creation is what
// `get --selector` resolves on, and removing it stops the resolution.
func TestLabelIsSelectableImmediatelyAfterTheWrite(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		kind    string
		plural  string
		args    []string
		scope   []string
		wantUID string
	}{
		{
			name: "project", kind: "project", plural: "projects",
			args: []string{"project", "alpha"}, wantUID: "prj-alpha",
		},
		{
			name: "window", kind: "window", plural: "windows",
			args:  []string{"window", "review", "--project", "alpha"},
			scope: []string{"--project", "alpha"}, wantUID: "win-alpha-review",
		},
		{
			name: "pane", kind: "pane", plural: "panes",
			args:  []string{"pane", "log", "--project", "alpha", "--window", "main"},
			scope: []string{"--project", "alpha"}, wantUID: "pan-alpha-log",
		},
		{
			name: "agent", kind: "agent", plural: "agents",
			args:  []string{"agent", "codex", "--project", "alpha", "--window", "main"},
			scope: []string{"--project", "alpha"}, wantUID: "agt-alpha-codex",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)

			if got := selectorMatches(t, store, test.plural, "tier=api", test.scope...); len(got) != 0 {
				t.Fatalf("the fixture already matches tier=api: %v", got)
			}

			stdout, stderr, err := runRoute(t, newTestLabelCommand(store), append(test.args, "tier=api")...)
			if err != nil {
				t.Fatalf("label %v error = %v (stderr=%s)", test.args, err, stderr)
			}
			if !strings.Contains(stdout, "tier=api") {
				t.Fatalf("label %v stdout = %q, want it to report the resulting labels", test.args, stdout)
			}
			if store.writes != 1 {
				t.Fatalf("label %v committed %d writes, want 1", test.args, store.writes)
			}

			if got := selectorMatches(t, store, test.plural, "tier=api", test.scope...); len(got) != 1 || got[0] != test.wantUID {
				t.Fatalf("after the write, tier=api resolved %v, want [%s]", got, test.wantUID)
			}

			if _, stderr, err = runRoute(t, newTestLabelCommand(store), append(test.args, "tier-")...); err != nil {
				t.Fatalf("label %v tier- error = %v (stderr=%s)", test.args, err, stderr)
			}
			if got := selectorMatches(t, store, test.plural, "tier=api", test.scope...); len(got) != 0 {
				t.Fatalf("after the removal, tier=api still resolved %v", got)
			}
		})
	}
}

// TestLabelSetsRemovesAndOverwritesInOneInvocation covers the grammar as the
// operator types it: mixed operands, an overwrite with no guard flag, and a
// removal of a key that was never there.
func TestLabelSetsRemovesAndOverwritesInOneInvocation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	// The fixture Window already carries role=shell, so this is the overwrite
	// case rather than a first write.
	stdout, stderr, err := runRoute(t, newTestLabelCommand(store),
		"window", "main", "--project", "alpha", "role=worker", "phase=task-0", "absent-")
	if err != nil {
		t.Fatalf("label window error = %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "phase=task-0 role=worker") {
		t.Fatalf("label stdout = %q, want the resulting label set", stdout)
	}

	_, meta, ok := resourceFor(store.registry, coremetadata.KindWindow, "win-alpha-main")
	if !ok {
		t.Fatal("window disappeared")
	}
	if meta.Labels["role"] != "worker" || meta.Labels["phase"] != "task-0" || len(meta.Labels) != 2 {
		t.Fatalf("stored labels = %v", meta.Labels)
	}

	// A repeat of the same write changes nothing and says so rather than
	// claiming a mutation it did not make.
	before := store.snapshot()
	stdout, _, err = runRoute(t, newTestLabelCommand(store),
		"window", "main", "--project", "alpha", "role=worker")
	if err != nil {
		t.Fatalf("repeat label error = %v", err)
	}
	if !strings.Contains(stdout, "labels unchanged") {
		t.Fatalf("repeat label stdout = %q, want it to report no change", stdout)
	}
	if store.snapshot() != before {
		t.Fatal("a no-op label changed the Registry")
	}
}

// TestLabelRefusesBeforeOutputOrMutation is acceptance 3. Every row leaves zero
// bytes on stdout, zero Registry writes, and a reason naming its own cause.
func TestLabelRefusesBeforeOutputOrMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		args       []string
		wantReason string
	}{
		{
			name: "no operand", args: []string{"window", "main", "--project", "alpha"},
			wantReason: "requires at least one key=value",
		},
		{
			name: "malformed operand", args: []string{"window", "main", "--project", "alpha", "role"},
			wantReason: "is neither a second reference nor a label change",
		},
		{
			name: "empty key", args: []string{"window", "main", "--project", "alpha", "=worker"},
			wantReason: "has an empty key",
		},
		{
			name: "one key changed twice", args: []string{"window", "main", "--project", "alpha", "role=a", "role-"},
			wantReason: "is changed twice",
		},
		{
			name: "two references", args: []string{"window", "main", "review", "--project", "alpha", "role=a"},
			wantReason: "accepts at most one resource reference",
		},
		{
			name: "no such resource", args: []string{"window", "nowhere", "--project", "alpha", "role=a"},
			wantReason: "matched no windows",
		},
		{
			name: "ambiguous reference", args: []string{"window", "main", "role=a"},
			wantReason: "want exactly one",
		},
		{
			name: "unknown kind", args: []string{"snapshot", "role=a"},
			wantReason: "is not available",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()

			stdout, _, err := runRoute(t, newTestLabelCommand(store), test.args...)
			if err == nil {
				t.Fatalf("label %v was accepted", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("label %v error = %v, want an operator-input error", test.args, err)
			}
			if !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("label %v error = %v, want it to say %q", test.args, err, test.wantReason)
			}
			if stdout != "" {
				t.Fatalf("label %v wrote %q to stdout before refusing", test.args, stdout)
			}
			if store.writes != 0 || before != store.snapshot() {
				t.Fatalf("label %v mutated the Registry before refusing", test.args)
			}
		})
	}
}

// TestLabelAcceptsEveryValueCreateLabelAccepts pins the parity that keeps one
// field from growing two contracts: `create --label` stores a value with no
// length or character-set policy, and the post-creation writer must not be
// stricter. A later change that adds a value rule to either writer fails here.
func TestLabelAcceptsEveryValueCreateLabelAccepts(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"tier=api",
		"tier=",
		"url=https://example.test/merge_requests/1?tab=diffs",
		"note=two words",
		"unicode=레이블",
		"upper=MixedCase",
		"long=" + strings.Repeat("x", 4096),
		"dashed-key=value",
		"value-ends-in-dash=task-0-",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			created, createErr := labelMap([]string{raw})
			store := newFakeResourceStore(t)
			_, _, labelErr := runRoute(t, newTestLabelCommand(store), "window", "review", "--project", "alpha", raw)
			if (createErr == nil) != (labelErr == nil) {
				t.Fatalf("%q: create --label error = %v, label error = %v; the two writers disagree", raw, createErr, labelErr)
			}
			if createErr != nil {
				return
			}
			_, meta, ok := resourceFor(store.registry, coremetadata.KindWindow, "win-alpha-review")
			if !ok {
				t.Fatal("window disappeared")
			}
			for key, value := range created {
				if meta.Labels[key] != value {
					t.Fatalf("%q: create --label stored %q=%q, label stored %q", raw, key, value, meta.Labels[key])
				}
			}
		})
	}
}
