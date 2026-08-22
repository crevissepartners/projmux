package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type metadataMirrorPlanRunner struct {
	path, logical, projectUID, role string
	windowUID, windowName, paneUID  string
	options                         map[string]string
	calls                           [][]string
}

func (r *metadataMirrorPlanRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, errors.New("unexpected executable")
	}
	r.calls = append(r.calls, slices.Clone(args))
	if len(args) >= 2 && args[0] == "-S" {
		if args[1] != r.path {
			return nil, errors.New("foreign physical socket")
		}
		args = args[2:]
	}
	if len(args) == 4 && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{socket_path}"}) {
		return []byte(r.path + "\n"), nil
	}
	if len(args) >= 3 && args[0] == "show-options" && args[len(args)-1] == tmuxopts.AppGlobal {
		return []byte("1\n"), nil
	}
	if len(args) >= 3 && args[0] == "show-options" && args[len(args)-1] == runtimeMutationSocketNameOption {
		return []byte(r.logical + "\n"), nil
	}
	if len(args) >= 1 && args[0] == "show-options" {
		option := args[len(args)-1]
		switch option {
		case tmuxopts.WindowUID:
			return []byte(r.windowUID + "\n"), nil
		case tmuxopts.PaneUID:
			return []byte(r.paneUID + "\n"), nil
		default:
			return []byte(r.options[option] + "\n"), nil
		}
	}
	if len(args) >= 1 && args[0] == "display-message" {
		format := args[len(args)-1]
		switch {
		case format == "#{window_name}":
			return []byte(r.windowName + "\n"), nil
		case strings.Contains(format, "#{window_id}") && strings.Contains(format, tmuxopts.SessionRole):
			return []byte(strings.Join([]string{"@2", r.windowUID, r.projectUID, r.role}, tmuxRowSep) + "\n"), nil
		case strings.Contains(format, "#{pane_id}"):
			return []byte(strings.Join([]string{"%3", r.paneUID, "win-2"}, tmuxRowSep) + "\n"), nil
		}
	}
	if len(args) >= 1 && args[0] == "set-option" {
		option, value := args[len(args)-2], args[len(args)-1]
		switch option {
		case tmuxopts.WindowUID:
			r.windowUID = value
		case tmuxopts.PaneUID:
			r.paneUID = value
		default:
			r.options[option] = value
		}
		return nil, nil
	}
	if len(args) == 4 && args[0] == "rename-window" {
		r.windowName = args[3]
		return nil, nil
	}
	return nil, nil
}

func newMetadataMirrorPlanFixture() (*metadataMirrorPlanRunner, runtimeMutationMetadataMirror) {
	runner := &metadataMirrorPlanRunner{
		path: "/tmp/metadata-mirror.sock", logical: "metadata-mirror", projectUID: "prj-1",
		options: map[string]string{},
	}
	target := explicitTmuxTarget{flag: "-S", value: runner.path}
	return runner, runtimeMutationMetadataMirror{runner: explicitTmuxRunner{runner: runner, target: target}}
}

func TestTypedMetadataMirrorWindowIsOrderedGuardedAndRepeatEmpty(t *testing.T) {
	runner, mirror := newMetadataMirrorPlanFixture()
	window := coremetadata.Window{Metadata: coremetadata.ObjectMeta{
		UID: "win-2", Name: "main", DisplayName: "Main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-1"},
	}}
	if err := mirror.MirrorWindow(context.Background(), "@2", window); err != nil {
		t.Fatal(err)
	}
	var writes [][]string
	firstWrite := -1
	guardRows := 0
	for index, call := range runner.calls {
		if slices.Contains(call, "display-message") && strings.Contains(strings.Join(call, " "), tmuxopts.SessionRole) {
			guardRows++
		}
		if slices.Contains(call, "set-option") || slices.Contains(call, "rename-window") {
			if firstWrite < 0 {
				firstWrite = index
			}
			writes = append(writes, call)
		}
	}
	if len(writes) != 4 || guardRows < 4 || firstWrite < guardRows {
		t.Fatalf("typed Window sequence guards=%d firstWrite=%d writes=%#v calls=%#v", guardRows, firstWrite, writes, runner.calls)
	}
	if got := []string{writes[0][len(writes[0])-2], writes[1][len(writes[1])-2], writes[2][len(writes[2])-2], writes[3][2]}; !reflect.DeepEqual(got, []string{tmuxopts.AutomaticRenameWindow, tmuxopts.WindowUID, tmuxopts.WindowName, "-S"}) {
		t.Fatalf("Window total order = %v", got)
	}
	before := len(writes)
	if err := mirror.MirrorWindow(context.Background(), "@2", window); err != nil {
		t.Fatal(err)
	}
	after := 0
	for _, call := range runner.calls {
		if slices.Contains(call, "set-option") || slices.Contains(call, "rename-window") {
			after++
		}
	}
	if after != before {
		t.Fatalf("repeat Window mirror wrote %d actions, want zero", after-before)
	}
}

func TestTypedMetadataMirrorRefusesForeignUIDContainmentAndControlOwner(t *testing.T) {
	window := coremetadata.Window{Metadata: coremetadata.ObjectMeta{
		UID: "win-2", Name: "main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-1"},
	}}
	for _, mutate := range []func(*metadataMirrorPlanRunner){
		func(r *metadataMirrorPlanRunner) { r.projectUID = "prj-foreign" },
		func(r *metadataMirrorPlanRunner) { r.windowUID = "win-foreign" },
	} {
		runner, mirror := newMetadataMirrorPlanFixture()
		mutate(runner)
		if err := mirror.MirrorWindow(context.Background(), "@2", window); err == nil {
			t.Fatal("foreign Window authority unexpectedly succeeded")
		}
		for _, call := range runner.calls {
			if slices.Contains(call, "set-option") || slices.Contains(call, "rename-window") {
				t.Fatalf("foreign Window authority wrote tmux: %#v", runner.calls)
			}
		}
	}
	runner, mirror := newMetadataMirrorPlanFixture()
	window.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindControlSession, UID: "ctl-home"}
	if err := mirror.MirrorWindow(context.Background(), "@2", window); err == nil || len(runner.calls) != 0 {
		t.Fatalf("ControlSession Window was not delegated/refused before runtime access: err=%v calls=%#v", err, runner.calls)
	}
}

func TestTypedMetadataMirrorPaneRefusesForeignUIDAndRepeatsEmpty(t *testing.T) {
	runner, mirror := newMetadataMirrorPlanFixture()
	pane := coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pan-3", Name: "shell"}}
	runner.paneUID = "pan-foreign"
	if err := mirror.MirrorPane(context.Background(), "%3", "win-2", pane); err == nil {
		t.Fatal("foreign Pane UID unexpectedly succeeded")
	}
	for _, call := range runner.calls {
		if slices.Contains(call, "set-option") {
			t.Fatalf("foreign Pane UID wrote tmux: %#v", runner.calls)
		}
	}
	runner.paneUID = ""
	runner.calls = nil
	if err := mirror.MirrorPane(context.Background(), "%3", "win-2", pane); err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, call := range runner.calls {
		if slices.Contains(call, "set-option") {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("Pane identity writes=%d, want 2", writes)
	}
	if err := mirror.MirrorPane(context.Background(), "%3", "win-2", pane); err != nil {
		t.Fatal(err)
	}
	after := 0
	for _, call := range runner.calls {
		if slices.Contains(call, "set-option") {
			after++
		}
	}
	if after != writes {
		t.Fatalf("repeat Pane mirror wrote %d actions, want zero", after-writes)
	}
}
