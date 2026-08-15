package metadata

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestClassifySchemaVersionFailsClosedForNewerEnvelopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		version    int
		wantAction SchemaAction
		wantErr    error
	}{
		{name: "current version reads as is", version: SchemaVersion, wantAction: SchemaCurrent},
		{name: "pre-release unversioned document migrates", version: 0, wantAction: SchemaMigrate},
		{name: "newer version is rejected fail closed", version: SchemaVersion + 1, wantAction: SchemaReject, wantErr: ErrSchemaTooNew},
		{name: "far newer version is rejected fail closed", version: 99, wantAction: SchemaReject, wantErr: ErrSchemaTooNew},
		{name: "negative version is unsupported", version: -1, wantAction: SchemaReject, wantErr: ErrSchemaUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			action, err := ClassifySchemaVersion(tt.version)
			if action != tt.wantAction {
				t.Fatalf("action = %s, want %s", action, tt.wantAction)
			}
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
			}
			if IsUsageError(err) {
				t.Fatalf("a schema fault is not user input and must not be a usage error: %v", err)
			}
		})
	}
}

func TestMigrateRegistryRejectsNewerEnvelopesWithoutTouchingTheInput(t *testing.T) {
	t.Parallel()

	source := NewRegistry()
	source.SchemaVersion = SchemaVersion + 1
	source.Projects = []Project{{Metadata: ObjectMeta{UID: "project-01", Name: "projmux"}}}

	before := mustJSON(t, source)
	if _, _, err := MigrateRegistry(source); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("error = %v, want ErrSchemaTooNew", err)
	}
	if got := mustJSON(t, source); got != before {
		t.Fatalf("a rejected migration mutated its input:\nbefore=%s\nafter=%s", before, got)
	}
}

func TestMigrateRegistryLiftsAPreReleaseDocumentToTheCurrentEnvelope(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/registry-v0-source.json")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	var source Registry
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if source.SchemaVersion != 0 {
		t.Fatalf("source schemaVersion = %d, want the unversioned 0", source.SchemaVersion)
	}

	migrated, ran, err := MigrateRegistry(source)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !ran {
		t.Fatal("expected a migration step to run")
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated registry is invalid: %v", err)
	}

	got := mustJSON(t, migrated) + "\n"
	want, err := os.ReadFile("testdata/registry-v1-migrated.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("migrated registry does not match the golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The migration is idempotent: re-running it is a no-op.
	again, ranAgain, err := MigrateRegistry(migrated)
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if ranAgain {
		t.Fatal("a current-version registry must not run a migration step")
	}
	if mustJSON(t, again) != mustJSON(t, migrated) {
		t.Fatal("re-migrating a current registry changed it")
	}
}

func TestMigrationBackfillsNameReservationsWithoutRenumberingExistingResources(t *testing.T) {
	t.Parallel()

	source := Registry{
		SchemaVersion: 0,
		Projects: []Project{
			{Metadata: ObjectMeta{UID: "project-01", Name: "projmux"}, Spec: ProjectSpec{Root: "/src/a"}},
			{Metadata: ObjectMeta{UID: "project-02", Name: "projmux-4"}, Spec: ProjectSpec{Root: "/src/b"}},
		},
	}
	migrated, _, err := MigrateRegistry(source)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated.Projects[0].Metadata.Name != "projmux" || migrated.Projects[1].Metadata.Name != "projmux-4" {
		t.Fatalf("migration renamed an existing project: %q/%q", migrated.Projects[0].Metadata.Name, migrated.Projects[1].Metadata.Name)
	}
	if len(migrated.NameReservations) != 2 {
		t.Fatalf("reservations = %+v, want one per project", migrated.NameReservations)
	}
	next, err := migrated.allocateName("test", "", KindProject, "projmux", "project-03")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if next != "projmux-1" {
		t.Fatalf("next = %q, want the lowest free projmux-1", next)
	}
}

func TestRegistryValidateRejectsStructuralViolations(t *testing.T) {
	t.Parallel()

	base := func() Registry {
		roots := dirSet{"/src/projmux": true}
		m := testMutator(roots)
		reg := NewRegistry()
		if _, err := registerFixture(m, &reg, "/src/projmux"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return reg
	}

	tests := []struct {
		name    string
		mutate  func(*Registry)
		wantErr error
	}{
		{name: "wrong api version", mutate: func(r *Registry) { r.APIVersion = "projmux.io/v1" }, wantErr: ErrInvalidRegistry},
		{name: "wrong schema version", mutate: func(r *Registry) { r.SchemaVersion = 7 }, wantErr: ErrInvalidRegistry},
		{name: "duplicate uid", mutate: func(r *Registry) { r.Windows[0].Metadata.UID = r.Projects[0].Metadata.UID }, wantErr: ErrInvalidRegistry},
		{name: "dangling owner", mutate: func(r *Registry) { r.Windows[0].Metadata.OwnerRef.UID = "missing" }, wantErr: ErrInvalidRegistry},
		{name: "project with an owner", mutate: func(r *Registry) {
			r.Projects[0].Metadata.OwnerRef = &OwnerRef{Kind: KindProject, UID: "x"}
		}, wantErr: ErrInvalidRegistry},
		{name: "relative root", mutate: func(r *Registry) { r.Projects[0].Spec.Root = "relative" }, wantErr: ErrInvalidRegistry},
		{name: "dangling primary pane", mutate: func(r *Registry) { r.Windows[0].Spec.PrimaryPaneRef = "missing" }, wantErr: ErrInvalidRegistry},
		{name: "missing name reservation", mutate: func(r *Registry) { r.NameReservations = nil }, wantErr: ErrInvalidRegistry},
		{name: "reservation for an unknown uid", mutate: func(r *Registry) {
			r.NameReservations = append(r.NameReservations, NameReservation{Kind: KindProject, Name: "ghost", UID: "nobody"})
		}, wantErr: ErrInvalidRegistry},
		{name: "invalid resource name", mutate: func(r *Registry) { r.Panes[0].Metadata.Name = "bad name" }, wantErr: ErrInvalidName},
		{name: "unsupported pane role", mutate: func(r *Registry) { r.Panes[0].Spec.Role = "weird" }, wantErr: ErrInvalidRegistry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := base()
			if err := reg.Validate(); err != nil {
				t.Fatalf("baseline registry is invalid: %v", err)
			}
			tt.mutate(&reg)
			err := reg.Validate()
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
			}
		})
	}
}
