package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
func (l controllerEventLog) key(target tmuxTransport) string {
	sum := sha256.Sum256([]byte(target.Flag() + "\x00" + target.Value))
	return hex.EncodeToString(sum[:8])
}

func (l controllerEventLog) eventsDir(target tmuxTransport) string {
	return filepath.Join(l.dir, l.key(target)+".events")
}

func (l controllerEventLog) lockPath(target tmuxTransport) string {
	return filepath.Join(l.dir, l.key(target)+".lock")
}

// mark records one dirty event for target.
//
// The body carries only the exact hook envelope. It never claims resulting
// machine state; the lease holder re-observes the exact socket before acting.
// Persisting these values prevents a pane-exited/window-unlinked burst from
// widening away the two causal halves before they reach the authority kernel.
type controllerEventRecord struct {
	Reason     controllerTriggerReason `json:"reason"`
	Session    string                  `json:"session,omitempty"`
	HookPane   string                  `json:"hookPane,omitempty"`
	HookWindow string                  `json:"hookWindow,omitempty"`
	// Retry is controller transport state. It bounds both causal unlink replay
	// and a worker's replay after a convergence error; it never becomes
	// Registry teardown evidence.
	Retry int `json:"retry,omitempty"`
}

// controllerPersistedEvent is one immutable event-file snapshot. The nonce is
// queue identity and body is its compare-before-ack guard: exhausted replay
// reads without acknowledgement, then removes only the exact record whose
// current-authority pass succeeded.
type controllerPersistedEvent struct {
	name    string
	body    []byte
	trigger controllerTrigger
}

func exhaustedCleanExitTrigger(trigger controllerTrigger) bool {
	return trigger.reason == controllerTriggerPaneExited &&
		trigger.retry == controllerTriggerMaxRetries &&
		validTmuxHookHandle(strings.TrimSpace(trigger.hookPane), '%') &&
		strings.TrimSpace(trigger.session) == "" &&
		strings.TrimSpace(trigger.hookWindow) == ""
}

func (l controllerEventLog) mark(trigger controllerTrigger) (string, error) {
	if strings.TrimSpace(l.dir) == "" {
		return "", errors.New("controller event log has no state directory")
	}
	target := trigger.target
	if target.Flag() == "" || target.Value == "" {
		return "", errors.New("controller event log requires an explicit tmux target")
	}
	dir := l.eventsDir(target)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("create controller event dir: %w", err)
	}
	nonce := l.newNonce
	if nonce == nil {
		nonce = newControllerEventNonce
	}
	name, err := nonce()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	body, err := json.Marshal(controllerEventRecord{
		Reason: trigger.reason, Session: trigger.session, HookPane: trigger.hookPane,
		HookWindow: trigger.hookWindow, Retry: trigger.retry,
	})
	if err != nil {
		return "", fmt.Errorf("encode controller event: %w", err)
	}
	body = append(body, '\n')
	// Build the record outside the queue directory, then publish it with one
	// rename. A lease holder may drain while producers are marking; exposing the
	// final nonce before WriteFile completes would let it observe empty or
	// partial JSON and turn an ordinary hook burst into a decode failure.
	tmp, err := os.CreateTemp(l.dir, "."+l.key(target)+".event-*")
	if err != nil {
		return "", fmt.Errorf("create controller event: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("secure controller event: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write controller event: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close controller event: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("publish controller event: %w", err)
	}
	cleanup = false
	return name, nil
}

// drain removes every recorded event for target and returns how many there were.
//
// It is called by the lease holder only, and it is called *before* the pass it
// answers rather than after. Acknowledging first is what makes an event that
// arrives during a pass count as a new one: the alternative acknowledges work
// the pass could not have seen, which is precisely the lost-wakeup this loop
// exists to avoid.
// drain reserves every exhausted record except the exact names minted by this
// worker while advancing its own bounded retry. A terminal event published by
// another worker at any point therefore remains startup-only evidence, while
// the current producer still receives its established retry-3 final attempt.
func (l controllerEventLog) drain(target tmuxTransport, allowedExhausted map[string]bool) ([]controllerTrigger, error) {
	events, err := l.read(target)
	if err != nil {
		return nil, err
	}
	drained := make([]controllerTrigger, 0, len(events))
	for _, event := range events {
		// Retry exhaustion is durable upgrade evidence. Ordinary hooks and the
		// generic config-apply pass must not accidentally acknowledge it; the
		// verified startup replay owns its separate read/evaluate/ack boundary.
		if exhaustedCleanExitTrigger(event.trigger) && !allowedExhausted[event.name] {
			continue
		}
		if err := l.ack(target, event); err != nil {
			return drained, err
		}
		drained = append(drained, event.trigger)
	}
	sortControllerTriggers(drained)
	return drained, nil
}

