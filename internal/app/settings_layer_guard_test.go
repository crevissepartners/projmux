package app

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
)

// settingsFrontEntryRoutes are the front entry points: the only routes that
// may read a front-layer (TUI or WEB) setting. A top-level name covers the
// whole route; a two-word name covers that sub-route alone. Every other
// public route reads the central layer only.
var settingsFrontEntryRoutes = []string{
	"settings", "web", "shell", "switch",
	"config render", "config apply", "config edit",
	"internal",
}

// isFrontLayer reports whether only the front entry points may read layer.
func isFrontLayer(layer config.SettingLayer) bool {
	return layer == config.LayerTUI || layer == config.LayerWeb
}

func isSettingsFrontEntryRoute(path ...string) bool {
	for i := 1; i <= len(path); i++ {
		if slices.Contains(settingsFrontEntryRoutes, strings.Join(path[:i], " ")) {
			return true
		}
	}
	return false
}

// settingsLayerGuardArgv is one representative invocation of every public
// route outside the front entry points, down to the second argv word. `{root}`
// is an existing Project root directory and `{tmp}` a scratch directory.
// `create project` runs first so later routes can resolve Project `alpha`.
var settingsLayerGuardArgv = [][]string{
	{"create", "project", "--root", "{root}", "--name", "alpha"},
	{"create", "window", "-p", "alpha"},
	{"create", "pane", "-p", "alpha"},
	{"create", "agent", "--provider", "codex", "-p", "alpha"},
	{"create", "notification", "--text", "hi", "--target", "alpha:1.0"},
	{"create", "codex", "-p", "alpha"},
	{"create", "claude", "-p", "alpha"},
	{"create", "antigravity", "-p", "alpha"},

	{"agent", "status", "get", "alpha-agent"},
	{"agent", "topic", "get", "alpha-agent"},
	{"agent", "resume", "alpha-agent"},
	{"agent", "persona", "attach", "alpha-agent", "reviewer", "--dry-run", "-o", "json"},
	{"agent", "persona", "detach", "alpha-agent", "--dry-run", "-o", "json"},
	{"agent", "turn", "start", "alpha-agent", "--", "hello"},
	{"agent", "approval", "review", "alpha-agent"},
	{"agent", "review", "alpha-agent"},
	{"agent", "integrate", "codex", "--dry-run"},
	{"agent", "usage", "--json"},
	{"agent", "capabilities", "--provider", "codex", "-o", "json"},
	{"agent", "message", "status", "msg-1", "-o", "json"},
	{"agent", "wait", "alpha-agent", "--timeout", "1s"},
	{"agent", "question", "list", "alpha-agent", "-o", "json"},

	{"attention", "toggle"},
	{"attention", "clear"},
	{"attention", "arm"},
	{"attention", "list"},
	{"attention", "window"},

	{"attach", "project", "alpha"},

	{"config", "providers"},
	{"config", "providers", "--enable", "codex"},

	{"delete", "project", "alpha", "--dry-run"},
	{"delete", "window", "w1", "-p", "alpha", "--dry-run"},
	{"delete", "pane", "p1", "-p", "alpha", "--dry-run"},
	{"delete", "agent", "alpha-agent", "--dry-run"},
	{"delete", "notification", "n1"},

	{"describe", "project", "alpha"},
	{"describe", "window", "w1", "-p", "alpha"},
	{"describe", "pane", "p1", "-p", "alpha"},
	{"describe", "agent", "alpha-agent"},

	{"doctor"},
	{"doctor", "--json"},

	{"diagnostics", "log", "--tail", "5"},
	{"diagnostics", "agent-hook", "--tail", "5"},
	{"diagnostics", "report", "--output", "{tmp}/support-report"},

	{"focus", "project", "alpha"},
	{"focus", "window", "w1", "-p", "alpha"},
	{"focus", "pane", "p1", "-p", "alpha", "-w", "w1"},

	{"get", "projects"},
	{"get", "windows", "-A"},
	{"get", "panes", "-A"},
	{"get", "agents", "-A"},
	{"get", "runtime", "sessions"},
	{"get", "runtime", "windows"},
	{"get", "runtime", "panes"},
	{"get", "notifications"},
	{"get", "pane", "--current"},

	{"hook", "list"},
	{"hook", "edit"},
	{"hook", "validate"},
	{"hook", "trust"},
	{"hook", "untrust"},

	{"notification", "ack", "--all"},
	{"notification", "reconcile", "--json"},

	{"open", "project", "alpha"},

	{"persona", "list"},
	{"persona", "set", "reviewer", "--file", "{tmp}/persona.md"},
	{"persona", "show", "reviewer"},
	{"persona", "edit", "reviewer"},
	{"persona", "delete", "reviewer", "--yes"},

	{"pin", "project", "list"},

	{"prune", "agent", "--older-than", "720h"},
	{"prune", "project", "--missing", "--older-than", "720h"},

	{"quit"},

	{"reconcile", "resources", "--dry-run"},
	{"reconcile", "registry", "--dry-run"},

	{"rebind", "project", "alpha", "--root", "{root}"},

	{"rename", "project", "alpha", "--name", "alpha"},
	{"rename", "window", "w1", "--name", "w2", "-p", "alpha"},
	{"rename", "pane", "p1", "--name", "p2", "-p", "alpha"},
	{"rename", "agent", "alpha-agent", "--name", "beta-agent"},

	{"resources"},

	{"runtime", "sessions"},
	{"runtime", "diagnostics"},
	{"runtime", "attach"},
	{"runtime", "stop"},
	{"runtime", "tag"},
	{"runtime", "prune"},

	{"setup"},
	{"setup", "terminal"},

	{"start", "project", "alpha"},
	{"stop", "project", "alpha"},

	{"unregister", "project", "alpha", "--dry-run"},

	{"update", "status"},
	{"update", "check"},
	{"update", "apply", "--dry-run", "--no-apply"},

	{"welcome"},

	{"window", "record"},
	{"window", "recent"},

	{"help"},
	{"version"},
}

