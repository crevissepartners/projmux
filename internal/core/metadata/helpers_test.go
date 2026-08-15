package metadata

import (
	"fmt"
	"strings"
	"time"
)

// fixedNow is the deterministic clock used by every metadata unit test.
var fixedNow = time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)

// sequentialUIDs mints predictable uids so goldens and table expectations can
// name a resource without depending on crypto/rand.
func sequentialUIDs() func(Kind) (string, error) {
	counts := map[Kind]int{}
	return func(kind Kind) (string, error) {
		counts[kind]++
		return fmt.Sprintf("%s-%02d", strings.ToLower(string(kind)), counts[kind]), nil
	}
}

// dirSet is a map-backed directory probe. Tests add and remove roots to model
// a project root disappearing and returning.
type dirSet map[string]bool

func (d dirSet) exists(path string) (bool, error) { return d[path], nil }

// testMutator builds a fully deterministic mutator over the supplied roots.
func testMutator(roots dirSet) Mutator {
	return Mutator{
		Now:       func() time.Time { return fixedNow },
		NewUID:    sequentialUIDs(),
		DirExists: roots.exists,
	}
}

// registerFixture registers one project with the default topology.
func registerFixture(m Mutator, reg *Registry, root string) (RegisterProjectResult, error) {
	return m.RegisterProject(reg, RegisterProjectOptions{
		Root:         root,
		DefaultShell: "/bin/zsh",
		OperationID:  "op-fixture",
	})
}
