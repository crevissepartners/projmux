// Package metadata persists the Projmux resource registry and mirrors live
// resource identity into tmux options.
//
// The pure resource model, validation, naming, and transaction rules live in
// internal/core/metadata; this package owns the file format, the cross-process
// lock, schema migration, and the tmux transport adapter.
//
// Registry file: <StateDir>/projmux/metadata/registry.json, written 0600 below
// a 0700 directory. Field spelling follows the resource-model contract
// (camelCase) rather than the snake_case used by the older projmux state
// files; see docs/architecture.md.
package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	stateDirName   = "metadata"
	registryFile   = "registry.json"
	lockFileSuffix = ".lock"
)

var (
	defaultLockMaxAttempts = 200
	defaultLockBaseDelay   = 2 * time.Millisecond
	defaultLockMaxDelay    = 50 * time.Millisecond
	defaultLockStaleAfter  = 30 * time.Second
)

// ErrMalformedRegistry marks registry JSON that cannot be decoded. Like a
// newer schema version it is handled fail-closed: the store refuses to read it
// as valid and performs no write, because quarantining or resetting the file
// would destroy state the operator still owns.
var ErrMalformedRegistry = errors.New("malformed resource registry JSON")

// storeHooks are failure-injection seams used by the atomicity tests. They are
// nil in production.
type storeHooks struct {
	afterBackup    func() error
	afterTempWrite func() error
	beforeRename   func() error
}

// Store persists one resource registry file behind a cross-process lock.
type Store struct {
	path     string
	lockPath string
	clock    func() time.Time
	rngMu    sync.Mutex
	rng      *rand.Rand
	hooks    storeHooks
	// migrations overrides the schema migration set. It is nil in production,
	// which means the (currently empty) production set; the migration
	// atomicity tests register a step here so the write sequence can be
	// exercised without shipping a migration.
	migrations coremetadata.MigrationSet
}

// NewStore builds a registry store for an explicit file path. Tests pass a
// temp path so they never touch the real user state directory.
func NewStore(path string) *Store {
	return &Store{
		path:     path,
		lockPath: path + lockFileSuffix,
		clock:    time.Now,
		// #nosec G404 -- lock retry jitter is scheduling noise, not a secret;
		// this matches the notify and recent-window store lock jitter.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// NewDefaultStore builds the store at the standard XDG state location.
func NewDefaultStore(paths config.Paths) *Store {
	return NewStore(PathFor(paths.StateDir))
}

// PathFor returns <stateDir>/metadata/registry.json.
func PathFor(stateDir string) string {
	return filepath.Join(stateDir, stateDirName, registryFile)
}

// Path returns the registry file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// SetClock overrides the timestamp source used for backups and stale-lock
// breaking.
func (s *Store) SetClock(clock func() time.Time) {
	if s != nil && clock != nil {
		s.clock = clock
	}
}

// DefaultMutator returns the production Mutator: wall clock, crypto/rand uids,
// and a real directory probe.
func DefaultMutator() coremetadata.Mutator {
	return coremetadata.Mutator{
		Now:       time.Now,
		NewUID:    coremetadata.NewUID,
		DirExists: DirExists,
	}
}

// DirExists reports whether path is an existing directory. Symlinks are
// followed so a project root reachable through a link still resolves.
func DirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if errors.Is(err, os.ErrPermission) {
			return false, nil
		}
		return false, fmt.Errorf("metadata: stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}

// Load reads the registry without writing anything.
//
// A newer-than-supported schemaVersion and malformed JSON both fail closed: an
// error is returned and the file is left byte-identical. A known older schema
// is migrated in memory only; the durable migration happens on the next write.
func (s *Store) Load() (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	var out coremetadata.Registry
	err := s.withLock(func() error {
		registry, _, _, err := s.read()
		if err != nil {
			return err
		}
		out = registry
		return nil
	})
	if err != nil {
		return coremetadata.Registry{}, err
	}
	return out, nil
}

// LoadReadOnly reads the registry without creating anything at all.
//
// Load takes the cross-process lock, and acquiring that lock creates the
// registry directory and a lock file. A read-only route must not do that: it
// would materialize <state>/projmux/metadata/ for an operator who has never
// registered a resource. So an absent registry file short-circuits to a fresh
// empty registry before any directory is touched.
//
// When the file does exist the normal locked read runs, whose directory already
// exists. Skipping the lock entirely would also be safe for readers, because
// every write lands through an atomic rename, but taking it keeps the read
// consistent with a concurrent migration.
func (s *Store) LoadReadOnly() (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return coremetadata.NewRegistry(), nil
		}
		return coremetadata.Registry{}, fmt.Errorf("metadata: stat registry %s: %w", s.path, err)
	}
	return s.Load()
}

// Update runs fn against a private copy of the registry under the store lock
// and writes the result only when fn and validation both succeed.
//
// A failing fn performs no write at all, so a pre-create failure leaves zero
// mutations on disk. When the on-disk envelope was an older schema version the
// write is preceded by a backup of the original file.
func (s *Store) Update(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.Registry{}, errors.New("metadata: nil registry store")
	}
	var out coremetadata.Registry
	err := s.withLock(func() error {
		registry, onDiskVersion, existed, err := s.read()
		if err != nil {
			return err
		}
		working := registry.Clone()
		if err := fn(&working); err != nil {
			return err
		}
		working = working.Normalize()
		if err := working.Validate(); err != nil {
			return err
		}
		migrating := existed && onDiskVersion != coremetadata.SchemaVersion
		if err := s.write(working, migrating); err != nil {
			return err
		}
		out = working
		return nil
	})
	if err != nil {
		return coremetadata.Registry{}, err
	}
	return out, nil
}

