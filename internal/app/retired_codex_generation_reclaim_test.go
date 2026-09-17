package app

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const codexReclaimLinePrefix = "reclaimed retired Codex generation files: "

// bundleDigest and hostKey build names with the exact shapes the retired pool
// wrote: a 64 hex character content address and a 32 hex character private
// host key.
func bundleDigest(fill string) string { return "sha256-" + strings.Repeat(fill, 64) }
func hostKey(fill string) string      { return strings.Repeat(fill, 32) }

type codexReclaimFixture struct {
	home     string
	stateDir string
	env      map[string]string
}

func newCodexReclaimFixture(t *testing.T) *codexReclaimFixture {
	t.Helper()
	home := t.TempDir()
	return &codexReclaimFixture{
		home:     home,
		stateDir: filepath.Join(home, ".local", "state", "projmux"),
		env:      map[string]string{"HOME": home},
	}
}

// useShortStateHome moves the state directory under a short /tmp root so a
// real listener fits in the platform socket bound.
func (f *codexReclaimFixture) useShortStateHome(t *testing.T) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pmxg")
	if err != nil {
		t.Skipf("short state home unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f.env["XDG_STATE_HOME"] = root
	f.stateDir = filepath.Join(root, "projmux")
}

func (f *codexReclaimFixture) command() *tmuxCommand {
	return &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return f.home, nil },
		lookupEnv:  func(name string) string { return f.env[name] },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
	}
}

// apply runs a real `tmux apply` (the route `config apply` forwards to) and
// returns the Codex reclaim lines it printed.
func (f *codexReclaimFixture) apply(t *testing.T) []string {
	t.Helper()
	args := []string{"--config", filepath.Join(f.home, "generated", "tmux.conf"), "--no-reload"}
	var stdout, stderr bytes.Buffer
	if err := f.command().runApply(args, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var lines []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.HasPrefix(line, codexReclaimLinePrefix) {
			lines = append(lines, line)
		}
	}
	if strings.Contains(stderr.String(), "generation") {
		t.Fatalf("reclaim wrote to stderr: %q", stderr.String())
	}
	return lines
}

func assertSingleCodexReclaimLine(t *testing.T, lines []string, want string) {
	t.Helper()
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("reclaim lines = %q, want exactly [%q]", lines, want)
	}
}

