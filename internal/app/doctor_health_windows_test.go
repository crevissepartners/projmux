//go:build windows

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorWindowsReportsACLPrivacyAsUnverified(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "journal")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path      string
		directory bool
		prefix    string
	}{
		{path: root, directory: true, prefix: "logs.directory"},
		{path: file, prefix: "logs.journal"},
	} {
		findings := doctorPathFindings(tc.path, tc.directory, tc.prefix, doctorRemediationInspectLogs)
		if len(findings) != 2 || findings[0].Code != tc.prefix+".privacy-unverified" || findings[0].Severity != doctorSeverityWarning || findings[1].Code != tc.prefix+".ready" || findings[1].Severity != doctorSeverityInfo {
			t.Fatalf("findings = %#v", findings)
		}
	}
}

func TestDoctorWindowsKeepsWritabilityFindingSeparateFromPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly-journal")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	findings := doctorPathFindings(path, false, "logs.journal", doctorRemediationInspectJournal)
	if len(findings) != 2 || findings[0].Code != "logs.journal.privacy-unverified" || findings[1].Code != "logs.journal.not-writable" {
		t.Fatalf("findings = %#v", findings)
	}
}
