package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProviderBinaries puts recording `codex` and `claude` stand-ins at the
// front of PATH and returns the file their invocations are appended to.
//
// Each stand-in writes one line — its own name followed by its argv — and
// exits 1, so a route that reaches for a provider still runs its real code up
// to the exec, but nothing it execs can install a package or start a daemon.
// Every other tool stays reachable through the rest of PATH.
func fakeProviderBinaries(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "invocations.log")
	for _, name := range []string{"codex", "claude"} {
		script := "#!/bin/sh\nprintf '%s' '" + name + "' >> '" + record + "'\n" +
			"for arg in \"$@\"; do printf ' %s' \"$arg\" >> '" + record + "'; done\n" +
			"printf '\\n' >> '" + record + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// providerInvocations reads the lines fakeProviderBinaries recorded so far.
func providerInvocations(t *testing.T, record string) []string {
	t.Helper()
	data, err := os.ReadFile(record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read provider invocation record: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// processesRunningFrom lists "pid exe" for every live process whose executable
// lives under root. ok is false when /proc cannot be scanned (e.g. macOS), in
// which case the caller has no evidence either way.
func processesRunningFrom(root string) (found []string, ok bool, err error) {
	if runtime.GOOS != "linux" {
		return nil, false, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false, nil
	}
	prefix := resolved + string(os.PathSeparator)
	for _, entry := range entries {
		pid := entry.Name()
		if pid == "" || strings.Trim(pid, "0123456789") != "" {
			continue
		}
		// Other users' processes and races with exiting ones are unreadable;
		// neither can be something this test started.
		exe, err := os.Readlink(filepath.Join("/proc", pid, "exe"))
		if err != nil {
			continue
		}
		// A daemon whose temp tree was already removed still reports its old
		// path, suffixed by the kernel.
		exe = strings.TrimSuffix(exe, " (deleted)")
		if strings.HasPrefix(exe, prefix) {
			found = append(found, pid+" "+exe)
		}
	}
	return found, true, nil
}

// assertNoProcessRunsFrom fails when any live process executes a binary under
// root. A provider that installed itself into a test HOME and daemonized
// (reparented to init) survives the test otherwise, invisibly.
//
// The Codex managed daemon is started synchronously by `daemon start`, so a
// leak is already visible when the route returns; the short poll only absorbs
// a process that is mid-exit.
func assertNoProcessRunsFrom(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		found, ok, err := processesRunningFrom(root)
		if err != nil {
			t.Fatalf("scan processes under %s: %v", root, err)
		}
		if !ok {
			t.Logf("process scan unavailable on %s; not checking for processes left under %s", runtime.GOOS, root)
			return
		}
		if len(found) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes still running from %s after the test:\n%s", root, strings.Join(found, "\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestProcessesRunningFromFindsAChildStartedUnderRoot is the positive control
// for the leak detector: a scan that never matches would make every caller
// pass vacuously.
func TestProcessesRunningFromFindsAChildStartedUnderRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process scan needs /proc")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	data, err := os.ReadFile(sleepPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	copied := filepath.Join(root, "bin", "sleep")
	if err := os.MkdirAll(filepath.Dir(copied), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o755); err != nil {
		t.Fatal(err)
	}

	if found, ok, err := processesRunningFrom(root); err != nil || !ok || len(found) != 0 {
		t.Fatalf("before start: found %v, ok %t, err %v; want an empty, successful scan", found, ok, err)
	}

	child := exec.Command(copied, "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})

	found, ok, err := processesRunningFrom(root)
	if err != nil || !ok {
		t.Fatalf("scan: ok %t, err %v", ok, err)
	}
	want := strconv.Itoa(child.Process.Pid) + " "
	if len(found) != 1 || !strings.HasPrefix(found[0], want) {
		t.Fatalf("found %v, want exactly the child pid %d", found, child.Process.Pid)
	}

	// Once the child is reaped the same root must scan clean again, which is
	// the state assertNoProcessRunsFrom accepts.
	_ = child.Process.Kill()
	_ = child.Wait()
	reaped = true
	if found, _, err := processesRunningFrom(root); err != nil || len(found) != 0 {
		t.Fatalf("after reap: found %v, err %v; want none", found, err)
	}
}
