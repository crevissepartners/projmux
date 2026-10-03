package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The registry read result fence pins what the envelope read returns for every
// input shape the two-pass decode order can see, so a later rewrite of that
// order has to reproduce it exactly. Pinned values below are literals observed
// initially on main 1e7888ca. Schema v5 deliberately changes the current
// envelope and migration output; its literals were re-observed with that
// storage change. Decode order and refusal categories remain pinned.

// legacyReadWithoutRepairWithReport is a frozen copy of
// (*Store).readWithoutRepairWithReport as of main 1e7888ca: decode the
// schemaVersion envelope, classify it, and only then decode the body. It is the
// differential reference and must not be edited to follow product changes.
func legacyReadWithoutRepairWithReport(s *Store) (coremetadata.Registry, int, bool, coremetadata.MigrationReport, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			registry, version, existed, absentErr := s.absentRegistry("missing")
			return registry, version, existed, coremetadata.MigrationReport{}, absentErr
		}
		if errors.Is(err, fs.ErrPermission) {
			return coremetadata.Registry{}, 0, true, coremetadata.MigrationReport{}, fmt.Errorf("metadata: read registry %s: %w: %w", s.path, ErrRegistryPermission, err)
		}
		return coremetadata.Registry{}, 0, false, coremetadata.MigrationReport{}, fmt.Errorf("metadata: read registry %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		registry, version, existed, absentErr := s.absentRegistry("empty")
		return registry, version, existed, coremetadata.MigrationReport{}, absentErr
	}

	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return coremetadata.Registry{}, 0, true, coremetadata.MigrationReport{}, fmt.Errorf("%w %s: %w", ErrMalformedRegistry, s.path, err)
	}
	if _, err := coremetadata.ClassifySchemaVersionWith(s.migrations, envelope.SchemaVersion); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, coremetadata.MigrationReport{}, fmt.Errorf("metadata: %s: %w", s.path, err)
	}

	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, coremetadata.MigrationReport{}, fmt.Errorf("%w %s: %w", ErrMalformedRegistry, s.path, err)
	}
	migrated, _, report, err := coremetadata.MigrateRegistryWithEnvironment(s.migrations, registry, s.migrationEnv)
	if err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, report, fmt.Errorf("metadata: %s: %w", s.path, err)
	}
	return migrated, envelope.SchemaVersion, true, report, nil
}

// legacyClassifyRegistryBytes is a frozen copy of classifyRegistryBytes as of
// main 1e7888ca, with the same envelope-then-body order. It is the differential
// reference and must not be edited to follow product changes.
func legacyClassifyRegistryBytes(info *RegistryFileInfo, data []byte, migrations coremetadata.MigrationSet) {
	if len(strings.TrimSpace(string(data))) == 0 {
		info.State = RegistryStateEmpty
		info.Detail = fmt.Sprintf("%s holds no content", info.Path)
		return
	}
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		info.State = RegistryStateMalformed
		info.Detail = fmt.Sprintf("%s is not decodable JSON: %v", info.Path, err)
		return
	}
	info.SchemaVersion = envelope.SchemaVersion
	if _, err := coremetadata.ClassifySchemaVersionWith(migrations, envelope.SchemaVersion); err != nil {
		info.State = RegistryStateSchemaTooNew
		info.Detail = fmt.Sprintf("%s: %v", info.Path, err)
		return
	}
	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		info.State = RegistryStateMalformed
		info.Detail = fmt.Sprintf("%s does not decode into a registry: %v", info.Path, err)
		return
	}
	migrated, _, _, err := coremetadata.MigrateRegistryWithEnvironment(migrations, registry, coremetadata.MigrationEnvironment{
		DirectoryExists: DirExists,
		NewUID:          coremetadata.NewUID,
	})
	if err != nil {
		info.State = RegistryStateInvalid
		info.Detail = fmt.Sprintf("%s cannot be migrated to the current schema: %v", info.Path, err)
		return
	}
	if err := migrated.Validate(); err != nil {
		info.State = RegistryStateInvalid
		info.Detail = fmt.Sprintf("%s is not a valid resource graph: %v", info.Path, err)
		return
	}
	info.State = RegistryStateValid
	info.Contents = contentsOf(migrated)
}

// fenceCurrentFields is every top-level field of a valid schema-v5 registry
// except schemaVersion, so rows can place the version key (or its absence,
// duplicates, and case variants) anywhere around the same body. Every root is
// absolute and absent from any machine.
const fenceCurrentFields = `  "apiVersion": "projmux.io/v1alpha1",
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Project", "metadata": {"uid": "project-fence", "name": "fence", "labels": {"fence": "current"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"root": "/projmux-fence-missing/fence", "primaryWindowRef": "window-fence"}, "status": {}}
  ],
  "windows": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Window", "metadata": {"uid": "window-fence", "name": "editor", "ownerRef": {"kind": "Project", "uid": "project-fence"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"anchorPaneRef": "pane-fence", "defaultShellPaneRef": "pane-fence"}}
  ],
  "panes": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Pane", "metadata": {"uid": "pane-fence", "name": "shell", "ownerRef": {"kind": "Window", "uid": "window-fence"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"runtime": {"kind": "tmux"}, "role": "shell", "cwd": "/projmux-fence-missing/fence", "command": "zsh"}, "status": {}}
  ],
  "nameReservations": [
    {"kind": "Project", "name": "fence", "uid": "project-fence"},
    {"scope": "project-fence", "kind": "Pane", "name": "shell", "uid": "pane-fence"},
    {"scope": "project-fence", "kind": "Window", "name": "editor", "uid": "window-fence"}
  ]`

