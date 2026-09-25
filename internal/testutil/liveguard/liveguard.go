// Package liveguard runs a test binary behind the shared live-machine and
// provider guards. It is test-support only: TestMain functions call it, and no
// product code imports it. It imports only the standard library so that the
// internal tests of any package, including the Registry store and the tmux
// runners it protects, can use it without an import cycle.
//
// A developer or agent usually runs `make test` from inside a managed Pane.
// That shell carries the live tmux client (TMUX, TMUX_PANE), the Pane anchor
// the runtime mutation routes read before TMUX_PANE, the activation handles a
// provider Pane receives, and a HOME and XDG layout whose state directory holds
// the live Registry. A test that forgets to inject a path resolves those
// defaults: it can reach the live tmux server and read or write the live
// Registry. A route that reaches for a provider resolves `codex` or `claude`
// through PATH, so a test that forgets its own stand-in runs the real CLI: it
// can install a package, start a managed daemon, or touch a live session. CI
// has neither the live Pane nor either CLI, so such a test passes there
// without anyone noticing.
//
// RunTests removes that inheritance for the whole test binary before any test
// runs, puts fail-closed stand-ins for both CLIs first on PATH, and fails the
// package when a test still wrote to a default per-user location (the Registry
// or any other state, config, cache, or data file) or executed a stand-in.
// RequireActive pins from inside the package that TestMain did so.
package liveguard

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const (
	// rootEnv hands the private root to every process the test binary
	// starts. Many tests re-execute the binary as a helper process that ends
	// in os.Exit inside m.Run, which skips the guard's deferred cleanup; a
	// helper that made its own root would leave it behind. A process that
	// inherits a root joins it instead (both guards: PATH is inherited with
	// the stand-ins on it), and the process that made the root removes it and
	// audits what every descendant wrote there.
	rootEnv = "PMX_TEST_LIVE_MACHINE_GUARD_ROOT"

	// rootPrefix names every private root. The owner's parent pid follows it
	// (see guardPrivateRoot), so an owner can find the roots its own children
	// made without inheriting rootEnv.
	rootPrefix = "pmx-live-guard-"

	// ownerLockName is the lock file at the top of a root its maker owns. The
	// owner holds an exclusive flock on it for its whole life, so a later
	// guarded owner can tell a root whose maker was killed (the lock is free)
	// from a live one (guardReclaimKilledRoots). ownerLockTempPattern is the
	// name it is written under before it is renamed into place locked.
	ownerLockName        = "owner.lock"
	ownerLockTempPattern = ".owner.lock-*"

	// providerDirName is the stand-in directory under the private root, and
	// providerRecordName the log each stand-in appends its argv to.
	providerDirName    = "provider-bin"
	providerRecordName = "invocations.log"

	appName = "projmux"

	registryFailLine = "FAIL: a test reached the default projmux Registry; inject a Registry path"
	otherFailLine    = "FAIL: a test wrote to a default per-user location; inject a path"
	providerFailLine = "FAIL: a test ran the real codex or claude from PATH; give the test its own stand-in (fakeProviderBinaries) or inject the provider seam"
)

// inheritedEnv names the variables through which a process inherits a live
// tmux client or a managed Pane's identity. The guard unsets them; a test that
// needs one sets its own value. The last four equal internal/app's
// runtimeMutationAnchorPaneEnv, internalActivationPaneUIDEnv,
// internalActivationGenerationEnv and internalClaudeRegistryPathEnv, which a
// test in internal/app keeps in step; this package cannot import them.
var inheritedEnv = []string{
	"TMUX",
	"TMUX_PANE",
	"__PROJMUX_RUNTIME_ANCHOR_PANE",
	"PMX_INTERNAL_ACTIVATION_PANE_UID",
	"PMX_INTERNAL_ACTIVATION_GENERATION",
	"PMX_INTERNAL_CLAUDE_REGISTRY_PATH",
}

// privateEnv maps each variable that locates per-user state to its directory
// under the guard's private root. XDG_STATE_HOME and HOME are the two ways the
// default Registry path is resolved; TMUX_TMPDIR is where a socket-name tmux
// call puts its server.
var privateEnv = []struct {
	key  string
	leaf string
}{
	{"HOME", "home"},
	{"XDG_CONFIG_HOME", "config"},
	{"XDG_STATE_HOME", "state"},
	{"XDG_CACHE_HOME", "cache"},
	{"XDG_DATA_HOME", "data"},
	{"TMUX_TMPDIR", "tmux"},
}

