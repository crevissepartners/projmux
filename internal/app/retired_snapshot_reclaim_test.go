package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const reclaimLinePrefix = "reclaimed retired Project snapshot files: "

type reclaimFixture struct {
	home      string
	stateDir  string
	configDir string
	env       map[string]string
}

func newReclaimFixture(t *testing.T) *reclaimFixture {
	t.Helper()
	home := t.TempDir()
	return &reclaimFixture{
		home:      home,
		stateDir:  filepath.Join(home, ".local", "state", "projmux"),
		configDir: filepath.Join(home, ".config", "projmux"),
		env:       map[string]string{"HOME": home},
	}
}

func (f *reclaimFixture) command() *tmuxCommand {
	return &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return f.home, nil },
		lookupEnv:  func(name string) string { return f.env[name] },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
	}
}

// apply runs a real `tmux apply` (the route `config apply` forwards to) and
// returns the reclaim lines it printed. The generated config goes outside the
// reclaimed trees so it never shows up in a tree snapshot.
func (f *reclaimFixture) apply(t *testing.T, extra ...string) []string {
	t.Helper()
	args := append([]string{"--config", filepath.Join(f.home, "generated", "tmux.conf")}, extra...)
	var stdout, stderr bytes.Buffer
	if err := f.command().runApply(args, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var lines []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.HasPrefix(line, reclaimLinePrefix) {
			lines = append(lines, line)
		}
	}
	if strings.Contains(stderr.String(), "snapshot") {
		t.Fatalf("reclaim wrote to stderr: %q", stderr.String())
	}
	return lines
}

func reclaimWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reclaimMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func reclaimSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func reclaimExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("lstat %s: %v", path, err)
	return false
}

// reclaimTree snapshots every entry under root with its kind, size and
// modification time, so "changes nothing" is a byte-free but exact claim.
func reclaimTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, fmt.Sprintf("%s|%s|%d|%d", rel, info.Mode(), info.ModTime().UnixNano(), info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSingleReclaimLine(t *testing.T, lines []string, want string) {
	t.Helper()
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("reclaim lines = %q, want exactly [%q]", lines, want)
	}
}

