package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
)

const centralStatusbarSeedLinePrefix = "seeded central status bar defaults: "

// centralStatusbarLeaf is one status bar visibility key with a central default,
// read through the TUI loader and through the central-only reader.
type centralStatusbarLeaf struct {
	name    string
	path    func(config.Paths) string
	load    func(homeDir func() (string, error), lookupEnv func(string) string) config.StatusbarVisibilityState
	central func(homeDir func() (string, error), lookupEnv func(string) string) config.StatusbarVisibilityState
	def     config.StatusbarVisibility
}

// centralStatusbarLeaves covers the eight keys: both HUDs, each usage provider
// and window of the capability map, and the four row one segments.
func centralStatusbarLeaves() []centralStatusbarLeaf {
	hud := func(name string, component statusbarHUDComponent, path func(config.Paths) string) centralStatusbarLeaf {
		return centralStatusbarLeaf{
			name: name,
			path: path,
			load: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadStatusbarHUDVisibilityState(h, e, component)
			},
			central: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadCentralStatusbarHUDVisibility(h, e, component)
			},
			def: config.StatusbarVisibilityOn,
		}
	}
	rowOne := func(name string, component statusbarRowOneComponent, path func(config.Paths) string) centralStatusbarLeaf {
		return centralStatusbarLeaf{
			name: name,
			path: path,
			load: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadStatusbarRowOneVisibilityState(h, e, component)
			},
			central: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadCentralStatusbarRowOneVisibility(h, e, component)
			},
			def: config.StatusbarVisibilityOn,
		}
	}
	usageLeaf := func(provider, window string, def config.StatusbarVisibility) centralStatusbarLeaf {
		leaf := agentUsageVisibilityLeaf{provider: provider, window: window}
		name := "usage-provider-" + provider
		path := func(p config.Paths) string { return p.StatusbarAgentUsageProviderVisibilityFile(provider) }
		if window != "" {
			name = "usage-window-" + provider + "-" + window
			path = func(p config.Paths) string { return p.StatusbarAgentUsageWindowVisibilityFile(provider, window) }
		}
		return centralStatusbarLeaf{
			name: name,
			path: path,
			load: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadAgentUsageVisibilityState(h, e, leaf)
			},
			central: func(h func() (string, error), e func(string) string) config.StatusbarVisibilityState {
				return loadCentralAgentUsageVisibility(h, e, leaf)
			},
			def: def,
		}
	}
	return []centralStatusbarLeaf{
		hud("notifications-hud", statusbarHUDNotifications, config.Paths.StatusbarNotificationsHUDVisibilityFile),
		hud("agent-usage-hud", statusbarHUDAgentUsage, config.Paths.StatusbarAgentUsageHUDVisibilityFile),
		usageLeaf("claude", "", config.StatusbarVisibilityOn),
		usageLeaf("codex", "", config.StatusbarVisibilityOn),
		usageLeaf("claude", "5h", config.StatusbarVisibilityOn),
		usageLeaf("claude", "weekly", config.StatusbarVisibilityOn),
		usageLeaf("codex", "5h", config.StatusbarVisibilityOff),
		usageLeaf("codex", "weekly", config.StatusbarVisibilityOn),
		rowOne("project", statusbarRowOneProject, config.Paths.StatusbarProjectVisibilityFile),
		rowOne("working-directory", statusbarRowOneWorkingDirectory, config.Paths.StatusbarWorkingDirectoryVisibilityFile),
		rowOne("git", statusbarRowOneGit, config.Paths.StatusbarGitVisibilityFile),
		rowOne("clock", statusbarRowOneClock, config.Paths.StatusbarClockVisibilityFile),
	}
}

func oppositeStatusbarVisibility(value config.StatusbarVisibility) config.StatusbarVisibility {
	if value == config.StatusbarVisibilityOn {
		return config.StatusbarVisibilityOff
	}
	return config.StatusbarVisibilityOn
}

