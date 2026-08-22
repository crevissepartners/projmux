package metadata

import (
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Mutator applies metadata operations to a registry. Every environment
// dependency is injected so the core stays free of I/O and tests stay
// deterministic.
type Mutator struct {
	// Now supplies operation timestamps. Defaults to time.Now.
	Now func() time.Time
	// NewUID mints opaque identities. Defaults to the crypto/rand generator.
	NewUID func(Kind) (string, error)
	// DirExists reports whether a path is an existing directory. It has no
	// default: root lifecycle operations require the caller to supply the
	// filesystem probe explicitly.
	DirExists func(string) (bool, error)
}

func (m Mutator) clock() func() time.Time {
	if m.Now != nil {
		return m.Now
	}
	return time.Now
}

func (m Mutator) mintUID(kind Kind) (string, error) {
	if m.NewUID != nil {
		return m.NewUID(kind)
	}
	return NewUID(kind)
}

func (m Mutator) dirExists(op, path string) (bool, error) {
	if m.DirExists == nil {
		return false, stateErr(op, ErrInvalidRoot, "no directory probe is configured")
	}
	return m.DirExists(path)
}

func cleanRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// validateRoot enforces "existing absolute directory" for a Project root.
func (m Mutator) validateRoot(op, root string) (string, error) {
	cleaned := cleanRoot(root)
	if cleaned == "" {
		return "", inputErr(op, ErrInvalidRoot, "project root must not be empty")
	}
	if !filepath.IsAbs(cleaned) {
		return "", inputErr(op, ErrInvalidRoot, "project root %q must be absolute", cleaned)
	}
	exists, err := m.dirExists(op, cleaned)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", inputErr(op, ErrInvalidRoot, "project root %q is not an existing directory", cleaned)
	}
	return cleaned, nil
}

// BootstrapPane declares one Pane of a Project startup topology.
type BootstrapPane struct {
	// Name is an explicit pane name. Empty means automatic naming.
	Name string
	// Command is the one-time name-derivation source for the Pane.
	Command string
	// CWD is the declared pane working directory.
	CWD string
	// Labels are creation-time key/value classification metadata. They are a
	// creation option, never a selector input for the resource being created.
	Labels map[string]string
}

// BootstrapWindow declares one Window of a Project startup topology. Every
// Window owns an initial Pane; an empty Panes list gets one default shell Pane.
type BootstrapWindow struct {
	Name    string
	Command string
	// Labels are creation-time key/value classification metadata.
	Labels map[string]string
	Panes  []BootstrapPane
}

// RegisterProjectOptions is the input to offline Project registration.
type RegisterProjectOptions struct {
	// Root is the absolute project root. It must already exist.
	Root string
	// Name is an explicit --name. A collision fails with ErrNameConflict and
	// zero mutations; it never receives an implicit suffix.
	Name string
	// DisplayName defaults to the root basename and may duplicate.
	DisplayName string
	Labels      map[string]string
	Annotations map[string]string
	// Topology is the configured project startup topology. Empty means one
	// default Window named after the configured shell basename with one
	// initial shell Pane.
	Topology []BootstrapWindow
	// DefaultShell is the configured shell path; its basename seeds default
	// Window and Pane names.
	DefaultShell string
	// SessionName optionally records the persistent tmux session projection.
	SessionName string
	// OperationID labels the transaction ledger.
	OperationID string
}

// RegisterProjectResult reports the resources an offline registration created.
type RegisterProjectResult struct {
	Project     Project
	Windows     []Window
	Panes       []Pane
	Reused      bool
	OperationID string
	Created     []string
}

