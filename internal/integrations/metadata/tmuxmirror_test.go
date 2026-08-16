package metadata

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// fakeRunner records every tmux invocation and replays canned output keyed by
// the first argument plus a discriminating flag.
type fakeRunner struct {
	calls   []string
	outputs map[string]string
	err     error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.err != nil {
		return nil, f.err
	}
	for key, out := range f.outputs {
		if strings.Contains(strings.Join(args, " "), key) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

func TestMirrorWritesResourceIdentityIntoScopedTmuxOptionsAndTurnsOffAutomaticRename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		run   func(m Mirror) error
		want  []string
		never []string
	}{
		{
			name: "project mirrors onto its session",
			run: func(m Mirror) error {
				return m.MirrorProject(context.Background(), "projmux", coremetadata.Project{
					Metadata: coremetadata.ObjectMeta{UID: "proj-1", Name: "projmux"},
				})
			},
			want: []string{
				"tmux set-option -t projmux -q @projmux_project_uid proj-1",
				"tmux set-option -t projmux -q @projmux_project_name projmux",
			},
		},
		{
			name: "window mirrors window-scoped options and pins the name",
			run: func(m Mirror) error {
				return m.MirrorWindow(context.Background(), "projmux:0", coremetadata.Window{
					Metadata: coremetadata.ObjectMeta{UID: "win-1", Name: "editor"},
				})
			},
			want: []string{
				"tmux set-option -w -t projmux:0 automatic-rename off",
				"tmux set-option -w -t projmux:0 -q @projmux_window_uid win-1",
				"tmux set-option -w -t projmux:0 -q @projmux_window_name editor",
				"tmux rename-window -t projmux:0 editor",
			},
		},
		{
			name: "pane mirrors the uid and the pane name",
			run: func(m Mirror) error {
				return m.MirrorPane(context.Background(), "%7", coremetadata.Pane{
					Metadata: coremetadata.ObjectMeta{UID: "pane-1", Name: "logs"},
				})
			},
			want: []string{
				"tmux set-option -p -t %7 -q @projmux_pane_uid pane-1",
				"tmux set-option -p -t %7 -q @projmux_pane_label logs",
			},
			never: []string{"pane_title", "select-pane -T", "rename-window"},
		},
		{
			name: "rename pane touches only the pane name mirror",
			run: func(m Mirror) error {
				return m.RenamePane(context.Background(), "%7", "review")
			},
			want:  []string{"tmux set-option -p -t %7 -q @projmux_pane_label review"},
			never: []string{"pane_title", "select-pane -T", "rename-window", "@projmux_ai_topic"},
		},
		{
			name: "rename window keeps automatic-rename off",
			run: func(m Mirror) error {
				return m.RenameWindow(context.Background(), "projmux:2", "server")
			},
			want: []string{
				"tmux set-option -w -t projmux:2 automatic-rename off",
				"tmux set-option -w -t projmux:2 -q @projmux_window_name server",
				"tmux rename-window -t projmux:2 server",
			},
			never: []string{"@projmux_pane_label"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := &fakeRunner{}
			if err := tt.run(NewMirror(runner)); err != nil {
				t.Fatalf("mirror: %v", err)
			}
			joined := strings.Join(runner.calls, "\n")
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("missing command %q in:\n%s", want, joined)
				}
			}
			for _, never := range tt.never {
				if strings.Contains(joined, never) {
					t.Fatalf("unexpected command fragment %q in:\n%s", never, joined)
				}
			}
			if len(runner.calls) != len(tt.want) {
				t.Fatalf("issued %d commands, want exactly %d:\n%s", len(runner.calls), len(tt.want), joined)
			}
		})
	}
}

