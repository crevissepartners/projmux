package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newGhosttyTestAdapter(t *testing.T) *GhosttyAdapter {
	t.Helper()
	a := NewGhosttyAdapter()
	a.now = func() time.Time { return time.Date(2026, 4, 30, 1, 32, 15, 0, time.UTC) }
	return a
}

func TestGhosttyAdapterDetect(t *testing.T) {
	t.Parallel()

	a := NewGhosttyAdapter()

	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "term program ghostty", env: map[string]string{"TERM_PROGRAM": "ghostty"}, want: true},
		{name: "term program uppercase", env: map[string]string{"TERM_PROGRAM": "GHOSTTY"}, want: true},
		{name: "resources dir", env: map[string]string{"GHOSTTY_RESOURCES_DIR": "/opt/ghostty"}, want: true},
		{name: "neither", env: map[string]string{"TERM_PROGRAM": "iTerm.app"}, want: false},
		{name: "empty env", env: map[string]string{}, want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := func(k string) string { return tc.env[k] }
			if got := a.Detect(env); got != tc.want {
				t.Fatalf("Detect(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestGhosttyAdapterDetectNilEnv(t *testing.T) {
	t.Parallel()
	a := NewGhosttyAdapter()
	if a.Detect(nil) {
		t.Fatalf("Detect(nil) = true, want false")
	}
}

func TestGhosttyAdapterConfigPathPrefersXDG(t *testing.T) {
	t.Parallel()

	a := NewGhosttyAdapter()
	env := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/xdg"
		}
		return ""
	}
	got, err := a.ConfigPath(env)
	if err != nil {
		t.Fatalf("ConfigPath() error = %v", err)
	}
	if want := filepath.Join("/xdg", "ghostty", "config"); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestGhosttyAdapterConfigPathFallsBackToHome(t *testing.T) {
	t.Parallel()

	a := NewGhosttyAdapter()
	a.userHomeDir = func() (string, error) { return "/home/u", nil }
	got, err := a.ConfigPath(func(string) string { return "" })
	if err != nil {
		t.Fatalf("ConfigPath() error = %v", err)
	}
	if want := filepath.Join("/home/u", ".config", "ghostty", "config"); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestGhosttyPlanMergeEmptyConfigAddsAllBindings(t *testing.T) {
	t.Parallel()

	a := newGhosttyTestAdapter(t)
	plan, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge empty error = %v", err)
	}
	if !plan.HasEffect() {
		t.Fatalf("expected effect on empty config")
	}
	if !plan.CreateNew {
		t.Fatalf("CreateNew = false, want true for missing file")
	}
	addCount := 0
	for _, ch := range plan.Changes {
		if ch.Kind == "add" {
			addCount++
		}
	}
	if addCount != len(ghosttyDesiredBindings) {
		t.Fatalf("add count = %d, want %d", addCount, len(ghosttyDesiredBindings))
	}
	for _, kb := range ghosttyDesiredBindings {
		needle := "keybind = " + kb.Trigger + "=" + kb.Action
		if !strings.Contains(plan.Updated, needle) {
			t.Fatalf("Updated missing %q\n--\n%s", needle, plan.Updated)
		}
	}
	if !strings.Contains(plan.Updated, ghosttyManagedHeader) {
		t.Fatalf("Updated missing managed header")
	}
}

func TestGhosttyPlanMergeIdempotent(t *testing.T) {
	t.Parallel()

	a := newGhosttyTestAdapter(t)
	first, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge first error = %v", err)
	}
	second, err := a.PlanMerge(first.Updated, true)
	if err != nil {
		t.Fatalf("PlanMerge second error = %v", err)
	}
	if second.HasEffect() {
		t.Fatalf("second merge unexpectedly changed config\nbefore:\n%s\nafter:\n%s", first.Updated, second.Updated)
	}
	for _, ch := range second.Changes {
		if ch.Kind != "noop" {
			t.Fatalf("expected all noop on second merge, got %+v", ch)
		}
	}
}