// toolchainEnv are the go command locations that default to HOME or
// XDG_CACHE_HOME. Tests that build the binary with `go build` would otherwise
// start from an empty module cache under the private HOME.
var toolchainEnv = []string{"GOPATH", "GOCACHE", "GOMODCACHE", "GOENV"}

// auditSkipPaths are the paths under the private root that the leak audit
// does not report.
var auditSkipPaths = []string{
	// The go command's telemetry counters live under os.UserConfigDir, which
	// follows the private XDG_CONFIG_HOME and has no variable of its own to
	// pin the way toolchainEnv does.
	filepath.Join("config", "go", "telemetry"),
	// The guard's own provider stand-ins and their invocation log; the
	// provider audit reads the log instead.
	providerDirName,
}

// The running guard's state, read by RequireActive. activeRoot is "" when the
// package runs without the guard; activeProviderDir is "" when the provider
// guard stepped aside.
var (
	activeRoot        string
	activeProviderDir string
	activeSteppedAway bool
)

// Option configures RunTests and RunGuarded.
type Option func(*guardOptions)

type guardOptions struct {
	providerOptIn []string
}

// ProviderOptIn names the variables a package's installed tests read to enable
// an intentional real-provider path. Those tests resolve the real CLI through
// PATH, so when any of them is non-empty the provider guard steps aside. The
// live-machine guard never steps aside.
func ProviderOptIn(names ...string) Option {
	return func(options *guardOptions) {
		options.providerOptIn = append(options.providerOptIn, names...)
	}
}

// InheritedEnv returns the live routing variables the guard unsets.
func InheritedEnv() []string { return slices.Clone(inheritedEnv) }

// RunTests runs m behind the guards and returns the package's exit code:
//
//	func TestMain(m *testing.M) { os.Exit(liveguard.RunTests(m)) }
func RunTests(m *testing.M, opts ...Option) int {
	return RunGuarded(m.Run, opts...)
}

// RunGuarded is RunTests for a TestMain that has setup of its own to run
// behind the guards before m.Run: the live-machine guard is outermost, the
// provider guard inside it, and run inside both.
func RunGuarded(run func() int, opts ...Option) int {
	var options guardOptions
	for _, opt := range opts {
		opt(&options)
	}

	root, owned, lock, err := guardPrivateRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live machine guard: create private root: %v\n", err)
		return 1
	}
	if owned {
		// The lock stays referenced, and so held, until the root is gone.
		defer func() {
			_ = os.RemoveAll(root)
			_ = lock.Close()
		}()
		guardReclaimKilledRoots("/tmp", root, os.Stderr)
	}

	// Resolve the go command's own locations while HOME is still the
	// caller's, so they stay the caller's after HOME moves.
	guardPinToolchainEnv()
	for _, key := range inheritedEnv {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: unset %s: %v\n", key, err)
			return 1
		}
	}
	for _, entry := range privateEnv {
		dir := filepath.Join(root, entry.leaf)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: create %s: %v\n", entry.key, err)
			return 1
		}
		if err := os.Setenv(entry.key, dir); err != nil {
			fmt.Fprintf(os.Stderr, "live machine guard: set %s: %v\n", entry.key, err)
			return 1
		}
	}
	if err := os.Setenv(rootEnv, root); err != nil {
		fmt.Fprintf(os.Stderr, "live machine guard: set %s: %v\n", rootEnv, err)
		return 1
	}
	activeRoot = root
	defer func() { activeRoot, activeProviderDir, activeSteppedAway = "", "", false }()

	providerDir := filepath.Join(root, providerDirName)
	switch {
	case guardOptedIn(options.providerOptIn):
		activeSteppedAway = true
	case !owned:
		// A joined process follows the owner: the stand-ins are already on
		// the PATH it inherited and record into the owner's log. When the
		// owner stepped aside there are none, and neither is there a guard
		// here.
		if _, err := os.Stat(filepath.Join(providerDir, "codex")); err == nil {
			activeProviderDir = providerDir
		} else {
			activeSteppedAway = true
		}
	default:
		if err := guardInstallProviderStandIns(providerDir); err != nil {
			fmt.Fprintf(os.Stderr, "provider guard: %v\n", err)
			return 1
		}
		path, hadPath := os.LookupEnv("PATH")
		newPath := providerDir
		if path != "" {
			newPath += string(os.PathListSeparator) + path
		}
		if err := os.Setenv("PATH", newPath); err != nil {
			fmt.Fprintf(os.Stderr, "provider guard: set PATH: %v\n", err)
			return 1
		}
		defer func() {
			if hadPath {
				_ = os.Setenv("PATH", path)
			} else {
				_ = os.Unsetenv("PATH")
			}
		}()
		activeProviderDir = providerDir
	}

	code := run()
	if !owned {
		return code
	}
	if guardAuditRoot(root, "") {
		code = max(code, 1)
	}
	if guardSweepHelperRoots() {
		code = max(code, 1)
	}
	return code
}

