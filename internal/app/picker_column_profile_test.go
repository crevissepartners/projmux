package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestColumnPickerProfileSettingsAndNativeStateParity(t *testing.T) {
	for _, action := range []string{registryColumnProfileAction, runtimeColumnProfileAction} {
		for _, custom := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/default", true: "/custom"}[custom], func(t *testing.T) {
				home := t.TempDir()
				settings := keybindingCorrectnessCommand(t, home, nil)
				spec, ok := keyBindingActionByID(defaultKeyBindingCatalog(), action)
				if !ok || spec.Kind != keyBindingActionPickerInternal || spec.Tier != keyBindingTierNativePickerInternal || spec.PlainChord != "M-w" {
					t.Fatalf("catalog: %+v", spec)
				}
				wantID := map[string]string{registryColumnProfileAction: "resource-inspector.columns.toggle", runtimeColumnProfileAction: "runtime-diagnostics.columns.toggle"}[action]
				if spec.CanonicalID != wantID {
					t.Fatalf("canonical ID: %s", spec.CanonicalID)
				}
				entries, err := settings.keybindingSurfaceEntries(spec.Surface)
				if err != nil || !hasEntryLabelContaining(entries, "Toggle compact / wide columns") {
					t.Fatalf("Settings surface missing action: %v %v", entries, err)
				}
				candidate, err := normalizeKeybindingAuthoringCandidate("M-v")
				if err != nil {
					t.Fatal(err)
				}
				if err = settings.validateKeybindingCandidateForAction(action, candidate, ""); err != nil {
					t.Fatal(err)
				}
				sequence, _ := normalizeKeybindingAuthoringCandidate("C-k C-v")
				if err = settings.validateKeybindingCandidateForAction(action, sequence, ""); err == nil {
					t.Fatal("picker-local sequence unexpectedly accepted")
				}
				key := "alt-w"
				var keymapPath string
				var before []byte
				if custom {
					key = "v"
					keymapPath, err = saveKeymapKeys(settings.keymapStore(), action, []string{"v"})
					if err != nil {
						t.Fatal(err)
					}
					before, err = os.ReadFile(keymapPath)
					if err != nil {
						t.Fatal(err)
					}
				}
				picker := &scriptedRuntimePicker{answers: []intpicker.Result{
					{Key: key, Value: "selected", Query: "한글"},
					{Key: key, Value: "selected", Query: "한글"},
					{Key: "enter", Value: "selected", Query: "한글"},
				}}
				rows := func(profile columnProfile) []intpickercompat.Entry {
					return []intpickercompat.Entry{{Label: string(profile) + " first", Value: "first", SearchKey: "한글 first"}, {Label: string(profile) + " selected", Value: "selected", SearchKey: "한글 selected full-uid raw-target"}}
				}
				var state columnPickerState
				result, err := state.run(settings.homeDir, settings.lookupEnv, picker, action, intpickercompat.Options{ExpectKeys: []string{"enter"}, Footer: "Enter: actions"}, rows)
				if err != nil {
					t.Fatal(err)
				}
				if result.Value != "selected" || result.Query != "한글" {
					t.Fatalf("result: %+v", result)
				}
				if len(picker.rendered) != 3 {
					t.Fatalf("renders=%d", len(picker.rendered))
				}
				for i, options := range picker.rendered {
					want := []string{"compact", "wide", "compact"}[i]
					if !strings.HasPrefix(options.Items[0].Label, want) {
						t.Fatalf("frame %d profile: %s", i, options.Items[0].Label)
					}
					keys := []string{}
					for _, a := range options.Actions {
						keys = append(keys, a.Key)
					}
					if !slices.Contains(keys, key) || (custom && slices.Contains(keys, "alt-w")) {
						t.Fatalf("effective keys: %v", keys)
					}
					if i > 0 && (options.InitialQuery != "한글" || !options.InitialIndexSet || options.InitialIndex != 1) {
						t.Fatalf("frame %d query/selection: %+v", i, options)
					}
					if got := options.Items[1]; got.Value != "selected" || got.SearchText != "한글 selected full-uid raw-target" {
						t.Fatal("identity/search drift")
					}
				}
				state.profile = columnWide // Closing while wide cannot make a new open inherit it.
				var reopened columnPickerState
				reopenPicker := &scriptedRuntimePicker{}
				_, err = reopened.run(settings.homeDir, settings.lookupEnv, reopenPicker, action, intpickercompat.Options{}, rows)
				if err != nil || !strings.HasPrefix(reopenPicker.rendered[0].Items[0].Label, "compact") || reopenPicker.rendered[0].InitialQuery != "" {
					t.Fatalf("reopen did not reset: %v", err)
				}
				if custom {
					after, err := os.ReadFile(keymapPath)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("toggle wrote keymap")
					}
				}
				if localizeUIText(i18n.Locale("ko-KR"), "wide columns") == "wide columns" {
					t.Fatal("toggle guide lacks Korean translation")
				}
			})
		}
	}
}

func TestColumnPickerToggleDoesNotObserveOrWriteAgain(t *testing.T) {
	for _, surface := range []string{"registry", "runtime"} {
		t.Run(surface, func(t *testing.T) {
			run := func(toggle bool) (int, []intpicker.Options) {
				answers := []intpicker.Result{}
				if toggle {
					answers = []intpicker.Result{{Key: "alt-w", Value: "", Query: "unmatched"}, {Key: "alt-w", Value: "", Query: "unmatched"}}
				}
				picker := &scriptedRuntimePicker{answers: answers}
				if surface == "registry" {
					reader, primary, _, _ := navigationFixtureReader(t, "1", "/tmp/fake-tmux/primary,0,0")
					before := primary.state()
					command := &registryNavigationCommand{reader: reader, native: picker, lookupEnv: func(string) string { return "" }}
					if err := command.runProject(context.Background(), switchUIPopup, runtimeFixtureProject, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
						t.Fatal(err)
					}
					if primary.state() != before {
						t.Fatal("Registry toggle mutated runtime")
					}
					return len(primary.calls), picker.rendered
				}
				command, _, primary, _, _ := runtimePickerFixture(t, "1", nil)
				command.native = picker
				before := primary.state()
				if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
					t.Fatal(err)
				}
				if primary.state() != before {
					t.Fatal("Runtime toggle mutated runtime")
				}
				return len(primary.calls), picker.rendered
			}
			base, _ := run(false)
			calls, frames := run(true)
			if calls != base || len(frames) != 3 {
				t.Fatalf("toggle calls=%d base=%d frames=%d", calls, base, len(frames))
			}
			values := func(options intpicker.Options) []string {
				var out []string
				for _, item := range options.Items {
					out = append(out, item.Value+"\x00"+item.SearchText)
				}
				return out
			}
			if !reflect.DeepEqual(values(frames[0]), values(frames[1])) || !reflect.DeepEqual(values(frames[0]), values(frames[2])) {
				t.Fatal("toggle changed row order/cardinality/identity/search")
			}
		})
	}
}