// fenceV1Registry is a valid schema-v1 document whose migration exercises uid
// minting (a Window-less Project and a shell-less Window), a missing shell cwd,
// legacy primaryPaneRef authority, and removed v3 presentation fields.
const fenceV1Registry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 1,
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Project", "metadata": {"uid": "project-v1", "name": "v1", "displayName": "v1 title", "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"root": "/projmux-fence-missing/v1"}, "status": {}},
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Project", "metadata": {"uid": "project-v1-bare", "name": "v1-bare", "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"root": "/projmux-fence-missing/v1-bare"}, "status": {}}
  ],
  "windows": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Window", "metadata": {"uid": "window-v1", "name": "editor", "ownerRef": {"kind": "Project", "uid": "project-v1"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"primaryPaneRef": "pane-v1"}},
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Window", "metadata": {"uid": "window-v1-empty", "name": "empty", "ownerRef": {"kind": "Project", "uid": "project-v1"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {}}
  ],
  "panes": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Pane", "metadata": {"uid": "pane-v1", "name": "shell", "ownerRef": {"kind": "Window", "uid": "window-v1"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"role": "shell", "cwd": "/projmux-fence-missing/v1/worktree", "command": "zsh"}, "status": {"displayTitle": "v1 pane title"}}
  ],
  "nameReservations": [
    {"kind": "Project", "name": "v1", "uid": "project-v1"},
    {"kind": "Project", "name": "v1-bare", "uid": "project-v1-bare"},
    {"scope": "project-v1", "kind": "Window", "name": "editor", "uid": "window-v1"},
    {"scope": "project-v1", "kind": "Window", "name": "empty", "uid": "window-v1-empty"},
    {"scope": "window-v1", "kind": "Pane", "name": "shell", "uid": "pane-v1"}
  ]
}
`

// fenceV2Registry is a valid intermediate schema-v2 document that still names
// its Window anchor through legacy primaryPaneRef.
const fenceV2Registry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 2,
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Project", "metadata": {"uid": "project-v2", "name": "v2", "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"root": "/projmux-fence-missing/v2", "primaryWindowRef": "window-v2"}, "status": {}}
  ],
  "windows": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Window", "metadata": {"uid": "window-v2", "name": "editor", "ownerRef": {"kind": "Project", "uid": "project-v2"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"primaryPaneRef": "pane-v2"}}
  ],
  "panes": [
    {"apiVersion": "projmux.io/v1alpha1", "kind": "Pane", "metadata": {"uid": "pane-v2", "name": "shell", "ownerRef": {"kind": "Window", "uid": "window-v2"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"role": "shell", "cwd": "/projmux-fence-missing/v2", "command": "zsh"}, "status": {}}
  ],
  "nameReservations": [
    {"kind": "Project", "name": "v2", "uid": "project-v2"},
    {"scope": "project-v2", "kind": "Window", "name": "editor", "uid": "window-v2"},
    {"scope": "window-v2", "kind": "Pane", "name": "shell", "uid": "pane-v2"}
  ]
}
`

// fenceLargeProjects keeps the generated document above 1 MiB.
const fenceLargeProjects = 1200

// fenceEnvelope wraps fenceCurrentFields with optional leading and trailing
// top-level members.
func fenceEnvelope(leading, trailing string) []byte {
	var b strings.Builder
	b.WriteString("{\n")
	if leading != "" {
		b.WriteString("  " + leading + ",\n")
	}
	b.WriteString(fenceCurrentFields)
	if trailing != "" {
		b.WriteString(",\n  " + trailing)
	}
	b.WriteString("\n}\n")
	return []byte(b.String())
}

// fenceLargeRegistry deterministically generates a whole-graph-valid registry
// of fenceLargeProjects Projects, each with one Window and one shell Pane.
func fenceLargeRegistry(schemaVersion int) []byte {
	var projects, windows, panes, reservations []string
	for i := range fenceLargeProjects {
		project := fmt.Sprintf("project-%04d", i)
		window := fmt.Sprintf("window-%04d", i)
		pane := fmt.Sprintf("pane-%04d", i)
		root := "/projmux-fence-missing/large/" + project
		projects = append(projects, fmt.Sprintf(`    {"apiVersion": "projmux.io/v1alpha1", "kind": "Project", "metadata": {"uid": %q, "name": %q, "labels": {"fence.projmux.io/index": "%04d"}, "annotations": {"fence.projmux.io/note": "generated large registry row %04d"}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"root": %q, "primaryWindowRef": %q}, "status": {}}`,
			project, project, i, i, root, window))
		windows = append(windows, fmt.Sprintf(`    {"apiVersion": "projmux.io/v1alpha1", "kind": "Window", "metadata": {"uid": %q, "name": "editor", "ownerRef": {"kind": "Project", "uid": %q}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"anchorPaneRef": %q, "defaultShellPaneRef": %q}}`,
			window, project, pane, pane))
		panes = append(panes, fmt.Sprintf(`    {"apiVersion": "projmux.io/v1alpha1", "kind": "Pane", "metadata": {"uid": %q, "name": "shell", "ownerRef": {"kind": "Window", "uid": %q}, "createdAt": "2026-08-15T09:30:00Z"}, "spec": {"role": "shell", "cwd": %q, "command": "zsh"}, "status": {}}`,
			pane, window, root))
		reservations = append(reservations,
			fmt.Sprintf(`    {"kind": "Project", "name": %q, "uid": %q}`, project, project),
			fmt.Sprintf(`    {"scope": %q, "kind": "Pane", "name": "shell", "uid": %q}`, project, pane),
			fmt.Sprintf(`    {"scope": %q, "kind": "Window", "name": "editor", "uid": %q}`, project, window))
	}
	return fmt.Appendf(nil, "{\n  \"apiVersion\": \"projmux.io/v1alpha1\",\n  \"schemaVersion\": %d,\n  \"updatedAt\": \"2026-08-15T09:30:00Z\",\n  \"projects\": [\n%s\n  ],\n  \"windows\": [\n%s\n  ],\n  \"panes\": [\n%s\n  ],\n  \"nameReservations\": [\n%s\n  ]\n}\n",
		schemaVersion, strings.Join(projects, ",\n"), strings.Join(windows, ",\n"), strings.Join(panes, ",\n"), strings.Join(reservations, ",\n"))
}

