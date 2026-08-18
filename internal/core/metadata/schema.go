package metadata

import "fmt"

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

// Migration lifts one registry document from an envelope version to the next.
type Migration func(*Registry) error

// MigrationSet maps a source envelope version onto the step that lifts it to
// the next version.
type MigrationSet map[int]Migration

// productionMigrations is deliberately EMPTY.
//
// schemaVersion 1 is the first envelope projmux has ever written, so no older
// registry document exists to migrate. A file that parses as JSON but carries
// no schemaVersion decodes as version 0, and that is an *unknown* document
// rather than a known older schema: accepting it would migrate and rewrite a
// corrupt or foreign file, which is exactly the write-on-unknown-input that
// fail-closed exists to prevent. Version 0 therefore has no step and is
// refused like any other unsupported version.
//
// The generic machinery below, and its backup -> temp write -> validate ->
// atomic replace sequence, is the contract for the first real schema bump.
// Tests register a step into a private MigrationSet to exercise that path
// without shipping one.
var productionMigrations = MigrationSet{}

// resolveMigrations treats a nil set as the production set, so callers that do
// not override migrations automatically track whatever production ships.
func resolveMigrations(set MigrationSet) MigrationSet {
	if set == nil {
		return productionMigrations
	}
	return set
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
	return MigrateRegistryWith(nil, reg)
}

// MigrateRegistryWith lifts reg to the current schema version against an
// explicit migration set. A nil set means the production set. It returns the
// migrated copy and whether any step ran. The input is never mutated, so a
// failing step can never leave a partially migrated value behind.
func MigrateRegistryWith(set MigrationSet, reg Registry) (Registry, bool, error) {
	set = resolveMigrations(set)
	action, err := ClassifySchemaVersionWith(set, reg.SchemaVersion)
	if err != nil {
		return Registry{}, false, err
	}
	if action == SchemaCurrent {
		return reg.Clone().normalized(), false, nil
	}

	working := reg.Clone()
	for working.SchemaVersion < SchemaVersion {
		step, ok := set[working.SchemaVersion]
		if !ok {
			return Registry{}, false, stateErr("migrate registry", ErrSchemaUnsupported,
				"no migration step from schemaVersion %d", working.SchemaVersion)
		}
		if err := step(&working); err != nil {
			return Registry{}, false, err
		}
	}
	return working.normalized(), true, nil
}

// rebuildMissingReservations backfills a reservation for every resource name
// that has none. Existing reservations win, so a migration never renumbers or
// renames an existing resource.
func (r *Registry) rebuildMissingReservations() {
	index := r.reservationIndex()
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
	for _, window := range r.Windows {
		add(window.Metadata.OwnerUID(), KindWindow, window.Metadata.Name, window.Metadata.UID)
	}
	for _, pane := range r.Panes {
		add(pane.Metadata.OwnerUID(), KindPane, pane.Metadata.Name, pane.Metadata.UID)
	}
	for _, agent := range r.Agents {
		add(agent.Metadata.OwnerUID(), KindAgent, agent.Metadata.Name, agent.Metadata.UID)
	}
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
	for i := range r.Windows {
		r.Windows[i].APIVersion = APIVersion
		r.Windows[i].Kind = KindWindow
		r.Windows[i].Metadata.CreatedAt = r.Windows[i].Metadata.CreatedAt.UTC()
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
