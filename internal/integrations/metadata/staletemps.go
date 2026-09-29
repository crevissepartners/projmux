package metadata

import (
	"path/filepath"
	"strings"
	"time"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

// stagedTempInfix separates a published name from the random part os.CreateTemp
// appends: every staged file of this store is created from the pattern
// "."+<published name>+".tmp-*".
const stagedTempInfix = ".tmp-"

// reclaimStaleTemps removes the staged files that Registry writes killed
// between CreateTemp and their rename left in the registry and recovery
// directories. A killed write never runs its deferred Remove, and the next
// write stages under a new random name, so without this nothing reclaims them.
//
// Call it only after a mutation committed a write and released the Registry
// lock. Holding the lock would not make the removal safer: a restore stages the
// registry, marker, and backup names under the recovery lock alone, without the
// Registry lock, so only the StaleTempAge guard tells an abandoned temp from
// one a live writer still owns. Running it outside the lock keeps the hold time
// unchanged. It is best effort and never fails the write that triggered it.
func (s *Store) reclaimStaleTemps() {
	localstate.ReclaimStaleTempsMatching(filepath.Dir(s.path), s.isRegistryDirTemp, s.hooks.removeStaleTemp)
	localstate.ReclaimStaleTempsMatching(s.recoveryDir, isRecoveryDirTemp, s.hooks.removeStaleTemp)
}

// isRegistryDirTemp reports whether name is a file this store stages next to
// the registry: the registry itself, the initialized marker, or a versioned
// backup and its migration report.
func (s *Store) isRegistryDirTemp(name string) bool {
	target, ok := stagedTempTarget(name)
	if !ok {
		return false
	}
	return target == filepath.Base(s.path) || target == markerFileName || s.isBackupName(target)
}

// isRecoveryDirTemp reports whether name is a file this store stages in the
// recovery directory: a write-side recovery copy or a restore's replaced copy.
func isRecoveryDirTemp(name string) bool {
	target, ok := stagedTempTarget(name)
	if !ok {
		return false
	}
	rest, ok := strings.CutPrefix(target, recoveryFilePrefix)
	if !ok {
		if rest, ok = strings.CutPrefix(target, preservedFilePrefix); !ok {
			return false
		}
	}
	rest, ok = strings.CutSuffix(rest, recoveryFileSuffix)
	if !ok {
		return false
	}
	stamp, sequence, ok := strings.Cut(rest, "-")
	return ok && isStamp(stamp) && len(sequence) >= 2 && isDigits(sequence)
}

// isBackupName reports whether target is a name backupBytes publishes,
// "<registry>.v<N>.<stamp>.bak" with an optional ".<i>" collision suffix, or the
// migration report writeMigrationEvidence publishes next to one.
func (s *Store) isBackupName(target string) bool {
	rest, ok := strings.CutPrefix(target, filepath.Base(s.path)+".v")
	if !ok {
		return false
	}
	version, rest, ok := strings.Cut(rest, ".")
	if !ok || !isDigits(version) {
		return false
	}
	stamp, rest, ok := strings.Cut(rest, ".")
	if !ok || !isStamp(stamp) {
		return false
	}
	rest, ok = strings.CutPrefix(rest, "bak")
	if !ok {
		return false
	}
	rest = strings.TrimSuffix(rest, migrationReportSuffix)
	if rest == "" {
		return true
	}
	collision, ok := strings.CutPrefix(rest, ".")
	return ok && isDigits(collision)
}

// stagedTempTarget returns the published name a staged file was created for,
// when name has the exact shape os.CreateTemp gives the pattern
// "."+target+".tmp-*": a non-empty target and a decimal random part.
func stagedTempTarget(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, ".")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, stagedTempInfix)
	if i <= 0 || !isDigits(rest[i+len(stagedTempInfix):]) {
		return "", false
	}
	return rest[:i], true
}

func isStamp(value string) bool {
	_, err := time.Parse(recoveryStampLayout, value)
	return err == nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
