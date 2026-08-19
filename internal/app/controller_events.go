package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// The dirty-event log: what a lifecycle producer is allowed to say, and how a
// burst of producers becomes one convergence.
//
// A tmux hook cannot converge anything itself. It fires once per pane exit in
// every session and once per window and split creation, it has no idea what the
// registry holds, and on `after-kill-pane` it cannot even name the pane that
// died. So the only honest statement it can make is "something on this exact
// server may have changed" -- a dirty event. Everything else is re-derived by a
// worker against a fresh observation of that same server, which is what makes
// the event advisory: a stale event, a duplicate event, and an event for an
// object that has since come back all converge on the same state as no event at
// all.
//
// Two files per exact server carry the whole protocol:
//
//   - `<key>.events/<nonce>` is one dirty event. Creating a file is the mark and
//     removing it is the acknowledgement, so marking never needs a lock: two
//     producers cannot collide on distinct nonces, and no counter can be lost to
//     a read-modify-write race.
//   - `<key>.lock` is the at-most-one-worker lease, held as an advisory whole-file
//     lock for exactly as long as the worker process lives. It is not a
//     timestamped lockfile on purpose. The registry's own `.lock` is a
//     create-exclusive file with a staleness heuristic because it must interop
//     with older binaries; this lease has one writer generation and can therefore
//     use the primitive the kernel releases on process death, so a crashed worker
//     leaves no lease behind and no successor has to guess whether a holder is
//     alive.
//
// The key is derived from the exact transport, so two servers -- the app-owned
// socket and a standalone one, or two `-L` sockets in a test -- never share a
// lease or an event queue. Sibling isolation is a property of the path, not of a
// check somebody has to remember to write.

// controllerEventLog is the on-disk dirty-event log and worker lease for the
// lifecycle triggers.
type controllerEventLog struct {
	// dir is <state>/projmux/controller. It is created on first mark, never on a
	// read: a producer that has nothing to say leaves no directory behind.
	dir string
	// newNonce mints one event file name. It is injectable so a test can state
	// the sequence instead of matching random hex.
	newNonce func() (string, error)
}

// controllerEventsDirName is the state subdirectory the log owns.
const controllerEventsDirName = "controller"

// newControllerEventLog resolves the log against the projmux state directory --
// the same one the registry itself lives in, so an isolated smoke that redirected
// `XDG_STATE_HOME` gets the events, the lease, and the registry in one place.
//
// The home and env readers are supplied rather than read from the process,
// because the command that owns them may have been given isolated ones. A nil
// reader falls back to the process environment, which is what every production
// caller wants.
func newControllerEventLog(homeDir func() (string, error), lookupEnv func(string) string) (controllerEventLog, error) {
	if homeDir == nil && lookupEnv == nil {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return controllerEventLog{}, fmt.Errorf("resolve projmux state paths: %w", err)
		}
		return controllerEventLog{dir: filepath.Join(paths.StateDir, controllerEventsDirName)}, nil
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		return controllerEventLog{}, fmt.Errorf("resolve user home: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    home,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return controllerEventLog{}, fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return controllerEventLog{dir: filepath.Join(paths.StateDir, controllerEventsDirName)}, nil
}

func newControllerEventNonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read controller event id entropy: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// key is the filename stem of one exact tmux server.
//
// It hashes the flag and the value together. `-L projmux` and `-S
// /tmp/x/projmux` may well be the same server, and treating them as one key
// would require resolving the socket path from a name -- a read this log must
// not perform, because the log is consulted before any tmux call. Two spellings
// of one server therefore get two leases, which costs one redundant convergence
// pass and never loses one.
func (l controllerEventLog) key(target explicitTmuxTarget) string {
	sum := sha256.Sum256([]byte(target.flag + "\x00" + target.value))
	return hex.EncodeToString(sum[:8])
}

func (l controllerEventLog) eventsDir(target explicitTmuxTarget) string {
	return filepath.Join(l.dir, l.key(target)+".events")
}

func (l controllerEventLog) lockPath(target explicitTmuxTarget) string {
	return filepath.Join(l.dir, l.key(target)+".lock")
}

// mark records one dirty event for target.
//
// The body is diagnostic only. Nothing reads it back to decide anything, which
// is deliberate: the moment a worker trusted an event's contents it would be
// acting on a claim about a machine state that has already moved on.
func (l controllerEventLog) mark(target explicitTmuxTarget, reason controllerTriggerReason) error {
	if strings.TrimSpace(l.dir) == "" {
		return errors.New("controller event log has no state directory")
	}
	if target.flag == "" || target.value == "" {
		return errors.New("controller event log requires an explicit tmux target")
	}
	dir := l.eventsDir(target)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("create controller event dir: %w", err)
	}
	nonce := l.newNonce
	if nonce == nil {
		nonce = newControllerEventNonce
	}
	name, err := nonce()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	body := string(reason) + " " + target.label() + "\n"
	// #nosec G306 -- PrivateFileMode is 0600; the event body is projmux's own
	// reason label and the exact target the caller already routed to.
	if err := os.WriteFile(path, []byte(body), localstate.PrivateFileMode); err != nil {
		return fmt.Errorf("record controller event: %w", err)
	}
	return nil
}

// drain removes every recorded event for target and returns how many there were.
//
// It is called by the lease holder only, and it is called *before* the pass it
// answers rather than after. Acknowledging first is what makes an event that
// arrives during a pass count as a new one: the alternative acknowledges work
// the pass could not have seen, which is precisely the lost-wakeup this loop
// exists to avoid.
func (l controllerEventLog) drain(target explicitTmuxTarget) (int, error) {
	dir := l.eventsDir(target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read controller events: %w", err)
	}
	drained := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return drained, fmt.Errorf("acknowledge controller event: %w", err)
		}
		drained++
	}
	return drained, nil
}

// pending reports whether any event is currently recorded for target. It is a
// read: it creates nothing and removes nothing.
func (l controllerEventLog) pending(target explicitTmuxTarget) (bool, error) {
	entries, err := os.ReadDir(l.eventsDir(target))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read controller events: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// acquire takes the at-most-one-worker lease for target.
//
// A false ok is the ordinary outcome under a burst and not an error: it means
// another worker holds the lease, and that worker will drain the event this
// caller just marked. The caller's correct response is to exit, which is what
// makes the burst cost one convergence instead of one per event.
func (l controllerEventLog) acquire(target explicitTmuxTarget) (func(), bool, error) {
	if strings.TrimSpace(l.dir) == "" {
		return nil, false, errors.New("controller event log has no state directory")
	}
	if err := localstate.EnsurePrivateDir(l.dir); err != nil {
		return nil, false, fmt.Errorf("create controller lease dir: %w", err)
	}
	return acquireControllerLease(l.lockPath(target))
}
