package liveguard

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestLiveMachineGuardReclaimsTheRootOfAKilledRun covers a guarded run killed
// before its deferred cleanup: its root stays behind, and the next guarded
// owner removes it.
//
// Both fixtures run under /bin/sh without exec. The killed fixture's root is
// then named after its shell, not after this live test binary, whose lock
// would rightly keep it for this binary's own sweep; and the later fixture's
// parent is not a live owner, so it reclaims instead of stepping aside as a
// helper. It is not parallel so this package's own fixtures do not reclaim
// the root first; another guarded run on the machine still may, which the
// test tolerates.
func TestLiveMachineGuardReclaimsTheRootOfAKilledRun(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", `"$0" -test.run='^$'; exit $?`, os.Args[0]) // #nosec G204 -- re-executes this test binary as a fixed guard fixture.
	command.Env = append(os.Environ(), rootEnv+"=", "TMPDIR="+t.TempDir(), liveMachineGuardChildEnv+"=block")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(stderr)
	var seen strings.Builder
	root, pid := "", 0
	for root == "" || pid == 0 {
		line, err := reader.ReadString('\n')
		seen.WriteString(line)
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), fixtureRootMarker); ok {
			root = value
		}
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), fixturePidMarker); ok {
			pid, _ = strconv.Atoi(value)
		}
		if err != nil {
			break
		}
	}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drained)
	}()
	// Closing stdin ends a fixture that is still blocked; either way the
	// shell and the fixture are reaped before the test ends.
	reaped := false
	reap := func() error {
		reaped = true
		_ = stdin.Close()
		<-drained
		return command.Wait()
	}
	t.Cleanup(func() {
		if !reaped {
			_ = reap()
		}
	})
	if root == "" || pid == 0 {
		t.Fatalf("fixture did not report its root and pid:\n%s", seen.String())
	}
	if want := rootPrefix + "p" + strconv.Itoa(command.Process.Pid) + "-"; filepath.Dir(root) != "/tmp" || !strings.HasPrefix(filepath.Base(root), want) {
		t.Fatalf("fixture root = %q, want one under /tmp starting %q", root, want)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if info, err := os.Lstat(filepath.Join(root, ownerLockName)); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("fixture root %s has no owner lock (err %v)", root, err)
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill fixture %d: %v", pid, err)
	}
	var exitErr *exec.ExitError
	if err := reap(); !errors.As(err, &exitErr) {
		t.Fatalf("killed fixture's shell exit: %v, want a non-zero exit", err)
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Logf("another guarded run already reclaimed %s", root)
		return
	} else if err != nil {
		t.Fatal(err)
	}

	later := runLiveMachineGuardChildUnderShell(t, []string{liveMachineGuardChildEnv + "=clean"})
	if later.code != 0 {
		t.Fatalf("later guard owner exit = %d, want 0; stderr:\n%s", later.code, later.stderr)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("a later guarded owner left the killed run's root %s (stat err %v); stderr:\n%s", root, err, later.stderr)
	}
}

// TestLiveMachineGuardReclaimLeavesARootWhoseOwnerHoldsItsLock pins that a
// root whose owner is alive is never reclaimed.
func TestLiveMachineGuardReclaimLeavesARootWhoseOwnerHoldsItsLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root := makeLockedRoot(t, dir, rootPrefix+"p1-live")
	var out bytes.Buffer
	if n := guardReclaimKilledRoots(dir, "", &out); n != 0 || out.Len() != 0 {
		t.Errorf("reclaim = %d with output %q, want 0 and nothing", n, out.String())
	}
	requireDir(t, root)
}

// TestLiveMachineGuardReclaimLeavesRootsWithoutAnOwnerLock pins that a root
// without the lock file is never reclaimed: an old-format name (even with a
// free lock inside), a new-format root with no lock, and one whose maker died
// before renaming its lock into place.
func TestLiveMachineGuardReclaimLeavesRootsWithoutAnOwnerLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oldFormat := makeFreeRoot(t, dir, rootPrefix+"123")
	noLock := filepath.Join(dir, rootPrefix+"p1-nolock")
	unrenamed := filepath.Join(dir, rootPrefix+"p1-unrenamed")
	for _, root := range []string{noLock, unrenamed} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(unrenamed, ".owner.lock-1"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if n := guardReclaimKilledRoots(dir, "", &out); n != 0 || out.Len() != 0 {
		t.Errorf("reclaim = %d with output %q, want 0 and nothing", n, out.String())
	}
	for _, root := range []string{oldFormat, noLock, unrenamed} {
		requireDir(t, root)
	}
}

