package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestProfileInspectionOutcomesWriteNothing(t *testing.T) {
	for _, verb := range []string{"parse", "preview"} {
		for _, tc := range []struct {
			name  string
			err   error
			usage bool
		}{
			{"success", nil, false},
			{"refusal", errors.New("profile-syntax-invalid"), true},
			{"io", errors.New("read failed"), false},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				before, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "absent", "diagnostics.jsonl")
				if err := RecordOutcome(NewStore(path), []string{"profile", verb}, "test", "test", "tmux", time.Now(), tc.err, tc.usage, false); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("read-only %s %s created journal directories: %v", verb, tc.name, after)
				}
			})
		}
	}
	// Preserve the established outcome policy for existing profile mutations.
	if JournalForbidden([]string{"profile", "set"}) || JournalForbidden([]string{"profile", "show"}) {
		t.Fatal("existing profile journal policy changed")
	}
}