func TestGhosttyPlanMergePartialAddsMissingBindings(t *testing.T) {
	t.Parallel()

	a := newGhosttyTestAdapter(t)
	// Pre-seed with the first two desired bindings, written by the user.
	current := "# user config\n" +
		"keybind = alt+1=csi:9005u\n" +
		"keybind = alt+2=csi:9003u\n"
	plan, err := a.PlanMerge(current, true)
	if err != nil {
		t.Fatalf("PlanMerge error = %v", err)
	}
	if !plan.HasEffect() {
		t.Fatalf("expected effect when bindings missing")
	}

	expectAdd := len(ghosttyDesiredBindings) - 2
	addCount := 0
	noopCount := 0
	for _, ch := range plan.Changes {
		switch ch.Kind {
		case "add":
			addCount++
		case "noop":
			noopCount++
		}
	}
	if addCount != expectAdd {
		t.Fatalf("add count = %d, want %d", addCount, expectAdd)
	}
	if noopCount != 2 {
		t.Fatalf("noop count = %d, want 2", noopCount)
	}
	if !strings.Contains(plan.Updated, "# user config") {
		t.Fatalf("Updated lost user content:\n%s", plan.Updated)
	}
	// Pre-existing user bindings still present.
	if !strings.Contains(plan.Updated, "keybind = alt+1=csi:9005u") {
		t.Fatalf("Updated dropped user-owned alt+1 binding")
	}
}

func TestGhosttyPlanMergeConflictSkipsAndWarns(t *testing.T) {
	t.Parallel()

	a := newGhosttyTestAdapter(t)
	current := "keybind = alt+1=new_window\n"
	plan, err := a.PlanMerge(current, true)
	if err != nil {
		t.Fatalf("PlanMerge error = %v", err)
	}
	var conflict *MergeChange
	for i := range plan.Changes {
		if plan.Changes[i].Trigger == "alt+1" {
			conflict = &plan.Changes[i]
			break
		}
	}
	if conflict == nil {
		t.Fatalf("missing alt+1 change in plan")
	}
	if conflict.Kind != "skip-conflict" {
		t.Fatalf("alt+1 kind = %q, want skip-conflict", conflict.Kind)
	}
	if conflict.Existing != "new_window" {
		t.Fatalf("alt+1 existing = %q, want new_window", conflict.Existing)
	}
	// Conflict trigger must NOT appear in the appended block.
	if strings.Contains(plan.Updated, "keybind = alt+1=csi:9005u") {
		t.Fatalf("Updated unexpectedly overrode user mapping:\n%s", plan.Updated)
	}
	// Original user mapping still present.
	if !strings.Contains(plan.Updated, "keybind = alt+1=new_window") {
		t.Fatalf("Updated dropped user's alt+1 mapping:\n%s", plan.Updated)
	}
}

func TestGhosttyApplyMergeWritesBackupAndConfig(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "ghostty", "config")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := "# original user config\n"
	if err := os.WriteFile(cfg, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := newGhosttyTestAdapter(t)
	plan, err := a.PlanMerge(original, true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	plan.ConfigPath = cfg
	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if string(got) != plan.Updated {
		t.Fatalf("written config != plan.Updated\ngot:\n%s\nwant:\n%s", got, plan.Updated)
	}

	backup := cfg + ".bak.20260430-013215"
	bak, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup %s: %v", backup, err)
	}
	if string(bak) != original {
		t.Fatalf("backup contents = %q, want %q", bak, original)
	}
}

func TestGhosttyApplyMergeCreatesNewConfigWithoutBackup(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "ghostty", "config")

	a := newGhosttyTestAdapter(t)
	plan, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	plan.ConfigPath = cfg

	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}

	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("written config missing: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(cfg))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".bak.") {
			t.Fatalf("unexpected backup created on first run: %s", entry.Name())
		}
	}
}