// settingsLayerGuardNonVacuity are front entry point invocations that must
// record front reads in the same harness, each with one item it must read.
// Without them a guard that recorded nothing at all would pass.
var settingsLayerGuardNonVacuity = []struct {
	argv []string
	item string
}{
	{[]string{"config", "render", "standalone"}, config.KeymapFileName},
	{[]string{"config", "render", "standalone"}, config.SettingConfigTheme},
	{[]string{"config", "render", "app"}, config.StatusbarGitVisibilityFileName},
	{[]string{"config", "edit", "--get"}, config.TmuxAISplitModeFileName},
}

// settingsLayerGuardRouteTimeout bounds one route. A route that runs longer
// fails the guard: nothing in this environment should wait.
const settingsLayerGuardRouteTimeout = 60 * time.Second

// settingsLayerGuardChildEnv marks a process started by a route under the
// guard. Such a child re-executes this test binary (os.Executable); TestMain
// sees the mark, logs the argv and exits before any test runs.
const settingsLayerGuardChildEnv = "PROJMUX_TEST_SETTINGS_LAYER_GUARD_CHILD"

// exitIfSettingsLayerGuardChild is called first thing in TestMain.
func exitIfSettingsLayerGuardChild() {
	dir := os.Getenv(settingsLayerGuardChildEnv)
	if dir == "" {
		return
	}
	if file, err := os.OpenFile(filepath.Join(dir, "self-exec.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(file, "%q\n", os.Args[1:])
		_ = file.Close()
	}
	os.Exit(3)
}

// TestSettingsLayerGuardCoversEveryPublicRoute keeps the guard complete: every
// public route (and each of its direct sub-routes) is either a front entry
// point or has a representative invocation in settingsLayerGuardArgv, so a
// new route cannot skip the guard by being absent from it.
func TestSettingsLayerGuardCoversEveryPublicRoute(t *testing.T) {
	t.Parallel()

	covered := func(path ...string) bool {
		for _, argv := range settingsLayerGuardArgv {
			if len(argv) >= len(path) && slices.Equal(argv[:len(path)], path) {
				return true
			}
		}
		return false
	}
	known := map[string]bool{}
	for _, route := range cli.Routes() {
		if route.Hidden && !isSettingsFrontEntryRoute(route.Name) {
			t.Errorf("hidden top-level route %q is neither a front entry point nor covered; decide its layer rule", route.Name)
		}
		known[route.Name] = true
		if isSettingsFrontEntryRoute(route.Name) {
			continue
		}
		if len(route.Children) == 0 {
			if !covered(route.Name) {
				t.Errorf("public route %q is not a front entry point and has no invocation in settingsLayerGuardArgv", route.Name)
			}
			continue
		}
		for _, child := range route.Children {
			known[route.Name+" "+child.Name] = true
			if isSettingsFrontEntryRoute(route.Name, child.Name) {
				continue
			}
			if !covered(route.Name, child.Name) {
				t.Errorf("public route %q is not a front entry point and has no invocation in settingsLayerGuardArgv", route.Name+" "+child.Name)
			}
		}
	}
	for _, name := range settingsFrontEntryRoutes {
		if !known[name] {
			t.Errorf("front entry point %q is not a route in the catalog", name)
		}
	}
	for _, argv := range settingsLayerGuardArgv {
		if isSettingsFrontEntryRoute(argv...) {
			t.Errorf("settingsLayerGuardArgv runs front entry point %q; front reads there are allowed", argv)
		}
		if !known[argv[0]] {
			t.Errorf("settingsLayerGuardArgv runs %q, which is not a catalog route", argv)
		}
	}
	for _, probe := range settingsLayerGuardNonVacuity {
		if !isSettingsFrontEntryRoute(probe.argv...) {
			t.Errorf("non-vacuity probe %q is not a front entry point", probe.argv)
		}
	}
}

// recordedFrontRead is one front read plus the call path that made it.
type recordedFrontRead struct {
	config.FrontRead
	callPath []string
}

// frontReadRecorder collects front reads from the config seam.
type frontReadRecorder struct {
	mu    sync.Mutex
	reads []recordedFrontRead
}

// record runs on the reading goroutine, so the caller's stack is the call
// path of the read.
func (r *frontReadRecorder) record(read config.FrontRead) {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	var path []string
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "/projmux/internal/") && !strings.HasSuffix(frame.File, "_test.go") {
			name := frame.Function[strings.LastIndex(frame.Function, "/")+1:]
			path = append(path, fmt.Sprintf("%s:%d %s", filepath.Base(frame.File), frame.Line, name))
		}
		if !more || len(path) == 8 {
			break
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, recordedFrontRead{FrontRead: read, callPath: path})
}

