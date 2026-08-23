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
			name: "window mirrors stable identity and a separate runtime display name",
			run: func(m Mirror) error {
				return m.MirrorWindow(context.Background(), "projmux:0", coremetadata.Window{
					Metadata: coremetadata.ObjectMeta{UID: "win-1", Name: "window", DisplayName: "editor"},
				})
			},
			want: []string{
				"tmux set-option -w -t projmux:0 automatic-rename off",
				"tmux set-option -w -t projmux:0 -q @projmux_window_uid win-1",
				"tmux set-option -w -t projmux:0 -q @projmux_window_name window",
				"tmux rename-window -t projmux:0 editor",
			},
		},
		{
			name: "window with no projected display name falls back to stable name",
			run: func(m Mirror) error {
				return m.MirrorWindow(context.Background(), "projmux:1", coremetadata.Window{
					Metadata: coremetadata.ObjectMeta{UID: "win-2", Name: "review"},
				})
			},
			want: []string{
				"tmux set-option -w -t projmux:1 automatic-rename off",
				"tmux set-option -w -t projmux:1 -q @projmux_window_uid win-2",
				"tmux set-option -w -t projmux:1 -q @projmux_window_name review",
				"tmux rename-window -t projmux:1 review",
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
			name: "rename project touches only the stable project name mirror",
			run: func(m Mirror) error {
				return m.RenameProject(context.Background(), "projmux", "workspace")
			},
			want:  []string{"tmux set-option -t projmux -q @projmux_project_name workspace"},
			never: []string{"rename-session", "@projmux_project_uid", "@projmux_project_path"},
		},
		{
			name: "rebind project touches only the project path anchor",
			run: func(m Mirror) error {
				return m.RebindProject(context.Background(), "projmux", "/src/moved")
			},
			want:  []string{"tmux set-option -t projmux -q @projmux_project_path /src/moved"},
			never: []string{"rename-session", "@projmux_project_uid", "@projmux_project_name"},
		},
		{
			name: "rename window touches only the stable window name mirror",
			run: func(m Mirror) error {
				return m.RenameWindow(context.Background(), "@4", "review")
			},
			want:  []string{"tmux set-option -w -t @4 -q @projmux_window_name review"},
			never: []string{"rename-window", "@projmux_window_uid", "automatic-rename"},
		},
		{
			name: "rename pane touches only the pane name mirror",
			run: func(m Mirror) error {
				return m.RenamePane(context.Background(), "%7", "review")
			},
			want:  []string{"tmux set-option -p -t %7 -q @projmux_pane_label review"},
			never: []string{"pane_title", "select-pane -T", "rename-window", "@projmux_ai_topic"},
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
		"list-windows":  "win-1" + sep + "@1" + sep + "projmux" + sep + "0\n" + "win-2" + sep + "@2" + sep + "projmux" + sep + "1\n",
		"list-sessions": "proj-1" + sep + "$1" + sep + "projmux\n",
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
	if got, found, err := m.FindWindowTargetForUID(ctx, "win-2"); err != nil || !found || got != "@2" {
		t.Fatalf("FindWindowTargetForUID = %q, %v, %v", got, found, err)
	}
	if got, found, err := m.FindSessionForProjectUID(ctx, "proj-1"); err != nil || !found || got != "$1" {
		t.Fatalf("FindSessionForProjectUID = %q, %v, %v", got, found, err)
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

func TestExactUIDTargetLookupDistinguishesOfflineAndDuplicateClaims(t *testing.T) {
	t.Parallel()

	sep := escapedFieldSep
	runner := &fakeRunner{outputs: map[string]string{
		"list-panes":    "pane-1" + sep + "%7\n" + "pane-1" + sep + "%8\n",
		"list-windows":  "win-1" + sep + "@1" + sep + "projmux" + sep + "0\n" + "win-1" + sep + "@2" + sep + "other" + sep + "1\n",
		"list-sessions": "proj-1" + sep + "$1" + sep + "projmux\n" + "proj-1" + sep + "$2" + sep + "other\n",
	}}
	m := NewMirror(runner)
	ctx := context.Background()
	for name, lookup := range map[string]func() (string, bool, error){
		"pane":    func() (string, bool, error) { return m.FindPaneTargetForUID(ctx, "pane-1") },
		"window":  func() (string, bool, error) { return m.FindWindowTargetForUID(ctx, "win-1") },
		"project": func() (string, bool, error) { return m.FindSessionForProjectUID(ctx, "proj-1") },
	} {
		t.Run(name+" duplicate", func(t *testing.T) {
			_, _, err := lookup()
			if !errors.Is(err, ErrAmbiguousMirror) {
				t.Fatalf("error = %v, want ErrAmbiguousMirror", err)
			}
		})
	}
	if target, found, err := m.FindPaneTargetForUID(ctx, "offline"); err != nil || found || target != "" {
		t.Fatalf("offline lookup = %q,%v,%v", target, found, err)
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
		"list-windows": "0" + sep + "editor" + sep + "off" + sep + "$1" + sep + "@4" + sep + "win-editor\n" +
			"1" + sep + "zsh" + sep + "on" + sep + "$1" + sep + "@5" + sep + "\n",
		// The last two pane fields are the provider conversation ids the AI
		// routes wrote onto the live pane; only the agent pane carries them.
		"list-panes": strings.Join([]string{
			strings.Join([]string{"0", "nvim", "", "", "nvim", "src/main.go", "/src/projmux", "%1", "pan-nvim", "", ""}, sep),
			strings.Join([]string{"0", "", "codex", "refactor naming", "codex", "codex", "/src/projmux", "%2", "", "sess-9", "thread-9"}, sep),
			strings.Join([]string{"1", "", "", "", "zsh", "~/src/projmux", "/src/projmux", "%3", "", "", ""}, sep),
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
	if legacy.Windows[0].RuntimeSessionID != "$1" || legacy.Windows[0].RuntimeID != "@4" ||
		legacy.Windows[1].RuntimeSessionID != "$1" || legacy.Windows[1].RuntimeID != "@5" {
		t.Fatalf("observed Window runtime bindings = %+v", legacy.Windows)
	}
	if legacy.Windows[0].Panes[0].UID != "pan-nvim" || legacy.Windows[0].Panes[1].UID != "" {
		t.Fatalf("observed pane uids = %q/%q, want pan-nvim and blank", legacy.Windows[0].Panes[0].UID, legacy.Windows[0].Panes[1].UID)
	}
	// The provider conversation ids ride along on the same query, so agent
	// runtime linkage never pays for a second per-pane read.
	if got := legacy.Windows[0].Panes[1]; got.SessionID != "sess-9" || got.ThreadID != "thread-9" {
		t.Fatalf("observed conversation ids = %q/%q, want sess-9 and thread-9", got.SessionID, got.ThreadID)
	}
	if got := legacy.Windows[0].Panes[0]; got.SessionID != "" || got.ThreadID != "" {
		t.Fatalf("a non-agent pane must carry no conversation ids, got %q/%q", got.SessionID, got.ThreadID)
	}

	// Runtime attributes remain available for display projection, but neither
	// automatic-rename mode can turn them into a stable Window name seed.
	if got := coremetadata.LegacyWindowNameSeed(legacy.Windows[0]); got != coremetadata.FallbackWindowNameBase {
		t.Fatalf("window 0 seed = %q, want %q", got, coremetadata.FallbackWindowNameBase)
	}
	if got := coremetadata.LegacyWindowNameSeed(legacy.Windows[1]); got != coremetadata.FallbackWindowNameBase {
		t.Fatalf("window 1 seed = %q, want %q", got, coremetadata.FallbackWindowNameBase)
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
		"list-windows": "win-1" + sep + "@1" + sep + "projmux" + sep + "0\n" +
			sep + "@2" + sep + "projmux" + sep + "1\n" +
			"win-2" + sep + "@3" + sep + "projmux" + sep + "2\n",
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

func TestLiveWindowSessionCountsIncludesUnmirroredWindows(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{outputs: map[string]string{"#{session_id}": "$1\n$1\n$2\n"}}
	counts, err := NewMirror(runner).LiveWindowSessionCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"$1": 2, "$2": 1}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("LiveWindowSessionCounts = %v, want %v", counts, want)
	}
}
