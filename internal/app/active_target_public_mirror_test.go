package app

import (
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// TestActiveTargetLookupMatchesPublicTmuxMirror pins the CLI lookup to the same
// environment table and expected four values as the public ReadTmuxMirror test
// TestTmuxMirrorPublicEqualsActiveTargetObservation in pkg/projmux: paneID, the
// mirrored Pane uid, the mirrored Window uid and inside. A drift on either side
// fails one of the two tests.
func TestActiveTargetLookupMatchesPublicTmuxMirror(t *testing.T) {
	t.Parallel()
	replies := map[string]string{
		"display-message -p -t %46 -F #{@projmux_pane_uid}":   " pan-alpha ",
		"display-message -p -t %46 -F #{@projmux_window_uid}": "win-alpha",
	}
	type observed struct {
		paneID, paneUID, windowUID string
		inside                     bool
	}
	inside := observed{"%46", "pan-alpha", "win-alpha", true}
	for _, test := range []struct {
		name    string
		env     map[string]string
		anchor  *string
		want    observed
		wantRun int
	}{
		{name: "inherited inside", env: map[string]string{"TMUX": "/tmp/tmux-1000/projmux,1,0", "TMUX_PANE": " %46 "}, want: inside, wantRun: 2},
		{name: "anchored inside", env: map[string]string{"TMUX": "x"}, anchor: strPtr(" %46 "), want: inside, wantRun: 2},
		{name: "no environment", env: nil},
		{name: "TMUX without pane", env: map[string]string{"TMUX": "x"}},
		{name: "pane without TMUX", env: map[string]string{"TMUX_PANE": "%46"}},
		{name: "blank values", env: map[string]string{"TMUX": " ", "TMUX_PANE": " "}},
		{name: "anchored without TMUX", env: map[string]string{}, anchor: strPtr("%46")},
		{name: "blank anchor", env: map[string]string{"TMUX": "x"}, anchor: strPtr("  ")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &activeTargetTmuxRunner{replies: replies}
			getenv := func(key string) string { return test.env[key] }
			mirror := intmetadata.NewMirror(runner)
			lookup := tmuxActiveTargetLookup(getenv, mirror)
			if test.anchor != nil {
				lookup = anchoredActiveTargetLookup(*test.anchor, getenv, mirror)
			}
			var got observed
			if observer, ok := lookup(); ok {
				got = observed{observer.paneID, observer.mirroredPaneUID(), observer.mirroredWindowUID(), true}
			}
			if got != test.want {
				t.Fatalf("observation = %+v, want %+v", got, test.want)
			}
			if len(runner.calls) != test.wantRun {
				t.Fatalf("tmux calls = %v, want %d", runner.calls, test.wantRun)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
