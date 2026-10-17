package diagnostics

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// The strict read/validate boundary must not suppress the existing policy
// controls, writes, other Agent routes, or similarly named unknown actions.
func TestGuidanceNoWriteBoundaryPreservesOtherOutcomes(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "guidance", "show"},
		{"agent", "guidance", "enable"},
		{"agent", "guidance", "disable"},
		{"agent", "guidance", "set"},
		{"agent", "guidance", "reset"},
		{"agent", "guidance", "reader"},
		{"agent", "guidance", "validate-extra"},
		{"agent", "status"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
			if err := RecordOutcome(store, args, "guidance-control", "test", "tmux", time.Now(), errors.New("controlled failure"), false, false); err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil || len(events) != 1 || events[0].Result != "error" {
				t.Fatalf("changed existing outcome policy for %v: %+v %v", args, events, err)
			}
		})
	}
}