func TestConfigApplyReclaimRemovesTargetsAndKeepsUnexpectedEntries(t *testing.T) {
	f := newReclaimFixture(t)
	sessions := filepath.Join(f.stateDir, "sessions")
	projects := filepath.Join(f.configDir, "sessionstate-projects")
	outside := filepath.Join(f.home, "outside", "keep.json")
	reclaimWrite(t, outside, "outside\n")

	reclaimWrite(t, filepath.Join(sessions, "alpha.json"), "{}")
	reclaimWrite(t, filepath.Join(sessions, "beta.json"), "{}")
	reclaimSymlink(t, outside, filepath.Join(sessions, "link.json"))
	reclaimWrite(t, filepath.Join(sessions, "notes.txt"), "notes")
	reclaimMkdir(t, filepath.Join(sessions, "nested.json"))
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-autosave"), "on\n")
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-autosave-interval"), "5m\n")
	reclaimWrite(t, filepath.Join(projects, "one", "autosave"), "off\n")
	reclaimWrite(t, filepath.Join(projects, "two", "autosave"), "on\n")

	// Files the reclamation must never touch.
	untouched := []string{
		filepath.Join(f.configDir, "config.toml"),
		filepath.Join(f.stateDir, "registry.json"),
		filepath.Join(f.stateDir, "usage", "snapshots.json"),
		filepath.Join(f.stateDir, "codex-generations", "g.json"),
		filepath.Join(f.stateDir, "backups", "b.json"),
		filepath.Join(f.home, "project", ".projmux", "layouts", "l.json"),
	}
	for _, path := range untouched {
		reclaimWrite(t, path, "keep\n")
	}

	lines := f.apply(t, "--no-reload")

	kept := []string{
		filepath.Join(sessions, "link.json"),
		filepath.Join(sessions, "nested.json"),
		filepath.Join(sessions, "notes.txt"),
	}
	// 2 session files + 2 global files + 2 Project autosave files. The three
	// emptied directories are removed without being counted.
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 6 files; kept 3 ("+strings.Join(kept, ", ")+")")

	for _, path := range []string{
		filepath.Join(sessions, "alpha.json"),
		filepath.Join(sessions, "beta.json"),
		filepath.Join(f.configDir, "sessionstate-autosave"),
		filepath.Join(f.configDir, "sessionstate-autosave-interval"),
		projects,
	} {
		if reclaimExists(t, path) {
			t.Fatalf("%s still exists", path)
		}
	}
	for _, path := range append(kept, sessions) {
		if !reclaimExists(t, path) {
			t.Fatalf("%s was removed", path)
		}
	}
	if target, err := os.Readlink(filepath.Join(sessions, "link.json")); err != nil || target != outside {
		t.Fatalf("symlink = %q, %v; want it pointing at %s", target, err, outside)
	}
	for _, path := range append(untouched, outside) {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want := "keep\n"
		if path == outside {
			want = "outside\n"
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	// Only the kept entries remain, so a rerun prints nothing and changes
	// nothing: they were reported once, next to the removal.
	before := reclaimTree(t, f.home)
	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("rerun printed %q, want nothing", lines)
	}
	if after := reclaimTree(t, f.home); !slices.Equal(before, after) {
		t.Fatalf("rerun changed the tree:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestConfigApplyReclaimSecondRunIsQuietNoOp(t *testing.T) {
	f := newReclaimFixture(t)
	sessions := filepath.Join(f.stateDir, "sessions")
	reclaimWrite(t, filepath.Join(sessions, "alpha.json"), "{}")
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-autosave"), "on\n")
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-projects", "one", "autosave"), "on\n")
	reclaimWrite(t, filepath.Join(f.configDir, retiredClosedStartupFileName), "off\n")

	first := f.apply(t, "--no-reload")
	// alpha.json, autosave, one/autosave; the emptied dirs are not counted. The
	// retired startup setting's file is reclaimed by its own step, not here.
	assertSingleReclaimLine(t, first, reclaimLinePrefix+"removed 3 files")
	if reclaimExists(t, filepath.Join(f.configDir, retiredClosedStartupFileName)) {
		t.Fatal("the retired closed-Project startup file survived the first apply")
	}

	before := reclaimTree(t, f.home)
	second := f.apply(t, "--no-reload")
	if len(second) != 0 {
		t.Fatalf("second run printed %q, want nothing", second)
	}
	if after := reclaimTree(t, f.home); !slices.Equal(before, after) {
		t.Fatalf("second run changed the tree:\nbefore=%q\nafter=%q", before, after)
	}
	if reclaimExists(t, filepath.Join(f.stateDir, "sessions")) {
		t.Fatal("empty sessions dir was left behind")
	}
}

func TestConfigApplyReclaimWithNothingToReclaimPrintsNothing(t *testing.T) {
	f := newReclaimFixture(t)
	reclaimWrite(t, filepath.Join(f.configDir, "unrelated-note"), "keep\n")
	reclaimWrite(t, filepath.Join(f.stateDir, "registry.json"), "{}")
	before := reclaimTree(t, filepath.Join(f.home, ".config"))
	stateBefore := reclaimTree(t, filepath.Join(f.home, ".local"))

	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("reclaim printed %q, want nothing", lines)
	}
	if after := reclaimTree(t, filepath.Join(f.home, ".config")); !slices.Equal(before, after) {
		t.Fatalf("config tree changed:\nbefore=%q\nafter=%q", before, after)
	}
	if after := reclaimTree(t, filepath.Join(f.home, ".local")); !slices.Equal(stateBefore, after) {
		t.Fatalf("state tree changed:\nbefore=%q\nafter=%q", stateBefore, after)
	}
}

func TestConfigApplyReclaimWithOnlyUnexpectedEntriesPrintsNothing(t *testing.T) {
	f := newReclaimFixture(t)
	outside := filepath.Join(f.home, "outside", "keep.json")
	reclaimWrite(t, outside, "outside\n")
	sessions := filepath.Join(f.stateDir, "sessions")
	projects := filepath.Join(f.configDir, "sessionstate-projects")
	reclaimWrite(t, filepath.Join(sessions, "notes.txt"), "notes")
	reclaimSymlink(t, outside, filepath.Join(sessions, "link.json"))
	reclaimMkdir(t, filepath.Join(sessions, "nested.json"))
	reclaimSymlink(t, outside, filepath.Join(f.configDir, "sessionstate-autosave"))
	reclaimWrite(t, filepath.Join(projects, "one", "other"), "x")
	reclaimWrite(t, filepath.Join(projects, "stray-file"), "x")
	// The first apply also writes generated/tmux.conf under home, so compare
	// the reclaimed trees and the symlink target only.
	roots := []string{filepath.Join(f.home, ".local"), filepath.Join(f.home, ".config"), filepath.Dir(outside)}
	var before [][]string
	for _, root := range roots {
		before = append(before, reclaimTree(t, root))
	}

	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("first run printed %q, want nothing", lines)
	}
	for i, root := range roots {
		if after := reclaimTree(t, root); !slices.Equal(before[i], after) {
			t.Fatalf("%s changed:\nbefore=%q\nafter=%q", root, before[i], after)
		}
	}
}

func TestConfigApplyReclaimRemovesAlreadyEmptyDirectoriesSilently(t *testing.T) {
	f := newReclaimFixture(t)
	sessions := filepath.Join(f.stateDir, "sessions")
	projects := filepath.Join(f.configDir, "sessionstate-projects")
	reclaimMkdir(t, sessions)
	reclaimMkdir(t, filepath.Join(projects, "one"))

	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("apply printed %q, want nothing", lines)
	}
	for _, path := range []string{sessions, projects} {
		if reclaimExists(t, path) {
			t.Fatalf("%s still exists", path)
		}
	}
	if !reclaimExists(t, f.stateDir) || !reclaimExists(t, f.configDir) {
		t.Fatal("a parent projmux directory was removed")
	}
}

