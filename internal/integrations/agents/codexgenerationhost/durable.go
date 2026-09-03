package codexgenerationhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const durableLaunchIntentVersion = 1

type durableLaunchPhase string

const (
	durableLaunchIntended durableLaunchPhase = "intended"
	durableLaunchRunning  durableLaunchPhase = "running"
)

// DurableLaunchIntent is written before any candidate process exists. The
// operation-owned supervisor inherits the intent guard lock, writes its own
// exact session leader identity, and keeps the lock for the full candidate
// lifetime. A coordinator death between launch and proof publication can
// therefore be distinguished from "no process was launched" without scanning
// processes or adopting a foreign socket.
type DurableLaunchIntent struct {
	Version           int                `json:"version"`
	OperationRef      string             `json:"operationRef"`
	Endpoint          EndpointIdentity   `json:"endpoint"`
	EndpointRuntimeID string             `json:"endpointRuntimeID"`
	ExecutablePath    string             `json:"executablePath"`
	SocketPath        string             `json:"socketPath"`
	SocketIdentity    *FileIdentity      `json:"socketIdentity,omitempty"`
	Phase             durableLaunchPhase `json:"phase"`
	PID               int                `json:"pid,omitempty"`
	ProcessGroupID    int                `json:"processGroupID,omitempty"`
}

func (intent DurableLaunchIntent) valid() bool {
	return intent.Version == durableLaunchIntentVersion && validLaunchToken(intent.OperationRef) &&
		intent.Endpoint.Valid() && validLaunchToken(intent.EndpointRuntimeID) &&
		absoluteNonRoot(intent.ExecutablePath) && absoluteNonRoot(intent.SocketPath) &&
		(intent.Phase == durableLaunchIntended || intent.Phase == durableLaunchRunning) &&
		((intent.Phase == durableLaunchIntended && intent.PID == 0 && intent.ProcessGroupID == 0) ||
			(intent.Phase == durableLaunchRunning && intent.PID > 0 && intent.ProcessGroupID == intent.PID))
}

func validLaunchToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func durableIntentPath(cfg PrivateGenerationConfig) string {
	return filepath.Join(cfg.PrivateRoot, ".projmux-launch-"+cfg.Endpoint.EndpointGenerationID+".json")
}

func durableGuardPath(cfg PrivateGenerationConfig) string { return durableIntentPath(cfg) + ".guard" }