// guardOptedIn reports whether an opt-in gate is set, and says so.
func guardOptedIn(names []string) bool {
	for _, key := range names {
		if os.Getenv(key) != "" {
			fmt.Fprintf(os.Stderr, "provider guard: stepped aside; %s enables an intentional real-provider path\n", key)
			return true
		}
	}
	return false
}

// guardPrivateRoot returns the root this process runs behind and whether it
// owns it. A process started by a guarded test binary joins the root it
// inherited while that root still exists; any other process makes a new one,
// named after its parent so that parent can sweep it (guardSweepHelperRoots).
// A new root is made under /tmp, whatever TMPDIR is: it holds TMUX_TMPDIR and
// XDG_STATE_HOME, so the tmux and broker sockets under it must stay within the
// unix socket path bound (104 bytes on macOS, 108 on Linux).
//
// A new root carries the owner lock (guardLockRoot), returned to the caller,
// which must keep it open until the root is removed: an unreferenced *os.File
// is closed by its finalizer, which would free a live owner's lock. The lock
// is nil for a joined root.
func guardPrivateRoot() (string, bool, *os.File, error) {
	if inherited := os.Getenv(rootEnv); inherited != "" {
		if info, err := os.Stat(inherited); err == nil && info.IsDir() { // #nosec G703 -- read-only check of the root a guarded parent test binary exported; nothing is opened or written through it here.
			return inherited, false, nil, nil
		}
	}
	root, err := os.MkdirTemp("/tmp", fmt.Sprintf("%sp%d-", rootPrefix, os.Getppid()))
	if err != nil {
		return "", false, nil, err
	}
	lock, err := guardLockRoot(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", false, nil, err
	}
	return root, true, lock, nil
}

// guardLockRoot creates the owner lock at the top of root and returns it held.
// The file is locked and given this process's pid under a temporary name, then
// renamed into place, so whoever sees ownerLockName sees it already locked and
// can read who holds it. Go opens it close-on-exec, so no child inherits the
// lock and keeps it held after this process dies.
func guardLockRoot(root string) (*os.File, error) {
	lock, err := os.CreateTemp(root, ownerLockTempPattern)
	if err != nil {
		return nil, fmt.Errorf("create owner lock: %w", err)
	}
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_, err = fmt.Fprintf(lock, "%d\n", os.Getpid())
	}
	if err == nil {
		err = os.Rename(lock.Name(), filepath.Join(root, ownerLockName))
	}
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("take owner lock: %w", err)
	}
	return lock, nil
}

