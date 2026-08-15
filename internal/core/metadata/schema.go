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

// migrations maps a source envelope version onto the step that lifts it to the
// next version. Version 0 is a pre-release registry document written before
// the envelope carried an explicit schemaVersion.
var migrations = map[int]func(*Registry) error{
	0: migrateV0ToV1,
}

// ClassifySchemaVersion decides how to read an envelope version.
//
// A newer-than-supported version is rejected fail-closed: the caller must not
// quarantine, reset, truncate, or otherwise write the file, because doing so
// would destroy state a newer build owns. Downgrade writes are unsupported.
func ClassifySchemaVersion(version int) (SchemaAction, error) {
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
		if _, ok := migrations[probe]; !ok {
			return SchemaReject, stateErr("read registry", ErrSchemaUnsupported,
				"no migration step from schemaVersion %d to %d", probe, probe+1)
		}
	}
	return SchemaMigrate, nil
}

// MigrateRegistry lifts reg to the current schema version. It returns the
// migrated copy and whether any step ran. The input is never mutated, so a
// failing step can never leave a partially migrated value behind.
func MigrateRegistry(reg Registry) (Registry, bool, error) {
	action, err := ClassifySchemaVersion(reg.SchemaVersion)
	if err != nil {
		return Registry{}, false, err
	}
	if action == SchemaCurrent {
		return reg.Clone().normalized(), false, nil
	}

	working := reg.Clone()
	for working.SchemaVersion < SchemaVersion {
		step, ok := migrations[working.SchemaVersion]
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

// migrateV0ToV1 lifts a pre-release, unversioned registry document to the v1
// envelope: it stamps the api and schema versions on the document and on every
// resource, and rebuilds any name reservations the document did not persist.
func migrateV0ToV1(reg *Registry) error {
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
	return nil
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
	}
	for i := range r.Agents {
		r.Agents[i].APIVersion = APIVersion
		r.Agents[i].Kind = KindAgent
		r.Agents[i].Metadata.CreatedAt = r.Agents[i].Metadata.CreatedAt.UTC()
		r.Agents[i].Status.LastTransitionAt = r.Agents[i].Status.LastTransitionAt.UTC()
	}
	return r
}

// Normalize returns the registry with the current envelope stamped and
// timestamps canonicalized to UTC.
func (r Registry) Normalize() Registry { return r.Clone().normalized() }
