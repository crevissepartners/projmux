package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestRegistryNavigationVirtualWindowLocaleAndOpen(t *testing.T) {
	row := registryview.Row{Kind: registryview.RowKindWindow, UID: "win-headless", ID: "uid:win-headless", Name: "work", Status: registryview.StatusVirtual, Actions: []registryview.Action{registryview.ActionOpen}}
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
		nav := registryNavigationView{locale: locale}
		want := "headless"
		if locale == "ko-KR" {
			want = "헤드리스"
		}
		cells := registryNavigationRowAt(row, locale, time.Time{}, columnWide)
		if !strings.Contains(strings.Join(cells, " "), want) || strings.Contains(strings.Join(cells, " "), "offline") {
			t.Fatalf("row %s: %v", locale, cells)
		}
		entry := nav.actionEntry(row, registryview.ActionOpen, "/tmp/fake-tmux/primary", true)
		if entry.Value != navActionOpen || !strings.Contains(entry.Label, want) {
			t.Fatalf("Open: %+v", entry)
		}
		if entry = nav.actionEntry(row, registryview.ActionOpen, "", true); entry.Value == navActionOpen {
			t.Fatal("unreadable route offered Open")
		}
		if entry = nav.actionEntry(row, registryview.ActionOpen, "", false); entry.Value == navActionOpen {
			t.Fatal("outside tmux offered focus")
		}
	}
	recorder := &navigationArgvRecorder{}
	command := &registryNavigationCommand{focus: recorder}
	var out bytes.Buffer
	if err := command.runFocus(row, "/tmp/fake-tmux/primary", &out, &out); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"window", "uid:win-headless", "--socket", "/tmp/fake-tmux/primary"}}
	if !reflect.DeepEqual(recorder.calls, want) {
		t.Fatalf("focus args=%v", recorder.calls)
	}
	// Propagate a failed materialization through the action rather than returning
	// to a picker built before the failed attempt.
	failure := &virtualNavigationFailure{}
	command.focus = failure
	if err := command.runFocus(row, "/tmp/fake-tmux/primary", &out, &out); err == nil {
		t.Fatal("focus failure swallowed")
	}
}

type virtualNavigationFailure struct{}

func (*virtualNavigationFailure) Run([]string, io.Writer, io.Writer) error {
	return errors.New("injected materialization failure")
}

func TestRegistryNavigationVirtualOpenActionUsesObservedSocket(t *testing.T) {
	command, picker := navigationCommandFixture(t, "/tmp/fake-tmux/primary,1,0", navActionOpen)
	recorder := &navigationArgvRecorder{}
	command.focus = recorder
	row := registryview.Row{Kind: registryview.RowKindWindow, UID: "win-headless", ID: "uid:win-headless", Status: registryview.StatusVirtual, Actions: []registryview.Action{registryview.ActionOpen}}
	nav := registryNavigationView{locale: i18n.FallbackLocale}
	var out bytes.Buffer
	done, err := command.runActions(context.Background(), nav, row, switchUIPopup, &out, &out)
	if err != nil || !done {
		t.Fatalf("Open action done=%v err=%v", done, err)
	}
	want := [][]string{{"window", "uid:win-headless", "--socket", "/tmp/fake-tmux/primary"}}
	if !reflect.DeepEqual(recorder.calls, want) {
		t.Fatalf("focus=%v", recorder.calls)
	}
	if len(picker.seen) != 1 {
		t.Fatalf("action menus=%d", len(picker.seen))
	}
}

// TestRegistryNavigationHeadlessLiveOpenTable pins the picker half of C-2:
// live process containers use Open and reuse the existing materialization route.
func TestRegistryNavigationHeadlessLiveOpenTable(t *testing.T) {
	for _, kind := range []registryview.RowKind{registryview.RowKindProject, registryview.RowKindWindow} {
		for _, inside := range []bool{false, true} {
			for _, socket := range []string{"", "/tmp/fake-tmux/primary"} {
				row := registryview.Row{Kind: kind, UID: "headless", ID: "uid:headless", Status: resourcegraph.StatusLive, Live: kind == registryview.RowKindWindow, Actions: []registryview.Action{registryview.ActionOpen}}
				nav := registryNavigationView{locale: i18n.FallbackLocale}
				entry := nav.actionEntry(row, registryview.ActionOpen, socket, inside)
				allowed := kind == registryview.RowKindProject && !inside || kind == registryview.RowKindWindow && inside && socket != ""
				if (entry.Value == navActionOpen || entry.Value == navActionStart) != allowed {
					t.Fatalf("kind=%s inside=%t socket=%q entry=%+v", kind, inside, socket, entry)
				}
				if allowed && !strings.Contains(entry.Label, "Open") {
					t.Fatalf("materialization mislabeled: %+v", entry)
				}
				if allowed && kind == registryview.RowKindProject && entry.Value != navActionStart {
					t.Fatalf("Project did not reuse attach: %+v", entry)
				}
			}
		}
	}
	row := registryview.Row{Kind: registryview.RowKindWindow, UID: "win-headless", ID: "uid:win-headless", Status: resourcegraph.StatusLive, Live: true}
	recorder := &navigationArgvRecorder{}
	command := &registryNavigationCommand{focus: recorder}
	var out bytes.Buffer
	if err := command.runFocus(row, "/tmp/fake-tmux/primary", &out, &out); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"window", "uid:win-headless", "--socket", "/tmp/fake-tmux/primary"}}
	if !reflect.DeepEqual(recorder.calls, want) {
		t.Fatalf("focus=%v", recorder.calls)
	}
}
