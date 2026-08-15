package app

import (
	"io"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// newTestDeleteCommand builds a delete route whose confirmation answer is
// scripted rather than read from a terminal.
func newTestDeleteCommand(store *fakeResourceStore, interactive bool, answer bool, prompts *[]string) *deleteCommand {
	return &deleteCommand{
		store: store.store(),
		confirm: &confirmer{
			interactive: func() bool { return interactive },
			ask: func(prompt string, _ io.Writer) (bool, error) {
				if prompts != nil {
					*prompts = append(*prompts, prompt)
				}
				return answer, nil
			},
		},
		resolveKinds: deleteRegistryKinds,
	}
}

func registryUIDs(registry coremetadata.Registry) map[string]bool {
	out := map[string]bool{}
	for _, project := range registry.Projects {
		out[project.Metadata.UID] = true
	}
	for _, window := range registry.Windows {
		out[window.Metadata.UID] = true
	}
	for _, pane := range registry.Panes {
		out[pane.Metadata.UID] = true
	}
	for _, agent := range registry.Agents {
		out[agent.Metadata.UID] = true
	}
	return out
}

// TestDeleteCascadeRemovesExactlyTheDescendantPlan is the cascade table.
//
// Each case names both what must disappear and what must survive, because the
// contract is as much about the preserved parent and siblings as it is about the
// removed descendants.
func TestDeleteCascadeRemovesExactlyTheDescendantPlan(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		gone    []string
		kept    []string
		cascade int
	}{
		{
			name:    "a window cascades to its agents and every descendant pane, and preserves the project",
			args:    []string{"window", "main", "--project", "alpha", "--yes"},
			gone:    []string{"win-alpha-main", "agt-alpha-codex", "pan-alpha-codex", "pan-alpha-zsh", "pan-alpha-log"},
			kept:    []string{"prj-alpha", "win-alpha-review", "pan-alpha-review", "win-beta-main"},
			cascade: 4,
		},
		{
			name:    "an agent cascades to its managed panes and preserves the window and sibling panes",
			args:    []string{"agent", "codex", "--project", "alpha", "--yes"},
			gone:    []string{"agt-alpha-codex", "pan-alpha-codex"},
			kept:    []string{"win-alpha-main", "pan-alpha-zsh", "pan-alpha-log", "agt-beta-codex"},
			cascade: 1,
		},
		{
			name:    "a leaf pane removes only itself",
			args:    []string{"pane", "log", "--project", "alpha", "--window", "main"},
			gone:    []string{"pan-alpha-log"},
			kept:    []string{"pan-alpha-zsh", "pan-alpha-codex", "agt-alpha-codex", "win-alpha-main"},
			cascade: 0,
		},
		{
			name:    "multiple targets fan out over the whole resolved set",
			args:    []string{"pane", "zsh", "log", "--project", "alpha", "--window", "main", "--yes"},
			gone:    []string{"pan-alpha-zsh", "pan-alpha-log"},
			kept:    []string{"pan-alpha-codex", "pan-alpha-review", "win-alpha-main"},
			cascade: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, stderr, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err != nil {
				t.Fatalf("delete %v error = %v (stderr=%s)", test.args, err, stderr)
			}
			if store.writes != 1 {
				t.Fatalf("delete %v committed %d writes, want 1", test.args, store.writes)
			}
			uids := registryUIDs(store.registry)
			for _, uid := range test.gone {
				if uids[uid] {
					t.Fatalf("delete %v left %q behind", test.args, uid)
				}
			}
			for _, uid := range test.kept {
				if !uids[uid] {
					t.Fatalf("delete %v removed %q, which must be preserved", test.args, uid)
				}
			}
			if !strings.Contains(stdout, "descendant resource") {
				t.Fatalf("delete %v stdout does not report the cascade:\n%s", test.args, stdout)
			}
			// The result the registry is left in must still validate, which is
			// what proves the cascade did not leave a dangling ownerRef,
			// primaryPaneRef, paneRef, or name reservation.
			if err := store.registry.Validate(); err != nil {
				t.Fatalf("delete %v left an invalid registry: %v", test.args, err)
			}
		})
	}
}