// PrepareDurableGeneration starts or recovers exactly one operation-owned
// candidate. The intent is durable before launch; the inherited guard proves a
// launch is still in flight; the running receipt is written by the supervisor
// itself before it starts the app-server child. afterLaunch is a crash-test
// boundary: an error leaves the owned supervisor running so a later call must
// recover and reuse it rather than start a second candidate.
func PrepareDurableGeneration(
	ctx context.Context,
	cfg PrivateGenerationConfig,
	operationRef string,
	authorizeLaunch func(func() error) error,
	afterLaunch func() error,
	publish func(LaunchProof) error,
) error {
	if publish == nil || !validLaunchToken(operationRef) || !cfg.Endpoint.Valid() ||
		!absoluteNonRoot(cfg.StateDomainPath) || !absoluteNonRoot(cfg.PrivateRoot) || !absoluteNonRoot(cfg.SocketPath) ||
		!absoluteNonRoot(cfg.LeaseRoot) || !cfg.RequiredProtocol.Valid() || filepath.Dir(cfg.SocketPath) != cfg.PrivateRoot {
		return hostRefuse(HostRefusalConfigInvalid, nil)
	}
	if _, err := ownerPrivateDirectory(cfg.PrivateRoot); err != nil {
		return err
	}
	if _, err := ownerPrivateDirectory(cfg.StateDomainPath); err != nil {
		return err
	}
	lease, err := verifyCompleteLease(cfg)
	if err != nil {
		return err
	}
	servers := lease.Paths(codexbundle.RoleServer)
	if len(servers) != 1 {
		return hostRefuse(HostRefusalBundleIncomplete, nil)
	}
	executable := servers[0]
	intentPath, guardPath := durableIntentPath(cfg), durableGuardPath(cfg)
	intent, exists, err := readDurableIntent(intentPath)
	if err != nil {
		return err
	}
	if !exists {
		runtimeID, randomErr := randomRuntimeID(privateRuntimeIDBytes)
		if randomErr != nil {
			return hostRefuse(HostRefusalLaunchFailed, randomErr)
		}
		intent = DurableLaunchIntent{
			Version: durableLaunchIntentVersion, OperationRef: operationRef, Endpoint: cfg.Endpoint,
			EndpointRuntimeID: runtimeID, ExecutablePath: executable, SocketPath: cfg.SocketPath,
			Phase: durableLaunchIntended,
		}
		if err := writeDurableIntent(intentPath, intent); err != nil {
			return err
		}
	} else if !sameDurableIntentRequest(intent, cfg, operationRef, executable) {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}

	guard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- exact private launch-intent sibling
	if err != nil {
		return hostRefuse(HostRefusalLaunchFailed, err)
	}
	ownedGuard := false
	switch lockErr := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
	case lockErr == nil:
		ownedGuard = true
	case errors.Is(lockErr, unix.EWOULDBLOCK), errors.Is(lockErr, unix.EAGAIN):
		_ = guard.Close()
	case lockErr != nil:
		_ = guard.Close()
		return hostRefuse(HostRefusalLaunchFailed, lockErr)
	}
	if ownedGuard {
		// A free guard proves no supervisor from this intent is alive. A stale
		// running receipt can only describe a process that has already released
		// its ownership; reset it before creating one replacement attempt.
		intent.Phase, intent.PID, intent.ProcessGroupID = durableLaunchIntended, 0, 0
		if err := writeDurableIntent(intentPath, intent); err != nil {
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return err
		}
		// The operation journal is revalidated only after this durable intent
		// and guard exist. Abort can therefore either prevent launch here or
		// observe/wait on this exact intent; there is no unowned gap between the
		// last authority check and the physical process start.
		start := func() error { return launchDurableSupervisor(cfg, intentPath, guardPath, guard) }
		if authorizeLaunch != nil {
			if err := authorizeLaunch(start); err != nil {
				_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
				_ = guard.Close()
				_ = removeDurableReceipts(intentPath, guardPath)
				return err
			}
		} else if err := start(); err != nil {
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return err
		}
		// The child inherited this exact open file description; closing the
		// parent's duplicate leaves the lock held by the supervisor.
		_ = guard.Close()
	}

	running, err := awaitDurableRunning(ctx, intentPath, cfg, operationRef, executable)
	if err != nil {
		return err
	}
	if afterLaunch != nil {
		if err := afterLaunch(); err != nil {
			return err
		}
	}
	proof, err := awaitDurableProof(ctx, cfg, lease, running)
	if err != nil {
		return err
	}
	return publish(proof)
}

func sameDurableIntentRequest(intent DurableLaunchIntent, cfg PrivateGenerationConfig, operationRef, executable string) bool {
	return intent.valid() && intent.OperationRef == operationRef && intent.Endpoint == cfg.Endpoint &&
		intent.ExecutablePath == executable && intent.SocketPath == cfg.SocketPath
}

func launchDurableSupervisor(cfg PrivateGenerationConfig, intentPath, guardPath string, guard *os.File) error {
	self, err := os.Executable()
	if err != nil {
		return hostRefuse(HostRefusalLaunchFailed, err)
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return hostRefuse(HostRefusalLaunchFailed, err)
	}
	args := []string{"internal", "codex-generation-launch", "--intent", intentPath, "--guard", guardPath}
	// #nosec G204 -- self is the running projmux binary and every argument is an exact private path.
	command := exec.Command(self, args...)
	command.Env = privateGenerationEnvironment(cfg)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.ExtraFiles = []*os.File{guard}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return hostRefuse(HostRefusalLaunchFailed, err)
	}
	defer devNull.Close()
	command.Stdin, command.Stdout, command.Stderr = devNull, devNull, devNull
	if err := command.Start(); err != nil {
		return hostRefuse(HostRefusalLaunchFailed, err)
	}
	if command.Process.Pid <= 0 {
		return hostRefuse(HostRefusalLaunchFailed, nil)
	}
	// Reap when this coordinator remains alive. If it exits first, init adopts
	// the supervisor; the durable guard/receipt remain the recovery authority.
	go func() { _ = command.Wait() }()
	return nil
}

