package hooks

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// openDevNull opens the null device read-only. It is a character device, so it
// is the input a mode-bit check wrongly accepts as a terminal.
func openDevNull(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestIsInteractiveReaderRejectsDevNullAndPipe(t *testing.T) {
	t.Parallel()

	if isInteractiveReader(openDevNull(t)) {
		t.Fatalf("isInteractiveReader(%s) = true, want false", os.DevNull)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})
	if isInteractiveReader(readEnd) {
		t.Fatalf("isInteractiveReader(pipe) = true, want false")
	}

	if isInteractiveReader(strings.NewReader("a\n")) {
		t.Fatalf("isInteractiveReader(strings.Reader) = true, want false")
	}
}

// newDevNullStdinRunner builds a Runner whose prompt input is the null device
// and whose trust prompt is the built-in terminal prompt.
func newDevNullStdinRunner(t *testing.T, trustPath string, prompt, logger *bytes.Buffer) *Runner {
	t.Helper()
	return &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		PromptReader:         openDevNull(t),
		PromptWriter:         prompt,
		Logger:               logger,
	}
}

func TestRunnerProjectTrustDevNullStdinWarnsWithoutPrompt(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	var prompt, logger bytes.Buffer
	runner := newDevNullStdinRunner(t, testTrustStorePath(t), &prompt, &logger)

	trusted, err := runner.AuthorizeProjectConfig(cwd)
	if err != nil {
		t.Fatalf("AuthorizeProjectConfig() error = %v", err)
	}
	if trusted {
		t.Fatalf("AuthorizeProjectConfig() = true, want false for an untrusted config")
	}
	if prompt.Len() != 0 {
		t.Fatalf("trust prompt written with /dev/null stdin:\n%s", prompt.String())
	}
	if got := strings.Count(logger.String(), "requires trust; skipping in non-interactive context"); got != 1 {
		t.Fatalf("non-interactive warning count = %d, want 1; log:\n%s", got, logger.String())
	}
}

func TestRunnerProjectTrustDevNullStdinHashChangedWarnsWithoutPrompt(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)
	if _, err := TrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	writeProjectConfig(t, cwd, `
[startup]
run = "echo updated"
`)
	var prompt, logger bytes.Buffer
	runner := newDevNullStdinRunner(t, trustPath, &prompt, &logger)

	trusted, err := runner.AuthorizeProjectConfig(cwd)
	if err != nil {
		t.Fatalf("AuthorizeProjectConfig() error = %v", err)
	}
	if trusted {
		t.Fatalf("AuthorizeProjectConfig() = true, want false for a changed config")
	}
	if prompt.Len() != 0 {
		t.Fatalf("trust prompt written with /dev/null stdin:\n%s", prompt.String())
	}
	log := logger.String()
	for _, want := range []string{"hash changed;", "skipping in non-interactive context"} {
		if !strings.Contains(log, want) {
			t.Fatalf("warning log missing %q:\n%s", want, log)
		}
	}
}
