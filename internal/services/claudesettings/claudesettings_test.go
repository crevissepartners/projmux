package claudesettings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requireNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".projmux-") {
			t.Fatalf("temporary file %s left in %s", entry.Name(), dir)
		}
	}
}

func TestReplaceWritesWithoutBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := Replace(path, nil, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, nil, []byte("{\"a\":1}\n")); err != nil {
		t.Fatalf("Replace without a base = %v, want an unchecked write", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\"a\":1}\n" {
		t.Fatalf("settings = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want the file's own 0600", info.Mode().Perm(), err)
	}
	requireNoTemp(t, filepath.Dir(path))
}

func TestReplaceComparesBase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial *string
		base    Base
		refused bool
	}{
		{name: "same bytes", initial: ptr("{}\n"), base: Base{Exists: true, Data: []byte("{}\n")}},
		{name: "absent as planned", base: Base{}},
		{name: "empty as planned", initial: ptr(""), base: Base{Exists: true}},
		{name: "other bytes", initial: ptr("{}\n"), base: Base{Exists: true, Data: []byte("{ }\n")}, refused: true},
		{name: "empty file planned absent", initial: ptr(""), base: Base{}, refused: true},
		{name: "absent file planned empty", base: Base{Exists: true}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if tc.initial != nil {
				if err := os.WriteFile(path, []byte(*tc.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			base := tc.base
			err := Replace(path, &base, []byte("next\n"))
			data, readErr := os.ReadFile(path)
			if !tc.refused {
				if err != nil || string(data) != "next\n" {
					t.Fatalf("Replace = %v, settings = %q; want written", err, data)
				}
				return
			}
			var changed *ChangedError
			if !errors.As(err, &changed) || changed.Path != path || !strings.HasPrefix(err.Error(), CodeChanged+": ") {
				t.Fatalf("Replace = %v, want %s", err, CodeChanged)
			}
			switch {
			case tc.initial == nil && !errors.Is(readErr, os.ErrNotExist):
				t.Fatalf("settings = %q, %v; want left absent", data, readErr)
			case tc.initial != nil && string(data) != *tc.initial:
				t.Fatalf("settings = %q, want left %q", data, *tc.initial)
			}
			requireNoTemp(t, dir)
		})
	}
}

func TestReplaceFollowsLinkChain(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	middle := filepath.Join(dir, "middle.json")
	if err := os.Symlink("real/settings.json", middle); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.Symlink(middle, path); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, &Base{Exists: true, Data: []byte("{}\n")}, []byte("next\n")); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{path, middle} {
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s = %v, %v; want the link kept", link, info, err)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "next\n" {
		t.Fatalf("target = %q, %v", data, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	requireNoTemp(t, filepath.Dir(target))
}

func TestReplaceRefusesLinkLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, nil, []byte("next\n")); err == nil || !strings.Contains(err.Error(), "too many levels of symbolic links") {
		t.Fatalf("Replace = %v, want a refused link loop", err)
	}
	requireNoTemp(t, dir)
}

func TestReplaceLeavesNoTempWhenCreateFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := Replace(path, nil, []byte("next\n")); err == nil {
		t.Fatal("Replace in a read-only directory = nil, want an error")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	requireNoTemp(t, dir)
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}\n" {
		t.Fatalf("settings = %q, %v; want unchanged", data, err)
	}
}

func ptr[T any](v T) *T { return &v }
