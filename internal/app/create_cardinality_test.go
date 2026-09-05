package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The three fixture Projects this file addresses, spelled once so a table row
// reads as a cardinality rather than as a uid.
const (
	// cardinalityMultiProject owns two Windows: win-alpha-main (its
	// spec.primaryWindowRef) and win-alpha-review.
	cardinalityMultiProject = "alpha"
	// cardinalitySingleProject owns exactly one Window, so the fan-out and the
	// primary selection coincide there.
	cardinalitySingleProject = "beta"
	// cardinalityEmptyProject is grafted onto the fixture by
	// withEmptyProject and owns no Window at all.
	cardinalityEmptyProject = "empty"
)

// withEmptyProject grafts a Window-less Project onto the shared fixture.
//
// A Project with no Windows is a legal registry state -- validate.go only
// requires a primaryWindowRef when the Project owns Windows -- and it is the
// zero row of the cardinality table: every spelling of a child create must
// refuse there rather than succeed having created nothing.
func withEmptyProject(t *testing.T, store *fakeResourceStore) *fakeResourceStore {
	t.Helper()
	store.registry.Projects = append(store.registry.Projects, coremetadata.Project{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{
			UID: "prj-empty", Name: cardinalityEmptyProject, CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.ProjectSpec{Root: "/srv/empty"},
	})
	store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
		Kind: coremetadata.KindProject, Name: cardinalityEmptyProject, UID: "prj-empty",
	})
	store.dirs["/srv/empty"] = true
	if err := store.registry.Normalize().Validate(); err != nil {
		t.Fatalf("the Window-less Project fixture is not a valid registry: %v", err)
	}
	return store
}

// withDanglingPrimary points one Project's spec.primaryWindowRef at a Window
// that is not there.
//
// The registry file this models is not one Projmux writes, which is the point:
// `--primary-window` reads a stored pointer, and a stored pointer that has gone
// stale must refuse before the transaction rather than resolve to nothing
// halfway through it.
func withDanglingPrimary(store *fakeResourceStore, projectUID, ref string) *fakeResourceStore {
	for i := range store.registry.Projects {
		if store.registry.Projects[i].Metadata.UID == projectUID {
			store.registry.Projects[i].Spec.PrimaryWindowRef = ref
		}
	}
	return store
}

// createReceipt runs one create route with `-o receipt` and decodes the result.
func createReceipt(t *testing.T, create *createCommand, args ...string) (cli.OperationReceipt, string) {
	t.Helper()
	stdout, stderr, err := runRoute(t, create, append(args, "-o", "receipt")...)
	if err != nil {
		t.Fatalf("create %v error = %v", args, err)
	}
	var receipt cli.OperationReceipt
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("decode receipt %q: %v", stdout, err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("create %v emitted a receipt its own route does not allow: %v", args, err)
	}
	return receipt, stderr
}