// guardReclaimKilledRoots removes the roots under dir whose owner was killed
// before its deferred cleanup ran (SIGKILL, a tool timeout's SIGTERM, a
// -test.timeout panic), and returns how many it removed. An owner holds its
// lock until it dies, so a root whose lock this process can take has no live
// owner. A root without the lock file, the old name format included, is never
// touched: it predates the lock, or its maker died before taking it, and it
// belongs to whoever left it. A dead run's writes are not audited.
//
// Two lock-proven cases stay out of the reclaim, keeping each audit where it
// belongs: this process is a dropped-environment helper when its parent holds
// a lock, and then reclaims nothing and prints nothing, because its stderr is
// the test's that started it; and a free root named after a live owner is that
// owner's dropped helper's, which its guardSweepHelperRoots audits and removes.
//
// dir is "/tmp" in production and a private directory in tests. own is the
// caller's own root. Errors are ignored: a reclaim never fails the run.
func guardReclaimKilledRoots(dir, own string, stderr io.Writer) int {
	found, _ := filepath.Glob(filepath.Join(dir, rootPrefix+"p*"))
	type deadRoot struct {
		path string
		lock *os.File
	}
	var dead []deadRoot
	live := map[int]bool{}
	for _, root := range found {
		if root == own {
			continue
		}
		if info, err := os.Lstat(root); err != nil || !info.IsDir() {
			continue
		}
		lockPath := filepath.Join(root, ownerLockName)
		if info, err := os.Lstat(lockPath); err != nil || !info.Mode().IsRegular() {
			continue
		}
		lock, err := os.OpenFile(lockPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the owner lock of a private root found under the scanned directory; opened read-only without following a symlink.
		if err != nil {
			continue
		}
		switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
		case err == nil:
			dead = append(dead, deadRoot{root, lock})
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			data, _ := io.ReadAll(io.LimitReader(lock, 32))
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				live[pid] = true
			}
		}
		_ = lock.Close()
	}

	helper := live[os.Getppid()]
	reclaimed := 0
	for _, root := range dead {
		if !helper && !live[guardRootMakerParent(root.path)] {
			if err := os.RemoveAll(root.path); err == nil {
				reclaimed++
			}
		}
		_ = root.lock.Close()
	}
	if reclaimed > 0 {
		fmt.Fprintf(stderr, "live machine guard: reclaimed %d private root(s) a killed guarded run left under %s\n", reclaimed, dir)
	}
	return reclaimed
}

// guardRootMakerParent returns the parent pid a root is named after
// (rootPrefix + "p<pid>-"), or 0 when the name does not carry one.
func guardRootMakerParent(root string) int {
	rest, ok := strings.CutPrefix(filepath.Base(root), rootPrefix+"p")
	if !ok {
		return 0
	}
	digits, _, _ := strings.Cut(rest, "-")
	pid, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return pid
}

// guardPinToolchainEnv exports the go command's current locations for the
// variables the caller left unset. Without a go command it does nothing; the
// tests that need one fail the way they would anyway.
func guardPinToolchainEnv() {
	var missing []string
	for _, key := range toolchainEnv {
		if _, ok := os.LookupEnv(key); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return
	}
	out, err := exec.Command("go", append([]string{"env"}, missing...)...).Output() // #nosec G204 -- fixed go executable; argv is fixed variable names.
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for i := 0; scanner.Scan() && i < len(missing); i++ {
		if value := strings.TrimSpace(scanner.Text()); value != "" {
			_ = os.Setenv(missing[i], value)
		}
	}
}

// guardInstallProviderStandIns writes fail-closed codex and claude stand-ins
// into dir. Each appends its argv to the record beside it and exits 1.
func guardInstallProviderStandIns(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create stand-in dir: %w", err)
	}
	record := filepath.Join(dir, providerRecordName)
	for _, name := range []string{"codex", "claude"} {
		// One printf of the whole line keeps concurrent appends whole.
		script := "#!/bin/sh\nline='" + name + "'\n" +
			"for arg in \"$@\"; do line=\"$line $arg\"; done\n" +
			"printf '%s\\n' \"$line\" >> '" + record + "'\n" +
			"echo 'provider guard: refused to run the real " + name + "' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { // #nosec G306 -- the stand-in must be executable.
			return fmt.Errorf("write %s stand-in: %w", name, err)
		}
	}
	return nil
}