type registryReadFenceRow struct {
	name string
	data []byte
	// initialized publishes the initialized marker before the read, which is
	// what separates first use from state loss for content-free bytes.
	initialized bool
	large       bool
}

// registryReadFenceRows is the one input matrix shared by the fence tables, the
// differential test, and the fuzz seeds.
func registryReadFenceRows() []registryReadFenceRow {
	current := fenceEnvelope(`"schemaVersion": 5`, "")
	unknown := fenceEnvelope(`"schemaVersion": 6`, "")
	return []registryReadFenceRow{
		{name: "current-v5-valid", data: current},
		{name: "current-v5-valid-initialized", data: current, initialized: true},
		{name: "migration-v1-valid", data: []byte(fenceV1Registry)},
		{name: "migration-v2-valid", data: []byte(fenceV2Registry)},
		{name: "migration-v3-valid", data: []byte(v3RootCollisionRegistry)},
		{name: "newer-v6", data: []byte(newerSchemaRegistry)},
		{name: "negative-version", data: fenceEnvelope(`"schemaVersion": -1`, "")},
		{name: "zero-version", data: fenceEnvelope(`"schemaVersion": 0`, "")},
		{name: "absent-version", data: fenceEnvelope("", "")},
		// A JSON null leaves the int untouched, so it reads exactly like an
		// absent key.
		{name: "null-version", data: fenceEnvelope(`"schemaVersion": null`, "")},
		// A string, a float literal, and an int64 overflow all fail the
		// envelope decode itself, so they are malformed and never classified.
		{name: "string-version", data: fenceEnvelope(`"schemaVersion": "5"`, "")},
		{name: "float-version", data: fenceEnvelope(`"schemaVersion": 5.0`, "")},
		{name: "overflow-version", data: fenceEnvelope(`"schemaVersion": 99999999999999999999`, "")},
		// json.Unmarshal validates the whole input before decoding, so a
		// truncated or trailing-garbage document is malformed even when its
		// version is unknown (C-1 is never reached).
		{name: "truncated-known-version", data: current[:len(current)/2]},
		{name: "truncated-unknown-version", data: unknown[:len(unknown)/2]},
		{name: "trailing-garbage", data: append(bytes.Clone(current), "garbage"...)},
		{name: "trailing-second-document", data: append(bytes.Clone(current), `{"schemaVersion": 6}`...)},
		{name: "version-last-key", data: fenceEnvelope("", `"schemaVersion": 5`)},
		// encoding/json matches object keys case-insensitively and the last
		// matching key wins, for the envelope and the body alike.
		{name: "duplicate-known-then-unknown", data: fenceEnvelope(`"schemaVersion": 5`, `"schemaVersion": 6`)},
		{name: "duplicate-unknown-then-known", data: fenceEnvelope(`"schemaVersion": 6`, `"schemaVersion": 5`)},
		{name: "case-variant-known", data: fenceEnvelope(`"SchemaVersion": 5`, "")},
		{name: "case-variant-unknown", data: fenceEnvelope(`"SCHEMAVERSION": 6`, "")},
		{name: "exact-known-then-case-variant-unknown", data: fenceEnvelope(`"schemaVersion": 5`, `"SCHEMAVERSION": 6`)},
		{name: "case-variant-unknown-then-exact-known", data: fenceEnvelope(`"SCHEMAVERSION": 6`, `"schemaVersion": 5`)},
		{name: "exact-unknown-then-case-variant-known", data: fenceEnvelope(`"schemaVersion": 6`, `"SchemaVersion": 5`)},
		{name: "case-variant-known-then-exact-unknown", data: fenceEnvelope(`"SchemaVersion": 5`, `"schemaVersion": 6`)},
		{name: "child-only-version", data: fenceEnvelope(`"extensions": {"schemaVersion": 5}`, "")},
		{name: "top-level-array", data: []byte(`[{"schemaVersion": 5}]`)},
		{name: "top-level-string", data: []byte(`"schemaVersion"`)},
		{name: "top-level-number", data: []byte(`4`)},
		// A top-level null decodes into the envelope without error, so it is
		// classified as version 0 rather than rejected as malformed.
		{name: "top-level-null", data: []byte(`null`)},
		{name: "utf8-bom", data: append([]byte("\xef\xbb\xbf"), current...)},
		{name: "current-body-type-error", data: []byte(`{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": "x"}`)},
		{name: "migration-v3-body-type-error", data: []byte(`{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 3, "projects": "x"}`)},
		// C-1: an unknown version is refused before the body is decoded, so a
		// body type error behind it must surface as a schema error.
		{name: "unknown-body-type-error", data: []byte(`{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 6, "projects": "x"}`)},
		{name: "current-invalid-graph", data: bytes.Replace(current, []byte(`"ownerRef": {"kind": "Window", "uid": "window-fence"}`), []byte(`"ownerRef": {"kind": "Window", "uid": "window-missing"}`), 1)},
		{name: "migration-v3-invalid-owner-graph", data: []byte(strings.Replace(v3RootCollisionRegistry,
			`"ownerRef":{"kind":"Project","uid":"project-root"}`, `"ownerRef":{"kind":"Project","uid":"missing-root"}`, 1))},
		{name: "empty-first-use", data: []byte{}},
		{name: "empty-initialized", data: []byte{}, initialized: true},
		{name: "whitespace-first-use", data: []byte(" \n\t\r\n")},
		{name: "whitespace-initialized", data: []byte(" \n\t\r\n"), initialized: true},
		{name: "large-current-valid", data: fenceLargeRegistry(5), large: true},
		{name: "large-unknown-version", data: fenceLargeRegistry(6), large: true},
	}
}

// fenceMigrationEnvironment returns a fresh deterministic environment: minted
// uids restart at 01 for every call, and no directory exists.
func fenceMigrationEnvironment() coremetadata.MigrationEnvironment {
	minted := map[coremetadata.Kind]int{}
	return coremetadata.MigrationEnvironment{
		DirectoryExists: func(string) (bool, error) { return false, nil },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			minted[kind]++
			return fmt.Sprintf("%s-fence-minted-%02d", strings.ToLower(string(kind)), minted[kind]), nil
		},
	}
}

