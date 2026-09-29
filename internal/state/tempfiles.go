package state

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleTempAge is how old an atomic-replace temp file must be before
// ReclaimStaleTemps treats it as abandoned.
const StaleTempAge = time.Minute

// ReclaimStaleTemps removes abandoned temp files an atomic-replace writer
// left in dir. pattern is the writer's own os.CreateTemp pattern, so only
// names CreateTemp could have produced for it match: the prefix before "*",
// the suffix after it, and a non-empty random part between them.
//
// A writer killed between CreateTemp and Rename never runs its deferred
// Remove, so the temp stays behind; the next successful write reclaims it.
// Temps younger than StaleTempAge may belong to an in-flight writer and are
// kept. Only regular files are removed, and every error is ignored because
// reclaiming is best effort and must never fail the write that triggered it.
func ReclaimStaleTemps(dir, pattern string) {
	prefix, suffix, ok := strings.Cut(pattern, "*")
	if !ok {
		return
	}
	ReclaimStaleTempsMatching(dir, func(name string) bool {
		return len(name) > len(prefix)+len(suffix) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix)
	}, nil)
}

// ReclaimStaleTempsMatching is ReclaimStaleTemps for a writer whose temp names
// one CreateTemp pattern cannot describe, such as a store that stages several
// files whose names carry a variable stamp. match decides from the name alone
// whether a writer could have created it; the age guard, the regular-file
// check, and the best-effort contract are the same. remove replaces os.Remove
// when not nil.
func ReclaimStaleTempsMatching(dir string, match func(name string) bool, remove func(path string) error) {
	if match == nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if remove == nil {
		remove = os.Remove
	}
	cutoff := time.Now().Add(-StaleTempAge)
	for _, entry := range entries {
		name := entry.Name()
		if !match(name) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		_ = remove(filepath.Join(dir, name))
	}
}

// RemoveLockedTemps removes the temp files that writes killed before their
// rename left in dir: every regular file whose name starts with prefix. It is
// for writers that create those temps only while holding a lock the kernel
// releases when its holder dies, and it must be called with that lock held, so
// any such file belongs to no live write and needs no age guard. remove
// replaces os.Remove when not nil. Removal is best effort: a file that cannot
// be listed or removed stays for a later write and never fails this one.
func RemoveLockedTemps(dir, prefix string, remove func(path string) error) {
	if prefix == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if remove == nil {
		remove = os.Remove
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		_ = remove(filepath.Join(dir, entry.Name()))
	}
}
