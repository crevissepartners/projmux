package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The saved Runtime diagnostics visibility policy.
//
// The assertions here are about the three answers a read can give -- nothing
// saved, a valid choice, something else -- and about the one thing a read must
// never do, which is write.

func TestRuntimeDiagnosticsVisibilityUnsetIsTheReadTimeDefault(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), RuntimeDiagnosticsVisibilityFileName)
	mode, origin, err := LoadRuntimeDiagnosticsVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadRuntimeDiagnosticsVisibilityFile() error = %v", err)
	}
	if mode != RuntimeDiagnosticsWhenNeeded {
		t.Fatalf("unset mode = %q, want %q", mode, RuntimeDiagnosticsWhenNeeded)
	}
	if origin != RuntimeDiagnosticsVisibilityDefaulted {
		t.Fatalf("unset origin = %q, want %q", origin, RuntimeDiagnosticsVisibilityDefaulted)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading an unset policy created %s, want no file written", path)
	}
}

func TestRuntimeDiagnosticsVisibilitySavedValueRoundTrips(t *testing.T) {
	t.Parallel()

	for _, want := range []RuntimeDiagnosticsVisibility{RuntimeDiagnosticsWhenNeeded, RuntimeDiagnosticsAlways} {
		path := filepath.Join(t.TempDir(), RuntimeDiagnosticsVisibilityFileName)
		if err := SaveRuntimeDiagnosticsVisibilityFile(path, want); err != nil {
			t.Fatalf("SaveRuntimeDiagnosticsVisibilityFile(%q) error = %v", want, err)
		}
		mode, origin, err := LoadRuntimeDiagnosticsVisibilityFile(path)
		if err != nil {
			t.Fatalf("LoadRuntimeDiagnosticsVisibilityFile() error = %v", err)
		}
		if mode != want {
			t.Fatalf("saved mode = %q, want %q", mode, want)
		}
		if origin != RuntimeDiagnosticsVisibilitySaved {
			t.Fatalf("saved origin = %q, want %q", origin, RuntimeDiagnosticsVisibilitySaved)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatalf("read config dir: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("config dir holds %d entries, want the saved file alone: %#v", len(entries), entries)
		}
	}
}

// TestRuntimeDiagnosticsVisibilityInvalidSavedValueFailsSafeWithoutWriting is
// the fail-safe half: an unrecognized value resolves to the default, says it is
// invalid rather than pretending nothing is saved, and leaves the bytes on disk
// exactly as the operator left them.
func TestRuntimeDiagnosticsVisibilityInvalidSavedValueFailsSafeWithoutWriting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), RuntimeDiagnosticsVisibilityFileName)
	const saved = "sometimes\n"
	if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
		t.Fatalf("write saved value: %v", err)
	}

	mode, origin, err := LoadRuntimeDiagnosticsVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadRuntimeDiagnosticsVisibilityFile() error = %v", err)
	}
	if mode != RuntimeDiagnosticsVisibilityDefault {
		t.Fatalf("invalid mode = %q, want the default %q", mode, RuntimeDiagnosticsVisibilityDefault)
	}
	if origin != RuntimeDiagnosticsVisibilityInvalid {
		t.Fatalf("invalid origin = %q, want %q", origin, RuntimeDiagnosticsVisibilityInvalid)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read saved value: %v", err)
	}
	if string(content) != saved {
		t.Fatalf("read rewrote the saved value to %q, want the untouched %q", string(content), saved)
	}
}

func TestNormalizeRuntimeDiagnosticsVisibilityNamesOnlyTheClosedChoice(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want RuntimeDiagnosticsVisibility
		ok   bool
	}{
		{"when-needed", RuntimeDiagnosticsWhenNeeded, true},
		{"  WHEN-NEEDED\n", RuntimeDiagnosticsWhenNeeded, true},
		{"always", RuntimeDiagnosticsAlways, true},
		{" Always ", RuntimeDiagnosticsAlways, true},
		{"", RuntimeDiagnosticsWhenNeeded, false},
		{"on", RuntimeDiagnosticsWhenNeeded, false},
		{"off", RuntimeDiagnosticsWhenNeeded, false},
		{"never", RuntimeDiagnosticsWhenNeeded, false},
	} {
		mode, ok := NormalizeRuntimeDiagnosticsVisibility(test.raw)
		if mode != test.want || ok != test.ok {
			t.Fatalf("NormalizeRuntimeDiagnosticsVisibility(%q) = (%q, %v), want (%q, %v)",
				test.raw, mode, ok, test.want, test.ok)
		}
	}
}

func TestSaveRuntimeDiagnosticsVisibilityRejectsAValueTheChoiceDoesNotName(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), RuntimeDiagnosticsVisibilityFileName)
	if err := SaveRuntimeDiagnosticsVisibilityFile(path, RuntimeDiagnosticsVisibility("sometimes")); err == nil {
		t.Fatal("SaveRuntimeDiagnosticsVisibilityFile() accepted an unknown choice, want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a rejected save created %s, want no file written", path)
	}
}
