package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestAppRunPinList(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	app := &App{
		pin: &pinCommand{
			store: &stubPinStore{
				list: []string{"/tmp/app", "/tmp/lib"},
			},
		},
	}

	if err := app.Run([]string{"pin", "list"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "/tmp/app\n/tmp/lib\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestPinCommandAddNormalizesDir(t *testing.T) {
	t.Parallel()

	store := &stubPinStore{}
	cmd := &pinCommand{store: store}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"add", "/tmp/app//"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.added, "/tmp/app"; got != want {
		t.Fatalf("added pin = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /tmp/app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestPinCommandRemoveAndToggle(t *testing.T) {
	t.Parallel()

	store := &stubPinStore{toggleResult: true}
	cmd := &pinCommand{store: store}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"remove", "/tmp/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	if got, want := store.removed, "/tmp/app"; got != want {
		t.Fatalf("removed pin = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "unpinned: /tmp/app\n"; got != want {
		t.Fatalf("remove stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	if err := cmd.Run([]string{"toggle", "/tmp/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle add error = %v", err)
	}
	if got, want := store.toggled, "/tmp/app"; got != want {
		t.Fatalf("toggled pin = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /tmp/app\n"; got != want {
		t.Fatalf("toggle stdout = %q, want %q", got, want)
	}

	store.toggleResult = false
	stdout.Reset()
	if err := cmd.Run([]string{"toggle", "/tmp/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("toggle remove error = %v", err)
	}
	if got, want := stdout.String(), "unpinned: /tmp/app\n"; got != want {
		t.Fatalf("toggle stdout = %q, want %q", got, want)
	}
}

func TestPinCommandClear(t *testing.T) {
	t.Parallel()

	store := &stubPinStore{}
	cmd := &pinCommand{store: store}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"clear"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !store.cleared {
		t.Fatal("expected store.Clear to be called")
	}
	if got, want := stdout.String(), "cleared pins\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestPinCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing subcommand",
			args: nil,
			want: "pin requires a subcommand",
		},
		{
			name: "unknown subcommand",
			args: []string{"unknown"},
			want: "unknown pin subcommand: unknown",
		},
		{
			name: "list args",
			args: []string{"list", "extra"},
			want: "pin list does not accept positional arguments",
		},
		{
			name: "add missing dir",
			args: []string{"add"},
			want: "pin add requires exactly 1 <dir> argument",
		},
		{
			name: "clear args",
			args: []string{"clear", "extra"},
			want: "pin clear does not accept positional arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&pinCommand{store: &stubPinStore{}}).Run(tt.args, &bytes.Buffer{}, &stderr)
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

func TestPinCommandPropagatesStoreSetupError(t *testing.T) {
	t.Parallel()

	cmd := &pinCommand{storeErr: errors.New("no home directory")}
	err := cmd.Run([]string{"list"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "configure pin store") {
		t.Fatalf("error = %v, want configure pin store", err)
	}
}

func TestNewPinCommandUsesDefaultStorePaths(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	cmd := newPinCommand()

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"add", "/tmp/app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(add) error = %v", err)
	}

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}

	data, err := os.ReadFile(paths.PinFile())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "/tmp/app\n"; got != want {
		t.Fatalf("pin file = %q, want %q", got, want)
	}
}

type stubPinStore struct {
	list         []string
	added        string
	removed      string
	toggled      string
	toggleResult bool
	cleared      bool
	err          error
}

func (s *stubPinStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
}

func (s *stubPinStore) Add(pin string) error {
	if s.err != nil {
		return s.err
	}
	s.added = filepath.Clean(pin)
	return nil
}

func (s *stubPinStore) Remove(pin string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = filepath.Clean(pin)
	return nil
}

func (s *stubPinStore) Toggle(pin string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.toggled = filepath.Clean(pin)
	return s.toggleResult, nil
}

func (s *stubPinStore) Clear() error {
	if s.err != nil {
		return s.err
	}
	s.cleared = true
	return nil
}
