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
//
// The registry is the source of truth for managed identity and desired
// topology, so the file format is a durable envelope rather than a bare JSON
// document. Beside registry.json the store keeps an initialized marker, which
// separates a legitimate first use from a lost registry, and a bounded set of
// recovery copies, which hold the last verified bytes replaced by a semantic
// write. Reading never creates any of them.
package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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
	// markerFileName records that at least one registry write has completed.
	// It is the boundary between "this operator has never registered a
	// resource" and "the registry that existed is gone".
	markerFileName = "registry.initialized"
	// recoveryDirName holds the bounded rolling copies of the bytes replaced
	// by semantic writes. Reading a copy back is deliberately not part of this
	// store: selecting and restoring a source is an operator decision.
	recoveryDirName    = "recovery"
	recoveryFilePrefix = "registry-"
	recoveryFileSuffix = ".json"
	// recoveryStampLayout keeps copy names sortable: lexicographic order over
	// stamp plus same-second sequence is chronological order.
	recoveryStampLayout = "20060102T150405Z"
	// recoverySequenceLimit bounds the same-second sequence suffix.
	recoverySequenceLimit = 100
)

var (
	defaultLockMaxAttempts = 200
	defaultLockBaseDelay   = 2 * time.Millisecond
	defaultLockMaxDelay    = 50 * time.Millisecond
	defaultLockStaleAfter  = 30 * time.Second
	// defaultRecoveryRetention is how many recovery copies survive a write.
	// The copies exist to undo the most recent damaging commits, not to be an
	// archive, so the bound is small and enforced on every semantic write.
	defaultRecoveryRetention = 5
)

// ErrMalformedRegistry marks registry JSON that cannot be decoded. Like a
// newer schema version it is handled fail-closed: the store refuses to read it
// as valid and performs no write, because quarantining or resetting the file
// would destroy state the operator still owns.
var ErrMalformedRegistry = errors.New("malformed resource registry JSON")

// ErrRegistryStateLost marks a registry that is missing or empty even though
// the initialized marker records a completed write. It is a different
// diagnostic from first use on purpose: silently answering an empty registry
// there would hide the loss of every managed uid, name reservation, and
// offline resource, and the next mutation would mint a fresh identity domain
// on top of it.
var ErrRegistryStateLost = errors.New("resource registry is missing after initialization")

// ErrRegistryPermission marks a registry file that exists but cannot be read.
// It stays distinct from missing, empty, malformed, and too-new so an operator
// is told to fix an access problem instead of to recover lost state.
var ErrRegistryPermission = errors.New("resource registry is not readable")

// storeHooks are failure-injection seams used by the atomicity tests. They are
// nil in production, and every one of them fires at a point where the live
// registry has not been touched yet, so a test can prove the prior bytes
// survive a failure at each step of the envelope.
type storeHooks struct {
	afterBackup       func() error
	afterTempWrite    func() error
	validateStaged    func(path string) error
	afterRecoveryCopy func() error
	afterMarker       func() error
	beforeRename      func() error
	syncFile          func(*os.File) error
	syncDir           func(string) error
}

// Store persists one resource registry file behind a cross-process lock.
type Store struct {
	path           string
	lockPath       string
	repairLockPath string
	markerPath     string
	recoveryDir    string
	// retention is the number of recovery copies kept after a semantic write.
	retention int
	clock     func() time.Time
	rngMu     sync.Mutex
	rng       *rand.Rand
	hooks     storeHooks
	// migrations overrides the schema migration set. It is nil in production,
	// which means the (currently empty) production set; the migration
	// atomicity tests register a step here so the write sequence can be
	// exercised without shipping a migration.
	migrations coremetadata.MigrationSet
}