func (r *frontReadRecorder) take() []recordedFrontRead {
	r.mu.Lock()
	defer r.mu.Unlock()
	reads := r.reads
	r.reads = nil
	return reads
}

// settingsLayerGuardViolations returns the reads a route outside the front
// entry points may not make: every front read except a picker display read.
// The seam grants the picker display purpose only to `[theme]` and
// keymap.toml; the item check here holds that line again, so a widened seam
// would still fail the guard.
func settingsLayerGuardViolations(reads []recordedFrontRead) []recordedFrontRead {
	var out []recordedFrontRead
	for _, read := range reads {
		picker := read.Purpose == config.FrontReadPickerDisplay &&
			(read.Item.Name == config.SettingConfigTheme || read.Item.Name == config.KeymapFileName)
		if !picker {
			out = append(out, read)
		}
	}
	return out
}

// describeFrontReads names each distinct setting read and the file it was
// read from.
func describeFrontReads(reads []recordedFrontRead) string {
	var parts []string
	seen := map[string]bool{}
	for _, read := range reads {
		purpose := "setting"
		if read.Purpose == config.FrontReadPickerDisplay {
			purpose = "picker display"
		}
		line := fmt.Sprintf("%s [%s, %s read] (%s)", read.Item.Name, read.Item.Layer, purpose, filepath.Base(read.Path))
		if seen[line] {
			continue
		}
		seen[line] = true
		parts = append(parts, line)
	}
	return strings.Join(parts, ", ")
}

