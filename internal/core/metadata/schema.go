package metadata

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"sort"
	"strings"
)

// SchemaAction is the read decision for one on-disk registry envelope.
type SchemaAction int

const (
	// SchemaCurrent means the envelope already matches SchemaVersion.
	SchemaCurrent SchemaAction = iota
	// SchemaMigrate means a known older envelope must be migrated forward
	// before it can be used.
	SchemaMigrate
	// SchemaReject means the envelope cannot be read. The caller must fail
	// closed and perform no write at all.
	SchemaReject
)

// String renders the action for diagnostics.
func (a SchemaAction) String() string {
	switch a {
	case SchemaCurrent:
		return "current"
	case SchemaMigrate:
		return "migrate"
	case SchemaReject:
		return "reject"
	default:
		return fmt.Sprintf("SchemaAction(%d)", int(a))
	}
}

// MigrationEnvironment supplies the only machine-dependent inputs a schema
// repair may use. Keeping them injected leaves this package pure and gives the
// production adapter and byte-fixed golden tests the exact same algorithm.
type MigrationEnvironment struct {
	DirectoryExists func(string) (bool, error)
	NewUID          func(Kind) (string, error)
}

// MigrationRepair is one stable, operator-reportable change made while an old
// envelope is lifted. InformationLoss distinguishes replacement of stale
// declared data from additive repair.
type MigrationRepair struct {
	Action          string `json:"action"`
	Kind            Kind   `json:"kind"`
	UID             string `json:"uid"`
	Field           string `json:"field"`
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
	InformationLoss bool   `json:"informationLoss"`
}

// MigrationReport records every repair made by a migration in Registry order.
type MigrationReport struct {
	FromVersion   int
	ToVersion     int
	Repairs       []MigrationRepair
	NameRepairs   []MigrationNameRepair
	FieldRemovals []MigrationFieldRemoval
}

// MigrationNameRepair is one v3 address canonicalization. The stable tuple is
// sorted by rootOwnerUID, kind, uid, oldName, newName, reason.
type MigrationNameRepair struct {
	RootOwnerUID string `json:"rootOwnerUid"`
	Kind         Kind   `json:"kind"`
	UID          string `json:"uid"`
	OldName      string `json:"oldName"`
	NewName      string `json:"newName"`
	Reason       string `json:"reason"`
}

// MigrationFieldRemoval is a content-free receipt for one removed schema-v3
// presentation key. The source bytes themselves never enter a report.
type MigrationFieldRemoval struct {
	Kind            Kind   `json:"kind"`
	UID             string `json:"uid"`
	Field           string `json:"field"`
	Present         bool   `json:"present"`
	ByteLength      int    `json:"byteLength"`
	SHA256          string `json:"sha256"`
	InformationLoss bool   `json:"informationLoss"`
}

// RepairCount returns every topology repair, name canonicalization, and
// removed presentation key represented by the report.
func (r MigrationReport) RepairCount() int {
	return len(r.Repairs) + len(r.NameRepairs) + len(r.FieldRemovals)
}

// InformationLossCount returns the number of repairs that replaced declared
// legacy information rather than only adding a missing invariant field.
func (r MigrationReport) InformationLossCount() int {
	count := 0
	for _, repair := range r.Repairs {
		if repair.InformationLoss {
			count++
		}
	}
	for _, removal := range r.FieldRemovals {
		if removal.InformationLoss {
			count++
		}
	}
	return count
}

// String renders a deterministic report suitable for an operator-facing
// migration result or archived test evidence.
func (r MigrationReport) String() string {
	var out strings.Builder
	fmt.Fprintf(&out, "registry schema migration %d -> %d: repairs=%d information-loss=%d", r.FromVersion, r.ToVersion, r.RepairCount(), r.InformationLossCount())
	for _, repair := range r.Repairs {
		fmt.Fprintf(&out, "\n- %s %s %s %s: %q -> %q (information-loss=%t)", repair.Action, repair.Kind, repair.UID, repair.Field, repair.From, repair.To, repair.InformationLoss)
	}
	for _, repair := range r.NameRepairs {
		fmt.Fprintf(&out, "\n- canonicalize-name %s %s %s: %q -> %q (%s)", repair.RootOwnerUID, repair.Kind, repair.UID, repair.OldName, repair.NewName, repair.Reason)
	}
	for _, removal := range r.FieldRemovals {
		fmt.Fprintf(&out, "\n- remove-field %s %s %s: present=%t bytes=%d sha256=%s (information-loss=%t)", removal.Kind, removal.UID, removal.Field, removal.Present, removal.ByteLength, removal.SHA256, removal.InformationLoss)
	}
	return out.String()
}

