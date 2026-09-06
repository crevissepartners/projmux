package app

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
)

func TestDetachedCrashArtifactPathUsesClosedRoleAndPIDVocabulary(t *testing.T) {
	stateDir := filepath.Join(string(os.PathSeparator), "tmp", "projmux-state")
	for _, test := range []struct {
		role detachedCrashRole
		pid  int
		want string
		ok   bool
	}{
		{role: detachedCrashRoleCodexBrokerWatch, pid: 41, want: "codex-broker-watch-41.sigquit.txt", ok: true},
		{role: detachedCrashRoleCodexBrokerServe, pid: 42, want: "codex-broker-serve-42.sigquit.txt", ok: true},
		{role: "../watch", pid: 43},
		{role: detachedCrashRoleCodexBrokerWatch, pid: 0},
		{role: detachedCrashRoleCodexBrokerServe, pid: -1},
	} {
		path, err := detachedCrashArtifactPath(stateDir, test.role, test.pid)
		if test.ok {
			if err != nil || filepath.Base(path) != test.want || filepath.Dir(path) != filepath.Join(stateDir, detachedCrashDirName) {
				t.Fatalf("artifactPath(%q, %d) = %q, %v", test.role, test.pid, path, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("artifactPath(%q, %d) accepted %q", test.role, test.pid, path)
		}
	}

	for _, unsafe := range []string{"", ".", "relative/state", string(os.PathSeparator), stateDir + string(os.PathSeparator), stateDir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "other"} {
		if _, err := detachedCrashArtifactPath(unsafe, detachedCrashRoleCodexBrokerWatch, 44); err == nil {
			t.Fatalf("unsafe state directory %q was accepted", unsafe)
		}
	}
}

func TestResolveDetachedCrashStateDirFollowsStandardStateHomeOnly(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	lookup := func(string) string { return "" }
	got, err := resolveDetachedCrashStateDir(lookup, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "projmux"); got != want {
		t.Fatalf("fallback state dir = %q, want %q", got, want)
	}
	stateHome := filepath.Join(root, "state")
	lookup = func(key string) string {
		if key == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	got, err = resolveDetachedCrashStateDir(lookup, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateHome, "projmux"); got != want {
		t.Fatalf("XDG state dir = %q, want %q", got, want)
	}
	got, err = resolveDetachedCrashStateDir(lookup, func() (string, error) {
		return "", errors.New("home unavailable")
	})
	if err != nil || got != filepath.Join(stateHome, "projmux") {
		t.Fatalf("absolute XDG state with unavailable home = %q, %v", got, err)
	}
	lookup = func(key string) string {
		if key == "XDG_STATE_HOME" {
			return "relative-state"
		}
		return ""
	}
	if _, err := resolveDetachedCrashStateDir(lookup, func() (string, error) { return home, nil }); err == nil {
		t.Fatal("relative XDG_STATE_HOME was accepted for crash artifacts")
	}
}

func TestDetachedCrashArtifactIsPrivateBoundedAndAtomicallyPublished(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := detachedCrashArtifactHeader(detachedCrashRoleCodexBrokerWatch, 71)
	stack := bytes.Repeat([]byte("goroutine 71 [running]:\nframe\n"), detachedCrashArtifactMaxBytes/16)
	artifact := formatDetachedCrashArtifact(header, stack, true)
	if len(artifact) != detachedCrashArtifactMaxBytes || !bytes.HasSuffix(artifact, detachedCrashTruncationMarker) {
		t.Fatalf("truncated artifact length=%d suffix=%t", len(artifact), bytes.HasSuffix(artifact, detachedCrashTruncationMarker))
	}
	if err := publishDetachedCrashArtifact(stateDir, detachedCrashRoleCodexBrokerWatch, 71, artifact); err != nil {
		t.Fatal(err)
	}
	target, err := detachedCrashArtifactPath(stateDir, detachedCrashRoleCodexBrokerWatch, 71)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		stateDir: 0o700, filepath.Dir(target): 0o700, target: 0o600,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%v, want %v", path, info.Mode().Perm(), want)
		}
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, artifact) {
		t.Fatalf("published artifact differs: len=%d err=%v", len(got), err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".sigquit-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic publish left temp files %q: %v", matches, err)
	}
	if err := publishDetachedCrashArtifact(stateDir, detachedCrashRoleCodexBrokerWatch, 71, []byte("replacement")); err == nil {
		t.Fatal("atomic no-clobber publish replaced an existing artifact")
	}
	again, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(again, artifact) {
		t.Fatal("failed no-clobber publish changed the existing artifact")
	}
}

func TestDetachedCrashArtifactRefusesSymlinkAndNonRegularTargets(t *testing.T) {
	t.Run("state directory symlink", func(t *testing.T) {
		root := t.TempDir()
		realState := filepath.Join(root, "real")
		if err := os.Mkdir(realState, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedState := filepath.Join(root, "state")
		if err := os.Symlink(realState, linkedState); err != nil {
			t.Fatal(err)
		}
		if err := publishDetachedCrashArtifact(linkedState, detachedCrashRoleCodexBrokerWatch, 81, []byte("stack")); err == nil {
			t.Fatal("symlink state directory was accepted")
		}
	})

	t.Run("artifact directory symlink", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "state")
		escape := filepath.Join(root, "escape")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(escape, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(escape, filepath.Join(stateDir, detachedCrashDirName)); err != nil {
			t.Fatal(err)
		}
		if err := publishDetachedCrashArtifact(stateDir, detachedCrashRoleCodexBrokerWatch, 82, []byte("stack")); err == nil {
			t.Fatal("symlink artifact directory was accepted")
		}
		entries, err := os.ReadDir(escape)
		if err != nil || len(entries) != 0 {
			t.Fatalf("symlink target mutated: entries=%v err=%v", entries, err)
		}
	})

	for _, test := range []struct {
		name string
		make func(string, string) error
	}{
		{name: "target symlink", make: func(target, victim string) error { return os.Symlink(victim, target) }},
		{name: "target directory", make: func(target, _ string) error { return os.Mkdir(target, 0o700) }},
		{name: "target regular file", make: func(target, _ string) error { return os.WriteFile(target, []byte("original"), 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			if err := ensureDetachedCrashDirectory(stateDir); err != nil {
				t.Fatal(err)
			}
			target, err := detachedCrashArtifactPath(stateDir, detachedCrashRoleCodexBrokerServe, 83)
			if err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.make(target, victim); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(target)
			if err := publishDetachedCrashArtifact(stateDir, detachedCrashRoleCodexBrokerServe, 83, []byte("replacement")); err == nil {
				t.Fatal("unsafe target was accepted")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("unsafe target changed: %v", err)
			}
			got, err := os.ReadFile(victim)
			if err != nil || string(got) != "victim" {
				t.Fatalf("victim changed to %q: %v", got, err)
			}
		})
	}
}

func TestDetachedCrashCaptureRequestsAllGoroutinesAndMarksTruncation(t *testing.T) {
	ready := make(chan struct{})
	release := make(chan struct{})
	go blockedDetachedCrashSentinel(ready, release)
	<-ready
	defer close(release)
	artifact := captureDetachedCrashStack(detachedCrashRoleCodexBrokerServe, 91)
	if len(artifact) > detachedCrashArtifactMaxBytes {
		t.Fatalf("capture length=%d exceeds %d", len(artifact), detachedCrashArtifactMaxBytes)
	}
	for _, want := range [][]byte{[]byte("role=codex-broker-serve"), []byte("pid=91"), []byte("scope=all-goroutines"), []byte("goroutine ")} {
		if !bytes.Contains(artifact, want) {
			t.Fatalf("capture lacks %q", want)
		}
	}
	if !bytes.Contains(artifact, []byte("blockedDetachedCrashSentinel")) || bytes.Count(artifact, []byte("goroutine ")) < 2 {
		t.Fatal("runtime.Stack did not include the blocked sentinel goroutine from all=true capture")
	}
	header := detachedCrashArtifactHeader(detachedCrashRoleCodexBrokerServe, 91)
	full := formatDetachedCrashArtifact(header, []byte("goroutine 1 [running]:\n"), false)
	if bytes.Contains(full, detachedCrashTruncationMarker) {
		t.Fatal("complete artifact was marked truncated")
	}
	truncated := formatDetachedCrashArtifact(header, bytes.Repeat([]byte("x"), detachedCrashArtifactMaxBytes*2), true)
	if len(truncated) != detachedCrashArtifactMaxBytes || !bytes.HasSuffix(truncated, detachedCrashTruncationMarker) {
		t.Fatal("oversized stack was not explicitly truncated at the byte bound")
	}
}

func TestDetachedCrashSinkNormalLifetimeCreatesNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	resolved := 0
	sink := startDetachedCrashSink(detachedCrashRoleCodexBrokerWatch, func() (string, error) {
		resolved++
		return stateDir, nil
	})
	sink.Close()
	time.Sleep(10 * time.Millisecond)
	if resolved != 0 {
		t.Fatalf("normal lifetime resolved state %d times", resolved)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal lifetime created state: %v", err)
	}
}

func TestDetachedCrashSinkPreservesQuietAndPrivateSurfaceBoundaries(t *testing.T) {
	wantHook := canonicalCodexHookRoute + aiHookPaneArgument + " >/dev/null 2>&1 || true"
	if codexHookCommand != wantHook || priorCodexHookCommand != canonicalCodexHookRoute+" >/dev/null 2>&1 || true" {
		t.Fatalf("Codex hook output/exit contract changed: current=%q prior=%q", codexHookCommand, priorCodexHookCommand)
	}
	for _, route := range cli.Routes() {
		if strings.Contains(route.Name, "sigquit") || strings.Contains(route.Name, "crash") {
			t.Fatalf("crash sink leaked into public route %q", route.Name)
		}
	}
	root := filepath.Join("..", "..")
	for _, test := range []struct {
		path      string
		forbidden []string
		required  string
	}{
		{path: filepath.Join(root, "internal", "app", "diagnostics_report.go"), forbidden: []string{"sigquit.txt", `"crash"`}},
		{path: filepath.Join(root, "internal", "app", "detached_crash_sink.go"), forbidden: []string{"PROJMUX_"}},
		{path: filepath.Join(root, "internal", "app", "supervise_child_unix.go"), required: "cmd.Stderr = os.Stderr"},
	} {
		raw, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range test.forbidden {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("%s contains forbidden public/support surface %q", test.path, forbidden)
			}
		}
		if test.required != "" && !bytes.Contains(raw, []byte(test.required)) {
			t.Fatalf("%s no longer contains %q", test.path, test.required)
		}
	}
}