// describeFrontReadPaths lists each distinct setting read with its call path,
// innermost frame first.
func describeFrontReadPaths(reads []recordedFrontRead) string {
	var lines []string
	seen := map[string]bool{}
	for _, read := range reads {
		line := fmt.Sprintf("\t%s at %s\n\t\t%s", read.Item.Name, read.Path, strings.Join(read.callPath, "\n\t\t"))
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// refusingTransport fails every HTTP request, so a route that would reach the
// network (update check) fails fast and offline.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled by the settings layer guard")
}

// settingsLayerGuardEnv isolates the process for in-process route runs: a
// temp HOME and XDG homes under it, no tmux client or server reachable (TMUX
// unset, PATH an empty directory, TMUX_TMPDIR a fresh short directory), no
// editor, an unreachable proxy, and stdin at /dev/null. It seeds every front
// file with a non-default value so any read of one would change behavior.
type settingsLayerGuardEnv struct {
	home  string
	root  string
	tmp   string
	trap  string
	paths config.Paths
}

func newSettingsLayerGuardEnv(t *testing.T) *settingsLayerGuardEnv {
	t.Helper()
	home := t.TempDir()
	env := &settingsLayerGuardEnv{
		home: home,
		root: filepath.Join(home, "work", "alpha"),
		tmp:  filepath.Join(home, "scratch"),
		trap: filepath.Join(home, "self-exec"),
	}
	for _, dir := range []string{env.root, env.tmp, env.trap, filepath.Join(home, "empty-bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tmuxTmp, err := os.MkdirTemp("", "pmxg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmp) })

	set := map[string]string{
		"HOME":                     home,
		"XDG_CONFIG_HOME":          filepath.Join(home, ".config"),
		"XDG_STATE_HOME":           filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":           filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":            filepath.Join(home, ".local", "share"),
		"XDG_RUNTIME_DIR":          filepath.Join(home, "run"),
		"PATH":                     filepath.Join(home, "empty-bin"),
		"TMUX_TMPDIR":              tmuxTmp,
		"EDITOR":                   filepath.Join(home, "no-editor"),
		"VISUAL":                   filepath.Join(home, "no-editor"),
		"HTTP_PROXY":               "http://127.0.0.1:9",
		"HTTPS_PROXY":              "http://127.0.0.1:9",
		settingsLayerGuardChildEnv: env.trap,
	}
	for key, value := range set {
		t.Setenv(key, value)
	}
	for _, key := range []string{
		"TMUX", "TMUX_PANE", runtimeMutationAnchorPaneEnv, "PROJMUX_CWD", "PROJMUX_SOCKET",
		"CODEX_HOME", "CLAUDE_CONFIG_DIR", "PROJMUX_USAGE_STATE_DIR", "NO_PROXY", "no_proxy",
		nativeKeysEnvName, aiResumePickerLimitEnv, aiResumeScanDepthEnv,
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(env.root)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() {
		os.Stdin = stdin
		_ = devNull.Close()
	})

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.paths = paths
	env.seedFrontFiles(t)
	if err := os.WriteFile(filepath.Join(env.tmp, "persona.md"), []byte("You review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return env
}

// seedFrontFiles writes every declared front file with a value that differs
// from its default.
func (e *settingsLayerGuardEnv) seedFrontFiles(t *testing.T) {
	t.Helper()
	p := e.paths
	files := map[string]string{
		p.StatusbarNotificationsHUDVisibilityFile():                  "off",
		p.StatusbarAgentUsageHUDVisibilityFile():                     "off",
		p.StatusbarAgentUsageProviderVisibilityFile("claude"):        "off",
		p.StatusbarAgentUsageProviderVisibilityFile("codex"):         "off",
		p.StatusbarAgentUsageWindowVisibilityFile("claude", "5h"):    "off",
		p.StatusbarAgentUsageWindowVisibilityFile("codex", "weekly"): "off",
		p.StatusbarProjectVisibilityFile():                           "off",
		p.StatusbarWorkingDirectoryVisibilityFile():                  "off",
		p.StatusbarGitVisibilityFile():                               "off",
		p.StatusbarClockVisibilityFile():                             "off",
		p.StatusbarSettingsLauncherVisibilityFile():                  "off",
		p.StatusbarDecorationFile():                                  "emoji",
		p.StatusbarDecorationCwdFile():                               "symbol",
		p.StatusbarDecorationGitFile():                               "symbol",
		p.StatusbarDecorationNotifyFile():                            "symbol",
		p.AIBadgeStyleFile():                                         "emoji",
		p.RuntimeDiagnosticsVisibilityFile():                         "always",
		p.TmuxAISplitModeFile():                                      "shell\n",
		p.KeymapFile():                                               "schema_version = 2\n\n[bindings.\"project-sidebar.toggle\"]\nkeys = [\"M-1\"]\n",
		p.WebSettingsFile():                                          "[statusbar]\ngit = false\nclock = false\n",
		p.GlobalConfigFile():                                         "[theme]\naccent = \"#ff00ff\"\n\n[ui]\nnative_keys = false\n\n[ai]\nresume_picker_limit = 7\nresume_scan_depth = 3\n",
	}
	seeded := map[string]bool{}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(p.ConfigDir, path)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := config.SettingForFile(rel); ok {
			seeded[item.Name] = true
		}
	}
	for _, item := range config.SettingItems() {
		if !isFrontLayer(item.Layer) || item.Shape == config.SettingConfigKeys {
			continue
		}
		if !seeded[item.Name] {
			t.Fatalf("front setting %q is not seeded; a read of it would not matter", item.Name)
		}
	}
}

func (e *settingsLayerGuardEnv) expand(argv []string) []string {
	out := make([]string, len(argv))
	for i, word := range argv {
		word = strings.ReplaceAll(word, "{root}", e.root)
		out[i] = strings.ReplaceAll(word, "{tmp}", e.tmp)
	}
	return out
}

type settingsLayerGuardRun struct {
	reads    []recordedFrontRead
	err      error
	panicked any
	timedOut bool
}

// run executes one invocation in-process through the same App.Run the binary
// uses, and returns the front reads recorded while it ran.
func (e *settingsLayerGuardEnv) run(recorder *frontReadRecorder, argv []string) settingsLayerGuardRun {
	recorder.take()
	type outcome struct {
		err      error
		panicked any
	}
	done := make(chan outcome, 1)
	go func() {
		var result outcome
		defer func() {
			result.panicked = recover()
			done <- result
		}()
		app := New()
		app.update.client = &http.Client{Transport: refusingTransport{}}
		var stdout, stderr bytes.Buffer
		result.err = app.Run(e.expand(argv), &stdout, &stderr)
	}()
	select {
	case result := <-done:
		return settingsLayerGuardRun{reads: recorder.take(), err: result.err, panicked: result.panicked}
	case <-time.After(settingsLayerGuardRouteTimeout):
		return settingsLayerGuardRun{reads: recorder.take(), timedOut: true}
	}
}

// TestSettingsLayerGuardPublicRoutesReadNoFrontSetting runs every public route
// outside the front entry points in-process and asserts, at the config seam,
// that none of them read a TUI or WEB setting. Routes may fail in this
// environment (there is no tmux); the assertion is the count of front reads,
// attributed to the route that ran.
//
// No t.Parallel: it installs the package-level front-read observer and swaps
// process environment, working directory and stdin.
func TestSettingsLayerGuardPublicRoutesReadNoFrontSetting(t *testing.T) {
	env := newSettingsLayerGuardEnv(t)
	recorder := &frontReadRecorder{}
	restore := config.ObserveFrontReads(recorder.record)
	t.Cleanup(restore)

	for _, argv := range settingsLayerGuardArgv {
		name := strings.Join(argv, " ")
		result := env.run(recorder, argv)
		if result.timedOut {
			t.Fatalf("route %q did not finish within %s under the settings layer guard", name, settingsLayerGuardRouteTimeout)
		}
		if testing.Verbose() {
			t.Logf("route %q: err=%v panic=%v front reads=%d", name, result.err, result.panicked, len(result.reads))
		}
		violations := settingsLayerGuardViolations(result.reads)
		if testing.Verbose() && len(violations) < len(result.reads) {
			t.Logf("route %q: picker display reads (allowed): %s", name, describeFrontReads(result.reads))
		}
		if len(violations) > 0 {
			t.Errorf("route %q is not a front entry point but read front setting(s): %s\n%s", name, describeFrontReads(violations), describeFrontReadPaths(violations))
		}
	}

	for _, probe := range settingsLayerGuardNonVacuity {
		name := strings.Join(probe.argv, " ")
		result := env.run(recorder, probe.argv)
		if result.timedOut {
			t.Fatalf("front entry point %q did not finish within %s", name, settingsLayerGuardRouteTimeout)
		}
		t.Logf("front entry point %q: err=%v front reads: %s", name, result.err, describeFrontReads(result.reads))
		// The probe item must arrive as a setting read: a front entry point
		// that reached it only through the picker exemption would not show
		// that the setting-read half of the seam is wired.
		if !slices.ContainsFunc(settingsLayerGuardViolations(result.reads), func(read recordedFrontRead) bool { return read.Item.Name == probe.item }) {
			t.Errorf("front entry point %q recorded no setting read of %s (reads: %s); the guard above would prove nothing", name, probe.item, describeFrontReads(result.reads))
		}
	}

	if raw, err := os.ReadFile(filepath.Join(env.trap, "self-exec.log")); err == nil {
		t.Logf("routes re-executed the test binary (stopped before any test ran):\n%s", raw)
	}
}

// TestAppFrontLoadersReportTheirReads proves each front reader in this
// package reports the declared item at the seam, including the ones the
// route guard's non-vacuity probes do not reach (native keys, resume_*,
// keymap migration). Without it a reader could lose its NoteFrontRead call
// and the guard would go blind to it.
//
// No t.Parallel: it installs the package-level front-read observer.
func TestAppFrontLoadersReportTheirReads(t *testing.T) {
	home := t.TempDir()
	lookupEnv := func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return filepath.Join(home, ".config")
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }
	project := filepath.Join(home, "project")

	recorder := &frontReadRecorder{}
	restore := config.ObserveFrontReads(recorder.record)
	t.Cleanup(restore)
	expectPurpose := func(label string, purpose config.FrontReadPurpose, want ...string) {
		t.Helper()
		var got []string
		for _, read := range recorder.take() {
			if item, ok := config.LookupSetting(read.Item.Name); !ok || !isFrontLayer(item.Layer) {
				t.Errorf("%s: reported %q, which is not a declared front setting", label, read.Item.Name)
			}
			if read.Purpose != purpose {
				t.Errorf("%s: %s read with purpose %d, want %d", label, read.Item.Name, read.Purpose, purpose)
			}
			got = append(got, read.Item.Name)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: front reads = %q, want %q", label, got, want)
		}
	}
	expect := func(label string, want ...string) {
		t.Helper()
		expectPurpose(label, config.FrontReadSetting, want...)
	}
	expectPicker := func(label string, want ...string) {
		t.Helper()
		expectPurpose(label, config.FrontReadPickerDisplay, want...)
	}

	_, _ = effectiveThemeFromConfig(homeDir, lookupEnv, "")
	expect("theme", config.SettingConfigTheme)
	_, _ = configRenderThemeSource(homeDir, lookupEnv, "")
	expect("theme for config render", config.SettingConfigTheme)
	nativeKeysSettingEnabled(lookupEnv, homeDir)
	expect("native keys", config.SettingConfigUINativeKeys)
	resolveAIResumePickerLimit(homeDir, lookupEnv, project)
	expect("resume picker limit", config.SettingConfigAIResume, config.SettingConfigAIResume)
	resolveAIResumeScanDepth(homeDir, lookupEnv, project)
	expect("resume scan depth", config.SettingConfigAIResume, config.SettingConfigAIResume)
	_, _, _ = loadMergedKeyBindingCatalog(keymapLoader{homeDir: homeDir, lookupEnv: lookupEnv})
	expect("keymap catalog", config.KeymapFileName)

	// The picker render path reports the same two files as picker display
	// reads, and nothing else it reads.
	_, _ = pickerRenderThemeSource(homeDir, lookupEnv)
	expectPicker("picker theme", config.SettingConfigTheme)
	effectivePickerKeysForActions(homeDir, lookupEnv, []string{"Sidebar:PinProject"}, nil)
	expectPicker("picker keys", config.KeymapFileName)
	pickerActionKeyGuide(homeDir, lookupEnv, []pickerActionKeyGuideItem{{ActionID: "Sidebar:PinProject", Label: "pin"}})
	expectPicker("picker key guide", config.KeymapFileName)
	pickerCloseActionsForPopupToggleMode(homeDir, lookupEnv, "recent-windows", "esc")
	expectPicker("picker close actions", config.KeymapFileName)
	_, _, _, _, _ = loadKeymapForEdit(keymapStore{homeDir: homeDir, lookupEnv: lookupEnv})
	expect("keymap edit", config.KeymapFileName)
	_, _ = planKeymapMigration(keymapStore{homeDir: homeDir, lookupEnv: lookupEnv})
	expect("keymap migration", config.KeymapFileName)
	(&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).getMode()
	expect("split launch default", config.TmuxAISplitModeFileName)
	loadStatusbarDecorationSet(homeDir, lookupEnv)
	expect("decorations", config.StatusbarDecorationFileName)

	// The central readers next to them report nothing.
	aiEnabledAgents(homeDir, lookupEnv)
	resolveUISplitCWDSource("", project, homeDir, lookupEnv)
	expect("central readers")
}

