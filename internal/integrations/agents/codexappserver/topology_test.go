package codexappserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestInstallCapabilityTopologyMatrix(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	managedPayload := filepath.Join(codexHome, "packages", "standalone", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(managedPayload), 0o700); err != nil {
		t.Fatal(err)
	}
	pathCLI := filepath.Join(root, "path", "codex")
	if err := os.MkdirAll(filepath.Dir(pathCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathCLI, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		codexHome string
		path      string
		prepare   func(t *testing.T)
		want      InstallCapability
	}{
		{name: "external CLI only", codexHome: codexHome, path: filepath.Dir(pathCLI), want: InstallCapabilityExternalCLIOnly},
		{
			name:      "managed standalone",
			codexHome: codexHome,
			path:      filepath.Dir(pathCLI),
			prepare: func(t *testing.T) {
				if err := os.WriteFile(managedPayload, []byte("managed"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(managedPayload) })
			},
			want: InstallCapabilityManagedReady,
		},
		{name: "CLI missing", codexHome: codexHome, path: filepath.Join(root, "empty-path"), want: InstallCapabilityCLIMissing},
		{name: "relative CODEX_HOME", codexHome: "relative", path: filepath.Dir(pathCLI), want: InstallCapabilityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prepare != nil {
				tt.prepare(t)
			}
			t.Setenv("CODEX_HOME", tt.codexHome)
			t.Setenv("PATH", tt.path)
			if got := ObserveDefaultInstallCapability(); got != tt.want {
				t.Fatalf("capability = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallCapabilityFallsBackToAbsoluteUserHome(t *testing.T) {
	info := fakeTopologyFileInfo{mode: 0o700}
	var observedPath string
	got := observeInstallCapability(
		func(string) string { return "" },
		func() (string, error) { return "/safe/home", nil },
		func(string) (string, error) { return "/path/codex", nil },
		func(path string) (os.FileInfo, error) {
			observedPath = path
			return info, nil
		},
		"linux",
	)
	if got != InstallCapabilityManagedReady {
		t.Fatalf("capability = %q", got)
	}
	want := filepath.Join("/safe/home", ".codex", "packages", "standalone", "current", "bin", "codex")
	if observedPath != want {
		t.Fatalf("observed path = %q, want canonical %q", observedPath, want)
	}

	for _, tc := range []struct {
		name string
		home string
		err  error
	}{
		{name: "relative", home: "relative"},
		{name: "unavailable", err: errors.New("no home")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := observeInstallCapability(
				func(string) string { return "" },
				func() (string, error) { return tc.home, tc.err },
				func(string) (string, error) { return "/path/codex", nil },
				func(string) (os.FileInfo, error) { called = true; return info, nil },
				"linux",
			)
			if got != InstallCapabilityUnknown || called {
				t.Fatalf("capability=%q stat-called=%v", got, called)
			}
		})
	}
}

func TestInstallCapabilityDoesNotInspectPayloadWhenCLIIsMissing(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want InstallCapability
	}{
		{name: "not found", err: exec.ErrNotFound, want: InstallCapabilityCLIMissing},
		{name: "wrapped not found", err: fmt.Errorf("lookup: %w", exec.ErrNotFound), want: InstallCapabilityCLIMissing},
		{name: "relative path refused", err: exec.ErrDot, want: InstallCapabilityUnknown},
		{name: "permission-like failure", err: os.ErrPermission, want: InstallCapabilityUnknown},
		{name: "arbitrary lookup failure", err: errors.New("lookup failed"), want: InstallCapabilityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			got := observeInstallCapability(
				func(string) string { return "/private/codex-home" },
				func() (string, error) { return "", errors.New("must not run") },
				func(string) (string, error) { return "", tt.err },
				func(string) (os.FileInfo, error) { called = true; return nil, errors.New("must not run") },
				"linux",
			)
			if got != tt.want || called {
				t.Fatalf("capability=%q want=%q stat-called=%v", got, tt.want, called)
			}
		})
	}
}

func TestDefaultInstallCapabilityObserverDoesNotWriteCodexHome(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	payload := filepath.Join(codexHome, "packages", "standalone", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("managed-payload-sentinel"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(codexHome, "user-state-sentinel")
	if err := os.WriteFile(marker, []byte("must-remain-byte-identical"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathDir := filepath.Join(root, "path")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", pathDir)
	before := snapshotTopologyTree(t, codexHome)
	if got := ObserveDefaultInstallCapability(); got != InstallCapabilityManagedReady {
		t.Fatalf("capability = %q", got)
	}
	after := snapshotTopologyTree(t, codexHome)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("observer changed CODEX_HOME tree:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func snapshotTopologyTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := "directory"
		if !entry.IsDir() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = fmt.Sprintf("%x", sha256.Sum256(contents))
		}
		snapshot = append(snapshot, fmt.Sprintf("%s|%s|%d|%s", relative, info.Mode(), info.Size(), digest))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(snapshot)
	return snapshot
}

type fakeTopologyFileInfo struct{ mode os.FileMode }

func (fakeTopologyFileInfo) Name() string        { return "codex" }
func (fakeTopologyFileInfo) Size() int64         { return 0 }
func (i fakeTopologyFileInfo) Mode() os.FileMode { return i.mode }
func (fakeTopologyFileInfo) ModTime() time.Time  { return time.Time{} }
func (i fakeTopologyFileInfo) IsDir() bool       { return i.mode.IsDir() }
func (fakeTopologyFileInfo) Sys() any            { return nil }