func TestMirrorResolvesUIDsAndTmuxTargetsInBothDirections(t *testing.T) {
	t.Parallel()

	// Real tmux prints the escaped separator spelling, not a raw 0x1F byte.
	sep := escapedFieldSep
	runner := &fakeRunner{outputs: map[string]string{
		"list-panes":    "pane-1" + sep + "%7\n" + "pane-2" + sep + "%8\n",
		"list-windows":  "win-1" + sep + "projmux" + sep + "0\n" + "win-2" + sep + "projmux" + sep + "1\n",
		"list-sessions": "proj-1" + sep + "projmux\n",
		"%8":            "pane-2\n",
		"projmux:1":     "win-2\n",
	}}
	m := NewMirror(runner)
	ctx := context.Background()

	if got, err := m.PaneTargetForUID(ctx, "pane-2"); err != nil || got != "%8" {
		t.Fatalf("PaneTargetForUID = %q, %v", got, err)
	}
	if got, err := m.WindowTargetForUID(ctx, "win-2"); err != nil || got != "projmux:1" {
		t.Fatalf("WindowTargetForUID = %q, %v", got, err)
	}
	if got, err := m.SessionForProjectUID(ctx, "proj-1"); err != nil || got != "projmux" {
		t.Fatalf("SessionForProjectUID = %q, %v", got, err)
	}
	if got, err := m.ResolvePaneUID(ctx, "%8"); err != nil || got != "pane-2" {
		t.Fatalf("ResolvePaneUID = %q, %v", got, err)
	}
	if got, err := m.ResolveWindowUID(ctx, "projmux:1"); err != nil || got != "win-2" {
		t.Fatalf("ResolveWindowUID = %q, %v", got, err)
	}
	if _, err := m.PaneTargetForUID(ctx, "pane-missing"); err == nil {
		t.Fatal("an unmirrored uid must not resolve")
	}
}

func TestObserveLegacySessionCollectsTheMigrationSeedsWithoutWriting(t *testing.T) {
	t.Parallel()

	// Real tmux prints the escaped separator spelling, not a raw 0x1F byte.
	sep := escapedFieldSep
	runner := &fakeRunner{outputs: map[string]string{
		"@projmux_project_path": "/src/projmux\n",
		// The last window field and the last pane field are the uid each live
		// object already carries: window 0 is still bound, window 1 is blank.
		"list-windows": "0" + sep + "editor" + sep + "off" + sep + "@4" + sep + "win-editor\n" +
			"1" + sep + "zsh" + sep + "on" + sep + "@5" + sep + "\n",
		"list-panes": strings.Join([]string{
			strings.Join([]string{"0", "nvim", "", "", "nvim", "src/main.go", "/src/projmux", "%1", "pan-nvim"}, sep),
			strings.Join([]string{"0", "", "codex", "refactor naming", "codex", "codex", "/src/projmux", "%2", ""}, sep),
			strings.Join([]string{"1", "", "", "", "zsh", "~/src/projmux", "/src/projmux", "%3", ""}, sep),
		}, "\n") + "\n",
	}}
	legacy, targets, err := NewMirror(runner).ObserveLegacySessionTargets(context.Background(), "projmux")
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	// The transport handles are positionally aligned with the observed model,
	// which is what lets a migration mirror allocated uids back onto exactly the
	// objects it imported.
	if !reflect.DeepEqual(targets.Windows, []string{"@4", "@5"}) {
		t.Fatalf("window targets = %v, want [@4 @5]", targets.Windows)
	}
	if !reflect.DeepEqual(targets.Panes, [][]string{{"%1", "%2"}, {"%3"}}) {
		t.Fatalf("pane targets = %v, want [[%%1 %%2] [%%3]]", targets.Panes)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "set-option") || strings.Contains(call, "rename-window") {
			t.Fatalf("observation must not write: %q", call)
		}
	}
	if legacy.Root != "/src/projmux" || legacy.Session != "projmux" {
		t.Fatalf("legacy session = %+v", legacy)
	}
	if len(legacy.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(legacy.Windows))
	}
	if legacy.Windows[0].AutomaticRename || !legacy.Windows[1].AutomaticRename {
		t.Fatalf("automatic-rename flags = %v/%v, want false/true", legacy.Windows[0].AutomaticRename, legacy.Windows[1].AutomaticRename)
	}
	if len(legacy.Windows[0].Panes) != 2 || len(legacy.Windows[1].Panes) != 1 {
		t.Fatalf("pane counts = %d/%d", len(legacy.Windows[0].Panes), len(legacy.Windows[1].Panes))
	}
	if legacy.Windows[0].Panes[1].Provider != "codex" || legacy.Windows[0].Panes[1].Topic != "refactor naming" {
		t.Fatalf("agent pane = %+v", legacy.Windows[0].Panes[1])
	}
	// The already-carried binding comes back with the observation, so adoption
	// can tell "still ours" from "blank" without a second query.
	if legacy.Windows[0].UID != "win-editor" || legacy.Windows[1].UID != "" {
		t.Fatalf("observed window uids = %q/%q, want win-editor and blank", legacy.Windows[0].UID, legacy.Windows[1].UID)
	}
	if legacy.Windows[0].Panes[0].UID != "pan-nvim" || legacy.Windows[0].Panes[1].UID != "" {
		t.Fatalf("observed pane uids = %q/%q, want pan-nvim and blank", legacy.Windows[0].Panes[0].UID, legacy.Windows[0].Panes[1].UID)
	}

	// The observed state drives the same seeds the pure core computes.
	if got := coremetadata.LegacyWindowNameSeed(legacy.Windows[0]); got != "editor" {
		t.Fatalf("window 0 seed = %q, want editor", got)
	}
	if got := coremetadata.LegacyWindowNameSeed(legacy.Windows[1]); got != "zsh" {
		t.Fatalf("window 1 seed = %q, want zsh", got)
	}
}