// RunDurableLaunchSupervisor is the hidden self-exec entrypoint. FD 3 is the
// inherited operation guard. The supervisor stays as the exact session leader,
// owns the guard for the child's full lifetime, and never selects a process,
// socket, bundle, or endpoint from ambient state.
func RunDurableLaunchSupervisor(args []string) error {
	if len(args) != 4 || args[0] != "--intent" || args[2] != "--guard" {
		return errors.New("codex generation launch requires exact --intent and --guard")
	}
	intentPath, guardPath := args[1], args[3]
	if !absoluteNonRoot(intentPath) || !absoluteNonRoot(guardPath) || guardPath != intentPath+".guard" {
		return errors.New("codex generation launch paths are invalid")
	}
	guard := os.NewFile(uintptr(3), "codex-generation-guard")
	if guard == nil {
		return errors.New("codex generation launch guard is missing")
	}
	defer guard.Close()
	expectedGuard, err := os.Lstat(guardPath)
	if err != nil {
		return err
	}
	inheritedGuard, err := guard.Stat()
	if err != nil || !os.SameFile(expectedGuard, inheritedGuard) {
		return errors.New("codex generation launch guard identity mismatch")
	}
	intent, exists, err := readDurableIntent(intentPath)
	if err != nil || !exists || intent.Phase != durableLaunchIntended {
		return errors.Join(errors.New("codex generation launch intent unavailable"), err)
	}
	pid := os.Getpid()
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return errors.New("codex generation supervisor is not its exact session leader")
	}
	intent.Phase, intent.PID, intent.ProcessGroupID = durableLaunchRunning, pid, pgid
	if err := writeDurableIntent(intentPath, intent); err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	// #nosec G204 -- executable and socket are the exact prewritten qualified intent, not ambient selectors.
	command := exec.Command(intent.ExecutablePath, "app-server", "--listen", "unix://"+intent.SocketPath)
	command.Env = os.Environ()
	command.Stdin, command.Stdout, command.Stderr = devNull, devNull, devNull
	command.ExtraFiles = []*os.File{guard}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(intent.SocketPath)); err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	observeOwnedSocket := func() {
		if intent.SocketIdentity != nil {
			return
		}
		info, statErr := os.Lstat(intent.SocketPath)
		if statErr != nil || info.Mode()&os.ModeSocket == 0 {
			return
		}
		identity := fileIdentity(info)
		intent.SocketIdentity = &identity
		_ = writeDurableIntent(intentPath, intent)
	}
	observeOwnedSocket()
	for {
		select {
		case waitErr := <-done:
			observeOwnedSocket()
			removeSocketWithIdentity(intent.SocketPath, intent.SocketIdentity)
			return waitErr
		case received := <-signals:
			// Keep the supervisor and its guard alive until the exact child exits
			// and the captured socket inode is cleaned.
			_ = command.Process.Signal(received)
		case event := <-watcher.Events:
			if filepath.Clean(event.Name) == filepath.Clean(intent.SocketPath) && event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) != 0 {
				observeOwnedSocket()
			}
		case <-watcher.Errors:
			// Readiness is decided by the coordinator. Losing this best-effort
			// watcher cannot authorize a non-exact unlink.
		}
	}
}