// fenceStore seeds a fresh temp store with data and, when asked, the
// initialized marker the store publishes on its first committed write.
func fenceStore(t *testing.T, data []byte, initialized bool) *Store {
	t.Helper()
	store := testStore(t)
	store.migrationEnv = fenceMigrationEnvironment()
	writeRegistryFile(t, store, string(data))
	if initialized {
		if _, err := store.ensureInitializedMarker(); err != nil {
			t.Fatalf("publish initialized marker: %v", err)
		}
	}
	return store
}

// fenceNormalize replaces every temp path this store owns with a placeholder.
func fenceNormalize(store *Store, text string) string {
	dir := filepath.Dir(store.Path())
	return strings.NewReplacer(
		store.markerPath, "<marker>",
		store.recoveryDir, "<recovery>",
		store.Path(), "<registry>",
		dir, "<registry-dir>",
		filepath.Dir(dir), "<state>",
	).Replace(text)
}

func fenceErrorText(store *Store, err error) string {
	if err == nil {
		return ""
	}
	return fenceNormalize(store, err.Error())
}

// fenceDigest is the sha256 of json.Marshal(value), so a zero registry or an
// empty report is pinned as precisely as a populated one.
func fenceDigest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "marshal-error: " + err.Error()
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fenceErrorFacts lists, in a fixed order, every sentinel errors.Is matches and
// every error type errors.As finds. Pinning the matched set pins the unmatched
// complement too.
func fenceErrorFacts(err error) string {
	if err == nil {
		return "nil"
	}
	var facts []string
	for _, sentinel := range []struct {
		name   string
		target error
	}{
		{"ErrMalformedRegistry", ErrMalformedRegistry},
		{"ErrRegistryStateLost", ErrRegistryStateLost},
		{"ErrRegistryPermission", ErrRegistryPermission},
		{"ErrSchemaTooNew", coremetadata.ErrSchemaTooNew},
		{"ErrSchemaUnsupported", coremetadata.ErrSchemaUnsupported},
		{"ErrInvalidRegistry", coremetadata.ErrInvalidRegistry},
	} {
		if errors.Is(err, sentinel.target) {
			facts = append(facts, sentinel.name)
		}
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		facts = append(facts, "*json.SyntaxError")
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		facts = append(facts, "*json.UnmarshalTypeError")
	}
	var stateErr *coremetadata.StateError
	if errors.As(err, &stateErr) {
		facts = append(facts, "*metadata.StateError")
	}
	if len(facts) == 0 {
		return "untyped"
	}
	return strings.Join(facts, " ")
}

const fenceClassifyPath = "<registry>"

func fenceRowsByName(t *testing.T, pinned int) []registryReadFenceRow {
	t.Helper()
	rows := registryReadFenceRows()
	if len(rows) != pinned {
		t.Fatalf("matrix has %d rows but %d are pinned; every row needs a pin", len(rows), pinned)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.name] {
			t.Fatalf("duplicate matrix row %q", row.name)
		}
		seen[row.name] = true
		if row.large && len(row.data) < 1<<20 {
			t.Fatalf("row %q is %d bytes, want at least 1 MiB", row.name, len(row.data))
		}
	}
	return rows
}

type registryReadFencePin struct {
	version  int
	existed  bool
	registry string
	report   string
	facts    string
	err      string
}

func TestRegistryReadResultFence(t *testing.T) {
	t.Parallel()
	for _, row := range fenceRowsByName(t, len(registryReadFencePins)) {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			want, ok := registryReadFencePins[row.name]
			if !ok {
				t.Fatalf("row %q has no pin", row.name)
			}
			store := fenceStore(t, row.data, row.initialized)
			registry, version, existed, report, err := store.readWithoutRepairWithReport()
			got := registryReadFencePin{
				version: version, existed: existed,
				registry: fenceDigest(registry), report: fenceDigest(report),
				facts: fenceErrorFacts(err), err: fenceErrorText(store, err),
			}
			if got != want {
				reportJSON, _ := json.Marshal(report)
				t.Fatalf("read result drifted from main:\n got  %#v\n want %#v\nreport %s", got, want, reportJSON)
			}
		})
	}
}

type registryEntryFenceOutcome struct {
	registry string
	facts    string
	err      string
}

type registryMigrationFenceOutcome struct {
	registry    string
	facts       string
	err         string
	fromVersion int
	migrated    bool
	backupPath  string
	reportPath  string
	report      string
}

type registryEntryFencePin struct {
	degraded  registryEntryFenceOutcome
	readOnly  registryEntryFenceOutcome
	migration registryMigrationFenceOutcome
}

func TestRegistryReadEntryPointResultFence(t *testing.T) {
	t.Parallel()
	for _, row := range fenceRowsByName(t, len(registryEntryFencePins)) {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			want, ok := registryEntryFencePins[row.name]
			if !ok {
				t.Fatalf("row %q has no pin", row.name)
			}
			t.Run("LoadDegradedReadOnly", func(t *testing.T) {
				t.Parallel()
				store := fenceStore(t, row.data, row.initialized)
				registry, err := store.LoadDegradedReadOnly()
				got := registryEntryFenceOutcome{registry: fenceDigest(registry), facts: fenceErrorFacts(err), err: fenceErrorText(store, err)}
				if got != want.degraded {
					t.Fatalf("LoadDegradedReadOnly drifted from main:\n got  %#v\n want %#v", got, want.degraded)
				}
			})
			t.Run("LoadReadOnly", func(t *testing.T) {
				t.Parallel()
				store := fenceStore(t, row.data, row.initialized)
				registry, err := store.LoadReadOnly()
				got := registryEntryFenceOutcome{registry: fenceDigest(registry), facts: fenceErrorFacts(err), err: fenceErrorText(store, err)}
				if got != want.readOnly {
					t.Fatalf("LoadReadOnly drifted from main:\n got  %#v\n want %#v", got, want.readOnly)
				}
			})
			t.Run("LoadWithMigrationResult", func(t *testing.T) {
				t.Parallel()
				store := fenceStore(t, row.data, row.initialized)
				registry, result, err := store.LoadWithMigrationResult()
				got := registryMigrationFenceOutcome{
					registry: fenceDigest(registry), facts: fenceErrorFacts(err), err: fenceErrorText(store, err),
					fromVersion: result.FromVersion, migrated: result.Migrated,
					backupPath: fenceNormalize(store, result.BackupPath), reportPath: fenceNormalize(store, result.ReportPath),
					report: fenceDigest(result.Report),
				}
				if got != want.migration {
					t.Fatalf("LoadWithMigrationResult drifted from main:\n got  %#v\n want %#v", got, want.migration)
				}
			})
		})
	}
}

