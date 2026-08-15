package metadata

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/paneidentity"
)

// maxNameLength bounds a name so it stays usable as a CLI query key.
const maxNameLength = 128

// maxAutoSuffix bounds the automatic suffix search. Exceeding it is a hard
// error rather than a silent fallback to a non-unique name.
const maxAutoSuffix = 100000

// Fallback name bases used when no better seed exists.
const (
	FallbackProjectNameBase = "project"
	FallbackWindowNameBase  = "window"
	FallbackPaneNameBase    = "pane"
	FallbackAgentNameBase   = "agent"
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

// commandBase extracts the basename of the executable in a command line.
// "nvim ." -> "nvim", "/usr/bin/zsh -l" -> "zsh".
func commandBase(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	return SanitizeNameBase(filepath.Base(fields[0]))
}

// shellBase extracts the basename of a configured shell path.
func shellBase(shell string) string {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		return ""
	}
	return SanitizeNameBase(filepath.Base(shell))
}

// ProjectNameBase derives the Project name base from its root basename.
// Project displayName seeds from the same value and may duplicate.
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

// ProjectDisplayName derives the duplicate-allowed Project display name.
func ProjectDisplayName(root string) string {
	root = cleanRoot(root)
	if root == "" || root == string(filepath.Separator) {
		return FallbackProjectNameBase
	}
	if base := strings.TrimSpace(filepath.Base(root)); base != "" {
		return base
	}
	return FallbackProjectNameBase
}

// WindowNameBase applies the one-time Window naming order: explicit name,
// initial command basename, configured shell basename, then "window". Agent
// topic and raw pane title are deliberately excluded as name seeds.
func WindowNameBase(explicit, command, shell string) string {
	if base := SanitizeNameBase(explicit); base != "" {
		return base
	}
	if base := commandBase(command); base != "" {
		return base
	}
	if base := shellBase(shell); base != "" {
		return base
	}
	return FallbackWindowNameBase
}

// PaneNameBase applies the shell Pane naming order: command basename,
// configured shell basename, then "pane".
func PaneNameBase(command, shell string) string {
	if base := commandBase(command); base != "" {
		return base
	}
	if base := shellBase(shell); base != "" {
		return base
	}
	return FallbackPaneNameBase
}

// ManagedPaneNameBase is the "<agent-name>-pane" base used for a Pane managed
// by an Agent.
func ManagedPaneNameBase(agentName string) string {
	if base := SanitizeNameBase(agentName); base != "" {
		return base + "-" + FallbackPaneNameBase
	}
	return FallbackPaneNameBase
}

// AgentNameBase applies the Agent naming order: explicit --name, normalized
// provider id, then "agent" when the provider is unknown.
func AgentNameBase(explicit, provider string) string {
	if base := SanitizeNameBase(explicit); base != "" {
		return base
	}
	if id := NormalizeProvider(provider); id != "" {
		return id
	}
	return FallbackAgentNameBase
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

// DerivePaneDisplayTitle computes the secondary, derived pane title from the
// Agent topic, a known interactive shell command, or the raw pane title. It is
// never a selector, an identity, or a Window name source, and the Pane
// metadata.name is deliberately not an input.
func DerivePaneDisplayTitle(agent, topic, command, rawTitle string) string {
	identity := paneidentity.Resolve(paneidentity.Inputs{
		AIAgent: agent,
		AITopic: topic,
		Command: command,
		Title:   rawTitle,
	})
	return identity.Value
}

// nameKey identifies one reservation slot.
type nameKey struct {
	Scope string
	Kind  Kind
	Name  string
}

// scopeFor returns the uniqueness scope for a kind and owner. Project names
// are unique across the whole registry, so their scope is empty.
func scopeFor(kind Kind, ownerUID string) string {
	if kind == KindProject {
		return ""
	}
	return ownerUID
}

func (r *Registry) reservationIndex() map[nameKey]string {
	index := make(map[nameKey]string, len(r.NameReservations))
	for _, reservation := range r.NameReservations {
		index[nameKey{Scope: reservation.Scope, Kind: reservation.Kind, Name: reservation.Name}] = reservation.UID
	}
	return index
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
func (r *Registry) reserveExplicitName(op, scope string, kind Kind, name, uid string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if owner, taken := r.nameOwner(scope, kind, name); taken && owner != uid {
		return inputErr(op, ErrNameConflict, "%s name %q is already used by %s", strings.ToLower(string(kind)), name, owner)
	}
	r.putReservation(scope, kind, name, uid)
	return nil
}

// allocateName claims base, or the lowest free `base-N` suffix when base is
// taken. Allocation reads the persisted reservation set and scans integer
// suffixes in ascending order, so the result never depends on resource scan or
// map iteration order.
func (r *Registry) allocateName(op, scope string, kind Kind, base, uid string) (string, error) {
	base = SanitizeNameBase(base)
	if base == "" {
		return "", inputErr(op, ErrInvalidName, "cannot derive a %s name base", strings.ToLower(string(kind)))
	}
	index := r.reservationIndex()
	if owner, taken := index[nameKey{Scope: scope, Kind: kind, Name: base}]; !taken || owner == uid {
		if err := ValidateName(base); err != nil {
			return "", err
		}
		r.putReservation(scope, kind, base, uid)
		return base, nil
	}
	for suffix := 1; suffix <= maxAutoSuffix; suffix++ {
		candidate := base + "-" + strconv.Itoa(suffix)
		if owner, taken := index[nameKey{Scope: scope, Kind: kind, Name: candidate}]; taken && owner != uid {
			continue
		}
		if err := ValidateName(candidate); err != nil {
			return "", err
		}
		r.putReservation(scope, kind, candidate, uid)
		return candidate, nil
	}
	return "", stateErr(op, ErrNameExhausted, "no free %s name for base %q after %d suffixes", strings.ToLower(string(kind)), base, maxAutoSuffix)
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

// releaseNames drops every reservation held by uid, in any scope, and every
// reservation scoped to uid as an owner. Deleting a resource frees both the
// name it held and the scope it owned.
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