func (f *reclaimFixture) paths(t *testing.T) config.Paths {
	t.Helper()
	paths, err := configPaths(func() (string, error) { return f.home, nil }, func(name string) string { return f.env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func (f *reclaimFixture) resolvers() (func() (string, error), func(string) string) {
	return func() (string, error) { return f.home, nil }, func(name string) string { return f.env[name] }
}

func writeCentralStatusbarDefaults(t *testing.T, paths config.Paths, values map[string]config.StatusbarVisibility) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"visibility":{`)
	first := true
	for key, value := range values {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + key + `":"` + string(value) + `"`)
	}
	b.WriteString("}}\n")
	reclaimWrite(t, paths.StatusbarDefaultsFile(), b.String())
}

func centralStatusbarKey(t *testing.T, path string) string {
	t.Helper()
	key, ok := config.StatusbarDefaultKey(path)
	if !ok {
		t.Fatalf("%s has no central key", path)
	}
	return key
}

// TestStatusbarVisibilityLoadersResolveTUIThenCentralThenDefault is acceptance
// 1 for every key: a TUI value wins, without one the central default decides,
// and without that the built-in (usage window: capability) default does. The
// central-only reader ignores the TUI file throughout.
func TestStatusbarVisibilityLoadersResolveTUIThenCentralThenDefault(t *testing.T) {
	for _, leaf := range centralStatusbarLeaves() {
		t.Run(leaf.name, func(t *testing.T) {
			f := newReclaimFixture(t)
			homeDir, lookupEnv := f.resolvers()
			paths := f.paths(t)
			other := oppositeStatusbarVisibility(leaf.def)

			for _, state := range []config.StatusbarVisibilityState{leaf.load(homeDir, lookupEnv), leaf.central(homeDir, lookupEnv)} {
				if state.Effective != leaf.def || state.Source != config.StatusbarVisibilitySourceDefault {
					t.Fatalf("nothing stored = %+v, want default %s", state, leaf.def)
				}
			}

			writeCentralStatusbarDefaults(t, paths, map[string]config.StatusbarVisibility{centralStatusbarKey(t, leaf.path(paths)): other})
			for _, state := range []config.StatusbarVisibilityState{leaf.load(homeDir, lookupEnv), leaf.central(homeDir, lookupEnv)} {
				if state.Effective != other || state.Source != config.StatusbarVisibilitySourceCentral {
					t.Fatalf("central only = %+v, want central %s", state, other)
				}
			}

			reclaimWrite(t, leaf.path(paths), string(leaf.def)+"\n")
			if state := leaf.load(homeDir, lookupEnv); state.Effective != leaf.def || state.Source != config.StatusbarVisibilitySourceSaved {
				t.Fatalf("TUI and central = %+v, want saved %s", state, leaf.def)
			}
			if state := leaf.central(homeDir, lookupEnv); state.Effective != other || state.Source != config.StatusbarVisibilitySourceCentral {
				t.Fatalf("central reader with a TUI value = %+v, want central %s", state, other)
			}

			reclaimWrite(t, leaf.path(paths), "maybe\n")
			if state := leaf.load(homeDir, lookupEnv); state.Effective != other || state.Source != config.StatusbarVisibilitySourceCentral || state.Invalid != "maybe" {
				t.Fatalf("invalid TUI value = %+v, want central %s with the invalid value reported", state, other)
			}
		})
	}
}

// TestCentralStatusbarReaderMakesNoFrontRead is acceptance 5: the reader a
// non-TUI surface falls back through reads the central layer alone.
//
// No t.Parallel: it installs the package-level observer.
func TestCentralStatusbarReaderMakesNoFrontRead(t *testing.T) {
	f := newReclaimFixture(t)
	homeDir, lookupEnv := f.resolvers()
	paths := f.paths(t)
	for _, leaf := range centralStatusbarLeaves() {
		reclaimWrite(t, leaf.path(paths), "off\n")
	}
	writeCentralStatusbarDefaults(t, paths, map[string]config.StatusbarVisibility{"clock": config.StatusbarVisibilityOff})

	var reads []config.FrontRead
	restore := config.ObserveFrontReads(func(read config.FrontRead) { reads = append(reads, read) })
	defer restore()
	for _, leaf := range centralStatusbarLeaves() {
		_ = leaf.central(homeDir, lookupEnv)
	}
	_ = loadCentralStatusbarRowOneVisibility(homeDir, lookupEnv, statusbarRowOneSettingsLauncher)
	if len(reads) != 0 {
		t.Fatalf("central reader front reads = %+v, want none", reads)
	}
	if item, ok := config.SettingForFile(config.StatusbarDefaultsFileName); !ok || item.Layer != config.LayerCentral {
		t.Fatalf("%s is declared %+v, %v; want central", config.StatusbarDefaultsFileName, item, ok)
	}
}

// TestSeedCentralStatusbarDefaultsKeepsTheTUIDisplay is acceptance 2: valid TUI
// values are copied as they are, invalid, empty and missing ones and the
// settings launcher are not, and every loader shows the same thing before and
// after. With the TUI files gone the copied keys still show the same values.
func TestSeedCentralStatusbarDefaultsKeepsTheTUIDisplay(t *testing.T) {
	f := newReclaimFixture(t)
	homeDir, lookupEnv := f.resolvers()
	paths := f.paths(t)
	leaves := centralStatusbarLeaves()
	wantCopied := map[string]config.StatusbarVisibility{}
	for i, leaf := range leaves {
		switch leaf.name {
		case "git":
			reclaimWrite(t, leaf.path(paths), "maybe\n")
		case "working-directory":
			reclaimWrite(t, leaf.path(paths), "")
		case "clock":
			// missing
		default:
			value := config.StatusbarVisibilityOff
			if i%2 == 0 {
				value = config.StatusbarVisibilityOn
			}
			reclaimWrite(t, leaf.path(paths), string(value)+"\n")
			wantCopied[centralStatusbarKey(t, leaf.path(paths))] = value
		}
	}
	reclaimWrite(t, paths.StatusbarSettingsLauncherVisibilityFile(), "off\n")

	before := map[string]config.StatusbarVisibilityState{}
	for _, leaf := range leaves {
		before[leaf.name] = leaf.load(homeDir, lookupEnv)
	}
	copied, err := seedCentralStatusbarDefaultsOnce(homeDir, lookupEnv, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if copied != len(wantCopied) {
		t.Fatalf("copied %d, want %d", copied, len(wantCopied))
	}
	central, err := config.LoadStatusbarDefaults(paths.StatusbarDefaultsFile())
	if err != nil {
		t.Fatal(err)
	}
	if len(central) != len(wantCopied) {
		t.Fatalf("central = %v, want %v", central, wantCopied)
	}
	for key, value := range wantCopied {
		if central[key] != value {
			t.Fatalf("central = %v, want %v", central, wantCopied)
		}
	}
	for _, leaf := range leaves {
		if after := leaf.load(homeDir, lookupEnv); after != before[leaf.name] {
			t.Errorf("%s: after the copy %+v, before %+v", leaf.name, after, before[leaf.name])
		}
	}

	for _, leaf := range leaves {
		key := centralStatusbarKey(t, leaf.path(paths))
		if _, ok := wantCopied[key]; !ok {
			continue
		}
		if err := os.Remove(leaf.path(paths)); err != nil {
			t.Fatal(err)
		}
		if after := leaf.load(homeDir, lookupEnv); after.Effective != before[leaf.name].Effective || after.Source != config.StatusbarVisibilitySourceCentral {
			t.Errorf("%s without its TUI file = %+v, want central %s", leaf.name, after, before[leaf.name].Effective)
		}
	}
}

// TestSeedCentralStatusbarDefaultsRunsOnceAndNeverOverwrites is acceptance 3:
// a second copy writes nothing, a TUI change after the copy never reaches the
// central file, and a central value stored before the copy stays.
func TestSeedCentralStatusbarDefaultsRunsOnceAndNeverOverwrites(t *testing.T) {
	f := newReclaimFixture(t)
	homeDir, lookupEnv := f.resolvers()
	paths := f.paths(t)
	writeCentralStatusbarDefaults(t, paths, map[string]config.StatusbarVisibility{"clock": config.StatusbarVisibilityOff})
	reclaimWrite(t, paths.StatusbarClockVisibilityFile(), "on\n")
	reclaimWrite(t, paths.StatusbarGitVisibilityFile(), "off\n")

	copied, err := seedCentralStatusbarDefaultsOnce(homeDir, lookupEnv, time.Now)
	if err != nil || copied != 1 {
		t.Fatalf("first copy = %d, %v; want 1 (git only)", copied, err)
	}
	central, err := config.LoadStatusbarDefaults(paths.StatusbarDefaultsFile())
	if err != nil {
		t.Fatal(err)
	}
	if central["clock"] != config.StatusbarVisibilityOff || central["git"] != config.StatusbarVisibilityOff {
		t.Fatalf("central = %v, want clock kept off and git copied off", central)
	}
	if _, err := os.Stat(paths.StatusbarDefaultsSeedStateFile()); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	before := reclaimTree(t, f.home)
	centralBefore, _ := os.ReadFile(paths.StatusbarDefaultsFile())
	if copied, err := seedCentralStatusbarDefaultsOnce(homeDir, lookupEnv, time.Now); err != nil || copied != 0 {
		t.Fatalf("second copy = %d, %v; want nothing", copied, err)
	}
	if after := reclaimTree(t, f.home); !slices.Equal(before, after) {
		t.Fatalf("second copy changed the tree:\nbefore=%q\nafter=%q", before, after)
	}

	reclaimWrite(t, paths.StatusbarGitVisibilityFile(), "on\n")
	reclaimWrite(t, paths.StatusbarProjectVisibilityFile(), "off\n")
	if copied, err := seedCentralStatusbarDefaultsOnce(homeDir, lookupEnv, time.Now); err != nil || copied != 0 {
		t.Fatalf("copy after a TUI change = %d, %v; want nothing", copied, err)
	}
	if centralAfter, _ := os.ReadFile(paths.StatusbarDefaultsFile()); string(centralAfter) != string(centralBefore) {
		t.Fatalf("a TUI change reached the central file:\nbefore=%s\nafter=%s", centralBefore, centralAfter)
	}
}

// markCentralStatusbarSeeded writes the seed record, for tests of other apply
// steps that hold the state tree unchanged: the one-time copy already ran.
func markCentralStatusbarSeeded(t *testing.T, stateDir string) {
	t.Helper()
	reclaimWrite(t, filepath.Join(stateDir, config.StatusbarDefaultsSeedStateFileName), "2026-09-24T00:00:00Z\n")
}

// applyCentralStatusbarSeed runs a real `tmux apply` and returns only the seed
// lines it printed.
func applyCentralStatusbarSeed(t *testing.T, f *reclaimFixture, extra ...string) []string {
	t.Helper()
	args := append([]string{"--config", filepath.Join(f.home, "generated", "tmux.conf")}, extra...)
	var stdout, stderr bytes.Buffer
	if err := f.command().runApply(args, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var lines []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.HasPrefix(line, centralStatusbarSeedLinePrefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestConfigApplySeedsCentralStatusbarDefaultsOnce is the copy at its one
// point, `config apply`, on both sides of the --no-reload branch: the first
// apply copies and says so, a later one is a quiet no-op.
func TestConfigApplySeedsCentralStatusbarDefaultsOnce(t *testing.T) {
	for _, extra := range [][]string{{"--no-reload"}, nil} {
		t.Run(strings.Join(append([]string{"args"}, extra...), " "), func(t *testing.T) {
			f := newReclaimFixture(t)
			paths := f.paths(t)
			reclaimWrite(t, paths.StatusbarClockVisibilityFile(), "off\n")
			reclaimWrite(t, paths.StatusbarAgentUsageWindowVisibilityFile("codex", "5h"), "on\n")

			if got, want := applyCentralStatusbarSeed(t, f, extra...), []string{centralStatusbarSeedLinePrefix + "copied 2 TUI value(s)"}; !slices.Equal(got, want) {
				t.Fatalf("first apply lines = %q, want %q", got, want)
			}
			central, err := config.LoadStatusbarDefaults(paths.StatusbarDefaultsFile())
			if err != nil || central["clock"] != config.StatusbarVisibilityOff || central["agent-usage-window-codex-5h"] != config.StatusbarVisibilityOn || len(central) != 2 {
				t.Fatalf("central = %v, %v", central, err)
			}
			if lines := applyCentralStatusbarSeed(t, f, extra...); len(lines) != 0 {
				t.Fatalf("second apply printed %q, want nothing", lines)
			}
		})
	}
}

// TestConfigApplySeedFailureNeverFailsTheApply keeps the copy off the apply's
// result, and retries it on the next apply because no record was written.
func TestConfigApplySeedFailureNeverFailsTheApply(t *testing.T) {
	f := newReclaimFixture(t)
	paths := f.paths(t)
	reclaimWrite(t, paths.StatusbarClockVisibilityFile(), "off\n")
	// A file where the state directory belongs makes the record unwritable.
	reclaimWrite(t, f.stateDir, "not a directory\n")

	lines := applyCentralStatusbarSeed(t, f, "--no-reload")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], centralStatusbarSeedLinePrefix+"failed (") {
		t.Fatalf("apply lines = %q, want one failure line", lines)
	}
	if err := os.Remove(f.stateDir); err != nil {
		t.Fatal(err)
	}
	if got, want := applyCentralStatusbarSeed(t, f, "--no-reload"), []string{centralStatusbarSeedLinePrefix + "copied 1 TUI value(s)"}; !slices.Equal(got, want) {
		t.Fatalf("retry lines = %q, want %q", got, want)
	}
	if _, err := os.Stat(paths.StatusbarDefaultsSeedStateFile()); err != nil {
		t.Fatalf("retry wrote no record: %v", err)
	}
}

// TestSeedCentralStatusbarDefaultsNeedsAnInjectedHome keeps a partially
// constructed command away from the real home directory.
func TestSeedCentralStatusbarDefaultsNeedsAnInjectedHome(t *testing.T) {
	var stdout bytes.Buffer
	(&tmuxCommand{}).seedCentralStatusbarDefaults(&stdout)
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}
