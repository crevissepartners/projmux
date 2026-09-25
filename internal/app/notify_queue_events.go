package app

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	notifyQueueEventDirName        = "notify-queue-events"
	notifyQueueEventSocketPattern  = "refresh-*.sock"
	notifyQueueEventSocketTemplate = "refresh-%d-%d.sock"
	notifyQueueEventLongestSocket  = "refresh-999999-18446744073709551615.sock"
	notifyQueueEventPayload        = "pending\n"
	notifyQueueEventWriteTimeout   = 20 * time.Millisecond
	notifyQueueEventMaxSocketPath  = 100
	// notifyQueueEventShortRoot is the last-resort root when neither the state
	// dir nor os.TempDir() leaves room for a unix socket path.
	notifyQueueEventShortRoot = "/tmp"
)

var notifyQueueEventSeq atomic.Uint64

type notifyQueueRefreshEvents interface {
	Publish() error
	Subscribe(context.Context) (<-chan struct{}, error)
}

// notifyQueueRefreshTransport carries the resolved socket dir. shared marks a
// dir in a shared temp area, which must pass the trust check for uid before
// Subscribe binds or Publish touches it. err records a resolution failure.
type notifyQueueRefreshTransport struct {
	dir          string
	shared       bool
	uid          int
	err          error
	processAlive func(int) bool
}

func (c *aiCommand) publishNotifyQueueRefreshBestEffort() {
	if c == nil {
		return
	}
	events := c.events
	if events == nil {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return
		}
		events = newNotifyQueueRefreshTransport(paths.StateDir)
	}
	_ = events.Publish()
}

func newNotifyQueueRefreshTransport(stateDir string) notifyQueueRefreshTransport {
	return newNotifyQueueRefreshTransportAt(stateDir, os.TempDir(), notifyQueueEventShortRoot, os.Getuid())
}

func newNotifyQueueRefreshTransportAt(stateDir, tempDir, shortRoot string, uid int) notifyQueueRefreshTransport {
	dir, shared, err := resolveNotifyQueueEventDir(stateDir, tempDir, shortRoot, uid)
	return notifyQueueRefreshTransport{
		dir:          dir,
		shared:       shared,
		uid:          uid,
		err:          err,
		processAlive: notifyQueueEventProcessAlive,
	}
}

// resolveNotifyQueueEventDir picks the first candidate whose longest socket
// path fits notifyQueueEventMaxSocketPath: the state dir, then os.TempDir(),
// then a short uid-scoped name under shortRoot. The choice depends on path
// lengths only, so publishers and subscribers agree on the same dir. Every
// candidate but the state dir is shared and needs the trust check.
func resolveNotifyQueueEventDir(stateDir, tempDir, shortRoot string, uid int) (string, bool, error) {
	stateDir = filepath.Clean(stateDir)
	if stateDir == "." || stateDir == "" {
		return "", false, nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(stateDir))
	sum := h.Sum64()
	candidates := []struct {
		dir    string
		shared bool
	}{
		{dir: filepath.Join(stateDir, notifyQueueEventDirName)},
		{dir: filepath.Join(tempDir, fmt.Sprintf("projmux-notify-queue-events-%016x", sum)), shared: true},
		{dir: filepath.Join(shortRoot, fmt.Sprintf("projmux-nq-%d-%016x", uid, sum)), shared: true},
	}
	tried := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		longestSocket := filepath.Join(candidate.dir, notifyQueueEventLongestSocket)
		if len(longestSocket) <= notifyQueueEventMaxSocketPath {
			return candidate.dir, candidate.shared, nil
		}
		tried = append(tried, fmt.Sprintf("%s (%d bytes)", longestSocket, len(longestSocket)))
	}
	return "", false, fmt.Errorf("notify queue event socket path exceeds %d bytes for every candidate: %s",
		notifyQueueEventMaxSocketPath, strings.Join(tried, ", "))
}

// checkNotifyQueueEventDir rejects a shared dir that another user could
// control: it must be a real directory, owned by uid, with mode exactly 0700.
// It never repairs the dir.
func checkNotifyQueueEventDir(dir string, uid int) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("untrusted notify queue event dir %s: is a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("untrusted notify queue event dir %s: is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("untrusted notify queue event dir %s: owner is unknown", dir)
	}
	// Widen both sides so the comparison never truncates a uid.
	if int64(stat.Uid) != int64(uid) {
		return fmt.Errorf("untrusted notify queue event dir %s: owned by uid %d, want %d", dir, stat.Uid, uid)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("untrusted notify queue event dir %s: mode %04o, want 0700", dir, perm)
	}
	return nil
}

// prepareNotifyQueueEventDir creates the socket dir for Subscribe. A shared
// dir is created 0700 when missing and must then pass the trust check.
func (t notifyQueueRefreshTransport) prepareNotifyQueueEventDir() error {
	if !t.shared {
		if err := os.MkdirAll(t.dir, 0o700); err != nil {
			return fmt.Errorf("create notify queue event dir: %w", err)
		}
		return nil
	}
	if err := os.Mkdir(t.dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create notify queue event dir: %w", err)
	}
	return checkNotifyQueueEventDir(t.dir, t.uid)
}

func (t notifyQueueRefreshTransport) Publish() error {
	if t.err != nil {
		return t.err
	}
	if t.dir == "" {
		return nil
	}
	if t.shared {
		// Without a subscriber the shared dir may not exist yet; never create,
		// sweep, or send into it unless it passes the trust check.
		if err := checkNotifyQueueEventDir(t.dir, t.uid); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
	}
	paths, err := filepath.Glob(filepath.Join(t.dir, notifyQueueEventSocketPattern))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if pid, ok := notifyQueueEventSocketPID(path); ok &&
			t.processAlive != nil && !t.processAlive(pid) {
			_ = os.Remove(path)
			continue
		}
		_ = publishNotifyQueueRefreshTo(path)
	}
	return nil
}

func notifyQueueEventSocketPID(path string) (int, bool) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "refresh-") || !strings.HasSuffix(name, ".sock") {
		return 0, false
	}
	pidText, _, ok := strings.Cut(strings.TrimPrefix(name, "refresh-"), "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func publishNotifyQueueRefreshTo(path string) error {
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(notifyQueueEventWriteTimeout))
	_, err = conn.Write([]byte(notifyQueueEventPayload))
	return err
}

func (t notifyQueueRefreshTransport) Subscribe(ctx context.Context) (<-chan struct{}, error) {
	if t.err != nil {
		return nil, t.err
	}
	if t.dir == "" {
		return nil, errors.New("notify queue event dir is empty")
	}
	if err := t.prepareNotifyQueueEventDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(t.dir, fmt.Sprintf(notifyQueueEventSocketTemplate, os.Getpid(), notifyQueueEventSeq.Add(1)))
	_ = os.Remove(path)

	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("listen for notify queue events: %w", err)
	}
	_ = os.Chmod(path, 0o600)

	events := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
		_ = os.Remove(path)
	}()
	go func() {
		defer close(events)
		defer os.Remove(path)
		var buf [64]byte
		for {
			if _, _, err := conn.ReadFromUnix(buf[:]); err != nil {
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	return events, nil
}
