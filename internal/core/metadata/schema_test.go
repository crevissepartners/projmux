package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

func migrationGoldenEnvironment() MigrationEnvironment {
	counts := map[Kind]int{}
	return MigrationEnvironment{
		DirectoryExists: func(path string) (bool, error) {
			return path == "/tmp" || path == "/", nil
		},
		NewUID: func(kind Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-migration-%02d", strings.ToLower(string(kind)), counts[kind]), nil
		},
	}
}

func TestRegistryGenerationMigrationGoldens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		generation   string
		sourceSHA256 string
		wantRepairs  int
		wantLosses   int
	}{
		{generation: "v010", sourceSHA256: "04a5d550f60df7d6b45e5ae80ac54667d91edd7506773f4a38653b54247b765b", wantRepairs: 2},
		{generation: "v011", sourceSHA256: "d31bbeee815af32d8bc940d99c1bf8daa17f84b63844c524b6ccd0cdd32893d1", wantRepairs: 3, wantLosses: 1},
		{generation: "v012", sourceSHA256: "9424984258a3977999cf8b58ee32e69f69606162c32780fbb41d6776712bbb4b", wantRepairs: 4, wantLosses: 1},
	}
	for _, tt := range tests {
		t.Run(tt.generation, func(t *testing.T) {
			t.Parallel()
			sourceBytes, err := os.ReadFile("testdata/registry-" + tt.generation + "-source.json")
			if err != nil {
				t.Fatalf("read source: %v", err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(sourceBytes)); got != tt.sourceSHA256 {
				t.Fatalf("source fixture bytes changed: sha256=%s, want %s", got, tt.sourceSHA256)
			}
			var source Registry
			if err := json.Unmarshal(sourceBytes, &source); err != nil {
				t.Fatalf("decode source: %v", err)
			}
			before := source.Clone()
			migrated, ran, report, err := MigrateRegistryWithEnvironment(nil, source, migrationGoldenEnvironment())
			if err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if !ran || report.FromVersion != 1 || report.ToVersion != SchemaVersion {
				t.Fatalf("migration = ran:%t report:%+v", ran, report)
			}
			if len(report.Repairs) != tt.wantRepairs || report.InformationLossCount() != tt.wantLosses {
				t.Fatalf("repair report = %s", report.String())
			}
			if err := migrated.Validate(); err != nil {
				t.Fatalf("migrated registry invalid: %v", err)
			}
			assertExistingIdentityAndAgentPointersPreserved(t, before, migrated)

			got := []byte(mustJSON(t, migrated) + "\n")
			if !bytes.Contains(got, []byte(`"schemaVersion": 4`)) || bytes.Contains(got, []byte(`"displayName"`)) || bytes.Contains(got, []byte(`"displayTitle"`)) {
				t.Fatalf("generation migration did not produce a presentation-free v4 document:\n%s", got)
			}

			again, ranAgain, secondReport, err := MigrateRegistryWithEnvironment(nil, migrated, migrationGoldenEnvironment())
			if err != nil || ranAgain || secondReport.RepairCount() != 0 {
				t.Fatalf("second pass = ran:%t report:%s err:%v", ranAgain, secondReport.String(), err)
			}
			if second := []byte(mustJSON(t, again) + "\n"); !bytes.Equal(second, got) {
				t.Fatal("second migration pass changed the current Registry")
			}
		})
	}
}

