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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-StaleTempAge)
	for _, entry := range entries {
		name := entry.Name()
		if len(name) <= len(prefix)+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