// TestChildCreateTargetCardinalityTable is the confirmed cardinality contract of
// the shared target planner, over zero, one, and several Windows.
//
// The three rows that matter are the default one and its two explicit
// spellings: `--project P` alone resolves exactly spec.primaryWindowRef, which
// is what `--primary-window` spells out, and `--all-windows` is the
// whole-Project fan-out that scope used to resolve. The selected set is read
// from the receipt because the receipt is what the planner publishes.
func TestChildCreateTargetCardinalityTable(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		args     []string
		selected []string
	}{
		{
			name:     "--project alone is the exact-one primary Window",
			args:     []string{"pane", "--project", cardinalityMultiProject},
			selected: []string{"win-alpha-main"},
		},
		{
			name:     "--all-windows is the opt-in whole-Project fan-out",
			args:     []string{"pane", "--project", cardinalityMultiProject, "--all-windows"},
			selected: []string{"win-alpha-main", "win-alpha-review"},
		},
		{
			name:     "--primary-window spells out what --project alone means",
			args:     []string{"pane", "--project", cardinalityMultiProject, "--primary-window"},
			selected: []string{"win-alpha-main"},
		},
		{
			name:     "explicit refs still dedupe in argv order",
			args:     []string{"pane", "--project", cardinalityMultiProject, "--window", "review", "--window", "review"},
			selected: []string{"win-alpha-review"},
		},
		{
			name:     "a one-Window Project resolves its only Window, which is its primary",
			args:     []string{"pane", "--project", cardinalitySingleProject},
			selected: []string{"win-beta-main"},
		},
		{
			// The fan-out and the primary selection coincide here, which is
			// why a one-Window Project cannot tell the three spellings apart
			// and the multi-Window rows above are the ones that fix the
			// contract.
			name:     "a one-Window Project spelled --all-windows says the same thing",
			args:     []string{"pane", "--project", cardinalitySingleProject, "--all-windows"},
			selected: []string{"win-beta-main"},
		},
		{
			name:     "a one-Window Project spelled --primary-window says the same thing",
			args:     []string{"pane", "--project", cardinalitySingleProject, "--primary-window"},
			selected: []string{"win-beta-main"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())

			receipt, stderr := createReceipt(t, create, test.args...)
			if !slicesEqualStrings(receipt.SelectedWindowUIDs, test.selected) {
				t.Fatalf("selectedWindowUIDs = %v, want %v", receipt.SelectedWindowUIDs, test.selected)
			}
			// The compatibility release is over: the default is now the
			// behavior the notice named, so no spelling of a child create
			// carries a warning on either channel.
			if len(receipt.CompatibilityWarnings) != 0 {
				t.Fatalf("compatibilityWarnings = %v, want none", receipt.CompatibilityWarnings)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want 0 bytes", stderr)
			}
		})
	}
}

// TestChildCreateZeroWindowCardinalityRefusesWithZeroWrites is the zero row of
// the same table. Every spelling refuses, and none of them costs a write.
func TestChildCreateZeroWindowCardinalityRefusesWithZeroWrites(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			// The default is the primary selection now, so the empty Project
			// refuses on the missing pointer rather than on a bare no-match,
			// and the refusal names --project because that is what was typed.
			name: "--project alone refuses when there is no primary Window",
			args: []string{"pane", "--project", cardinalityEmptyProject},
			want: "--project: project/empty has no spec.primaryWindowRef",
		},
		{
			name: "--all-windows refuses on an empty Project",
			args: []string{"pane", "--project", cardinalityEmptyProject, "--all-windows"},
			want: "matched no windows",
		},
		{
			name: "--primary-window refuses when there is no primary Window",
			args: []string{"pane", "--project", cardinalityEmptyProject, "--primary-window"},
			want: "has no spec.primaryWindowRef",
		},
		{
			name: "--all-windows refuses for an Agent too",
			args: []string{"agent", "--provider", "claude", "--project", cardinalityEmptyProject, "--all-windows"},
			want: "matched no agents",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := withEmptyProject(t, newFakeResourceStore(t))
			before := store.snapshot()
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, test.args...)
			assertPreflightRefusal(t, err, stdout, test.want, store, before, tmux, launcher)
		})
	}
}