func TestDetachedRoutesPersistActualSIGQUITWithStderrDiscarded(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("SIGQUIT artifact contract is supported on Linux and macOS")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "projmux")
	build := exec.Command("go", "build", "-o", binary, "./cmd/projmux")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build production binary: %v\n%s", err, output)
	}

	t.Run("broker serve", func(t *testing.T) {
		processRoot := newDetachedCrashProcessRoot(t, "broker")
		stateDomain := filepath.Join(processRoot, "state", "projmux")
		cmd := exec.Command(binary, "internal", "codex-broker", "serve", "--state-domain", stateDomain, "--idle-timeout", "30s")
		cmd.Env = detachedCrashSubprocessEnv(t, processRoot)
		stderr := openDetachedCrashDevNull(t)
		cmd.Stderr = stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stdout).ReadString('\n')
			ready <- line
		}()
		select {
		case line := <-ready:
			if strings.TrimSpace(line) != codexBrokerReadyLine {
				t.Fatalf("broker readiness = %q", line)
			}
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("broker did not report readiness")
		}
		assertDetachedCrashPreSignalState(t, processRoot)
		assertDetachedCrashSignalExit(t, cmd, processRoot, detachedCrashRoleCodexBrokerServe)
	})

	t.Run("broker watcher", func(t *testing.T) {
		processRoot := newDetachedCrashProcessRoot(t, "watcher")
		truePath, err := exec.LookPath("true")
		if err != nil {
			t.Fatal(err)
		}
		truePath, err = filepath.Abs(truePath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary,
			"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute,
			"--agent-uid", "agent-sigquit", "--pane-uid", "pane-sigquit", "--pane", "%991",
			"--generation", "generation-sigquit", "--thread", "thread-sigquit",
			"--state-domain", "state-sigquit", "--endpoint-generation", "endpoint-sigquit",
			"--endpoint-state", "current", "--endpoint-socket", filepath.Join(processRoot, "missing.sock"),
			"--tui-executable", truePath, "--tmux-socket-name", filepath.Base(processRoot),
		)
		cmd.Env = detachedCrashSubprocessEnv(t, processRoot)
		cmd.Stdout = io.Discard
		cmd.Stderr = openDetachedCrashDevNull(t)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// The exact route waits three seconds for its isolated, deliberately
		// absent Pane binding. Its sink is installed before that wait begins.
		time.Sleep(200 * time.Millisecond)
		assertDetachedCrashPreSignalState(t, processRoot)
		assertDetachedCrashSignalExit(t, cmd, processRoot, detachedCrashRoleCodexBrokerWatch)
	})
}

