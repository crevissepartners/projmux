// Package socketreuse measures whether the running host can give a Unix socket
// recreated at the same path the identity of the socket it replaced. It is
// test-support only: no product code imports it.
//
// Several tests stage a replacement by closing and unlinking a private socket
// and binding a new one at the same path in the same instant, then expect the
// product's identity check to tell the two apart. On Linux before 6.13 the
// change time has clock-tick granularity and the freed inode is reused at
// once, so both sockets can share device, inode, and change time: the
// replacement is indistinguishable, and the test would fail for a reason the
// product cannot see. Such a test measures the host first and skips only when
// the collapse is observed.
package socketreuse

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Iterations is how many same-path replacements a test measures before it
// trusts the host. Each costs two bind/unlink pairs, so the probe is cheap.
const Iterations = 200

// Measurement counts the replacements whose identity equaled the original's.
type Measurement struct {
	Iterations int
	Collisions int
}

// MeasureSameTickReplacement stages iterations same-path replacements in a fresh directory under
// parent and compares each pair through identity. The caller passes the
// product's own inspection function, so the comparison covers exactly the
// fields the product compares. Each socket is chmod 0600 before inspection, as
// the owned sockets are.
func MeasureSameTickReplacement[T comparable](parent string, iterations int, identity func(path string) (T, error)) (Measurement, error) {
	dir, err := os.MkdirTemp(parent, "pmx-sockreuse-")
	if err != nil {
		return Measurement{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "probe.sock")
	bind := func() (T, error) {
		var zero T
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			return zero, err
		}
		listener.SetUnlinkOnClose(false)
		defer func() {
			_ = listener.Close()
			_ = os.Remove(path)
		}()
		if err := os.Chmod(path, 0o600); err != nil {
			return zero, err
		}
		return identity(path)
	}
	measured := Measurement{Iterations: iterations}
	for range iterations {
		original, err := bind()
		if err != nil {
			return Measurement{}, err
		}
		replacement, err := bind()
		if err != nil {
			return Measurement{}, err
		}
		if replacement == original {
			measured.Collisions++
		}
	}
	return measured, nil
}

// SkipIfReplacementCollapses skips t when a same-path replacement under parent
// can share the original's identity. Otherwise the test runs as written.
func SkipIfReplacementCollapses[T comparable](t testing.TB, parent string, identity func(path string) (T, error)) {
	t.Helper()
	measured, err := MeasureSameTickReplacement(parent, Iterations, identity)
	if err != nil {
		t.Fatalf("measure same-path socket replacement: %v", err)
	}
	if measured.Collisions > 0 {
		kernel := "(unknown)"
		var name unix.Utsname
		if unix.Uname(&name) == nil {
			kernel = unix.ByteSliceToString(name.Release[:])
		}
		t.Skipf("kernel %s gave a same-path socket replacement the identity of the socket it replaced in %d/%d tries, so the replacement this test stages cannot be told apart",
			kernel, measured.Collisions, measured.Iterations)
	}
}