// TestChildCreateCardinalityFlagConflictsAreUsageErrors pins every mutual
// exclusion of the two new flags.
//
// Each pair asks for two different target sets in one invocation, so there is
// no precedence rule that could answer it. The refusal is a UsageError, which
// cmd/projmux maps to exit code 2, and it lands inside the parser -- before the
// projection, the scope derivation, and the transaction -- so it costs zero
// Registry writes, zero tmux objects, and zero provider calls.
func TestChildCreateCardinalityFlagConflictsAreUsageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "--all-windows against --window",
			args: []string{"pane", "--project", "alpha", "--all-windows", "--window", "main"},
			want: "--all-windows already fixes the target Window set and cannot be combined with --window",
		},
		{
			name: "--all-windows against --pane",
			args: []string{"pane", "--project", "alpha", "--all-windows", "--pane", "zsh"},
			want: "cannot be combined with --pane",
		},
		{
			name: "--all-windows against --selector",
			args: []string{"pane", "--project", "alpha", "--all-windows", "--selector", "role=shell"},
			want: "cannot be combined with --selector",
		},
		{
			name: "--all-windows against --create-window",
			args: []string{"pane", "--project", "alpha", "--all-windows", "--create-window", "--window", "spawn"},
			want: "cannot be combined with",
		},
		{
			name: "--all-windows against --primary-window",
			args: []string{"pane", "--project", "alpha", "--all-windows", "--primary-window"},
			want: "--all-windows and --primary-window select different target sets",
		},
		{
			name: "--primary-window against --window",
			args: []string{"pane", "--project", "alpha", "--primary-window", "--window", "main"},
			want: "--primary-window already fixes the target Window set and cannot be combined with --window",
		},
		{
			name: "--primary-window against --pane",
			args: []string{"pane", "--project", "alpha", "--primary-window", "--pane", "zsh"},
			want: "cannot be combined with --pane",
		},
		{
			name: "--primary-window against --selector",
			args: []string{"pane", "--project", "alpha", "--primary-window", "--selector", "role=shell"},
			want: "cannot be combined with --selector",
		},
		{
			name: "--primary-window against --create-window",
			args: []string{"pane", "--project", "alpha", "--primary-window", "--create-window", "--window", "spawn"},
			want: "cannot be combined with",
		},
		{
			name: "the Agent route shares the exclusions",
			args: []string{"agent", "--provider", "claude", "--project", "alpha", "--all-windows", "--window", "main"},
			want: "cannot be combined with --window",
		},
		{
			name: "so does a provider shortcut",
			args: []string{"codex", "--project", "alpha", "--primary-window", "--selector", "role=shell"},
			want: "cannot be combined with --selector",
		},
		{
			// `create window` creates exactly one Window and therefore has no
			// target Window set to fix. The flags are not registered there at
			// all, which is still exit 2.
			name: "create window does not register the cardinality flags",
			args: []string{"window", "--project", "alpha", "--all-windows"},
			want: "flag provided but not defined",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, test.args...)
			assertPreflightRefusal(t, err, stdout, test.want, store, before, tmux, launcher)
		})
	}
}

// TestChildCreatePreflightRefusalsCostNothing covers the remaining refusals the
// new paths can raise, on the same zero-write assertion.
func TestChildCreatePreflightRefusalsCostNothing(t *testing.T) {
	t.Parallel()

	t.Run("a dangling primaryWindowRef refuses", func(t *testing.T) {
		t.Parallel()
		store := withDanglingPrimary(newFakeResourceStore(t), "prj-alpha", "win-vanished")
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "pane", "--project", "alpha", "--primary-window")
		assertPreflightRefusal(t, err, stdout, "is dangling or owned by another Project", store, before, tmux, launcher)
	})

	t.Run("a dangling primaryWindowRef refuses through the --project default too", func(t *testing.T) {
		t.Parallel()
		// The default spelling shares the flag's preflight, so it shares the
		// refusal -- and the message names --project rather than a flag the
		// operator never typed.
		store := withDanglingPrimary(newFakeResourceStore(t), "prj-alpha", "win-vanished")
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "pane", "--project", "alpha")
		assertPreflightRefusal(t, err,
			stdout, "create pane --project: project/alpha spec.primaryWindowRef", store, before, tmux, launcher)
	})

	t.Run("a cross-root primaryWindowRef refuses", func(t *testing.T) {
		t.Parallel()
		// win-beta-main exists, but it belongs to project/beta.
		store := withDanglingPrimary(newFakeResourceStore(t), "prj-alpha", "win-beta-main")
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "agent", "--provider", "claude", "--project", "alpha", "--primary-window")
		assertPreflightRefusal(t, err, stdout, "is dangling or owned by another Project", store, before, tmux, launcher)
	})

	t.Run("an explicit --name cannot address a multi-Window --all-windows set", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create,
			"pane", "--project", "alpha", "--all-windows", "--name", "solo")
		assertPreflightRefusal(t, err, stdout, "", store, before, tmux, launcher)
	})
}