// RegisterProject registers a Project and creates its offline Window/Pane
// topology with no tmux involvement. The exact saved root reappearing reuses
// the same uid; nothing else ever merges uids.
func (m Mutator) RegisterProject(reg *Registry, opts RegisterProjectOptions) (RegisterProjectResult, error) {
	const op = "register project"

	root, err := m.validateRoot(op, opts.Root)
	if err != nil {
		return RegisterProjectResult{}, err
	}
	if existing, ok := reg.ProjectByRoot(root); ok {
		return RegisterProjectResult{
			Project:     existing.Clone(),
			Windows:     reg.WindowsOf(existing.Metadata.UID),
			Panes:       reg.projectPanes(existing.Metadata.UID),
			Reused:      true,
			OperationID: opts.OperationID,
		}, nil
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, opts.OperationID)
	result, err := m.registerProjectTx(txn, reg, root, now, opts)
	if err != nil {
		txn.Rollback()
		return RegisterProjectResult{}, err
	}
	result.Created = txn.Created()
	result.OperationID = txn.ID()
	txn.Commit()
	reg.UpdatedAt = now
	return result, nil
}

func (m Mutator) registerProjectTx(txn *Transaction, reg *Registry, root string, now time.Time, opts RegisterProjectOptions) (RegisterProjectResult, error) {
	const op = "register project"

	projectUID, err := m.mintUID(KindProject)
	if err != nil {
		return RegisterProjectResult{}, err
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		displayName = ProjectDisplayName(root)
	}

	var name string
	if explicit := strings.TrimSpace(opts.Name); explicit != "" {
		if err := reg.reserveExplicitName(op, "", KindProject, explicit, projectUID); err != nil {
			return RegisterProjectResult{}, err
		}
		name = explicit
	} else {
		name, err = reg.allocateName(op, "", KindProject, ProjectNameBase(root), projectUID)
		if err != nil {
			return RegisterProjectResult{}, err
		}
	}

	project := Project{
		APIVersion: APIVersion,
		Kind:       KindProject,
		Metadata: ObjectMeta{
			UID:         projectUID,
			Name:        name,
			DisplayName: displayName,
			Labels:      cloneStringMap(opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
			CreatedAt:   now,
		},
		Spec: ProjectSpec{Root: root},
	}
	if session := strings.TrimSpace(opts.SessionName); session != "" {
		project.Status.Session = &SessionProjection{Name: session}
	}
	reg.Projects = append(reg.Projects, project)
	txn.record(KindProject, projectUID)

	topology := opts.Topology
	if len(topology) == 0 {
		topology = []BootstrapWindow{{}}
	}

	result := RegisterProjectResult{}
	for _, declared := range topology {
		window, panes, err := m.addWindowTx(txn, reg, op, projectUID, declared, opts.DefaultShell, root, now)
		if err != nil {
			return RegisterProjectResult{}, err
		}
		result.Windows = append(result.Windows, window)
		result.Panes = append(result.Panes, panes...)
	}
	if len(result.Windows) > 0 {
		stored, _ := reg.Project(projectUID)
		stored.Spec.PrimaryWindowRef = result.Windows[0].Metadata.UID
	}

	stored, _ := reg.Project(projectUID)
	result.Project = stored.Clone()
	return result, nil
}

// AddWindow creates one offline Window plus its initial Pane below an existing
// Project. No tmux window is created.
func (m Mutator) AddWindow(reg *Registry, projectUID string, declared BootstrapWindow, defaultShell, operationID string) (Window, []Pane, error) {
	const op = "create window"

	project, ok := reg.Project(projectUID)
	if !ok {
		return Window{}, nil, stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	window, panes, err := m.addWindowTx(txn, reg, op, projectUID, declared, defaultShell, project.Spec.Root, now)
	if err != nil {
		txn.Rollback()
		return Window{}, nil, err
	}
	txn.Commit()
	reg.UpdatedAt = now
	return window, panes, nil
}

func (m Mutator) addWindowTx(txn *Transaction, reg *Registry, op, projectUID string, declared BootstrapWindow, defaultShell, defaultCWD string, now time.Time) (Window, []Pane, error) {
	windowUID, err := m.mintUID(KindWindow)
	if err != nil {
		return Window{}, nil, err
	}

	var name string
	if explicit := strings.TrimSpace(declared.Name); explicit != "" {
		if err := reg.reserveExplicitName(op, projectUID, KindWindow, explicit, windowUID); err != nil {
			return Window{}, nil, err
		}
		name = explicit
	} else {
		name, err = reg.allocateName(op, projectUID, KindWindow, WindowNameBase("", declared.Command, defaultShell), windowUID)
		if err != nil {
			return Window{}, nil, err
		}
	}

	window := Window{
		APIVersion: APIVersion,
		Kind:       KindWindow,
		Metadata: ObjectMeta{
			UID:       windowUID,
			Name:      name,
			Labels:    cloneStringMap(declared.Labels),
			OwnerRef:  &OwnerRef{Kind: KindProject, UID: projectUID},
			CreatedAt: now,
		},
	}
	reg.Windows = append(reg.Windows, window)
	txn.record(KindWindow, windowUID)

	declaredPanes := declared.Panes
	if len(declaredPanes) == 0 {
		declaredPanes = []BootstrapPane{{Command: declared.Command}}
	}

	panes := make([]Pane, 0, len(declaredPanes))
	for _, declaredPane := range declaredPanes {
		cwd := strings.TrimSpace(declaredPane.CWD)
		if cwd == "" {
			cwd = defaultCWD
		}
		pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, declaredPane.Name, PaneNameBase(declaredPane.Command, defaultShell), declaredPane.Command, cwd, declaredPane.Labels, now)
		if err != nil {
			return Window{}, nil, err
		}
		panes = append(panes, pane)
	}

	stored, _ := reg.Window(windowUID)
	stored.Spec.PrimaryPaneRef = panes[0].Metadata.UID
	return stored.Clone(), panes, nil
}

func (m Mutator) addPaneTx(txn *Transaction, reg *Registry, op, ownerUID string, ownerKind Kind, role PaneRole, explicitName, nameBase, command, cwd string, labels map[string]string, now time.Time) (Pane, error) {
	paneUID, err := m.mintUID(KindPane)
	if err != nil {
		return Pane{}, err
	}

	var name string
	if explicit := strings.TrimSpace(explicitName); explicit != "" {
		if err := reg.reserveExplicitName(op, ownerUID, KindPane, explicit, paneUID); err != nil {
			return Pane{}, err
		}
		name = explicit
	} else {
		name, err = reg.allocateName(op, ownerUID, KindPane, nameBase, paneUID)
		if err != nil {
			return Pane{}, err
		}
	}

	pane := Pane{
		APIVersion: APIVersion,
		Kind:       KindPane,
		Metadata: ObjectMeta{
			UID:       paneUID,
			Name:      name,
			Labels:    cloneStringMap(labels),
			OwnerRef:  &OwnerRef{Kind: ownerKind, UID: ownerUID},
			CreatedAt: now,
		},
		Spec: PaneSpec{Role: role, CWD: cwd, Command: strings.TrimSpace(command)},
	}
	reg.Panes = append(reg.Panes, pane)
	txn.record(KindPane, paneUID)
	reg.adoptPrimaryPaneRef(ownerUID, ownerKind, paneUID)
	return pane, nil
}

// adoptPrimaryPaneRef gives a Window its primaryPaneRef back when this Pane is
// the first one it owns again.
//
// Validate states the rule as a pair: a Window with no owned Pane carries an
// empty primaryPaneRef, and a Window that owns one carries a resolvable ref.
// Deleting a Window's last Pane empties the ref by design, so the very next Pane
// added under that Window has to reclaim it or the registry the caller just wrote
// is one Validate rejects -- and it is rejected on the *next* write, not this one,
// which turns a legal `delete pane` followed by a legal `create pane` into a
// registry nothing can mutate any more.
//
// Agent-owned Panes never count: the primary anchor is exactly a Window-owned
// role=shell Pane. Adoption is strictly a repair of the empty case and never
// re-points a Window that already has a resolvable primary.
func (r *Registry) adoptPrimaryPaneRef(ownerUID string, ownerKind Kind, paneUID string) {
	if ownerKind != KindWindow {
		return
	}
	for i := range r.Windows {
		if r.Windows[i].Metadata.UID != ownerUID {
			continue
		}
		if strings.TrimSpace(r.Windows[i].Spec.PrimaryPaneRef) != "" {
			return
		}
		r.Windows[i].Spec.PrimaryPaneRef = paneUID
		return
	}
}

// AddPane creates one offline shell Pane inside an existing Window.
func (m Mutator) AddPane(reg *Registry, windowUID string, declared BootstrapPane, defaultShell, operationID string) (Pane, error) {
	const op = "create pane"

	window, ok := reg.Window(windowUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	cwd := strings.TrimSpace(declared.CWD)
	if cwd == "" {
		if project, ok := reg.Project(window.Metadata.OwnerUID()); ok {
			cwd = project.Spec.Root
		}
	}
	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, declared.Name, PaneNameBase(declared.Command, defaultShell), declared.Command, cwd, declared.Labels, now)
	if err != nil {
		txn.Rollback()
		return Pane{}, err
	}
	txn.Commit()
	reg.UpdatedAt = now
	return pane, nil
}

// RebindProjectRoot changes only spec.root of exactly one Project. It never
// moves files and never changes metadata.uid.
func (m Mutator) RebindProjectRoot(reg *Registry, projectUID, newRoot string) (Project, error) {
	const op = "rebind project"

	project, ok := reg.Project(projectUID)
	if !ok {
		return Project{}, stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	root, err := m.validateRoot(op, newRoot)
	if err != nil {
		return Project{}, err
	}
	if other, ok := reg.ProjectByRoot(root); ok && other.Metadata.UID != projectUID {
		return Project{}, inputErr(op, ErrRootConflict, "root %q is already bound to project %s", root, other.Metadata.Name)
	}
	if project.Spec.Root == root {
		return project.Clone(), nil
	}

	now := m.clock()().UTC()
	project.Spec.Root = root
	clearCondition(&project.Status.Conditions, ConditionMissingRoot)
	reg.UpdatedAt = now
	return project.Clone(), nil
}

// RenameProject sets a Project metadata.name. An explicit collision fails with
// ErrNameConflict and zero mutations.
func (m Mutator) RenameProject(reg *Registry, projectUID, name string) (Project, error) {
	const op = "rename project"

	project, ok := reg.Project(projectUID)
	if !ok {
		return Project{}, stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	name = strings.TrimSpace(name)
	if err := reg.reserveExplicitName(op, "", KindProject, name, projectUID); err != nil {
		return Project{}, err
	}
	project.Metadata.Name = name
	reg.UpdatedAt = m.clock()().UTC()
	return project.Clone(), nil
}

// ObserveWindowDisplayName projects one live tmux window_name onto the
// duplicate-allowed, non-identifying metadata.displayName field. It never
// changes the Window uid, stable name, owner, or name reservation.
func (m Mutator) ObserveWindowDisplayName(reg *Registry, windowUID, displayName string) (Window, error) {
	const op = "observe window display name"

	window, ok := reg.Window(windowUID)
	if !ok {
		return Window{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	if window.Metadata.DisplayName == displayName {
		return window.Clone(), nil
	}
	window.Metadata.DisplayName = displayName
	reg.UpdatedAt = m.clock()().UTC()
	return window.Clone(), nil
}

// ObserveWindowRuntimeBinding records the exact live tmux owner pair for one
// managed Window. The binding is retained when a later inventory reports the
// Window missing: teardown hooks need the last positive $N/@N observation to
// pair pane-exited with window-unlinked without consulting current client
// context after the Window is gone.
func (m Mutator) ObserveWindowRuntimeBinding(reg *Registry, windowUID, sessionID, runtimeID string) (Window, error) {
	const op = "observe window runtime binding"

	window, ok := reg.Window(windowUID)
	if !ok {
		return Window{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	sessionID = strings.TrimSpace(sessionID)
	runtimeID = strings.TrimSpace(runtimeID)
	if !validRuntimeHandle(sessionID, '$') || !validRuntimeHandle(runtimeID, '@') {
		return Window{}, inputErr(op, ErrInvalidRegistry, "window %q requires exact $N/@N runtime handles", windowUID)
	}
	if window.Status.RuntimeSessionID == sessionID && window.Status.RuntimeID == runtimeID {
		return window.Clone(), nil
	}
	window.Status.RuntimeSessionID = sessionID
	window.Status.RuntimeID = runtimeID
	reg.UpdatedAt = m.clock()().UTC()
	return window.Clone(), nil
}

// RenameWindow sets a Window metadata.name inside its owning Project scope.
func (m Mutator) RenameWindow(reg *Registry, windowUID, name string) (Window, error) {
	const op = "rename window"

	window, ok := reg.Window(windowUID)
	if !ok {
		return Window{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	name = strings.TrimSpace(name)
	if err := reg.reserveExplicitName(op, window.Metadata.OwnerUID(), KindWindow, name, windowUID); err != nil {
		return Window{}, err
	}
	window.Metadata.Name = name
	reg.UpdatedAt = m.clock()().UTC()
	return window.Clone(), nil
}

// RenamePane sets a Pane metadata.name inside its owner scope. The adapter
// mirrors the new name into the legacy @projmux_pane_label option and never
// writes the raw tmux pane_title.
func (m Mutator) RenamePane(reg *Registry, paneUID, name string) (Pane, error) {
	const op = "rename pane"

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	name = strings.TrimSpace(name)
	if err := reg.reserveExplicitName(op, pane.Metadata.OwnerUID(), KindPane, name, paneUID); err != nil {
		return Pane{}, err
	}
	pane.Metadata.Name = name
	reg.UpdatedAt = m.clock()().UTC()
	return pane.Clone(), nil
}

// SetPaneDisplayTitle stores the derived secondary pane title.
func (m Mutator) SetPaneDisplayTitle(reg *Registry, paneUID, agent, topic, command, rawTitle string) (Pane, error) {
	const op = "set pane display title"

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	pane.Status.DisplayTitle = DerivePaneDisplayTitle(agent, topic, command, rawTitle)
	reg.UpdatedAt = m.clock()().UTC()
	return pane.Clone(), nil
}

// BindProjectSession records the 1:1 persistent tmux session projection. It
// never changes metadata.uid, so a Project keeps its identity across runtime
// creation, teardown, and recreation.
func (m Mutator) BindProjectSession(reg *Registry, projectUID, sessionName string, live bool) (Project, error) {
	const op = "bind project session"

	project, ok := reg.Project(projectUID)
	if !ok {
		return Project{}, stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		project.Status.Session = nil
	} else {
		project.Status.Session = &SessionProjection{Name: sessionName, Live: live}
	}
	reg.UpdatedAt = m.clock()().UTC()
	return project.Clone(), nil
}

// ObserveProjectRoots refreshes the MissingRoot condition for every Project.
// A missing root never deletes or re-identifies a Project and never releases
// its name reservations; a returning root recovers the same uid and clears the
// condition.
func (m Mutator) ObserveProjectRoots(reg *Registry) error {
	const op = "observe project roots"

	now := m.clock()().UTC()
	changed := false
	for i := range reg.Projects {
		project := &reg.Projects[i]
		exists, err := m.dirExists(op, project.Spec.Root)
		if err != nil {
			return err
		}
		if exists {
			if clearCondition(&project.Status.Conditions, ConditionMissingRoot) {
				changed = true
			}
			continue
		}
		if setMissingRootCondition(&project.Status.Conditions, project.Spec.Root, now) {
			changed = true
		}
	}
	if changed {
		reg.UpdatedAt = now
	}
	return nil
}

// ObserveRuntimeBindings refreshes the MissingRuntime condition of every Window
// and Pane against one live-tmux observation.
//
// This is the reconciler's half of the observation contract. It records *why* a
// runtime object is gone so the reason survives the invocation that noticed it;
// the live/offline answer itself is derived per read from the observation and
// never from what this writes. That split is deliberate: a stored liveness bool
// is what made Windows and Panes report live against nothing at all.
//
// It is an inventory diff, not an event handler, which is what lets it converge
// with no hook firing at all: an object whose uid is mirrored on no live tmux
// object is judged orphan on the very next pass, and a re-bound object clears
// the condition on the pass after that.
//
// Nothing here deletes, prunes, or re-identifies a resource, and nothing
// releases a name reservation. A Window or Pane whose tmux object disappeared
// stays queryable forever, exactly like a MissingRoot Project.
func (m Mutator) ObserveRuntimeBindings(reg *Registry, observed RuntimeObservation) {
	now := m.clock()().UTC()
	changed := false
	for i := range reg.Windows {
		window := &reg.Windows[i]
		if m.refreshRuntimeCondition(&window.Status.Conditions,
			observed.BoundWindow(window.Metadata.UID),
			"no live tmux window mirrors window uid "+window.Metadata.UID, now) {
			changed = true
		}
	}
	for i := range reg.Panes {
		pane := &reg.Panes[i]
		if m.refreshRuntimeCondition(&pane.Status.Conditions,
			observed.BoundPane(pane.Metadata.UID),
			"no live tmux pane mirrors pane uid "+pane.Metadata.UID, now) {
			changed = true
		}
	}
	if changed {
		reg.UpdatedAt = now
	}
}

// refreshRuntimeCondition sets or clears MissingRuntime for one resource and
// reports whether anything changed.
func (m Mutator) refreshRuntimeCondition(conditions *[]Condition, bound bool, message string, now time.Time) bool {
	if bound {
		return clearCondition(conditions, ConditionMissingRuntime)
	}
	return setCondition(conditions, Condition{
		Type:             ConditionMissingRuntime,
		Status:           ConditionTrue,
		Reason:           ReasonRuntimeUnbound,
		Message:          message,
		FirstObservedAt:  now,
		LastTransitionAt: now,
	})
}

// setMissingRootCondition records MissingRoot, preserving the first-observed
// timestamp across repeat observations.
func setMissingRootCondition(conditions *[]Condition, root string, now time.Time) bool {
	return setCondition(conditions, Condition{
		Type:             ConditionMissingRoot,
		Status:           ConditionTrue,
		Reason:           "RootDisappeared",
		Message:          "project root " + root + " is not an existing directory",
		FirstObservedAt:  now,
		LastTransitionAt: now,
	})
}

// setCondition appends condition unless its type is already recorded.
//
// A repeat observation is a no-op on purpose: FirstObservedAt must keep naming
// the first pass that saw the problem, and rewriting LastTransitionAt on every
// pass would make an unchanged registry look freshly mutated and churn the file
// on disk for nothing.
func setCondition(conditions *[]Condition, condition Condition) bool {
	for i := range *conditions {
		if (*conditions)[i].Type == condition.Type {
			return false
		}
	}
	*conditions = append(*conditions, condition)
	return true
}

func clearCondition(conditions *[]Condition, conditionType string) bool {
	for i := range *conditions {
		if (*conditions)[i].Type == conditionType {
			*conditions = slices.Delete(*conditions, i, i+1)
			if len(*conditions) == 0 {
				*conditions = nil
			}
			return true
		}
	}
	return false
}

// HasCondition reports whether a Project carries conditionType.
func (p Project) HasCondition(conditionType string) (Condition, bool) {
	return findCondition(p.Status.Conditions, conditionType)
}

// HasCondition reports whether a Window carries conditionType.
func (w Window) HasCondition(conditionType string) (Condition, bool) {
	return findCondition(w.Status.Conditions, conditionType)
}

// HasCondition reports whether a Pane carries conditionType.
func (p Pane) HasCondition(conditionType string) (Condition, bool) {
	return findCondition(p.Status.Conditions, conditionType)
}

func findCondition(conditions []Condition, conditionType string) (Condition, bool) {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return Condition{}, false
}

// projectPanes returns every Pane transitively owned by a Project.
func (r *Registry) projectPanes(projectUID string) []Pane {
	owners := map[string]bool{}
	for _, window := range r.Windows {
		if window.Metadata.OwnerUID() == projectUID {
			owners[window.Metadata.UID] = true
		}
	}
	for _, agent := range r.Agents {
		if owners[agent.Metadata.OwnerUID()] {
			owners[agent.Metadata.UID] = true
		}
	}
	var out []Pane
	for _, pane := range r.Panes {
		if owners[pane.Metadata.OwnerUID()] {
			out = append(out, pane)
		}
	}
	return out
}

// Project ref resolution deliberately lives in exactly one place: the selector
// package. An accessor here that accepted a bare uid would be a second, laxer
// grammar for the same `--project <ref>` input -- the contract requires the
// `uid:` prefix, because uids and names share a character set and an unprefixed
// value is ambiguous. See internal/core/selector.ParseRef.
