package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func nativePickerFromCompatRunner(r intpickercompat.Runner) intpicker.Runner {
	return pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		// Compatibility fixtures do not run the native event loop. Resume Picker
		// rows are already the settled snapshot; invoke the deferred seam once for
		// detail/chrome only and never wait for obsolete row-mutation signals.
		if options.UI == "ai-resume-picker" && options.DeferredUpdate != nil {
			if update, err := options.DeferredUpdate(); err == nil && update.Items != nil {
				options.Items = update.Items
				if update.AfterApply != nil {
					update.AfterApply()
				}
			}
		}
		result, err := r.Run(compatOptionsFromNativePickerForTest(options))
		if err != nil {
			return intpicker.Result{}, err
		}
		return intpicker.Result{
			Key:    result.Key,
			Value:  result.Value,
			Query:  result.Query,
			Closed: result.Value == "" && result.Key == "",
		}, nil
	})
}

// compatOptionsFromNativePickerForTest adapts the remaining older test runner
// fixtures to the product-native picker contract. It is intentionally test-only:
// production has no native-to-compatibility routing path.
func compatOptionsFromNativePickerForTest(options intpicker.Options) intpickercompat.Options {
	entries := make([]intpickercompat.Entry, 0, len(options.Items))
	for _, item := range options.Items {
		entries = append(entries, intpickercompat.Entry{
			Label:     item.EffectiveLabel(),
			Value:     item.Value,
			SearchKey: item.EffectiveSearchText(),
		})
	}

	compatOptions := intpickercompat.Options{
		UI:              options.UI,
		Entries:         entries,
		Read0:           options.MultiLine,
		Title:           options.Title,
		TitleChips:      options.TitleChips,
		Prompt:          options.Prompt,
		Header:          options.Header,
		Footer:          options.Footer,
		MoreNotLoaded:   options.MoreNotLoaded,
		Locale:          options.Locale,
		InitialQuery:    options.InitialQuery,
		DisableSearch:   options.DisableSearch,
		AcceptQuery:     options.AcceptQuery,
		ColorGrid:       options.ColorGrid,
		Recorder:        options.Recorder,
		PreviewCommand:  options.Preview.Command,
		PreviewWindow:   options.Preview.Window,
		SelectionDetail: options.SelectionDetail,
		Theme:           options.Theme,
	}
	for _, action := range options.Actions {
		key := strings.TrimSpace(action.Key)
		if key == "" {
			continue
		}
		switch action.Intent {
		case intpicker.ActionClose:
			compatOptions.Bindings = append(compatOptions.Bindings, key+":abort")
		case intpicker.ActionAccept, intpicker.ActionCustom:
			if action.Intent == intpicker.ActionCustom && strings.TrimSpace(action.Command) != "" {
				binding := key + ":execute-silent(" + strings.TrimSpace(action.Command) + ")"
				if action.Refresh {
					binding += "+refresh-preview"
				}
				compatOptions.Bindings = append(compatOptions.Bindings, binding)
				continue
			}
			compatOptions.ExpectKeys = append(compatOptions.ExpectKeys, key)
		}
	}
	if options.InitialIndexSet || options.InitialIndex > 0 {
		compatOptions.Bindings = append(compatOptions.Bindings, "start:pos("+strconv.Itoa(options.InitialIndex+1)+")")
	}
	return compatOptions
}

func TestRunNativePickerOptionUsesNativeContract(t *testing.T) {
	t.Parallel()

	native := pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		if options.UI != "settings" {
			t.Fatalf("native UI = %q, want settings", options.UI)
		}
		if len(options.Items) != 1 || options.Items[0].Value != "ai" {
			t.Fatalf("native items = %#v, want compat entries converted", options.Items)
		}
		return intpicker.Result{Key: "enter", Value: "ai"}, nil
	})

	result, err := runNativePickerOption(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		native,
		intpickercompat.Options{
			UI:      "settings",
			Entries: []intpickercompat.Entry{{Label: "AI Settings", Value: "ai"}},
		},
	)
	if err != nil {
		t.Fatalf("runNativePickerOption() error = %v", err)
	}
	if result.Key != "enter" || result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
}

func TestRunNativePickerOptionPopulatesFallbackTheme(t *testing.T) {
	t.Parallel()

	var gotTheme *theme.EffectiveTheme
	_, err := runNativePickerOption(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotTheme = options.Theme
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		intpickercompat.Options{UI: "settings"},
	)
	if err != nil {
		t.Fatalf("runNativePickerOption() error = %v", err)
	}
	if gotTheme == nil {
		t.Fatal("native picker Theme = nil, want fallback effective theme")
	}
	if gotTheme.Background.Source != theme.SourceFallback || gotTheme.Foreground.Source != theme.SourceFallback {
		t.Fatalf("native picker Theme = %#v, want fallback effective theme", gotTheme)
	}
}

func TestRunNativePickerOptionPreservesSuppliedTheme(t *testing.T) {
	t.Parallel()

	supplied := theme.ResolveTheme(theme.ThemeConfig{
		Background: "#010203",
		Foreground: "#aabbcc",
	})
	var gotTheme *theme.EffectiveTheme
	_, err := runNativePickerOption(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotTheme = options.Theme
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		intpickercompat.Options{UI: "settings", Theme: &supplied},
	)
	if err != nil {
		t.Fatalf("runNativePickerOption() error = %v", err)
	}
	if gotTheme == nil {
		t.Fatal("native picker Theme = nil, want supplied effective theme")
	}
	if gotTheme.Background.Value.Hex != "#010203" || gotTheme.Foreground.Value.Hex != "#aabbcc" {
		t.Fatalf("native picker Theme = %#v, want supplied effective theme", gotTheme)
	}
}