// Migration lifts one registry document from an envelope version to the next.
type Migration func(*Registry, MigrationEnvironment, *MigrationReport) error

// MigrationSet maps a source envelope version onto the step that lifts it to
// the next version.
type MigrationSet map[int]Migration

// productionMigrations recognizes every shipped Registry envelope older than
// the current one. Version 0 remains unknown: a file with no schemaVersion is
// never rewritten.
var productionMigrations = MigrationSet{
	1: migrateV1ToV2,
	2: migrateV2ToV3,
	3: migrateV3ToV4,
}

// resolveMigrations treats a nil set as the production set, so callers that do
// not override migrations automatically track whatever production ships.
func resolveMigrations(set MigrationSet) MigrationSet {
	if set == nil {
		return productionMigrations
	}
	return set
}

// ProductionMigrationSet returns a copy of the shipped migration registry.
// Integration tests extend this copy with a pre-v1 fixture step without
// replacing the production v1 repair they are meant to exercise.
func ProductionMigrationSet() MigrationSet {
	out := make(MigrationSet, len(productionMigrations))
	maps.Copy(out, productionMigrations)
	return out
}

// ClassifySchemaVersion decides how to read an envelope version using the
// production migration set.
func ClassifySchemaVersion(version int) (SchemaAction, error) {
	return ClassifySchemaVersionWith(nil, version)
}

// ClassifySchemaVersionWith decides how to read an envelope version against an
// explicit migration set. A nil set means the production set.
//
// Everything that is not the current version and not covered by a registered
// migration step is rejected fail-closed: the caller must not quarantine,
// reset, truncate, or otherwise write the file. A newer version would destroy
// state a newer build owns; an unversioned or otherwise unknown document is
// not proven to be a projmux registry at all. Downgrade writes are
// unsupported.
func ClassifySchemaVersionWith(set MigrationSet, version int) (SchemaAction, error) {
	set = resolveMigrations(set)
	switch {
	case version == SchemaVersion:
		return SchemaCurrent, nil
	case version > SchemaVersion:
		return SchemaReject, stateErr("read registry", ErrSchemaTooNew,
			"schemaVersion %d is newer than the supported version %d; refusing to read or write", version, SchemaVersion)
	case version < 0:
		return SchemaReject, stateErr("read registry", ErrSchemaUnsupported, "schemaVersion %d is negative", version)
	}
	for probe := version; probe < SchemaVersion; probe++ {
		if _, ok := set[probe]; !ok {
			if probe == 0 {
				return SchemaReject, stateErr("read registry", ErrSchemaUnsupported,
					"registry document has no usable schemaVersion; schemaVersion %d is the first envelope projmux has ever written, so an unversioned document is refused rather than migrated", SchemaVersion)
			}
			return SchemaReject, stateErr("read registry", ErrSchemaUnsupported,
				"no migration step from schemaVersion %d to %d", probe, probe+1)
		}
	}
	return SchemaMigrate, nil
}

// MigrateRegistry lifts reg to the current schema version using the production
// migration set.
func MigrateRegistry(reg Registry) (Registry, bool, error) {
	migrated, ran, _, err := MigrateRegistryWithEnvironment(nil, reg, MigrationEnvironment{})
	return migrated, ran, err
}

// MigrateRegistryWithEnvironment lifts reg with an explicit migration set,
// filesystem and uid inputs, and the complete repair report.
func MigrateRegistryWithEnvironment(set MigrationSet, reg Registry, env MigrationEnvironment) (Registry, bool, MigrationReport, error) {
	set = resolveMigrations(set)
	report := MigrationReport{FromVersion: reg.SchemaVersion, ToVersion: reg.SchemaVersion}
	action, err := ClassifySchemaVersionWith(set, reg.SchemaVersion)
	if err != nil {
		return Registry{}, false, report, err
	}
	working := reg.Clone()
	shapeNormalized, err := normalizeWindowSpecShapes(&working, &report)
	if err != nil {
		return Registry{}, false, report, err
	}
	if action == SchemaCurrent {
		return working.normalized(), shapeNormalized, report, nil
	}

	for working.SchemaVersion < SchemaVersion {
		step, ok := set[working.SchemaVersion]
		if !ok {
			return Registry{}, false, report, stateErr("migrate registry", ErrSchemaUnsupported,
				"no migration step from schemaVersion %d", working.SchemaVersion)
		}
		if err := step(&working, env, &report); err != nil {
			return Registry{}, false, report, err
		}
	}
	report.ToVersion = working.SchemaVersion
	return working.normalized(), true, report, nil
}