// TestLiveMachineGuardReclaimReportsItsCountOnOneLine pins the reclaim's one
// stderr line and that it removes every root with a free lock but the
// caller's own.
func TestLiveMachineGuardReclaimReportsItsCountOnOneLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	killed := []string{makeFreeRoot(t, dir, rootPrefix+"p1-a"), makeFreeRoot(t, dir, rootPrefix+"p1-b")}
	own := makeFreeRoot(t, dir, rootPrefix+"p1-own")
	var out bytes.Buffer
	if n := guardReclaimKilledRoots(dir, own, &out); n != 2 {
		t.Errorf("reclaim = %d, want 2", n)
	}
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") || !strings.Contains(got, "reclaimed 2 private root(s)") {
		t.Errorf("reclaim output = %q, want one line with the count 2", got)
	}
	for _, root := range killed {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("reclaim left the killed root %s (stat err %v)", root, err)
		}
	}
	requireDir(t, own)
}

// TestLiveMachineGuardReclaimLeavesAFreeRootNamedAfterALiveOwner pins that a
// free root named after a live owner is left to that owner's own sweep, which
// audits it.
func TestLiveMachineGuardReclaimLeavesAFreeRootNamedAfterALiveOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeLockedRoot(t, dir, rootPrefix+"p1-owner")
	helperRoot := makeFreeRoot(t, dir, rootPrefix+"p"+strconv.Itoa(os.Getpid())+"-helper")
	var out bytes.Buffer
	if n := guardReclaimKilledRoots(dir, "", &out); n != 0 || out.Len() != 0 {
		t.Errorf("reclaim = %d with output %q, want 0 and nothing", n, out.String())
	}
	requireDir(t, helperRoot)
}

// TestLiveMachineGuardHelperOfALiveOwnerReclaimsNothing pins that a process
// whose parent holds an owner lock is that owner's helper: it reclaims
// nothing and prints nothing into the stderr of the test that started it.
func TestLiveMachineGuardHelperOfALiveOwnerReclaimsNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	parentRoot := makeLockedRoot(t, dir, rootPrefix+"p1-parent")
	// The lock stays held through its own descriptor; only the pid it names
	// changes, to this process's parent.
	if err := os.WriteFile(filepath.Join(parentRoot, ownerLockName), []byte(strconv.Itoa(os.Getppid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	killed := makeFreeRoot(t, dir, rootPrefix+"p1-killed")
	var out bytes.Buffer
	if n := guardReclaimKilledRoots(dir, "", &out); n != 0 || out.Len() != 0 {
		t.Errorf("reclaim = %d with output %q, want 0 and nothing", n, out.String())
	}
	requireDir(t, killed)
}

// TestLiveMachineGuardOwnerLockIsHeldOnceVisible pins that the owner lock is
// never visible unlocked: the moment guardPrivateRoot returns, another open of
// it cannot take the lock. It names its holder, and the audit does not report
// it.
//
// It is not parallel: t.Setenv.
func TestLiveMachineGuardOwnerLockIsHeldOnceVisible(t *testing.T) {
	t.Setenv(rootEnv, "")

	root, owned, lock, err := guardPrivateRoot()
	if err != nil {
		t.Fatalf("guardPrivateRoot() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
		_ = lock.Close()
	})
	if !owned || lock == nil {
		t.Fatalf("guardPrivateRoot() owned = %v, lock = %v, want an owned root with its lock", owned, lock)
	}

	path := filepath.Join(root, ownerLockName)
	other, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the owner lock of the root this test just made.
	if err != nil {
		t.Fatalf("open owner lock: %v", err)
	}
	defer func() { _ = other.Close() }()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Errorf("flock of a visible owner lock = %v, want EWOULDBLOCK", err)
	}
	if data, err := io.ReadAll(other); err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("owner lock names %q (err %v), want this process %d", data, err, os.Getpid())
	}
	if left, _ := filepath.Glob(filepath.Join(root, ownerLockTempPattern)); len(left) != 0 {
		t.Errorf("owner lock left its temporary name behind: %v", left)
	}
	if guardAuditRoot(root, "") {
		t.Errorf("the audit reported the guard's own owner lock in %s", root)
	}
}

// makeLockedRoot makes a root under dir the way an owner does, with its lock
// held until the test ends, and returns it.
func makeLockedRoot(t *testing.T, dir, name string) string {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := guardLockRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return root
}

// makeFreeRoot makes a root under dir whose owner has died: the lock file is
// in place, names this process, and is free. It is written without ever being
// locked, so it is free by construction: a lock taken and then released could
// still be held by a parallel test's fork, which carries the descriptor until
// its child execs, and the reclaim would read that as a live owner.
func makeFreeRoot(t *testing.T, dir, name string) string {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ownerLockName), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// requireDir fails t unless root is still a directory.
func requireDir(t *testing.T, root string) {
	t.Helper()
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Errorf("reclaim removed %s (err %v), want it kept", root, err)
	}
}
