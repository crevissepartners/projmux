package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