func assertExistingIdentityAndAgentPointersPreserved(t *testing.T, before, after Registry) {
	t.Helper()
	for _, project := range before.Projects {
		got, ok := after.Project(project.Metadata.UID)
		if !ok || got.Metadata.OwnerRef != nil {
			t.Fatalf("Project identity changed: before=%+v after=%+v", project, got)
		}
	}
	for _, window := range before.Windows {
		got, ok := after.Window(window.Metadata.UID)
		if !ok || !equalOwnerRef(got.Metadata.OwnerRef, window.Metadata.OwnerRef) {
			t.Fatalf("Window identity changed: before=%+v after=%+v", window, got)
		}
	}
	for _, pane := range before.Panes {
		got, ok := after.Pane(pane.Metadata.UID)
		if !ok || !equalOwnerRef(got.Metadata.OwnerRef, pane.Metadata.OwnerRef) {
			t.Fatalf("Pane identity changed: before=%+v after=%+v", pane, got)
		}
	}
	for _, agent := range before.Agents {
		got, ok := after.Agent(agent.Metadata.UID)
		if !ok || !equalOwnerRef(got.Metadata.OwnerRef, agent.Metadata.OwnerRef) ||
			!bytes.Equal([]byte(mustJSON(t, got.Status.SessionRef)), []byte(mustJSON(t, agent.Status.SessionRef))) {
			t.Fatalf("Agent identity/sessionRef changed: before=%+v after=%+v", agent, got)
		}
	}
}

func equalOwnerRef(left, right *OwnerRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestV1MigrationCreatesCanonicalShellChainForAZeroWindowProject(t *testing.T) {
	t.Parallel()
	reg := Registry{
		APIVersion: APIVersion, SchemaVersion: 1,
		Projects: []Project{{
			APIVersion: APIVersion, Kind: KindProject,
			Metadata: ObjectMeta{UID: "proj-empty", Name: "empty", CreatedAt: fixedNow},
			Spec:     ProjectSpec{Root: "/tmp"},
		}},
		NameReservations: []NameReservation{{Kind: KindProject, Name: "empty", UID: "proj-empty"}},
	}
	migrated, _, report, err := MigrateRegistryWithEnvironment(nil, reg, migrationGoldenEnvironment())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated zero-Window Project is invalid: %v", err)
	}
	if len(report.Repairs) != 4 || report.InformationLossCount() != 0 {
		t.Fatalf("report = %s", report.String())
	}
	project := migrated.Projects[0]
	if !validProjectPrimary(&migrated, project) {
		t.Fatalf("Project chain is not canonical: %+v", project.Spec)
	}
}

