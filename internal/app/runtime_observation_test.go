package app

import (
	"context"
	"errors"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// runtime_observation_test.go covers the live-tmux observation seam: the
// snapshot the read verbs derive Window and Pane status from.
//
// The subject is a diff between the registry and the machine, so every test
// here states the machine as a mirrored-uid set and never as a stored bool. The
// stored bool is precisely what these tests exist to keep out of the answer.

// stubRuntimeInventory is the mirrored-uid inventory seam with a scripted
// answer. It counts its calls, because "one observation per invocation" is part
// of the contract: a route that resolves several times must still read the
// machine once.
type stubRuntimeInventory struct {
	windows     map[string]bool
	panes       map[string]bool
	windowErr   error
	paneErr     error
	windowCalls int
	paneCalls   int
}

func (s *stubRuntimeInventory) LiveWindowUIDs(context.Context) (map[string]bool, error) {
	s.windowCalls++
	if s.windowErr != nil {
		return nil, s.windowErr
	}
	return s.windows, nil
}

func (s *stubRuntimeInventory) LivePaneUIDs(context.Context) (map[string]bool, error) {
	s.paneCalls++
	if s.paneErr != nil {
		return nil, s.paneErr
	}
	return s.panes, nil
}

func uidSet(uids ...string) map[string]bool {
	out := make(map[string]bool, len(uids))
	for _, uid := range uids {
		out[uid] = true
	}
	return out
}

// fixedRuntimeLookup is a runtimeLookup over a literal observation, for the
// route tests that only need to state which objects are live.
func fixedRuntimeLookup(windows, panes []string) runtimeLookup {
	observation := coremetadata.RuntimeObservation{Windows: uidSet(windows...), Panes: uidSet(panes...)}
	return func() coremetadata.RuntimeObservation { return observation }
}

// liveAlphaRuntime is the observation the read-verb route tests run against.
//
// Every Window and Pane of the fixture Project "alpha" is mirrored on a live
// tmux object; nothing under the offline "beta" or the MissingRoot "gone" is.
// That is what makes the fixtures' `status=live` expectations mean something:
// before this seam existed they were satisfied by a bool nobody had refreshed
// since the Project was imported.
func liveAlphaRuntime() runtimeLookup {
	return fixedRuntimeLookup(
		[]string{"win-alpha-main", "win-alpha-review"},
		[]string{"pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review"},
	)
}

// TestRuntimeObservationIsTakenOncePerInvocation pins the memoization. The
// budget is two tmux queries for a whole invocation regardless of how many
// resolutions it runs, and every resolution must judge against the same
// snapshot so one invocation cannot report a Pane live and then offline.
func TestRuntimeObservationIsTakenOncePerInvocation(t *testing.T) {
	t.Parallel()

	inventory := &stubRuntimeInventory{windows: uidSet("win-1"), panes: uidSet("pan-1")}
	lookup := tmuxRuntimeLookup(inventory)

	for range 5 {
		observed := lookup()
		if !observed.BoundWindow("win-1") || !observed.BoundPane("pan-1") {
			t.Fatalf("observation lost its bindings: %+v", observed)
		}
	}
	if inventory.windowCalls != 1 || inventory.paneCalls != 1 {
		t.Fatalf("observed the machine %d window / %d pane times, want 1 each",
			inventory.windowCalls, inventory.paneCalls)
	}
}

// TestRuntimeObservationFailsToOfflineNeverToStored is the fail-closed rule.
//
// A tmux query that errors -- above all "no server running", which is the
// truthful answer of a machine with nothing live on it -- leaves that half of
// the observation empty. Empty can only downgrade a resource to offline. It can
// never invent a live one, and it never falls back to a stored value, which is
// the whole failure mode this change removes.
func TestRuntimeObservationFailsToOfflineNeverToStored(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		inventory  *stubRuntimeInventory
		wantWindow bool
		wantPane   bool
	}{
		{
			name:       "both queries answer",
			inventory:  &stubRuntimeInventory{windows: uidSet("win-1"), panes: uidSet("pan-1")},
			wantWindow: true, wantPane: true,
		},
		{
			name: "window query fails, the pane half still answers",
			inventory: &stubRuntimeInventory{
				windowErr: errors.New("no server running on /tmp/tmux-1000/projmux"),
				panes:     uidSet("pan-1"),
			},
			wantWindow: false, wantPane: true,
		},
		{
			name: "pane query fails, the window half still answers",
			inventory: &stubRuntimeInventory{
				windows: uidSet("win-1"),
				paneErr: errors.New("no server running on /tmp/tmux-1000/projmux"),
			},
			wantWindow: true, wantPane: false,
		},
		{
			name: "both queries fail",
			inventory: &stubRuntimeInventory{
				windowErr: errors.New("lost server"),
				paneErr:   errors.New("lost server"),
			},
			wantWindow: false, wantPane: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRuntime(context.Background(), test.inventory)
			if got := observed.BoundWindow("win-1"); got != test.wantWindow {
				t.Fatalf("BoundWindow(win-1) = %t, want %t", got, test.wantWindow)
			}
			if got := observed.BoundPane("pan-1"); got != test.wantPane {
				t.Fatalf("BoundPane(pan-1) = %t, want %t", got, test.wantPane)
			}
		})
	}
}

// TestNilRuntimeLookupIsTheEmptyObservation pins the default of a route that
// has not opted in. It must be "nothing is bound", not "ask the registry".
func TestNilRuntimeLookupIsTheEmptyObservation(t *testing.T) {
	t.Parallel()

	var lookup runtimeLookup
	observed := lookup.observation()
	if observed.BoundWindow("win-1") || observed.BoundPane("pan-1") {
		t.Fatalf("a nil lookup reported a binding: %+v", observed)
	}
}