func blockedDetachedCrashSentinel(ready chan<- struct{}, release <-chan struct{}) {
	close(ready)
	<-release
}

func newDetachedCrashProcessRoot(t *testing.T, role string) string {
	t.Helper()
	base := os.TempDir()
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "pmxq-"+role+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func detachedCrashSubprocessEnv(t *testing.T, root string) []string {
	t.Helper()
	for _, dir := range []string{"home", "config", "state", "runtime", "tmp", "tmux"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	drop := map[string]bool{
		"HOME": true, "XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true,
		"TMPDIR": true, "TMUX": true, "TMUX_PANE": true, "TMUX_TMPDIR": true,
	}
	env := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			env = append(env, entry)
		}
	}
	return append(env,
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"),
		"TMPDIR="+filepath.Join(root, "tmp"),
		"TMUX_TMPDIR="+filepath.Join(root, "tmux"),
	)
}

func openDetachedCrashDevNull(t *testing.T) *os.File {
	t.Helper()
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func assertDetachedCrashPreSignalState(t *testing.T, root string) {
	t.Helper()
	crashDir := filepath.Join(root, "state", "projmux", detachedCrashDirName)
	if _, err := os.Lstat(crashDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal startup created crash artifact state: %v", err)
	}
}

func assertDetachedCrashSignalExit(t *testing.T, cmd *exec.Cmd, root string, role detachedCrashRole) {
	t.Helper()
	pid := cmd.Process.Pid
	if err := cmd.Process.Signal(syscall.SIGQUIT); err != nil {
		t.Fatalf("SIGQUIT pid %d: %v", pid, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != detachedCrashExitCode {
			t.Fatalf("SIGQUIT exit = %v, want code %d", err, detachedCrashExitCode)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("SIGQUIT pid %d did not exit", pid)
	}
	stateDir := filepath.Join(root, "state", "projmux")
	path, err := detachedCrashArtifactPath(stateDir, role, pid)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SIGQUIT artifact %s: %v", filepath.Base(path), err)
	}
	for _, want := range []string{"role=" + string(role), "pid=" + strconv.Itoa(pid), "scope=all-goroutines", "goroutine "} {
		if !bytes.Contains(artifact, []byte(want)) {
			t.Fatalf("SIGQUIT artifact lacks %q", want)
		}
	}
	if len(artifact) > detachedCrashArtifactMaxBytes {
		t.Fatalf("SIGQUIT artifact length=%d exceeds bound", len(artifact))
	}
	for target, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatalf("lstat %s: %v", target, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%v, want %v", target, info.Mode().Perm(), want)
		}
	}
}

func TestDetachedCrashArtifactHeaderRetainsOnlyClosedMetadata(t *testing.T) {
	header := string(detachedCrashArtifactHeader(detachedCrashRoleCodexBrokerWatch, 101))
	for _, forbidden := range []string{"thread", "agent", "pane", "socket", "cwd", "prompt", "payload"} {
		if strings.Contains(header, forbidden) {
			t.Fatalf("artifact header contains %q: %q", forbidden, header)
		}
	}
}