// exhausted returns terminal exact pane-exited events without changing queue
// bytes. It is called only while the target lease is held by verified startup.
func (l controllerEventLog) exhausted(target tmuxTransport) ([]controllerPersistedEvent, error) {
	events, err := l.read(target)
	if err != nil {
		return nil, err
	}
	out := events[:0]
	for _, event := range events {
		if exhaustedCleanExitTrigger(event.trigger) {
			out = append(out, event)
		}
	}
	return out, nil
}

func (l controllerEventLog) read(target tmuxTransport) ([]controllerPersistedEvent, error) {
	dir := l.eventsDir(target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read controller events: %w", err)
	}
	events := make([]controllerPersistedEvent, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		// #nosec G304 -- path is one entry returned by ReadDir for the private,
		// target-keyed controller queue; no caller-supplied path is accepted.
		body, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return events, fmt.Errorf("read controller event: %w", readErr)
		}
		if readErr != nil {
			continue
		}
		var record controllerEventRecord
		if err := json.Unmarshal(body, &record); err != nil {
			return events, fmt.Errorf("decode controller event: %w", err)
		}
		if !slices.Contains(controllerTriggerReasons(), record.Reason) {
			return events, fmt.Errorf("decode controller event: unknown reason %q", record.Reason)
		}
		events = append(events, controllerPersistedEvent{
			name: entry.Name(), body: body,
			trigger: controllerTrigger{
				reason: record.Reason, target: target, session: record.Session,
				hookPane: record.HookPane, hookWindow: record.HookWindow, retry: record.Retry,
			},
		})
	}
	slices.SortFunc(events, func(left, right controllerPersistedEvent) int {
		if priority := controllerEventPriority(left.trigger.reason) - controllerEventPriority(right.trigger.reason); priority != 0 {
			return priority
		}
		return strings.Compare(left.name, right.name)
	})
	return events, nil
}

func (l controllerEventLog) ack(target tmuxTransport, event controllerPersistedEvent) error {
	path := filepath.Join(l.eventsDir(target), event.name)
	// #nosec G304 -- event.name came from ReadDir for this private target queue.
	current, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("re-read controller event before acknowledge: %w", err)
	}
	if !slices.Equal(current, event.body) {
		return errors.New("controller event changed before acknowledge")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("acknowledge controller event: %w", err)
	}
	return nil
}

func controllerEventPriority(reason controllerTriggerReason) int {
	switch reason {
	case controllerTriggerPaneExited:
		return 0
	case controllerTriggerWindowUnlinked:
		return 1
	default:
		return 2
	}
}

// pending reports whether any event is currently recorded for target. It is a
// read: it creates nothing and removes nothing.
// pending excludes exhausted clean-exit evidence when includeExhausted is
// false. Without this distinction one retained terminal event would keep an
// otherwise converged hook worker spinning until its pass bound.
func (l controllerEventLog) pending(target tmuxTransport, includeExhausted bool) (bool, error) {
	events, err := l.read(target)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if includeExhausted || !exhaustedCleanExitTrigger(event.trigger) {
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
func (l controllerEventLog) acquire(target tmuxTransport) (func(), bool, error) {
	if strings.TrimSpace(l.dir) == "" {
		return nil, false, errors.New("controller event log has no state directory")
	}
	if err := localstate.EnsurePrivateDir(l.dir); err != nil {
		return nil, false, fmt.Errorf("create controller lease dir: %w", err)
	}
	return acquireControllerLease(l.lockPath(target))
}