// assertPreflightRefusal is the shared "exit 2 and nothing happened" assertion:
// a usage error, zero bytes on stdout, a byte-identical Registry with zero
// committed writes, zero tmux objects, and zero provider launches.
func assertPreflightRefusal(
	t *testing.T,
	err error,
	stdout, want string,
	store *fakeResourceStore,
	before string,
	tmux *fakeTmux,
	launcher *fakeAgentLauncher,
) {
	t.Helper()
	if err == nil {
		t.Fatal("the refusal succeeded")
	}
	if !IsUsageError(err) {
		t.Fatalf("error is not a usage error (exit 2): %v", err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to mention %q", err, want)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.snapshot() != before || store.writes != 0 {
		t.Fatalf("a refusal mutated the Registry: writes=%d", store.writes)
	}
	if tmuxMutationCallCount(tmux) != 0 || tmux.paneCount() != 0 || len(tmux.sessions) != 0 {
		t.Fatalf("a refusal mutated tmux:\n%s", tmux.state())
	}
	// The provider assertion is over launches, not over launch *construction*:
	// `create agent` deliberately builds the argv before it allocates anything,
	// so that a missing provider binary fails while the operation still owns
	// nothing. What must stay at zero is every effect that reaches the provider
	// -- the managed-pane binding and the activation probe.
	if launcher != nil && (len(launcher.bound) != 0 || len(launcher.activationPanes) != 0) {
		t.Fatalf("a refusal reached the provider: bound=%+v activations=%q",
			launcher.bound, launcher.activationPanes)
	}
}

// TestChildCreateSelectedWindowSetIsIdenticalAcrossProviders is the "provider
// 별 selected set drift 0" acceptance item.
//
// The same Window scope, spelled the same way, must produce the same
// selectedWindowUIDs through `create pane`, the canonical `create agent`, and
// each provider shortcut. It holds because the receipt quotes the shared target
// planner rather than the results, so a route that produces Agents instead of
// Panes has nothing of its own to disagree with.
func TestChildCreateSelectedWindowSetIsIdenticalAcrossProviders(t *testing.T) {
	t.Parallel()

	routes := [][]string{
		{"pane"},
		{"agent", "--provider", "codex", "--interactive-only"},
		{"agent", "--provider", "claude"},
		{"codex", "--interactive-only"},
		{"claude"},
		{"antigravity"},
	}

	for _, scope := range [][]string{
		{"--project", cardinalityMultiProject},
		{"--project", cardinalityMultiProject, "--all-windows"},
		{"--project", cardinalityMultiProject, "--primary-window"},
		{"--project", cardinalitySingleProject, "--all-windows"},
	} {
		t.Run(strings.Join(scope, " "), func(t *testing.T) {
			t.Parallel()
			var want []string
			var wantWarnings []string
			for _, route := range routes {
				store := newFakeResourceStore(t)
				create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				args := append(append([]string(nil), route...), scope...)

				receipt, _ := createReceipt(t, create, args...)
				if want == nil {
					want, wantWarnings = receipt.SelectedWindowUIDs, receipt.CompatibilityWarnings
					continue
				}
				if !slicesEqualStrings(receipt.SelectedWindowUIDs, want) {
					t.Fatalf("%v selectedWindowUIDs = %v, want the %v set %v",
						route, receipt.SelectedWindowUIDs, routes[0], want)
				}
				if !slicesEqualStrings(receipt.CompatibilityWarnings, wantWarnings) {
					t.Fatalf("%v compatibilityWarnings = %v, want %v",
						route, receipt.CompatibilityWarnings, wantWarnings)
				}
			}
		})
	}
}

// TestProjectOnlyOmissionIsThePrimaryWindowDefault is the cutover guarantee of
// this release: the spelling the previous release warned about now resolves the
// exact-one Window that warning named, and the set it used to resolve is still
// reachable, unchanged, under the flag that warning named for it.
//
// Nothing here is silent. The narrowing is the one the notice announced, the
// wide set is preserved verbatim behind `--all-windows`, and the two spellings
// that were never part of the compatibility surface -- a wholly omitted scope
// and an explicit `--primary-window` -- keep the meanings they had.
func TestProjectOnlyOmissionIsThePrimaryWindowDefault(t *testing.T) {
	t.Parallel()

	t.Run("the omission is byte-identical to --primary-window", func(t *testing.T) {
		t.Parallel()
		omitted := newFakeResourceStore(t)
		omittedCreate, _ := newTestResourceCreateCommand(t, omitted, newFakeTmux())
		explicit := newFakeResourceStore(t)
		explicitCreate, _ := newTestResourceCreateCommand(t, explicit, newFakeTmux())

		omittedStdout, omittedStderr, err := runRoute(t, omittedCreate, "pane", "--project", "alpha")
		if err != nil {
			t.Fatalf("--project-only error = %v", err)
		}
		explicitStdout, explicitStderr, err := runRoute(t, explicitCreate, "pane", "--project", "alpha", "--primary-window")
		if err != nil {
			t.Fatalf("--primary-window error = %v", err)
		}
		if omittedStdout != explicitStdout {
			t.Fatalf("stdout drifted: omitted=%q primary-window=%q", omittedStdout, explicitStdout)
		}
		if strings.Count(omittedStdout, "pane/") != 1 {
			t.Fatalf("the --project-only create did not produce exactly one Pane: %q", omittedStdout)
		}
		if omittedStderr != "" || explicitStderr != "" {
			t.Fatalf("stderr = %q / %q, want 0 bytes on both", omittedStderr, explicitStderr)
		}
		if omitted.snapshot() != explicit.snapshot() {
			t.Fatal("the two spellings of the primary selection produced different Registries")
		}
	})

	t.Run("the old fan-out is preserved verbatim under --all-windows", func(t *testing.T) {
		t.Parallel()
		// This is the "silent target 확대/축소 0" assertion in observable form:
		// the wide set is still exactly both Windows, the narrowed default is
		// exactly the primary one, and the difference between them is the
		// single Window the deprecation notice said would drop out.
		wide := newFakeResourceStore(t)
		wideCreate, _ := newTestResourceCreateCommand(t, wide, newFakeTmux())
		wideBefore := paneUIDsByWindow(wide)
		wideReceipt, wideStderr := createReceipt(t, wideCreate, "pane", "--project", "alpha", "--all-windows")
		wideAdded := addedPaneUIDs(wideBefore, paneUIDsByWindow(wide))

		narrow := newFakeResourceStore(t)
		narrowCreate, _ := newTestResourceCreateCommand(t, narrow, newFakeTmux())
		narrowBefore := paneUIDsByWindow(narrow)
		narrowReceipt, narrowStderr := createReceipt(t, narrowCreate, "pane", "--project", "alpha")
		narrowAdded := addedPaneUIDs(narrowBefore, paneUIDsByWindow(narrow))

		if len(wideAdded["win-alpha-main"]) != 1 || len(wideAdded["win-alpha-review"]) != 1 {
			t.Fatalf("--all-windows stopped being the whole-Project fan-out: %v", wideAdded)
		}
		if len(narrowAdded["win-alpha-main"]) != 1 || len(narrowAdded["win-alpha-review"]) != 0 {
			t.Fatalf("--project alone targeted %v, want only the primary Window", narrowAdded)
		}
		if !slicesEqualStrings(wideReceipt.SelectedWindowUIDs, []string{"win-alpha-main", "win-alpha-review"}) {
			t.Fatalf("--all-windows selected %v", wideReceipt.SelectedWindowUIDs)
		}
		if !slicesEqualStrings(narrowReceipt.SelectedWindowUIDs, []string{"win-alpha-main"}) {
			t.Fatalf("--project alone selected %v", narrowReceipt.SelectedWindowUIDs)
		}
		if wideStderr != "" || narrowStderr != "" {
			t.Fatalf("stderr = %q / %q, want 0 bytes on both", wideStderr, narrowStderr)
		}
	})

	t.Run("an omitted scope inside tmux still targets the active Window", func(t *testing.T) {
		t.Parallel()
		// The natural-omitted contract is untouched by the cutover: with no
		// --project at all the create resolves the Window the operator is
		// looking at, which is neither the fan-out nor the primary selection.
		store, tmux := aliveAlphaRuntime(t)
		withDanglingPrimary(store, "prj-alpha", "win-alpha-review")
		seedLiveWindow(t, tmux, tmux.session("alpha"), "win-alpha-review", "pan-alpha-review")
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))

		before := paneUIDsByWindow(store)
		_, stderr, err := runRoute(t, create, "pane")
		if err != nil {
			t.Fatalf("natural-omitted create error = %v", err)
		}
		if stderr != "" {
			t.Fatalf("the natural-omitted create wrote to stderr: %q", stderr)
		}
		added := addedPaneUIDs(before, paneUIDsByWindow(store))
		if len(added["win-alpha-main"]) != 1 || len(added["win-alpha-review"]) != 0 {
			t.Fatalf("the natural-omitted create changed shape: %v", added)
		}
	})

	t.Run("a --project scope inside tmux takes the primary, not the active Window", func(t *testing.T) {
		t.Parallel()
		// The distinguishing fixture: the operator sits in win-alpha-main and
		// the Project's primary is the other Window. Typing --project is what
		// makes the whole scope explicit, so the create must follow the stored
		// primary rather than blend with the runtime it was invoked from.
		store, tmux := aliveAlphaRuntime(t)
		withDanglingPrimary(store, "prj-alpha", "win-alpha-review")
		seedLiveWindow(t, tmux, tmux.session("alpha"), "win-alpha-review", "pan-alpha-review")
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))

		before := paneUIDsByWindow(store)
		if _, stderr, err := runRoute(t, create, "pane", "--project", "alpha"); err != nil {
			t.Fatalf("--project-only create error = %v (stderr %q)", err, stderr)
		}
		added := addedPaneUIDs(before, paneUIDsByWindow(store))
		if len(added["win-alpha-review"]) != 1 || len(added["win-alpha-main"]) != 0 {
			t.Fatalf("--project alone targeted %v, want only the primary Window", added)
		}
	})

	t.Run("--primary-window inside tmux replaces the active Window", func(t *testing.T) {
		t.Parallel()
		// The active Pane is in win-alpha-main and the Project's primary Window
		// is win-alpha-main too, so the distinguishing fixture points the
		// primary at the other Window: an explicit cardinality flag must fix
		// the target rather than blend with the runtime the operator sits in.
		store, tmux := aliveAlphaRuntime(t)
		withDanglingPrimary(store, "prj-alpha", "win-alpha-review")
		seedLiveWindow(t, tmux, tmux.session("alpha"), "win-alpha-review", "pan-alpha-review")
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		withActiveTarget(create, insideTmux("pan-alpha-zsh", "win-alpha-main"))

		before := paneUIDsByWindow(store)
		if _, stderr, err := runRoute(t, create, "pane", "--primary-window"); err != nil {
			t.Fatalf("--primary-window error = %v (stderr %q)", err, stderr)
		}
		added := addedPaneUIDs(before, paneUIDsByWindow(store))
		if len(added["win-alpha-review"]) != 1 || len(added["win-alpha-main"]) != 0 {
			t.Fatalf("--primary-window targeted %v, want only the primary Window", added)
		}
	})
}

