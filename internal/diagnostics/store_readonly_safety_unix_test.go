package diagnostics

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStoreReadOnlyRejectsUnsafeTypesWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		path  string
		setup func(string) error
	}{
		{name: "symlink", setup: func(path string) error { return os.Symlink(target, path) }},
		{name: "fifo", setup: func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{name: "directory", setup: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "device", path: "/dev/null", setup: func(string) error { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if tc.path != "" {
				path = tc.path
			}
			if err := tc.setup(path); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := NewStore(path).ReadOnly()
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("unsafe type was accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("read-only journal inspection blocked")
			}
		})
	}
}
