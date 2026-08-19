package app

import (
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// TestResourceAgeCellBoundaries is the relative-time boundary table.
//
// Every unit change is pinned from both sides -- the last second that still
// renders the smaller unit and the first that rolls over -- because a rounding
// or a `<=` slip is invisible in the middle of a range and obvious at its edge.
// The empty-cell rules are in the same table rather than a separate test, so
// "what does AGE show" has exactly one place to read.
func TestResourceAgeCellBoundaries(t *testing.T) {
	t.Parallel()

	const uid = "prj-alpha"
	created := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "created this instant", elapsed: 0, want: "0s"},
		{name: "sub-second still reads as zero seconds", elapsed: 999 * time.Millisecond, want: "0s"},
		{name: "seconds", elapsed: 36 * time.Second, want: "36s"},
		{name: "the last second before a minute", elapsed: 59 * time.Second, want: "59s"},
		{name: "the first minute", elapsed: time.Minute, want: "1m"},
		{name: "minutes", elapsed: 12 * time.Minute, want: "12m"},
		{name: "the last minute before an hour", elapsed: 59*time.Minute + 59*time.Second, want: "59m"},
		{name: "the first hour", elapsed: time.Hour, want: "1h"},
		{name: "hours", elapsed: 5 * time.Hour, want: "5h"},
		{name: "the last hour before a day", elapsed: 23*time.Hour + 59*time.Minute, want: "23h"},
		{name: "the first day", elapsed: 24 * time.Hour, want: "1d"},
		{name: "days", elapsed: 3*24*time.Hour + 7*time.Hour, want: "3d"},
		{name: "very old resources keep counting in days", elapsed: 900 * 24 * time.Hour, want: "900d"},
		// A registry stamped by a machine whose clock has since moved backwards
		// reads as an age in the future. It clamps to zero rather than printing
		// a negative token; the alternative is a `-4h` cell in a width-measured
		// column.
		{name: "a clock behind the creation stamp clamps to zero", elapsed: -4 * time.Hour, want: "0s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry := ageFixtureRegistry(t, created)
			got := resourceAgeCell(registry, coremetadata.KindProject, uid, created.Add(test.elapsed))
			if got != test.want {
				t.Fatalf("age after %s = %q, want %q", test.elapsed, got, test.want)
			}
		})
	}

	// The three cases that render nothing at all.
	for _, test := range []struct {
		name     string
		registry coremetadata.Registry
		uid      string
		now      time.Time
	}{
		{
			name:     "a uid that is no longer in the registry",
			registry: ageFixtureRegistry(t, created),
			uid:      "prj-vanished",
			now:      created.Add(time.Hour),
		},
		{
			name:     "a resource stored before createdAt was stamped",
			registry: ageFixtureRegistry(t, time.Time{}),
			uid:      uid,
			now:      created.Add(time.Hour),
		},
		{
			name:     "a caller that passed no clock",
			registry: ageFixtureRegistry(t, created),
			uid:      uid,
			now:      time.Time{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resourceAgeCell(test.registry, coremetadata.KindProject, test.uid, test.now); got != "" {
				t.Fatalf("age = %q, want an empty cell", got)
			}
		})
	}
}

// ageFixtureRegistry is a one-Project registry whose creation stamp the caller
// chooses, including the zero Time a pre-createdAt registry file decodes to.
func ageFixtureRegistry(t *testing.T, created time.Time) coremetadata.Registry {
	t.Helper()
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindProject,
		Metadata:   coremetadata.ObjectMeta{UID: "prj-alpha", Name: "alpha", CreatedAt: created},
		Spec:       coremetadata.ProjectSpec{Root: "/srv/alpha"},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Kind: coremetadata.KindProject, Name: "alpha", UID: "prj-alpha"},
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("age fixture is not a valid registry: %v", err)
	}
	return registry
}