func TestGhosttyApplyMergeNoEffectIsNoop(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	a := newGhosttyTestAdapter(t)

	plan := MergePlan{
		ConfigPath: cfg,
		Original:   "x",
		Updated:    "x",
	}
	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge no-op error = %v", err)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("expected no file written, stat err = %v", err)
	}
}

func TestGhosttyApplyMergeRequiresConfigPath(t *testing.T) {
	t.Parallel()

	a := newGhosttyTestAdapter(t)
	plan := MergePlan{Original: "a", Updated: "b"}
	err := a.ApplyMerge(plan)
	if err == nil || !strings.Contains(err.Error(), "ConfigPath") {
		t.Fatalf("ApplyMerge without ConfigPath error = %v", err)
	}
}

func TestParseGhosttyKeybind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line        string
		wantTrigger string
		wantAction  string
		wantOk      bool
	}{
		{line: "keybind = alt+1=csi:9005u", wantTrigger: "alt+1", wantAction: "csi:9005u", wantOk: true},
		{line: "  keybind=alt+2=csi:9003u  ", wantTrigger: "alt+2", wantAction: "csi:9003u", wantOk: true},
		{line: "# keybind = alt+1=csi:9005u", wantOk: false},
		{line: "", wantOk: false},
		{line: "keybind = onlytrigger", wantOk: false},
		{line: "font-size = 14", wantOk: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			gotTrigger, gotAction, gotOk := parseGhosttyKeybind(tc.line)
			if gotOk != tc.wantOk {
				t.Fatalf("parse(%q) ok = %v, want %v", tc.line, gotOk, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if gotTrigger != tc.wantTrigger || gotAction != tc.wantAction {
				t.Fatalf("parse(%q) = (%q, %q), want (%q, %q)", tc.line, gotTrigger, gotAction, tc.wantTrigger, tc.wantAction)
			}
		})
	}
}

func TestSplitGhosttyConfigStripsManagedBlock(t *testing.T) {
	t.Parallel()

	raw := "# user\n" +
		"keybind = ctrl+a=csi:9999u\n" +
		ghosttyManagedHeader + "\n" +
		"keybind = alt+1=csi:9005u\n" +
		ghosttyManagedFooter + "\n" +
		"font-size = 14\n"

	stripped, bindings := splitGhosttyConfig(raw)
	if strings.Contains(stripped, ghosttyManagedHeader) {
		t.Fatalf("stripped still contains managed header:\n%s", stripped)
	}
	if strings.Contains(stripped, "alt+1=csi:9005u") {
		t.Fatalf("stripped still contains managed binding:\n%s", stripped)
	}
	if got, ok := bindings["ctrl+a"]; !ok || got != "csi:9999u" {
		t.Fatalf("user binding ctrl+a = (%q,%v), want csi:9999u true", got, ok)
	}
	// Managed-block bindings are kept in the map so the merge can recognise
	// them as noop on a second run. They are still excluded from stripped
	// (verified above).
	if got, ok := bindings["alt+1"]; !ok || got != "csi:9005u" {
		t.Fatalf("alt+1 binding from managed block = (%q,%v), want csi:9005u", got, ok)
	}
	if !strings.Contains(stripped, "font-size = 14") {
		t.Fatalf("stripped lost trailing user content:\n%s", stripped)
	}
}

func TestSplitGhosttyConfigUserOverrideTrumpsManagedBlock(t *testing.T) {
	t.Parallel()

	// User declared alt+1=new_window before our managed block. The managed
	// block contains alt+1=csi:9005u. The user's intent must win so the
	// merge classifies alt+1 as skip-conflict instead of noop.
	raw := "keybind = alt+1=new_window\n" +
		ghosttyManagedHeader + "\n" +
		"keybind = alt+1=csi:9005u\n" +
		ghosttyManagedFooter + "\n"

	_, bindings := splitGhosttyConfig(raw)
	if got := bindings["alt+1"]; got != "new_window" {
		t.Fatalf("alt+1 = %q, want new_window (user override should win)", got)
	}
}

