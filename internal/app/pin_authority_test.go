package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// authorityOver binds a fake pin file to an explicit Registry projection.
func authorityOver(store *stubSwitchPinStore, refs ...pins.ProjectRef) pinAuthority {
	return pinAuthority{
		store:    store,
		projects: func() ([]pins.ProjectRef, error) { return refs, nil },
	}
}

// TestManagedPinSurvivesRebindRenameAndMissingRoot is acceptance (2).
//
// A managed pin is a uid, so none of the three things that used to lose it can:
// a rebind rewrites spec.root, a rename rewrites metadata.name, and a vanished
// directory leaves the row with no usable root at all. The pin, and the sidebar
// tier it produces, are unchanged through all three.
func TestManagedPinSurvivesRebindRenameAndMissingRoot(t *testing.T) {
	t.Parallel()

	const uid = "proj-app"
	store := newStubPinStore(uid)

	for _, tc := range []struct {
		name string
		refs []pins.ProjectRef
		row  registryview.Row
	}{
		{
			name: "unchanged",
			refs: []pins.ProjectRef{{UID: uid, Root: "/srv/app"}},
			row:  registryview.Row{Kind: registryview.RowKindProject, UID: uid, Name: "app", Root: "/srv/app", Status: resourcegraph.StatusOffline},
		},
		{
			name: "after a rebind to a different root",
			refs: []pins.ProjectRef{{UID: uid, Root: "/srv/moved"}},
			row:  registryview.Row{Kind: registryview.RowKindProject, UID: uid, Name: "app", Root: "/srv/moved", Status: resourcegraph.StatusOffline},
		},
		{
			name: "after a rename",
			refs: []pins.ProjectRef{{UID: uid, Root: "/srv/app"}},
			row:  registryview.Row{Kind: registryview.RowKindProject, UID: uid, Name: "renamed", DisplayName: "Renamed", Root: "/srv/app", Status: resourcegraph.StatusOffline},
		},
		{
			name: "with a MissingRoot row that offers rebind instead of open",
			refs: []pins.ProjectRef{{UID: uid, Root: "/srv/gone"}},
			row: registryview.Row{
				Kind: registryview.RowKindProject, UID: uid, Name: "app", Root: "/srv/gone",
				Status: resourcegraph.StatusOffline, Actions: []registryview.Action{registryview.ActionRebind},
			},
		},
		{
			name: "with no root at all",
			refs: []pins.ProjectRef{{UID: uid}},
			row:  registryview.Row{Kind: registryview.RowKindProject, UID: uid, Name: "app", Status: resourcegraph.StatusOffline},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			selection, err := authorityOver(store, tc.refs...).selection()
			if err != nil {
				t.Fatalf("selection() error = %v", err)
			}
			if !selection.pinnedProject(uid) {
				t.Fatalf("Project %s lost its managed pin", uid)
			}
			if got := switchManagedProjectTierOf(tc.row, selection); got != switchManagedTierPinned {
				t.Fatalf("sidebar tier = %d, want the pinned tier", got)
			}
		})
	}
}

// TestALegacyPathPinIsProjectedNotMigratedByAReadIsTheOtherHalf shows why the uid
// matters. A legacy file still holds a path, so a projected read follows the root;
// migrating it is what makes the preference survive a later rebind.
func TestALegacyPathPinIsProjectedNotMigratedByARead(t *testing.T) {
	t.Parallel()

	const uid = "proj-app"
	store := newLegacyStubPinStore("/srv/app")
	authority := authorityOver(store, pins.ProjectRef{UID: uid, Root: "/srv/app"})

	selection, err := authority.selection()
	if err != nil {
		t.Fatalf("selection() error = %v", err)
	}
	if !selection.pinnedProject(uid) {
		t.Fatal("a legacy path pin on a registered root must project onto the Project uid")
	}
	if store.writes != 0 {
		t.Fatalf("a read wrote the pin file %d times, want 0", store.writes)
	}
	if store.set.Format != pins.FormatLegacy {
		t.Fatalf("format = %q, want the legacy file untouched by a read", store.set.Format)
	}

	// Migrating stores the uid, and from then on the root may move freely.
	if _, err := authority.migrate(); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	moved := authorityOver(store, pins.ProjectRef{UID: uid, Root: "/srv/moved"})
	movedSelection, err := moved.selection()
	if err != nil {
		t.Fatalf("selection() after rebind error = %v", err)
	}
	if !movedSelection.pinnedProject(uid) {
		t.Fatal("a migrated managed pin did not survive a rebind")
	}
}

// TestAnUnresolvedLegacyPathStaysACandidateAndMintsNothing is the zero-match half
// of acceptance (2).
func TestAnUnresolvedLegacyPathStaysACandidateAndMintsNothing(t *testing.T) {
	t.Parallel()

	store := newLegacyStubPinStore("/srv/unclaimed")
	authority := authorityOver(store, pins.ProjectRef{UID: "proj-other", Root: "/srv/other"})

	resolution, err := authority.migrate()
	if err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	if got, want := resolution.Set.CandidatePaths(), []string{"/srv/unclaimed"}; !equalStrings(got, want) {
		t.Fatalf("candidate pins = %#v, want %#v", got, want)
	}
	if len(resolution.Set.ProjectUIDs()) != 0 {
		t.Fatalf("an unclaimed path minted managed identity: %#v", resolution.Set.ProjectUIDs())
	}
	if got, want := store.set.Format, pins.FormatTyped; got != want {
		t.Fatalf("stored format = %q, want %q", got, want)
	}
}

