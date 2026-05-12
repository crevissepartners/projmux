package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
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
				"0\x1f0\x1f1\x1f" + project + "\x1f\x1f\x1f\x1f\x1f\x1f",
				"0\x1f1\x1f0\x1f" + service + "\x1fstartup\x1fmake watch\x1f\x1f\x1f\x1f",
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