// TestDeletePaneLeavesAnAgentOwnedCurrentPaneAgentAliveAsOffline pins the one
// lifecycle transition the delete verb owns.
func TestDeletePaneLeavesAnAgentOwnedCurrentPaneAgentAliveAsOffline(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"pane", "codex-pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("delete pane error = %v", err)
	}
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("deleting the managed pane removed the agent")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("agent phase = %q, want Offline", agent.Status.Phase)
	}
	if agent.Status.PaneRef != "" {
		t.Fatalf("agent still points at pane %q", agent.Status.PaneRef)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("the managed pane survived the delete")
	}
}

// TestDeleteDryRunPrintsTheFullPlanAndMutatesNothing is the dry-run half of the
// destructive UX contract.
func TestDeleteDryRunPrintsTheFullPlanAndMutatesNothing(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"window", "main", "--project", "alpha", "--dry-run"},
		{"agent", "codex", "--project", "alpha", "--dry-run"},
		{"pane", "zsh", "log", "--project", "alpha", "--window", "main", "--dry-run"},
	} {
		store := newFakeResourceStore(t)
		before := store.snapshot()
		stdout, stderr, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), args...)
		if err != nil {
			t.Fatalf("delete %v --dry-run error = %v", args, err)
		}
		if store.transactions != 0 {
			t.Fatalf("delete %v --dry-run opened %d write transactions, want 0", args, store.transactions)
		}
		if store.snapshot() != before {
			t.Fatalf("delete %v --dry-run mutated the registry", args)
		}
		if !strings.Contains(stdout, "would delete") || !strings.Contains(stdout, "dry-run: nothing was deleted") {
			t.Fatalf("delete %v --dry-run stdout = %q", args, stdout)
		}
		if stderr != "" {
			t.Fatalf("delete %v --dry-run stderr = %q, want none", args, stderr)
		}
	}

	// The dry run lists the whole descendant plan, not just the targets.
	store := newFakeResourceStore(t)
	stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"window", "main", "--project", "alpha", "--dry-run")
	if err != nil {
		t.Fatalf("dry run error = %v", err)
	}
	for _, want := range []string{
		"window/main uid=win-alpha-main owner=project/alpha",
		"cascade agent/codex uid=agt-alpha-codex",
		"cascade pane/codex-pane uid=pan-alpha-codex",
		"cascade pane/zsh uid=pan-alpha-zsh",
		"cascade pane/log uid=pan-alpha-log",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run plan is missing %q:\n%s", want, stdout)
		}
	}
}

// TestDeleteRequiresConfirmationAndRefusesNonInteractivelyWithoutYes is
// acceptance criterion 5's confirmation half.
func TestDeleteRequiresConfirmationAndRefusesNonInteractivelyWithoutYes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "cascade", args: []string{"window", "main", "--project", "alpha"}},
		{name: "agent cascade", args: []string{"agent", "codex", "--project", "alpha"}},
		{name: "multi target", args: []string{"pane", "zsh", "log", "--project", "alpha", "--window", "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err == nil {
				t.Fatalf("delete %v ran without confirmation", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("delete %v refusal is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("delete %v refusal does not name --yes: %v", test.args, err)
			}
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry before confirmation", test.args)
			}
			if stdout != "" {
				t.Fatalf("delete %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}

			// On a TTY the same invocation asks exactly once and honors "no".
			store = newFakeResourceStore(t)
			before = store.snapshot()
			var prompts []string
			_, _, err = runRoute(t, newTestDeleteCommand(store, true, false, &prompts), test.args...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("delete %v declined confirmation error = %v", test.args, err)
			}
			if len(prompts) != 1 {
				t.Fatalf("delete %v asked %d times, want 1", test.args, len(prompts))
			}
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry after a declined confirmation", test.args)
			}

			// A "yes" answer on a TTY runs without --yes.
			store = newFakeResourceStore(t)
			prompts = nil
			if _, _, err := runRoute(t, newTestDeleteCommand(store, true, true, &prompts), test.args...); err != nil {
				t.Fatalf("delete %v after an accepted confirmation error = %v", test.args, err)
			}
			if store.writes != 1 {
				t.Fatalf("delete %v after confirmation committed %d writes, want 1", test.args, store.writes)
			}
		})
	}
}

