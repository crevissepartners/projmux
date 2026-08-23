package metadata

import (
	"fmt"
	"maps"
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
	FromVersion int
	ToVersion   int
	Repairs     []MigrationRepair
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
	return count
}

// String renders a deterministic report suitable for an operator-facing
// migration result or archived test evidence.
func (r MigrationReport) String() string {
	var out strings.Builder
	fmt.Fprintf(&out, "registry schema migration %d -> %d: repairs=%d information-loss=%d", r.FromVersion, r.ToVersion, len(r.Repairs), r.InformationLossCount())
	for _, repair := range r.Repairs {
		fmt.Fprintf(&out, "\n- %s %s %s %s: %q -> %q (information-loss=%t)", repair.Action, repair.Kind, repair.UID, repair.Field, repair.From, repair.To, repair.InformationLoss)
	}
	return out.String()
}

// Migration lifts one registry document from an envelope version to the next.
type Migration func(*Registry, MigrationEnvironment, *MigrationReport) error

// MigrationSet maps a source envelope version onto the step that lifts it to
// the next version.
type MigrationSet map[int]Migration

// productionMigrations recognizes exactly the first shipped Registry envelope.
// Version 0 remains unknown: a file with no schemaVersion is never rewritten.
var productionMigrations = MigrationSet{1: migrateV1ToV2}

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
	if action == SchemaCurrent {
		return reg.Clone().normalized(), false, report, nil
	}

	working := reg.Clone()
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
		name, err := reg.allocateName(op, project.Metadata.UID, KindWindow, FallbackWindowNameBase, uid)
		if err != nil {
			return err
		}
		reg.Windows = append(reg.Windows, Window{
			APIVersion: APIVersion,
			Kind:       KindWindow,
			Metadata: ObjectMeta{
				UID:       uid,
				Name:      name,
				OwnerRef:  &OwnerRef{Kind: KindProject, UID: project.Metadata.UID},
				CreatedAt: project.Metadata.CreatedAt,
			},
		})
		report.Repairs = append(report.Repairs, MigrationRepair{Action: "create-canonical-window", Kind: KindWindow, UID: uid, Field: "resource", To: name})
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
			continue
		}
		from := window.Spec.PrimaryPaneRef
		primaryUID := firstWindowOwnedShellUID(reg, window.Metadata.UID)
		if primaryUID == "" {
			uid, err := migrationUID(reg, env, KindPane)
			if err != nil {
				return err
			}
			name, err := reg.allocateName(op, window.Metadata.UID, KindPane, FallbackPaneNameBase, uid)
			if err != nil {
				return err
			}
			cwd := ""
			if hasProject {
				cwd = project.Spec.Root
			}
			reg.Panes = append(reg.Panes, Pane{
				APIVersion: APIVersion,
				Kind:       KindPane,
				Metadata: ObjectMeta{
					UID:       uid,
					Name:      name,
					OwnerRef:  &OwnerRef{Kind: KindWindow, UID: window.Metadata.UID},
					CreatedAt: window.Metadata.CreatedAt,
				},
				Spec: PaneSpec{Role: PaneRoleShell, CWD: cwd},
			})
			primaryUID = uid
			report.Repairs = append(report.Repairs, MigrationRepair{Action: "create-bare-shell", Kind: KindPane, UID: uid, Field: "resource", To: name})
		}
		window.Spec.PrimaryPaneRef = primaryUID
		report.Repairs = append(report.Repairs, MigrationRepair{
			Action: "repair-primary-pane", Kind: KindWindow, UID: window.Metadata.UID,
			Field: "spec.primaryPaneRef", From: from, To: primaryUID, InformationLoss: strings.TrimSpace(from) != "",
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
	primary, ok := reg.Pane(strings.TrimSpace(window.Spec.PrimaryPaneRef))
	return ok && primary.Spec.Role == PaneRoleShell && primary.Metadata.OwnerRef != nil &&
		primary.Metadata.OwnerRef.Kind == KindWindow && primary.Metadata.OwnerRef.UID == window.Metadata.UID
}

func validProjectPrimary(reg *Registry, project Project) bool {
	window, ok := reg.Window(strings.TrimSpace(project.Spec.PrimaryWindowRef))
	return ok && window.Metadata.OwnerRef != nil && window.Metadata.OwnerRef.Kind == KindProject &&
		window.Metadata.OwnerRef.UID == project.Metadata.UID && validWindowPrimary(reg, *window)
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
	for _, control := range r.ControlSessions {
		add("", KindControlSession, control.Metadata.Name, control.Metadata.UID)
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
	for i := range r.ControlSessions {
		r.ControlSessions[i].APIVersion = APIVersion
		r.ControlSessions[i].Kind = KindControlSession
		r.ControlSessions[i].Metadata.CreatedAt = r.ControlSessions[i].Metadata.CreatedAt.UTC()
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
