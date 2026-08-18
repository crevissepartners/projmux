package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

// The app-side boundary between the three collections a Project surface shows.
//
// Settings and the sidebar used to read one list of paths and infer everything
// else from it: whether the path was a Project, what to call it, whether it
// should sit at the top. That is three authorities in one file, and each of them
// answered wrong in a different way -- a rebind lost the pin, an unregistered
// directory looked managed, and a filesystem scan decided membership.
//
// pinAuthority keeps them apart. Workdirs stay a scan source and are not read
// here at all. The Registry stays managed identity: this type reads it, never
// writes it, and never mints a uid from a path. Pins stay preferences, typed by
// which of the two they point at.

// pinSetStore is the file half of the pin collection.
type pinSetStore interface {
	Path() string
	Load() (pins.Set, error)
	Save(pins.Set) error
}

// pinAuthority resolves stored pins against Registry Project identity.
type pinAuthority struct {
	store pinSetStore
	// projects reads the Registry identities a resolution matches against. It is
	// a read-only snapshot: resolving a pin must not create Registry state on a
	// machine that has none.
	projects func() ([]pins.ProjectRef, error)
}

// newPinAuthority binds a pin file to the Registry read.
func newPinAuthority(store pinSetStore) pinAuthority {
	return pinAuthority{store: store, projects: registryProjectRefs}
}

// registryProjectRefs reads every Project's uid and root without writing.
func registryProjectRefs() ([]pins.ProjectRef, error) {
	registry, err := loadResourceRegistry()
	if err != nil {
		return nil, err
	}
	return projectRefsOf(registry), nil
}

func projectRefsOf(registry coremetadata.Registry) []pins.ProjectRef {
	refs := make([]pins.ProjectRef, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		refs = append(refs, pins.ProjectRef{UID: project.Metadata.UID, Root: project.Spec.Root})
	}
	return refs
}

func (a pinAuthority) refs() ([]pins.ProjectRef, error) {
	if a.projects == nil {
		return nil, nil
	}
	return a.projects()
}

// resolved returns the typed reading of the pin file. It writes nothing, so every
// rendering surface can call it on a refresh.
//
// A legacy file is projected rather than migrated, which is what keeps the
// sidebar identical before and after `pin project migrate`.
func (a pinAuthority) resolved() (pins.Resolution, error) {
	stored, resolver, err := a.read()
	if err != nil {
		return pins.Resolution{}, err
	}
	return resolver.Resolve(stored), nil
}

// migrate persists the typed envelope. It is the step every mutation runs first:
// writing a typed file over unresolved legacy paths is exactly a migration, and
// doing it silently is how a pinned Project would lose its identity.
//
// An already-typed file costs one read and no write. An ambiguous one is refused
// with the pin file byte-identical, because the write is the last thing that
// happens and every refusal happens before it.
func (a pinAuthority) migrate() (pins.Resolution, error) {
	return a.runMigration(true)
}

// planMigration is migrate without the write.
func (a pinAuthority) planMigration() (pins.Resolution, error) {
	return a.runMigration(false)
}

func (a pinAuthority) runMigration(write bool) (pins.Resolution, error) {
	stored, resolver, err := a.read()
	if err != nil {
		return pins.Resolution{}, err
	}
	resolution := resolver.Resolve(stored)
	if len(resolution.Ambiguous) > 0 {
		return resolution, &pins.AmbiguousMigrationError{Path: a.store.Path(), Ambiguous: resolution.Ambiguous}
	}
	if !write || stored.Format.Typed() {
		return resolution, nil
	}
	if err := a.store.Save(resolution.Set); err != nil {
		return resolution, err
	}
	return resolution, nil
}

// read loads the stored set together with the resolver that types it.
func (a pinAuthority) read() (pins.Set, pins.Resolver, error) {
	if a.store == nil {
		return pins.Set{}, pins.Resolver{}, errNoPinStore
	}
	stored, err := a.store.Load()
	if err != nil {
		return pins.Set{}, pins.Resolver{}, err
	}
	refs, err := a.refs()
	if err != nil {
		return pins.Set{}, pins.Resolver{}, err
	}
	return stored, pins.Resolver{Projects: refs}, nil
}

// pinSelection is the row-level pin lookup of one render pass.
//
// The two maps answer different questions and that is the point. A managed Project
// is pinned by uid, so the tier survives a rebind, a rename and a missing root. An
// unregistered candidate is pinned by its folded path key, because a path is the
// only thing anyone knows about it. projectsByRootKey is the bridge a path
// argument crosses to reach the first question.
type pinSelection struct {
	projectUIDs       map[string]bool
	candidateKeys     map[string]bool
	projectsByRootKey map[string][]string
}