func TestClassifyRegistryBytesResultFence(t *testing.T) {
	t.Parallel()
	for _, row := range fenceRowsByName(t, len(classifyRegistryBytesFencePins)) {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			want, ok := classifyRegistryBytesFencePins[row.name]
			if !ok {
				t.Fatalf("row %q has no pin", row.name)
			}
			want.Path = fenceClassifyPath
			got := RegistryFileInfo{Path: fenceClassifyPath}
			classifyRegistryBytes(&got, row.data, coremetadata.ProductionMigrationSet())
			if got != want {
				t.Fatalf("classification drifted from main:\n got  %#v\n want %#v", got, want)
			}
		})
	}
}

func TestRegistryReadMatchesLegacyTwoPassReference(t *testing.T) {
	t.Parallel()
	for _, row := range registryReadFenceRows() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			compareRegistryReadWithLegacyTwoPass(t, row.data, row.initialized)
		})
	}
}

func FuzzRegistryReadMatchesLegacyTwoPassReference(f *testing.F) {
	for _, row := range registryReadFenceRows() {
		f.Add(row.data, row.initialized)
	}
	f.Fuzz(func(t *testing.T, data []byte, initialized bool) {
		compareRegistryReadWithLegacyTwoPass(t, data, initialized)
	})
}

// fenceMintedUID matches coremetadata.NewUID output. classifyRegistryBytes mints
// real random uids during a v1 migration, so a Detail that names one cannot be
// compared across two calls byte for byte.
var fenceMintedUID = regexp.MustCompile(`\b(?:proj|win|pane|agent|ctl)-[a-z2-7]{26}\b`)

// compareRegistryReadWithLegacyTwoPass runs the frozen two-pass references and
// the product read and classifier over the same bytes, each with an identically
// reset deterministic environment, and requires identical results.
func compareRegistryReadWithLegacyTwoPass(t *testing.T, data []byte, initialized bool) {
	t.Helper()
	store := fenceStore(t, data, initialized)

	store.migrationEnv = fenceMigrationEnvironment()
	wantRegistry, wantVersion, wantExisted, wantReport, wantErr := legacyReadWithoutRepairWithReport(store)
	store.migrationEnv = fenceMigrationEnvironment()
	gotRegistry, gotVersion, gotExisted, gotReport, gotErr := store.readWithoutRepairWithReport()

	if gotVersion != wantVersion || gotExisted != wantExisted {
		t.Errorf("read version/existed = %d/%t, legacy two-pass = %d/%t", gotVersion, gotExisted, wantVersion, wantExisted)
	}
	if !reflect.DeepEqual(gotRegistry, wantRegistry) {
		t.Errorf("read registry differs from legacy two-pass:\n got  %s\n want %s", fenceJSON(gotRegistry), fenceJSON(wantRegistry))
	}
	if !reflect.DeepEqual(gotReport, wantReport) {
		t.Errorf("read report differs from legacy two-pass:\n got  %s\n want %s", fenceJSON(gotReport), fenceJSON(wantReport))
	}
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || (gotErr == nil) != (wantErr == nil) {
		t.Errorf("read error differs from legacy two-pass:\n got  %v\n want %v", gotErr, wantErr)
	}
	if got, want := fenceErrorFacts(gotErr), fenceErrorFacts(wantErr); got != want {
		t.Errorf("read error facts = %q, legacy two-pass = %q", got, want)
	}

	want := RegistryFileInfo{Path: fenceClassifyPath}
	legacyClassifyRegistryBytes(&want, data, store.migrations)
	got := RegistryFileInfo{Path: fenceClassifyPath}
	classifyRegistryBytes(&got, data, store.migrations)
	want.Detail = fenceMintedUID.ReplaceAllString(want.Detail, "<minted-uid>")
	got.Detail = fenceMintedUID.ReplaceAllString(got.Detail, "<minted-uid>")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("classification differs from legacy two-pass:\n got  %#v\n want %#v", got, want)
	}
}

func fenceJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "marshal-error: " + err.Error()
	}
	if len(data) > 2048 {
		return fmt.Sprintf("%s... (%d bytes, sha256 %s)", data[:2048], len(data), fenceDigest(value))
	}
	return string(data)
}