func TestConfigApplyReclaimRemovesOnlyDirectoriesThatEndUpEmpty(t *testing.T) {
	f := newReclaimFixture(t)
	projects := filepath.Join(f.configDir, "sessionstate-projects")
	reclaimWrite(t, filepath.Join(projects, "empty-after", "autosave"), "on\n")
	reclaimWrite(t, filepath.Join(projects, "busy", "autosave"), "on\n")
	reclaimWrite(t, filepath.Join(projects, "busy", "other"), "x")
	reclaimMkdir(t, filepath.Join(projects, "already-empty"))
	reclaimWrite(t, filepath.Join(f.stateDir, "sessions", "a.json"), "{}")
	reclaimWrite(t, filepath.Join(f.stateDir, "sessions", "keep.txt"), "x")

	lines := f.apply(t, "--no-reload")
	kept := []string{
		filepath.Join(f.stateDir, "sessions", "keep.txt"),
		filepath.Join(projects, "busy", "other"),
	}
	// a.json, empty-after/autosave, busy/autosave
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 3 files; kept 2 ("+strings.Join(kept, ", ")+")")

	for _, path := range []string{filepath.Join(projects, "empty-after"), filepath.Join(projects, "already-empty"), filepath.Join(projects, "busy", "autosave")} {
		if reclaimExists(t, path) {
			t.Fatalf("%s still exists", path)
		}
	}
	for _, path := range append(kept, projects, filepath.Join(projects, "busy"), filepath.Join(f.stateDir, "sessions")) {
		if !reclaimExists(t, path) {
			t.Fatalf("%s was removed", path)
		}
	}
}

func TestConfigApplyReclaimDoesNotDescendSymlinkedDirectories(t *testing.T) {
	f := newReclaimFixture(t)
	realSessions := filepath.Join(f.home, "elsewhere", "sessions")
	realProject := filepath.Join(f.home, "elsewhere", "project")
	realProjects := filepath.Join(f.home, "elsewhere", "projects")
	reclaimWrite(t, filepath.Join(realSessions, "a.json"), "{}")
	reclaimWrite(t, filepath.Join(realProject, "autosave"), "on\n")
	reclaimWrite(t, filepath.Join(realProjects, "p", "autosave"), "on\n")
	sessions := filepath.Join(f.stateDir, "sessions")
	reclaimSymlink(t, realSessions, sessions)
	linkedProject := filepath.Join(f.configDir, "sessionstate-projects", "linked")
	reclaimSymlink(t, realProject, linkedProject)
	autosaveLink := filepath.Join(f.configDir, "sessionstate-autosave")
	reclaimSymlink(t, filepath.Join(realProject, "autosave"), autosaveLink)
	// A second config home whose sessionstate-projects is itself a symlink.
	altConfig := filepath.Join(f.home, "alt-config")
	reclaimSymlink(t, realProjects, filepath.Join(altConfig, "projmux", "sessionstate-projects"))
	// One real target per config home, so each run prints its kept list.
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-autosave-interval"), "5m\n")
	reclaimWrite(t, filepath.Join(altConfig, "projmux", "sessionstate-autosave-interval"), "5m\n")
	before := reclaimTree(t, filepath.Join(f.home, "elsewhere"))

	lines := f.apply(t, "--no-reload")
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 1 file; kept 3 ("+strings.Join([]string{sessions, autosaveLink, linkedProject}, ", ")+")")
	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("rerun with only symlinks left printed %q, want nothing", lines)
	}

	f.env["XDG_CONFIG_HOME"] = altConfig
	lines = f.apply(t, "--no-reload")
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 1 file; kept 2 ("+strings.Join([]string{sessions, filepath.Join(altConfig, "projmux", "sessionstate-projects")}, ", ")+")")

	if after := reclaimTree(t, filepath.Join(f.home, "elsewhere")); !slices.Equal(before, after) {
		t.Fatalf("symlink targets changed:\nbefore=%q\nafter=%q", before, after)
	}
	for _, link := range []string{sessions, linkedProject, autosaveLink} {
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a symlink: %v", link, err)
		}
	}
}