// guardAuditRoot reports every file a test left under root and every provider
// stand-in a test ran, and returns whether it found any. The guard itself
// creates only directories and its stand-ins, so any other file means a test
// resolved a default per-user location. Reaching the Registry directory at
// all, even to create a lock beside a missing registry, is reported on its own
// because it is the live machine's shared state. A non-empty origin names the
// helper root the paths are relative to. The owner lock at the top of root is
// the guard's own and is not reported.
func guardAuditRoot(root, origin string) bool {
	failed := false
	report := func(message string, lines []string) {
		if len(lines) == 0 {
			return
		}
		failed = true
		fmt.Fprintln(os.Stderr, message)
		if origin != "" {
			fmt.Fprintf(os.Stderr, "  (in %s)\n", origin)
		}
		for _, line := range lines {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, providerDirName, providerRecordName)) // #nosec G304 -- the guard's own record under its private root.
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "provider guard: read record: %v\n", err)
		failed = true
	}
	if calls := strings.TrimRight(string(data), "\n"); calls != "" {
		report(providerFailLine, strings.Split(calls, "\n"))
	}

	var registry, other []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			other = append(other, fmt.Sprintf("unreadable guard path: %v", err))
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			if slices.Contains(auditSkipPaths, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ownerLockName {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "metadata" && filepath.Base(filepath.Dir(filepath.Dir(path))) == appName {
			registry = append(registry, rel)
		} else {
			other = append(other, rel)
		}
		return nil
	})
	report(registryFailLine, registry)
	report(otherFailLine, other)
	return failed
}

// guardSweepHelperRoots audits and removes the roots this process's own
// children made, and returns whether any audit failed. A helper that inherits
// rootEnv joins the owner's root, but a test that re-executes the binary with
// an environment of its own (cmd.Env = []string{...}) drops rootEnv and TMPDIR
// with it: the helper cannot join, makes a root of its own, and then ends in
// os.Exit inside m.Run or is killed, so it never removes it. guardPrivateRoot
// makes such a root under /tmp, named after its parent, so the owner finds it
// there. The owner also scans its own temp directory, where a helper built
// before roots moved to /tmp put one. Only the process that made a root, or
// this owner of its maker, removes it; a joined process never does.
func guardSweepHelperRoots() bool {
	failed := false
	pattern := fmt.Sprintf("%sp%d-*", rootPrefix, os.Getpid())
	var dirs []string
	for _, dir := range []string{os.TempDir(), "/tmp"} {
		if dir = filepath.Clean(dir); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	for _, dir := range dirs {
		found, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, helperRoot := range found {
			if info, err := os.Lstat(helperRoot); err != nil || !info.IsDir() {
				continue
			}
			if guardAuditRoot(helperRoot, "helper root "+helperRoot) {
				failed = true
			}
			_ = os.RemoveAll(helperRoot)
		}
	}
	return failed
}

// RequireActive fails t unless TestMain runs the package behind RunTests:
// nothing inherited routes to a live tmux client or Pane, every per-user
// location, the default Registry path included, is private, helper processes
// can join the root, and, unless an opt-in gate made it step aside, both
// provider CLIs resolve to the guard's stand-ins.
func RequireActive(t testing.TB) {
	t.Helper()
	for _, key := range inheritedEnv {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s=%q is set; the guard must unset inherited live routing", key, value)
		}
	}
	root := activeRoot
	if root == "" {
		t.Fatal("TestMain did not run the package behind liveguard.RunTests")
	}
	for _, entry := range privateEnv {
		if got, want := os.Getenv(entry.key), filepath.Join(root, entry.leaf); got != want {
			t.Errorf("%s = %q, want the private %q", entry.key, got, want)
		}
	}
	if got := os.Getenv(rootEnv); got != root {
		t.Errorf("%s = %q, want the private root %q so helper processes join it", rootEnv, got, root)
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	if registry := filepath.Join(stateHome, appName, "metadata", "registry.json"); !strings.HasPrefix(registry, root+string(filepath.Separator)) {
		t.Errorf("default Registry path = %q, want it under the private root %q", registry, root)
	}

	if activeSteppedAway {
		t.Log("provider guard stepped aside for an opt-in real-provider run")
		return
	}
	if activeProviderDir == "" {
		t.Fatal("TestMain did not run the package behind liveguard.RunTests: no provider stand-ins")
	}
	for _, name := range []string{"codex", "claude"} {
		resolved, err := exec.LookPath(name)
		if err != nil {
			t.Errorf("look up %s: %v", name, err)
			continue
		}
		if filepath.Dir(resolved) != activeProviderDir {
			t.Errorf("%s resolves to %q, want the guard stand-in under %q", name, resolved, activeProviderDir)
		}
	}
}
