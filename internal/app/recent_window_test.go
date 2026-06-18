package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestRecentWindowPickerItemEmphasizesNamesAndAge(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	snapshot := recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		LastPaneID:    "%54",
		LastPaneTitle: "codex-review",
		LastPaneTopic: "Phase 1 picker",
		LastCommand:   "codex",
		LastFocusedAt: at,
	}
	item := recentWindowPickerItem(recentwindows.Candidate{
		Snapshot: snapshot,
		Label:    recentwindows.BuildLabel(snapshot),
	}, at.Add(12*time.Second))

	if got, want := item.Title, "projmux"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if strings.Contains(item.Title, "@6") || strings.Contains(item.Title, "%54") {
		t.Fatalf("Title = %q, want no raw tmux IDs", item.Title)
	}
	text := item.Title + "\n" + strings.Join(item.MetaLines, "\n")
	for _, want := range []string{"codex-review", "Phase 1 picker", "codex", "12s ago", "Projmux", "repos-projmux"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render text = %q, want %q", text, want)
		}
	}
}

func TestRecentWindowRunEmptyQueueShowsMessage(t *testing.T) {
	t.Parallel()

	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs:   []string{"current" + recentWindowFieldSep + "@1\n"},
	}
	store := &recentWindowStubStore{}
	var pickerCalled bool
	cmd := &recentWindowCommand{
		runner: runner,
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			pickerCalled = true
			return intpicker.Result{}, nil
		}),
		now: func() time.Time { return time.Unix(0, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if pickerCalled {
		t.Fatal("picker was called for empty recent windows")
	}
	if !runner.sawDisplayMessage("no recent windows") {
		t.Fatalf("calls = %#v, want no recent windows display-message", runner.calls)
	}
}

func TestRecentWindowRunSwitchesCrossSessionWindowWithoutPaneRestore(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux,1,0")

	now := time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC)
	target := recentWindowCandidate(recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "other-project",
		WindowID:      "@2",
		WindowName:    "agent",
		Project:       "Other",
		LastPaneID:    "%22",
		LastPaneTitle: "Claude",
		LastCommand:   "claude",
		LastFocusedAt: now.Add(-2 * time.Minute),
	})
	store := &recentWindowStubStore{candidates: []recentwindows.Candidate{target}}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: "current" + recentWindowFieldSep + "@1\n" +
			"other-project" + recentWindowFieldSep + "@2\n",
	}
	var pickerOptions intpicker.Options
	cmd := &recentWindowCommand{
		runner: runner,
		opener: inttmux.NewClient(runner),
		storeFactory: func(string) (recentWindowStore, error) {
			return store, nil
		},
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			pickerOptions = options
			return intpicker.Result{Key: "enter", Value: recentWindowValue(target)}, nil
		}),
		now: func() time.Time { return now },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runner.sawCall("tmux", "switch-client", "-t", "=other-project:@2") {
		t.Fatalf("calls = %#v, want switch-client to selected window id", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "switch-client" && strings.Contains(strings.Join(call.args, " "), ".%22") {
			t.Fatalf("switch-client call = %#v, must not target last pane id", call)
		}
	}
	if got, want := pickerOptions.UI, "recent-windows"; got != want {
		t.Fatalf("picker UI = %q, want %q", got, want)
	}
	if len(pickerOptions.Items) != 1 {
		t.Fatalf("picker items = %#v, want one", pickerOptions.Items)
	}
	if got := strings.Join(append([]string{pickerOptions.Items[0].Title}, pickerOptions.Items[0].MetaLines...), "\n"); !strings.Contains(got, "Other") || !strings.Contains(got, "2m ago") {
		t.Fatalf("picker text = %q, want project and age", got)
	}
	if got, want := store.currents[0], (recentwindows.WindowKey{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"}); got != want {
		t.Fatalf("store current = %+v, want %+v", got, want)
	}
	if got := store.lives[0]; !reflect.DeepEqual(got, []recentwindows.LiveWindow{
		{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"},
		{Socket: "/tmp/tmux", Session: "other-project", WindowID: "@2"},
	}) {
		t.Fatalf("store live windows = %+v", got)
	}
}

func TestRecentWindowSwitchFailureRefreshesAndPrunesQueue(t *testing.T) {
	t.Parallel()

	stateStore := recentwindows.NewStore(t.TempDir() + "/recent.json")
	target := recentwindows.Snapshot{
		Socket:        "/tmp/tmux",
		Session:       "gone-project",
		WindowID:      "@7",
		WindowName:    "gone",
		LastPaneTitle: "old",
		LastFocusedAt: time.Unix(20, 0),
	}
	if _, err := stateStore.Record(target, 0); err != nil {
		t.Fatalf("record target: %v", err)
	}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: []string{
			"gone-project" + recentWindowFieldSep + "@7\n",
			"",
		},
	}
	cmd := &recentWindowCommand{
		runner: runner,
		opener: &recentWindowStubOpener{err: errors.New("target gone")},
		storeFactory: func(string) (recentWindowStore, error) {
			return stateStore, nil
		},
		nativePicker: pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{Key: "enter", Value: "gone-project" + recentWindowFieldSep + "@7"}, nil
		}),
		now: func() time.Time { return time.Unix(30, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runner.sawDisplayMessage("recent window unavailable: gone (gone-project)") {
		t.Fatalf("calls = %#v, want unavailable display-message", runner.calls)
	}
	state, err := stateStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("state entries = %+v, want target pruned after refresh", state.Entries)
	}
}

func TestRecentWindowGoneCandidatePrunedBeforePicker(t *testing.T) {
	t.Parallel()

	stateStore := recentwindows.NewStore(t.TempDir() + "/recent.json")
	for _, snapshot := range []recentwindows.Snapshot{
		{Socket: "/tmp/tmux", Session: "gone", WindowID: "@2", WindowName: "gone", LastFocusedAt: time.Unix(20, 0)},
		{Socket: "/tmp/tmux", Session: "alive", WindowID: "@3", WindowName: "alive", LastFocusedAt: time.Unix(10, 0)},
	} {
		if _, err := stateStore.Record(snapshot, 0); err != nil {
			t.Fatalf("Record(%s) error = %v", snapshot.Session, err)
		}
	}
	runner := &recentWindowFakeRunner{
		currentOutput: "/tmp/tmux" + recentWindowFieldSep + "current" + recentWindowFieldSep + "@1\n",
		listOutputs: "current" + recentWindowFieldSep + "@1\n" +
			"alive" + recentWindowFieldSep + "@3\n",
	}
	opener := &recentWindowStubOpener{}
	var pickerOptions intpicker.Options
	cmd := &recentWindowCommand{
		runner: runner,
		opener: opener,
		storeFactory: func(string) (recentWindowStore, error) {
			return stateStore, nil
		},
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			pickerOptions = options
			return intpicker.Result{Key: "enter", Value: "alive" + recentWindowFieldSep + "@3"}, nil
		}),
		now: func() time.Time { return time.Unix(30, 0) },
	}

	if err := cmd.Run(nil, nil, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(pickerOptions.Items) != 1 || pickerOptions.Items[0].Title != "alive" {
		t.Fatalf("picker items = %#v, want only alive candidate", pickerOptions.Items)
	}
	if opener.session != "alive" || opener.window != "@3" {
		t.Fatalf("opener = %q %q, want alive @3", opener.session, opener.window)
	}
	state, err := stateStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(state.Entries) != 1 || state.Entries[0].Session != "alive" {
		t.Fatalf("state entries = %+v, want gone pruned", state.Entries)
	}
}