// Pinned literals shared by many rows, re-observed for schema v5. They are
// deliberately not recomputed from the code under test.
const (
	// fenceZeroRegistry is the digest of coremetadata.Registry{}, the registry
	// every refused read answers.
	fenceZeroRegistry = "04e514cff1341646a8d470e18315231801705cd2d256963e749c6896003d0115"
	// fenceFirstUseRegistry is the digest of coremetadata.NewRegistry().
	fenceFirstUseRegistry = "dcf47ddccf0c347343035e07ac723a5482c76c2cb273618b0634031680f5278d"
	// fenceCurrentRegistry is the digest of the current-v5-valid row's read.
	fenceCurrentRegistry = "e365b21788e9a445f1ff4b31c7b6f2a8fa352a81e805e56cdf313249636b217e"
	// fenceEmptyReport is the digest of coremetadata.MigrationReport{}.
	fenceEmptyReport = "bd77ee8eb79f7c4853af1fd47e24f58af7c8e833eeedd8b9e45ba06dc2c7590d"
	// fenceCurrentReport is the digest of a no-repair 5 -> 5 report.
	fenceCurrentReport = "2d9a848fd8c2a209e3c02f0f1e6cd8a49f69fef7c3e91a4f1e4e2389757247b9"

	fenceFactsNil          = "nil"
	fenceFactsTooNew       = "ErrSchemaTooNew *metadata.StateError"
	fenceFactsUnsupported  = "ErrSchemaUnsupported *metadata.StateError"
	fenceFactsSyntax       = "ErrMalformedRegistry *json.SyntaxError"
	fenceFactsType         = "ErrMalformedRegistry *json.UnmarshalTypeError"
	fenceFactsInvalid      = "ErrInvalidRegistry *metadata.StateError"
	fenceFactsStateLost    = "ErrRegistryStateLost"
	fenceDetailTooNew      = "<registry>: read registry: schemaVersion 6 is newer than the supported version 5; refusing to read or write"
	fenceDetailUnversioned = "<registry>: read registry: registry document has no usable schemaVersion; schemaVersion 5 is the first envelope projmux has ever written, so an unversioned document is refused rather than migrated"
	fenceErrTooNew         = "metadata: <registry>: read registry: schemaVersion 6 is newer than the supported version 5; refusing to read or write"
	fenceErrUnversioned    = "metadata: <registry>: read registry: registry document has no usable schemaVersion; schemaVersion 5 is the first envelope projmux has ever written, so an unversioned document is refused rather than migrated"
	fenceErrStateLostEmpty = "metadata: resource registry is missing after initialization: <marker> records a completed registry write but <registry> is empty; restore a verified copy from <recovery>, or remove the marker to accept an empty registry"
)

