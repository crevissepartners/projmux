package app

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

func TestDoctorGeneratedConfigRejectsUnsafeAndOversizeInputsWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(withTmuxConfigDigest("set -g @fixture 1\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		path  string
		setup func(string) error
	}{
		{name: "symlink", setup: func(path string) error { return os.Symlink(target, path) }},
		{name: "fifo", setup: func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{name: "directory", setup: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "device", path: "/dev/null", setup: func(string) error { return nil }},
		{name: "oversize", setup: func(path string) error {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return err
			}
			return os.Truncate(path, doctorGeneratedConfigMaxBytes+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if tc.path != "" {
				path = tc.path
			}
			if err := tc.setup(path); err != nil {
				t.Fatal(err)
			}
			cmd := newStubDoctorCommand("linux", map[string]bool{})
			cmd.resolveGeneratedConfig = func() (string, error) { return path, nil }
			cmd.readGeneratedConfig = doctorReadRegularFileBounded
			done := make(chan string, 1)
			go func() {
				state, _ := cmd.generatedConfigHealth()
				done <- state
			}()
			select {
			case state := <-done:
				if state != "unreadable" {
					t.Fatalf("state = %q", state)
				}
			case <-time.After(time.Second):
				t.Fatal("generated config inspection blocked")
			}
		})
	}
}

func TestDoctorUnsafeJournalSkipsOperationsReader(t *testing.T) {
	cmd, root := healthDoctor(t, diagnostics.ReadResult{}, nil, doctorRuntimeProbe{}, nil)
	path := filepath.Join(root, "state", "projmux", "logs", diagnostics.LogFileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.readRuntimeHealth = func(diagnostics.ReadOnlyStore) (diagnostics.RuntimeHealth, error) {
		t.Fatal("operations reader called for unsafe journal")
		return diagnostics.RuntimeHealth{}, nil
	}
	want := []doctorFinding{
		{Severity: doctorSeverityInfo, Code: "logs.state.ready", Remediation: doctorRemediationNone},
		{Severity: doctorSeverityInfo, Code: "logs.directory.ready", Remediation: doctorRemediationNone},
		{Severity: doctorSeverityError, Code: "logs.journal.unsafe-type", Remediation: doctorRemediationInspectJournal},
		{Severity: doctorSeverityWarning, Code: "logs.recent-errors.unavailable", Remediation: doctorRemediationInspectJournal},
	}
	got := cmd.evaluateLogFindings()
	if len(got) != len(want) {
		t.Fatalf("findings = %#v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %#v, want %#v", got, want)
	}
}
