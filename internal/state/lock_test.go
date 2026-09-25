package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func setRow(row string) func([]string) ([]string, bool, error) {
	key, _, _ := strings.Cut(row, "\t")
	return func(lines []string) ([]string, bool, error) {
		next := make([]string, 0, len(lines)+1)
		for _, line := range lines {
			if lineKey, _, _ := strings.Cut(line, "\t"); lineKey == key {
				continue
			}
			next = append(next, line)
		}
		return append(next, row), true, nil
	}
}

func TestLinesFileUpdateKeepsConcurrentRowsUnderTheLock(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	aRead := make(chan struct{})
	releaseA := make(chan struct{})
	bContended := make(chan struct{})

	fileA := NewLinesFile(path)
	fileA.hooks = &linesHooks{afterRead: func() {
		close(aRead)
		<-releaseA
	}}
	fileB := NewLinesFile(path)
	fileB.hooks = &linesHooks{afterContendedLock: func() { close(bContended) }}

	aDone := make(chan error, 1)
	go func() { aDone <- fileA.Update(setRow("session-a\t1\t0")) }()
	<-aRead

	bDone := make(chan error, 1)
	go func() { bDone <- fileB.Update(setRow("session-b\t2\t0")) }()

	// With the lock, B must contend. Without it, B reads the empty file and
	// finishes first; A then overwrites B's row and the final assertion fails.
	var bErr error
	bFinished := false
	select {
	case <-bContended:
	case bErr = <-bDone:
		bFinished = true
	}

	close(releaseA)
	if err := <-aDone; err != nil {
		t.Fatalf("A Update() error = %v", err)
	}
	if !bFinished {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatalf("B Update() error = %v", bErr)
	}

	got, err := NewLinesFile(path).Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []string{"session-a\t1\t0", "session-b\t2\t0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q (a concurrent update was lost)", got, want)
	}
}

func TestLinesFileUpdateGivesUpAfterTheWaitLimit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	file := NewLinesFile(path)
	if err := file.Write([]string{"session-a\t1\t0"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	holder := holdLock(t, file.LockPath())

	called := false
	err := file.WithLockWaitLimit(20 * time.Millisecond).Update(func(lines []string) ([]string, bool, error) {
		called = true
		return lines, true, nil
	})
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Update() error = %v, want ErrLockTimeout", err)
	}
	if !strings.Contains(err.Error(), file.LockPath()) {
		t.Fatalf("Update() error = %q, want it to name %s", err, file.LockPath())
	}
	if called {
		t.Fatal("Update() called update without the lock")
	}
	assertFileContent(t, path, "session-a\t1\t0\n")

	if err := holder.Close(); err != nil {
		t.Fatalf("Close(holder) error = %v", err)
	}

	// The abandoned waiter is granted the lock once the holder leaves and must
	// hand it back; a blocking Update with the normal limit then gets it.
	for attempt := 1; ; attempt++ {
		err := file.Update(setRow("session-b\t2\t0"))
		if err == nil {
			break
		}
		if !errors.Is(err, ErrLockTimeout) || attempt == 3 {
			t.Fatalf("Update() after release error = %v", err)
		}
	}
	assertFileContent(t, path, "session-a\t1\t0\nsession-b\t2\t0\n")
}

func TestLinesFileUpdateLockSurvivesTempReclaimAndIsPrivate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "preview-state")
	file := NewLinesFile(path)
	if err := file.Update(setRow("session-a\t1\t0")); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	lockPath := file.LockPath()
	if got, want := lockPath, filepath.Join(dir, ".preview-state.lock"); got != want {
		t.Fatalf("LockPath() = %q, want %q", got, want)
	}
	old := time.Now().Add(-2 * StaleTempAge)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if err := file.Write([]string{"session-b\t2\t0"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertMode(t, lockPath, PrivateFileMode)
}

func TestLinesFileUpdateWritesNothingWhenUpdateDeclines(t *testing.T) {
	t.Parallel()

	updateErr := errors.New("update failed")
	tests := []struct {
		name    string
		update  func([]string) ([]string, bool, error)
		wantErr error
	}{
		{
			name: "write false",
			update: func([]string) ([]string, bool, error) {
				return []string{"replaced"}, false, nil
			},
		},
		{
			name: "error",
			update: func([]string) ([]string, bool, error) {
				return []string{"replaced"}, true, updateErr
			},
			wantErr: updateErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "preview-state")
			if err := NewLinesFile(path).Write([]string{"kept"}); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			old := time.Now().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatalf("Chtimes() error = %v", err)
			}

			var seen []string
			err := NewLinesFile(path).Update(func(lines []string) ([]string, bool, error) {
				seen = lines
				return tt.update(lines)
			})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(seen, []string{"kept"}) {
				t.Fatalf("update saw %q, want [kept]", seen)
			}
			assertFileContent(t, path, "kept\n")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat() error = %v", err)
			}
			if !info.ModTime().Equal(old) {
				t.Fatalf("mtime = %v, want unchanged %v", info.ModTime(), old)
			}
		})
	}
}

// holdLock takes the exclusive lock the way another process would; closing
// the returned file releases it.
func holdLock(t *testing.T, lockPath string) *os.File {
	t.Helper()
	if err := EnsurePrivateDir(filepath.Dir(lockPath)); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, PrivateFileMode)
	if err != nil {
		t.Fatalf("OpenFile(lock) error = %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("Flock() error = %v", err)
	}
	return held
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if got := string(raw); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
