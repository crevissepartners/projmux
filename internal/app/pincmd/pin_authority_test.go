package pincmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/pins"
)

// stubPinStore is an in-memory pin file that counts the writes it was asked for.
type stubPinStore struct {
	set    pins.Set
	writes int
	err    error
}

// newStubPinStore seeds a typed store with managed pins by uid.
func newStubPinStore(uids ...string) *stubPinStore {
	set := pins.Set{Format: pins.FormatTyped}
	for _, uid := range uids {
		set = set.With(pins.Pin{Kind: pins.KindProject, Value: uid})
	}
	return &stubPinStore{set: set}
}

// newLegacyStubPinStore seeds the pre-v2 shape: bare paths with no statement about
// which of them a Project claims.
func newLegacyStubPinStore(paths ...string) *stubPinStore {
	set := pins.Set{Format: pins.FormatLegacy}
	for _, path := range paths {
		set.Pins = append(set.Pins, pins.Pin{Kind: pins.KindCandidate, Value: path})
	}
	if len(set.Pins) == 0 {
		set.Format = pins.FormatAbsent
	}
	return &stubPinStore{set: set}
}

func (s *stubPinStore) Path() string { return "/fixture/pins" }

func (s *stubPinStore) Load() (pins.Set, error) {
	if s.err != nil {
		return pins.Set{}, s.err
	}
	return s.set, nil
}

// Update mirrors pins.Store.Update: load, decide, save only when asked to.
func (s *stubPinStore) Update(update func(pins.Set) (pins.Set, bool, error)) error {
	stored, err := s.Load()
	if err != nil {
		return err
	}
	next, write, err := update(stored)
	if err != nil || !write {
		return err
	}
	s.writes++
	s.set = next
	return nil
}

// authorityOver binds a fake pin file to an explicit Registry projection.
func authorityOver(store *stubPinStore, refs ...pins.ProjectRef) Authority {
	return Authority{
		store:    store,
		projects: func() ([]pins.ProjectRef, error) { return refs, nil },
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

	selection, err := authority.Selection()
	if err != nil {
		t.Fatalf("selection() error = %v", err)
	}
	if !selection.PinnedProject(uid) {
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
	movedSelection, err := moved.Selection()
	if err != nil {
		t.Fatalf("selection() after rebind error = %v", err)
	}
	if !movedSelection.PinnedProject(uid) {
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
	if got, want := resolution.Set.CandidatePaths(), []string{"/srv/unclaimed"}; !slices.Equal(got, want) {
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
	explicit, err := authority.PinTargetForSelector("uid:proj-dup-b")
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

	store := &stubPinStore{err: pins.ErrCorruptPinFile}
	authority := authorityOver(store)

	if _, err := authority.Resolved(); !errors.Is(err, pins.ErrCorruptPinFile) {
		t.Fatalf("resolved() error = %v, want ErrCorruptPinFile", err)
	}
	if _, err := authority.migrate(); !errors.Is(err, pins.ErrCorruptPinFile) {
		t.Fatalf("migrate() error = %v, want ErrCorruptPinFile", err)
	}
	if err := authority.Add(pins.Pin{Kind: pins.KindCandidate, Value: "/srv/a"}); !errors.Is(err, pins.ErrCorruptPinFile) {
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

	store := &stubPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
		{Kind: pins.KindProject, Value: "proj-gone"},
	}}}
	authority := authorityOver(store, pins.ProjectRef{UID: "proj-app", Root: "/srv/moved"})

	paths, err := authority.DiscoveryPaths()
	if err != nil {
		t.Fatalf("discoveryPaths() error = %v", err)
	}
	if want := []string{"/srv/moved", "/srv/scratch"}; !slices.Equal(paths, want) {
		t.Fatalf("discoveryPaths() = %#v, want %#v; a pin with no Registry Project contributes no path", paths, want)
	}
}

// TestPinnedRowsSeparateTheActionReferenceFromTheDisplayedRoot is the row contract
// Settings and the picker both rely on.
func TestPinnedRowsSeparateTheActionReferenceFromTheDisplayedRoot(t *testing.T) {
	t.Parallel()

	store := &stubPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
	}}}
	rows, _, err := authorityOver(store, pins.ProjectRef{UID: "proj-app", Root: "/srv/app"}).PinnedRows()
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