func awaitDurableRunning(ctx context.Context, intentPath string, cfg PrivateGenerationConfig, operationRef, executable string) (DurableLaunchIntent, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return DurableLaunchIntent{}, err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(intentPath)); err != nil {
		return DurableLaunchIntent{}, err
	}
	observe := func() (DurableLaunchIntent, bool) {
		intent, exists, readErr := readDurableIntent(intentPath)
		return intent, readErr == nil && exists && sameDurableIntentRequest(intent, cfg, operationRef, executable) && intent.Phase == durableLaunchRunning
	}
	if intent, ok := observe(); ok {
		return intent, nil
	}
	for {
		select {
		case <-ctx.Done():
			return DurableLaunchIntent{}, hostRefuse(HostRefusalReadinessFailed, ctx.Err())
		case event := <-watcher.Events:
			if filepath.Clean(event.Name) == filepath.Clean(intentPath) && event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) != 0 {
				if intent, ok := observe(); ok {
					return intent, nil
				}
			}
		case err := <-watcher.Errors:
			if err != nil {
				return DurableLaunchIntent{}, hostRefuse(HostRefusalReadinessFailed, err)
			}
		}
	}
}

func awaitDurableProof(ctx context.Context, cfg PrivateGenerationConfig, lease codexbundle.Lease, intent DurableLaunchIntent) (LaunchProof, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return LaunchProof{}, err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(cfg.SocketPath)); err != nil {
		return LaunchProof{}, err
	}
	observe := func() (LaunchProof, bool) {
		if pgid, pgErr := syscall.Getpgid(intent.PID); pgErr != nil || pgid != intent.ProcessGroupID {
			return LaunchProof{}, false
		}
		socket, socketErr := os.Lstat(cfg.SocketPath)
		if socketErr != nil || socket.Mode()&os.ModeSocket == 0 {
			return LaunchProof{}, false
		}
		ready := cfg.ready
		if ready == nil {
			ready = readyPrivateGeneration
		}
		if readyErr := ready(ctx, cfg.SocketPath); readyErr != nil {
			return LaunchProof{}, false
		}
		executable, executableErr := os.Lstat(intent.ExecutablePath)
		artifact, ok := leaseArtifact(lease, intent.ExecutablePath)
		if executableErr != nil || !ok {
			return LaunchProof{}, false
		}
		latest, latestErr := os.Lstat(cfg.SocketPath)
		if latestErr != nil || !os.SameFile(socket, latest) {
			return LaunchProof{}, false
		}
		return LaunchProof{
			Endpoint: intent.Endpoint, EndpointRuntimeID: intent.EndpointRuntimeID,
			PID: intent.PID, ProcessGroupID: intent.ProcessGroupID, SocketPath: cfg.SocketPath,
			SocketIdentity: fileIdentity(socket), ExecutablePath: intent.ExecutablePath,
			Executable: fileIdentity(executable), ExecutableSHA256: artifact.SHA256, BundleID: lease.ID,
		}, true
	}
	if proof, ok := observe(); ok {
		return proof, nil
	}
	for {
		select {
		case <-ctx.Done():
			return LaunchProof{}, hostRefuse(HostRefusalReadinessFailed, ctx.Err())
		case event := <-watcher.Events:
			if filepath.Clean(event.Name) == filepath.Clean(cfg.SocketPath) && event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) != 0 {
				if proof, ok := observe(); ok {
					return proof, nil
				}
			}
		case err := <-watcher.Errors:
			if err != nil {
				return LaunchProof{}, hostRefuse(HostRefusalReadinessFailed, err)
			}
		}
	}
}