func TestConfigApplyReclaimFailureDoesNotFailApplyAndRerunConverges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newReclaimFixture(t)
	sessions := filepath.Join(f.stateDir, "sessions")
	reclaimWrite(t, filepath.Join(sessions, "a.json"), "{}")
	reclaimWrite(t, filepath.Join(f.configDir, "sessionstate-autosave"), "on\n")
	if err := os.Chmod(sessions, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o755) })

	lines := f.apply(t, "--no-reload")
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 1 file; failed 1 ("+filepath.Join(sessions, "a.json")+": permission denied)")
	if !reclaimExists(t, filepath.Join(sessions, "a.json")) {
		t.Fatal("a.json vanished despite the failure")
	}
	// The rest of apply still ran.
	if !reclaimExists(t, filepath.Join(f.home, "generated", "tmux.conf")) {
		t.Fatal("apply did not write the generated config after a reclaim failure")
	}

	if err := os.Chmod(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	lines = f.apply(t, "--no-reload")
	// Only a.json is left to remove; the emptied sessions dir goes silently.
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 1 file")
	if reclaimExists(t, sessions) || reclaimExists(t, filepath.Join(f.configDir, "sessionstate-autosave")) {
		t.Fatal("rerun did not converge")
	}
	if lines := f.apply(t, "--no-reload"); len(lines) != 0 {
		t.Fatalf("third run printed %q, want nothing", lines)
	}
}

func TestConfigApplyReclaimHonorsXDGOverridesWithoutNoReload(t *testing.T) {
	f := newReclaimFixture(t)
	stateHome := filepath.Join(f.home, "xdg-state")
	configHome := filepath.Join(f.home, "xdg-config")
	f.env["XDG_STATE_HOME"] = stateHome
	f.env["XDG_CONFIG_HOME"] = configHome
	// Files under the HOME fallbacks must stay: the overrides replace them.
	defaultSession := filepath.Join(f.stateDir, "sessions", "a.json")
	defaultAutosave := filepath.Join(f.configDir, "sessionstate-autosave")
	reclaimWrite(t, defaultSession, "{}")
	reclaimWrite(t, defaultAutosave, "on\n")
	reclaimWrite(t, filepath.Join(stateHome, "projmux", "sessions", "b.json"), "{}")
	reclaimWrite(t, filepath.Join(configHome, "projmux", "sessionstate-autosave-interval"), "5m\n")

	// No --no-reload and no runner: apply takes the "no tmux runner" branch,
	// and reclamation still runs once.
	var stdout, stderr bytes.Buffer
	args := []string{"--config", filepath.Join(f.home, "generated", "tmux.conf")}
	if err := f.command().runApply(args, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v; stderr=%s", err, stderr.String())
	}
	var lines []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.HasPrefix(line, reclaimLinePrefix) {
			lines = append(lines, line)
		}
	}
	assertSingleReclaimLine(t, lines, reclaimLinePrefix+"removed 2 files")
	if !strings.Contains(stdout.String(), "skipped reload: no tmux runner configured") {
		t.Fatalf("stdout = %q, want the no-runner branch", stdout.String())
	}
	if reclaimExists(t, filepath.Join(stateHome, "projmux", "sessions")) || reclaimExists(t, filepath.Join(configHome, "projmux", "sessionstate-autosave-interval")) {
		t.Fatal("XDG targets were not reclaimed")
	}
	if !reclaimExists(t, defaultSession) || !reclaimExists(t, defaultAutosave) {
		t.Fatal("HOME fallback files were touched despite XDG overrides")
	}
}

func TestConfigApplyReclaimSkipsWithoutInjectedHome(t *testing.T) {
	var stdout bytes.Buffer
	(&tmuxCommand{lookupEnv: func(string) string { return "" }}).reclaimRetiredSnapshotFiles(&stdout)
	(&tmuxCommand{homeDir: func() (string, error) { return t.TempDir(), nil }}).reclaimRetiredSnapshotFiles(&stdout)
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}