// TestSettingsLayerGuardRejectsPickerReadsOfOtherSettings holds the picker
// display exemption to its two items: a picker display read of any other
// front setting still fails the guard, and so do setting reads of the two.
//
// No t.Parallel: it installs the package-level front-read observer.
func TestSettingsLayerGuardRejectsPickerReadsOfOtherSettings(t *testing.T) {
	recorder := &frontReadRecorder{}
	restore := config.ObserveFrontReads(recorder.record)
	t.Cleanup(restore)

	for _, item := range config.SettingItems() {
		if !isFrontLayer(item.Layer) {
			continue
		}
		exempt := item.Name == config.SettingConfigTheme || item.Name == config.KeymapFileName
		config.NotePickerDisplayRead(item.Name, "/x/"+item.File)
		if got := len(settingsLayerGuardViolations(recorder.take())); got != 0 && exempt {
			t.Errorf("a picker display read of %s failed the guard; it is exempt", item.Name)
		} else if got != 1 && !exempt {
			t.Errorf("a picker display read of %s passed the guard; only [theme] and keymap.toml are exempt", item.Name)
		}
		config.NoteFrontRead(item.Name, "/x/"+item.File)
		if got := len(settingsLayerGuardViolations(recorder.take())); got != 1 {
			t.Errorf("a setting read of %s passed the guard", item.Name)
		}
	}
	// Even a read reported as picker display must name an exempt item to pass.
	forged := recordedFrontRead{FrontRead: config.FrontRead{Purpose: config.FrontReadPickerDisplay}}
	forged.Item, _ = config.LookupSetting(config.StatusbarGitVisibilityFileName)
	if len(settingsLayerGuardViolations([]recordedFrontRead{forged})) != 1 {
		t.Error("the guard passed a picker display read of statusbar-visibility-git")
	}
}

