package app

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

// pinFixture wires the pin command over an in-memory typed pin file and an
// explicit Registry, so every assertion below is about the two collections rather
// than about whatever the host machine happens to have registered.
func pinFixture(store *stubSwitchPinStore, projects ...coremetadata.Project) *pinCommand {
	registry := coremetadata.Registry{Projects: projects}
	authority := newPinAuthority(store)
	authority.projects = func() ([]pins.ProjectRef, error) { return projectRefsOf(registry), nil }
	return &pinCommand{
		authority: authority,
		registry:  func() (coremetadata.Registry, error) { return registry, nil },
	}
}

func pinFixtureProject(uid, name, root string) coremetadata.Project {
	project := coremetadata.Project{}
	project.Metadata.UID = uid
	project.Metadata.Name = name
	project.Spec.Root = root
	return project
}

// TestPinListStatesTheKindOfEveryEntry is the vocabulary contract of the listing.
// The old output was one path per line, which could not distinguish a managed
// Project from a directory nobody had registered; the kind column is that
// distinction, and a managed row's root is projected from the Registry.
func TestPinListStatesTheKindOfEveryEntry(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
		{Kind: pins.KindProject, Value: "proj-gone"},
	}}}
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"project", "list"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "project\tuid:proj-app\t/srv/app\tapp\n" +
		"candidate\t/srv/scratch\n" +
		"project\tuid:proj-gone\t(no Registry Project)\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if store.writes != 0 {
		t.Fatalf("listing wrote the pin file %d times, want 0", store.writes)
	}
}

func TestPinListKindFilterSelectsOneCollection(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
	}}}
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project", "list", "--kind", "candidate"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "candidate\t/srv/scratch\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	var stderr bytes.Buffer
	if err := cmd.Run([]string{"project", "list", "--kind", "workdir"}, &bytes.Buffer{}, &stderr); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}