// selection builds the render-pass pin lookup. It writes nothing.
func (a pinAuthority) selection() (pinSelection, error) {
	stored, resolver, err := a.read()
	if err != nil {
		return pinSelection{}, err
	}
	out := pinSelection{
		projectUIDs:       map[string]bool{},
		candidateKeys:     map[string]bool{},
		projectsByRootKey: map[string][]string{},
	}
	for _, ref := range resolver.Projects {
		if key := candidates.MatchKey(ref.Root); key != "" {
			out.projectsByRootKey[key] = append(out.projectsByRootKey[key], ref.UID)
		}
	}
	resolution := resolver.Resolve(stored)
	for _, uid := range resolution.Set.ProjectUIDs() {
		out.projectUIDs[uid] = true
	}
	for _, path := range resolution.Set.CandidatePaths() {
		if key := candidates.MatchKey(path); key != "" {
			out.candidateKeys[key] = true
		}
	}
	return out, nil
}

// pinnedProject reports whether a Registry Project carries a managed pin.
func (s pinSelection) pinnedProject(uid string) bool {
	return s.projectUIDs[strings.TrimSpace(uid)]
}

// pinnedCandidate reports whether an unregistered path carries a candidate pin.
func (s pinSelection) pinnedCandidate(path string) bool {
	key := candidates.MatchKey(path)
	return key != "" && s.candidateKeys[key]
}

// pinnedPath reports whether the pin a path argument resolves to is present.
//
// It follows the same resolve-or-candidate rule the pin actions use, so the row
// that says "already pinned" and the action that would pin it agree about which
// entry they mean.
func (s pinSelection) pinnedPath(path string) bool {
	key := candidates.MatchKey(path)
	if key == "" {
		return false
	}
	for _, uid := range s.projectsByRootKey[key] {
		if s.projectUIDs[uid] {
			return true
		}
	}
	return s.candidateKeys[key]
}

// pinRow is one typed pin prepared for a picker row.
//
// Reference is what an action carries and Root is what a human reads. Keeping them
// apart is what makes a managed pin survive a rebind: the action still names the
// uid, and the directory shown is re-read from the Registry every render.
type pinRow struct {
	Pin       pins.Pin
	Reference string
	Root      string
}

// pinnedRows returns the pin collections as rows, in file order, together with the
// resolution they came from.
func (a pinAuthority) pinnedRows() ([]pinRow, pins.Resolution, error) {
	stored, resolver, err := a.read()
	if err != nil {
		return nil, pins.Resolution{}, err
	}
	roots := map[string]string{}
	known := map[string]bool{}
	for _, ref := range resolver.Projects {
		roots[ref.UID] = ref.Root
		known[ref.UID] = true
	}
	resolution := resolver.Resolve(stored)
	rows := make([]pinRow, 0, len(resolution.Set.Pins))
	for _, pin := range resolution.Set.Pins {
		row := pinRow{Pin: pin, Reference: pin.Value}
		if pin.Kind == pins.KindProject {
			row.Reference = "uid:" + pin.Value
			row.Root = strings.TrimSpace(roots[pin.Value])
		} else {
			row.Root = pin.Value
		}
		rows = append(rows, row)
	}
	return rows, resolution, nil
}

// discoveryPaths returns the paths the pin collections contribute to filesystem
// candidate discovery and pane attribution.
//
// Both kinds contribute a path, but they get it from different places, and that is
// the point. A candidate pin contributes its own spelling, because that spelling
// is all anyone knows about it. A managed pin contributes the root the Registry
// currently holds, so a rebind moves the discovery input with the Project instead
// of leaving a stale directory in the scan.
//
// Contributing a path here is not membership. Nothing in discovery registers a
// Project; the sidebar drops any discovered path a Project already claims, and
// what remains is a candidate.
func (a pinAuthority) discoveryPaths() ([]string, error) {
	stored, resolver, err := a.read()
	if err != nil {
		return nil, err
	}
	roots := map[string]string{}
	for _, ref := range resolver.Projects {
		roots[ref.UID] = ref.Root
	}
	resolution := resolver.Resolve(stored)
	out := make([]string, 0, len(resolution.Set.Pins))
	for _, pin := range resolution.Set.Pins {
		switch pin.Kind {
		case pins.KindCandidate:
			out = append(out, pin.Value)
		case pins.KindProject:
			if root := strings.TrimSpace(roots[pin.Value]); root != "" {
				out = append(out, root)
			}
		}
	}
	return out, nil
}

// errAmbiguousPinTarget refuses to type a pin whose path more than one Project
// claims. Choosing either uid would move the operator's preference onto a Project
// they did not name.
type errAmbiguousPinTarget struct {
	path string
	uids []string
}

