package pins

import (
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/state"
)

// Store is the file half of the pin collection: it reads and it writes, and it
// decides nothing. Update runs a caller's decision under the pin file's lock,
// but the decision stays the caller's.
//
// Every rule about what a pin means -- which kind a path resolves to, when a
// legacy file may be rewritten, when a mutation is a no-op -- lives in pure
// functions over Set instead, so the surfaces that own those decisions can be
// tested without a filesystem and cannot each grow their own version.
//
// Reads never write. That is not an optimization: rendering the sidebar, listing
// the pins and resolving a tier all read this file, and a read that rewrote it
// would turn every refresh into a migration attempt against whatever the Registry
// happened to look like at that moment.
type Store struct {
	file state.LinesFile
	// afterLoad is a test seam run inside a locked update after the stored set
	// was parsed.
	afterLoad func()
}

// NewStore builds a pin store for the provided file path.
func NewStore(path string) Store {
	return Store{file: state.NewLinesFile(path)}
}

// NewDefaultStore builds a pin store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.PinFile())
}

// Path returns the file path used by this store.
func (s Store) Path() string {
	return s.file.Path()
}

// Load returns the stored set exactly as written, with the format it was written
// in. A legacy file comes back as candidate pins with FormatLegacy; resolving it
// against the Registry is the caller's separate, write-free step.
func (s Store) Load() (Set, error) {
	lines, err := s.file.Read()
	if err != nil {
		return Set{}, err
	}
	return parse(lines)
}

// Save replaces the file with the typed envelope of set, atomically.
func (s Store) Save(set Set) error {
	if err := validSet(set); err != nil {
		return err
	}
	return s.file.Write(format(set))
}

// Update runs one read-modify-write of the pin file under its exclusive lock, so
// overlapping updates never drop each other's changes. update receives the
// stored set exactly as Load would return it and returns the next set and
// whether to write it. write=false or an error writes nothing; a returned set
// holding an invalid pin is refused the same way Save refuses it.
func (s Store) Update(update func(Set) (Set, bool, error)) error {
	return s.file.Update(func(lines []string) ([]string, bool, error) {
		stored, err := parse(lines)
		if err != nil {
			return nil, false, err
		}
		if s.afterLoad != nil {
			s.afterLoad()
		}
		next, write, err := update(stored)
		if err != nil || !write {
			return nil, false, err
		}
		if err := validSet(next); err != nil {
			return nil, false, err
		}
		return format(next), true, nil
	})
}

func validSet(set Set) error {
	for _, pin := range set.Pins {
		if _, err := validPin(pin); err != nil {
			return err
		}
	}
	return nil
}