// TestPinAddTypesAPathByItsRegistryMatch is the compatibility rule. The argv an
// operator already types is unchanged, and it now resolves to a *typed* pin: a
// registered root becomes the Project's uid, an unregistered one stays a path.
func TestPinAddTypesAPathByItsRegistryMatch(t *testing.T) {
	t.Parallel()

	project := pinFixtureProject("proj-app", "app", "/srv/app")

	t.Run("registered root becomes a managed uid pin", func(t *testing.T) {
		t.Parallel()
		store := newStubPinStore()
		cmd := pinFixture(store, project)

		var stdout bytes.Buffer
		if err := cmd.Run([]string{"project", "add", "/srv/app//"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if got, want := store.set.ProjectUIDs(), []string{"proj-app"}; !slices.Equal(got, want) {
			t.Fatalf("managed pins = %#v, want %#v", got, want)
		}
		if len(store.set.CandidatePaths()) != 0 {
			t.Fatalf("candidate pins = %#v, want none", store.set.CandidatePaths())
		}
		if got, want := stdout.String(), "pinned: project proj-app\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("unregistered root stays a candidate pin", func(t *testing.T) {
		t.Parallel()
		store := newStubPinStore()
		cmd := pinFixture(store, project)

		var stdout bytes.Buffer
		if err := cmd.Run([]string{"project", "add", "/srv/scratch"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if got, want := store.set.CandidatePaths(), []string{"/srv/scratch"}; !slices.Equal(got, want) {
			t.Fatalf("candidate pins = %#v, want %#v", got, want)
		}
		if len(store.set.ProjectUIDs()) != 0 {
			t.Fatalf("pinning an unregistered path minted managed identity: %#v", store.set.ProjectUIDs())
		}
		if got, want := stdout.String(), "pinned: candidate /srv/scratch\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("explicit uid selector is accepted", func(t *testing.T) {
		t.Parallel()
		store := newStubPinStore()
		cmd := pinFixture(store, project)

		if err := cmd.Run([]string{"project", "add", "uid:proj-app"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if got, want := store.set.ProjectUIDs(), []string{"proj-app"}; !slices.Equal(got, want) {
			t.Fatalf("managed pins = %#v, want %#v", got, want)
		}
	})
}

// TestPinAddRepeatIsWriteFree is the no-op half of the mutation contract.
func TestPinAddRepeatIsWriteFree(t *testing.T) {
	t.Parallel()

	store := newStubPinStore()
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	for range 3 {
		if err := cmd.Run([]string{"project", "add", "/srv/app"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	}
	if store.writes != 1 {
		t.Fatalf("pin file writes = %d, want exactly the first one", store.writes)
	}
}

// TestPinAddRefusesAnAmbiguousPathWithoutWriting is the refusal path of the typed
// resolution: two Projects claiming one root means the operator has to say which.
func TestPinAddRefusesAnAmbiguousPathWithoutWriting(t *testing.T) {
	t.Parallel()

	store := newStubPinStore()
	cmd := pinFixture(store,
		pinFixtureProject("proj-a", "a", "/srv/dup"),
		pinFixtureProject("proj-b", "b", "/srv/dup"))

	err := cmd.Run([]string{"project", "add", "/srv/dup"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an ambiguous path must be refused")
	}
	for _, want := range []string{"proj-a", "proj-b", "uid:<uid>"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if store.writes != 0 {
		t.Fatalf("a refused pin wrote the file %d times, want 0", store.writes)
	}
}

func TestPinRemoveAndToggleAddressTypedPins(t *testing.T) {
	t.Parallel()

	store := newStubPinStore("proj-app")
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project", "remove", "uid:proj-app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	if len(store.set.Pins) != 0 {
		t.Fatalf("pins = %#v, want empty", store.set.Pins)
	}
	if got, want := stdout.String(), "unpinned: project proj-app\n"; got != want {
		t.Fatalf("remove stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	if err := cmd.Run([]string{"project", "toggle", "/srv/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle on error = %v", err)
	}
	if got, want := stdout.String(), "pinned: project proj-app\n"; got != want {
		t.Fatalf("toggle stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	if err := cmd.Run([]string{"project", "toggle", "/srv/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle off error = %v", err)
	}
	if got, want := stdout.String(), "unpinned: project proj-app\n"; got != want {
		t.Fatalf("toggle stdout = %q, want %q", got, want)
	}
}

func TestPinClearEmptiesBothCollectionsAndIsWriteFreeWhenEmpty(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: "proj-app"},
		{Kind: pins.KindCandidate, Value: "/srv/scratch"},
	}}}
	cmd := pinFixture(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project", "clear"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(store.set.Pins) != 0 {
		t.Fatalf("pins = %#v, want empty", store.set.Pins)
	}
	if got, want := stdout.String(), "cleared pins\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	writes := store.writes
	if err := cmd.Run([]string{"project", "clear"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("second clear error = %v", err)
	}
	if store.writes != writes {
		t.Fatalf("clearing an empty typed file wrote again (%d -> %d)", writes, store.writes)
	}
}

// TestPinMigrateStoresTheTypedFormOfALegacyFile is acceptance (2)'s migration half:
// an exact single match becomes a uid preference, a zero match is preserved as a
// candidate, and nothing invents identity.
func TestPinMigrateStoresTheTypedFormOfALegacyFile(t *testing.T) {
	t.Parallel()

	store := newLegacyStubPinStore("/srv/app", "/srv/scratch")
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project", "migrate"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.set.Format, pins.FormatTyped; got != want {
		t.Fatalf("format = %q, want %q", got, want)
	}
	if got, want := store.set.ProjectUIDs(), []string{"proj-app"}; !slices.Equal(got, want) {
		t.Fatalf("managed pins = %#v, want %#v", got, want)
	}
	if got, want := store.set.CandidatePaths(), []string{"/srv/scratch"}; !slices.Equal(got, want) {
		t.Fatalf("candidate pins = %#v, want %#v", got, want)
	}
	out := stdout.String()
	for _, want := range []string{"migrated: /srv/app -> uid:proj-app", "candidate: /srv/scratch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q does not report %q", out, want)
		}
	}

	writes := store.writes
	if err := cmd.Run([]string{"project", "migrate"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("repeat migrate error = %v", err)
	}
	if store.writes != writes {
		t.Fatalf("migrating an already-typed file wrote again (%d -> %d)", writes, store.writes)
	}
}

// TestPinMigrateDryRunReportsWithoutWriting keeps the preview honest.
func TestPinMigrateDryRunReportsWithoutWriting(t *testing.T) {
	t.Parallel()

	store := newLegacyStubPinStore("/srv/app")
	cmd := pinFixture(store, pinFixtureProject("proj-app", "app", "/srv/app"))

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project", "migrate", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.writes != 0 {
		t.Fatalf("dry run wrote the pin file %d times, want 0", store.writes)
	}
	if got := store.set.Format; got != pins.FormatLegacy {
		t.Fatalf("format = %q, want the legacy file untouched", got)
	}
	if !strings.Contains(stdout.String(), "would migrate: /srv/app -> uid:proj-app") {
		t.Fatalf("stdout = %q, want a would-migrate line", stdout.String())
	}
}

// TestPinMigrateRefusesAnAmbiguousLegacyPathAndKeepsTheBytes is acceptance (4).
func TestPinMigrateRefusesAnAmbiguousLegacyPathAndKeepsTheBytes(t *testing.T) {
	t.Parallel()

	store := newLegacyStubPinStore("/srv/dup", "/srv/app")
	before := store.set
	cmd := pinFixture(store,
		pinFixtureProject("proj-a", "a", "/srv/dup"),
		pinFixtureProject("proj-b", "b", "/srv/dup"),
		pinFixtureProject("proj-app", "app", "/srv/app"))

	err := cmd.Run([]string{"project", "migrate"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an ambiguous legacy pin must refuse the whole migration")
	}
	var ambiguous *pins.AmbiguousMigrationError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("error = %v, want AmbiguousMigrationError", err)
	}
	if store.writes != 0 {
		t.Fatalf("a refused migration wrote the file %d times, want 0", store.writes)
	}
	if !store.set.Equal(before) || store.set.Format != pins.FormatLegacy {
		t.Fatalf("pin state = %#v, want the legacy file byte-identical", store.set)
	}
	// The unambiguous pin in the same file is not partially migrated either: the
	// migration is one transaction, so `/srv/app` keeps its legacy line too.
	if len(store.set.ProjectUIDs()) != 0 {
		t.Fatalf("partial migration recorded managed pins: %#v", store.set.ProjectUIDs())
	}
}

// TestPinMutationRefusesWhileALegacyFileIsAmbiguous keeps a mutation from silently
// rewriting a file whose migration was refused.
func TestPinMutationRefusesWhileALegacyFileIsAmbiguous(t *testing.T) {
	t.Parallel()

	store := newLegacyStubPinStore("/srv/dup")
	cmd := pinFixture(store,
		pinFixtureProject("proj-a", "a", "/srv/dup"),
		pinFixtureProject("proj-b", "b", "/srv/dup"))

	if err := cmd.Run([]string{"project", "add", "/srv/other"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("a mutation over an unresolvable legacy file must refuse")
	}
	if store.writes != 0 {
		t.Fatalf("a refused mutation wrote the file %d times, want 0", store.writes)
	}
}

func TestPinCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "pin requires a subcommand"},
		{name: "unknown subcommand", args: []string{"unknown"}, want: "unknown pin subcommand: unknown"},
		{name: "list args", args: []string{"list", "extra"}, want: "pin list does not accept positional arguments"},
		{name: "add missing dir", args: []string{"add"}, want: "pin add requires exactly 1 <dir|uid:uid> argument"},
		{name: "clear args", args: []string{"clear", "extra"}, want: "pin clear does not accept positional arguments"},
		{name: "migrate args", args: []string{"migrate", "extra"}, want: "pin migrate does not accept positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := pinFixture(newStubPinStore()).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

// TestPinUsageSeparatesTheThreeCollections is acceptance (5)'s CLI half: the help
// text names the two pin kinds and points workdirs somewhere else, so the surface
// itself states which authority is which.
func TestPinUsageSeparatesTheThreeCollections(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	if err := pinFixture(newStubPinStore()).Run([]string{"project", "help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"projmux pin project migrate",
		"a Registry Project uid",
		"a filesystem path that no Registry Project claims",
		"Discovery roots (workdirs) are a separate collection",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage %q does not state %q", out, want)
		}
	}
}

func TestPinCommandPropagatesStoreSetupError(t *testing.T) {
	t.Parallel()

	cmd := &pinCommand{storeErr: errors.New("no home directory")}
	err := cmd.Run([]string{"list"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "configure pin store") {
		t.Fatalf("error = %v, want configure pin store", err)
	}
}

// TestNewPinCommandWritesTheTypedEnvelopeToTheDefaultPath is the end-to-end file
// shape: a fresh pin on a machine with no Registry is a candidate pin, stored in
// the v2 envelope.
func TestNewPinCommandWritesTheTypedEnvelopeToTheDefaultPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	cmd := newPinCommand()

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"add", "/tmp/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(add) error = %v", err)
	}

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}

	data, err := os.ReadFile(paths.PinFile())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "projmux-pins v2\ncandidate /tmp/app\n"; got != want {
		t.Fatalf("pin file = %q, want %q", got, want)
	}
}
