package metadata

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

// maxNameLength bounds a name so it stays usable as a CLI query key.
const maxNameLength = 128

// maxUIDNameAttempts bounds unpublished UID/name candidate minting. A
// collision discards the candidate; it never creates a numeric name suffix.
const maxUIDNameAttempts = 100

// Legacy/context fallbacks. Neither participates in automatic Registry naming.
const (
	FallbackProjectNameBase = "project"
	FallbackWindowNameBase  = "window"
)

// ValidateName rejects names that cannot serve as a stable, unambiguous query
// key. Case is preserved: `Projmux` and `projmux` are distinct names.
func ValidateName(name string) error {
	switch {
	case name == "":
		return inputErr("name", ErrInvalidName, "name must not be empty")
	case name != strings.TrimSpace(name):
		return inputErr("name", ErrInvalidName, "name %q must not have leading or trailing whitespace", name)
	case len(name) > maxNameLength:
		return inputErr("name", ErrInvalidName, "name %q exceeds %d bytes", name, maxNameLength)
	case name == "." || name == "..":
		return inputErr("name", ErrInvalidName, "name %q is reserved", name)
	}
	for _, r := range name {
		if !nameRuneAllowed(r) {
			return inputErr("name", ErrInvalidName, "name %q contains an unsupported character %q", name, string(r))
		}
	}
	return nil
}

// ValidateExplicitNameReuse accepts an omitted reuse name or the exact stored
// spelling. A different explicit name is a rename request, not registration
// reuse, and is refused before any mutation.
func ValidateExplicitNameReuse(op string, kind Kind, existing ObjectMeta, requested string) error {
	if requested == "" {
		return nil
	}
	if err := ValidateName(requested); err != nil {
		return err
	}
	if requested != existing.Name {
		return inputErr(op, ErrNameConflict,
			"%s %s is already registered as %q; explicit name %q requires rename",
			kind, existing.UID, existing.Name, requested)
	}
	return nil
}

// nameRuneAllowed keeps names free of whitespace, path separators, and the
// punctuation the Phase 2 selector grammar reserves.
func nameRuneAllowed(r rune) bool {
	if unicode.IsControl(r) || unicode.IsSpace(r) {
		return false
	}
	switch r {
	case '/', '\\', ':', ',', '=', '@', '"', '\'', '`', '$', '(', ')', '[', ']', '{', '}', '<', '>', '|', '&', ';', '*', '?', '!', '#', '%', '^', '~', '+':
		return false
	}
	return true
}

