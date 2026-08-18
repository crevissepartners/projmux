package pins

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
)

// ProjectRef is the identity half of one Registry Project: the uid a managed pin
// stores and the root a legacy path pin has to be matched against.
type ProjectRef struct {
	UID  string
	Root string
}

// Move is one legacy path that resolved to exactly one Project.
type Move struct {
	Path string
	UID  string
}

// Ambiguity is one legacy path that more than one Project claims.
//
// It is the whole reason migration can refuse. Two Projects whose roots are two
// spellings of one directory is a real Registry state -- a rebind and an explicit
// register can produce it -- and picking either uid would silently move the
// operator's pin onto a Project they did not name.
type Ambiguity struct {
	Path string
	UIDs []string
}

// Resolution is the typed reading of a set plus what resolving it involved.
type Resolution struct {
	// From is the format the resolved set was read from. It is the only place the
	// pre-v2 shape survives resolution, so a caller can say "this file is still
	// legacy" even when every one of its lines happened to stay a candidate.
	From Format
	// Set is the typed projection. It is safe to render and never written by
	// resolving; Store.Migrate is the only thing that persists it.
	Set Set
	// Moved lists the legacy paths that became managed pins.
	Moved []Move
	// Kept lists the legacy paths no Project claimed. They stay candidate pins:
	// zero matches means "not registered", not "delete the preference".
	Kept []string
	// Ambiguous lists the legacy paths more than one Project claimed. They are
	// left as candidate pins so nothing disappears from the surface, and they are
	// reported so a caller that is about to write can refuse instead.
	Ambiguous []Ambiguity
}

// Resolver matches legacy path pins against Registry Projects.
//
// GOOS selects the path folding rules, so a Windows resolution is assertable from
// a Linux test run. An empty GOOS uses the running one.
type Resolver struct {
	GOOS     string
	Projects []ProjectRef
}

// Resolve returns the typed reading of set. It writes nothing and it mints
// nothing: a path either matches exactly one already-existing Project uid or it
// stays a path.
func (r Resolver) Resolve(set Set) Resolution {
	if set.Format.Typed() {
		return Resolution{From: set.Format, Set: set}
	}
	byKey := map[string][]string{}
	for _, project := range r.Projects {
		key := r.key(project.Root)
		uid := strings.TrimSpace(project.UID)
		if key == "" || uid == "" {
			continue
		}
		byKey[key] = append(byKey[key], uid)
	}
	for key := range byKey {
		sort.Strings(byKey[key])
	}

	out := Resolution{From: set.Format, Set: Set{Format: FormatTyped}}
	for _, pin := range set.Pins {
		if pin.Kind != KindCandidate {
			out.Set = out.Set.With(pin)
			continue
		}
		uids := byKey[r.key(pin.Value)]
		switch len(uids) {
		case 1:
			managed, err := ProjectPin(uids[0])
			if err != nil {
				// A Registry uid the pin envelope cannot hold is not a uid this
				// migration invents a spelling for. The path stays a path.
				out.Set = out.Set.With(pin)
				out.Kept = append(out.Kept, pin.Value)
				continue
			}
			out.Set = out.Set.With(managed)
			out.Moved = append(out.Moved, Move{Path: pin.Value, UID: uids[0]})
		case 0:
			out.Set = out.Set.With(pin)
			out.Kept = append(out.Kept, pin.Value)
		default:
			out.Set = out.Set.With(pin)
			out.Ambiguous = append(out.Ambiguous, Ambiguity{Path: pin.Value, UIDs: uids})
		}
	}
	return out
}

func (r Resolver) key(path string) string {
	if strings.TrimSpace(r.GOOS) == "" {
		return candidates.MatchKey(path)
	}
	return candidates.MatchKeyFor(r.GOOS, path)
}

// AmbiguousMigrationError refuses a migration and names every unresolvable pin
// together with the repair action.
type AmbiguousMigrationError struct {
	Path      string
	Ambiguous []Ambiguity
}

func (e *AmbiguousMigrationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "migrate pins in %s: %d pinned path(s) match more than one Project", e.Path, len(e.Ambiguous))
	for _, ambiguity := range e.Ambiguous {
		fmt.Fprintf(&b, "; %s matches %s", ambiguity.Path, strings.Join(ambiguity.UIDs, ", "))
	}
	b.WriteString("; the pin file is unchanged. Repair with `projmux rebind project` so one Project claims the path, then re-run `projmux pin project migrate`, or pin the Project you meant with `projmux pin project add uid:<uid>`")
	return b.String()
}
