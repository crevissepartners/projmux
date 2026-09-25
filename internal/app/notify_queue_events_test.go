package app

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestNotifyQueueRefreshTransport builds a transport over stateDir with the
// short root injected under shortTempDomain, so the last-resort candidate never
// lands in the real /tmp. When the socket dir falls back outside stateDir
// (t.TempDir() is too long for a unix socket path), that fallback dir is
// removed on cleanup, whether it sits in os.TempDir() or the short root.
func newTestNotifyQueueRefreshTransport(t *testing.T, stateDir string) notifyQueueRefreshTransport {
	t.Helper()
	transport := newNotifyQueueRefreshTransportAt(stateDir, os.TempDir(), shortTempDomain(t), os.Getuid())
	if transport.err != nil {
		t.Fatalf("resolve notify queue event dir: %v", transport.err)
	}
	if transport.dir != "" && !notifyQueueTestPathWithin(stateDir, transport.dir) {
		// Runs after t.Context() is cancelled; a subscriber goroutine may be
		// removing its socket concurrently, which RemoveAll tolerates.
		t.Cleanup(func() {
			if err := os.RemoveAll(transport.dir); err != nil {
				t.Errorf("RemoveAll(%q) error = %v", transport.dir, err)
			}
		})
	}
	return transport
}

func notifyQueueTestPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestNotifyQueueRefreshTransportPublishesToSubscriber(t *testing.T) {
	t.Parallel()

	transport := newTestNotifyQueueRefreshTransport(t, shortTempDomain(t))
	ctx := t.Context()

	events, err := transport.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notify queue refresh event")
	}
}

func TestNotifyQueueRefreshTransportPublishWithoutSubscribersIsNoop(t *testing.T) {
	t.Parallel()

	transport := newTestNotifyQueueRefreshTransport(t, t.TempDir())
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestNotifyQueueRefreshTransportSweepsDeadPIDSocketAndPreservesLivePIDSocket(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	transport := newTestNotifyQueueRefreshTransport(t, dir)
	if err := os.MkdirAll(transport.dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	deadPath := filepath.Join(transport.dir, "refresh-1001-1.sock")
	livePath := filepath.Join(transport.dir, "refresh-1002-2.sock")
	for _, path := range []string{deadPath, livePath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	transport.processAlive = func(pid int) bool {
		return pid == 1002
	}

	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("dead socket Stat() error = %v, want not exist", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live socket Stat() error = %v, want preserved", err)
	}
}

// TestNotifyQueueRefreshTransportTestHelperRemovesFallbackDir guards against
// leaving projmux-notify-queue-events-* or projmux-nq-* dirs behind on every
// test run, whichever shared candidate the current TMPDIR length selects.
func TestNotifyQueueRefreshTransportTestHelperRemovesFallbackDir(t *testing.T) {
	var dir string
	t.Run("fallback", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), strings.Repeat("s", 64))
		transport := newTestNotifyQueueRefreshTransport(t, stateDir)
		dir = transport.dir
		if notifyQueueTestPathWithin(stateDir, dir) || !transport.shared {
			t.Fatalf("transport dir = %q (shared %v), want shared fallback outside %q", dir, transport.shared, stateDir)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "refresh-1002-2.sock"), nil, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	})
	if dir == "" {
		t.Fatal("fallback subtest did not record a transport dir")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		_ = os.RemoveAll(dir)
		t.Fatalf("fallback dir Stat(%q) error = %v, want not exist after subtest cleanup", dir, err)
	}
}

func TestNotifyQueueEventSocketPID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		pid  int
		ok   bool
	}{
		{name: "valid", path: "/tmp/refresh-123-9.sock", pid: 123, ok: true},
		{name: "missing sequence", path: "/tmp/refresh-123.sock"},
		{name: "non numeric pid", path: "/tmp/refresh-nope-9.sock"},
		{name: "wrong suffix", path: "/tmp/refresh-123-9.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pid, ok := notifyQueueEventSocketPID(tt.path)
			if pid != tt.pid || ok != tt.ok {
				t.Fatalf("notifyQueueEventSocketPID(%q) = (%d, %v), want (%d, %v)", tt.path, pid, ok, tt.pid, tt.ok)
			}
		})
	}
}

// notifyQueueTestHash mirrors the fnv64a suffix of the shared candidates.
func notifyQueueTestHash(stateDir string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(filepath.Clean(stateDir)))
	return fmt.Sprintf("%016x", h.Sum64())
}

// notifyQueueTestPathOfLen returns an absolute path of exactly n bytes.
func notifyQueueTestPathOfLen(n int) string {
	return "/" + strings.Repeat("t", n-1)
}

// notifyQueueTestLongStateDir returns a state dir too long for candidate ①.
func notifyQueueTestLongStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), strings.Repeat("s", 80))
}