func TestMirrorRequiresARunnerAndSurfacesCommandFailures(t *testing.T) {
	t.Parallel()

	var empty Mirror
	if err := empty.RenamePane(context.Background(), "%1", "x"); err == nil {
		t.Fatal("a mirror without a runner must fail")
	}

	boom := errors.New("tmux exploded")
	runner := &fakeRunner{err: boom}
	if err := NewMirror(runner).MirrorWindow(context.Background(), "projmux:0", coremetadata.Window{}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the runner failure", err)
	}
}

// TestLiveUIDInventoriesAreTheMachineHalfOfTheRegistryDiff pins the two read
// queries the observed-status contract is built on.
//
// A tmux object carrying no mirrored uid contributes nothing, so the result is
// exactly the set of resources that still have a transport binding. That is
// what makes "the registry holds this uid, the inventory does not" a sound
// orphan judgment rather than a guess.
func TestLiveUIDInventoriesAreTheMachineHalfOfTheRegistryDiff(t *testing.T) {
	t.Parallel()

	sep := escapedFieldSep
	runner := &fakeRunner{outputs: map[string]string{
		// The middle row of each inventory is an unclaimed tmux object: real,
		// live, and carrying no projmux identity.
		"list-panes": "pane-1" + sep + "%7\n" + sep + "%8\n" + "pane-2" + sep + "%9\n",
		"list-windows": "win-1" + sep + "projmux" + sep + "0\n" +
			sep + "projmux" + sep + "1\n" +
			"win-2" + sep + "projmux" + sep + "2\n",
	}}
	m := NewMirror(runner)
	ctx := context.Background()

	panes, err := m.LivePaneUIDs(ctx)
	if err != nil {
		t.Fatalf("LivePaneUIDs: %v", err)
	}
	if want := map[string]bool{"pane-1": true, "pane-2": true}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("LivePaneUIDs = %v, want %v", panes, want)
	}

	windows, err := m.LiveWindowUIDs(ctx)
	if err != nil {
		t.Fatalf("LiveWindowUIDs: %v", err)
	}
	if want := map[string]bool{"win-1": true, "win-2": true}; !reflect.DeepEqual(windows, want) {
		t.Fatalf("LiveWindowUIDs = %v, want %v", windows, want)
	}

	// One query each, and both are reads. The inventory must never write,
	// re-mirror, or adopt a uid onto a live tmux object: reattaching a lost
	// binding is a separate concern with its own failure modes.
	for _, call := range runner.calls {
		if !strings.HasPrefix(call, "tmux list-panes -a") && !strings.HasPrefix(call, "tmux list-windows -a") {
			t.Fatalf("the inventory issued a non-inventory command: %q", call)
		}
	}
	if len(runner.calls) != 2 {
		t.Fatalf("issued %d commands, want exactly one per inventory:\n%s", len(runner.calls), strings.Join(runner.calls, "\n"))
	}
}

// TestLiveUIDInventoriesPropagateAQueryFailure keeps the fail-closed decision
// at the caller. An empty map and an error must stay distinguishable: the
// reconciler treats a failed query as "record nothing", which it can only do if
// the failure is reported rather than flattened into an empty inventory.
func TestLiveUIDInventoriesPropagateAQueryFailure(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{err: errors.New("no server running on /tmp/tmux-1000/projmux")}
	m := NewMirror(runner)
	ctx := context.Background()

	if _, err := m.LivePaneUIDs(ctx); err == nil {
		t.Fatal("LivePaneUIDs swallowed a query failure")
	}
	if _, err := m.LiveWindowUIDs(ctx); err == nil {
		t.Fatal("LiveWindowUIDs swallowed a query failure")
	}
}