// normalizeWindowSpecShapes performs the same-version prerelease cutover by
// raw field presence. A document has exactly one authority: every decoded
// Window is either intermediate primaryPaneRef or final anchorPaneRef. Mixing
// those authorities, including within one Window, is refused rather than
// dual-read or guessed from Pane role.
func normalizeWindowSpecShapes(reg *Registry, report *MigrationReport) (bool, error) {
	legacyCount := 0
	finalCount := 0
	for _, window := range reg.Windows {
		switch window.Spec.sourceShape {
		case windowSpecSourceLegacy:
			if reg.SchemaVersion == 2 {
				legacyCount++
			} else if reg.SchemaVersion >= 3 {
				return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
					"schemaVersion %d window %q uses legacy primaryPaneRef authority", reg.SchemaVersion, window.Metadata.Name)
			}
		case windowSpecSourceFinal:
			finalCount++
		case windowSpecSourceMixed:
			return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
				"window %q mixes legacy primaryPaneRef with final anchorPaneRef/defaultShellPaneRef authority", window.Metadata.Name)
		case windowSpecSourceUnknown:
			if reg.SchemaVersion >= 2 {
				return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
					"window %q has neither legacy primaryPaneRef nor required final anchorPaneRef", window.Metadata.Name)
			}
		case windowSpecSourceTyped:
			if strings.TrimSpace(window.Spec.AnchorPaneRef) != "" {
				finalCount++
			} else if reg.SchemaVersion >= 2 {
				return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
					"window %q has no required anchorPaneRef", window.Metadata.Name)
			}
		}
	}
	if reg.SchemaVersion == 2 && legacyCount != 0 && finalCount != 0 {
		return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
			"registry mixes legacy primaryPaneRef Windows with final anchorPaneRef Windows")
	}
	if reg.SchemaVersion == 1 && finalCount != 0 {
		// Programmatically constructed v1 fixtures use typed fields and carry no
		// raw source marker. Only a decoded final-v2 field in a v1 envelope is a
		// cross-version mixed authority.
		for _, window := range reg.Windows {
			if window.Spec.sourceShape == windowSpecSourceFinal {
				return false, stateErr("normalize registry window schema", ErrInvalidRegistry,
					"schemaVersion 1 window %q uses final-v2 anchorPaneRef authority", window.Metadata.Name)
			}
		}
	}
	if legacyCount == 0 {
		return false, nil
	}
	for i := range reg.Windows {
		window := &reg.Windows[i]
		if window.Spec.sourceShape != windowSpecSourceLegacy {
			continue
		}
		legacy := window.Spec.legacyPrimaryPaneRef
		window.Spec.AnchorPaneRef = legacy
		window.Spec.DefaultShellPaneRef = legacy
		window.Spec.legacyPrimaryPaneRef = ""
		window.Spec.sourceShape = windowSpecSourceFinal
		report.Repairs = append(report.Repairs, MigrationRepair{
			Action: "normalize-window-anchor-schema", Kind: KindWindow, UID: window.Metadata.UID,
			Field: "spec.primaryPaneRef->spec.anchorPaneRef+spec.defaultShellPaneRef", From: legacy, To: legacy,
		})
	}
	return true, nil
}

