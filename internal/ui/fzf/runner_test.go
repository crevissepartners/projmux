package fzf

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunnerRunInvokesFZFWithCandidates(t *testing.T) {
	t.Parallel()

	fake := &fakeCommand{stdout: "/tmp/project-b\n"}

	r := &runner{
		lookupPath: func(name string) (string, error) {
			if name != binaryName {
				t.Fatalf("lookupPath name = %q, want %q", name, binaryName)
			}
			return "/usr/bin/fzf", nil
		},
		newCommand: func(name string, args ...string) command {
			if name != "/usr/bin/fzf" {
				t.Fatalf("command name = %q, want /usr/bin/fzf", name)
			}
			if got, want := args, []string{"--prompt", "projmux popup> ", "--delimiter", "\t", "--with-nth", "1"}; !equalStrings(got, want) {
				t.Fatalf("command args = %q, want %q", got, want)
			}
			return fake
		},
	}

	got, err := r.Run(Options{
		UI:         "popup",
		Candidates: []string{"/tmp/project-a", "/tmp/project-b"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "/tmp/project-b" {
		t.Fatalf("Run() = %q, want /tmp/project-b", got)
	}
	if got, want := fake.stdin.String(), "/tmp/project-a\t/tmp/project-a\n/tmp/project-b\t/tmp/project-b"; got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
}

func TestRunnerRunReturnsHiddenEntryValue(t *testing.T) {
	t.Parallel()

	fake := &fakeCommand{stdout: "dotfiles  [existing]  /home/tester/dotfiles\t/home/tester/dotfiles\n"}

	r := &runner{
		lookupPath: func(string) (string, error) { return "/usr/bin/fzf", nil },
		newCommand: func(string, ...string) command { return fake },
	}

	got, err := r.Run(Options{
		UI: "popup",
		Entries: []Entry{
			{Label: "dotfiles  [existing]  /home/tester/dotfiles", Value: "/home/tester/dotfiles"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "/home/tester/dotfiles" {
		t.Fatalf("Run() = %q, want hidden value", got)
	}
	if got, want := fake.stdin.String(), "dotfiles  [existing]  /home/tester/dotfiles\t/home/tester/dotfiles"; got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
}

func TestRunnerRunReportsUnavailableBinary(t *testing.T) {
	t.Parallel()

	r := &runner{
		lookupPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
		newCommand: func(string, ...string) command {
			t.Fatal("newCommand should not be called")
			return nil
		},
	}

	_, err := r.Run(Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fzf is not available") {
		t.Fatalf("error = %v, want unavailable message", err)
	}
}

func TestRunnerRunReportsExecutionFailure(t *testing.T) {
	t.Parallel()

	r := &runner{
		lookupPath: func(string) (string, error) { return "/usr/bin/fzf", nil },
		newCommand: func(string, ...string) command {
			return &fakeCommand{
				runErr: errors.New("exit status 2"),
				stderr: "broken\n",
			}
		},
	}

	_, err := r.Run(Options{UI: "sidebar"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "run fzf") || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %v, want runner failure with stderr", err)
	}
}

type fakeCommand struct {
	stdin  bytes.Buffer
	stdout string
	stderr string
	runErr error
}

func (c *fakeCommand) SetStdin(r io.Reader) {
	_, _ = io.Copy(&c.stdin, r)
}

func (c *fakeCommand) SetStdout(w io.Writer) {
	if c.stdout == "" {
		return
	}
	_, _ = io.WriteString(w, c.stdout)
}

func (c *fakeCommand) SetStderr(w io.Writer) {
	if c.stderr == "" {
		return
	}
	_, _ = io.WriteString(w, c.stderr)
}

func (c *fakeCommand) Run() error {
	return c.runErr
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