func TestNotifyQueueEventDirResolverPicksFirstCandidateThatFits(t *testing.T) {
	t.Parallel()

	const uid = 1000
	longState := "/home/user/" + strings.Repeat("s", 80)
	// ① fits exactly when len(stateDir) + len("/notify-queue-events/") + 40 <= 100.
	boundaryState := notifyQueueTestPathOfLen(39)
	tests := []struct {
		name     string
		stateDir string
		tempDir  string
		want     string
		shared   bool
	}{
		{
			name:     "short state dir uses state dir",
			stateDir: "/home/u/.local/state/projmux",
			tempDir:  notifyQueueTestPathOfLen(110),
			want:     "/home/u/.local/state/projmux/notify-queue-events",
		},
		{
			name:     "state dir at the byte limit uses state dir",
			stateDir: boundaryState,
			tempDir:  "/tmp",
			want:     filepath.Join(boundaryState, notifyQueueEventDirName),
		},
		{
			name:     "state dir one byte over the limit uses temp dir",
			stateDir: notifyQueueTestPathOfLen(40),
			tempDir:  "/tmp",
			want:     "/tmp/projmux-notify-queue-events-" + notifyQueueTestHash(notifyQueueTestPathOfLen(40)),
			shared:   true,
		},
		{
			name:     "long state dir with short temp dir keeps the existing temp name",
			stateDir: longState,
			tempDir:  "/tmp",
			want:     "/tmp/projmux-notify-queue-events-" + notifyQueueTestHash(longState),
			shared:   true,
		},
		{
			name:     "long state dir with 55 byte temp dir uses short root",
			stateDir: longState,
			tempDir:  notifyQueueTestPathOfLen(55),
			want:     fmt.Sprintf("/tmp/projmux-nq-%d-%s", uid, notifyQueueTestHash(longState)),
			shared:   true,
		},
		{
			name:     "long state dir with 110 byte temp dir uses short root",
			stateDir: longState,
			tempDir:  notifyQueueTestPathOfLen(110),
			want:     fmt.Sprintf("/tmp/projmux-nq-%d-%s", uid, notifyQueueTestHash(longState)),
			shared:   true,
		},
		{
			name:     "empty state dir resolves to no dir",
			stateDir: "",
			tempDir:  "/tmp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, shared, err := resolveNotifyQueueEventDir(tt.stateDir, tt.tempDir, "/tmp", uid)
			if err != nil {
				t.Fatalf("resolveNotifyQueueEventDir() error = %v", err)
			}
			if dir != tt.want || shared != tt.shared {
				t.Fatalf("resolveNotifyQueueEventDir() = (%q, %v), want (%q, %v)", dir, shared, tt.want, tt.shared)
			}
		})
	}
}

func TestNotifyQueueEventDirShortRootFitsTenDigitUID(t *testing.T) {
	t.Parallel()

	dir, _, err := resolveNotifyQueueEventDir(notifyQueueTestPathOfLen(90), notifyQueueTestPathOfLen(110), "/tmp", 4294967295)
	if err != nil {
		t.Fatalf("resolveNotifyQueueEventDir() error = %v", err)
	}
	if got := len(filepath.Join(dir, notifyQueueEventLongestSocket)); got > notifyQueueEventMaxSocketPath {
		t.Fatalf("worst-case socket path = %d bytes, want <= %d", got, notifyQueueEventMaxSocketPath)
	}
}

func TestNotifyQueueEventDirReportsByteCountsWhenNoCandidateFits(t *testing.T) {
	t.Parallel()

	stateDir := notifyQueueTestPathOfLen(90)
	transport := newNotifyQueueRefreshTransportAt(stateDir, notifyQueueTestPathOfLen(110), notifyQueueTestPathOfLen(70), 1000)
	if transport.err == nil {
		t.Fatalf("transport dir = %q, want resolution error", transport.dir)
	}
	_, subscribeErr := transport.Subscribe(t.Context())
	publishErr := transport.Publish()
	for name, err := range map[string]error{"Subscribe": subscribeErr, "Publish": publishErr} {
		if err == nil {
			t.Fatalf("%s() error = nil, want too-long error", name)
		}
		msg := err.Error()
		for _, want := range []string{"exceeds 100 bytes", "(151 bytes)", "(196 bytes)", fmt.Sprintf("(%d bytes)", 70+1+len("projmux-nq-1000-")+16+1+len(notifyQueueEventLongestSocket))} {
			if !strings.Contains(msg, want) {
				t.Fatalf("%s() error = %q, want it to contain %q", name, msg, want)
			}
		}
	}
}

