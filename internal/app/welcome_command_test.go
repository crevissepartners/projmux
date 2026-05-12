package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestWelcomeCommandWritesShellGuide(t *testing.T) {
	t.Parallel()

	cmd := newWelcomeCommand(nil)
	cmd.lookupEnv = func(string) string { return "" }

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Welcome to projmux shell") {
		t.Fatalf("stdout = %q, want welcome heading", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestWelcomeCommandRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	cmd := newWelcomeCommand(nil)

	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"extra"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run(extra) returned nil, want error")
	}
	if !strings.Contains(err.Error(), "does not accept positional arguments") {
		t.Fatalf("err = %v, want positional-arg error", err)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestAppDispatchesWelcomeCommand(t *testing.T) {
	t.Parallel()

	app := New()
	app.welcome.lookupEnv = func(string) string { return "" }

	var stdout, stderr bytes.Buffer
	if err := app.Run([]string{"welcome"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run(welcome) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Welcome to projmux shell") {
		t.Fatalf("stdout = %q, want welcome output", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
