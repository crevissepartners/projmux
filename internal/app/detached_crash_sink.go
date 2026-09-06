package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	detachedCrashDirName          = "crash"
	detachedCrashArtifactMaxBytes = 1024 * 1024
	detachedCrashExitCode         = 2

	detachedCrashRoleCodexBrokerWatch detachedCrashRole = "codex-broker-watch"
	detachedCrashRoleCodexBrokerServe detachedCrashRole = "codex-broker-serve"
)

var detachedCrashTruncationMarker = []byte("\n--- projmux SIGQUIT stack truncated at 1048576 bytes ---\n")

// detachedCrashRole is the closed filename vocabulary for detached processes
// whose inherited stderr is intentionally unavailable. Keep this list narrow:
// the signal sink is not a general process logger.
type detachedCrashRole string

func (role detachedCrashRole) valid() bool {
	switch role {
	case detachedCrashRoleCodexBrokerWatch, detachedCrashRoleCodexBrokerServe:
		return true
	default:
		return false
	}
}

// detachedCrashSink owns one process-local SIGQUIT subscription. State path
// resolution and filesystem access are deliberately deferred until a signal
// arrives, so an ordinary process lifetime creates no artifact and holds no
// append log open.
type detachedCrashSink struct {
	signals chan os.Signal
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func startDetachedCrashSink(role detachedCrashRole, stateDir func() (string, error)) *detachedCrashSink {
	sink := &detachedCrashSink{signals: make(chan os.Signal, 1), stop: make(chan struct{}), done: make(chan struct{})}
	signal.Notify(sink.signals, syscall.SIGQUIT)
	go func() {
		defer close(sink.done)
		select {
		case <-sink.signals:
			if stateDir != nil {
				if dir, err := stateDir(); err == nil {
					_ = publishDetachedCrashArtifact(dir, role, os.Getpid(), captureDetachedCrashStack(role, os.Getpid()))
				}
			}
			// Go's default SIGQUIT path is fatal. Intercepting it only changes
			// where the dump lands; retain that non-zero termination meaning
			// whether capture or publication succeeded or failed.
			os.Exit(detachedCrashExitCode)
		case <-sink.stop:
		}
	}()
	return sink
}

func (sink *detachedCrashSink) Close() {
	if sink == nil {
		return
	}
	sink.once.Do(func() {
		signal.Stop(sink.signals)
		close(sink.stop)
		<-sink.done
	})
}

// resolveDetachedCrashStateDir resolves the standard projmux state directory
// without adding another environment or configuration surface.
func resolveDetachedCrashStateDir(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	stateHome := strings.TrimSpace(lookupEnv("XDG_STATE_HOME"))
	if stateHome == "" {
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		home, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", errors.New("home directory is required when XDG_STATE_HOME is unset")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	stateDir := filepath.Join(stateHome, config.AppName)
	if err := validateDetachedCrashStateDir(stateDir); err != nil {
		return "", err
	}
	return stateDir, nil
}

func validateDetachedCrashStateDir(stateDir string) error {
	if strings.TrimSpace(stateDir) == "" || stateDir != strings.TrimSpace(stateDir) || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return errors.New("detached crash state directory must be a clean absolute path")
	}
	volume := filepath.VolumeName(stateDir) + string(os.PathSeparator)
	if stateDir == volume {
		return errors.New("detached crash state directory cannot be a filesystem root")
	}
	return nil
}

func detachedCrashArtifactPath(stateDir string, role detachedCrashRole, pid int) (string, error) {
	if err := validateDetachedCrashStateDir(stateDir); err != nil {
		return "", err
	}
	if !role.valid() || pid <= 0 {
		return "", errors.New("detached crash artifact requires a known role and positive pid")
	}
	name := string(role) + "-" + strconv.Itoa(pid) + ".sigquit.txt"
	return filepath.Join(stateDir, detachedCrashDirName, name), nil
}

func captureDetachedCrashStack(role detachedCrashRole, pid int) []byte {
	header := detachedCrashArtifactHeader(role, pid)
	available := detachedCrashArtifactMaxBytes - len(header)
	if available <= len(detachedCrashTruncationMarker) {
		return append([]byte(nil), header[:detachedCrashArtifactMaxBytes]...)
	}
	// One extra byte distinguishes a dump that fits from one that reached the
	// artifact boundary. runtime.Stack was asked for every goroutine even when
	// the bounded representation has to be marked as truncated.
	stack := make([]byte, available+1)
	n := runtime.Stack(stack, true)
	truncated := n == len(stack)
	if !truncated {
		return formatDetachedCrashArtifact(header, stack[:n], false)
	}
	return formatDetachedCrashArtifact(header, stack[:n], true)
}

func detachedCrashArtifactHeader(role detachedCrashRole, pid int) []byte {
	return fmt.Appendf(nil, "projmux detached SIGQUIT stack\nrole=%s\npid=%d\nscope=all-goroutines\n\n", role, pid)
}

func formatDetachedCrashArtifact(header, stack []byte, truncated bool) []byte {
	limit := max(detachedCrashArtifactMaxBytes-len(header), 0)
	if !truncated && len(stack) <= limit {
		artifact := make([]byte, 0, len(header)+len(stack))
		artifact = append(artifact, header...)
		return append(artifact, stack...)
	}
	stackLimit := max(limit-len(detachedCrashTruncationMarker), 0)
	if len(stack) > stackLimit {
		stack = stack[:stackLimit]
	}
	artifact := make([]byte, 0, len(header)+len(stack)+len(detachedCrashTruncationMarker))
	artifact = append(artifact, header...)
	artifact = append(artifact, stack...)
	return append(artifact, detachedCrashTruncationMarker...)
}

func publishDetachedCrashArtifact(stateDir string, role detachedCrashRole, pid int, artifact []byte) error {
	target, err := detachedCrashArtifactPath(stateDir, role, pid)
	if err != nil {
		return err
	}
	if len(artifact) > detachedCrashArtifactMaxBytes {
		return errors.New("detached crash artifact exceeds its byte bound")
	}
	if err := ensureDetachedCrashDirectory(stateDir); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("detached crash artifact target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, ".sigquit-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if written, err := temp.Write(artifact); err != nil {
		_ = temp.Close()
		return err
	} else if written != len(artifact) {
		_ = temp.Close()
		return io.ErrShortWrite
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Linking a complete private temp file makes publication atomic and, unlike
	// rename, refuses every existing target including a symlink or FIFO.
	if err := os.Link(tempName, target); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !detachedCrashPathOwnedByCurrentUser(info) {
		_ = os.Remove(target)
		if err != nil {
			return err
		}
		return errors.New("detached crash artifact did not publish as a private regular file")
	}
	return nil
}

func ensureDetachedCrashDirectory(stateDir string) error {
	if err := validateDetachedCrashStateDir(stateDir); err != nil {
		return err
	}
	if err := ensureDetachedCrashDirectoryComponent(stateDir); err != nil {
		return fmt.Errorf("prepare detached crash state directory: %w", err)
	}
	crashDir := filepath.Join(stateDir, detachedCrashDirName)
	if err := os.Mkdir(crashDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := ensureDetachedCrashDirectoryComponent(crashDir); err != nil {
		return fmt.Errorf("prepare detached crash artifact directory: %w", err)
	}
	return nil
}

func ensureDetachedCrashDirectoryComponent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !detachedCrashPathOwnedByCurrentUser(info) {
		return errors.New("path component is not a non-symlink directory")
	}
	// #nosec G302 -- path is a verified owner-owned, non-symlink directory; owner-private mode 0700 intentionally includes execute permission required for directory traversal, not executable file content.
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 || !detachedCrashPathOwnedByCurrentUser(info) {
		if err != nil {
			return err
		}
		return errors.New("path component is not an owner-private directory")
	}
	return nil
}

func detachedCrashPathOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