func TestValidV3CanonicalAnchorMigratesOnceToV4(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/registry-v010-v3-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	var source Registry
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	migrated, ran, report, err := MigrateRegistryWithEnvironment(nil, source, migrationGoldenEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if !ran || report.FromVersion != 3 || report.ToVersion != 4 {
		t.Fatalf("valid v3 anchor migration = ran:%t report:%s", ran, report.String())
	}
	again, ranAgain, secondReport, err := MigrateRegistryWithEnvironment(nil, migrated, migrationGoldenEnvironment())
	if err != nil || ranAgain || secondReport.RepairCount() != 0 || mustJSON(t, again) != mustJSON(t, migrated) {
		t.Fatalf("v4 repeat = ran:%t report:%s err:%v", ranAgain, secondReport.String(), err)
	}
}

func TestCanonicalV2MigratesToV3WithoutInformationLoss(t *testing.T) {
	t.Parallel()
	current, err := os.ReadFile("testdata/registry-v010-v3-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	v2 := bytes.Replace(current, []byte(`"schemaVersion": 3`), []byte(`"schemaVersion": 2`), 1)
	var source Registry
	if err := json.Unmarshal(v2, &source); err != nil {
		t.Fatal(err)
	}
	migrated, ran, report, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	if !ran || report.FromVersion != 2 || report.ToVersion != 4 || len(report.Repairs) != 0 || report.InformationLossCount() != 0 {
		t.Fatalf("v2 -> v4 migration = ran:%t report:%s", ran, report.String())
	}
	var v3 Registry
	if err := json.Unmarshal(current, &v3); err != nil {
		t.Fatal(err)
	}
	want, _, _, err := MigrateRegistryWithEnvironment(nil, v3, MigrationEnvironment{})
	if err != nil || mustJSON(t, migrated) != mustJSON(t, want) {
		t.Fatalf("v2 and v3 sources did not converge on one v4 document: %v", err)
	}
	again, ranAgain, secondReport, err := MigrateRegistryWithEnvironment(nil, migrated, MigrationEnvironment{})
	if err != nil || ranAgain || secondReport.RepairCount() != 0 || mustJSON(t, again) != mustJSON(t, migrated) {
		t.Fatalf("v3 repeat = ran:%t report:%s err:%v", ranAgain, secondReport.String(), err)
	}
}

func TestIntermediateV2NormalizesDirectlyToFinalV2GoldenAndSecondPassIsZeroByte(t *testing.T) {
	t.Parallel()
	sourceBytes, err := os.ReadFile("testdata/registry-v010-intermediate-v2-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source Registry
	if err := json.Unmarshal(sourceBytes, &source); err != nil {
		t.Fatal(err)
	}
	before := source.Clone()
	normalized, ran, report, err := MigrateRegistryWithEnvironment(nil, source, migrationGoldenEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if !ran || report.FromVersion != 2 || report.ToVersion != SchemaVersion || len(report.Repairs) != 1 {
		t.Fatalf("same-version normalization = ran:%t report:%s", ran, report.String())
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("normalized Registry: %v", err)
	}
	assertExistingIdentityAndAgentPointersPreserved(t, before, normalized)
	wantBytes, err := os.ReadFile("testdata/registry-v010-v3-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	var v3 Registry
	if err := json.Unmarshal(wantBytes, &v3); err != nil {
		t.Fatal(err)
	}
	want, _, _, err := MigrateRegistryWithEnvironment(nil, v3, migrationGoldenEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	got := []byte(mustJSON(t, normalized) + "\n")
	if mustJSON(t, normalized) != mustJSON(t, want) {
		t.Fatalf("intermediate-v2 did not converge with canonical v3:\n--- got ---\n%s\n--- want ---\n%s", got, mustJSON(t, want))
	}
	if bytes.Contains(got, []byte(`"primaryPaneRef"`)) {
		t.Fatal("final-v2 writer emitted legacy primaryPaneRef")
	}
	again, ranAgain, secondReport, err := MigrateRegistryWithEnvironment(nil, normalized, migrationGoldenEnvironment())
	if err != nil || ranAgain || len(secondReport.Repairs) != 0 {
		t.Fatalf("second pass = ran:%t report:%s err:%v", ranAgain, secondReport.String(), err)
	}
	if second := []byte(mustJSON(t, again) + "\n"); !bytes.Equal(second, got) {
		t.Fatal("final-v2 second pass changed bytes")
	}
}

func TestWindowAnchorAndDefaultShellValidationClosedTable(t *testing.T) {
	t.Parallel()
	registry, project, mutator := pbtRegistryOver(t, t.TempDir())
	window := registry.WindowsOf(project.Metadata.UID)[0]
	shell := registry.PanesOf(window.Metadata.UID)[0]
	agent, err := mutator.CreateAgent(registry, window.Metadata.UID, CreateAgentOptions{Provider: "codex", OperationID: "anchor-table-agent"})
	if err != nil {
		t.Fatal(err)
	}
	agentPane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, BootstrapPane{}, "anchor-table-pane")
	if err != nil {
		t.Fatal(err)
	}
	sibling, siblingPanes, err := mutator.AddWindow(registry, project.Metadata.UID, BootstrapWindow{}, "sh", "anchor-table-sibling")
	if err != nil {
		t.Fatal(err)
	}
	_ = sibling

	tests := []struct {
		name         string
		anchor       string
		defaultShell string
		wantErr      string
	}{
		{name: "shell anchor with default", anchor: shell.Metadata.UID, defaultShell: shell.Metadata.UID},
		{name: "shell anchor without default", anchor: shell.Metadata.UID},
		{name: "Agent anchor with default", anchor: agentPane.Metadata.UID, defaultShell: shell.Metadata.UID},
		{name: "Agent anchor without default", anchor: agentPane.Metadata.UID},
		{name: "dangling anchor", anchor: "pane-missing", wantErr: "anchorPaneRef"},
		{name: "cross-Window anchor", anchor: siblingPanes[0].Metadata.UID, wantErr: "same-Window"},
		{name: "dangling default", anchor: shell.Metadata.UID, defaultShell: "pane-missing", wantErr: "defaultShellPaneRef"},
		{name: "Agent default", anchor: agentPane.Metadata.UID, defaultShell: agentPane.Metadata.UID, wantErr: "direct Window-owned shell"},
		{name: "cross-Window default", anchor: shell.Metadata.UID, defaultShell: siblingPanes[0].Metadata.UID, wantErr: "direct Window-owned shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := registry.Clone()
			stored, _ := candidate.Window(window.Metadata.UID)
			stored.Spec.AnchorPaneRef = tt.anchor
			stored.Spec.DefaultShellPaneRef = tt.defaultShell
			err := candidate.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestMixedLegacyAndFinalWindowAuthorityFailsClosedWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"one Window carries both authorities": `{
  "apiVersion":"projmux.io/v1alpha1","schemaVersion":2,
  "windows":[{"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"win","name":"main"},"spec":{"primaryPaneRef":"pane","anchorPaneRef":"pane"}}]
}`,
		"different Windows carry different authorities": `{
  "apiVersion":"projmux.io/v1alpha1","schemaVersion":2,
  "windows":[
    {"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"win-a","name":"a"},"spec":{"primaryPaneRef":"pane-a"}},
    {"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"win-b","name":"b"},"spec":{"anchorPaneRef":"pane-b"}}
  ]
}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			var source Registry
			if err := json.Unmarshal([]byte(document), &source); err != nil {
				t.Fatal(err)
			}
			before := source.Clone()
			if _, ran, _, err := MigrateRegistryWithEnvironment(nil, source, migrationGoldenEnvironment()); !errors.Is(err, ErrInvalidRegistry) || ran {
				t.Fatalf("normalize = ran:%t err:%v, want zero-write ErrInvalidRegistry", ran, err)
			}
			if !bytes.Equal([]byte(mustJSON(t, source)), []byte(mustJSON(t, before))) {
				t.Fatal("mixed-shape refusal mutated its input")
			}
		})
	}
}

func TestRandomValidFinalV2GraphsAreDeterministicAndIdempotent(t *testing.T) {
	seed := int64(20260824)
	random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic property fixture
	for iteration := range 64 {
		registry, project, mutator := pbtRegistryOver(t, t.TempDir())
		for extra := 0; extra < random.Intn(4); extra++ {
			if _, _, err := mutator.AddWindow(registry, project.Metadata.UID, BootstrapWindow{}, "sh", fmt.Sprintf("property-window-%d-%d", iteration, extra)); err != nil {
				t.Fatal(err)
			}
		}
		for _, window := range registry.WindowsOf(project.Metadata.UID) {
			stored, _ := registry.Window(window.Metadata.UID)
			if random.Intn(2) == 0 {
				agent, err := mutator.CreateAgent(registry, window.Metadata.UID, CreateAgentOptions{Provider: "codex", OperationID: fmt.Sprintf("property-agent-%d", iteration)})
				if err != nil {
					t.Fatal(err)
				}
				pane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, BootstrapPane{}, fmt.Sprintf("property-pane-%d", iteration))
				if err != nil {
					t.Fatal(err)
				}
				stored.Spec.AnchorPaneRef = pane.Metadata.UID
			}
			if random.Intn(2) == 0 {
				stored.Spec.DefaultShellPaneRef = ""
			}
		}
		if err := registry.Validate(); err != nil {
			t.Fatalf("seed=%d iteration=%d invalid generated graph: %v", seed, iteration, err)
		}
		first := mustJSON(t, registry.Normalize())
		var decoded Registry
		if err := json.Unmarshal([]byte(first), &decoded); err != nil {
			t.Fatal(err)
		}
		normalized, ran, report, err := MigrateRegistryWithEnvironment(nil, decoded, migrationGoldenEnvironment())
		if err != nil || ran || len(report.Repairs) != 0 {
			t.Fatalf("seed=%d iteration=%d normalize = ran:%t report:%s err:%v", seed, iteration, ran, report.String(), err)
		}
		if second := mustJSON(t, normalized); second != first {
			t.Fatalf("seed=%d iteration=%d final-v2 round trip changed bytes", seed, iteration)
		}
	}
}

// testMigrationSet is the private migration set the schema tests register a
// step into. Production owns the v1 -> v2 step; this fixture prepends a v0 ->
// v1 step to exercise a multi-step migration without changing production.
func testMigrationSet() MigrationSet {
	set := ProductionMigrationSet()
	set[0] = testMigrateV0ToV1
	return set
}

// testMigrateV0ToV1 is a representative older-envelope step: it stamps the api
// and schema versions on the document and on every resource, and rebuilds any
// name reservations the document did not persist.
func testMigrateV0ToV1(reg *Registry, _ MigrationEnvironment, _ *MigrationReport) error {
	reg.APIVersion = APIVersion
	reg.SchemaVersion = 1
	for i := range reg.Projects {
		reg.Projects[i].APIVersion = APIVersion
		reg.Projects[i].Kind = KindProject
		reg.Projects[i].Spec.Root = cleanRoot(reg.Projects[i].Spec.Root)
	}
	for i := range reg.Windows {
		reg.Windows[i].APIVersion = APIVersion
		reg.Windows[i].Kind = KindWindow
	}
	for i := range reg.Panes {
		reg.Panes[i].APIVersion = APIVersion
		reg.Panes[i].Kind = KindPane
		if reg.Panes[i].Spec.Role == "" {
			reg.Panes[i].Spec.Role = PaneRoleShell
		}
	}
	for i := range reg.Agents {
		reg.Agents[i].APIVersion = APIVersion
		reg.Agents[i].Kind = KindAgent
		if reg.Agents[i].Status.Phase == "" {
			reg.Agents[i].Status.Phase = PhaseOffline
		}
	}
	reg.rebuildMissingReservations()
	rewriteLegacyReservationScopes(reg)
	return nil
}

func rewriteLegacyReservationScopes(reg *Registry) {
	for i := range reg.NameReservations {
		reservation := &reg.NameReservations[i]
		switch reservation.Kind {
		case KindProject, KindControlSession:
			reservation.Scope = ""
		case KindWindow:
			if resource, ok := reg.Window(reservation.UID); ok {
				reservation.Scope = resource.Metadata.OwnerUID()
			}
		case KindPane:
			if resource, ok := reg.Pane(reservation.UID); ok {
				reservation.Scope = resource.Metadata.OwnerUID()
			}
		case KindAgent:
			if resource, ok := reg.Agent(reservation.UID); ok {
				reservation.Scope = resource.Metadata.OwnerUID()
			}
		}
	}
}

func TestProductionShipsEveryMigrationThroughRootScopedSchemaV4(t *testing.T) {
	t.Parallel()

	if len(productionMigrations) != 3 || productionMigrations[1] == nil || productionMigrations[2] == nil || productionMigrations[3] == nil {
		t.Fatalf("production migrations = %v, want v1 -> v2, v2 -> v3, and v3 -> v4 steps", productionMigrations)
	}
}

func TestClassifySchemaVersionFailsClosedForNewerAndUnversionedEnvelopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		version    int
		wantAction SchemaAction
		wantErr    error
	}{
		{name: "current version reads as is", version: SchemaVersion, wantAction: SchemaCurrent},
		{name: "an unversioned document is rejected fail closed", version: 0, wantAction: SchemaReject, wantErr: ErrSchemaUnsupported},
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

func TestARegisteredOlderStepTurnsRejectionIntoMigrationWithoutChangingProduction(t *testing.T) {
	t.Parallel()

	// With no registered step, version 0 is refused.
	if action, err := ClassifySchemaVersion(0); action != SchemaReject || !errors.Is(err, ErrSchemaUnsupported) {
		t.Fatalf("production classify(0) = %s, %v; want reject", action, err)
	}
	// With a step registered in a private set, the same version migrates.
	action, err := ClassifySchemaVersionWith(testMigrationSet(), 0)
	if err != nil {
		t.Fatalf("classify with an injected step: %v", err)
	}
	if action != SchemaMigrate {
		t.Fatalf("classify with an injected step = %s, want migrate", action)
	}
	// Registering a step in a private set never mutates the production set.
	if len(productionMigrations) != 3 || productionMigrations[1] == nil || productionMigrations[2] == nil || productionMigrations[3] == nil {
		t.Fatalf("production migrations were mutated: %v", productionMigrations)
	}
}

func TestMigrateRegistryRejectsAnUnversionedDocumentWithoutTouchingTheInput(t *testing.T) {
	t.Parallel()

	source := Registry{
		Projects: []Project{{Metadata: ObjectMeta{UID: "project-01", Name: "projmux"}, Spec: ProjectSpec{Root: "/src/projmux"}}},
	}
	if source.SchemaVersion != 0 {
		t.Fatalf("fixture schemaVersion = %d, want the absent-field 0", source.SchemaVersion)
	}

	before := mustJSON(t, source)
	_, ran, err := MigrateRegistry(source)
	if !errors.Is(err, ErrSchemaUnsupported) {
		t.Fatalf("error = %v, want ErrSchemaUnsupported", err)
	}
	if ran {
		t.Fatal("no migration step may run for an unversioned document")
	}
	if got := mustJSON(t, source); got != before {
		t.Fatalf("a rejected migration mutated its input:\nbefore=%s\nafter=%s", before, got)
	}
	if IsUsageError(err) {
		t.Fatalf("an unknown registry document is not user input: %v", err)
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

func TestARegisteredOlderStepLiftsItsDocumentToTheCurrentEnvelope(t *testing.T) {
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
		t.Fatalf("source schemaVersion = %d, want the fixture's older 0", source.SchemaVersion)
	}

	migrated, ran, _, err := MigrateRegistryWithEnvironment(testMigrationSet(), source, migrationGoldenEnvironment())
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
	if migrated.SchemaVersion != SchemaVersion || strings.Contains(got, `"displayName"`) || strings.Contains(got, `"displayTitle"`) {
		t.Fatalf("older envelope did not converge on schema v4 without presentation fields:\n%s", got)
	}

	// The migration is idempotent: re-running it is a no-op.
	again, ranAgain, _, err := MigrateRegistryWithEnvironment(testMigrationSet(), migrated, migrationGoldenEnvironment())
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
	migrated, _, _, err := MigrateRegistryWithEnvironment(testMigrationSet(), source, MigrationEnvironment{NewUID: sequentialUIDs()})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated.Projects[0].Metadata.Name != "projmux" || migrated.Projects[1].Metadata.Name != "projmux-4" {
		t.Fatalf("migration renamed an existing project: %q/%q", migrated.Projects[0].Metadata.Name, migrated.Projects[1].Metadata.Name)
	}
	for _, project := range migrated.Projects {
		if reservationUID, _ := migrated.nameOwner("", KindProject, project.Metadata.Name); reservationUID != project.Metadata.UID {
			t.Fatalf("project %q reservation = %q, want %q", project.Metadata.Name, reservationUID, project.Metadata.UID)
		}
	}
	m := Mutator{NewUID: func(Kind) (string, error) { return "project-03", nil }}
	uid, next, err := m.mintAndReserveName(&migrated, "test", "", KindProject, "")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if uid != "project-03" || next != uid {
		t.Fatalf("uid/name = %q/%q, want exact full uid project-03", uid, next)
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
		{name: "non-empty project without primary window", mutate: func(r *Registry) { r.Projects[0].Spec.PrimaryWindowRef = "" }, wantErr: ErrInvalidRegistry},
		{name: "dangling primary pane", mutate: func(r *Registry) { r.Windows[0].Spec.AnchorPaneRef = "missing" }, wantErr: ErrInvalidRegistry},
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
