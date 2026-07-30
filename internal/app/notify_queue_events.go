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
)

var notifyQueueEventSeq atomic.Uint64

type notifyQueueRefreshEvents interface {
	Publish() error
	Subscribe(context.Context) (<-chan struct{}, error)
}

type notifyQueueRefreshTransport struct {
	dir          string
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
	return notifyQueueRefreshTransport{
		dir:          notifyQueueEventDir(stateDir),
		processAlive: notifyQueueEventProcessAlive,
	}
}

func notifyQueueEventDir(stateDir string) string {
	stateDir = filepath.Clean(stateDir)
	if stateDir == "." || stateDir == "" {
		return ""
	}
	candidate := filepath.Join(stateDir, notifyQueueEventDirName)
	longestSocket := filepath.Join(candidate, notifyQueueEventLongestSocket)
	if len(longestSocket) <= notifyQueueEventMaxSocketPath {
		return candidate
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(stateDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("projmux-notify-queue-events-%016x", h.Sum64()))
}

func (t notifyQueueRefreshTransport) Publish() error {
	if t.dir == "" {
		return nil
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
	if t.dir == "" {
		return nil, errors.New("notify queue event dir is empty")
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create notify queue event dir: %w", err)
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