// SanitizeNameBase turns an arbitrary seed into a valid name base. Runes that
// cannot appear in a name collapse into a single `-`. An empty result means
// the caller must fall back to the next seed in its priority order.
func SanitizeNameBase(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range seed {
		if nameRuneAllowed(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > maxNameLength {
		out = strings.Trim(out[:maxNameLength], "-.")
	}
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// ProjectNameBase derives the historical path-based Project lookup spelling.
// It is used only to recognize a previously registered root when old callers
// provide that spelling; automatic Registry names are always exact UIDs.
func ProjectNameBase(root string) string {
	root = cleanRoot(root)
	if root == "" || root == string(filepath.Separator) {
		return FallbackProjectNameBase
	}
	if base := SanitizeNameBase(filepath.Base(root)); base != "" {
		return base
	}
	return FallbackProjectNameBase
}

// NormalizeProvider maps a provider spelling onto its registered id
// (codex, claude, antigravity) and returns "" when it is unknown.
func NormalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return ""
	}
	if meta, ok := aiprovider.Lookup(provider); ok {
		return string(meta.ID)
	}
	return ""
}

// nameKey identifies one reservation slot.
type nameKey struct {
	Scope string
	Kind  Kind
	Name  string
}

// scopeFor returns the v4 root-wide uniqueness scope for a kind and direct
// owner. Project and ControlSession names are kind-global and use an empty
// scope; every descendant uses the exact Project or ControlSession UID at the
// top of its owner chain.
//
// The two root kinds share the empty scope but not a slot: nameKey carries the
// Kind, so a Project named `home` and a ControlSession named `home` are two
// reservations that never collide. That is deliberate -- a control session's
// name comes from a tmux session name the operator chose long before any
// Project existed, and failing `projmux shell` because a Project already holds
// that word would make the app's own entrypoint hostage to registry contents.
func (r Registry) scopeFor(kind Kind, ownerUID string) (string, error) {
	if kind == KindProject || kind == KindControlSession {
		return "", nil
	}
	ownerUID = strings.TrimSpace(ownerUID)
	switch kind {
	case KindWindow:
		if _, ok := r.Project(ownerUID); ok {
			return ownerUID, nil
		}
		if _, ok := r.ControlSession(ownerUID); ok {
			return ownerUID, nil
		}
	case KindAgent:
		if window, ok := r.Window(ownerUID); ok {
			return r.scopeFor(KindWindow, window.Metadata.OwnerUID())
		}
	case KindPane:
		if window, ok := r.Window(ownerUID); ok {
			return r.scopeFor(KindWindow, window.Metadata.OwnerUID())
		}
		if agent, ok := r.Agent(ownerUID); ok {
			return r.scopeFor(KindAgent, agent.Metadata.OwnerUID())
		}
	}
	return "", stateErr("resolve name scope", ErrInvalidRegistry,
		"cannot resolve %s owner %q to a Project or ControlSession root", kind, ownerUID)
}

// nameOwner reports the uid holding name inside scope.
func (r *Registry) nameOwner(scope string, kind Kind, name string) (string, bool) {
	for _, reservation := range r.NameReservations {
		if reservation.Scope == scope && reservation.Kind == kind && reservation.Name == name {
			return reservation.UID, true
		}
	}
	return "", false
}

// reserveExplicitName claims an operator-supplied name. A collision fails with
// ErrNameConflict and never falls back to an implicit suffix.
func (r *Registry) reserveExplicitName(op, ownerUID string, kind Kind, name, uid string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	scope, err := r.scopeFor(kind, ownerUID)
	if err != nil {
		return err
	}
	if owner, taken := r.nameOwner(scope, kind, name); taken && owner != uid {
		return inputErr(op, ErrNameConflict, "%s name %q is already used by %s", strings.ToLower(string(kind)), name, owner)
	}
	r.putReservation(scope, kind, name, uid)
	return nil
}

// mintAndReserveName mints one resource identity and reserves its address.
// Automatic addresses are the exact full UID. A colliding unpublished UID/name
// candidate is discarded and reminted at most 100 times; no sibling scan,
// semantic base, prefix truncation, or integer suffix participates.
func (m Mutator) mintAndReserveName(reg *Registry, op, ownerUID string, kind Kind, explicit string) (string, string, error) {
	if explicit != "" {
		if err := ValidateName(explicit); err != nil {
			return "", "", err
		}
		scope, err := reg.scopeFor(kind, ownerUID)
		if err != nil {
			return "", "", err
		}
		if owner, taken := reg.nameOwner(scope, kind, explicit); taken {
			return "", "", inputErr(op, ErrNameConflict, "%s name %q is already used by %s", strings.ToLower(string(kind)), explicit, owner)
		}
	}
	for range maxUIDNameAttempts {
		uid, err := m.mintUID(kind)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(uid) == "" || registryHasUID(*reg, uid) {
			continue
		}
		name := explicit
		if name == "" {
			name = uid
			if err := ValidateName(name); err != nil {
				continue
			}
			scope, err := reg.scopeFor(kind, ownerUID)
			if err != nil {
				return "", "", err
			}
			if owner, taken := reg.nameOwner(scope, kind, name); taken && owner != uid {
				continue
			}
		}
		if err := reg.reserveExplicitName(op, ownerUID, kind, name, uid); err != nil {
			return "", "", err
		}
		return uid, name, nil
	}
	return "", "", stateErr(op, ErrNameExhausted,
		"no free %s uid/name pair after %d candidates", strings.ToLower(string(kind)), maxUIDNameAttempts)
}

// putReservation records or replaces the reservation for name inside scope and
// drops any other reservation the uid held for the same scope and kind.
func (r *Registry) putReservation(scope string, kind Kind, name, uid string) {
	kept := r.NameReservations[:0]
	replaced := false
	for _, reservation := range r.NameReservations {
		sameSlot := reservation.Scope == scope && reservation.Kind == kind && reservation.Name == name
		staleForUID := reservation.Scope == scope && reservation.Kind == kind && reservation.UID == uid
		if sameSlot {
			reservation.UID = uid
			kept = append(kept, reservation)
			replaced = true
			continue
		}
		if staleForUID {
			continue
		}
		kept = append(kept, reservation)
	}
	r.NameReservations = kept
	if !replaced {
		r.NameReservations = append(r.NameReservations, NameReservation{Scope: scope, Kind: kind, Name: name, UID: uid})
	}
}

func sortNameReservations(reservations []NameReservation) {
	sort.SliceStable(reservations, func(i, j int) bool {
		a, b := reservations[i], reservations[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UID < b.UID
	})
}

// rescopeName moves one uid's existing reservation from oldScope to newScope,
// keeping the name byte-identical.
//
// Reservations are keyed by (root scope, kind, name). A same-root Pane
// re-parent therefore preserves the exact reservation bytes; a cross-root move
// changes only the scope after proving the destination slot is free. The name
// itself is deliberately untouched: existing names are never silently
// rewritten, and a re-parent is not a rename.
//
// It reports false and writes nothing when the name is already taken in
// newScope by a different uid. A caller that cannot move the reservation must
// not move the resource either, or the two would disagree.
func (r *Registry) rescopeName(oldScope, newScope string, kind Kind, name, uid string) bool {
	oldRoot, oldErr := r.scopeFor(kind, oldScope)
	newRoot, newErr := r.scopeFor(kind, newScope)
	if oldErr != nil || newErr != nil {
		return false
	}
	if oldRoot == newRoot {
		return true
	}
	if owner, taken := r.nameOwner(newRoot, kind, name); taken && owner != uid {
		return false
	}
	kept := r.NameReservations[:0]
	for _, reservation := range r.NameReservations {
		if reservation.Scope == oldRoot && reservation.Kind == kind && reservation.UID == uid {
			continue
		}
		kept = append(kept, reservation)
	}
	r.NameReservations = kept
	r.putReservation(newRoot, kind, name, uid)
	return true
}

// releaseNames drops every reservation held by uid and every reservation whose
// root scope is uid. Deleting a root therefore frees its whole namespace;
// deleting a descendant frees only the names of the exact cascade members.
func (r *Registry) releaseNames(uid string) {
	if uid == "" {
		return
	}
	kept := r.NameReservations[:0]
	for _, reservation := range r.NameReservations {
		if reservation.UID == uid || reservation.Scope == uid {
			continue
		}
		kept = append(kept, reservation)
	}
	r.NameReservations = kept
	if len(r.NameReservations) == 0 {
		r.NameReservations = nil
	}
}
