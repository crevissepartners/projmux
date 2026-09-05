package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentAndPaneNames returns the Agent created in a Window together with the
// name of the managed Pane it owns.
func agentAndPaneNames(t *testing.T, store *fakeResourceStore, windowUID string) (coremetadata.Agent, coremetadata.Pane) {
	t.Helper()
	var found []coremetadata.Agent
	for _, agent := range store.registry.Agents {
		if agent.Metadata.OwnerUID() == windowUID {
			found = append(found, agent)
		}
	}
	if len(found) != 1 {
		t.Fatalf("agents under window %q = %d, want exactly 1:\n%s", windowUID, len(found), store.snapshot())
	}
	pane, ok := store.registry.Pane(found[0].Status.PaneRef)
	if !ok {
		t.Fatalf("agent %q has no managed Pane:\n%s", found[0].Metadata.UID, store.snapshot())
	}
	return found[0], *pane
}

// TestCreateAgentNamesItsManagedPaneAfterItsAgent is acceptance criterion 1.
//
// `create agent` supplies an *explicit* `<agent-name>-pane` for the Pane the
// Agent owns, so the managed Pane is never addressed by its own raw UID. The
// rule used to live only in the launcher documentation as a mandatory
// follow-up `rename pane`; it is code now.
func TestCreateAgentNamesItsManagedPaneAfterItsAgent(t *testing.T) {
	t.Parallel()

	// 123 bytes is the largest Agent name whose derived Pane name still fits
	// the 128-byte ValidateName bound exactly.
	atTheLimit := strings.Repeat("a", 123)
	// 128 bytes is the largest legal Agent name, and 128+len("-pane") is 133.
	overTheLimit := strings.Repeat("b", 128)

	for _, test := range []struct {
		name string
		// explicit is the --name argument, empty for automatic Agent naming.
		explicit string
		// wantPaneName is empty when the Pane must fall back to its exact UID.
		wantPaneName string
	}{
		{name: "an explicit Agent name derives the Pane name", explicit: "reviewer", wantPaneName: "reviewer-pane"},
		// With no --name the Agent's own automatic name is its exact full UID,
		// so the Pane becomes `<agent-uid>-pane`. That is still not the *Pane's*
		// UID, which is the property this criterion is about.
		{name: "an automatic Agent name still derives the Pane name", explicit: "", wantPaneName: "agent-test-1-pane"},
		{name: "a 123-byte Agent name derives a 128-byte Pane name", explicit: atTheLimit, wantPaneName: atTheLimit + "-pane"},
		// The only permitted fallback. Refusing here would break
		// `create agent --name <128 bytes>` calls that succeed today.
		{name: "a 128-byte Agent name falls back to automatic Pane naming", explicit: overTheLimit, wantPaneName: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())

			args := []string{"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "review"}
			if test.explicit != "" {
				args = append(args, "--name", test.explicit)
			}
			if _, _, err := runRoute(t, create, args...); err != nil {
				t.Fatalf("create %v error = %v", args, err)
			}

			agent, pane := agentAndPaneNames(t, store, "win-alpha-review")
			if test.explicit != "" && agent.Metadata.Name != test.explicit {
				t.Fatalf("agent name = %q, want %q", agent.Metadata.Name, test.explicit)
			}
			if test.wantPaneName == "" {
				if pane.Metadata.Name != pane.Metadata.UID {
					t.Fatalf("over-long derivation did not fall back: pane name = %q, want the exact uid %q",
						pane.Metadata.Name, pane.Metadata.UID)
				}
			} else {
				if pane.Metadata.Name != test.wantPaneName {
					t.Fatalf("pane name = %q, want %q", pane.Metadata.Name, test.wantPaneName)
				}
				if pane.Metadata.Name == pane.Metadata.UID {
					t.Fatalf("pane name is still its own uid %q", pane.Metadata.UID)
				}
			}
			// Whatever the name, the reservation follows it, so the Pane is
			// addressable by that exact spelling.
			scope := "prj-alpha"
			var reserved string
			for _, reservation := range store.registry.NameReservations {
				if reservation.Scope == scope && reservation.Kind == coremetadata.KindPane && reservation.UID == pane.Metadata.UID {
					reserved = reservation.Name
				}
			}
			if reserved != pane.Metadata.Name {
				t.Fatalf("pane reservation = %q, want %q", reserved, pane.Metadata.Name)
			}
		})
	}
}