// MigrationResult reports what Migrate did.
type MigrationResult struct {
	// FromVersion is the schemaVersion found on disk.
	FromVersion int
	// Migrated is true when a durable migration write happened.
	Migrated bool
	// BackupPath is the copy of the pre-migration file, when one was taken.
	BackupPath string
}

// Migrate performs the durable schema migration: backup, temp write, validate,
// atomic replace. It is a no-op when the file is missing or already current,
// and it fails closed without writing when the envelope is newer than this
// build supports.
func (s *Store) Migrate() (MigrationResult, error) {
	if s == nil {
		return MigrationResult{}, errors.New("metadata: nil registry store")
	}
	var out MigrationResult
	err := s.withLock(func() error {
		registry, onDiskVersion, existed, err := s.read()
		if err != nil {
			return err
		}
		out.FromVersion = onDiskVersion
		if !existed || onDiskVersion == coremetadata.SchemaVersion {
			return nil
		}
		if err := registry.Validate(); err != nil {
			return err
		}
		backup, err := s.backup(onDiskVersion)
		if err != nil {
			return err
		}
		out.BackupPath = backup
		if err := s.writeAfterBackup(registry); err != nil {
			return err
		}
		out.Migrated = true
		return nil
	})
	if err != nil {
		return MigrationResult{}, err
	}
	return out, nil
}

// read loads, classifies, and (in memory) migrates the registry file. It
// returns the registry, the schemaVersion found on disk, and whether the file
// existed with content.
func (s *Store) read() (coremetadata.Registry, int, bool, error) {
	localstate.RepairPrivateFile(s.path)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return coremetadata.NewRegistry(), coremetadata.SchemaVersion, false, nil
		}
		return coremetadata.Registry{}, 0, false, fmt.Errorf("metadata: read registry %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return coremetadata.NewRegistry(), coremetadata.SchemaVersion, false, nil
	}

	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return coremetadata.Registry{}, 0, true, fmt.Errorf("%w %s: %w", ErrMalformedRegistry, s.path, err)
	}
	// Classify before decoding the body so an unknown envelope is refused
	// without this build reinterpreting fields it does not understand. An
	// absent schemaVersion decodes as 0, which is unknown rather than
	// pre-release, so it is refused here too.
	if _, err := coremetadata.ClassifySchemaVersionWith(s.migrations, envelope.SchemaVersion); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, fmt.Errorf("metadata: %s: %w", s.path, err)
	}

	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, fmt.Errorf("%w %s: %w", ErrMalformedRegistry, s.path, err)
	}
	migrated, _, err := coremetadata.MigrateRegistryWith(s.migrations, registry)
	if err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, true, fmt.Errorf("metadata: %s: %w", s.path, err)
	}
	return migrated, envelope.SchemaVersion, true, nil
}

func (s *Store) write(registry coremetadata.Registry, migrating bool) error {
	if migrating {
		if _, err := s.backup(0); err != nil {
			return err
		}
	}
	return s.writeAfterBackup(registry)
}

