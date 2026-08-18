package pins

import (
	"errors"
	"fmt"
	"strings"
)

// envelopeHeader is the first line of the typed pin file.
//
// Its only job is to make the two formats tell themselves apart without a
// heuristic. A legacy file holds absolute paths, so its first line can never be
// this literal, and a v2 file always starts with it.
const envelopeHeader = "projmux-pins v2"

// ErrCorruptPinFile is returned for a typed file with a line the envelope cannot
// mean. It is deliberately not recoverable by guessing: a pin is a preference,
// and a wrong guess about which resource a preference points at is worse than
// refusing to load one.
var ErrCorruptPinFile = errors.New("corrupt pin file")

// ErrUnsupportedPinVersion is returned for an envelope written by a newer
// projmux. Downgrading rewrites the file, so the older binary refuses to read it
// rather than dropping the entries it does not understand.
var ErrUnsupportedPinVersion = errors.New("unsupported pin file version")

// parse turns stored lines into a typed set.
//
// Reading is the only thing it does. A legacy file parses into candidate pins and
// keeps FormatLegacy, which is what tells the caller a migration is available;
// the file itself is untouched until something explicitly migrates it.
func parse(lines []string) (Set, error) {
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed = append(trimmed, line)
	}
	if len(trimmed) == 0 {
		return Set{Format: FormatAbsent}, nil
	}
	if strings.TrimSpace(trimmed[0]) != envelopeHeader {
		if version, ok := envelopeVersion(trimmed[0]); ok {
			return Set{}, fmt.Errorf("%w: %q was written by a newer projmux; upgrade instead of downgrading", ErrUnsupportedPinVersion, version)
		}
		return parseLegacy(trimmed)
	}
	set := Set{Format: FormatTyped, Pins: make([]Pin, 0, len(trimmed)-1)}
	for i, line := range trimmed[1:] {
		kind, value, found := strings.Cut(strings.TrimRight(line, " \t"), " ")
		if !found {
			return Set{}, fmt.Errorf("%w: line %d %q is not \"<kind> <value>\"", ErrCorruptPinFile, i+2, line)
		}
		pin, err := validPin(Pin{Kind: Kind(kind), Value: strings.TrimSpace(value)})
		if err != nil {
			return Set{}, fmt.Errorf("%w: line %d: %w", ErrCorruptPinFile, i+2, err)
		}
		if set.Has(pin) {
			continue
		}
		set.Pins = append(set.Pins, pin)
	}
	return set, nil
}

// envelopeVersion reports the version of a `projmux-pins vN` header line this
// binary does not know. A line that is not a header at all is not a version.
func envelopeVersion(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "projmux-pins v") {
		return "", false
	}
	return line, true
}

// parseLegacy reads the pre-v2 format: one bare path per line.
//
// Every line becomes a candidate pin, which is the honest unresolved reading --
// the file says "this path" and says nothing about whether a Project claims it.
// Resolve is what upgrades the ones that a Registry lookup answers exactly.
func parseLegacy(lines []string) (Set, error) {
	set := Set{Format: FormatLegacy, Pins: make([]Pin, 0, len(lines))}
	for i, line := range lines {
		pin, err := CandidatePin(line)
		if err != nil {
			return Set{}, fmt.Errorf("%w: line %d: %w", ErrCorruptPinFile, i+1, err)
		}
		if set.Has(pin) {
			continue
		}
		set.Pins = append(set.Pins, pin)
	}
	return set, nil
}

// format renders a typed set as the lines to store. A set with no pins still
// carries the header, so an emptied v2 file does not read back as legacy.
func format(set Set) []string {
	lines := make([]string, 0, len(set.Pins)+1)
	lines = append(lines, envelopeHeader)
	for _, pin := range set.Pins {
		lines = append(lines, pin.String())
	}
	return lines
}