// TestADerivedAgentPaneNameCollisionIsATypedZeroWriteRefusal is acceptance
// criterion 2.
//
// The derived name goes through the same explicit-name machinery an operator's
// `--name` does, so a name already taken root-wide for kind Pane is
// `ErrNameConflict` -- a typed usage refusal. The whole metadata phase runs
// before the first tmux or provider call, so the refusal costs zero Registry
// commits, zero tmux objects, zero provider processes, and publishes no uid.
//
// Length is the only fallback. A collision must never silently rename.
func TestADerivedAgentPaneNameCollisionIsATypedZeroWriteRefusal(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	// Take the derived name with an ordinary Window-owned Pane in the same
	// root, so the collision is root-wide rather than owner-local.
	if _, err := store.mutator().RenamePane(&store.registry, "pan-alpha-log", "reviewer-pane"); err != nil {
		t.Fatalf("seeding the colliding Pane name: %v", err)
	}
	before := store.snapshot()
	store.writes = 0
	store.newUIDs = nil

	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)

	// The Agent name `reviewer` itself is free; only its derived Pane name is
	// taken, so this falsifies the derivation rather than the Agent naming.
	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "review", "--name", "reviewer")
	if err == nil {
		t.Fatalf("a derived Pane name collision succeeded:\n%s", store.snapshot())
	}
	if !IsUsageError(err) {
		t.Fatalf("a derived Pane name collision is not a usage error: %v", err)
	}
	if stdout != "" {
		t.Fatalf("a derived Pane name collision wrote %q to stdout", stdout)
	}
	if store.snapshot() != before {
		t.Fatalf("a derived Pane name collision mutated the registry:\n%s", store.snapshot())
	}
	if store.writes != 0 {
		t.Fatalf("registry writes = %d, want 0", store.writes)
	}
	// No suffix was invented, and no uid the rolled-back transaction minted was
	// published into the committed registry.
	if strings.Contains(store.snapshot(), "reviewer-pane-1") || strings.Contains(store.snapshot(), "reviewer-pane-pane") {
		t.Fatal("a derived Pane name collision fell back to a second derivation")
	}
	for _, uid := range store.newUIDs {
		if strings.Contains(store.snapshot(), uid) {
			t.Fatalf("minted uid %q was published despite the refusal", uid)
		}
	}
	if tmuxMutationCallCount(tmux) != 0 || tmux.paneCount() != 0 {
		t.Fatalf("a derived Pane name collision created %d panes", tmux.paneCount())
	}
	// `PlanAgentLaunch` is pure argv construction and starts nothing; the
	// binding is the step that means a provider process exists.
	if len(launcher.bound) != 0 || len(launcher.startupTimeouts) != 0 {
		t.Fatal("a derived Pane name collision reached the provider launch")
	}
}

// TestAgentAndPaneRenamesStayIndependentAfterTheDerivation is acceptance
// criteria 3 and 4.
//
// The derivation runs once, at create. It is deliberately not an invariant:
// making it one would give the Pane name two owners, and would force
// `rename agent` off its `cardinality=exact-one` effect tuple.
func TestAgentAndPaneRenamesStayIndependentAfterTheDerivation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "review", "--name", "reviewer"); err != nil {
		t.Fatalf("create agent error = %v", err)
	}
	agent, pane := agentAndPaneNames(t, store, "win-alpha-review")
	if pane.Metadata.Name != "reviewer-pane" {
		t.Fatalf("derived pane name = %q, want reviewer-pane", pane.Metadata.Name)
	}

	rename := newTestRenameCommand(store)

	// A launcher that still issues the documented follow-up rename to the very
	// name the derivation already produced must keep working. `reserveExplicitName`
	// guards with `owner != uid`, so this is a successful no-op, not a conflict.
	t.Run("the documented follow-up rename to the same name is a no-op", func(t *testing.T) {
		if _, _, err := runRoute(t, rename,
			"pane", "reviewer-pane", "--project", "alpha", "--window", "review", "--name", "reviewer-pane"); err != nil {
			t.Fatalf("re-renaming the Pane to its derived name = %v, want success", err)
		}
		_, after := agentAndPaneNames(t, store, "win-alpha-review")
		if after.Metadata.Name != "reviewer-pane" || after.Metadata.UID != pane.Metadata.UID {
			t.Fatalf("the no-op rename moved the Pane: %+v", after.Metadata)
		}
	})

	// Criterion 3: an explicit `rename pane` is neither forbidden nor
	// restricted, and it does not follow the Agent name.
	t.Run("rename pane still works and does not follow the Agent", func(t *testing.T) {
		if _, _, err := runRoute(t, rename,
			"pane", "reviewer-pane", "--project", "alpha", "--window", "review", "--name", "build"); err != nil {
			t.Fatalf("rename pane error = %v", err)
		}
		gotAgent, gotPane := agentAndPaneNames(t, store, "win-alpha-review")
		if gotPane.Metadata.Name != "build" {
			t.Fatalf("pane name = %q, want build", gotPane.Metadata.Name)
		}
		if gotAgent.Metadata.Name != "reviewer" {
			t.Fatalf("rename pane changed the Agent name to %q", gotAgent.Metadata.Name)
		}
	})

	// Criterion 4: `rename agent` still touches exactly one resource. Its
	// receipt counts the Agent and nothing else, so the effect tuple stays
	// `cardinality=exact-one` and the Pane name is untouched.
	t.Run("rename agent touches exactly one resource and never the Pane", func(t *testing.T) {
		stdout, _, err := runRoute(t, rename,
			"agent", "reviewer", "--project", "alpha", "--window", "review", "--name", "auditor")
		if err != nil {
			t.Fatalf("rename agent error = %v", err)
		}
		if !strings.Contains(stdout, "projects=0 windows=0 panes=0 agents=1") {
			t.Fatalf("rename agent receipt = %q, want it to count exactly one Agent and zero Panes", stdout)
		}
		gotAgent, gotPane := agentAndPaneNames(t, store, "win-alpha-review")
		if gotAgent.Metadata.Name != "auditor" {
			t.Fatalf("agent name = %q, want auditor", gotAgent.Metadata.Name)
		}
		if gotPane.Metadata.Name != "build" {
			t.Fatalf("rename agent propagated onto the Pane name: %q, want build", gotPane.Metadata.Name)
		}
		if gotAgent.Status.PaneRef != gotPane.Metadata.UID {
			t.Fatalf("rename agent moved the managed Pane binding: %q", gotAgent.Status.PaneRef)
		}
	})

	// And the reverse direction does not exist either: nothing derives an Agent
	// name from a Pane name.
	if agent.Metadata.UID == "" {
		t.Fatal("fixture lost the Agent uid")
	}
}