// CleanupDurableCandidate stops only the exact operation-owned candidate and
// removes only its exact socket/intent/guard. The immutable bundle lease is
// retained; this is explicitly not Phase 5 lease release.
func CleanupDurableCandidate(ctx context.Context, cfg PrivateGenerationConfig, operationRef string, proof *LaunchProof) (bool, error) {
	if !validLaunchToken(operationRef) || !cfg.Endpoint.Valid() || !absoluteNonRoot(cfg.PrivateRoot) ||
		!absoluteNonRoot(cfg.SocketPath) || filepath.Dir(cfg.SocketPath) != cfg.PrivateRoot {
		return false, hostRefuse(HostRefusalConfigInvalid, nil)
	}
	if _, err := ownerPrivateDirectory(cfg.PrivateRoot); err != nil {
		return false, err
	}
	intentPath, guardPath := durableIntentPath(cfg), durableGuardPath(cfg)
	intent, exists, err := readDurableIntent(intentPath)
	if err != nil {
		return false, err
	}
	if !exists {
		if _, socketErr := os.Lstat(cfg.SocketPath); socketErr == nil || !errors.Is(socketErr, fs.ErrNotExist) {
			return false, hostRefuse(HostRefusalLaunchProofMismatch, socketErr)
		}
		return false, cleanupUnlockedOrphanGuard(guardPath, cfg.SocketPath)
	}
	if !sameDurableIntentRequest(intent, cfg, operationRef, intent.ExecutablePath) {
		return false, hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	if proof != nil && (intent.Phase != durableLaunchRunning || intent.PID != proof.PID ||
		intent.ProcessGroupID != proof.ProcessGroupID || intent.EndpointRuntimeID != proof.EndpointRuntimeID ||
		intent.ExecutablePath != proof.ExecutablePath) {
		return false, hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	guard, err := os.OpenFile(guardPath, os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- exact operation-owned guard
	if err != nil {
		return false, hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	defer guard.Close()
	lockErr := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if lockErr == nil {
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		if err := removeCandidateSocket(cfg.SocketPath, proof, intent.SocketIdentity); err != nil {
			return false, err
		}
		return intent.Phase == durableLaunchRunning, removeDurableReceipts(intentPath, guardPath)
	}
	if !errors.Is(lockErr, unix.EWOULDBLOCK) && !errors.Is(lockErr, unix.EAGAIN) {
		return false, lockErr
	}
	if intent.Phase == durableLaunchIntended {
		// The supervisor inherited the guard but has not published its PID yet.
		// Wait on the intent rename or the guard-release barrier; never guess a
		// process from the system process table.
		updated, running, waitErr := awaitRunningOrGuardRelease(ctx, intentPath, guardPath, cfg, operationRef, intent.ExecutablePath)
		if waitErr != nil {
			return false, waitErr
		}
		if !running {
			if err := removeCandidateSocket(cfg.SocketPath, proof, intent.SocketIdentity); err != nil {
				return false, err
			}
			return false, removeDurableReceipts(intentPath, guardPath)
		}
		intent = updated
	}
	if proof != nil {
		if err := ObservePrivateGeneration(ctx, cfg, *proof); err != nil {
			return false, err
		}
	}
	// The durable session-leader identity names the whole operation-owned
	// process group. Signalling the group also closes the exact recovery case
	// where the supervisor died after starting its child: the inherited guard
	// proves an owned descendant is still present even though the leader PID can
	// no longer receive a signal.
	if err := syscall.Kill(-intent.ProcessGroupID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, hostRefuse(HostRefusalProcessExited, err)
	}
	if err := awaitGuardRelease(ctx, guardPath); err != nil {
		return false, err
	}
	latest, latestExists, latestErr := readDurableIntent(intentPath)
	if latestErr != nil {
		return false, latestErr
	}
	if latestExists {
		intent = latest
	}
	if err := removeCandidateSocket(cfg.SocketPath, proof, intent.SocketIdentity); err != nil {
		return false, err
	}
	return true, removeDurableReceipts(intentPath, guardPath)
}

// cleanupUnlockedOrphanGuard completes the only recoverable half-written
// teardown: intent unlink succeeded, then the cleaner died before unlinking
// the guard. Absence of both intent and socket plus an exact unlocked guard is
// sufficient because the guard path is operation-specific inside the already
// validated private root. A locked/rebound guard or any socket refuses without
// unlinking either path.
func cleanupUnlockedOrphanGuard(guardPath, socketPath string) error {
	expected, err := os.Lstat(guardPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !expected.Mode().IsRegular() || expected.Mode().Perm() != localstate.PrivateFileMode {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	guard, err := os.OpenFile(guardPath, os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- exact private orphan guard
	if err != nil {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	defer guard.Close()
	opened, err := guard.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	if err := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	defer unix.Flock(int(guard.Fd()), unix.LOCK_UN) //nolint:errcheck -- best effort on exact private guard
	latest, err := os.Lstat(guardPath)
	if err != nil || !os.SameFile(opened, latest) {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	if _, err := os.Lstat(socketPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	if err := os.Remove(guardPath); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(guardPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func awaitGuardRelease(ctx context.Context, guardPath string) error {
	guard, err := os.OpenFile(guardPath, os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- exact operation-owned guard
	if err != nil {
		return err
	}
	fd := int(guard.Fd())
	granted := make(chan error, 1)
	go func() { granted <- unix.Flock(fd, unix.LOCK_EX) }()
	select {
	case err := <-granted:
		if err == nil {
			_ = unix.Flock(fd, unix.LOCK_UN)
		}
		_ = guard.Close()
		return err
	case <-ctx.Done():
		go func() {
			if err := <-granted; err == nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
			}
			_ = guard.Close()
		}()
		return hostRefuse(HostRefusalProcessExited, ctx.Err())
	}
}

func awaitRunningOrGuardRelease(ctx context.Context, intentPath, guardPath string, cfg PrivateGenerationConfig, operationRef, executable string) (DurableLaunchIntent, bool, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	release := make(chan error, 1)
	go func() { release <- awaitGuardRelease(waitCtx, guardPath) }()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return DurableLaunchIntent{}, false, err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(intentPath)); err != nil {
		return DurableLaunchIntent{}, false, err
	}
	for {
		intent, exists, readErr := readDurableIntent(intentPath)
		if readErr != nil {
			return DurableLaunchIntent{}, false, readErr
		}
		if exists && sameDurableIntentRequest(intent, cfg, operationRef, executable) && intent.Phase == durableLaunchRunning {
			return intent, true, nil
		}
		select {
		case err := <-release:
			return DurableLaunchIntent{}, false, err
		case <-ctx.Done():
			return DurableLaunchIntent{}, false, ctx.Err()
		case <-watcher.Events:
		case err := <-watcher.Errors:
			if err != nil {
				return DurableLaunchIntent{}, false, err
			}
		}
	}
}

func removeCandidateSocket(socketPath string, proof *LaunchProof, durable *FileIdentity) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return hostRefuse(HostRefusalLaunchProofMismatch, err)
	}
	expected := durable
	if proof != nil {
		expected = &proof.SocketIdentity
	}
	if expected == nil || fileIdentity(info) != *expected {
		return hostRefuse(HostRefusalLaunchProofMismatch, nil)
	}
	return os.Remove(socketPath)
}

func removeSocketWithIdentity(socketPath string, expected *FileIdentity) {
	if expected == nil {
		return
	}
	info, err := os.Lstat(socketPath)
	if err == nil && info.Mode()&os.ModeSocket != 0 && fileIdentity(info) == *expected {
		_ = os.Remove(socketPath)
	}
}

func removeDurableReceipts(intentPath, guardPath string) error {
	if err := os.Remove(intentPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(guardPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir, err := os.Open(filepath.Dir(intentPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readDurableIntent(path string) (DurableLaunchIntent, bool, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- exact private generation intent
	if errors.Is(err, fs.ErrNotExist) {
		return DurableLaunchIntent{}, false, nil
	}
	if err != nil {
		return DurableLaunchIntent{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var intent DurableLaunchIntent
	if err := decoder.Decode(&intent); err != nil {
		return DurableLaunchIntent{}, false, err
	}
	if !intent.valid() {
		return DurableLaunchIntent{}, false, errors.New("invalid durable launch intent")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DurableLaunchIntent{}, false, errors.New("durable launch intent has trailing JSON")
	}
	return intent, true, nil
}

func writeDurableIntent(path string, intent DurableLaunchIntent) error {
	if !intent.valid() {
		return hostRefuse(HostRefusalConfigInvalid, nil)
	}
	body, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".launch-intent-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(body); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