// TestGetListAgeColumnRendersEveryUnitInOneRead is acceptance criterion 1 and 3
// at the surface an operator actually uses.
//
// The fixture is restamped so one read spans all four units at once: a Pane
// created seconds ago next to one created days ago, in the same table, with the
// AGE column measured against one injected clock. A per-row assertion would not
// catch it, but a whole-table golden does: the widest cell sets the column, so a
// spread of ages is also the alignment case.
func TestGetListAgeColumnRendersEveryUnitInOneRead(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	restampFixtureCreatedAt(t, store, "pan-alpha-zsh", resourceFixtureReadClock.Add(-9*time.Second))
	restampFixtureCreatedAt(t, store, "pan-alpha-log", resourceFixtureReadClock.Add(-47*time.Minute))
	restampFixtureCreatedAt(t, store, "pan-alpha-codex", resourceFixtureReadClock.Add(-6*time.Hour))
	restampFixtureCreatedAt(t, store, "pan-alpha-review", resourceFixtureReadClock.Add(-11*24*time.Hour))

	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), "panes", "--project", "alpha")
	if err != nil {
		t.Fatalf("get panes error = %v (stderr %q)", err, stderr)
	}
	const want = "DISPLAY NAME  NAME        STATUS  PROJECT  WINDOW  AGENT  TERMINATION  AGE\n" +
		"zsh           zsh         live    alpha    main                        9s\n" +
		"zsh           log         live    alpha    main                        47m\n" +
		"codex-pane    codex-pane  live    alpha    main    codex               6h\n" +
		"zsh           zsh         live    alpha    review                      11d\n"
	if stdout != want {
		t.Fatalf("get panes stdout =\n%q\nwant\n%q", stdout, want)
	}

	// The parsed rows have to agree with the header offsets too, so a widened
	// AGE column cannot be read as "aligned" merely because the bytes matched.
	for _, row := range columnarRows(t, stdout) {
		if row["AGE"] == "" {
			t.Fatalf("a row parsed an empty AGE column: %v", row)
		}
		if row["STATUS"] != "live" {
			t.Fatalf("a row parsed its STATUS column as %q: %v", row["STATUS"], row)
		}
	}
}

// TestGetListAgeAgreesWithTheStructuredCreatedAt is acceptance criterion 1's
// "the value matches `-o json`" half, asserted rather than eyeballed.
//
// The same read is taken twice through the same clock, once as the columnar
// default and once as `-o json`, and the rendered AGE is recomputed from the
// emitted `createdAt`. That is what makes the column a projection of the stored
// field instead of a second, independently drifting source.
func TestGetListAgeAgreesWithTheStructuredCreatedAt(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	restampFixtureCreatedAt(t, store, "prj-alpha", resourceFixtureReadClock.Add(-30*time.Hour))

	structured, _, err := runRoute(t, newTestListGetCommand(t, store), "projects", "-o", "json")
	if err != nil {
		t.Fatalf("get projects -o json error = %v", err)
	}
	const stamp = `"createdAt": "2026-08-16T05:30:00Z"`
	if !strings.Contains(structured, stamp) {
		t.Fatalf("get projects -o json is missing %s:\n%s", stamp, structured)
	}

	stdout, _, err := runRoute(t, newTestListGetCommand(t, store), "projects")
	if err != nil {
		t.Fatalf("get projects error = %v", err)
	}
	rows := columnarRows(t, stdout)
	if len(rows) == 0 {
		t.Fatalf("get projects produced no rows:\n%s", stdout)
	}
	// 2026-08-16T05:30:00Z to the read clock is 30 hours, which is one day.
	if rows[0]["NAME"] != "alpha" || rows[0]["AGE"] != "1d" {
		t.Fatalf("the alpha row = %v, want AGE 1d to match the emitted createdAt", rows[0])
	}
}

// TestGetListAgeIsPinnedByTheInjectedClock is acceptance criterion 6.
//
// It states determinism as a two-sided claim, because only one side is
// falsifiable on its own. Same clock twice must be byte-identical, which a
// renderer that ignored the clock entirely would also satisfy -- so a different
// clock must produce a different table, which a renderer reading time.Now would
// fail. The pair holds only for a renderer that reads the passed-in clock and
// nothing else.
func TestGetListAgeIsPinnedByTheInjectedClock(t *testing.T) {
	t.Parallel()

	read := func(now time.Time) string {
		store := newFakeResourceStore(t)
		cmd := &getCommand{
			loadRegistry: store.store().load,
			currentPath:  &stubCurrentPath{},
			runtime:      liveAlphaRuntime(),
			now:          func() time.Time { return now },
		}
		stdout, _, err := runRoute(t, cmd, "projects")
		if err != nil {
			t.Fatalf("get projects at %s error = %v", now, err)
		}
		return stdout
	}

	frozen := read(resourceFixtureReadClock)
	if again := read(resourceFixtureReadClock); again != frozen {
		t.Fatalf("the same clock produced two different tables:\n%q\n%q", frozen, again)
	}
	if !strings.Contains(frozen, "2d") {
		t.Fatalf("the frozen read shows no age:\n%s", frozen)
	}
	if later := read(resourceFixtureReadClock.Add(72 * time.Hour)); later == frozen {
		t.Fatalf("advancing the clock by three days changed nothing, so AGE is not read from it:\n%s", frozen)
	}
}

