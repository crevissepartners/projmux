package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func TestLayoutListPrintsProjectPresets(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
description = "Daily dev"

[[windows]]
index = 0
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
recipe = "shell"
`)
	cmd := &layoutCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{"NAME\tMODE\tWINDOWS\tPANES\tDESCRIPTION", "dev\tinherit-autosave\t1\t1\tDaily dev"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLayoutListJSONSkipsMalformedWithWarning(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1

[[windows]]
index = 0
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
recipe = "shell"
`)
	writeLayoutTestFile(t, project, "bad", `schema_version = "nope"`)
	cmd := &layoutCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"name": "dev"`) {
		t.Fatalf("stdout = %s, want dev JSON entry", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning: skip layout preset") || !strings.Contains(stderr.String(), "bad.toml") {
		t.Fatalf("stderr = %q, want malformed warning", stderr.String())
	}
}

func TestLayoutShowPrintsPresetContent(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "review", `
schema_version = 1
description = "Review"
`)
	cmd := &layoutCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"show", "review"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, `description = "Review"`) {
		t.Fatalf("stdout = %q, want raw preset content", got)
	}
}

func TestLayoutSaveCapturesPortablePresetWithDescription(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	runner := newLayoutSaveTestRunner(project)
	cmd := &layoutCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"save", "--description", "Daily dev", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "saved layout preset: dev (1 window, 2 panes)") {
		t.Fatalf("stdout = %q, want saved summary", got)
	}
	preset, err := corelayout.NewStore(project).Load("dev")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if preset.Description != "Daily dev" {
		t.Fatalf("Description = %q, want Daily dev", preset.Description)
	}
	if preset.DefaultCWD != "${PROJMUX_CWD}" {
		t.Fatalf("DefaultCWD = %q, want portable project root", preset.DefaultCWD)
	}
	if got := preset.Windows[0].Panes[0].CWD; got != "${PROJMUX_CWD}" {
		t.Fatalf("pane 0 CWD = %q, want portable project root", got)
	}
	if got := preset.Windows[0].Panes[1].CWD; got != "${PROJMUX_CWD}/service" {
		t.Fatalf("pane 1 CWD = %q, want portable project relative path", got)
	}
	body, err := os.ReadFile(filepath.Join(project, ".projmux", "layouts", "dev.toml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(body), project) {
		t.Fatalf("preset body contains absolute project path %q:\n%s", project, string(body))
	}
}

func TestLayoutSaveFreshMode(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	cmd := &layoutCommand{
		runner: newLayoutSaveTestRunner(project),
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
	}

	if err := cmd.Run([]string{"save", "--fresh", "scratch"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	preset, err := corelayout.NewStore(project).Load("scratch")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if preset.Mode != corelayout.ModeFreshEachTime {
		t.Fatalf("Mode = %q, want fresh-each-time", preset.Mode)
	}
}

func TestLayoutSaveFailsClearlyOutsideTmux(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	cmd := &layoutCommand{
		runner: &recordingTmuxRunner{},
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	err := cmd.Run([]string{"save", "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a current tmux session") {
		t.Fatalf("error = %v, want clear current session failure", err)
	}
}

func TestLayoutRemoveForceDeletesPreset(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
`)
	cmd := &layoutCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"remove", "--force", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "removed layout preset: dev") {
		t.Fatalf("stdout = %q, want removed message", got)
	}
	if _, err := os.Stat(filepath.Join(project, ".projmux", "layouts", "dev.toml")); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want removed preset", err)
	}
}

func TestLayoutRemoveWithoutForceDoesNotDeletePreset(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	path := filepath.Join(project, ".projmux", "layouts", "dev.toml")
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
`)
	cmd := &layoutCommand{
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	err := cmd.Run([]string{"remove", "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires --force") {
		t.Fatalf("error = %v, want force safety message", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want preset left in place", err)
	}
}

func TestLayoutApplyRequiresForceOrDryRun(t *testing.T) {
	t.Parallel()

	cmd := &layoutCommand{lookupEnv: func(string) string { return "" }}
	err := cmd.Run([]string{"apply", "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires --force") || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("error = %v, want force and dry-run guidance", err)
	}
}

func TestLayoutApplyForceRequiresCurrentSession(t *testing.T) {
	t.Parallel()

	cmd := &layoutCommand{lookupEnv: func(string) string { return "" }}
	err := cmd.Run([]string{"apply", "dev", "--force"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a current tmux session") {
		t.Fatalf("error = %v, want current-session failure", err)
	}
}

func TestLayoutApplyDryRunPrintsSessionStatePreview(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
description = "Daily dev"
default_cwd = "${PROJMUX_CWD}"

[[windows]]
index = 0
name = "main"
layout = "layout-a"
active_pane_index = 1

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
recipe = "shell"

[[windows.panes]]
index = 1
cwd = "${PROJMUX_CWD}/service"
command = "make watch"
`)
	service := filepath.Join(project, "service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{"#{session_name}": "workspace"}}
	cmd := &layoutCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply", "dev", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"Session State Restore Preview",
		"session:      workspace",
		"source:       layout(dev)",
		"window 0 main (2 panes)",
		"pane 0.1 startup make watch",
		"Dry run only; no tmux commands were executed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout missing %q:\n%s", want, output)
		}
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "tmux" || strings.Join(runner.calls[0].args, " ") != "display-message -p -F #{session_name}" {
		t.Fatalf("tmux calls = %#v, want only current-session resolution", runner.calls)
	}
}

