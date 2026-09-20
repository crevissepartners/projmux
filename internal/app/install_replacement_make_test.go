package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the real install recipe with an inert executable. The build is marked
// old, every command only writes its argv, and publication is inside TempDir:
// no tmux, broker, shared binary, or user config is reached.
func TestMakeInstallPropagatesReplacementFailureAfterResidue(t *testing.T) {
	t.Parallel()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	makefile, err := filepath.Abs("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		replacementStatus string
		residueStatus     string
		wantFailure       bool
	}{
		{name: "successful replacement", replacementStatus: "0", residueStatus: "0"},
		{name: "unreachable replacement", replacementStatus: "1", residueStatus: "0", wantFailure: true},
		{name: "both diagnostics fail", replacementStatus: "7", residueStatus: "9", wantFailure: true},
		{name: "residue stays best effort", replacementStatus: "0", residueStatus: "9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := filepath.Join(root, "fixture")
			log := filepath.Join(root, "calls")
			installed := filepath.Join(root, "bin", "projmux")
			body := `#!/bin/sh
printf '%s\n' "$*" >> "$INSTALL_TEST_CALLS"
case "$*" in
  'internal install-replace') exit "$INSTALL_TEST_REPLACEMENT_STATUS" ;;
  'internal install-residue') exit "$INSTALL_TEST_RESIDUE_STATUS" ;;
esac
`
			if err := os.WriteFile(fixture, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(makePath, "--no-print-directory", "-f", makefile, "-o", "build", "install", // #nosec G204 -- make and fixture arguments are this test's own.
				"GO=true", "PROJMUX_BIN="+fixture, "INSTALL_DIR="+filepath.Dir(installed))
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"INSTALL_TEST_CALLS="+log,
				"INSTALL_TEST_REPLACEMENT_STATUS="+tc.replacementStatus,
				"INSTALL_TEST_RESIDUE_STATUS="+tc.residueStatus,
				"MAKEFLAGS=", "MFLAGS=")
			output, runErr := cmd.CombinedOutput()
			if (runErr != nil) != tc.wantFailure {
				t.Fatalf("make install = %v, want failure %v:\n%s", runErr, tc.wantFailure, output)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "config apply --bin " + installed + " --socket projmux\n" +
				"config apply --socket projmux\nnotification reconcile\ninternal install-replace\ninternal install-residue\n"
			if string(calls) != wantCalls {
				t.Fatalf("install command sequence = %q, want %q", calls, wantCalls)
			}
			published, err := os.ReadFile(installed)
			if err != nil || string(published) != body {
				t.Fatalf("replacement failure undid binary publication: %v", err)
			}
			wantOutput := ">> converging live config before binary publication...\n" +
				">> atomically replaced " + installed + "\n" +
				">> verifying post-publication live config...\n" +
				">> reconciling notify queue...\n" +
				">> replacing long-lived processes running the superseded image...\n"
			if !tc.wantFailure && string(output) != wantOutput {
				t.Fatalf("successful output = %q, want %q", output, wantOutput)
			}
			if tc.wantFailure && !strings.Contains(string(output), "Error "+tc.replacementStatus) {
				t.Fatalf("make did not retain replacement status %s: %s", tc.replacementStatus, output)
			}
		})
	}
}