// TestPinTargetForPathFollowsTheResolveOrCandidateRule is the compatibility rule of
// every path argument, in one place.
func TestPinTargetForPathFollowsTheResolveOrCandidateRule(t *testing.T) {
	t.Parallel()

	authority := authorityOver(newStubPinStore(),
		pins.ProjectRef{UID: "proj-app", Root: "/srv/app"},
		pins.ProjectRef{UID: "proj-dup-a", Root: "/srv/dup"},
		pins.ProjectRef{UID: "proj-dup-b", Root: "/srv/dup"})

	managed, err := authority.pinTargetForPath("/srv/app")
	if err != nil {
		t.Fatalf("pinTargetForPath(registered) error = %v", err)
	}
	if managed != (pins.Pin{Kind: pins.KindProject, Value: "proj-app"}) {
		t.Fatalf("registered root resolved to %#v", managed)
	}

	candidate, err := authority.pinTargetForPath("/srv/scratch")
	if err != nil {
		t.Fatalf("pinTargetForPath(unregistered) error = %v", err)
	}
	if candidate != (pins.Pin{Kind: pins.KindCandidate, Value: "/srv/scratch"}) {
		t.Fatalf("unregistered root resolved to %#v", candidate)
	}

	if _, err := authority.pinTargetForPath("/srv/dup"); err == nil {
		t.Fatal("a root two Projects claim must be refused rather than guessed")
	}

	// An explicit uid selector bypasses the path question entirely, which is the
	// escape hatch the ambiguity refusal points at.
	explicit, err := authority.pinTargetForSelector("uid:proj-dup-b")
	if err != nil {
		t.Fatalf("pinTargetForSelector(uid) error = %v", err)
	}
	if explicit != (pins.Pin{Kind: pins.KindProject, Value: "proj-dup-b"}) {
		t.Fatalf("uid selector resolved to %#v", explicit)
	}
}

// TestPinAuthorityRefusesACorruptPinFileWithoutWriting keeps a damaged file from
// being silently replaced by whatever the reader could still parse.
func TestPinAuthorityRefusesACorruptPinFileWithoutWriting(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{err: pins.ErrCorruptPinFile}
	authority := authorityOver(store)

	if _, err := authority.resolved(); !errors.Is(err, pins.ErrCorruptPinFile) {
		t.Fatalf("resolved() error = %v, want ErrCorruptPinFile", err)
	}
	if _, err := authority.migrate(); !errors.Is(err, pins.ErrCorruptPinFile) {
		t.Fatalf("migrate() error = %v, want ErrCorruptPinFile", err)
	}
	if err := authority.add(pins.Pin{Kind: pins.KindCandidate, Value: "/srv/a"}); !errors.Is(err, pins.ErrCorruptPinFile) {
		t.Fatalf("add() error = %v, want ErrCorruptPinFile", err)
	}
	if store.writes != 0 {
		t.Fatalf("a corrupt pin file was written %d times, want 0", store.writes)
	}
}

// TestPinDiscoveryPathsTakeTheRootFromTheRegistry keeps discovery inputs honest
// after a rebind: the managed pin contributes the root the Registry holds now, not
// the directory it used to point at.
func TestPinDiscoveryPathsTakeTheRootFromTheRegistry(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
		{Kind: pins.KindProject, Value: "proj-gone"},
	}}}
	authority := authorityOver(store, pins.ProjectRef{UID: "proj-app", Root: "/srv/moved"})

	paths, err := authority.discoveryPaths()
	if err != nil {
		t.Fatalf("discoveryPaths() error = %v", err)
	}
	if want := []string{"/srv/moved", "/srv/scratch"}; !equalStrings(paths, want) {
		t.Fatalf("discoveryPaths() = %#v, want %#v; a pin with no Registry Project contributes no path", paths, want)
	}
}

// TestPinnedRowsSeparateTheActionReferenceFromTheDisplayedRoot is the row contract
// Settings and the picker both rely on.
func TestPinnedRowsSeparateTheActionReferenceFromTheDisplayedRoot(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
	}}}
	rows, _, err := authorityOver(store, pins.ProjectRef{UID: "proj-app", Root: "/srv/app"}).pinnedRows()
	if err != nil {
		t.Fatalf("pinnedRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want both collections", rows)
	}
	if got, want := rows[0].Reference, "uid:proj-app"; got != want {
		t.Fatalf("managed reference = %q, want %q", got, want)
	}
	if got, want := rows[0].Root, "/srv/app"; got != want {
		t.Fatalf("managed root = %q, want the projected %q", got, want)
	}
	if got, want := rows[1].Reference, "/srv/scratch"; got != want {
		t.Fatalf("candidate reference = %q, want %q", got, want)
	}
	if !strings.HasPrefix(rows[0].Pin.String(), "project ") || !strings.HasPrefix(rows[1].Pin.String(), "candidate ") {
		t.Fatalf("pin strings = %q / %q, want the kind first", rows[0].Pin, rows[1].Pin)
	}
}