func TestLayoutApplyDryRunFailsWithoutCurrentSession(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
`)
	cmd := &layoutCommand{
		runner: &recordingTmuxRunner{},
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
	}

	err := cmd.Run([]string{"apply", "dev", "--dry-run"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "current session") {
		t.Fatalf("error = %v, want current-session failure", err)
	}
}

func TestLayoutApplyForceReplaysPresetIntoCurrentSession(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "dev", `
schema_version = 1
description = "Daily dev"
default_cwd = "${PROJMUX_CWD}"

[[windows]]
index = 0
name = "main"
active_pane_index = 1

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
recipe = "shell"

[[windows.panes]]
index = 1
cwd = "${PROJMUX_CWD}/service"
command = "make watch"
`)
	service := filepath.Join(project, "service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &layoutApplyForceTestRunner{}
	cmd := &layoutCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply", "dev", "--force"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "applied layout preset: dev (1 window, 2 panes) -> workspace") {
		t.Fatalf("stdout = %q, want applied summary", got)
	}
	for _, want := range [][]string{
		{"move-window", "-d", "-k", "-s", "@20", "-t", "workspace:0"},
		{"kill-window", "-t", "@old"},
		{"select-window", "-t", "workspace:0"},
		{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_source", "layout(dev)"},
	} {
		if !layoutTestHasCall(runner.calls, want) {
			t.Fatalf("tmux calls = %#v, want call %#v", runner.calls, want)
		}
	}
	if got := layoutTestDisplayMessageCalls(runner.calls); got != 1 {
		t.Fatalf("display-message calls = %d in %#v, want one current-session resolution", got, runner.calls)
	}
}

func TestLayoutApplyForceFreshPresetMarksFreshSource(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	writeLayoutTestFile(t, project, "scratch", `
schema_version = 1
mode = "fresh-each-time"
default_cwd = "${PROJMUX_CWD}"

[[windows]]
index = 0
name = "main"
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
recipe = "shell"
	`)
	runner := &layoutApplyForceTestRunner{}
	sessionStore := sessionstate.NewStore(t.TempDir())
	if err := sessionStore.Save(sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: project,
		SavedAt:    time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "old",
			ActivePaneIndex: 0,
			Panes:           []sessionstate.Pane{{Index: 0, CWD: project, Recipe: sessionstate.ShellRecipe()}},
		}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cmd := &layoutCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return project
			case "TMUX":
				return "/tmp/tmux,1,0"
			default:
				return ""
			}
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionStore, nil },
	}

	if err := cmd.Run([]string{"apply", "scratch", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !layoutTestHasCall(runner.calls, []string{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_source", "fresh"}) {
		t.Fatalf("tmux calls = %#v, want fresh source marker", runner.calls)
	}
	if _, err := sessionStore.Load("workspace"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("fresh layout snapshot load error = %v, want %v", err, sessionstate.ErrNotFound)
	}
}

func TestLayoutRequiresProjectContext(t *testing.T) {
	t.Parallel()

	cmd := &layoutCommand{
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	err := cmd.Run([]string{"list"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a project context") {
		t.Fatalf("error = %v, want project context error", err)
	}
}

func newLayoutSaveTestRunner(project string) *recordingTmuxRunner {
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
	}, "\x1f")
	service := filepath.Join(project, "service")
	return &recordingTmuxRunner{
		formats: map[string]string{
			"#{session_name}": "workspace",
		},
		outputs: map[string]string{
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"): "0\x1fmain\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"): strings.Join([]string{
				"0\x1f0\x1fshell\x1f1\x1f" + project + "\x1f\x1f\x1f\x1f\x1f\x1f",
				"0\x1f1\x1fwatcher\x1f0\x1f" + service + "\x1fstartup\x1fmake watch\x1f\x1f\x1f\x1f",
				"",
			}, "\n"),
		},
	}
}

func writeLayoutTestFile(t *testing.T, project, name, body string) {
	t.Helper()
	path := filepath.Join(project, ".projmux", "layouts", name+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(body, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

type layoutApplyForceTestRunner struct {
	tempSession string
	calls       []recordedTmuxCall
}

func (r *layoutApplyForceTestRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) == 4 && reflect.DeepEqual(args[:3], []string{"display-message", "-p", "-F"}) && args[3] == "#{session_name}" {
		return []byte("workspace\n"), nil
	}
	if name == "tmux" && len(args) >= 4 && reflect.DeepEqual(args[:3], []string{"new-session", "-d", "-s"}) {
		r.tempSession = args[3]
		return nil, nil
	}
	if name == "tmux" && reflect.DeepEqual(args, []string{"list-windows", "-t", r.tempSession, "-F", "#{window_id}\t#{window_index}"}) {
		return []byte("@20\t0\n"), nil
	}
	if name == "tmux" && reflect.DeepEqual(args, []string{"list-windows", "-t", "workspace", "-F", "#{window_id}"}) {
		return []byte("@20\n@old\n"), nil
	}
	return nil, nil
}

func layoutTestHasCall(calls []recordedTmuxCall, args []string) bool {
	for _, call := range calls {
		if call.name == "tmux" && reflect.DeepEqual(call.args, args) {
			return true
		}
	}
	return false
}

func layoutTestDisplayMessageCalls(calls []recordedTmuxCall) int {
	count := 0
	for _, call := range calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "display-message" {
			count++
		}
	}
	return count
}