// NewStore builds a registry store for an explicit file path. Tests pass a
// temp path so they never touch the real user state directory.
func NewStore(path string) *Store {
	dir := filepath.Dir(path)
	return &Store{
		path:           path,
		lockPath:       path + lockFileSuffix,
		repairLockPath: path + ".repair" + lockFileSuffix,
		markerPath:     filepath.Join(dir, markerFileName),
		recoveryDir:    filepath.Join(dir, recoveryDirName),
		retention:      defaultRecoveryRetention,
		clock:          time.Now,
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
// A registry that is gone or empty while the initialized marker exists fails
// with ErrRegistryStateLost rather than answering the empty first-use registry.
func (s *Store) Load() (coremetadata.Registry, error) {
	return s.load(true)
}

// load reads through the normal lock and optionally applies the whole-graph
// read guard. Ordinary low-level loads validate. The only caller that opts out
// is LoadDegradedReadOnly, the explicit app read seam used while repair is
// needed.
func (s *Store) load(validate bool) (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	var out coremetadata.Registry
	err := s.withLock(func() error {
		registry, _, _, err := s.read()
		if err != nil {
			return err
		}
		if validate {
			if err := registry.Validate(); err != nil {
				return err
			}
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
// exists. Call LoadSnapshot when even transient lock and permission-repair side
// effects are forbidden.
func (s *Store) LoadReadOnly() (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The short-circuit is also where first use and state loss are
			// separated, and both answers come from stats alone: an absent
			// registry must not create the directory just to learn that the
			// marker beside it is gone too.
			initialized, markerErr := s.initialized()
			if markerErr != nil {
				return coremetadata.Registry{}, markerErr
			}
			if initialized {
				return coremetadata.Registry{}, s.stateLossError("missing")
			}
			return coremetadata.NewRegistry(), nil
		}
		return coremetadata.Registry{}, fmt.Errorf("metadata: stat registry %s: %w", s.path, err)
	}
	return s.Load()
}

// LoadDegradedReadOnly is the explicit app-facing read gate for a Registry that
// decodes but fails whole-graph validation. It preserves the no-state-on-first-
// use behavior of LoadReadOnly, refuses malformed/unsupported/lost bytes, and
// opts out only from the final graph guard so read-only resource projections
// remain available while ordinary mutations are disabled.
func (s *Store) LoadDegradedReadOnly() (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			initialized, markerErr := s.initialized()
			if markerErr != nil {
				return coremetadata.Registry{}, markerErr
			}
			if initialized {
				return coremetadata.Registry{}, s.stateLossError("missing")
			}
			return coremetadata.NewRegistry(), nil
		}
		return coremetadata.Registry{}, fmt.Errorf("metadata: stat registry %s: %w", s.path, err)
	}
	return s.load(false)
}

// LoadSnapshot reads an atomic Registry snapshot without taking the lock or
// repairing permissions. Writers use atomic replace, so the result is either
// the complete prior or next envelope and the read performs zero writes.
func (s *Store) LoadSnapshot() (coremetadata.Registry, error) {
	if s == nil {
		return coremetadata.NewRegistry(), nil
	}
	registry, _, _, err := s.readWithoutRepair()
	return registry, err
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
	if err := s.refuseDegradedMutation(); err != nil {
		return coremetadata.Registry{}, err
	}
	var out coremetadata.Registry
	err := s.withLock(func() error {
		registry, onDiskVersion, existed, err := s.read()
		if err != nil {
			return s.mapDegradedMutationError(err)
		}
		if err := registry.Validate(); err != nil {
			return s.mapDegradedMutationError(err)
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

// UpdateConvergent is Update with a byte-write no-op for an unchanged
// registry. Runtime binding convergence uses it because the authoritative
// identity lives in the registry while the repaired copy lives in tmux: once
// the two agree, another lifecycle/apply pass must not replace registry.json
// merely because it took the mutation lock.
//
// The callback still runs under the normal cross-process lock and the result is
// still normalized and validated before comparison. Only the final temp-file
// and rename sequence is skipped, so callers do not gain a weaker transaction
// or schema path.
func (s *Store) UpdateConvergent(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
	if s == nil {
		return coremetadata.Registry{}, false, errors.New("metadata: nil registry store")
	}
	if err := s.refuseDegradedMutation(); err != nil {
		return coremetadata.Registry{}, false, err
	}
	var out coremetadata.Registry
	changed := false
	err := s.withLock(func() error {
		registry, onDiskVersion, existed, err := s.read()
		if err != nil {
			return s.mapDegradedMutationError(err)
		}
		if err := registry.Validate(); err != nil {
			return s.mapDegradedMutationError(err)
		}
		working := registry.Clone()
		if err := fn(&working); err != nil {
			return err
		}
		working = working.Normalize()
		if err := working.Validate(); err != nil {
			return err
		}
		if existed && onDiskVersion == coremetadata.SchemaVersion && reflect.DeepEqual(working, registry.Normalize()) {
			out = working
			return nil
		}
		migrating := existed && onDiskVersion != coremetadata.SchemaVersion
		if err := s.write(working, migrating); err != nil {
			return err
		}
		out = working
		changed = true
		return nil
	})
	if err != nil {
		return coremetadata.Registry{}, false, err
	}
	return out, changed, nil
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
	if err := s.refuseDegradedMutation(); err != nil {
		return MigrationResult{}, err
	}
	var out MigrationResult
	err := s.withLock(func() error {
		registry, onDiskVersion, existed, err := s.read()
		if err != nil {
			return s.mapDegradedMutationError(err)
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
		if err := s.writeAfterBackup(registry, false); err != nil {
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
	return s.readWithoutRepair()
}

func (s *Store) readWithoutRepair() (coremetadata.Registry, int, bool, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.absentRegistry("missing")
		}
		if errors.Is(err, fs.ErrPermission) {
			return coremetadata.Registry{}, 0, true, fmt.Errorf("metadata: read registry %s: %w: %w", s.path, ErrRegistryPermission, err)
		}
		return coremetadata.Registry{}, 0, false, fmt.Errorf("metadata: read registry %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s.absentRegistry("empty")
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

// absentRegistry answers the two ways registry.json can carry no content. Which
// answer is correct is not a property of the file: it depends on whether this
// state directory has ever held a committed registry. Without the marker the
// empty registry is the legitimate first use; with it, the content is gone and
// every caller -- reads and mutations alike -- must fail instead of quietly
// starting a second identity domain.
func (s *Store) absentRegistry(state string) (coremetadata.Registry, int, bool, error) {
	initialized, err := s.initialized()
	if err != nil {
		return coremetadata.Registry{}, 0, false, err
	}
	if initialized {
		return coremetadata.Registry{}, 0, false, s.stateLossError(state)
	}
	return coremetadata.NewRegistry(), coremetadata.SchemaVersion, false, nil
}

// initialized reports whether a registry write has ever completed in this state
// directory. It only stats, so it is safe on the zero-write read routes.
func (s *Store) initialized() (bool, error) {
	if _, err := os.Stat(s.markerPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return false, fmt.Errorf("metadata: stat initialized marker %s: %w: %w", s.markerPath, ErrRegistryPermission, err)
		}
		return false, fmt.Errorf("metadata: stat initialized marker %s: %w", s.markerPath, err)
	}
	return true, nil
}

// stateLossError names both halves of the evidence and the two ways out, so the
// message is actionable without this store choosing a recovery for the
// operator. Restoring a copy is deliberately a separate, reviewed decision.
func (s *Store) stateLossError(state string) error {
	return fmt.Errorf("metadata: %w: %s records a completed registry write but %s is %s; restore a verified copy from %s, or remove the marker to accept an empty registry",
		ErrRegistryStateLost, s.markerPath, s.path, state, s.recoveryDir)
}

// refuseDegradedMutation is the lock-free gate in front of every ordinary
// write. It keeps a damaged Registry from waiting behind (or entering) the
// normal mutation transaction and turns the content classification into one
// actionable recovery instruction.
func (s *Store) refuseDegradedMutation() error {
	inspection, err := s.InspectRecovery()
	if err != nil {
		return err
	}
	mode := inspection.DegradedMode()
	if !mode.Active {
		return nil
	}
	registry, _, _, readErr := s.readWithoutRepair()
	if readErr != nil {
		mode.Cause = readErr
	} else {
		mode.Cause = registry.Validate()
	}
	return mode.Error()
}

// mapDegradedMutationError closes the race between the lock-free gate and the
// locked read. If the Registry changed into a degraded state in that window,
// the mutation still returns the same actionable refusal instead of exposing a
// bare decode, schema, or graph-validation error.
func (s *Store) mapDegradedMutationError(fallback error) error {
	inspection, err := s.InspectRecovery()
	if err == nil {
		mode := inspection.DegradedMode()
		mode.Cause = fallback
		if degraded := mode.Error(); degraded != nil {
			return degraded
		}
	}
	return fallback
}

func (s *Store) write(registry coremetadata.Registry, migrating bool) error {
	if migrating {
		if _, err := s.backup(0); err != nil {
			return err
		}
	}
	// A migration already copied the pre-migration bytes to its own versioned
	// backup, so it does not also take a same-version recovery copy.
	return s.writeAfterBackup(registry, !migrating)
}

// writeAfterBackup performs the durable envelope sequence: stage -> fsync ->
// validate -> recovery copy -> initialized marker -> directory sync -> atomic
// replace -> directory sync.
//
// Every step before the rename is reversible, and every failure after the first
// reversible step undoes what this write created. The live registry is only ever
// touched by the final rename, so a failure or an interruption anywhere leaves
// the prior bytes byte-identical with no staged file left behind.
//
// The marker is published before the rename rather than after it so that its
// failure cannot leave a replaced registry behind. The cost is a crash window of
// one rename: a hard crash between the marker and the first registry rename
// leaves a marker with no registry, which reads as state loss instead of first
// use. That direction is deliberate -- it asks the operator instead of silently
// starting over -- and the message names the marker so it can be removed.
func (s *Store) writeAfterBackup(registry coremetadata.Registry, keepRecoveryCopy bool) error {
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
	if err := s.syncFile(tmp); err != nil {
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
	if err := s.validateStaged(tmpName); err != nil {
		return err
	}

	rollback := newRollback()
	if keepRecoveryCopy {
		copied, err := s.recoveryCopy()
		if err != nil {
			return rollback.undo(err)
		}
		if copied != "" {
			rollback.add(func() { _ = os.Remove(copied) })
		}
		if s.hooks.afterRecoveryCopy != nil {
			if err := s.hooks.afterRecoveryCopy(); err != nil {
				return rollback.undo(err)
			}
		}
	}
	created, err := s.ensureInitializedMarker()
	if err != nil {
		return rollback.undo(err)
	}
	if created {
		rollback.add(func() { _ = os.Remove(s.markerPath) })
	}
	if s.hooks.afterMarker != nil {
		if err := s.hooks.afterMarker(); err != nil {
			return rollback.undo(err)
		}
	}
	// Make the staged file, the marker, and the recovery copy durable directory
	// entries before the entry for the live registry is repointed at any of them.
	if err := s.syncDir(dir); err != nil {
		return rollback.undo(err)
	}
	if s.hooks.beforeRename != nil {
		if err := s.hooks.beforeRename(); err != nil {
			return rollback.undo(err)
		}
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return rollback.undo(fmt.Errorf("metadata: rename temp registry: %w", err))
	}
	cleanup = false
	// Past this point the new bytes are the registry. The remaining steps are
	// durability and hygiene: a failure must not be reported as a failed write,
	// and there is nothing to roll back to.
	_ = s.syncDir(dir)
	localstate.RepairPrivateFile(s.path)
	if keepRecoveryCopy {
		s.pruneRecoveryCopies()
	}
	return nil
}

// rollbackStack undoes the reversible steps of one write in reverse order.
type rollbackStack struct {
	steps []func()
}

func newRollback() *rollbackStack { return &rollbackStack{} }

func (r *rollbackStack) add(step func()) { r.steps = append(r.steps, step) }

// undo runs the recorded steps newest first and returns err unchanged, so a
// failing step reads as `return rollback.undo(err)` at the call site.
func (r *rollbackStack) undo(err error) error {
	for i := len(r.steps) - 1; i >= 0; i-- {
		r.steps[i]()
	}
	return err
}

// syncFile flushes a staged file. The seam exists so the failure matrix can
// exercise an fsync failure without an unwritable filesystem.
func (s *Store) syncFile(f *os.File) error {
	if s.hooks.syncFile != nil {
		return s.hooks.syncFile(f)
	}
	return f.Sync()
}

// syncDir makes directory entries durable so a crash cannot leave the rename
// unrecorded. Filesystems that do not support directory fsync -- DrvFs and
// friends, the same ones that reject the permission repair -- must not lose the
// ability to write state over it, so an unsupported, invalid, or refused sync is
// not an error. An injected failure is.
func (s *Store) syncDir(dir string) error {
	if s.hooks.syncDir != nil {
		return s.hooks.syncDir(dir)
	}
	f, err := os.Open(dir) // #nosec G304 -- dir is the store's own registry directory
	if err != nil {
		if ignorableSyncError(err) {
			return nil
		}
		return fmt.Errorf("metadata: open registry dir %s for sync: %w", dir, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		if ignorableSyncError(err) {
			return nil
		}
		return fmt.Errorf("metadata: sync registry dir %s: %w", dir, err)
	}
	return nil
}

func ignorableSyncError(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, fs.ErrInvalid) ||
		errors.Is(err, fs.ErrPermission)
}

// validateStaged re-reads the staged file so only a decodable, valid registry
// can replace the live one.
func (s *Store) validateStaged(path string) error {
	if s.hooks.validateStaged != nil {
		return s.hooks.validateStaged(path)
	}
	return validateWrittenFile(path)
}

// ensureInitializedMarker publishes the marker when it is absent and reports
// whether this write created it. The marker is written through the same
// stage-and-rename sequence as the registry, so it is either absent or complete.
func (s *Store) ensureInitializedMarker() (bool, error) {
	initialized, err := s.initialized()
	if err != nil {
		return false, err
	}
	if initialized {
		return false, nil
	}
	payload, err := json.Marshal(initializedMarker{
		SchemaVersion: coremetadata.SchemaVersion,
		InitializedAt: s.clock().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return false, fmt.Errorf("metadata: encode initialized marker: %w", err)
	}
	if err := s.writePrivateFile(s.markerPath, append(payload, '\n')); err != nil {
		return false, fmt.Errorf("metadata: write initialized marker %s: %w", s.markerPath, err)
	}
	return true, nil
}

// initializedMarker is the marker payload. It is diagnostic only: the file's
// existence carries the contract, and no reader depends on these fields.
type initializedMarker struct {
	SchemaVersion int    `json:"schemaVersion"`
	InitializedAt string `json:"initializedAt"`
}

// recoveryCopy preserves the bytes a semantic write is about to replace and
// returns the copy path, or "" when there is nothing worth preserving.
//
// Only verified bytes are kept. An absent or empty registry has nothing to
// recover, and bytes that do not decode into a valid registry are not a copy an
// operator could safely restore, so neither produces a copy -- and neither
// blocks the write, because refusing to replace an unusable file would strand
// the store.
func (s *Store) recoveryCopy() (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("metadata: read registry for recovery copy %s: %w: %w", s.path, ErrRegistryPermission, err)
		}
		return "", fmt.Errorf("metadata: read registry for recovery copy %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	if !decodesToValidRegistry(data) {
		return "", nil
	}
	if err := localstate.EnsurePrivateDir(s.recoveryDir); err != nil {
		return "", fmt.Errorf("metadata: create registry recovery dir %s: %w", s.recoveryDir, err)
	}
	path, err := s.nextRecoveryPath()
	if err != nil {
		return "", err
	}
	if err := s.writePrivateFile(path, data); err != nil {
		return "", fmt.Errorf("metadata: write registry recovery copy %s: %w", path, err)
	}
	return path, nil
}

func decodesToValidRegistry(data []byte) bool {
	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return false
	}
	return registry.Validate() == nil
}

// nextRecoveryPath allocates the next copy name so that lexicographic order over
// the recovery directory is creation order.
//
// The stamp has one-second resolution, so a sequence suffix separates copies
// taken inside one second. The sequence continues from the newest name present
// rather than from the first free slot: retention frees the earliest names, and
// reusing one would sort the newest copy to the front and make "the last
// verified bytes" mean the wrong file.
func (s *Store) nextRecoveryPath() (string, error) {
	stamp := s.clock().UTC().Format(recoveryStampLayout)
	seq := 0
	if names := s.recoveryCopyNames(); len(names) > 0 {
		newestStamp, newestSeq, ok := parseRecoveryName(names[len(names)-1])
		// The layout is fixed-width, so string order over stamps is time order.
		if ok && newestStamp >= stamp {
			stamp, seq = newestStamp, newestSeq+1
		}
	}
	for range recoverySequenceLimit * recoverySequenceLimit {
		if seq >= recoverySequenceLimit {
			// More copies inside one stamp than the sequence holds. Borrowing
			// the next second keeps the order total instead of failing a write
			// that has already been staged and validated.
			parsed, err := time.Parse(recoveryStampLayout, stamp)
			if err != nil {
				return "", fmt.Errorf("metadata: unparsable registry recovery stamp %q in %s", stamp, s.recoveryDir)
			}
			stamp, seq = parsed.Add(time.Second).Format(recoveryStampLayout), 0
		}
		path := filepath.Join(s.recoveryDir, fmt.Sprintf("%s%s-%02d%s", recoveryFilePrefix, stamp, seq, recoveryFileSuffix))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("metadata: stat registry recovery copy %s: %w", path, err)
		}
		seq++
	}
	return "", fmt.Errorf("metadata: no free registry recovery copy name near %s in %s", stamp, s.recoveryDir)
}

// parseRecoveryName splits a copy name into its stamp and sequence. The stamp
// layout carries no separator, so the single dash before the sequence is
// unambiguous.
func parseRecoveryName(name string) (string, int, bool) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, recoveryFilePrefix), recoveryFileSuffix)
	stamp, sequence, ok := strings.Cut(trimmed, "-")
	if !ok {
		return "", 0, false
	}
	seq, err := strconv.Atoi(sequence)
	if err != nil {
		return "", 0, false
	}
	return stamp, seq, true
}

// pruneRecoveryCopies keeps the newest retained copies and removes the rest.
// It runs after the replace succeeded, so it is hygiene rather than durability:
// a filesystem that refuses the removals must not turn a committed write into a
// reported failure.
func (s *Store) pruneRecoveryCopies() {
	retention := s.retention
	if retention <= 0 {
		retention = defaultRecoveryRetention
	}
	names := s.recoveryCopyNames()
	if len(names) <= retention {
		return
	}
	for _, name := range names[:len(names)-retention] {
		_ = os.Remove(filepath.Join(s.recoveryDir, name))
	}
}

// recoveryCopyNames lists the recovery copies oldest first. Entries that are not
// copies -- a staged temp file, an operator's own note -- are ignored so the
// retention bound never removes something this store did not create.
func (s *Store) recoveryCopyNames() []string {
	entries, err := os.ReadDir(s.recoveryDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, recoveryFilePrefix) || !strings.HasSuffix(name, recoveryFileSuffix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// writePrivateFile publishes data at path through a staged temp file, an fsync,
// an atomic rename, and a directory sync. os.CreateTemp already creates the
// staged file at 0600, and the containing directory is created 0700 by
// EnsurePrivateDir, so the published file inherits both.
func (s *Store) writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
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
		return fmt.Errorf("write temp: %w", err)
	}
	if err := s.syncFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	cleanup = false
	_ = s.syncDir(dir)
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

// withRecoveryLock serializes explicit recovery operations without sharing the
// ordinary registry write lock. That separation is what keeps a stale or
// blocked normal writer from disabling the only route able to replace an
// invalid Registry. Recovery still has its own lock and source/current/staged
// verification; it is a distinct transaction, not a validation bypass.
func (s *Store) withRecoveryLock(fn func() error) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(s.repairLockPath)); err != nil {
		return fmt.Errorf("metadata: create recovery lock dir: %w", err)
	}
	if err := s.acquireRecoveryLock(); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(s.repairLockPath)
	}()
	return fn()
}

func (s *Store) acquireRecoveryLock() error {
	delay := defaultLockBaseDelay
	for range defaultLockMaxAttempts {
		f, err := os.OpenFile(s.repairLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, localstate.PrivateFileMode)
		if err == nil {
			_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
			_ = f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("metadata: acquire recovery lock: %w", err)
		}
		if s.tryBreakStaleRecoveryLock() {
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
	return fmt.Errorf("metadata: acquire recovery lock: exhausted %d attempts on %s", defaultLockMaxAttempts, s.repairLockPath)
}

func (s *Store) tryBreakStaleRecoveryLock() bool {
	info, err := os.Stat(s.repairLockPath)
	if err != nil {
		return false
	}
	if s.clock().Sub(info.ModTime()) < defaultLockStaleAfter {
		return false
	}
	return os.Remove(s.repairLockPath) == nil
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