// TestDeleteExactOneLeafPaneNeedsNoConfirmation is the single carve-out.
func TestDeleteExactOneLeafPaneNeedsNoConfirmation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	var prompts []string
	// Non-interactive, no --yes: the leaf delete still runs.
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, &prompts),
		"pane", "log", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("leaf pane delete error = %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("leaf pane delete prompted: %v", prompts)
	}
	if store.writes != 1 {
		t.Fatalf("leaf pane delete committed %d writes, want 1", store.writes)
	}

	// The carve-out is exactly "one target, Pane kind, no descendants": a single
	// Window target with no descendants is still confirmed, and so is a single
	// Agent target.
	store = newFakeResourceStore(t)
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"window", "review", "--project", "alpha"); err == nil {
		t.Fatal("a single Window delete ran without confirmation")
	}
	if store.transactions != 0 {
		t.Fatalf("a refused Window delete opened %d transactions", store.transactions)
	}
}

// TestDeleteValidationFailuresLeaveZeroMutations is acceptance criterion 5's
// atomicity half: any preflight failure exits 2 with the registry untouched.
func TestDeleteValidationFailuresLeaveZeroMutations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no match", args: []string{"pane", "nosuch", "--project", "alpha", "--yes"}, want: "matched no panes"},
		{name: "one missing target in a multi-target set", args: []string{"pane", "zsh", "nosuch", "--project", "alpha", "--window", "main", "--yes"}, want: `nosuch matched no panes`},
		{name: "label filter empties the set", args: []string{"pane", "zsh", "--project", "alpha", "--window", "main", "--selector", "role=nosuch", "--yes"}, want: "matched no panes"},
		{name: "unknown project scope", args: []string{"pane", "zsh", "--project", "nosuch", "--yes"}, want: "--project nosuch"},
		{name: "comma is never split", args: []string{"pane", "zsh,log", "--project", "alpha", "--window", "main", "--yes"}, want: "matched no panes"},
		{name: "unknown kind", args: []string{"project", "alpha", "--yes"}, want: "not available"},
		{name: "no kind", args: nil, want: "delete requires a resource kind"},
		{name: "unknown flag", args: []string{"pane", "--bogus"}, want: "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err == nil {
				t.Fatalf("delete %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("delete %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("delete %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("delete %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if store.transactions != 0 {
				t.Fatalf("delete %v opened %d write transactions, want 0", test.args, store.transactions)
			}
			if store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry", test.args)
			}
		})
	}
}

// TestDeleteAbortsWhenTheCascadePlanChangesBeforeExecution proves the preflight
// is enforced rather than advisory: a plan that no longer matches inside the
// transaction aborts the whole operation with zero mutations.
func TestDeleteAbortsWhenTheCascadePlanChangesBeforeExecution(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	cmd := newTestDeleteCommand(store, false, false, nil)
	commit := cmd.store.update
	var beforeExecution string
	// Simulate a concurrent writer: a new Pane appears under the target Window
	// between the preflight read and the locked transaction.
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{
				UID: "pan-alpha-late", Name: "late",
				OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"},
				CreatedAt: resourceFixtureClock,
			},
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
		})
		store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
			Scope: "win-alpha-main", Kind: coremetadata.KindPane, Name: "late", UID: "pan-alpha-late",
		})
		beforeExecution = store.snapshot()
		return commit(fn)
	}

	stdout, _, err := runRoute(t, cmd, "window", "main", "--project", "alpha", "--yes")
	if err == nil {
		t.Fatal("delete ran against a stale cascade plan")
	}
	if !strings.Contains(err.Error(), "cascade plan changed") {
		t.Fatalf("error = %v, want the stale-plan abort", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.writes != 0 {
		t.Fatalf("committed %d writes, want 0", store.writes)
	}
	if beforeExecution == "" {
		t.Fatal("the transaction never opened, so the abort proves nothing")
	}
	if store.snapshot() != beforeExecution {
		t.Fatalf("the aborted delete still mutated the registry")
	}
}