// TestChildCreateCardinalityFlagsMaterializeAndRollBack keeps the new spellings
// on the shared runtime path rather than on a private one.
func TestChildCreateCardinalityFlagsMaterializeAndRollBack(t *testing.T) {
	t.Parallel()

	t.Run("an offline Project is materialized detached", func(t *testing.T) {
		t.Parallel()
		for _, spelling := range []string{"--all-windows", "--primary-window"} {
			t.Run(spelling, func(t *testing.T) {
				t.Parallel()
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				create, sessions := newTestResourceCreateCommand(t, store, tmux)

				// project/beta's session projection is not live, so this create
				// has to ensure the session before it can split anything.
				if _, _, err := runRoute(t, create, "pane", "--project", "beta", spelling); err != nil {
					t.Fatalf("offline %s error = %v", spelling, err)
				}
				if len(sessions.created) != 1 || sessions.created[0] != "beta" {
					t.Fatalf("offline materialization created %v, want exactly [beta]", sessions.created)
				}
				assertNoClientMovement(t, tmux)
			})
		}
	})

	t.Run("a failed split rolls the whole fan-out back", func(t *testing.T) {
		t.Parallel()
		for _, spelling := range []string{"--all-windows", "--primary-window"} {
			t.Run(spelling, func(t *testing.T) {
				t.Parallel()
				store := newFakeResourceStore(t)
				before := store.snapshot()
				tmux := newFakeTmux()
				tmux.fail = []string{"split-window"}
				tmux.failMessage = "split-window: no space for new pane"
				create, _ := newTestResourceCreateCommand(t, store, tmux)

				if _, _, err := runRoute(t, create, "pane", "--project", "alpha", spelling); err == nil {
					t.Fatalf("the failed split of %s reported success", spelling)
				}
				if store.snapshot() != before || store.writes != 0 {
					t.Fatalf("a failed %s left Registry residue: writes=%d", spelling, store.writes)
				}
				if tmux.paneCount() != 0 {
					t.Fatalf("a failed %s left tmux residue:\n%s", spelling, tmux.state())
				}
			})
		}
	})

	t.Run("a provider refusal on the new paths reaches nothing", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)
		launcher.disabled = map[string]bool{"claude": true}

		stdout, _, err := runRoute(t, create,
			"claude", "--project", "alpha", "--all-windows")
		if err == nil {
			t.Fatal("a disabled provider still created an Agent")
		}
		if stdout != "" || store.snapshot() != before || store.writes != 0 || len(launcher.plans) != 0 {
			t.Fatalf("the Settings gate cost something: stdout=%q writes=%d plans=%+v",
				stdout, store.writes, launcher.plans)
		}
	})
}