func recentWindowCandidate(snapshot recentwindows.Snapshot) recentwindows.Candidate {
	return recentwindows.Candidate{
		Snapshot: snapshot,
		Label:    recentwindows.BuildLabel(snapshot),
	}
}

type recentWindowStubStore struct {
	candidates []recentwindows.Candidate
	currents   []recentwindows.WindowKey
	lives      [][]recentwindows.LiveWindow
}

func (s *recentWindowStubStore) Candidates(current recentwindows.WindowKey, live []recentwindows.LiveWindow, _ int) ([]recentwindows.Candidate, error) {
	s.currents = append(s.currents, current)
	s.lives = append(s.lives, append([]recentwindows.LiveWindow(nil), live...))
	return s.candidates, nil
}

type recentWindowStubOpener struct {
	session string
	window  string
	pane    string
	err     error
}

func (o *recentWindowStubOpener) OpenSessionTarget(_ context.Context, sessionName, windowIndex, paneIndex string) error {
	o.session = sessionName
	o.window = windowIndex
	o.pane = paneIndex
	return o.err
}

type recentWindowFakeRunner struct {
	currentOutput string
	listOutputs   any
	calls         []recentWindowCall
}

type recentWindowCall struct {
	name string
	args []string
}

func (r *recentWindowFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recentWindowCall{name: name, args: append([]string(nil), args...)})
	if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", strings.Join([]string{"#{socket_path}", "#{session_name}", "#{window_id}"}, recentWindowFieldSep)}) {
		return []byte(r.currentOutput), nil
	}
	if name == "tmux" && reflect.DeepEqual(args, []string{"list-windows", "-a", "-F", strings.Join([]string{"#{session_name}", "#{window_id}"}, recentWindowFieldSep)}) {
		switch outputs := r.listOutputs.(type) {
		case []string:
			if len(outputs) == 0 {
				return nil, nil
			}
			out := outputs[0]
			r.listOutputs = outputs[1:]
			return []byte(out), nil
		case string:
			return []byte(outputs), nil
		default:
			return nil, nil
		}
	}
	if name == "tmux" && len(args) == 2 && args[0] == "display-message" {
		return nil, nil
	}
	return nil, nil
}

func (r *recentWindowFakeRunner) sawDisplayMessage(message string) bool {
	return r.sawCall("tmux", "display-message", message)
}

func (r *recentWindowFakeRunner) sawCall(name string, args ...string) bool {
	for _, call := range r.calls {
		if call.name == name && reflect.DeepEqual(call.args, args) {
			return true
		}
	}
	return false
}