// writeAfterBackup performs the temp write -> validate -> atomic replace
// sequence. An interruption at any point leaves the original file intact,
// because the destination is only ever touched by the final rename.
func (s *Store) writeAfterBackup(registry coremetadata.Registry) error {
	if s.hooks.afterBackup != nil {
		if err := s.hooks.afterBackup(); err != nil {
			return err
		}
	}

	dir := filepath.Dir(s.path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("metadata: create registry dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(registry.Normalize(), "", "  ")
	if err != nil {
		return fmt.Errorf("metadata: encode registry: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("metadata: create temp registry: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("metadata: write temp registry: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("metadata: sync temp registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("metadata: close temp registry: %w", err)
	}
	if s.hooks.afterTempWrite != nil {
		if err := s.hooks.afterTempWrite(); err != nil {
			return err
		}
	}
	if err := validateWrittenFile(tmpName); err != nil {
		return err
	}
	if s.hooks.beforeRename != nil {
		if err := s.hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("metadata: rename temp registry: %w", err)
	}
	cleanup = false
	localstate.RepairPrivateFile(s.path)
	return nil
}

// validateWrittenFile re-reads the staged file so only a decodable, valid
// registry can replace the live one.
func validateWrittenFile(path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the store's own temp file
	if err != nil {
		return fmt.Errorf("metadata: reread staged registry: %w", err)
	}
	var staged coremetadata.Registry
	if err := json.Unmarshal(data, &staged); err != nil {
		return fmt.Errorf("%w %s: %w", ErrMalformedRegistry, path, err)
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("metadata: validate staged registry: %w", err)
	}
	return nil
}

// backup copies the current registry file next to itself before a migration
// write. Downgrade writes are unsupported, so the backup is the only way back
// to the pre-migration bytes.
func (s *Store) backup(fromVersion int) (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("metadata: read registry for backup %s: %w", s.path, err)
	}
	if fromVersion == 0 {
		var envelope struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		_ = json.Unmarshal(data, &envelope)
		fromVersion = envelope.SchemaVersion
	}
	stamp := s.clock().UTC().Format("20060102T150405Z")
	base := fmt.Sprintf("%s.v%d.%s.bak", s.path, fromVersion, stamp)
	path := base
	for i := 1; ; i++ {
		if _, err := os.Stat(path); err == nil {
			path = fmt.Sprintf("%s.%d", base, i)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("metadata: stat registry backup %s: %w", path, err)
		}
		break
	}
	// #nosec G703 -- path is the store's own registry path plus a generated
	// version/timestamp suffix; no caller-supplied value reaches it.
	if err := os.WriteFile(path, data, localstate.PrivateFileMode); err != nil {
		return "", fmt.Errorf("metadata: write registry backup %s: %w", path, err)
	}
	return path, nil
}

func (s *Store) withLock(fn func() error) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(s.lockPath)); err != nil {
		return fmt.Errorf("metadata: create lock dir: %w", err)
	}
	if err := s.acquireLock(); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(s.lockPath)
	}()
	return fn()
}

func (s *Store) acquireLock() error {
	delay := defaultLockBaseDelay
	for range defaultLockMaxAttempts {
		f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, localstate.PrivateFileMode)
		if err == nil {
			_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
			_ = f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("metadata: acquire lock: %w", err)
		}
		if s.tryBreakStaleLock() {
			continue
		}
		time.Sleep(delay + s.lockJitter())
		if delay < defaultLockMaxDelay {
			delay *= 2
			if delay > defaultLockMaxDelay {
				delay = defaultLockMaxDelay
			}
		}
	}
	return fmt.Errorf("metadata: acquire lock: exhausted %d attempts on %s", defaultLockMaxAttempts, s.lockPath)
}

func (s *Store) lockJitter() time.Duration {
	s.rngMu.Lock()
	defer s.rngMu.Unlock()
	return time.Duration(s.rng.Int63n(int64(defaultLockBaseDelay) + 1))
}

func (s *Store) tryBreakStaleLock() bool {
	info, err := os.Stat(s.lockPath)
	if err != nil {
		return false
	}
	if s.clock().Sub(info.ModTime()) < defaultLockStaleAfter {
		return false
	}
	return os.Remove(s.lockPath) == nil
}