func (e *errAmbiguousPinTarget) Error() string {
	return fmt.Sprintf("pin %s: %d Projects claim that root (%s); name one with `uid:<uid>` or repair the duplicate with `projmux rebind project`",
		e.path, len(e.uids), strings.Join(e.uids, ", "))
}

// pinTargetForPath types a path argument without changing what the operator may
// type.
//
// The compatibility rule is resolve-or-candidate, and it is the only place a path
// is allowed to decide a pin's kind: exactly one Project with that root makes the
// pin managed, no Project makes it a candidate, and more than one is refused. The
// match folds path spellings, which is a statement about the path; the uid it
// finds already existed, so nothing here mints or merges managed identity.
func (a pinAuthority) pinTargetForPath(path string) (pins.Pin, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return pins.Pin{}, fmt.Errorf("%w: empty path", pins.ErrInvalidPin)
	}
	refs, err := a.refs()
	if err != nil {
		return pins.Pin{}, err
	}
	var uids []string
	key := candidates.MatchKey(path)
	for _, ref := range refs {
		if key != "" && candidates.MatchKey(ref.Root) == key {
			uids = append(uids, ref.UID)
		}
	}
	switch len(uids) {
	case 1:
		return pins.ProjectPin(uids[0])
	case 0:
		return pins.CandidatePin(path)
	default:
		return pins.Pin{}, &errAmbiguousPinTarget{path: path, uids: uids}
	}
}

// pinTargetForSelector types an explicit `uid:<uid>` argument or falls back to the
// path rule.
func (a pinAuthority) pinTargetForSelector(value string) (pins.Pin, error) {
	value = strings.TrimSpace(value)
	if uid, ok := strings.CutPrefix(value, "uid:"); ok {
		return pins.ProjectPin(uid)
	}
	return a.pinTargetForPath(value)
}

// mutate migrates first, then applies one typed change, and writes only when the
// result differs. A repeated pin action therefore reaches the filesystem zero
// times.
func (a pinAuthority) mutate(apply func(pins.Set) pins.Set) error {
	if _, err := a.migrate(); err != nil {
		return err
	}
	stored, err := a.store.Load()
	if err != nil {
		return err
	}
	if !stored.Format.Typed() {
		return fmt.Errorf("pin file %s still holds legacy path lines", a.store.Path())
	}
	next := apply(stored)
	if next.Equal(stored) {
		return nil
	}
	return a.store.Save(next)
}

// add pins a typed target.
func (a pinAuthority) add(pin pins.Pin) error {
	if _, err := pinValidated(pin); err != nil {
		return err
	}
	return a.mutate(func(set pins.Set) pins.Set { return set.With(pin) })
}

// remove unpins a typed target.
func (a pinAuthority) remove(pin pins.Pin) error {
	return a.mutate(func(set pins.Set) pins.Set { return set.Without(pin) })
}

// toggle flips a typed target and reports whether it is now pinned.
func (a pinAuthority) toggle(pin pins.Pin) (bool, error) {
	if _, err := pinValidated(pin); err != nil {
		return false, err
	}
	pinned := false
	err := a.mutate(func(set pins.Set) pins.Set {
		if set.Has(pin) {
			pinned = false
			return set.Without(pin)
		}
		pinned = true
		return set.With(pin)
	})
	if err != nil {
		return false, err
	}
	return pinned, nil
}

// clear drops every pin of both kinds.
//
// Unlike the other mutations it accepts a legacy file: dropping every preference
// needs no Registry lookup, and the empty typed envelope it leaves behind is the
// migrated state. An already-empty typed file is a write-free no-op.
func (a pinAuthority) clear() error {
	if a.store == nil {
		return errNoPinStore
	}
	stored, err := a.store.Load()
	if err != nil {
		return err
	}
	if len(stored.Pins) == 0 && stored.Format == pins.FormatTyped {
		return nil
	}
	return a.store.Save(pins.Set{Format: pins.FormatTyped})
}

// pinValidated re-runs the constructor validation on a pin built elsewhere, so a
// hand-assembled pins.Pin cannot reach the file unchecked.
func pinValidated(pin pins.Pin) (pins.Pin, error) {
	switch pin.Kind {
	case pins.KindProject:
		return pins.ProjectPin(pin.Value)
	case pins.KindCandidate:
		return pins.CandidatePin(pin.Value)
	default:
		return pins.Pin{}, fmt.Errorf("%w: unknown pin kind %q", pins.ErrInvalidPin, pin.Kind)
	}
}

// errNoPinStore reports an unconfigured pin store rather than silently doing
// nothing, so a broken configuration cannot look like an empty pin set.
var errNoPinStore = errors.New("pin store is not configured")
