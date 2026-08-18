// Package pins stores the sidebar's pin preferences.
//
// A pin is a presentation preference and nothing else. It says "keep this at the
// top of the list"; it does not say what exists. That distinction is the whole
// point of the type on every entry:
//
//   - A managed pin names a Registry Project uid. The root and the name it
//     displays are projected from the Registry every time it is rendered, so the
//     pin follows the Project through a rebind, a rename and a missing root.
//   - A candidate pin names a filesystem path that no Project claims. It is a
//     preference about a directory the operator has not registered, and it stays
//     one: reading it, rendering it, or pinning it never mints a Project.
//
// Neither kind is a discovery source and neither is managed identity. Workdirs
// and project roots find candidate directories; the Registry owns which of them
// are Projects. Three collections, three authorities.
package pins

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Kind is the authority one pin entry points at.
type Kind string

const (
	// KindProject pins a managed Registry Project by uid.
	KindProject Kind = "project"
	// KindCandidate pins an unregistered filesystem path.
	KindCandidate Kind = "candidate"
)

// ErrInvalidPin is returned for a value no pin of that kind can hold.
var ErrInvalidPin = errors.New("invalid pin")

// projectUIDPrefix is the uid prefix metadata.NewUID allocates for a Project.
// A pin that names any other kind of resource is a corrupt entry, not a pin the
// sidebar can render, so it is refused at parse time rather than silently kept.
const projectUIDPrefix = "proj-"

// Pin is one typed presentation preference.
type Pin struct {
	Kind  Kind
	Value string
}

// ProjectPin builds a validated managed pin.
func ProjectPin(uid string) (Pin, error) {
	return validPin(Pin{Kind: KindProject, Value: strings.TrimSpace(uid)})
}

// CandidatePin builds a validated candidate pin.
func CandidatePin(path string) (Pin, error) {
	return validPin(Pin{Kind: KindCandidate, Value: strings.TrimSpace(path)})
}

// String renders a pin for a CLI line and for the on-disk envelope. The two are
// deliberately the same text: what `pin project list` prints is what the file
// holds, so an operator reading either one is reading the same fact.
func (p Pin) String() string {
	return string(p.Kind) + " " + p.Value
}

func validPin(pin Pin) (Pin, error) {
	if pin.Value == "" || strings.ContainsAny(pin.Value, "\r\n") {
		return Pin{}, fmt.Errorf("%w: %s value %q", ErrInvalidPin, pin.Kind, pin.Value)
	}
	switch pin.Kind {
	case KindProject:
		if strings.ContainsAny(pin.Value, " \t") || !strings.HasPrefix(pin.Value, projectUIDPrefix) {
			return Pin{}, fmt.Errorf("%w: %q is not a Project uid", ErrInvalidPin, pin.Value)
		}
	case KindCandidate:
	default:
		return Pin{}, fmt.Errorf("%w: unknown pin kind %q", ErrInvalidPin, pin.Kind)
	}
	return pin, nil
}

// Format is the on-disk shape a Set was read from.
type Format string

const (
	// FormatAbsent is an empty or missing pin file. It is already typed: there
	// is nothing legacy to migrate.
	FormatAbsent Format = "absent"
	// FormatLegacy is the pre-v2 file: one bare path per line, with no statement
	// about whether that path is a Project.
	FormatLegacy Format = "legacy-paths"
	// FormatTyped is the v2 envelope.
	FormatTyped Format = "typed-v2"
)

// Typed reports whether a set needs no migration.
func (f Format) Typed() bool {
	return f != FormatLegacy
}

// Set is one ordered pin collection together with the format it was read from.
//
// Order is insertion order and it is preserved verbatim: the sidebar's tier
// comparator is stable, so the file's order is what decides how two pinned
// Projects sit relative to each other.
type Set struct {
	Pins   []Pin
	Format Format
}

// LegacyPaths returns the bare paths of a legacy set, in file order.
//
// A typed set has none: its paths are candidate pins and its Projects are uids.
func (s Set) LegacyPaths() []string {
	if s.Format != FormatLegacy {
		return nil
	}
	out := make([]string, 0, len(s.Pins))
	for _, pin := range s.Pins {
		if pin.Kind == KindCandidate {
			out = append(out, pin.Value)
		}
	}
	return out
}

// ProjectUIDs returns the managed pins in order.
func (s Set) ProjectUIDs() []string {
	return s.valuesOf(KindProject)
}

// CandidatePaths returns the candidate pins in order.
func (s Set) CandidatePaths() []string {
	return s.valuesOf(KindCandidate)
}

func (s Set) valuesOf(kind Kind) []string {
	out := make([]string, 0, len(s.Pins))
	for _, pin := range s.Pins {
		if pin.Kind == kind {
			out = append(out, pin.Value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Has reports whether an exact typed pin is present.
func (s Set) Has(pin Pin) bool {
	return slices.Contains(s.Pins, pin)
}

// With returns a copy with pin appended, or the same set when it is already
// present. Adding a pin twice is a no-op rather than a duplicate row, which is
// what makes a repeated pin action write-free.
func (s Set) With(pin Pin) Set {
	if s.Has(pin) {
		return s
	}
	out := s.clone()
	out.Pins = append(out.Pins, pin)
	return out
}

// Without returns a copy with every occurrence of pin removed.
func (s Set) Without(pin Pin) Set {
	out := s.clone()
	out.Pins = slices.DeleteFunc(out.Pins, func(candidate Pin) bool {
		return candidate == pin
	})
	return out
}

// Equal reports whether two sets hold the same pins in the same order. Format is
// not compared: it answers "would writing this change the file's meaning", which
// is what a write-free no-op check needs.
func (s Set) Equal(other Set) bool {
	return slices.Equal(s.Pins, other.Pins)
}

func (s Set) clone() Set {
	return Set{Pins: slices.Clone(s.Pins), Format: s.Format}
}