// codexReclaimListen occupies path the way a retired private host did.
func codexReclaimListen(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	if len(path) > managedCodexSocketPathMaxBytes {
		t.Skipf("socket path is %d bytes, over the %d byte bound", len(path), managedCodexSocketPathMaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	unix, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener type = %T, want *net.UnixListener", listener)
	}
	t.Cleanup(func() { _ = unix.Close() })
	return unix
}

// codexReclaimDeadSocket leaves a socket file behind that nothing listens on,
// the way a private host that died without cleaning up would have.
func codexReclaimDeadSocket(t *testing.T, path string) {
	t.Helper()
	listener := codexReclaimListen(t, path)
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode().Type() != os.ModeSocket {
		t.Fatalf("dead socket = %v, %v; want a socket file", info, err)
	}
}

func TestConfigApplyCodexReclaimRemovesTargetsAndKeepsUnexpectedEntries(t *testing.T) {
	f := newCodexReclaimFixture(t)
	gens := filepath.Join(f.stateDir, retiredCodexGenerationsDirName)
	bundles := filepath.Join(gens, "bundles")
	bundle := filepath.Join(bundles, bundleDigest("a"))
	qualification := filepath.Join(gens, "qualification")
	hosts := filepath.Join(f.stateDir, "g")
	liveHost := filepath.Join(hosts, hostKey("0"))
	busyHost := filepath.Join(hosts, hostKey("1"))

	reclaimWrite(t, filepath.Join(gens, "rolling-upgrade.json"), "{}")
	reclaimWrite(t, filepath.Join(gens, "rolling-upgrade.json.flock"), "")
	reclaimWrite(t, filepath.Join(qualification, "0.153.2_0.153.4.json"), "{}")
	reclaimWrite(t, filepath.Join(bundle, "manifest.json"), "{}")
	reclaimWrite(t, filepath.Join(bundle, "codex-path"), "/usr/bin/codex\n")
	reclaimWrite(t, filepath.Join(bundle, "bin", "codex"), "ELF")
	reclaimWrite(t, filepath.Join(bundle, "codex-resources", "r.json"), "{}")
	reclaimWrite(t, filepath.Join(liveHost, ".projmux-launch-codex-0.153.0.json"), "{}")
	reclaimWrite(t, filepath.Join(liveHost, ".projmux-launch-codex-0.153.0.json.guard"), "")

	// Entries whose names do not have a target shape.
	reclaimMkdir(t, filepath.Join(bundles, "sha256-deadbeef"))
	reclaimWrite(t, filepath.Join(gens, "notes.txt"), "notes")
	reclaimWrite(t, filepath.Join(qualification, "notes.txt"), "notes")
	reclaimWrite(t, filepath.Join(busyHost, "stray.txt"), "stray")
	reclaimMkdir(t, filepath.Join(hosts, "keep-me"))

	// Files the reclamation must never touch.
	untouched := []string{
		filepath.Join(f.stateDir, "registry.json"),
		filepath.Join(f.stateDir, "usage", "snapshots.json"),
		filepath.Join(f.home, ".config", "projmux", "config.toml"),
		filepath.Join(f.home, "outside", "keep.json"),
	}
	for _, path := range untouched {
		reclaimWrite(t, path, "keep\n")
	}

	lines := f.apply(t)

	// Traversal order: codex-generations (bundles, notes.txt, qualification,
	// rolling-upgrade.json, .flock), then g.
	kept := []string{
		filepath.Join(bundles, "sha256-deadbeef"),
		filepath.Join(gens, "notes.txt"),
		filepath.Join(qualification, "notes.txt"),
		filepath.Join(busyHost, "stray.txt"),
		filepath.Join(hosts, "keep-me"),
	}
	// 4 bundle files + 1 qualification + journal + lock + 2 launch files. The
	// emptied directories are removed without being counted.
	assertSingleCodexReclaimLine(t, lines, codexReclaimLinePrefix+"removed 9 files; kept 5 ("+strings.Join(kept, ", ")+")")

	for _, path := range []string{
		bundle,
		filepath.Join(gens, "rolling-upgrade.json"),
		filepath.Join(gens, "rolling-upgrade.json.flock"),
		filepath.Join(qualification, "0.153.2_0.153.4.json"),
		liveHost,
	} {
		if reclaimExists(t, path) {
			t.Fatalf("%s still exists", path)
		}
	}
	for _, path := range append(slices.Clone(kept), gens, bundles, qualification, hosts, busyHost) {
		if !reclaimExists(t, path) {
			t.Fatalf("%s was removed", path)
		}
	}
	for _, path := range untouched {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "keep\n" {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, "keep\n")
		}
	}

	// Only the kept entries remain, so a rerun prints nothing and changes
	// nothing: they were reported once, next to the removal.
	before := reclaimTree(t, f.stateDir)
	if lines := f.apply(t); len(lines) != 0 {
		t.Fatalf("rerun printed %q, want nothing", lines)
	}
	if after := reclaimTree(t, f.stateDir); !slices.Equal(before, after) {
		t.Fatalf("rerun changed the tree:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestConfigApplyCodexReclaimSecondRunIsQuietNoOp(t *testing.T) {
	f := newCodexReclaimFixture(t)
	gens := filepath.Join(f.stateDir, retiredCodexGenerationsDirName)
	hosts := filepath.Join(f.stateDir, "g")
	reclaimWrite(t, filepath.Join(gens, "rolling-upgrade.json"), "{}")
	reclaimWrite(t, filepath.Join(gens, "qualification", "a_b.json"), "{}")
	reclaimWrite(t, filepath.Join(gens, "bundles", bundleDigest("c"), "manifest.json"), "{}")
	reclaimWrite(t, filepath.Join(hosts, hostKey("d"), ".projmux-launch-codex-1.2.3.json"), "{}")
	reclaimWrite(t, filepath.Join(f.stateDir, "registry.json"), "{}")

	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 4 files")

	before := reclaimTree(t, f.stateDir)
	if second := f.apply(t); len(second) != 0 {
		t.Fatalf("second run printed %q, want nothing", second)
	}
	if after := reclaimTree(t, f.stateDir); !slices.Equal(before, after) {
		t.Fatalf("second run changed the tree:\nbefore=%q\nafter=%q", before, after)
	}
	for _, path := range []string{gens, hosts} {
		if reclaimExists(t, path) {
			t.Fatalf("emptied %s was left behind", path)
		}
	}
	if !reclaimExists(t, filepath.Join(f.stateDir, "registry.json")) {
		t.Fatal("the state directory itself was reclaimed")
	}
}

func TestConfigApplyCodexReclaimWithNothingToReclaimPrintsNothing(t *testing.T) {
	f := newCodexReclaimFixture(t)
	reclaimWrite(t, filepath.Join(f.stateDir, "registry.json"), "{}")
	before := reclaimTree(t, f.stateDir)

	if lines := f.apply(t); len(lines) != 0 {
		t.Fatalf("reclaim printed %q, want nothing", lines)
	}
	if after := reclaimTree(t, f.stateDir); !slices.Equal(before, after) {
		t.Fatalf("state tree changed:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestConfigApplyCodexReclaimDoesNotFollowSymlinks(t *testing.T) {
	f := newCodexReclaimFixture(t)
	elsewhere := filepath.Join(f.home, "elsewhere")
	reclaimWrite(t, filepath.Join(elsewhere, "gens", "rolling-upgrade.json"), "{}")
	reclaimWrite(t, filepath.Join(elsewhere, "gens", "bundles", bundleDigest("a"), "manifest.json"), "{}")
	reclaimWrite(t, filepath.Join(elsewhere, "hosts", hostKey("0"), ".projmux-launch-codex-1.json"), "{}")
	reclaimWrite(t, filepath.Join(elsewhere, "qual", "a_b.json"), "{}")
	reclaimWrite(t, filepath.Join(elsewhere, "keep.json"), "keep\n")
	before := reclaimTree(t, elsewhere)

	// First state home: both roots are symlinks, so nothing is even opened.
	linkedState := filepath.Join(f.home, "linked-state")
	f.env["XDG_STATE_HOME"] = linkedState
	reclaimSymlink(t, filepath.Join(elsewhere, "gens"), filepath.Join(linkedState, "projmux", retiredCodexGenerationsDirName))
	reclaimSymlink(t, filepath.Join(elsewhere, "hosts"), filepath.Join(linkedState, "projmux", "g"))

	if lines := f.apply(t); len(lines) != 0 {
		t.Fatalf("symlinked roots printed %q, want nothing", lines)
	}

	// Second state home: the roots are real, and every target-shaped child is
	// a symlink except one journal lock and one bundle tree.
	realState := filepath.Join(f.home, "real-state")
	f.env["XDG_STATE_HOME"] = realState
	gens := filepath.Join(realState, "projmux", retiredCodexGenerationsDirName)
	bundles := filepath.Join(gens, "bundles")
	hosts := filepath.Join(realState, "projmux", "g")
	reclaimWrite(t, filepath.Join(gens, "rolling-upgrade.json.flock"), "")
	reclaimWrite(t, filepath.Join(bundles, bundleDigest("a"), "manifest.json"), "{}")
	reclaimSymlink(t, filepath.Join(elsewhere, "keep.json"), filepath.Join(bundles, bundleDigest("a"), "link.json"))
	reclaimSymlink(t, filepath.Join(elsewhere, "gens", "bundles", bundleDigest("a")), filepath.Join(bundles, bundleDigest("b")))
	reclaimSymlink(t, filepath.Join(elsewhere, "qual"), filepath.Join(gens, "qualification"))
	reclaimSymlink(t, filepath.Join(elsewhere, "gens", "rolling-upgrade.json"), filepath.Join(gens, "rolling-upgrade.json"))
	reclaimSymlink(t, filepath.Join(elsewhere, "hosts", hostKey("0")), filepath.Join(hosts, hostKey("0")))
	reclaimSymlink(t, filepath.Join(elsewhere, "hosts", hostKey("0"), ".projmux-launch-codex-1.json"),
		filepath.Join(hosts, hostKey("1"), ".projmux-launch-codex-1.json"))

	kept := []string{
		filepath.Join(bundles, bundleDigest("b")),
		filepath.Join(gens, "qualification"),
		filepath.Join(gens, "rolling-upgrade.json"),
		filepath.Join(hosts, hostKey("0")),
		filepath.Join(hosts, hostKey("1"), ".projmux-launch-codex-1.json"),
	}
	// manifest.json and the symlink beside it inside the real bundle tree,
	// plus the real journal lock. The symlink is unlinked, not followed.
	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 3 files; kept 5 ("+strings.Join(kept, ", ")+")")

	if after := reclaimTree(t, elsewhere); !slices.Equal(before, after) {
		t.Fatalf("symlink targets changed:\nbefore=%q\nafter=%q", before, after)
	}
	links := append(slices.Clone(kept),
		filepath.Join(linkedState, "projmux", retiredCodexGenerationsDirName),
		filepath.Join(linkedState, "projmux", "g"),
	)
	for _, link := range links {
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a symlink: %v", link, err)
		}
	}
}

func TestConfigApplyCodexReclaimKeepsAPrivateHostThatStillAnswers(t *testing.T) {
	f := newCodexReclaimFixture(t)
	f.useShortStateHome(t)
	hosts := filepath.Join(f.stateDir, "g")
	running := filepath.Join(hosts, hostKey("0"))
	stopped := filepath.Join(hosts, hostKey("1"))
	listener := codexReclaimListen(t, filepath.Join(running, "s"))
	reclaimWrite(t, filepath.Join(running, ".projmux-launch-codex-0.153.0.json"), "{}")
	codexReclaimDeadSocket(t, filepath.Join(stopped, "s"))
	reclaimWrite(t, filepath.Join(stopped, ".projmux-launch-codex-0.153.0.json"), "{}")

	// The stopped host loses its launch intent and its stale socket; the
	// running one is reported and left whole.
	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 2 files; still running 1 ("+running+")")
	if reclaimExists(t, stopped) {
		t.Fatalf("%s survived with no listener", stopped)
	}
	for _, path := range []string{running, filepath.Join(running, "s"), filepath.Join(running, ".projmux-launch-codex-0.153.0.json")} {
		if !reclaimExists(t, path) {
			t.Fatalf("%s was removed while the host still answered", path)
		}
	}
	if !dialRetiredCodexSocket(filepath.Join(running, "s")) {
		t.Fatal("the running host stopped answering")
	}

	// A rerun keeps reporting it and still removes nothing.
	before := reclaimTree(t, f.stateDir)
	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 0 files; still running 1 ("+running+")")
	if after := reclaimTree(t, f.stateDir); !slices.Equal(before, after) {
		t.Fatalf("rerun changed the tree:\nbefore=%q\nafter=%q", before, after)
	}

	// Once the host is gone, the next apply converges on the same rules.
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 2 files")
	if reclaimExists(t, hosts) {
		t.Fatalf("%s was left behind after the last host went away", hosts)
	}
}

func TestCodexReclaimTreeKeepsABundleWhoseNestedSocketAnswers(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmxb")
	if err != nil {
		t.Skipf("short root unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bundle := filepath.Join(root, bundleDigest("a"))
	reclaimWrite(t, filepath.Join(bundle, "manifest.json"), "{}")
	socket := filepath.Join(bundle, "bin", "s")
	codexReclaimListen(t, socket)

	var r retiredCodexGenerationReclaim
	if r.removeTree(bundle) {
		t.Fatal("removeTree reported a tree with an answering socket as gone")
	}
	if r.removed != 0 || !slices.Equal(r.live, []string{bundle}) || len(r.failed) != 0 {
		t.Fatalf("removed=%d live=%q failed=%q; want only the live tree", r.removed, r.live, r.failed)
	}
	if !reclaimExists(t, filepath.Join(bundle, "manifest.json")) {
		t.Fatal("a file under the live tree was removed")
	}
}

func TestConfigApplyCodexReclaimFailureDoesNotFailApplyAndRerunConverges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newCodexReclaimFixture(t)
	gens := filepath.Join(f.stateDir, retiredCodexGenerationsDirName)
	qualification := filepath.Join(gens, "qualification")
	reclaimWrite(t, filepath.Join(qualification, "a_b.json"), "{}")
	reclaimWrite(t, filepath.Join(gens, "rolling-upgrade.json"), "{}")
	if err := os.Chmod(qualification, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(qualification, 0o755) })

	lines := f.apply(t)
	assertSingleCodexReclaimLine(t, lines, codexReclaimLinePrefix+"removed 1 file; failed 1 ("+filepath.Join(qualification, "a_b.json")+": permission denied)")
	if !reclaimExists(t, filepath.Join(qualification, "a_b.json")) {
		t.Fatal("a_b.json vanished despite the failure")
	}
	// The rest of apply still ran.
	if !reclaimExists(t, filepath.Join(f.home, "generated", "tmux.conf")) {
		t.Fatal("apply did not write the generated config after a reclaim failure")
	}

	if err := os.Chmod(qualification, 0o755); err != nil {
		t.Fatal(err)
	}
	assertSingleCodexReclaimLine(t, f.apply(t), codexReclaimLinePrefix+"removed 1 file")
	if reclaimExists(t, gens) {
		t.Fatal("rerun did not converge")
	}
	if lines := f.apply(t); len(lines) != 0 {
		t.Fatalf("third run printed %q, want nothing", lines)
	}
}

func TestConfigApplyCodexReclaimSkipsWithoutInjectedHome(t *testing.T) {
	var stdout bytes.Buffer
	(&tmuxCommand{lookupEnv: func(string) string { return "" }}).reclaimRetiredCodexGenerationFiles(&stdout)
	(&tmuxCommand{homeDir: func() (string, error) { return t.TempDir(), nil }}).reclaimRetiredCodexGenerationFiles(&stdout)
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}

func TestRetiredCodexNameShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
		got  bool
	}{
		{bundleDigest("a"), true, retiredCodexBundleDirName(bundleDigest("a"))},
		{"sha256-" + strings.Repeat("A", 64), false, retiredCodexBundleDirName("sha256-" + strings.Repeat("A", 64))},
		{"sha256-deadbeef", false, retiredCodexBundleDirName("sha256-deadbeef")},
		{"sha512-" + strings.Repeat("a", 64), false, retiredCodexBundleDirName("sha512-" + strings.Repeat("a", 64))},
		{hostKey("0"), true, retiredCodexPrivateHostDirName(hostKey("0"))},
		{hostKey("g"), false, retiredCodexPrivateHostDirName(hostKey("g"))},
		{strings.Repeat("0", 31), false, retiredCodexPrivateHostDirName(strings.Repeat("0", 31))},
		{".projmux-launch-codex-0.153.0.json", true, retiredCodexLaunchFileName(".projmux-launch-codex-0.153.0.json")},
		{".projmux-launch-codex-0.153.0.json.guard", true, retiredCodexLaunchFileName(".projmux-launch-codex-0.153.0.json.guard")},
		{".projmux-launch-.json", false, retiredCodexLaunchFileName(".projmux-launch-.json")},
		{".projmux-launch-a/b.json", false, retiredCodexLaunchFileName(".projmux-launch-a/b.json")},
		{".projmux-launch-codex-1.txt", false, retiredCodexLaunchFileName(".projmux-launch-codex-1.txt")},
		{"projmux-launch-codex-1.json", false, retiredCodexLaunchFileName("projmux-launch-codex-1.json")},
		{".projmux-launch-" + strings.Repeat("a", retiredCodexLaunchTokenMax+1) + ".json", false,
			retiredCodexLaunchFileName(".projmux-launch-" + strings.Repeat("a", retiredCodexLaunchTokenMax+1) + ".json")},
	} {
		if tc.got != tc.want {
			t.Errorf("%q = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