// restampFixtureCreatedAt moves one fixture resource's creation stamp so a read
// can span several age units without inventing a second registry.
func restampFixtureCreatedAt(t *testing.T, store *fakeResourceStore, uid string, created time.Time) {
	t.Helper()
	switch {
	case strings.HasPrefix(uid, "prj-"):
		project, ok := store.registry.Project(uid)
		if !ok {
			t.Fatalf("fixture project %q missing", uid)
		}
		project.Metadata.CreatedAt = created
	case strings.HasPrefix(uid, "win-"):
		window, ok := store.registry.Window(uid)
		if !ok {
			t.Fatalf("fixture window %q missing", uid)
		}
		window.Metadata.CreatedAt = created
	case strings.HasPrefix(uid, "pan-"):
		pane, ok := store.registry.Pane(uid)
		if !ok {
			t.Fatalf("fixture pane %q missing", uid)
		}
		pane.Metadata.CreatedAt = created
	case strings.HasPrefix(uid, "agt-"):
		agent, ok := store.registry.Agent(uid)
		if !ok {
			t.Fatalf("fixture agent %q missing", uid)
		}
		agent.Metadata.CreatedAt = created
	default:
		t.Fatalf("restampFixtureCreatedAt does not know uid %q", uid)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("restamped fixture is not a valid registry: %v", err)
	}
}

// TestDescribeSurfacesTheStoredTimestamps is acceptance criterion 2, per kind.
//
// Every row asserted here is a value the registry already held and no human
// surface printed: the creation instant of each of the four kinds, the Agent's
// phase transition, and the second half of a condition's stored pair.
func TestDescribeSurfacesTheStoredTimestamps(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "project",
			args: []string{"project", "alpha"},
			want: map[string]string{"CreatedAt": "2026-08-15T09:00:00Z"},
		},
		{
			name: "window",
			args: []string{"window", "review", "--project", "alpha"},
			want: map[string]string{"CreatedAt": "2026-08-15T09:00:00Z"},
		},
		{
			name: "pane",
			args: []string{"pane", "log", "--project", "alpha", "--window", "main"},
			want: map[string]string{"CreatedAt": "2026-08-15T09:00:00Z"},
		},
		{
			name: "agent carries its phase transition as well",
			args: []string{"agent", "codex", "--project", "alpha"},
			want: map[string]string{
				"CreatedAt":  "2026-08-15T09:00:00Z",
				"PhaseSince": "2026-08-15T09:00:00Z",
			},
		},
		{
			name: "a condition carries both stored instants",
			args: []string{"project", "gone"},
			want: map[string]string{
				"Condition": "MissingRoot=True reason=RootDisappeared " +
					"firstObservedAt=2026-07-04T17:00:00Z lastTransitionAt=2026-07-04T17:00:00Z",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store), test.args...)
			if err != nil {
				t.Fatalf("describe %v error = %v (stderr %q)", test.args, err, stderr)
			}
			rows := describeRows(t, stdout)
			for key, want := range test.want {
				got := rows[key]
				if len(got) != 1 || got[0] != want {
					t.Fatalf("describe %v row %q = %v, want [%q]\n%s", test.args, key, got, want, stdout)
				}
			}
		})
	}
}

// TestDescribeOmitsTimestampRowsARegistryDoesNotCarry keeps the addition
// backward compatible with registry files written before the fields were
// stamped.
//
// A zero time is what an absent JSON key decodes to, so the alternative to
// omitting the row is dating every pre-existing resource to January of year 1.
func TestDescribeOmitsTimestampRowsARegistryDoesNotCarry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	restampFixtureCreatedAt(t, store, "prj-alpha", time.Time{})
	restampFixtureCreatedAt(t, store, "agt-alpha-codex", time.Time{})
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("fixture agent missing")
	}
	agent.Status.LastTransitionAt = time.Time{}

	for _, test := range []struct {
		args []string
		gone []string
	}{
		{args: []string{"project", "alpha"}, gone: []string{"CreatedAt"}},
		{args: []string{"agent", "codex", "--project", "alpha"}, gone: []string{"CreatedAt", "PhaseSince"}},
	} {
		stdout, _, err := runRoute(t, newTestDescribeCommand(t, store), test.args...)
		if err != nil {
			t.Fatalf("describe %v error = %v", test.args, err)
		}
		rows := describeRows(t, stdout)
		for _, key := range test.gone {
			if got, ok := rows[key]; ok {
				t.Fatalf("describe %v rendered %q = %v from an unstamped registry\n%s", test.args, key, got, stdout)
			}
		}
		// The rest of the block is untouched by the omission.
		if len(rows["Kind"]) != 1 || len(rows["UID"]) != 1 {
			t.Fatalf("describe %v lost its identity rows:\n%s", test.args, stdout)
		}
	}
}