func migrateV1ToV2(reg *Registry, env MigrationEnvironment, report *MigrationReport) error {
	const op = "migrate registry v1 to v2"
	if reg.SchemaVersion != 1 {
		return stateErr(op, ErrSchemaUnsupported, "source schemaVersion %d is not 1", reg.SchemaVersion)
	}

	// A Project with no Window cannot name a canonical shell chain. Add the
	// smallest possible topology without changing any existing resource.
	for i := range reg.Projects {
		project := &reg.Projects[i]
		if len(reg.WindowsOf(project.Metadata.UID)) != 0 {
			continue
		}
		uid, err := migrationUID(reg, env, KindWindow)
		if err != nil {
			return err
		}
		reg.putReservation(project.Metadata.UID, KindWindow, uid, uid)
		reg.Windows = append(reg.Windows, Window{
			APIVersion: APIVersion,
			Kind:       KindWindow,
			Metadata: ObjectMeta{
				UID:       uid,
				Name:      uid,
				OwnerRef:  &OwnerRef{Kind: KindProject, UID: project.Metadata.UID},
				CreatedAt: project.Metadata.CreatedAt,
			},
		})
		report.Repairs = append(report.Repairs, MigrationRepair{Action: "create-canonical-window", Kind: KindWindow, UID: uid, Field: "resource", To: uid})
	}

	for i := range reg.Windows {
		window := &reg.Windows[i]
		project, hasProject := projectOwningWindow(reg, *window)

		// Only Project materialization consumes stored shell cwd. Empty cwd
		// already means the Project root and is left byte-semantically intact.
		for j := range reg.Panes {
			pane := &reg.Panes[j]
			if !hasProject || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindWindow ||
				pane.Metadata.OwnerRef.UID != window.Metadata.UID || pane.Spec.Role != PaneRoleShell ||
				strings.TrimSpace(pane.Spec.CWD) == "" {
				continue
			}
			exists, err := migrationDirectoryExists(env, pane.Spec.CWD)
			if err != nil {
				return fmt.Errorf("%s: inspect Pane %q cwd %q: %w", op, pane.Metadata.Name, pane.Spec.CWD, err)
			}
			if exists {
				continue
			}
			from := pane.Spec.CWD
			pane.Spec.CWD = project.Spec.Root
			report.Repairs = append(report.Repairs, MigrationRepair{Action: "downgrade-missing-cwd", Kind: KindPane, UID: pane.Metadata.UID, Field: "spec.cwd", From: from, To: project.Spec.Root, InformationLoss: true})
		}

		if validWindowPrimary(reg, *window) {
			primaryUID := window.Spec.migrationShellPaneRef()
			window.Spec.AnchorPaneRef = primaryUID
			window.Spec.DefaultShellPaneRef = primaryUID
			window.Spec.legacyPrimaryPaneRef = ""
			window.Spec.sourceShape = windowSpecSourceTyped
			report.Repairs = append(report.Repairs, MigrationRepair{
				Action: "migrate-window-anchor", Kind: KindWindow, UID: window.Metadata.UID,
				Field: "spec.primaryPaneRef->spec.anchorPaneRef+spec.defaultShellPaneRef", From: primaryUID, To: primaryUID,
			})
			continue
		}
		from := window.Spec.migrationShellPaneRef()
		primaryUID := firstWindowOwnedShellUID(reg, window.Metadata.UID)
		if primaryUID == "" {
			uid, err := migrationUID(reg, env, KindPane)
			if err != nil {
				return err
			}
			reg.putReservation(window.Metadata.UID, KindPane, uid, uid)
			cwd := ""
			if hasProject {
				cwd = project.Spec.Root
			}
			reg.Panes = append(reg.Panes, Pane{
				APIVersion: APIVersion,
				Kind:       KindPane,
				Metadata: ObjectMeta{
					UID:       uid,
					Name:      uid,
					OwnerRef:  &OwnerRef{Kind: KindWindow, UID: window.Metadata.UID},
					CreatedAt: window.Metadata.CreatedAt,
				},
				Spec: PaneSpec{Role: PaneRoleShell, CWD: cwd},
			})
			primaryUID = uid
			report.Repairs = append(report.Repairs, MigrationRepair{Action: "create-bare-shell", Kind: KindPane, UID: uid, Field: "resource", To: uid})
		}
		window.Spec.AnchorPaneRef = primaryUID
		window.Spec.DefaultShellPaneRef = primaryUID
		window.Spec.legacyPrimaryPaneRef = ""
		window.Spec.sourceShape = windowSpecSourceTyped
		report.Repairs = append(report.Repairs, MigrationRepair{
			Action: "repair-window-anchor", Kind: KindWindow, UID: window.Metadata.UID,
			Field: "spec.anchorPaneRef+spec.defaultShellPaneRef", From: from, To: primaryUID, InformationLoss: strings.TrimSpace(from) != "",
		})
	}

	for i := range reg.Projects {
		project := &reg.Projects[i]
		if validProjectPrimary(reg, *project) {
			continue
		}
		from := project.Spec.PrimaryWindowRef
		to := ""
		for _, window := range reg.Windows {
			if window.Metadata.OwnerRef != nil && window.Metadata.OwnerRef.Kind == KindProject &&
				window.Metadata.OwnerRef.UID == project.Metadata.UID && validWindowPrimary(reg, window) {
				to = window.Metadata.UID
				break
			}
		}
		if to == "" {
			return stateErr(op, ErrInvalidRegistry, "project %q has no valid Window-owned shell chain after repair", project.Metadata.Name)
		}
		project.Spec.PrimaryWindowRef = to
		report.Repairs = append(report.Repairs, MigrationRepair{
			Action: "select-primary-window", Kind: KindProject, UID: project.Metadata.UID,
			Field: "spec.primaryWindowRef", From: from, To: to, InformationLoss: strings.TrimSpace(from) != "",
		})
	}

	reg.SchemaVersion = 2
	return nil
}