func TestNotifyQueueRefreshTransportLongStateDirDeliversThroughShortRoot(t *testing.T) {
	t.Parallel()

	for _, tempLen := range []int{55, 110} {
		t.Run(fmt.Sprintf("temp dir %d bytes", tempLen), func(t *testing.T) {
			t.Parallel()
			tempRoot := shortTempDomain(t)
			tempDir := filepath.Join(tempRoot, strings.Repeat("t", tempLen-len(tempRoot)-1))
			if err := os.MkdirAll(tempDir, 0o700); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			shortRoot := shortTempDomain(t)
			stateDir := notifyQueueTestLongStateDir(t)
			uid := os.Getuid()

			subscriber := newNotifyQueueRefreshTransportAt(stateDir, tempDir, shortRoot, uid)
			want := filepath.Join(shortRoot, fmt.Sprintf("projmux-nq-%d-%s", uid, notifyQueueTestHash(stateDir)))
			if subscriber.dir != want || !subscriber.shared {
				t.Fatalf("transport dir = (%q, %v), want (%q, true)", subscriber.dir, subscriber.shared, want)
			}
			events, err := subscriber.Subscribe(t.Context())
			if err != nil {
				t.Fatalf("Subscribe() error = %v", err)
			}
			info, err := os.Lstat(want)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("Lstat(%q) = (%v, %v), want 0700 dir", want, info, err)
			}

			// The publisher resolves independently and must reach the same dir.
			publisher := newNotifyQueueRefreshTransportAt(stateDir, tempDir, shortRoot, uid)
			if err := publisher.Publish(); err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			select {
			case <-events:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for notify queue refresh event")
			}
		})
	}
}

func TestNotifyQueueRefreshTransportPublishSkipsMissingSharedDir(t *testing.T) {
	t.Parallel()

	transport := newNotifyQueueRefreshTransportAt(notifyQueueTestLongStateDir(t), notifyQueueTestPathOfLen(110), shortTempDomain(t), os.Getuid())
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if _, err := os.Lstat(transport.dir); !os.IsNotExist(err) {
		t.Fatalf("Lstat(%q) error = %v, want Publish not to create the shared dir", transport.dir, err)
	}
}

// TestNotifyQueueRefreshTransportRejectsUntrustedSharedDir covers both shared
// candidates. The temp dir candidate is built directly because selecting it
// through the resolver needs a temp dir of at most 14 bytes, which tests
// cannot create portably; the trust check is the same code path.
func TestNotifyQueueRefreshTransportRejectsUntrustedSharedDir(t *testing.T) {
	t.Parallel()

	candidates := map[string]func(t *testing.T, uid int) notifyQueueRefreshTransport{
		"temp dir": func(t *testing.T, uid int) notifyQueueRefreshTransport {
			stateDir := notifyQueueTestLongStateDir(t)
			return notifyQueueRefreshTransport{
				dir:          filepath.Join(shortTempDomain(t), "projmux-notify-queue-events-"+notifyQueueTestHash(stateDir)),
				shared:       true,
				uid:          uid,
				processAlive: notifyQueueEventProcessAlive,
			}
		},
		"short root": func(t *testing.T, uid int) notifyQueueRefreshTransport {
			return newNotifyQueueRefreshTransportAt(notifyQueueTestLongStateDir(t), notifyQueueTestPathOfLen(110), shortTempDomain(t), uid)
		},
	}
	cases := []struct {
		name string
		// setup prepares the rejected dir and returns where sockets would land.
		setup func(t *testing.T, dir string) string
		uid   func() int
		want  string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, dir string) string {
				target := dir + "-target"
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return target
			},
			want: "is a symlink",
		},
		{
			name: "wrong mode",
			setup: func(t *testing.T, dir string) string {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				return dir
			},
			want: "mode 0755, want 0700",
		},
		{
			name: "other owner",
			setup: func(t *testing.T, dir string) string {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				return dir
			},
			uid:  func() int { return os.Getuid() + 1 },
			want: fmt.Sprintf("owned by uid %d, want %d", os.Getuid(), os.Getuid()+1),
		},
	}
	for candidateName, build := range candidates {
		for _, tc := range cases {
			t.Run(candidateName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				uid := os.Getuid()
				if tc.uid != nil {
					uid = tc.uid()
				}
				transport := build(t, uid)
				if transport.err != nil || !transport.shared {
					t.Fatalf("transport = (%q, shared %v, err %v), want shared candidate", transport.dir, transport.shared, transport.err)
				}
				socketDir := tc.setup(t, transport.dir)

				_, err := transport.Subscribe(t.Context())
				if err == nil || !strings.Contains(err.Error(), transport.dir) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Subscribe() error = %v, want it to name %q and %q", err, transport.dir, tc.want)
				}
				entries, err := os.ReadDir(socketDir)
				if err != nil {
					t.Fatalf("ReadDir(%q) error = %v", socketDir, err)
				}
				if len(entries) != 0 {
					t.Fatalf("ReadDir(%q) = %d entries, want no socket after rejected Subscribe", socketDir, len(entries))
				}

				stale := filepath.Join(socketDir, "refresh-1001-1.sock")
				if err := os.WriteFile(stale, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				transport.processAlive = func(int) bool { return false }
				if err := transport.Publish(); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Publish() error = %v, want %q", err, tc.want)
				}
				if _, err := os.Stat(stale); err != nil {
					t.Fatalf("Stat(%q) error = %v, want Publish to leave the untrusted dir untouched", stale, err)
				}
			})
		}
	}
}