func TestRunNativePickerOptionDoesNotObserveRetiredBackendArtifacts(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	configDir := filepath.Join(configHome, "projmux")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(configDir, "picker-backend")
	staleContent := []byte("retired-value\n")
	if err := os.WriteFile(stalePath, staleContent, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(stalePath)
	if err != nil {
		t.Fatal(err)
	}

	effective := theme.ResolveTheme(theme.ThemeConfig{})
	result, err := runNativePickerOption(
		func() (string, error) {
			t.Fatal("retired picker backend file path was resolved")
			return "", nil
		},
		func(name string) string {
			t.Fatalf("environment %q was looked up with explicit locale and theme", name)
			return ""
		},
		pickerRunnerFunc(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{Key: "enter", Value: "ai"}, nil
		}),
		intpickercompat.Options{UI: "settings", Locale: i18n.FallbackLocale, Theme: &effective},
	)
	if err != nil {
		t.Fatalf("runNativePickerOption() error = %v", err)
	}
	if result.Value != "ai" {
		t.Fatalf("result = %#v, want native selection", result)
	}
	after, err := os.Stat(stalePath)
	if err != nil {
		t.Fatalf("retired picker backend file was deleted: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("retired picker backend file was replaced")
	}
	got, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(staleContent) {
		t.Fatalf("retired picker backend file = %q, want unchanged %q", got, staleContent)
	}
}

func TestRunNativePickerOptionErrorsWhenNativeMissing(t *testing.T) {
	t.Parallel()

	_, err := runNativePickerOption(
		func() (string, error) { return "", nil },
		func(string) string { return "" },
		nil,
		intpickercompat.Options{UI: "settings"},
	)

	if err == nil || !strings.Contains(err.Error(), "native picker is not configured") {
		t.Fatalf("runNativePickerOption() error = %v, want native picker error", err)
	}
}

func TestProductionPickerConstructorsDoNotCreateCompatRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if cmd := newAICommand(); cmd.runner != nil {
		t.Fatal("newAICommand() created compat runner")
	}
	if cmd := newSettingsCommand(testAICommand(t.TempDir()), testSettingsSwitchCommand(t, newStubPinStore()), nil, nil); cmd.runner != nil {
		t.Fatal("newSettingsCommand() created compat runner")
	}
	if cmd := newSwitchCommand(); cmd.runner != nil {
		t.Fatal("newSwitchCommand() created compat runner")
	}
	if cmd := newSessionsCommand(); cmd.runner != nil {
		t.Fatal("newSessionsCommand() created compat runner")
	}
	if cmd := newNotifyCommand(newDefaultLivePaneLister()); cmd.picker != nil {
		t.Fatal("newNotifyCommand() created compat runner")
	}
}

func TestPickerOptionsFromCompatPickerMapsCandidatesWhenEntriesAreEmpty(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		UI:         "compat",
		Candidates: []string{"/tmp/project-a", " ", "/tmp/project-b"},
	})

	if len(options.Items) != 2 {
		t.Fatalf("Items = %#v, want two candidate-backed items", options.Items)
	}
	if options.Items[0].Value != "/tmp/project-a" || options.Items[0].SearchText != "/tmp/project-a" {
		t.Fatalf("first item = %#v, want candidate as label/value/search text", options.Items[0])
	}
	if options.Items[1].Value != "/tmp/project-b" {
		t.Fatalf("second item = %#v, want second candidate", options.Items[1])
	}
}

func TestPickerOptionsFromCompatPickerPreservesTheme(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{Background: "#010203"})
	options := pickerOptionsFromCompatPicker(intpickercompat.Options{Theme: &effective})
	if options.Theme != &effective {
		t.Fatalf("Theme = %p, want %p", options.Theme, &effective)
	}
}

func TestSettingsUsesNativePicker(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var out strings.Builder
	var compatCalled bool
	cmd := &settingsCommand{
		ai:        testAICommand(home),
		switcher:  testSettingsSwitchCommand(t, newStubPinStore()),
		lookupEnv: func(string) string { return "" },
		// Root tab chrome moved out of the entry list in Phase 2.5, so AI
		// Settings is now the second row in the Global tab. AI Settings
		// opens Default split mode, whose detail keeps codex on row 4.
		nativePicker: intpicker.NativeRunner{In: strings.NewReader("2\n2\n4\n"), Out: &out},
		runner: switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
	}

	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called for Settings")
	}
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Settings > ") {
		t.Fatalf("native output = %q, want settings prompt", out.String())
	}
}

func TestPickerActionsFromCompatBindingPreservesExecuteSilentCommand(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		Bindings: []string{"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview"},
	})
	if len(options.Actions) != 1 {
		t.Fatalf("actions = %#v, want one action", options.Actions)
	}
	action := options.Actions[0]
	if action.Key != "right" || action.Command != "exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next'" {
		t.Fatalf("action = %#v, want execute-silent command preserved", action)
	}
}

func TestPickerOptionsFromCompatPickerMapsStartPosToInitialIndex(t *testing.T) {
	t.Parallel()

	options := pickerOptionsFromCompatPicker(intpickercompat.Options{
		Bindings: []string{
			"focus:execute-silent(exec '/tmp/projmux' 'switch' 'sidebar-focus' {2})",
			"start:pos(3)",
		},
	})
	if options.InitialIndex != 2 {
		t.Fatalf("InitialIndex = %d, want 2", options.InitialIndex)
	}
	for _, action := range options.Actions {
		if action.Key == "start" {
			t.Fatalf("actions = %#v, want start binding consumed as initial index", options.Actions)
		}
	}
}