func TestGhosttyAdapterConfigPathCandidatesXDG(t *testing.T) {
	t.Parallel()

	a := NewGhosttyAdapter()
	env := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/xdg"
		}
		return ""
	}
	got, err := a.ConfigPathCandidates(env)
	if err != nil {
		t.Fatalf("ConfigPathCandidates() error = %v", err)
	}
	want := []string{
		filepath.Join("/xdg", "ghostty", "config"),
		filepath.Join("/xdg", "ghostty", "config.ghostty"),
	}
	if len(got) != len(want) {
		t.Fatalf("ConfigPathCandidates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ConfigPathCandidates()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGhosttyAdapterConfigPathCandidatesHomeFallback(t *testing.T) {
	t.Parallel()

	a := NewGhosttyAdapter()
	a.userHomeDir = func() (string, error) { return "/home/u", nil }
	got, err := a.ConfigPathCandidates(func(string) string { return "" })
	if err != nil {
		t.Fatalf("ConfigPathCandidates() error = %v", err)
	}
	want := []string{
		filepath.Join("/home/u", ".config", "ghostty", "config"),
		filepath.Join("/home/u", ".config", "ghostty", "config.ghostty"),
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// newGhosttyTestInitCommand returns an init command wired to operate inside a
// per-test temp dir as if it were $XDG_CONFIG_HOME. The Ghostty adapter is
// the sole registered terminal so the test exercises the production code
// path including ConfigPathCandidates.
func newGhosttyTestInitCommand(t *testing.T, xdg string) *initCommand {
	t.Helper()
	reg := newTestRegistry()
	reg.register(NewGhosttyAdapter())
	return &initCommand{
		registry: reg,
		getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
		readFile: os.ReadFile,
		stat:     os.Stat,
		lstat:    os.Lstat,
		getwd:    os.Getwd,
	}
}

func TestGhosttyInitNoCandidatesCreatesConfig(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr.String())
	}
	cfg := filepath.Join(tmp, "ghostty", "config")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("expected canonical config to be created at %s: %v", cfg, err)
	}
	other := filepath.Join(tmp, "ghostty", "config.ghostty")
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("config.ghostty should not be created when neither candidate exists, stat err=%v", err)
	}
}

func TestGhosttyInitOnlyConfigExistsMergesIntoIt(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := filepath.Join(cfgDir, "config")
	if err := os.WriteFile(cfg, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr.String())
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(got), ghosttyManagedHeader) {
		t.Fatalf("merge did not write managed block:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "config.ghostty")); !os.IsNotExist(err) {
		t.Fatalf("config.ghostty should not be created when only config exists, stat err=%v", err)
	}
}

func TestGhosttyInitOnlyConfigGhosttyExistsMergesIntoIt(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(cfgDir, "config.ghostty")
	if err := os.WriteFile(target, []byte("# dotfiles\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read config.ghostty: %v", err)
	}
	if !strings.Contains(string(got), ghosttyManagedHeader) {
		t.Fatalf("merge did not write managed block into config.ghostty:\n%s", got)
	}
	// The other candidate must NOT be created — that would split the user's
	// dotfiles between two files.
	if _, err := os.Stat(filepath.Join(cfgDir, "config")); !os.IsNotExist(err) {
		t.Fatalf("canonical config should not be created when config.ghostty exists, stat err=%v", err)
	}
	if !strings.Contains(stdout.String(), "config.ghostty") {
		t.Fatalf("output should reference config.ghostty path, got:\n%s", stdout.String())
	}
}

