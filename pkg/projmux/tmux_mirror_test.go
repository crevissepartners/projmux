package projmux

import (
	"context"
	"errors"
	"strings"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

type tmuxMirrorRunner struct {
	calls   [][]string
	replies map[string]string
	fail    bool
}

func (r *tmuxMirrorRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.fail {
		return nil, errors.New("no server")
	}
	return []byte(r.replies[strings.Join(args, " ")] + "\n"), nil
}

const (
	mirrorPaneKey   = "display-message -p -t %46 -F #{@projmux_pane_uid}"
	mirrorWindowKey = "display-message -p -t %46 -F #{@projmux_window_uid}"
)

func mirrorEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// The expectation table below repeats, row for row and value for value, the one
// TestActiveTargetLookupMatchesPublicTmuxMirror pins on the CLI lookup in
// internal/app, so a drift on either side fails one of the two.
func TestTmuxMirrorPublicEqualsActiveTargetObservation(t *testing.T) {
	t.Parallel()
	inside := map[string]string{"TMUX": "/tmp/tmux-1000/projmux,1,0", "TMUX_PANE": " %46 "}
	replies := map[string]string{mirrorPaneKey: " pan-alpha ", mirrorWindowKey: "win-alpha"}
	for _, test := range []struct {
		name    string
		env     map[string]string
		anchor  *string
		want    TmuxMirror
		wantRun int
	}{
		{name: "inherited inside", env: inside, want: TmuxMirror{PaneID: "%46", PaneUID: "pan-alpha", WindowUID: "win-alpha", Inside: true}, wantRun: 2},
		{name: "anchored inside", env: map[string]string{"TMUX": "x"}, anchor: ptr(" %46 "), want: TmuxMirror{PaneID: "%46", PaneUID: "pan-alpha", WindowUID: "win-alpha", Inside: true}, wantRun: 2},
		{name: "no environment", env: nil},
		{name: "TMUX without pane", env: map[string]string{"TMUX": "x"}},
		{name: "pane without TMUX", env: map[string]string{"TMUX_PANE": "%46"}},
		{name: "blank values", env: map[string]string{"TMUX": " ", "TMUX_PANE": " "}},
		{name: "anchored without TMUX", env: map[string]string{}, anchor: ptr("%46")},
		{name: "blank anchor", env: map[string]string{"TMUX": "x"}, anchor: ptr("  ")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &tmuxMirrorRunner{replies: replies}
			pane := func() (string, bool) { return store.InheritedTmuxPane(mirrorEnv(test.env)) }
			if test.anchor != nil {
				pane = func() (string, bool) { return store.AnchoredTmuxPane(*test.anchor, mirrorEnv(test.env)) }
			}
			got := readTmuxMirror(context.Background(), runner, pane)
			if got != test.want {
				t.Fatalf("mirror = %+v, want %+v", got, test.want)
			}
			if len(runner.calls) != test.wantRun {
				t.Fatalf("tmux calls = %v, want %d", runner.calls, test.wantRun)
			}
		})
	}
}

func TestTmuxMirrorReadFailureIsEmptyNotError(t *testing.T) {
	t.Parallel()
	runner := &tmuxMirrorRunner{fail: true}
	got := readTmuxMirror(context.Background(), runner, func() (string, bool) { return "%46", true })
	if got != (TmuxMirror{PaneID: "%46", Inside: true}) {
		t.Fatalf("mirror = %+v, want inside with empty uids", got)
	}
}

func TestTmuxMirrorNilLookupEnvIsOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/projmux,1,0")
	t.Setenv("TMUX_PANE", "%46")
	// A nil lookupEnv must not fall back to the process environment, which here
	// says the process is inside tmux.
	if got := ReadTmuxMirror(context.Background(), nil); got != (TmuxMirror{}) {
		t.Fatalf("ReadTmuxMirror(nil) = %+v", got)
	}
	if got := ReadTmuxMirrorAt(context.Background(), "%46", nil); got != (TmuxMirror{}) {
		t.Fatalf("ReadTmuxMirrorAt(nil) = %+v", got)
	}
}

func TestTmuxMirrorOutsideTmuxContactsNothing(t *testing.T) {
	t.Parallel()
	outside := func(string) string { return "" }
	if got := ReadTmuxMirror(context.Background(), outside); got != (TmuxMirror{}) {
		t.Fatalf("ReadTmuxMirror outside tmux = %+v", got)
	}
	if got := ReadTmuxMirrorAt(context.Background(), "%46", outside); got != (TmuxMirror{}) {
		t.Fatalf("ReadTmuxMirrorAt outside tmux = %+v", got)
	}
}

func ptr(s string) *string { return &s }
