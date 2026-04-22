package app

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/config"
)

func TestAppRunTagList(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	app := &App{
		tag: &tagCommand{
			store: &stubTagStore{
				list: []string{"session-a", "session-b"},
			},
		},
	}

	if err := app.Run([]string{"tag", "list"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "session-a\nsession-b\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTagCommandToggle(t *testing.T) {
	t.Parallel()

	store := &stubTagStore{toggleResult: true}
	cmd := &tagCommand{store: store}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle", "session-a"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle add error = %v", err)
	}
	if got, want := store.toggled, "session-a"; got != want {
		t.Fatalf("toggled tag = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "tagged: session-a\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	store.toggleResult = false
	stdout.Reset()
	if err := cmd.Run([]string{"toggle", "session-a"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle remove error = %v", err)
	}
	if got, want := stdout.String(), "untagged: session-a\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestTagCommandClear(t *testing.T) {
	t.Parallel()

	store := &stubTagStore{}
	cmd := &tagCommand{store: store}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"clear"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !store.cleared {
		t.Fatal("expected store.Clear to be called")
	}
	if got, want := stdout.String(), "cleared tags\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestTagCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "tag requires a subcommand"},
		{name: "unknown subcommand", args: []string{"unknown"}, want: "unknown tag subcommand: unknown"},
		{name: "list args", args: []string{"list", "extra"}, want: "tag list does not accept positional arguments"},
		{name: "toggle missing name", args: []string{"toggle"}, want: "tag toggle requires exactly 1 <name> argument"},
		{name: "toggle blank name", args: []string{"toggle", "   "}, want: "tag toggle requires a non-empty <name> argument"},
		{name: "clear args", args: []string{"clear", "extra"}, want: "tag clear does not accept positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&tagCommand{store: &stubTagStore{}}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestTagCommandPropagatesStoreSetupError(t *testing.T) {
	t.Parallel()

	cmd := &tagCommand{storeErr: errors.New("no home directory")}
	err := cmd.Run([]string{"list"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "configure tag store") {
		t.Fatalf("error = %v, want configure tag store", err)
	}
}

func TestNewTagCommandUsesDefaultStorePaths(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	cmd := newTagCommand()

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle", "session-a"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(toggle) error = %v", err)
	}

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}

	data, err := os.ReadFile(paths.TagFile())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "session-a\n"; got != want {
		t.Fatalf("tag file = %q, want %q", got, want)
	}
}

type stubTagStore struct {
	list         []string
	toggled      string
	toggleResult bool
	cleared      bool
	err          error
}

func (s *stubTagStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
}

func (s *stubTagStore) Toggle(name string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.toggled = name
	return s.toggleResult, nil
}

func (s *stubTagStore) Clear() error {
	if s.err != nil {
		return s.err
	}
	s.cleared = true
	return nil
}