func TestGhosttyInitBothCandidatesExistRequiresExplicitConfig(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"config", "config.ghostty"} {
		if err := os.WriteFile(filepath.Join(cfgDir, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"ghostty", "--apply"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected ambiguity error when both candidates exist")
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("error message should mention --config, got: %v", err)
	}
	// Neither file should have been mutated.
	for _, name := range []string{"config", "config.ghostty"} {
		got, readErr := os.ReadFile(filepath.Join(cfgDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(got), ghosttyManagedHeader) {
			t.Fatalf("%s mutated despite ambiguity:\n%s", name, got)
		}
	}
}

func TestGhosttyInitBothCandidatesExistResolvedByConfigOverride(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"config", "config.ghostty"} {
		if err := os.WriteFile(filepath.Join(cfgDir, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	target := filepath.Join(cfgDir, "config.ghostty")
	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply", "--config", target}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() with --config error = %v (stderr=%s)", err, stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(got), ghosttyManagedHeader) {
		t.Fatalf("--config override did not merge into chosen file:\n%s", got)
	}
	// The other candidate must remain pristine.
	other, err := os.ReadFile(filepath.Join(cfgDir, "config"))
	if err != nil {
		t.Fatalf("read other: %v", err)
	}
	if strings.Contains(string(other), ghosttyManagedHeader) {
		t.Fatalf("non-targeted candidate should not be mutated:\n%s", other)
	}
}

func TestGhosttyInitSymlinkRefusedByDefault(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Pretend the user keeps their real config in a dotfiles directory and
	// has symlinked ~/.config/ghostty/config.ghostty to it.
	dotfiles := filepath.Join(tmp, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatalf("mkdir dotfiles: %v", err)
	}
	source := filepath.Join(dotfiles, "ghostty.conf")
	original := "# tracked in dotfiles repo\n"
	if err := os.WriteFile(source, []byte(original), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	link := filepath.Join(cfgDir, "config.ghostty")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"ghostty", "--apply"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected symlink refusal")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should mention symlink, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--allow-symlink") {
		t.Fatalf("error should mention --allow-symlink, got: %v", err)
	}
	got, readErr := os.ReadFile(source)
	if readErr != nil {
		t.Fatalf("read source: %v", readErr)
	}
	if string(got) != original {
		t.Fatalf("source mutated despite refusal:\n%s", got)
	}
}

func TestGhosttyInitSymlinkProceedsWithAllowFlag(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dotfiles := filepath.Join(tmp, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatalf("mkdir dotfiles: %v", err)
	}
	source := filepath.Join(dotfiles, "ghostty.conf")
	if err := os.WriteFile(source, []byte("# tracked\n"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	link := filepath.Join(cfgDir, "config.ghostty")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply", "--allow-symlink"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() with --allow-symlink error = %v (stderr=%s)", err, stderr.String())
	}
	// Writing through the symlink must update the real source file.
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(got), ghosttyManagedHeader) {
		t.Fatalf("merge did not write through symlink:\n%s", got)
	}
}

func TestGhosttyInitConfigOverrideToSymlinkStillRefused(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	source := filepath.Join(tmp, "real.conf")
	if err := os.WriteFile(source, []byte("# real\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := filepath.Join(tmp, "linked.conf")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	// Use a separate xdg dir with no candidates so override is the only
	// path being considered.
	xdg := filepath.Join(tmp, "xdg")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatalf("mkdir xdg: %v", err)
	}
	cmd := newGhosttyTestInitCommand(t, xdg)
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"ghostty", "--apply", "--config", link}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected --config override to a symlink to be refused")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should mention symlink, got: %v", err)
	}
}

func TestGhosttyInitConfigOverrideAcceptsRelativePath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	target := filepath.Join(tmp, "custom.conf")
	if err := os.WriteFile(target, []byte("# custom\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newGhosttyTestInitCommand(t, tmp)
	cmd.getwd = func() (string, error) { return tmp, nil }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"ghostty", "--apply", "--config", "custom.conf"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() with relative --config error = %v (stderr=%s)", err, stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(got), ghosttyManagedHeader) {
		t.Fatalf("relative --config did not merge into target:\n%s", got)
	}
}