// projectScopedChildCreate matches a repository call site that scopes a child
// create with --project and then leaves the target cardinality implicit.
var projectScopedChildCreate = regexp.MustCompile(`create\s+(pane|agent|codex|claude|antigravity)\b`)

// windowCardinalitySpellings are the tokens that make a child create's target
// Window set explicit.
var windowCardinalitySpellings = []string{
	"--window", "-w ", "--pane ", "--selector", "--create-window", "--all-windows", "--primary-window",
}

// cardinalityDefaultFixtureMarker exempts one shell invocation from the audit
// below.
//
// It exists for the fixtures whose subject *is* the implicit spelling: a smoke
// that proves `--project P` alone resolves the primary Window has to type
// `--project P` alone. The marker is per-line and has to be written out, so an
// exemption is a visible decision rather than a silence.
const cardinalityDefaultFixtureMarker = "projmux-cardinality-default-fixture"

// TestRepositoryCallSitesSpellTheirChildCreateCardinality is the generated
// config / script migration gate.
//
// Every executable call site the repository owns -- generated config, hooks,
// smoke scripts, fixtures -- has to say which Windows it means, because those
// are exactly the invocations nobody will be around to re-read the next time
// the default moves. Prose that documents the implicit spelling is deliberately
// out of scope: docs/cli-guide.md has to be able to show it, and a fixture that
// tests the default opts out with cardinalityDefaultFixtureMarker.
func TestRepositoryCallSitesSpellTheirChildCreateCardinality(t *testing.T) {
	t.Parallel()

	root := repoRootForTest(t)
	var offenders []string
	for _, dir := range []string{"scripts", "test", "npm"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".sh" {
				return err
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for number, line := range projectScopedInvocations(string(raw)) {
				offenders = append(offenders, path+":"+number+": "+line)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("these call sites scope a child create with --project but leave the target cardinality implicit; "+
			"spell them --all-windows or --primary-window:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// projectScopedInvocations returns the compatibility-spelled child creates in
// one shell script, keyed by line number. Backslash continuations are joined
// first, because the --window occurrence that makes an invocation explicit is
// routinely on the next physical line.
func projectScopedInvocations(script string) map[string]string {
	lines := strings.Split(script, "\n")
	out := map[string]string{}
	for i := 0; i < len(lines); i++ {
		start, joined := i, lines[i]
		for strings.HasSuffix(strings.TrimRight(joined, " \t"), `\`) && i+1 < len(lines) {
			i++
			joined = strings.TrimSuffix(strings.TrimRight(joined, " \t"), `\`) + " " + lines[i]
		}
		if !projectScopedChildCreate.MatchString(joined) || !strings.Contains(joined, "--project") {
			continue
		}
		if strings.Contains(joined, cardinalityDefaultFixtureMarker) {
			continue
		}
		explicit := false
		for _, token := range windowCardinalitySpellings {
			if strings.Contains(joined, token) {
				explicit = true
				break
			}
		}
		if !explicit {
			out[strconv.Itoa(start+1)] = strings.TrimSpace(joined)
		}
	}
	return out
}

func slicesEqualStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
