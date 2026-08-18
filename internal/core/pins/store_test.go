package pins

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestNewDefaultStoreUsesConfigPinFile(t *testing.T) {
	t.Parallel()

	paths := config.DefaultPaths("/tmp/config-home", "/tmp/state-home")
	store := NewDefaultStore(paths)

	if got, want := store.Path(), paths.PinFile(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

// TestLoadMissingFileIsAbsentNotLegacy separates "nothing pinned" from "pinned in
// the old format". A missing file has no legacy paths to migrate, so it must not
// invite a migration that would rewrite a file nobody created.
func TestLoadMissingFileIsAbsentNotLegacy(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "pins"))

	set, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if set.Format != FormatAbsent {
		t.Fatalf("Format = %q, want %q", set.Format, FormatAbsent)
	}
	if !set.Format.Typed() {
		t.Fatal("an absent pin file needs no migration, so it must report as typed")
	}
	if len(set.Pins) != 0 {
		t.Fatalf("Pins = %#v, want none", set.Pins)
	}
}

// TestSaveAndLoadRoundTripsBothKinds is the envelope contract: a typed file states
// the kind of every entry, so reloading it never has to guess which authority an
// entry points at.
func TestSaveAndLoadRoundTripsBothKinds(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	store := NewStore(path)

	managed, err := ProjectPin("proj-aaaa")
	if err != nil {
		t.Fatalf("ProjectPin() error = %v", err)
	}
	candidate, err := CandidatePin("/srv/work/with space")
	if err != nil {
		t.Fatalf("CandidatePin() error = %v", err)
	}
	want := Set{Format: FormatTyped, Pins: []Pin{managed, candidate}}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(raw), "projmux-pins v2\nproject proj-aaaa\ncandidate /srv/work/with space\n"; got != want {
		t.Fatalf("stored bytes = %q, want %q", got, want)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

// TestSaveEmptyTypedSetKeepsTheHeader keeps an emptied file typed. Without the
// header an empty v2 file would read back as legacy and re-offer a migration for
// pins that no longer exist.
func TestSaveEmptyTypedSetKeepsTheHeader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	store := NewStore(path)
	if err := store.Save(Set{Format: FormatTyped}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	set, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if set.Format != FormatTyped {
		t.Fatalf("Format = %q, want %q", set.Format, FormatTyped)
	}
}

func TestLoadLegacyPathsReadAsCandidates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(path, []byte("/srv/a\n/srv/b\n/srv/a\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	set, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if set.Format != FormatLegacy {
		t.Fatalf("Format = %q, want %q", set.Format, FormatLegacy)
	}
	if got, want := set.LegacyPaths(), []string{"/srv/a", "/srv/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LegacyPaths() = %#v, want %#v (duplicates collapse, order preserved)", got, want)
	}
}

// TestLoadRefusesACorruptTypedLine is the refusal half of the envelope. A line the
// envelope cannot mean is not repaired by guessing: a pin points at a resource, and
// guessing which resource is worse than declining to load one.
func TestLoadRefusesACorruptTypedLine(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"unknown kind":        "projmux-pins v2\nworkdir /srv/a\n",
		"missing value":       "projmux-pins v2\nproject\n",
		"empty value":         "projmux-pins v2\nproject \n",
		"non-project uid":     "projmux-pins v2\nproject win-aaaa\n",
		"uid with whitespace": "projmux-pins v2\nproject proj-a b\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "pins")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := NewStore(path).Load(); !errors.Is(err, ErrCorruptPinFile) {
				t.Fatalf("Load() error = %v, want ErrCorruptPinFile", err)
			}
		})
	}
}

// TestLoadRefusesANewerEnvelope keeps a downgrade from silently dropping entries it
// cannot parse. Saving after such a read would rewrite the file, so the read fails
// instead.
func TestLoadRefusesANewerEnvelope(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(path, []byte("projmux-pins v9\nproject proj-aaaa\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewStore(path).Load(); !errors.Is(err, ErrUnsupportedPinVersion) {
		t.Fatalf("Load() error = %v, want ErrUnsupportedPinVersion", err)
	}
}

func TestSaveRefusesAnInvalidPin(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	store := NewStore(path)
	if err := store.Save(Set{Format: FormatTyped, Pins: []Pin{{Kind: KindProject, Value: "not-a-project-uid"}}}); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("Save() error = %v, want ErrInvalidPin", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want the file never to be created", err)
	}
}

func TestSetWithAndWithoutAreOrderPreservingAndDeduplicated(t *testing.T) {
	t.Parallel()

	a := Pin{Kind: KindCandidate, Value: "/srv/a"}
	b := Pin{Kind: KindProject, Value: "proj-b"}
	set := Set{Format: FormatTyped}.With(a).With(b).With(a)
	if got, want := set.Pins, []Pin{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pins = %#v, want %#v", got, want)
	}
	if !set.Equal(set.With(a)) {
		t.Fatal("adding a present pin must not change the set")
	}
	if got, want := set.Without(a).Pins, []Pin{b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Without() = %#v, want %#v", got, want)
	}
}