// TestMachineOutputModesAreUnchangedByTheTimestampColumns is acceptance
// criterion 4.
//
// The machine-consumer projections are frozen against the exact bytes they
// emitted before AGE and the describe rows existed. `-o json` is the one that
// could plausibly have moved -- it is rendered from the same ObjectMeta the AGE
// cell reads -- so it is asserted as a whole document rather than by substring:
// a new key anywhere in it reddens this.
func TestMachineOutputModesAreUnchangedByTheTimestampColumns(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"panes", "--project", "beta", "-o", "uid"}, want: "pan-beta-zsh\n"},
		{args: []string{"panes", "--project", "beta", "-o", "name"}, want: "zsh\n"},
		{args: []string{"panes", "--project", "beta", "-o", "ref"}, want: "pane/zsh\n"},
		{args: []string{"panes", "--project", "beta", "-o", "none"}, want: ""},
		{
			args: []string{"windows", "--project", "beta", "-o", "metadata"},
			want: `{
  "apiVersion": "projmux.io/v1alpha1",
  "kind": "WindowMetadataList",
  "items": [
    {
      "uid": "win-beta-main",
      "name": "main",
      "ownerRef": {
        "kind": "Project",
        "uid": "prj-beta"
      },
      "createdAt": "2026-08-15T09:00:00Z"
    }
  ]
}
`,
		},
		{
			args: []string{"windows", "--project", "beta", "-o", "json"},
			want: `{
  "apiVersion": "projmux.io/v1alpha1",
  "kind": "WindowList",
  "items": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Window",
      "metadata": {
        "uid": "win-beta-main",
        "name": "main",
        "ownerRef": {
          "kind": "Project",
          "uid": "prj-beta"
        },
        "createdAt": "2026-08-15T09:00:00Z"
      },
      "spec": {
        "primaryPaneRef": "pan-beta-zsh"
      }
    }
  ]
}
`,
		},
	} {
		store := newFakeResourceStore(t)
		stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), test.args...)
		if err != nil {
			t.Fatalf("get %v error = %v (stderr %q)", test.args, err, stderr)
		}
		if stdout != test.want {
			t.Fatalf("get %v stdout =\n%q\nwant\n%q", test.args, stdout, test.want)
		}
	}
}

// TestSingularProjectionsRenderNoAge is the other half of criterion 4: the AGE
// column belongs to the plural read alone.
//
// `get pane`, `rename pane`, and every `describe -o <mode>` share the render
// seam the table lives in, and all of them are handed the zero clock. The
// summary line they emit is frozen here, so an age appended to it -- or a clock
// quietly wired into the seam -- reddens.
func TestSingularProjectionsRenderNoAge(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stdout, _, err := runRoute(t, newTestDescribeCommand(t, store),
		"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "-o", "ref")
	if err != nil {
		t.Fatalf("describe pane -o ref error = %v", err)
	}
	if stdout != "pane/zsh\n" {
		t.Fatalf("describe pane -o ref = %q", stdout)
	}

	renamed, _, err := runRoute(t, newTestRenameCommand(newFakeResourceStore(t)),
		"pane", "--project", "alpha", "--window", "main", "--pane", "log", "--name", "renamed")
	if err != nil {
		t.Fatalf("rename pane error = %v", err)
	}
	if renamed != "pane/renamed status=live owner=project/alpha window/main\n" {
		t.Fatalf("rename pane = %q, want the summary with no age", renamed)
	}

	// The seam itself: a singular projection is handed no clock, and asking it
	// for one is what would silently reintroduce a wall-clock read.
	if got := resourceAgeCell(store.registry, coremetadata.KindPane, "pan-alpha-zsh", time.Time{}); got != "" {
		t.Fatalf("the zero clock rendered an age of %q", got)
	}
	row := resourceTableRow(
		selector.Match{Kind: coremetadata.KindPane, UID: "pan-alpha-zsh", Name: "zsh", Status: selector.StatusLive},
		coremetadata.KindPane, store.registry, time.Time{})
	if row[len(row)-1] != "" {
		t.Fatalf("the AGE cell rendered %q without a clock", row[len(row)-1])
	}
}
