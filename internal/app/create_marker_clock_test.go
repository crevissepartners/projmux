package app

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNewCreateOperationMarkerAtStampsTheGivenClock pins the marker format and
// proves the unix field is read from the supplied instant, not the wall clock.
func TestNewCreateOperationMarkerAtStampsTheGivenClock(t *testing.T) {
	t.Parallel()
	fixed := time.Unix(1_600_000_000, 0).UTC()
	want := fmt.Sprintf("v1:%d:%d:op-fixed", os.Getpid(), fixed.Unix())
	if got := newCreateOperationMarkerAt(" op-fixed ", fixed); got != want {
		t.Fatalf("newCreateOperationMarkerAt = %q, want %q", got, want)
	}
	if !activeCreateOperationMarker(want, fixed.Add(time.Minute)) {
		t.Fatalf("marker %q is not active one minute after its own clock", want)
	}
}

// TestCreateTransactMarkerCarriesInjectedClock is the C-1 enforcement: a create
// transaction stamps its operation marker from the injected createCommand clock,
// so the tmux call ledger two byte-equivalent runs compare cannot drift across a
// second boundary.
func TestCreateTransactMarkerCarriesInjectedClock(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("create window error = %v", err)
	}
	want := fmt.Sprintf("v1:%d:%d:op-test", os.Getpid(), testCreateOperationClock.Unix())

	// The ledger marker reaches tmux twice: as the create lease (new-session
	// -e NAME=marker or set-environment NAME marker) and as the finalize lease
	// (set-environment NAME marker). Every stamped operand must be the injected
	// instant; the -u unset calls carry no value and are skipped.
	var recorded []string
	for _, call := range tmux.calls {
		for i, operand := range call {
			if marker, ok := strings.CutPrefix(operand, createOperationEnvironment+"="); ok {
				recorded = append(recorded, marker)
				continue
			}
			if (operand == createOperationEnvironment || operand == finalizeOperationEnvironment) &&
				i+1 < len(call) && !slices.Contains(call, "-u") {
				recorded = append(recorded, call[i+1])
			}
		}
	}
	if len(recorded) == 0 {
		t.Fatalf("no tmux call carried %s=: %v", createOperationEnvironment, tmux.calls)
	}
	for _, marker := range recorded {
		if marker != want {
			t.Fatalf("tmux call ledger marker = %q, want %q", marker, want)
		}
	}
}
