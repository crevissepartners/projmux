package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const closedStartupReclaimLinePrefix = "reclaimed retired closed-Project startup setting: "

// applyClosedStartupReclaim runs a real `tmux apply` and returns only the
// closed-Project startup reclaim lines it printed.
func applyClosedStartupReclaim(t *testing.T, f *reclaimFixture) []string {
	t.Helper()
	args := []string{"--config", filepath.Join(f.home, "generated", "tmux.conf"), "--no-reload"}
	var stdout, stderr bytes.Buffer
	if err := f.command().runApply(args, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var lines []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.HasPrefix(line, closedStartupReclaimLinePrefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestConfigApplyReclaimsTheRetiredClosedStartupFile is the retirement of the
// closed-Project startup setting on disk: whatever the file saved, the first
// apply removes it and reports it once, and every later apply is a quiet no-op.
func TestConfigApplyReclaimsTheRetiredClosedStartupFile(t *testing.T) {
	for _, saved := range []string{"on\n", "off\n", "garbage"} {
		t.Run(strings.TrimSpace(saved), func(t *testing.T) {
			f := newReclaimFixture(t)
			path := filepath.Join(f.configDir, retiredClosedStartupFileName)
			reclaimWrite(t, path, saved)
			neighbor := filepath.Join(f.configDir, "unrelated-note")
			reclaimWrite(t, neighbor, "keep\n")

			if got, want := applyClosedStartupReclaim(t, f), []string{closedStartupReclaimLinePrefix + "removed 1 file"}; !slices.Equal(got, want) {
				t.Fatalf("first apply lines = %q, want %q", got, want)
			}
			if reclaimExists(t, path) {
				t.Fatalf("%s survived the apply", path)
			}
			if got, err := os.ReadFile(neighbor); err != nil || string(got) != "keep\n" {
				t.Fatalf("neighbor = %q, %v; want it untouched", got, err)
			}

			before := reclaimTree(t, f.home)
			if lines := applyClosedStartupReclaim(t, f); len(lines) != 0 {
				t.Fatalf("second apply printed %q, want nothing", lines)
			}
			if after := reclaimTree(t, f.home); !slices.Equal(before, after) {
				t.Fatalf("second apply changed the tree:\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

// TestConfigApplyKeepsANonRegularClosedStartupEntry never follows or removes
// anything but a regular file under the retired name.
func TestConfigApplyKeepsANonRegularClosedStartupEntry(t *testing.T) {
	f := newReclaimFixture(t)
	outside := filepath.Join(f.home, "outside", "keep")
	reclaimWrite(t, outside, "outside\n")
	link := filepath.Join(f.configDir, retiredClosedStartupFileName)
	reclaimSymlink(t, outside, link)

	if lines := applyClosedStartupReclaim(t, f); len(lines) != 0 {
		t.Fatalf("apply printed %q, want nothing for a kept entry", lines)
	}
	if target, err := os.Readlink(link); err != nil || target != outside {
		t.Fatalf("symlink = %q, %v; want it left pointing at %s", target, err, outside)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside\n" {
		t.Fatalf("symlink target = %q, %v; want it untouched", got, err)
	}
}

// TestConfigApplyClosedStartupReclaimFailureDoesNotFailTheApply keeps the
// apply result independent of the reclaim, like the other retired-file steps.
func TestConfigApplyClosedStartupReclaimFailureDoesNotFailTheApply(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission")
	}
	f := newReclaimFixture(t)
	path := filepath.Join(f.configDir, retiredClosedStartupFileName)
	reclaimWrite(t, path, "off\n")
	if err := os.Chmod(f.configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.configDir, 0o755) })

	lines := applyClosedStartupReclaim(t, f)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], closedStartupReclaimLinePrefix+"failed (") {
		t.Fatalf("apply lines = %q, want one failed report", lines)
	}
	if !reclaimExists(t, path) {
		t.Fatalf("%s was removed through a read-only directory", path)
	}
}