// migrateV2ToV3 is intentionally information-lossless. Version 3 closes the
// termination vocabulary over interrupted/control-action evidence; every v2
// document already has the same resource shape and therefore only needs the
// envelope advanced. The integration store still backs up the exact v2 bytes
// and writes the migration report before replacing them.
func migrateV2ToV3(reg *Registry, _ MigrationEnvironment, _ *MigrationReport) error {
	if reg.SchemaVersion != 2 {
		return stateErr("migrate registry v2 to v3", ErrInvalidRegistry,
			"source schemaVersion %d is not 2", reg.SchemaVersion)
	}
	reg.SchemaVersion = 3
	return nil
}

const (
	migrationReasonDuplicateGroup     = "duplicate-group"
	migrationReasonDestinationClosure = "destination-closure"
	migrationReasonAlreadyCanonical   = "already-canonical"
)

type migrationResource struct {
	root string
	kind Kind
	uid  string
	name *string
	meta *ObjectMeta
	pane *PaneStatus
}

// migrateV3ToV4 atomically changes the address authority, removes persisted
// presentation, and reconstructs every reservation from the owner graph.
func migrateV3ToV4(reg *Registry, _ MigrationEnvironment, report *MigrationReport) error {
	const op = "migrate registry v3 to v4"
	if reg.SchemaVersion != 3 {
		return stateErr(op, ErrSchemaUnsupported, "source schemaVersion %d is not 3", reg.SchemaVersion)
	}
	if err := validateV3MigrationSource(*reg); err != nil {
		return err
	}
	resources, err := migrationResources(reg)
	if err != nil {
		return err
	}
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		if a.root != b.root {
			return a.root < b.root
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.uid < b.uid
	})

	type addressKey struct {
		root string
		kind Kind
		name string
	}
	groups := make(map[addressKey][]int)
	for i, resource := range resources {
		groups[addressKey{resource.root, resource.kind, *resource.name}] = append(groups[addressKey{resource.root, resource.kind, *resource.name}], i)
	}
	reasons := make(map[int]string)
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		for _, index := range members {
			reasons[index] = migrationReasonDuplicateGroup
		}
	}
	for {
		changed := false
		for index := range reasons {
			resource := resources[index]
			for _, occupant := range groups[addressKey{resource.root, resource.kind, resource.uid}] {
				if _, exists := reasons[occupant]; exists {
					continue
				}
				reasons[occupant] = migrationReasonDestinationClosure
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for index, reason := range reasons {
		resource := resources[index]
		oldName := *resource.name
		if oldName == resource.uid {
			reason = migrationReasonAlreadyCanonical
		}
		report.NameRepairs = append(report.NameRepairs, MigrationNameRepair{
			RootOwnerUID: resource.root, Kind: resource.kind, UID: resource.uid,
			OldName: oldName, NewName: resource.uid, Reason: reason,
		})
	}
	sort.Slice(report.NameRepairs, func(i, j int) bool {
		a, b := report.NameRepairs[i], report.NameRepairs[j]
		if a.RootOwnerUID != b.RootOwnerUID {
			return a.RootOwnerUID < b.RootOwnerUID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		if a.OldName != b.OldName {
			return a.OldName < b.OldName
		}
		if a.NewName != b.NewName {
			return a.NewName < b.NewName
		}
		return a.Reason < b.Reason
	})
	for index := range reasons {
		*resources[index].name = resources[index].uid
	}

	for _, resource := range resources {
		appendRemovedPresentation(report, resource.kind, resource.uid, "metadata.displayName", resource.meta.removedDisplayName)
		resource.meta.removedDisplayName = removedPresentation{}
		if resource.pane != nil {
			appendRemovedPresentation(report, resource.kind, resource.uid, "status.displayTitle", resource.pane.removedDisplayTitle)
			resource.pane.removedDisplayTitle = removedPresentation{}
		}
	}
	sort.Slice(report.FieldRemovals, func(i, j int) bool {
		a, b := report.FieldRemovals[i], report.FieldRemovals[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		return a.Field < b.Field
	})
	reg.rebuildAllReservations()
	reg.SchemaVersion = 4
	return nil
}

func appendRemovedPresentation(report *MigrationReport, kind Kind, uid, field string, removed removedPresentation) {
	if !removed.present {
		return
	}
	digest := sha256.Sum256([]byte(removed.value))
	report.FieldRemovals = append(report.FieldRemovals, MigrationFieldRemoval{
		Kind: kind, UID: uid, Field: field, Present: true,
		ByteLength: len([]byte(removed.value)), SHA256: fmt.Sprintf("%x", digest),
		InformationLoss: len([]byte(removed.value)) > 0,
	})
}

func migrationResources(reg *Registry) ([]migrationResource, error) {
	resources := make([]migrationResource, 0, len(reg.Projects)+len(reg.ControlSessions)+len(reg.Windows)+len(reg.Panes)+len(reg.Agents))
	for i := range reg.Projects {
		p := &reg.Projects[i]
		resources = append(resources, migrationResource{kind: KindProject, uid: p.Metadata.UID, name: &p.Metadata.Name, meta: &p.Metadata})
	}
	for i := range reg.ControlSessions {
		c := &reg.ControlSessions[i]
		resources = append(resources, migrationResource{kind: KindControlSession, uid: c.Metadata.UID, name: &c.Metadata.Name, meta: &c.Metadata})
	}
	for i := range reg.Windows {
		w := &reg.Windows[i]
		root, err := reg.scopeFor(KindWindow, w.Metadata.OwnerUID())
		if err != nil {
			return nil, err
		}
		resources = append(resources, migrationResource{root: root, kind: KindWindow, uid: w.Metadata.UID, name: &w.Metadata.Name, meta: &w.Metadata})
	}
	for i := range reg.Panes {
		p := &reg.Panes[i]
		root, err := reg.scopeFor(KindPane, p.Metadata.OwnerUID())
		if err != nil {
			return nil, err
		}
		resources = append(resources, migrationResource{root: root, kind: KindPane, uid: p.Metadata.UID, name: &p.Metadata.Name, meta: &p.Metadata, pane: &p.Status})
	}
	for i := range reg.Agents {
		a := &reg.Agents[i]
		root, err := reg.scopeFor(KindAgent, a.Metadata.OwnerUID())
		if err != nil {
			return nil, err
		}
		resources = append(resources, migrationResource{root: root, kind: KindAgent, uid: a.Metadata.UID, name: &a.Metadata.Name, meta: &a.Metadata})
	}
	return resources, nil
}

// validateV3MigrationSource proves the v3 graph and direct-owner reservation
// authority before any canonicalization. It deliberately does not apply v4
// root-wide uniqueness to the source document.
func validateV3MigrationSource(reg Registry) error {
	const op = "migrate registry v3 to v4"
	for _, resource := range allResourceMeta(reg) {
		if err := ValidateName(resource.Name); err != nil {
			return err
		}
	}
	probe := reg.Clone()
	probe.SchemaVersion = SchemaVersion
	probe.NameReservations = nil
	resources, err := migrationResources(&probe)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if err := ValidateName(resource.uid); err != nil {
			return stateErr(op, ErrInvalidRegistry, "%s uid %q cannot be its exact v4 name", resource.kind, resource.uid)
		}
		*resource.name = resource.uid
		resource.meta.removedDisplayName = removedPresentation{}
		if resource.pane != nil {
			resource.pane.removedDisplayTitle = removedPresentation{}
		}
		probe.putReservation(resource.root, resource.kind, resource.uid, resource.uid)
	}
	if err := probe.Validate(); err != nil {
		return stateErr(op, ErrInvalidRegistry, "source owner graph is invalid: %v", err)
	}
	return nil
}

func allResourceMeta(reg Registry) []ObjectMeta {
	out := make([]ObjectMeta, 0, len(reg.Projects)+len(reg.ControlSessions)+len(reg.Windows)+len(reg.Panes)+len(reg.Agents))
	for _, v := range reg.Projects {
		out = append(out, v.Metadata)
	}
	for _, v := range reg.ControlSessions {
		out = append(out, v.Metadata)
	}
	for _, v := range reg.Windows {
		out = append(out, v.Metadata)
	}
	for _, v := range reg.Panes {
		out = append(out, v.Metadata)
	}
	for _, v := range reg.Agents {
		out = append(out, v.Metadata)
	}
	return out
}

// canonicalizeRootConflictsWithReport applies the v3 duplicate-group plus
// destination-closure rule to one imported snapshot root while preserving
// every unique name outside the closure.
func (r *Registry) canonicalizeRootConflictsWithReport(rootUID string) ([]MigrationNameRepair, error) {
	resources, err := migrationResources(r)
	if err != nil {
		return nil, err
	}
	type addressKey struct {
		root string
		kind Kind
		name string
	}
	groups := make(map[addressKey][]int)
	for index, resource := range resources {
		if resource.root != rootUID {
			continue
		}
		groups[addressKey{resource.root, resource.kind, *resource.name}] = append(groups[addressKey{resource.root, resource.kind, *resource.name}], index)
	}
	reasons := map[int]string{}
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		for _, index := range members {
			reasons[index] = migrationReasonDuplicateGroup
		}
	}
	for {
		changed := false
		for index := range reasons {
			resource := resources[index]
			for _, occupant := range groups[addressKey{resource.root, resource.kind, resource.uid}] {
				if _, exists := reasons[occupant]; exists {
					continue
				}
				reasons[occupant] = migrationReasonDestinationClosure
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	repairs := make([]MigrationNameRepair, 0, len(reasons))
	for index, reason := range reasons {
		resource := resources[index]
		oldName := *resource.name
		if oldName == resource.uid {
			reason = migrationReasonAlreadyCanonical
		}
		repairs = append(repairs, MigrationNameRepair{RootOwnerUID: resource.root, Kind: resource.kind,
			UID: resource.uid, OldName: oldName, NewName: resource.uid, Reason: reason})
		*resources[index].name = resources[index].uid
	}
	sort.Slice(repairs, func(i, j int) bool {
		a, b := repairs[i], repairs[j]
		if a.RootOwnerUID != b.RootOwnerUID {
			return a.RootOwnerUID < b.RootOwnerUID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		if a.OldName != b.OldName {
			return a.OldName < b.OldName
		}
		if a.NewName != b.NewName {
			return a.NewName < b.NewName
		}
		return a.Reason < b.Reason
	})
	return repairs, nil
}

// rebuildAllReservations reconstructs the v4 reservation table from the owner
// graph in stable root/kind/UID order. Persisted v3 reservations are not an
// authority for migration.
func (r *Registry) rebuildAllReservations() {
	resources, err := migrationResources(r)
	if err != nil {
		return
	}
	r.NameReservations = nil
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		if a.root != b.root {
			return a.root < b.root
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.uid < b.uid
	})
	for _, resource := range resources {
		r.putReservation(resource.root, resource.kind, *resource.name, resource.uid)
	}
}

func migrationDirectoryExists(env MigrationEnvironment, path string) (bool, error) {
	if env.DirectoryExists == nil {
		return true, nil
	}
	return env.DirectoryExists(path)
}

func migrationUID(reg *Registry, env MigrationEnvironment, kind Kind) (string, error) {
	if env.NewUID == nil {
		return "", stateErr("migrate registry v1 to v2", ErrInvalidRegistry, "repair must create %s but no uid generator was supplied", kind)
	}
	for range 100 {
		uid, err := env.NewUID(kind)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(uid) == "" || registryHasUID(*reg, uid) {
			continue
		}
		return uid, nil
	}
	return "", stateErr("migrate registry v1 to v2", ErrInvalidRegistry, "uid generator did not produce a unique %s uid", kind)
}

func registryHasUID(reg Registry, uid string) bool {
	_, project := reg.Project(uid)
	_, control := reg.ControlSession(uid)
	_, window := reg.Window(uid)
	_, pane := reg.Pane(uid)
	_, agent := reg.Agent(uid)
	return project || control || window || pane || agent
}

func projectOwningWindow(reg *Registry, window Window) (*Project, bool) {
	if window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != KindProject {
		return nil, false
	}
	return reg.Project(window.Metadata.OwnerRef.UID)
}

func firstWindowOwnedShellUID(reg *Registry, windowUID string) string {
	for _, pane := range reg.Panes {
		if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == KindWindow &&
			pane.Metadata.OwnerRef.UID == windowUID && pane.Spec.Role == PaneRoleShell {
			return pane.Metadata.UID
		}
	}
	return ""
}

func validWindowPrimary(reg *Registry, window Window) bool {
	primary, ok := reg.Pane(strings.TrimSpace(window.Spec.migrationShellPaneRef()))
	return ok && primary.Spec.Role == PaneRoleShell && primary.Metadata.OwnerRef != nil &&
		primary.Metadata.OwnerRef.Kind == KindWindow && primary.Metadata.OwnerRef.UID == window.Metadata.UID
}

func validProjectPrimary(reg *Registry, project Project) bool {
	window, ok := reg.Window(strings.TrimSpace(project.Spec.PrimaryWindowRef))
	return ok && window.Metadata.OwnerRef != nil && window.Metadata.OwnerRef.Kind == KindProject &&
		window.Metadata.OwnerRef.UID == project.Metadata.UID && validWindowPrimary(reg, *window)
}

// rebuildMissingReservations reconstructs missing v4 root-wide reservations.
func (r *Registry) rebuildMissingReservations() {
	index := make(map[nameKey]string, len(r.NameReservations))
	for _, reservation := range r.NameReservations {
		index[nameKey{Scope: reservation.Scope, Kind: reservation.Kind, Name: reservation.Name}] = reservation.UID
	}
	add := func(scope string, kind Kind, name, uid string) {
		if name == "" || uid == "" {
			return
		}
		key := nameKey{Scope: scope, Kind: kind, Name: name}
		if _, ok := index[key]; ok {
			return
		}
		index[key] = uid
		r.NameReservations = append(r.NameReservations, NameReservation{Scope: scope, Kind: kind, Name: name, UID: uid})
	}
	for _, project := range r.Projects {
		add("", KindProject, project.Metadata.Name, project.Metadata.UID)
	}
	for _, control := range r.ControlSessions {
		add("", KindControlSession, control.Metadata.Name, control.Metadata.UID)
	}
	for _, window := range r.Windows {
		if scope, err := r.scopeFor(KindWindow, window.Metadata.OwnerUID()); err == nil {
			add(scope, KindWindow, window.Metadata.Name, window.Metadata.UID)
		}
	}
	for _, pane := range r.Panes {
		if scope, err := r.scopeFor(KindPane, pane.Metadata.OwnerUID()); err == nil {
			add(scope, KindPane, pane.Metadata.Name, pane.Metadata.UID)
		}
	}
	for _, agent := range r.Agents {
		if scope, err := r.scopeFor(KindAgent, agent.Metadata.OwnerUID()); err == nil {
			add(scope, KindAgent, agent.Metadata.Name, agent.Metadata.UID)
		}
	}
	sortNameReservations(r.NameReservations)
}

// normalized stamps the current envelope and canonicalizes timestamps.
func (r Registry) normalized() Registry {
	r.APIVersion = APIVersion
	r.SchemaVersion = SchemaVersion
	r.UpdatedAt = r.UpdatedAt.UTC()
	for i := range r.Projects {
		r.Projects[i].APIVersion = APIVersion
		r.Projects[i].Kind = KindProject
		r.Projects[i].Metadata.CreatedAt = r.Projects[i].Metadata.CreatedAt.UTC()
	}
	for i := range r.ControlSessions {
		r.ControlSessions[i].APIVersion = APIVersion
		r.ControlSessions[i].Kind = KindControlSession
		r.ControlSessions[i].Metadata.CreatedAt = r.ControlSessions[i].Metadata.CreatedAt.UTC()
	}
	for i := range r.Windows {
		r.Windows[i].APIVersion = APIVersion
		r.Windows[i].Kind = KindWindow
		r.Windows[i].Metadata.CreatedAt = r.Windows[i].Metadata.CreatedAt.UTC()
		if r.Windows[i].Spec.sourceShape == windowSpecSourceFinal {
			r.Windows[i].Spec.sourceShape = windowSpecSourceTyped
			r.Windows[i].Spec.legacyPrimaryPaneRef = ""
		}
	}
	for i := range r.Panes {
		r.Panes[i].APIVersion = APIVersion
		r.Panes[i].Kind = KindPane
		r.Panes[i].Metadata.CreatedAt = r.Panes[i].Metadata.CreatedAt.UTC()
		r.Panes[i].Status.Activation.StartedAt = r.Panes[i].Status.Activation.StartedAt.UTC()
		normalizeTermination(r.Panes[i].Status.LastTermination)
	}
	for i := range r.Agents {
		r.Agents[i].APIVersion = APIVersion
		r.Agents[i].Kind = KindAgent
		r.Agents[i].Metadata.CreatedAt = r.Agents[i].Metadata.CreatedAt.UTC()
		r.Agents[i].Status.LastTransitionAt = r.Agents[i].Status.LastTransitionAt.UTC()
		r.Agents[i].Status.Interaction.ObservedAt = r.Agents[i].Status.Interaction.ObservedAt.UTC()
		r.Agents[i].Status.Activation.ObservedAt = r.Agents[i].Status.Activation.ObservedAt.UTC()
		r.Agents[i].Status.Progress.StartedAt = r.Agents[i].Status.Progress.StartedAt.UTC()
		r.Agents[i].Status.Progress.ObservedAt = r.Agents[i].Status.Progress.ObservedAt.UTC()
		normalizeTermination(r.Agents[i].Status.LastTermination)
	}
	return r
}

// normalizeTermination canonicalizes one receipt's timestamp in place. A nil
// receipt is the common case and stays nil, so a registry written before
// termination evidence existed round-trips byte-identically.
func normalizeTermination(receipt *TerminationEvidence) {
	if receipt == nil {
		return
	}
	receipt.ObservedAt = receipt.ObservedAt.UTC()
}

// Normalize returns the registry with the current envelope stamped and
// timestamps canonicalized to UTC.
func (r Registry) Normalize() Registry { return r.Clone().normalized() }