// settingsInlinePathAllowed are literal names a path onto the projmux config
// directory may still spell inline, with the reason.
var settingsInlinePathAllowed = map[string]string{
	"tmux.conf": "the generated tmux config, not a setting",
	"icons":     "notification icons live under the data home, not in the config directory",
}

// TestAppNamesNoSettingPathInline keeps internal/app from naming a file under
// the projmux config directory by a string literal: a setting's path comes
// from its path symbol in internal/config, where its layer is declared.
func TestAppNamesNoSettingPathInline(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Join" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "filepath" {
				return true
			}
			// A path is under the projmux config directory once it has
			// passed Paths.ConfigDir, or a config home followed by the
			// projmux directory name.
			inConfig, configHome := false, false
			for _, arg := range call.Args {
				switch expr := arg.(type) {
				case *ast.SelectorExpr:
					switch {
					case expr.Sel.Name == "ConfigDir":
						inConfig = true
					case expr.Sel.Name == "AppName" && configHome:
						inConfig = true
					}
				case *ast.Ident:
					lower := strings.ToLower(expr.Name)
					if strings.Contains(lower, "confighome") || strings.Contains(lower, "configdir") {
						configHome = true
					}
				case *ast.BasicLit:
					if expr.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(expr.Value)
					if err != nil {
						continue
					}
					switch {
					case value == ".config":
						configHome = true
					case value == config.AppName && configHome:
						inConfig = true
					case !inConfig:
					case settingsInlinePathAllowed[value] != "":
					default:
						t.Errorf("%s: filepath.Join names %q under the projmux config directory inline; add a path symbol in internal/config and declare its layer", fset.Position(expr.Pos()), value)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