var registryReadFencePins = map[string]registryReadFencePin{
	"current-v5-valid": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"current-v5-valid-initialized": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"migration-v1-valid": {version: 1, existed: true, registry: "d1b318deae80503b2eca993623ece890294df35a282256e3cd864eb33749dd9b", report: "1971bfc39531f8652d3d3ebdeee46df3216908edf4fddf694482b6059ecd8511",
		facts: fenceFactsNil, err: ""},
	"migration-v2-valid": {version: 2, existed: true, registry: "1234ecc1c6adec1b264b15acef2b298b157cf114617e2518e6514743a86caaa0", report: "c5eedad792aea2b42c82be5c4956db25fceb02a86a6987cf4ade6ab463092643",
		facts: fenceFactsNil, err: ""},
	"migration-v3-valid": {version: 3, existed: true, registry: "fa2ba992466b3f55ae17ac11d92a073887dd5bea4069069f71d59d3ce41ffc54", report: "9ca5701fd6cfaeef6419d45ecd40b8466bdb59fa1180e3bfde5b33caa6893a51",
		facts: fenceFactsNil, err: ""},
	"newer-v6": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"negative-version": {version: -1, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: "metadata: <registry>: read registry: schemaVersion -1 is negative"},
	"zero-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: fenceErrUnversioned},
	"absent-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: fenceErrUnversioned},
	"null-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: fenceErrUnversioned},
	"string-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field .schemaVersion of type int"},
	"float-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number 5.0 into Go struct field .schemaVersion of type int"},
	"overflow-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number 99999999999999999999 into Go struct field .schemaVersion of type int"},
	"truncated-known-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: unexpected end of JSON input"},
	"truncated-unknown-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: unexpected end of JSON input"},
	"trailing-garbage": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character 'g' after top-level value"},
	"trailing-second-document": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character '{' after top-level value"},
	"version-last-key": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"duplicate-known-then-unknown": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"duplicate-unknown-then-known": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"case-variant-known": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"case-variant-unknown": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"exact-known-then-case-variant-unknown": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"case-variant-unknown-then-exact-known": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"exact-unknown-then-case-variant-known": {version: 5, existed: true, registry: fenceCurrentRegistry, report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"case-variant-known-then-exact-unknown": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"child-only-version": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: fenceErrUnversioned},
	"top-level-array": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal array into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-string": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-number": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-null": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsUnsupported, err: fenceErrUnversioned},
	"utf8-bom": {version: 0, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character 'ï' looking for beginning of value"},
	"current-body-type-error": {version: 5, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
	"migration-v3-body-type-error": {version: 3, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
	"unknown-body-type-error": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
	"current-invalid-graph": {version: 5, existed: true, registry: "bc674fe3a1a6dd5cd51186665274c6a0105c26039f26cfc48bf35144a68bd1c8", report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"migration-v3-invalid-owner-graph": {version: 3, existed: true, registry: fenceZeroRegistry, report: "1da81af93cafeb450218500f5f4d9ffa9319e05e6c5a6d962b3dbf65f28add17",
		facts: fenceFactsInvalid, err: "metadata: <registry>: resolve name scope: cannot resolve Window owner \"missing-root\" to a Project or ControlSession root"},
	"empty-first-use": {version: 5, existed: false, registry: fenceFirstUseRegistry, report: fenceEmptyReport,
		facts: fenceFactsNil, err: ""},
	"empty-initialized": {version: 0, existed: false, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsStateLost, err: fenceErrStateLostEmpty},
	"whitespace-first-use": {version: 5, existed: false, registry: fenceFirstUseRegistry, report: fenceEmptyReport,
		facts: fenceFactsNil, err: ""},
	"whitespace-initialized": {version: 0, existed: false, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsStateLost, err: fenceErrStateLostEmpty},
	"large-current-valid": {version: 5, existed: true, registry: "e128edb3836c554caa642b725f47ab3373c320fa4c958906bad4a63a38c9919b", report: fenceCurrentReport,
		facts: fenceFactsNil, err: ""},
	"large-unknown-version": {version: 6, existed: true, registry: fenceZeroRegistry, report: fenceEmptyReport,
		facts: fenceFactsTooNew, err: fenceErrTooNew},
}

var registryEntryFencePins = map[string]registryEntryFencePin{
	"current-v5-valid": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"current-v5-valid-initialized": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"migration-v1-valid": {
		degraded: registryEntryFenceOutcome{"d1b318deae80503b2eca993623ece890294df35a282256e3cd864eb33749dd9b", fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{"d1b318deae80503b2eca993623ece890294df35a282256e3cd864eb33749dd9b", fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: "d1b318deae80503b2eca993623ece890294df35a282256e3cd864eb33749dd9b", facts: fenceFactsNil, err: "",
			fromVersion: 1, migrated: true, backupPath: "<registry>.v1.20260815T093000Z.bak", reportPath: "<registry>.v1.20260815T093000Z.bak.migration-report.json", report: "1971bfc39531f8652d3d3ebdeee46df3216908edf4fddf694482b6059ecd8511"},
	},
	"migration-v2-valid": {
		degraded: registryEntryFenceOutcome{"1234ecc1c6adec1b264b15acef2b298b157cf114617e2518e6514743a86caaa0", fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{"1234ecc1c6adec1b264b15acef2b298b157cf114617e2518e6514743a86caaa0", fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: "1234ecc1c6adec1b264b15acef2b298b157cf114617e2518e6514743a86caaa0", facts: fenceFactsNil, err: "",
			fromVersion: 2, migrated: true, backupPath: "<registry>.v2.20260815T093000Z.bak", reportPath: "<registry>.v2.20260815T093000Z.bak.migration-report.json", report: "c5eedad792aea2b42c82be5c4956db25fceb02a86a6987cf4ade6ab463092643"},
	},
	"migration-v3-valid": {
		degraded: registryEntryFenceOutcome{"fa2ba992466b3f55ae17ac11d92a073887dd5bea4069069f71d59d3ce41ffc54", fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{"fa2ba992466b3f55ae17ac11d92a073887dd5bea4069069f71d59d3ce41ffc54", fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: "fa2ba992466b3f55ae17ac11d92a073887dd5bea4069069f71d59d3ce41ffc54", facts: fenceFactsNil, err: "",
			fromVersion: 3, migrated: true, backupPath: "<registry>.v3.20260815T093000Z.bak", reportPath: "<registry>.v3.20260815T093000Z.bak.migration-report.json", report: "9ca5701fd6cfaeef6419d45ecd40b8466bdb59fa1180e3bfde5b33caa6893a51"},
	},
	"newer-v6": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"negative-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, "metadata: <registry>: read registry: schemaVersion -1 is negative"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, "metadata: <registry>: read registry: schemaVersion -1 is negative"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: "metadata: <registry>: read registry: schemaVersion -1 is negative",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"zero-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: fenceErrUnversioned,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"absent-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: fenceErrUnversioned,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"null-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: fenceErrUnversioned,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"string-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field .schemaVersion of type int"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field .schemaVersion of type int"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field .schemaVersion of type int",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"float-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number 5.0 into Go struct field .schemaVersion of type int"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number 5.0 into Go struct field .schemaVersion of type int"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number 5.0 into Go struct field .schemaVersion of type int",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"overflow-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number 99999999999999999999 into Go struct field .schemaVersion of type int"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number 99999999999999999999 into Go struct field .schemaVersion of type int"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number 99999999999999999999 into Go struct field .schemaVersion of type int",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"truncated-known-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: unexpected end of JSON input"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: unexpected end of JSON input"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: unexpected end of JSON input",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"truncated-unknown-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: unexpected end of JSON input"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: unexpected end of JSON input"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: unexpected end of JSON input",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"trailing-garbage": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character 'g' after top-level value"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character 'g' after top-level value"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character 'g' after top-level value",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"trailing-second-document": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character '{' after top-level value"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character '{' after top-level value"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character '{' after top-level value",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"version-last-key": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"duplicate-known-then-unknown": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"duplicate-unknown-then-known": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"case-variant-known": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"case-variant-unknown": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"exact-known-then-case-variant-unknown": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"case-variant-unknown-then-exact-known": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"exact-unknown-then-case-variant-known": {
		degraded: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceCurrentRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceCurrentRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"case-variant-known-then-exact-unknown": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"child-only-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: fenceErrUnversioned,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"top-level-array": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal array into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal array into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal array into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"top-level-string": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"top-level-number": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal number into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal number into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"top-level-null": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsUnsupported, fenceErrUnversioned},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsUnsupported, err: fenceErrUnversioned,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"utf8-bom": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character 'ï' looking for beginning of value"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsSyntax, "malformed resource registry JSON <registry>: invalid character 'ï' looking for beginning of value"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsSyntax, err: "malformed resource registry JSON <registry>: invalid character 'ï' looking for beginning of value",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"current-body-type-error": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"migration-v3-body-type-error": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsType, "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsType, err: "malformed resource registry JSON <registry>: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"unknown-body-type-error": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"current-invalid-graph": {
		degraded: registryEntryFenceOutcome{"bc674fe3a1a6dd5cd51186665274c6a0105c26039f26cfc48bf35144a68bd1c8", fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsInvalid, "validate registry: Pane \"shell\" ownerRef \"window-missing\" does not exist"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsInvalid, err: "validate registry: Pane \"shell\" ownerRef \"window-missing\" does not exist",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"migration-v3-invalid-owner-graph": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsInvalid, "metadata: <registry>: resolve name scope: cannot resolve Window owner \"missing-root\" to a Project or ControlSession root"},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsInvalid, "metadata: <registry>: resolve name scope: cannot resolve Window owner \"missing-root\" to a Project or ControlSession root"},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsInvalid, err: "metadata: <registry>: resolve name scope: cannot resolve Window owner \"missing-root\" to a Project or ControlSession root",
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"empty-first-use": {
		degraded: registryEntryFenceOutcome{fenceFirstUseRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceFirstUseRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceFirstUseRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"empty-initialized": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsStateLost, fenceErrStateLostEmpty},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsStateLost, fenceErrStateLostEmpty},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsStateLost, err: fenceErrStateLostEmpty,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"whitespace-first-use": {
		degraded: registryEntryFenceOutcome{fenceFirstUseRegistry, fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{fenceFirstUseRegistry, fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: fenceFirstUseRegistry, facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"whitespace-initialized": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsStateLost, fenceErrStateLostEmpty},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsStateLost, fenceErrStateLostEmpty},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsStateLost, err: fenceErrStateLostEmpty,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
	"large-current-valid": {
		degraded: registryEntryFenceOutcome{"e128edb3836c554caa642b725f47ab3373c320fa4c958906bad4a63a38c9919b", fenceFactsNil, ""},
		readOnly: registryEntryFenceOutcome{"e128edb3836c554caa642b725f47ab3373c320fa4c958906bad4a63a38c9919b", fenceFactsNil, ""},
		migration: registryMigrationFenceOutcome{registry: "e128edb3836c554caa642b725f47ab3373c320fa4c958906bad4a63a38c9919b", facts: fenceFactsNil, err: "",
			fromVersion: 5, migrated: false, backupPath: "", reportPath: "", report: fenceCurrentReport},
	},
	"large-unknown-version": {
		degraded: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		readOnly: registryEntryFenceOutcome{fenceZeroRegistry, fenceFactsTooNew, fenceErrTooNew},
		migration: registryMigrationFenceOutcome{registry: fenceZeroRegistry, facts: fenceFactsTooNew, err: fenceErrTooNew,
			fromVersion: 0, migrated: false, backupPath: "", reportPath: "", report: fenceEmptyReport},
	},
}

var classifyRegistryBytesFencePins = map[string]RegistryFileInfo{
	"current-v5-valid": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"current-v5-valid-initialized": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"migration-v1-valid": {State: RegistryStateValid, SchemaVersion: 1,
		Contents: RegistryContents{Projects: 2, Windows: 3, Panes: 3, Agents: 0, Reservations: 8}},
	"migration-v2-valid": {State: RegistryStateValid, SchemaVersion: 2,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"migration-v3-valid": {State: RegistryStateValid, SchemaVersion: 3,
		Contents: RegistryContents{Projects: 1, Windows: 2, Panes: 2, Agents: 0, Reservations: 5}},
	"newer-v6": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"negative-version": {State: RegistryStateSchemaTooNew, SchemaVersion: -1,
		Detail: "<registry>: read registry: schemaVersion -1 is negative"},
	"zero-version": {State: RegistryStateSchemaTooNew, SchemaVersion: 0,
		Detail: fenceDetailUnversioned},
	"absent-version": {State: RegistryStateSchemaTooNew, SchemaVersion: 0,
		Detail: fenceDetailUnversioned},
	"null-version": {State: RegistryStateSchemaTooNew, SchemaVersion: 0,
		Detail: fenceDetailUnversioned},
	"string-version": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal string into Go struct field .schemaVersion of type int"},
	"float-version": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal number 5.0 into Go struct field .schemaVersion of type int"},
	"overflow-version": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal number 99999999999999999999 into Go struct field .schemaVersion of type int"},
	"truncated-known-version": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: unexpected end of JSON input"},
	"truncated-unknown-version": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: unexpected end of JSON input"},
	"trailing-garbage": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: invalid character 'g' after top-level value"},
	"trailing-second-document": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: invalid character '{' after top-level value"},
	"version-last-key": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"duplicate-known-then-unknown": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"duplicate-unknown-then-known": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"case-variant-known": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"case-variant-unknown": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"exact-known-then-case-variant-unknown": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"case-variant-unknown-then-exact-known": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"exact-unknown-then-case-variant-known": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1, Windows: 1, Panes: 1, Agents: 0, Reservations: 3}},
	"case-variant-known-then-exact-unknown": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"child-only-version": {State: RegistryStateSchemaTooNew, SchemaVersion: 0,
		Detail: fenceDetailUnversioned},
	"top-level-array": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal array into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-string": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal string into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-number": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: json: cannot unmarshal number into Go value of type struct { SchemaVersion int \"json:\\\"schemaVersion\\\"\" }"},
	"top-level-null": {State: RegistryStateSchemaTooNew, SchemaVersion: 0,
		Detail: fenceDetailUnversioned},
	"utf8-bom": {State: RegistryStateMalformed, SchemaVersion: 0,
		Detail: "<registry> is not decodable JSON: invalid character 'ï' looking for beginning of value"},
	"current-body-type-error": {State: RegistryStateMalformed, SchemaVersion: 5,
		Detail: "<registry> does not decode into a registry: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
	"migration-v3-body-type-error": {State: RegistryStateMalformed, SchemaVersion: 3,
		Detail: "<registry> does not decode into a registry: json: cannot unmarshal string into Go struct field Registry.projects of type []metadata.Project"},
	"unknown-body-type-error": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
	"current-invalid-graph": {State: RegistryStateInvalid, SchemaVersion: 5,
		Detail: "<registry> is not a valid resource graph: validate registry: Pane \"shell\" ownerRef \"window-missing\" does not exist"},
	"migration-v3-invalid-owner-graph": {State: RegistryStateInvalid, SchemaVersion: 3,
		Detail: "<registry> cannot be migrated to the current schema: resolve name scope: cannot resolve Window owner \"missing-root\" to a Project or ControlSession root"},
	"empty-first-use": {State: RegistryStateEmpty, SchemaVersion: 0,
		Detail: "<registry> holds no content"},
	"empty-initialized": {State: RegistryStateEmpty, SchemaVersion: 0,
		Detail: "<registry> holds no content"},
	"whitespace-first-use": {State: RegistryStateEmpty, SchemaVersion: 0,
		Detail: "<registry> holds no content"},
	"whitespace-initialized": {State: RegistryStateEmpty, SchemaVersion: 0,
		Detail: "<registry> holds no content"},
	"large-current-valid": {State: RegistryStateValid, SchemaVersion: 5,
		Contents: RegistryContents{Projects: 1200, Windows: 1200, Panes: 1200, Agents: 0, Reservations: 3600}},
	"large-unknown-version": {State: RegistryStateSchemaTooNew, SchemaVersion: 6,
		Detail: fenceDetailTooNew},
}
