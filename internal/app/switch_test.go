package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/core/pins"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

func TestAppRunSwitchDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	var gotRunnerOptions intpickercompat.Options
	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{"workspace": true},
	}

	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { gotRunnerOptions = o },
			reply: intpickercompat.Result{Value: "/home/tester/workspace"}},
	})
	app := &App{
		switcher: &switchCommand{
			discover: func(inputs candidates.Inputs) ([]string, error) {
				gotInputs = inputs
				return []string{"/home/tester", "/home/tester/workspace"}, nil
			},
			pinStore: func() (switchPinStore, error) {
				return newCandidateStubPinStore("/pins/app"), nil
			},
			runner:       runner,
			nativePicker: native,
			sessions:     executor,
			executable:   func() (string, error) { return "/tmp/projmux", nil },
			identity: switchIdentityResolverFunc(func(path string) (string, error) {
				switch path {
				case "/home/tester/workspace":
					return "workspace", nil
				case "/home/tester":
					return "tester", nil
				default:
					return "", errors.New("unexpected path")
				}
			}),
			validate:   func(string) error { return nil },
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/rp/repo-a/nested", nil },
			lookupEnv: func(name string) string {
				switch name {
				case projdirEnvVar:
					return "/rp"
				case managedRootsEnvVar:
					return "/managed/a:/managed/b"
				default:
					return ""
				}
			},
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := app.Run([]string{"switch"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if got, want := gotInputs.HomeDir, "/home/tester"; got != want {
		t.Fatalf("inputs.HomeDir = %q, want %q", got, want)
	}
	if got, want := gotInputs.RepoRoot, "/rp"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{"/managed/a", "/managed/b"}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
	if got, want := gotInputs.Pins, []string{"/pins/app"}; !equalStrings(got, want) {
		t.Fatalf("inputs.Pins = %q, want %q", got, want)
	}
	if got, want := gotInputs.CurrentPath, "/rp/repo-a/nested"; got != want {
		t.Fatalf("inputs.CurrentPath = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.UI, switchUIPopup; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
		t.Fatalf("runner expect keys = %q, want %q", got, want)
	}
	if !gotRunnerOptions.Read0 {
		t.Fatal("runner Read0 = false, want true")
	}
	if got, want := gotRunnerOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Footer, "Preview follows the focused target.\nDestructive actions keep the current confirmation policy."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' '--ui=popup' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"left:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intpickercompat.Entry{
		{Label: "\x1b[1m\x1b[32mworkspace\x1b[0m\n  \x1b[38;5;242m~/workspace\x1b[0m", Value: "/home/tester/workspace"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := gotRunnerOptions.Entries[0].SearchKey, "workspace"; got != want {
		t.Fatalf("runner entry search key = %q, want %q", got, want)
	}
	if got, want := executor.ensureSessionName, ""; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.ensureCWD, ""; got != want {
		t.Fatalf("ensure cwd = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "workspace"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
}

func stripAnsi(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

func TestSwitchExecuteSidebarHookProjectLaunchesContinuationBeforeSelfClose(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".projmux", "config.toml"), []byte("[startup]\nrun = \"agent\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &switchCommand{
		tmuxRunner: tmuxRunner,
		sessions:   &capturingSwitchSessionExecutor{exists: map[string]bool{"target": false}},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "target"},
		lookupEnv: func(name string) string {
			if name == hookTrustPopupTargetClientEnv {
				return "/dev/pts/9"
			}
			return ""
		},
	}

	reopen, err := cmd.execute(context.Background(), switchPlan{
		UI:          switchUISidebar,
		Selection:   target,
		SessionName: "target",
	}, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if reopen {
		t.Fatal("execute() reopen = true, want false")
	}
	if len(tmuxRunner.calls) != 1 {
		t.Fatalf("tmux calls = %#v, want one run-shell continuation", tmuxRunner.calls)
	}
	call := tmuxRunner.calls[0]
	if call.name != "tmux" || len(call.args) != 3 || !reflect.DeepEqual(call.args[:2], []string{"run-shell", "-b"}) {
		t.Fatalf("tmux call = %#v, want detached continuation", call)
	}
	command := call.args[2]
	for _, want := range []string{
		"PROJMUX_HOOK_TRUST_TARGET_CLIENT='/dev/pts/9'",
		"PROJMUX_SWITCH_TARGET_CLIENT='/dev/pts/9'",
		"'/tmp/projmux' 'switch' 'sidebar-open'",
		"'--path' " + tmuxShellQuote(target),
		"'--session' 'target'",
		"'--mode' 'continue'",
		"'--client' '/dev/pts/9'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("continuation command = %q, want substring %q", command, want)
		}
	}
	if strings.Contains(command, "display-popup -C") {
		t.Fatalf("continuation command = %q, should not self-close before launching", command)
	}
}

func TestSwitchExecuteSidebarTrustDenyRefreshesWithoutSessionCreate(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	sessions := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: false}
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &switchCommand{
		sessions:   sessions,
		tmuxRunner: tmuxRunner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}
	wireFakeProjectSessionPlan(cmd)

	err := cmd.runSidebarOpen([]string{
		"--path", target,
		"--session", "target",
		"--mode", projectStartupKindTopology,
		"--query", "tar",
		"--client", "/dev/pts/9",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if sessions.ensureSessionName != "" || sessions.openSessionName != "" {
		t.Fatalf("deny should not create/replay/open: %#v", sessions)
	}
	if got, want := sessions.calls, []string{"authorize:" + target}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if len(tmuxRunner.calls) != 2 {
		t.Fatalf("tmux calls = %#v, want close then reopen", tmuxRunner.calls)
	}
	if got, want := tmuxRunner.calls[0], (recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-c", "/dev/pts/9", "-C"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	reopen := tmuxRunner.calls[1]
	if reopen.name != "tmux" || len(reopen.args) != 3 || !reflect.DeepEqual(reopen.args[:2], []string{"run-shell", "-b"}) {
		t.Fatalf("reopen call = %#v, want detached popup-toggle", reopen)
	}
	command := reopen.args[2]
	for _, want := range []string{
		switchInitialQueryEnv + "='tar'",
		switchInitialSelectionEnv + "=" + tmuxShellQuote(target),
		switchStatusMessageEnv + "='Trust denied'",
		"'/tmp/projmux' 'internal' 'tmux' 'popup-toggle' '--client' '/dev/pts/9' 'sessionizer-sidebar'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("reopen command = %q, want substring %q", command, want)
		}
	}
}

func TestSwitchSidebarOpenApproveContinuesSelectedEmptyOpen(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	sessions := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &switchCommand{
		sessions:   sessions,
		tmuxRunner: tmuxRunner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}
	wireFakeProjectSessionPlan(cmd)

	err := cmd.runSidebarOpen([]string{
		"--path", target,
		"--session", "target",
		"--mode", projectStartupKindTopology,
		"--client", "/dev/pts/9",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if got, want := sessions.calls, []string{"authorize:" + target, "ensure:target"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if len(tmuxRunner.calls) != 2 {
		t.Fatalf("tmux calls = %#v, want sidebar close then exact handoff", tmuxRunner.calls)
	}
	if got, want := tmuxRunner.calls[0], (recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-c", "/dev/pts/9", "-C"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	wantHandoff := recordedTmuxCall{name: "tmux", args: []string{"-L", "projmux", "switch-client", "-c", "/dev/pts/9", "-t", "=target"}}
	if !reflect.DeepEqual(tmuxRunner.calls[1], wantHandoff) {
		t.Fatalf("final handoff = %#v, want %#v", tmuxRunner.calls[1], wantHandoff)
	}
}

func TestSwitchSidebarOpenTrustPopupUsesClientScope(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	tmuxRunner := &recordingTmuxRunner{}
	popupRunner := &hookTrustPopupRecordingRunner{}
	var cmd *switchCommand
	sessions := &sidebarOpenTrustPopupExecutor{
		lookupEnv: func(name string) string { return cmd.lookupEnv(name) },
		executable: func() (string, error) {
			return "/tmp/projmux", nil
		},
		popupRunner: popupRunner,
	}
	cmd = &switchCommand{
		sessions:   sessions,
		tmuxRunner: tmuxRunner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux/default,1,0"
			}
			return ""
		},
	}
	wireFakeProjectSessionPlan(cmd)

	err := cmd.runSidebarOpen([]string{
		"--path", target,
		"--session", "target",
		"--mode", projectStartupKindTopology,
		"--client", "/dev/pts/9",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if got, want := sessions.calls, []string{"authorize:" + target, "ensure:target"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if len(popupRunner.calls) != 1 {
		t.Fatalf("trust popup calls = %#v, want one display-popup", popupRunner.calls)
	}
	call := popupRunner.calls[0]
	if call.name != "tmux" || len(call.args) == 0 || call.args[0] != "display-popup" {
		t.Fatalf("trust popup call = %#v, want display-popup", call)
	}
	if !containsTmuxArgPair(call.args, "-c", "/dev/pts/9") {
		t.Fatalf("trust popup args = %#v, want client scope", call.args)
	}
	if containsTmuxArg(call.args, "-t") {
		t.Fatalf("trust popup args = %#v, want no unrelated pane target", call.args)
	}
}

func TestSwitchExecutePopupProjectCreateUsesProjectOpenTrust(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".projmux", "config.toml"), []byte("[hooks.post-create]\nrun = \"echo hook\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := &capturingSwitchSessionExecutor{exists: map[string]bool{"target": false}}
	cmd := &switchCommand{
		sessions:  sessions,
		identity:  stubSwitchIdentityResolver{name: "target"},
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
	}
	wireFakeProjectSessionPlan(cmd)

	reopen, err := cmd.execute(context.Background(), switchPlan{
		UI:          switchUIPopup,
		Selection:   target,
		SessionName: "target",
	}, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if reopen {
		t.Fatal("execute() reopen = true, want false")
	}
	if got, want := sessions.calls, []string{"authorize:" + target, "ensure:target", "open:target"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want project trust before create", got)
	}
}

func TestSwitchExecuteSidebarExistingHookProjectOpensDirectly(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".projmux", "config.toml"), []byte("[startup]\nrun = \"agent\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := &capturingSwitchSessionExecutor{exists: map[string]bool{"target": true}}
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &switchCommand{
		tmuxRunner: tmuxRunner,
		sessions:   sessions,
		identity:   stubSwitchIdentityResolver{name: "target"},
	}

	_, err := cmd.execute(context.Background(), switchPlan{
		UI:          switchUISidebar,
		Selection:   target,
		SessionName: "target",
	}, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if sessions.ensureSessionName != "" || sessions.openSessionName != "target" {
		t.Fatalf("sessions = ensure %q open %q, want direct open target", sessions.ensureSessionName, sessions.openSessionName)
	}
	if len(tmuxRunner.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want none", tmuxRunner.calls)
	}
}

func TestAppRunSwitchUsesNativePickerWithoutBackendLookup(t *testing.T) {
	t.Parallel()

	var compatCalled bool
	var gotNativeOptions intpicker.Options
	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{"workspace": true},
	}

	app := &App{
		switcher: &switchCommand{
			discover: func(candidates.Inputs) ([]string, error) {
				return []string{"/home/tester/workspace"}, nil
			},
			pinStore: func() (switchPinStore, error) {
				return newStubPinStore(), nil
			},
			runner: switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
				compatCalled = true
				return intpickercompat.Result{}, nil
			}),
			nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
				gotNativeOptions = options
				return intpicker.Result{Key: "enter", Value: "/home/tester/workspace"}, nil
			}),
			sessions:   executor,
			executable: func() (string, error) { return "/tmp/projmux", nil },
			identity:   stubSwitchIdentityResolver{name: "workspace"},
			validate:   func(string) error { return nil },
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/home/tester/workspace", nil },
			lookupEnv: func(name string) string {
				if name == "PROJMUX_PICKER_BACKEND" {
					t.Fatal("retired picker backend env was looked up")
				}
				return ""
			},
		},
	}

	if err := app.Run([]string{"switch"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called instead of native picker")
	}
	if got, want := gotNativeOptions.UI, switchUIPopup; got != want {
		t.Fatalf("native UI = %q, want %q", got, want)
	}
	if len(gotNativeOptions.Items) != 1 || gotNativeOptions.Items[0].Value != "/home/tester/workspace" {
		t.Fatalf("native items = %#v, want switch picker item", gotNativeOptions.Items)
	}
	if gotNativeOptions.Theme == nil || gotNativeOptions.Theme.Background.Source != theme.SourceFallback {
		t.Fatalf("native theme = %#v, want fallback effective theme populated", gotNativeOptions.Theme)
	}
	if got := gotNativeOptions.Items[0].MetaLines; len(got) != 0 {
		t.Fatalf("native item meta lines = %#v, want merged into Label to avoid duplicate card metadata", got)
	}
	if got := gotNativeOptions.Items[0].Label; strings.Count(got, "~/workspace") != 1 {
		t.Fatalf("native item label = %q, want one rendered path/git metadata line", got)
	}
	if got, want := executor.openSessionName, "workspace"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
}

func TestSwitchCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { gotRunnerOptions = o },
			reply: intpickercompat.Result{Value: "/tmp/app"}},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		executable:   func() (string, error) { return "/tmp/projmux", nil },
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
	}
	wireFakeProjectSessionPlan(cmd)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Footer, "Alt-P: pin project  |  Ctrl-X: kill session"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewCommand, ""; got != want {
		t.Fatalf("runner preview command = %q, want deferred preview", got)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "down,25%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want reserved deferred preview frame %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"focus:execute-silent(exec '/tmp/projmux' 'switch' 'sidebar-focus' {2})",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intpickercompat.Entry{
		expectedSidebarEntry("app", "/tmp/app", "/tmp/app", "existing", false),
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestSwitchSidebarFooterReadsKeymapGuide(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings."Sidebar:PinProject"]
keys = ["p", "M-p"]

[bindings."Sidebar:KillSession"]
keys = ["K"]
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := switchPickerFooter(switchUISidebar, "", func() (string, error) { return home, nil }, func(string) string { return "" })
	want := "Alt-P: pin project  |  K: kill session"
	if got != want {
		t.Fatalf("switchPickerFooter() = %q, want %q", got, want)
	}
}

func TestSwitchCommandNativeSidebarSetsTitle(t *testing.T) {
	t.Parallel()

	var gotNativeOptions intpicker.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner: switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			t.Fatal("compat runner should not be called for native sidebar")
			return intpickercompat.Result{}, nil
		}),
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotNativeOptions = options
			return intpicker.Result{Value: "/tmp/app"}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := gotNativeOptions.UI, switchUISidebar; got != want {
		t.Fatalf("native UI = %q, want %q", got, want)
	}
	if got, want := gotNativeOptions.Title, "Projects"; got != want {
		t.Fatalf("native title = %q, want %q", got, want)
	}
}

func TestSwitchCommandSidebarRowsIncludeAttentionBadge(t *testing.T) {
	t.Parallel()

	var gotNativeOptions intpicker.Options
	var gotDeferred intpicker.DeferredUpdate
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotNativeOptions = options
			var err error
			gotDeferred, err = options.DeferredUpdate()
			if err != nil {
				t.Fatalf("DeferredUpdate() error = %v", err)
			}
			return intpicker.Result{}, nil
		}),
		sessions: &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		inventory: &stubPreviewInventory{panes: []corepreview.Pane{{
			SessionName:    "tmp-app",
			Title:          "server",
			AttentionState: attentionStateBusy,
		}}},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	initialLabel := gotNativeOptions.Items[0].EffectiveLabel()
	if got, want := len(strings.Split(initialLabel, "\n")), 3; got != want {
		t.Fatalf("initial row line count = %d, want card-like 3-line sidebar row: %q", got, initialLabel)
	}
	deferredLabel := gotDeferred.Items[0].EffectiveLabel()
	if got, want := len(strings.Split(deferredLabel, "\n")), 3; got != want {
		t.Fatalf("deferred row line count = %d, want card-like 3-line sidebar row: %q", got, deferredLabel)
	}
	if !strings.Contains(deferredLabel, "●") {
		t.Fatalf("deferred entry = %q, want attention marker", deferredLabel)
	}
	if got, want := gotDeferred.Preview.Window, "down,25%,border-top"; got != want {
		t.Fatalf("deferred preview window = %q, want %q", got, want)
	}
}

func TestSwitchCommandSidebarUsesBulkExistingSessionMap(t *testing.T) {
	t.Parallel()

	executor := &bulkSwitchSessionExecutor{existing: map[string]bool{"tmp-live": true}}
	cmd := &switchCommand{
		sessions: executor,
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/live":
				return "tmp-live", nil
			case "/tmp/new":
				return "tmp-new", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		homeDir:  func() (string, error) { return "/home/tester", nil },
	}

	rows, _, _, err := cmd.renderRows(context.Background(), switchUISidebar, []string{"/tmp/live", "/tmp/new"})
	if err != nil {
		t.Fatalf("renderRows() error = %v", err)
	}
	if executor.bulkCalls != 1 {
		t.Fatalf("ExistingSessions calls = %d, want 1", executor.bulkCalls)
	}
	if len(executor.existsCalls) != 0 {
		t.Fatalf("SessionExists calls = %q, want none", executor.existsCalls)
	}
	if got, want := rows[0].Value, "/tmp/live"; got != want {
		t.Fatalf("first row value = %q, want existing session first", got)
	}
	if !strings.Contains(rows[0].Label, "\x1b[32mlive\x1b[0m") {
		t.Fatalf("existing row label = %q, want existing styling", rows[0].Label)
	}
	if !strings.Contains(rows[1].Label, "new") || strings.Contains(rows[1].Label, "\x1b[32mnew") {
		t.Fatalf("new row label = %q, want new styling", rows[1].Label)
	}
}

func TestSwitchCommandBulkExistingSessionFailureFallsBackPerCandidate(t *testing.T) {
	t.Parallel()

	executor := &bulkSwitchSessionExecutor{
		existing: map[string]bool{"tmp-live": true},
		bulkErr:  errors.New("list failed"),
	}
	cmd := &switchCommand{
		sessions: executor,
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/live":
				return "tmp-live", nil
			case "/tmp/live-copy":
				return "tmp-live", nil
			case "/tmp/new":
				return "tmp-new", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		homeDir:  func() (string, error) { return "/home/tester", nil },
	}

	if _, _, _, err := cmd.renderRows(context.Background(), switchUISidebar, []string{"/tmp/live", "/tmp/live-copy", "/tmp/new"}); err != nil {
		t.Fatalf("renderRows() error = %v", err)
	}
	if executor.bulkCalls != 1 {
		t.Fatalf("ExistingSessions calls = %d, want 1", executor.bulkCalls)
	}
	if got, want := sortedStrings(executor.existsCalls), []string{"tmp-live", "tmp-new"}; !equalStrings(got, want) {
		t.Fatalf("SessionExists calls = %q, want unique fallback calls %q", got, want)
	}
}

func TestSwitchCommandNativeSidebarDefersGitWindowsAttentionAndPreview(t *testing.T) {
	t.Parallel()

	var gitCalls []string
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{{Index: "0", Name: "main", Active: true}},
		panes: []corepreview.Pane{{
			SessionName:    "tmp-app",
			WindowIndex:    "0",
			Title:          "server",
			AttentionState: attentionStateBusy,
		}},
	}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			if len(gitCalls) != 0 {
				t.Fatalf("git calls before first paint = %q, want none", gitCalls)
			}
			if len(inventory.sessionWindowsSessions) != 0 || len(inventory.sessionPanesSessions) != 0 {
				t.Fatalf("inventory before first paint = windows %q panes %q, want none", inventory.sessionWindowsSessions, inventory.sessionPanesSessions)
			}
			if options.Preview.Command != "" {
				t.Fatalf("preview command before first paint = %q, want deferred", options.Preview.Command)
			}
			if options.Preview.Window != "down,25%,border-top" {
				t.Fatalf("preview window before first paint = %q, want reserved deferred preview frame", options.Preview.Window)
			}
			initialLabel := options.Items[0].EffectiveLabel()
			if got, want := len(strings.Split(initialLabel, "\n")), 3; got != want {
				t.Fatalf("initial row line count = %d, want card-like 3-line sidebar row: %q", got, initialLabel)
			}
			if strings.Contains(initialLabel, "branch-main") || strings.Contains(initialLabel, " server ") || strings.Contains(initialLabel, "●") {
				t.Fatalf("initial item = %q, want reserved lanes without metadata", initialLabel)
			}
			update, err := options.DeferredUpdate()
			if err != nil {
				t.Fatalf("DeferredUpdate() error = %v", err)
			}
			if update.Preview.Command == "" || update.Preview.Window != "down,25%,border-top" {
				t.Fatalf("deferred preview = %#v, want sidebar preview command", update.Preview)
			}
			label := update.Items[0].EffectiveLabel()
			for _, want := range []string{"branch-main", " main ", "●"} {
				if !strings.Contains(label, want) {
					t.Fatalf("deferred label = %q, want metadata %q", label, want)
				}
			}
			return intpicker.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		inventory:  inventory,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		gitBranch: func(path string) string {
			gitCalls = append(gitCalls, path)
			return "branch-main"
		},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := gitCalls, []string{"/tmp/app"}; !equalStrings(got, want) {
		t.Fatalf("git calls after deferred update = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSessions, []string{"tmp-app"}; !equalStrings(got, want) {
		t.Fatalf("SessionWindows calls = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSessions, []string{"", "tmp-app"}; !equalStrings(got, want) {
		t.Fatalf("SessionPanes calls = %q, want all-session attention plus row tabs %q", got, want)
	}
}

func TestSwitchCommandDeferredSidebarEnrichmentUpdatesAllRowsWithoutChangingValues(t *testing.T) {
	t.Parallel()

	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{{Index: "0", Name: "main", Active: true}},
		panes: []corepreview.Pane{
			{SessionName: "tmp-api", WindowIndex: "0", Title: "api", AttentionState: attentionStateReply},
			{SessionName: "tmp-web", WindowIndex: "0", Title: "web", AttentionState: attentionStateBusy},
		},
	}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/api", "/tmp/web"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			initialValues := pickerItemValues(options.Items)
			update, err := options.DeferredUpdate()
			if err != nil {
				t.Fatalf("DeferredUpdate() error = %v", err)
			}
			if got := pickerItemValues(update.Items); !equalStrings(got, initialValues) {
				t.Fatalf("deferred values = %q, want unchanged %q", got, initialValues)
			}
			if got := pickerItemSearchTexts(update.Items); !equalStrings(got, pickerItemSearchTexts(options.Items)) {
				t.Fatalf("deferred search texts = %q, want unchanged", got)
			}
			labels := strings.Join([]string{update.Items[0].EffectiveLabel(), update.Items[1].EffectiveLabel()}, "\n")
			for _, want := range []string{"api-branch", "web-branch", " main ", "●"} {
				if !strings.Contains(labels, want) {
					t.Fatalf("deferred labels = %q, want metadata %q", labels, want)
				}
			}
			return intpicker.Result{Value: "/tmp/web"}, nil
		}),
		sessions: &capturingSwitchSessionExecutor{exists: map[string]bool{
			"tmp-api": true,
			"tmp-web": true,
		}},
		inventory:  inventory,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		gitBranch: func(path string) string {
			return strings.TrimPrefix(path, "/tmp/") + "-branch"
		},
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			return "tmp-" + strings.TrimPrefix(path, "/tmp/"), nil
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSwitchCommandSidebarAggregatesSemanticBadgePriority(t *testing.T) {
	t.Parallel()

	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "0", Name: "main", Active: true},
			{Index: "1", Name: "worker"},
		},
		panes: []corepreview.Pane{
			{SessionName: "tmp-app", WindowIndex: "0", Title: "running", AIState: "thinking", AIBadgeKind: aiBadgeKindInProgress},
			{SessionName: "tmp-app", WindowIndex: "0", Title: "done", AIState: "waiting", AIBadgeKind: aiBadgeKindResponseComplete},
			{SessionName: "tmp-app", WindowIndex: "1", Title: "approve", AIState: "waiting", AIBadgeKind: aiBadgeKindApprovalRequired},
		},
	}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			update, err := options.DeferredUpdate()
			if err != nil {
				t.Fatalf("DeferredUpdate() error = %v", err)
			}
			label := update.Items[0].EffectiveLabel()
			if !strings.Contains(label, "\x1b[38;5;214m●\x1b[0m") {
				t.Fatalf("deferred label = %q, want prompt-required warning badge", label)
			}
			if !strings.Contains(label, " main ") || !strings.Contains(label, " worker ") {
				t.Fatalf("deferred label = %q, want both semantic window tabs", label)
			}
			return intpicker.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		inventory:  inventory,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSwitchCommandPopupRowsUseSemanticSessionBadge(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		sessions: &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		inventory: &stubPreviewInventory{
			windows: []corepreview.Window{{Index: "0", Name: "main", Active: true}},
			panes: []corepreview.Pane{
				{SessionName: "tmp-app", WindowIndex: "0", Title: "work", AIBadgeKind: aiBadgeKindInProgress},
				{SessionName: "tmp-app", WindowIndex: "0", Title: "approve", AIBadgeKind: aiBadgeKindInputRequired},
			},
		},
		identity: stubSwitchIdentityResolver{name: "tmp-app"},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		homeDir:  func() (string, error) { return "/home/tester", nil },
	}

	rows, _, _, err := cmd.renderRows(context.Background(), switchUIPopup, []string{"/tmp/app"})
	if err != nil {
		t.Fatalf("renderRows() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want one existing popup row", rows)
	}
	if !strings.Contains(rows[0].Label, "\x1b[38;5;214m●\x1b[0m") {
		t.Fatalf("popup label = %q, want semantic prompt warning badge", rows[0].Label)
	}
}

func TestSwitchCommandSidebarUsesContextSessionForInitialPosition(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { gotRunnerOptions = o }},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/a", "/tmp/b"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions: &capturingSwitchSessionExecutor{
			exists: map[string]bool{"session-b": true},
		},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/a":
				return "session-a", nil
			case "/tmp/b":
				return "session-b", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp/a/deeper", nil },
		lookupEnv: func(name string) string {
			if name == switchContextSessionEnv {
				return "session-b"
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"focus:execute-silent(exec '/tmp/projmux' 'switch' 'sidebar-focus' {2})",
		"start:pos(1)",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
}

func TestSwitchCommandSidebarFocusOpensExistingSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{"tmp-app": true},
	}
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "tmp-app"},
	}

	if err := cmd.Run([]string{"sidebar-focus", "/tmp/app"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := executor.openSessionName, "tmp-app"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got := executor.ensureSessionName; got != "" {
		t.Fatalf("ensure session called unexpectedly: %q", got)
	}
}

func TestSwitchProjectOpenStartupPickerHasExactlyTwoActions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	project := filepath.Join(home, "workspace")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	var startupOptions intpickercompat.Options
	executor := &capturingSwitchSessionExecutor{}
	runner, native := scriptedPicker(t, []pickerStep{{
		observe: func(o intpickercompat.Options) { startupOptions = o },
		reply:   intpickercompat.Result{Value: projectStartupValueTopology},
	}})
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		runner:       runner,
		nativePicker: native,
	}
	wireFakeProjectSessionPlan(cmd)

	if err := cmd.openProjectTarget(context.Background(), project, "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := startupOptions.UI, "project-startup"; got != want {
		t.Fatalf("startup UI = %q, want %q", got, want)
	}
	if got, want := len(startupOptions.Entries), 2; got != want {
		t.Fatalf("startup rows = %d, want %d: %#v", got, want, startupOptions.Entries)
	}
	requireSwitchEntryLabel(t, startupOptions.Entries, "Continue project")
	requireSwitchEntryLabel(t, startupOptions.Entries, "Open fresh")
	requireSwitchEntryValueOrder(t, startupOptions.Entries, []string{
		projectStartupValueTopology,
		projectStartupValueNew,
	})
	if got, want := executor.ensureSessionName, "workspace"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
}
func TestUsageOmitsLayoutPrimaryCommand(t *testing.T) {
	t.Parallel()

	var usage bytes.Buffer
	if err := cli.RenderRootHelp(&usage); err != nil {
		t.Fatalf("RenderRootHelp returned error: %v", err)
	}
	if strings.Contains(usage.String(), "\n  layout") {
		t.Fatalf("usage = %q, want no primary layout command", usage.String())
	}
	if !strings.Contains(usage.String(), "  restore") {
		t.Fatalf("usage = %q, want canonical snapshot restore surface", usage.String())
	}
}

func TestSwitchSidebarProjectStartupEscReturnsToProjectsWithoutHandoff(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	project := filepath.Join(home, "workspace")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	var startupOptions intpickercompat.Options
	executor := &capturingSwitchSessionExecutor{}
	tmux := &recordingTmuxRunner{}
	cmd := &switchCommand{
		sessions:   executor,
		identity:   stubSwitchIdentityResolver{name: "workspace"},
		tmuxRunner: tmux,
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			startupOptions = options
			return intpickercompat.Result{}, nil
		})),
	}

	reopen, err := cmd.execute(context.Background(), switchPlan{
		UI:          switchUISidebar,
		Selection:   project,
		SessionName: "workspace",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if !reopen {
		t.Fatal("execute() reopen = false, want project list after Esc")
	}
	if got, want := startupOptions.Header, "Start project"; got != want {
		t.Fatalf("startup header = %q, want %q", got, want)
	}
	if got, want := len(startupOptions.Entries), 2; got != want {
		t.Fatalf("startup rows = %d, want %d", got, want)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want no popup handoff", tmux.calls)
	}
	if executor.ensureSessionName != "" || executor.openSessionName != "" || executor.authorizeCalled {
		t.Fatalf("Esc should not create/open/authorize: %#v", executor)
	}
}

func TestProjectStartupPickerFailureFallsBackToContinue(t *testing.T) {
	t.Parallel()
	cmd := &switchCommand{
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{}, errors.New("picker unavailable")
		})),
	}
	if got := cmd.pickProjectStartupMode("workspace", "/tmp/workspace"); got.Kind != projectStartupKindTopology {
		t.Fatalf("picker failure mode = %+v, want Continue project", got)
	}
}

func TestProjectStartupPickerLabelStateColors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		candidate projectStartupCandidate
		wantColor string
	}{
		{name: "continue", candidate: topologyProjectStartupCandidate(), wantColor: settingsColorType},
		{name: "fresh", candidate: newProjectStartupCandidate(), wantColor: settingsColorType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label := projectStartupPickerLabel(tc.candidate)
			if !strings.Contains(label, tc.wantColor) {
				t.Fatalf("projectStartupPickerLabel(%s) = %q, want color %q", tc.name, label, tc.wantColor)
			}
		})
	}
}
func TestAppRunLayoutCommandRemovedFromPublicSurface(t *testing.T) {
	t.Parallel()

	err := New().Run([]string{"layout", "list"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown command: layout") {
		t.Fatalf("Run(layout list) error = %v, want unknown command", err)
	}
}

func TestSwitchProjectOpenExistingSessionSkipsStartupPicker(t *testing.T) {
	t.Parallel()

	var pickerCalled bool
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"workspace": true}}
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(intpickercompat.Options) { pickerCalled = true }, reply: intpickercompat.Result{Value: projectStartupValueTopology}},
	})
	cmd := &switchCommand{
		sessions:     executor,
		identity:     stubSwitchIdentityResolver{name: "workspace"},
		runner:       runner,
		nativePicker: native,
	}
	wireFakeProjectSessionPlan(cmd)

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if pickerCalled {
		t.Fatal("startup picker was called for an existing session")
	}
	if executor.ensureSessionName != "" || executor.openSessionName != "workspace" {
		t.Fatalf("sessions = ensure %q open %q, want open existing only", executor.ensureSessionName, executor.openSessionName)
	}
}

func TestSwitchProjectOpenStartupPickerOffCreatesEmptyWithoutPicker(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var pickerCalled bool
	executor := &capturingSwitchSessionExecutor{}
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(intpickercompat.Options) { pickerCalled = true }},
	})
	cmd := &switchCommand{
		sessions:     executor,
		identity:     stubSwitchIdentityResolver{name: "workspace"},
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		runner:       runner,
		nativePicker: native,
	}
	wireFakeProjectSessionPlan(cmd)

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if pickerCalled {
		t.Fatal("startup picker was called when disabled")
	}
	if executor.ensureSessionName != "workspace" || executor.openSessionName != "workspace" {
		t.Fatalf("sessions = ensure %q open %q, want empty create and open", executor.ensureSessionName, executor.openSessionName)
	}
}

func TestSwitchProjectOpenTrustDenyAfterStartupSelectionAbortsWithoutSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	var pickerCalled bool
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: false}
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(intpickercompat.Options) { pickerCalled = true }, reply: intpickercompat.Result{Value: projectStartupValueTopology}},
	})
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		runner:       runner,
		nativePicker: native,
	}

	err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace")
	if !errors.Is(err, errProjectTrustDenied) {
		t.Fatalf("openProjectTarget() error = %v, want trust denied", err)
	}
	if !executor.authorizeCalled {
		t.Fatal("trust gate was not checked")
	}
	if !pickerCalled {
		t.Fatal("startup picker was not called before trust gate")
	}
	if executor.ensureSessionName != "" || executor.ensureCWD != "" || executor.openSessionName != "" {
		t.Fatalf("deny should not create/open a session: %#v", executor)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestSwitchProjectOpenStartupPickerOffStillChecksTrustBeforeCreate(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:  executor,
		identity:  stubSwitchIdentityResolver{name: "workspace"},
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
	}
	wireFakeProjectSessionPlan(cmd)

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace", "ensure:workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestSwitchCommandMarksExistingSessionsInRows(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { gotRunnerOptions = o }},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/new-app", "/tmp/live-app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions: &capturingSwitchSessionExecutor{
			exists: map[string]bool{"tmp-live-app": true},
		},
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/live-app":
				return "tmp-live-app", nil
			case "/tmp/new-app":
				return "tmp-new-app", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Entries, []intpickercompat.Entry{
		{Label: "\x1b[1m\x1b[32mlive-app\x1b[0m\n  \x1b[38;5;242m/tmp/live-app\x1b[0m", Value: "/tmp/live-app"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestNewSwitchCommandUsesEnvAndDefaultPinStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("pins/app")
	fixture.mkdir("rp/repo-a")
	fixture.mkdir("managed/work-a/nested")
	fixture.mkdir("managed/work-b")

	configHome := fixture.path("xdg-config")
	stateHome := fixture.path("xdg-state")
	t.Setenv("HOME", fixture.path("home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, fixture.path("rp"))
	t.Setenv(managedRootsEnvVar, fixture.path("managed"))

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.PinFile()), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(paths.PinFile(), []byte(fixture.path("pins/app")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Chdir(fixture.path("managed/work-a/nested"))

	cmd := newSwitchCommand()
	fakeRunner := &capturingSwitchRunner{result: intpickercompat.Result{Value: fixture.path("managed/work-a")}}
	fakeExecutor := &capturingSwitchSessionExecutor{}
	cmd.runner = fakeRunner
	cmd.nativePicker = nativePickerFromCompatRunner(fakeRunner)
	cmd.sessions = fakeExecutor
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.rawExecutable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.tmuxRunner = &recordingTmuxRunner{}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	wantEntries := []intpickercompat.Entry{
		expectedCheapSidebarEntry("home", "~", fixture.path("home"), false),
		expectedCheapSidebarEntry("app", fixture.path("pins/app"), fixture.path("pins/app"), true),
		expectedCheapSidebarEntry("repo-a", "~rp/repo-a", fixture.path("rp/repo-a"), false),
		expectedCheapSidebarEntry("work-a", fixture.path("managed/work-a"), fixture.path("managed/work-a"), false),
		expectedCheapSidebarEntry("work-b", fixture.path("managed/work-b"), fixture.path("managed/work-b"), false),
		// The Registry-first list ends with the Runtime escape hatch. This
		// fixture has no Registry, so every directory is an unregistered
		// candidate and the link reports no transport.
		expectedCheapSidebarEntry("Runtime", "Runtime - no tmux transport", switchRuntimeSentinel, false),
	}
	if got := fakeRunner.last.Entries; !equalEntries(got, wantEntries) {
		t.Fatalf("runner entries = %#v, want %#v", got, wantEntries)
	}
	if got, want := fakeRunner.last.PreviewCommand, ""; got != want {
		t.Fatalf("runner preview command = %q, want deferred preview", got)
	}
	if got, want := fakeRunner.last.PreviewWindow, "down,25%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want reserved deferred preview frame %q", got, want)
	}
	if got, want := fakeRunner.last.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"focus:execute-silent(exec '/tmp/projmux' 'switch' 'sidebar-focus' {2})",
		"start:pos(4)",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.Footer, "Alt-P: pin project  |  Ctrl-X: kill session"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got := fakeExecutor.ensureSessionName; got != "" {
		t.Fatalf("ensure session = %q, want continuation handoff", got)
	}
	if got := fakeExecutor.ensureCWD; got != "" {
		t.Fatalf("ensure cwd = %q, want continuation handoff", got)
	}
	if got := fakeExecutor.openSessionName; got != "" {
		t.Fatalf("open session = %q, want continuation handoff", got)
	}
}

func TestNewSwitchCommandDoesNotInferRepoRootFromHomeSourceRepos(t *testing.T) {
	fixture := newSwitchFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("home/source/repos/app/nested")
	fixture.mkdir("home/source/repos/lib")

	t.Setenv("HOME", fixture.path("home"))
	t.Setenv("XDG_CONFIG_HOME", fixture.path("xdg-config"))
	t.Setenv("XDG_STATE_HOME", fixture.path("xdg-state"))
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")
	t.Chdir(fixture.path("home/source/repos/app/nested"))

	cmd := newSwitchCommand()
	fakeRunner := &capturingSwitchRunner{result: intpickercompat.Result{}}
	cmd.runner = fakeRunner
	cmd.nativePicker = nativePickerFromCompatRunner(fakeRunner)
	cmd.sessions = &capturingSwitchSessionExecutor{}
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.rawExecutable = func() (string, error) { return "/tmp/projmux", nil }

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantEntries := []intpickercompat.Entry{
		expectedCheapSidebarEntry("home", "~", fixture.path("home"), false),
		expectedCheapSidebarEntry("repos", "~/source/repos", fixture.path("home/source/repos"), false),
		expectedCheapSidebarEntry("Runtime", "Runtime - no tmux transport", switchRuntimeSentinel, false),
	}
	if got := fakeRunner.last.Entries; !equalEntries(got, wantEntries) {
		t.Fatalf("runner entries = %#v, want %#v", got, wantEntries)
	}
}

func TestSwitchCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "invalid ui",
			args: []string{"--ui=dialog"},
			want: "invalid --ui value",
		},
		{
			name: "positional args",
			args: []string{"extra"},
			want: "switch does not accept positional arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner, native := scriptedPicker(t, nil)
			var stderr bytes.Buffer
			err := (&switchCommand{
				discover:     func(candidates.Inputs) ([]string, error) { return nil, nil },
				pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
				runner:       runner,
				nativePicker: native,
				sessions:     &capturingSwitchSessionExecutor{},
				identity:     stubSwitchIdentityResolver{name: "tmp"},
				validate:     func(string) error { return nil },
				homeDir:      func() (string, error) { return "/home/tester", nil },
				workingDir:   func() (string, error) { return "/tmp", nil },
			}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestSwitchCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	emptyRunner, emptyNative := scriptedPicker(t, nil)
	errRunner, errNative := scriptedPicker(t, []pickerStep{
		{err: errors.New("picker exploded")},
	})
	appRunner, appNative := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Value: "/tmp/app"}},
	})
	appRunner2, appNative2 := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Value: "/tmp/app"}},
	})

	tests := []struct {
		name string
		cmd  *switchCommand
		want string
	}{
		{
			name: "home dir",
			cmd: &switchCommand{
				homeDir: func() (string, error) { return "", errors.New("no home") },
			},
			want: "resolve home directory",
		},
		{
			name: "pin store",
			cmd: &switchCommand{
				homeDir:  func() (string, error) { return "/home/tester", nil },
				pinStore: func() (switchPinStore, error) { return nil, errors.New("no config") },
			},
			want: "configure pin store",
		},
		{
			name: "working dir",
			cmd: &switchCommand{
				homeDir:      func() (string, error) { return "/home/tester", nil },
				pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
				runner:       emptyRunner,
				nativePicker: emptyNative,
				workingDir: func() (string, error) {
					return "", errors.New("no cwd")
				},
			},
			want: "resolve current working directory",
		},
		{
			name: "runner",
			cmd: &switchCommand{
				discover:     func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:      func() (string, error) { return "/home/tester", nil },
				pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
				workingDir:   func() (string, error) { return "/tmp", nil },
				identity:     stubSwitchIdentityResolver{name: "tmp-app"},
				runner:       errRunner,
				nativePicker: errNative,
			},
			want: "run native switch picker",
		},
		{
			name: "identity setup",
			cmd: &switchCommand{
				discover:     func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:      func() (string, error) { return "/home/tester", nil },
				pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
				workingDir:   func() (string, error) { return "/tmp", nil },
				runner:       appRunner,
				nativePicker: appNative,
				validate:     func(string) error { return nil },
				identityErr:  errors.New("missing home"),
			},
			want: "configure session identity resolver",
		},
		{
			name: "open session",
			cmd: &switchCommand{
				discover:     func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:      func() (string, error) { return "/home/tester", nil },
				pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
				workingDir:   func() (string, error) { return "/tmp", nil },
				runner:       appRunner2,
				nativePicker: appNative2,
				identity:     stubSwitchIdentityResolver{name: "tmp-app"},
				validate:     func(string) error { return nil },
				sessions: &capturingSwitchSessionExecutor{
					openErr: errors.New("attach exploded"),
				},
			},
			want: "open tmux session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.name == "open session" {
				wireFakeProjectSessionPlan(tt.cmd)
			}

			err := tt.cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSwitchCommandAllowsEmptySelection(t *testing.T) {
	t.Parallel()

	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover:     func(candidates.Inputs) ([]string, error) { return []string{"/tmp/a"}, nil },
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-a"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

func TestSwitchCommandToggleTagUsesCurrentSnappedCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested")

	store := &capturingSwitchTagStore{tagged: true}
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		tagStore: func() (switchTagStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("managed/work-a/nested"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"toggle-tag"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.calls, []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "tagged: "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestSwitchCommandUsesWeakManagedRootHeuristicsWhenEnvUnset(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    func(string) string { return "" },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/home/tester/source",
		"/home/tester/work",
		"/home/tester/projects",
		"/home/tester/src",
		"/home/tester/code",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandUsesSavedWorkdirsWhenEnvUnset(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    func(string) string { return "" },
		loadWorkdirs: func(homeDir string) ([]string, error) {
			if homeDir != "/home/tester" {
				t.Fatalf("loadWorkdirs homeDir = %q, want /home/tester", homeDir)
			}
			return []string{"/srv/projects", "/srv/lib"}, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/srv/projects",
		"/srv/lib",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandManagedRootsEnvBeatsSavedWorkdirs(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return "/env/one:/env/two"
			}
			return ""
		},
		loadWorkdirs: func(string) ([]string, error) {
			t.Fatalf("loadWorkdirs should not be consulted when env is set")
			return nil, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/env/one",
		"/env/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandMultiPathProjdirSplitsPrimaryAndExtras(t *testing.T) {
	t.Setenv("TMUX", "")

	multi := strings.Join([]string{"/main/repo", "/extra/one", "/extra/two"}, string(os.PathListSeparator))
	t.Setenv(projdirEnvVar, multi)

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    os.Getenv,
		loadWorkdirs: func(string) ([]string, error) {
			t.Fatalf("loadWorkdirs should not be consulted when PROJMUX_PROJDIR provides extras")
			return nil, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.RepoRoot, "/main/repo"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{
		"/extra/one",
		"/extra/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandMultiPathProjdirCombinesWithManagedRootsEnv(t *testing.T) {
	t.Setenv("TMUX", "")

	multi := strings.Join([]string{"/main/repo", "/extra/one"}, string(os.PathListSeparator))
	t.Setenv(projdirEnvVar, multi)
	t.Setenv(managedRootsEnvVar, strings.Join([]string{"/extra/one", "/managed/two"}, string(os.PathListSeparator)))

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    os.Getenv,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// extras prepend; PROJMUX_MANAGED_ROOTS follows, dedupe drops the
	// repeated /extra/one.
	if got, want := gotInputs.ManagedRoots, []string{
		"/extra/one",
		"/managed/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandSinglePathProjdirRetainsSavedWorkdirs(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "/main/repo")

	var gotInputs candidates.Inputs
	runner, native := scriptedPicker(t, nil)
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    os.Getenv,
		loadWorkdirs: func(string) ([]string, error) {
			return []string{"/srv/projects"}, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.RepoRoot, "/main/repo"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{"/srv/projects"}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q (single-path PROJMUX_PROJDIR must not suppress saved workdirs)", got, want)
	}
}

func TestProjectDiscoveryInputsReadOnlyPathNeverMemoizes(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		pinStore: func() (switchPinStore, error) {
			return newCandidateStubPinStore("/pinned/project"), nil
		},
		homeDir: func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) {
			t.Fatal("project root discovery must not consult the app working directory")
			return "", nil
		},
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return strings.Join([]string{"/repos", "/managed/extra"}, string(os.PathListSeparator))
			}
			return ""
		},
		tmuxProjdir: func() string {
			t.Fatal("env project root should win before tmux option")
			return ""
		},
		loadProjdir: func(string) (string, error) {
			t.Fatal("env project root should win before saved config")
			return "", nil
		},
		saveProjdir: func(string, string) error {
			t.Fatal("read-only project discovery must never memoize")
			return nil
		},
		loadWorkdirs: func(string) ([]string, error) {
			t.Fatal("extra PROJMUX_PROJDIR roots suppress saved workdirs")
			return nil, nil
		},
	}

	got, err := cmd.projectDiscoveryInputs(false)
	if err != nil {
		t.Fatal(err)
	}
	want := candidates.Inputs{
		HomeDir:      "/home/tester",
		RepoRoot:     "/repos",
		ManagedRoots: []string{"/managed/extra"},
		Pins:         []string{"/pinned/project"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projectDiscoveryInputs(false) = %#v, want %#v", got, want)
	}
}

func TestDetectGitBranchWithRunnerPreservesNormalAndDetachedPaths(t *testing.T) {
	t.Parallel()

	t.Run("symbolic ref", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		got := detectGitBranchWithRunner("/repo", time.Second, func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return []byte("main\n"), nil
		})
		if got != "main" {
			t.Fatalf("detectGitBranchWithRunner() = %q, want main", got)
		}
		want := [][]string{{"git", "-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		got := detectGitBranchWithRunner("/repo", time.Second, func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			if slices.Contains(args, "symbolic-ref") {
				return nil, errors.New("detached")
			}
			return []byte("abc1234\n"), nil
		})
		if got != "abc1234" {
			t.Fatalf("detectGitBranchWithRunner() = %q, want abc1234", got)
		}
		want := [][]string{
			{"git", "-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"},
			{"git", "-C", "/repo", "rev-parse", "--short", "HEAD"},
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	})
}

func TestSwitchFullRowRenderDoesNotBlockOnSlowGitRepository(t *testing.T) {
	t.Parallel()

	const branchLimit = 20 * time.Millisecond
	active := 0
	var slowDeadlines []time.Time
	gitBranch := func(path string) string {
		return detectGitBranchWithRunner(path, branchLimit, func(ctx context.Context, _ string, args ...string) ([]byte, error) {
			if slices.Contains(args, "/slow") {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("slow git context has no deadline")
				}
				slowDeadlines = append(slowDeadlines, deadline)
				active++
				defer func() { active-- }()
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []byte("main\n"), nil
		})
	}
	cmd := &switchCommand{
		pinStore:  func() (switchPinStore, error) { return newStubPinStore(), nil },
		sessions:  &bulkSwitchSessionExecutor{existing: map[string]bool{}},
		inventory: &stubPreviewInventory{},
		identity:  switchIdentityResolverFunc(func(path string) (string, error) { return filepath.Base(path), nil }),
		homeDir:   func() (string, error) { return "/home/tester", nil },
		lookupEnv: func(string) string { return "" },
		gitBranch: gitBranch,
	}

	start := time.Now()
	_, items, _, err := cmd.renderFullRows(context.Background(), switchUISidebar, []string{"/slow", "/fast"})
	if err != nil {
		t.Fatalf("renderFullRows() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("renderFullRows() elapsed = %v, want bounded slow-repo enrichment", elapsed)
	}
	if active != 0 {
		t.Fatalf("active hanging git runners = %d, want 0 after timeout", active)
	}
	if len(slowDeadlines) != 2 || !slowDeadlines[0].Equal(slowDeadlines[1]) {
		t.Fatalf("slow git deadlines = %v, want one shared deadline for branch and detached-head probes", slowDeadlines)
	}
	if got := len(items); got != 2 {
		t.Fatalf("item count = %d, want 2", got)
	}
	labels := items[0].EffectiveLabel() + "\n" + items[1].EffectiveLabel()
	if !strings.Contains(labels, "main") {
		t.Fatalf("rendered labels = %q, want normal repository branch", labels)
	}
}

func TestSwitchCommandPreviewRendersExistingSessionContext(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{
		readSelection: corepreview.Selection{
			SessionName: "repo-a",
			WindowIndex: "2",
			PaneIndex:   "1",
		},
		readFound: true,
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "1"},
			{Index: "2", Active: true},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "0"},
			{ID: "%9", WindowIndex: "2", Index: "1", Active: true},
		},
		snapshot: "npm test\nok",
	}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"repo-a": true}},
		previewStore: store,
		inventory:    inventory,
		gitBranch:    func(string) string { return "main" },
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		workingDir:   func() (string, error) { return fixture.path("home/source/repos/repo-a/subdir"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2msession\x1b[0m  repo-a\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[32mexisting\x1b[0m\n" +
		"\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] -                   0p\n" +
		"\x1b[1m\x1b[32m[2] -                   0p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[2.0] -                  -\n" +
		"\x1b[1m\x1b[32m[2.1] -                  -\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPane Snapshot\x1b[0m\n" +
		"\x1b[2m────────────────────────────────────────────────────────────────\x1b[0m\n" +
		"npm test\nok\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "repo-a"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "repo-a"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
	if got, want := inventory.snapshotTarget, "%9"; got != want {
		t.Fatalf("CapturePane target = %q, want %q", got, want)
	}
	if got, want := inventory.snapshotStartLine, -60; got != want {
		t.Fatalf("CapturePane start line = %d, want %d", got, want)
	}
}

func TestSwitchCommandPreviewRendersNewSessionContextWithoutInventory(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	inventory := &stubPreviewInventory{}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		sessions:     &capturingSwitchSessionExecutor{},
		previewStore: &stubPreviewStore{},
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		workingDir:   func() (string, error) { return fixture.path("home/source/repos/repo-a/subdir"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2msession\x1b[0m  repo-a\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[33mnew session\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mAction\x1b[0m\n" +
		"  \x1b[2menter\x1b[0m  switch/create this session\n" +
		"  \x1b[2mresult\x1b[0m  tmux new-session -d -s <name> -c <dir>\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := inventory.sessionWindowsSession; got != "" {
		t.Fatalf("SessionWindows session = %q, want empty", got)
	}
	if got := inventory.sessionPanesSession; got != "" {
		t.Fatalf("SessionPanes session = %q, want empty", got)
	}
}

func TestSwitchCommandPreviewRendersSettingsSentinel(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		pinStore: func() (switchPinStore, error) {
			return newCandidateStubPinStore("/home/tester/source/repos/app"), nil
		},
		homeDir: func() (string, error) { return "/home/tester", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview", switchSettingsSentinel}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"settings\n" +
		"pins:\n" +
		"  * candidate  ~rp/app\n" +
		"keys:\n" +
		"  enter  open settings menu\n" +
		"  alt-p  pin/unpin focused directory\n" +
		"menu:\n" +
		"  + add pin...\n" +
		"  + add current pin\n" +
		"  x remove pin\n" +
		"  x clear all pins\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandSettingsMenuOffersAddCurrentPin(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/app", "/home/tester/source/repos/new-app"}, nil
		},
		pinStore: func() (switchPinStore, error) {
			return newCandidateStubPinStore("/home/tester/source/repos/app"), nil
		},
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		validate:   func(string) error { return nil },
		identity:   stubSwitchIdentityResolver{name: "new-app"},
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	entries, err := cmd.settingsEntries()
	if err != nil {
		t.Fatalf("settingsEntries() error = %v", err)
	}

	want := []intpickercompat.Entry{
		{Label: "+ Add pin...", Value: "add-interactive"},
		{Label: "+ Add current pin  ~rp/new-app", Value: "add:/home/tester/source/repos/new-app"},
		{Label: "x Clear all pins", Value: "clear"},
		{Label: "x Remove  ~rp/app", Value: "pin:/home/tester/source/repos/app"},
	}
	if !equalEntries(entries, want) {
		t.Fatalf("settings entries = %#v, want %#v", entries, want)
	}
}

func TestSwitchCommandSettingsMenuSkipsAddWhenCurrentTargetAlreadyPinned(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/app"}, nil
		},
		pinStore: func() (switchPinStore, error) {
			return newCandidateStubPinStore("/home/tester/source/repos/app"), nil
		},
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/app/subdir", nil },
		validate:   func(string) error { return nil },
		identity:   stubSwitchIdentityResolver{name: "app"},
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	entries, err := cmd.settingsEntries()
	if err != nil {
		t.Fatalf("settingsEntries() error = %v", err)
	}

	want := []intpickercompat.Entry{
		{Label: "+ Add pin...", Value: "add-interactive"},
		{Label: "x Clear all pins", Value: "clear"},
		{Label: "x Remove  ~rp/app", Value: "pin:/home/tester/source/repos/app"},
	}
	if !equalEntries(entries, want) {
		t.Fatalf("settings entries = %#v, want %#v", entries, want)
	}
}

func TestSwitchCommandCycleWindowUpdatesStoredPreviewSelection(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{
		cycleWindowResult: corepreview.CycleResult{
			Cursor:   corepreview.Cursor{WindowIndex: "3", PaneIndex: "1"},
			Selected: true,
			Changed:  true,
		},
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "2"},
			{Index: "3", Active: true},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "3", Index: "1", Active: true},
		},
	}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"repo-a": true}},
		previewStore: store,
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"cycle-window", fixture.path("home/source/repos/repo-a/subdir"), "next"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.cycleWindowSession, "repo-a"; got != want {
		t.Fatalf("cycle window session = %q, want %q", got, want)
	}
	if got, want := store.cycleWindowDirection, corepreview.DirectionNext; got != want {
		t.Fatalf("cycle window direction = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "repo-a"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "repo-a"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
}

func TestSwitchCommandCyclePaneNoOpsForNewSessionCandidates(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{}
	inventory := &stubPreviewInventory{}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		sessions:     &capturingSwitchSessionExecutor{},
		previewStore: store,
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"cycle-pane", fixture.path("home/source/repos/repo-a/subdir"), "prev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := store.cyclePaneSession; got != "" {
		t.Fatalf("cycle pane session = %q, want empty", got)
	}
	if got := inventory.sessionPanesSession; got != "" {
		t.Fatalf("SessionPanes session = %q, want empty", got)
	}
}

func TestSwitchCommandPickerCtrlXSwitchesToPreviousActiveSessionBeforeKill(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions []intpickercompat.Options
	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{
			"tmp-app":      true,
			"tmp-previous": true,
		},
		recentSessions: []string{"tmp-app", "tmp-previous"},
	}
	var cleaned []string
	stopRow := strings.Join([]string{"$7", "tmp-app", "", "", ""}, tmuxRowSep) + "\n"
	stopRunner := &unmanagedStopRunner{appMarker: "1", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux", listRows: []string{stopRow, stopRow, stopRow}}

	observe := func(o intpickercompat.Options) { gotRunnerOptions = append(gotRunnerOptions, o) }
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: observe, reply: intpickercompat.Result{Key: switchKillExpectKey, Value: "/tmp/app"}},
		{observe: observe},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app", "/tmp/previous"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     executor,
		tmuxRunner:   stopRunner,
		lookupEnv:    func(string) string { return "" },
		executable:   func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/app":
				return "tmp-app", nil
			case "/tmp/previous":
				return "tmp-previous", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		cleanupKilledSession: func(sessionName string) {
			cleaned = append(cleaned, sessionName)
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(gotRunnerOptions), 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	for i, options := range gotRunnerOptions {
		if got, want := options.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
		if got, want := options.UI, switchUISidebar; got != want {
			t.Fatalf("runner UI call %d = %q, want %q", i, got, want)
		}
	}
	if !containsString(gotRunnerOptions[1].Bindings, "start:pos(2)") {
		t.Fatalf("second runner bindings = %q, want fallback focus start:pos(2)", gotRunnerOptions[1].Bindings)
	}
	if got := stopRunner.topologyWrites(); got != 1 {
		t.Fatalf("typed unmanaged stop writes = %d, want 1", got)
	}
	if got, want := cleaned, []string{"tmp-app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned sessions = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "tmp-previous"; got != want {
		t.Fatalf("fallback open session = %q, want %q", got, want)
	}
	if got := executor.ensureSessionName; got != "" {
		t.Fatalf("ensure session called unexpectedly: %q", got)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty for ctrl-x loop", got)
	}
}

func TestSwitchCommandPickerCtrlXBlocksKillWithoutPreviousLiveSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists:         map[string]bool{"tmp-app": true},
		recentSessions: []string{"tmp-app"},
	}
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: switchKillExpectKey, Value: "/tmp/app"}},
		{},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     executor,
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := executor.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
	if got := executor.openSessionName; got != "" {
		t.Fatalf("open session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandPickerCtrlXDoesNotKillHome(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"home": true}}
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: switchKillExpectKey, Value: "/home/tester"}},
		{},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     executor,
		identity:     stubSwitchIdentityResolver{name: "home"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/home/tester", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := executor.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandPickerSidebarKillMutatesNativePickerAndRefreshesRows(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{
			"tmp-app":      true,
			"tmp-previous": true,
			"tmp-worker":   true,
		},
		recentSessions: []string{"tmp-app", "tmp-previous", "tmp-worker"},
	}
	executor.killHook = func(sessionName string) {
		executor.exists[sessionName] = false
	}
	stopRow := strings.Join([]string{"$7", "tmp-app", "", "", ""}, tmuxRowSep) + "\n"
	stopRunner := &unmanagedStopRunner{appMarker: "1", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux", listRows: []string{stopRow, stopRow, stopRow}}

	var nativeCalls int
	var mutateCalls int
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app", "/tmp/previous", "/tmp/worker"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			nativeCalls++
			if options.UI != switchUISidebar {
				t.Fatalf("native UI = %q, want %q", options.UI, switchUISidebar)
			}
			var killAction intpicker.Action
			for _, action := range options.Actions {
				if action.Key == switchKillExpectKey {
					killAction = action
					break
				}
			}
			if killAction.Mutate == nil {
				t.Fatalf("kill action = %#v, want mutable native action", killAction)
			}
			update, err := killAction.Mutate(intpicker.ActionContext{
				Key:           switchKillExpectKey,
				Value:         "/tmp/app",
				SelectedIndex: 0,
			})
			if err != nil {
				t.Fatalf("kill mutate error = %v", err)
			}
			mutateCalls++
			values := pickerItemValues(update.Items)
			if !slices.Contains(values, "/tmp/app") {
				t.Fatalf("refreshed values = %q, want killed project path preserved as normal candidate", values)
			}
			if !slices.Contains(values, "/tmp/previous") || !slices.Contains(values, "/tmp/worker") {
				t.Fatalf("refreshed values = %q, want normal neighboring rows", values)
			}
			if update.Preview.Command == "" {
				t.Fatal("refreshed preview command is empty")
			}
			if got, want := update.Preview.Window, switchPreviewWindow(switchUISidebar); got != want {
				t.Fatalf("refreshed preview window = %q, want %q", got, want)
			}
			return intpicker.Result{Closed: true}, nil
		}),
		sessions:   executor,
		tmuxRunner: stopRunner,
		lookupEnv:  func(string) string { return "" },
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/app":
				return "tmp-app", nil
			case "/tmp/previous":
				return "tmp-previous", nil
			case "/tmp/worker":
				return "tmp-worker", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if nativeCalls != 1 {
		t.Fatalf("native calls = %d, want 1 in-place picker session", nativeCalls)
	}
	if mutateCalls != 1 {
		t.Fatalf("mutate calls = %d, want 1", mutateCalls)
	}
	if got, want := executor.calls, []string{"open:tmp-previous"}; !equalStrings(got, want) {
		t.Fatalf("session calls = %q, want %q", got, want)
	}
	if got := stopRunner.topologyWrites(); got != 1 {
		t.Fatalf("typed unmanaged stop writes = %d, want 1", got)
	}
	if got, want := cmd.focusSession, "tmp-previous"; got != want {
		t.Fatalf("focus session = %q, want %q", got, want)
	}
}

func TestSwitchCommandPickerSidebarKillRefreshFocusesActiveSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{
			"tmp-app":      true,
			"tmp-previous": true,
			"tmp-worker":   true,
		},
		recentSessions: []string{"tmp-app", "tmp-previous", "tmp-worker"},
	}
	executor.killHook = func(sessionName string) {
		executor.exists[sessionName] = false
	}
	stopRow := strings.Join([]string{"$7", "tmp-app", "", "", ""}, tmuxRowSep) + "\n"
	stopRunner := &unmanagedStopRunner{appMarker: "1", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux", listRows: []string{stopRow, stopRow, stopRow}}

	var focusValue string
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app", "/tmp/previous", "/tmp/worker"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			var killAction intpicker.Action
			for _, action := range options.Actions {
				if action.Key == switchKillExpectKey {
					killAction = action
					break
				}
			}
			if killAction.Mutate == nil {
				t.Fatalf("kill action = %#v, want mutable native action", killAction)
			}
			update, err := killAction.Mutate(intpicker.ActionContext{
				Key:           switchKillExpectKey,
				Value:         "/tmp/app",
				SelectedIndex: 0,
			})
			if err != nil {
				t.Fatalf("kill mutate error = %v", err)
			}
			focusValue = update.FocusValue
			return intpicker.Result{Closed: true}, nil
		}),
		sessions:   executor,
		tmuxRunner: stopRunner,
		lookupEnv:  func(string) string { return "" },
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/app":
				return "tmp-app", nil
			case "/tmp/previous":
				return "tmp-previous", nil
			case "/tmp/worker":
				return "tmp-worker", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// After the kill tmux switches to tmp-previous; the refresh must point
	// FocusValue at that session's row path so the sidebar cursor follows.
	if got, want := cmd.focusSession, "tmp-previous"; got != want {
		t.Fatalf("focus session = %q, want %q", got, want)
	}
	if got, want := focusValue, "/tmp/previous"; got != want {
		t.Fatalf("refresh FocusValue = %q, want active session path %q", got, want)
	}
}

func TestSwitchCommandPickerSidebarKillMutateBlocksWithoutPreviousLiveSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists:         map[string]bool{"tmp-app": true},
		recentSessions: []string{"tmp-app"},
	}
	var mutateCalls int
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		nativePicker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			var killAction intpicker.Action
			for _, action := range options.Actions {
				if action.Key == switchKillExpectKey {
					killAction = action
					break
				}
			}
			if killAction.Mutate == nil {
				t.Fatalf("kill action = %#v, want mutable native action", killAction)
			}
			update, err := killAction.Mutate(intpicker.ActionContext{
				Key:           switchKillExpectKey,
				Value:         "/tmp/app",
				SelectedIndex: 0,
			})
			if err != nil {
				t.Fatalf("kill mutate error = %v", err)
			}
			mutateCalls++
			if values := pickerItemValues(update.Items); !slices.Contains(values, "/tmp/app") {
				t.Fatalf("refreshed values = %q, want blocked kill row preserved", values)
			}
			return intpicker.Result{Closed: true}, nil
		}),
		sessions:   executor,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if mutateCalls != 1 {
		t.Fatalf("mutate calls = %d, want 1", mutateCalls)
	}
	if got := executor.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
	if got := executor.openSessionName; got != "" {
		t.Fatalf("open session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandPickerAltPLoopsUntilSelection(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions []intpickercompat.Options
	store := newStubPinStore()
	executor := &capturingSwitchSessionExecutor{}

	observe := func(o intpickercompat.Options) { gotRunnerOptions = append(gotRunnerOptions, o) }
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: observe, reply: intpickercompat.Result{Key: switchPinExpectKey, Value: "/tmp/app"}},
		{observe: observe, reply: intpickercompat.Result{Value: "/tmp/app"}},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return store, nil },
		runner:       runner,
		nativePicker: native,
		sessions:     executor,
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
	}
	wireFakeProjectSessionPlan(cmd)

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(gotRunnerOptions), 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	for i, options := range gotRunnerOptions {
		if got, want := options.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
	}
	// alt-p on an unregistered directory pins a candidate, not a Project: the
	// keystroke states a preference and never registers anything.
	if got, want := store.set.CandidatePaths(), []string{"/tmp/app"}; !equalStrings(got, want) {
		t.Fatalf("candidate pins = %q, want %q", got, want)
	}
	if len(store.set.ProjectUIDs()) != 0 {
		t.Fatalf("alt-p minted managed identity: %#v", store.set.ProjectUIDs())
	}
	if got, want := executor.ensureSessionName, "tmp-app"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "tmp-app"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty for alt-p loop", got)
	}
}

func TestSwitchCommandSettingsSubcommandRunsSettingsMenu(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := newCandidateStubPinStore("/tmp/app")
	tick := func(intpickercompat.Options) { runnerCalls++ }
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: tick, reply: intpickercompat.Result{Value: "clear"}},
		{observe: tick},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return store, nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if len(store.set.Pins) != 0 {
		t.Fatalf("pins after clear = %#v, want empty", store.set.Pins)
	}
}

// TestSwitchSettingsSelectionStaysInsideOuterNativeThemeOwner pins the Settings
// audit: unlike Runtime, this menu is rendered directly by switchCommand and
// therefore does not enter a second command-level theme boundary.
func TestSwitchSettingsSelectionStaysInsideOuterNativeThemeOwner(t *testing.T) {
	t.Parallel()

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Value: switchSettingsSentinel}},
		{observe: func(options intpickercompat.Options) {
			if got, want := options.UI, "settings"; got != want {
				t.Fatalf("nested picker UI = %q, want %q", got, want)
			}
		}},
	})
	cmd := &switchCommand{
		discover:     func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "tmp-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/tmp", nil },
		lookupEnv:    func(string) string { return "" },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSwitchCommandSettingsMenuAddCurrentPin(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := newStubPinStore()
	tick := func(intpickercompat.Options) { runnerCalls++ }
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: tick, reply: intpickercompat.Result{Value: "add:/home/tester/source/repos/new-app"}},
		{observe: tick},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/new-app"}, nil
		},
		pinStore:     func() (switchPinStore, error) { return store, nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "new-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.set.CandidatePaths(), []string{"/home/tester/source/repos/new-app"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: candidate /home/tester/source/repos/new-app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandSettingsMenuInteractiveAddPin(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := newCandidateStubPinStore("/home/tester/source/repos/app")
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) {
			runnerCalls++
			if got, want := o.UI, "settings"; got != want {
				t.Fatalf("settings picker UI = %q, want %q", got, want)
			}
		}, reply: intpickercompat.Result{Value: "add-interactive"}},
		{observe: func(o intpickercompat.Options) {
			runnerCalls++
			if got, want := o.UI, "pin"; got != want {
				t.Fatalf("add-pin picker UI = %q, want %q", got, want)
			}
			wantEntries := []intpickercompat.Entry{
				{Label: "~rp/new-app", Value: "/home/tester/source/repos/new-app"},
				{Label: "~rp/lib", Value: "/home/tester/source/repos/lib"},
			}
			if !equalEntries(o.Entries, wantEntries) {
				t.Fatalf("add-pin entries = %#v, want %#v", o.Entries, wantEntries)
			}
		}, reply: intpickercompat.Result{Value: "/home/tester/source/repos/lib"}},
		{observe: func(intpickercompat.Options) { runnerCalls++ }},
	})
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{
				"/home/tester/source/repos/app",
				"/home/tester/source/repos/new-app",
				"/home/tester/source/repos/lib",
			}, nil
		},
		pinStore:     func() (switchPinStore, error) { return store, nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "new-app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return "/home/tester", nil },
		workingDir:   func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 3; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.set.CandidatePaths(), []string{"/home/tester/source/repos/app", "/home/tester/source/repos/lib"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: candidate /home/tester/source/repos/lib\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandToggleTagSnapsExplicitPathToCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested/deeper")

	store := &capturingSwitchTagStore{tagged: false}
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return newStubPinStore(), nil },
		tagStore: func() (switchTagStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("home"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle-tag", fixture.path("managed/work-a/nested/deeper")}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.calls, []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "untagged: "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandToggleTagRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "too many args", args: []string{"toggle-tag", "/tmp/a", "/tmp/b"}, want: "switch toggle-tag accepts at most 1 [path] argument"},
		{name: "blank arg", args: []string{"toggle-tag", "   "}, want: "switch toggle-tag requires a non-empty [path] argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := cmd.Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestSwitchCommandTogglePinSnapsExplicitPathToCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested/deeper")

	store := newStubPinStore()
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("home"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle-pin", fixture.path("managed/work-a/nested/deeper")}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.set.CandidatePaths(), []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("candidate pins = %q, want %q", got, want)
	}
	// The message states the kind, because "pinned" alone could no longer say
	// which of the two collections just changed.
	if got, want := stdout.String(), "pinned: candidate "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

type switchRunnerFunc func(options intpickercompat.Options) (intpickercompat.Result, error)

func (f switchRunnerFunc) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	return f(options)
}

type capturingSwitchRunner struct {
	last   intpickercompat.Options
	result intpickercompat.Result
	err    error
}

func (r *capturingSwitchRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.last = options
	return r.result, r.err
}

type stubSwitchIdentityResolver struct {
	name string
	err  error
}

func (r stubSwitchIdentityResolver) SessionIdentityForPath(string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.name, nil
}

type switchIdentityResolverFunc func(path string) (string, error)

func (f switchIdentityResolverFunc) SessionIdentityForPath(path string) (string, error) {
	return f(path)
}

type pickerRunnerFunc func(options intpicker.Options) (intpicker.Result, error)

func (f pickerRunnerFunc) Run(options intpicker.Options) (intpicker.Result, error) {
	return f(options)
}

type capturingSwitchSessionExecutor struct {
	ensureSessionName     string
	ensureCWD             string
	openSessionName       string
	killSessionName       string
	authorizeCalled       bool
	authorizeResult       bool
	authorizeSet          bool
	layoutAuthorizeCalled bool
	layoutAuthorizeResult bool
	layoutAuthorizeSet    bool
	exists                map[string]bool
	recentSessions        []string
	calls                 []string
	killHook              func(string)
	ensureErr             error
	openErr               error
	killErr               error
	authorizeErr          error
	layoutAuthorizeErr    error
	existsErr             error
	recentErr             error
}

type bulkSwitchSessionExecutor struct {
	existing    map[string]bool
	bulkErr     error
	bulkCalls   int
	existsCalls []string
}

type sidebarOpenTrustPopupExecutor struct {
	lookupEnv   func(string) string
	executable  func() (string, error)
	popupRunner tmuxRunner
	calls       []string
}

func (e *bulkSwitchSessionExecutor) EnsureSession(context.Context, string, string) error {
	return nil
}

func (e *bulkSwitchSessionExecutor) OpenSession(context.Context, string) error {
	return nil
}

func (e *bulkSwitchSessionExecutor) ExistingSessions(context.Context) (map[string]bool, error) {
	e.bulkCalls++
	if e.bulkErr != nil {
		return nil, e.bulkErr
	}
	existing := make(map[string]bool, len(e.existing))
	maps.Copy(existing, e.existing)
	return existing, nil
}

func (e *bulkSwitchSessionExecutor) SessionExists(_ context.Context, sessionName string) (bool, error) {
	e.existsCalls = append(e.existsCalls, sessionName)
	return e.existing[sessionName], nil
}

func (e *sidebarOpenTrustPopupExecutor) EnsureSession(_ context.Context, sessionName, _ string) error {
	e.calls = append(e.calls, "ensure:"+sessionName)
	return nil
}

func (e *sidebarOpenTrustPopupExecutor) OpenSession(_ context.Context, sessionName string) error {
	e.calls = append(e.calls, "open:"+sessionName)
	return nil
}

func (e *sidebarOpenTrustPopupExecutor) SessionExists(context.Context, string) (bool, error) {
	return false, nil
}

func (e *sidebarOpenTrustPopupExecutor) AuthorizeProjectHooks(_ context.Context, cwd string) (bool, error) {
	e.calls = append(e.calls, "authorize:"+cwd)
	prompt := tmuxProjectHookPrompt(e.lookupEnv, e.executable, e.popupRunner)
	decision := prompt(hooks.ProjectHookPromptRequest{
		RepoPath:     cwd,
		RelativePath: ".projmux/config.toml",
		SHA256:       "abc123",
	})
	return decision != hooks.ProjectHookDeny, nil
}

func (e *sidebarOpenTrustPopupExecutor) AuthorizeProjectLayout(_ context.Context, cwd string, artifact corelayout.Artifact) (bool, error) {
	e.calls = append(e.calls, "authorize-layout:"+artifact.RelativePath)
	prompt := tmuxProjectHookPrompt(e.lookupEnv, e.executable, e.popupRunner)
	decision := prompt(hooks.ProjectHookPromptRequest{
		RepoPath:     cwd,
		RelativePath: artifact.RelativePath,
		ArtifactKind: "project layout",
		SHA256:       "abc123",
		Preview:      strings.Join(artifact.ExecutableCommands(), "\n"),
	})
	return decision != hooks.ProjectHookDeny, nil
}

func (e *capturingSwitchSessionExecutor) EnsureSession(_ context.Context, sessionName, cwd string) error {
	e.ensureSessionName = sessionName
	e.ensureCWD = cwd
	e.calls = append(e.calls, "ensure:"+sessionName)
	return e.ensureErr
}

func (e *capturingSwitchSessionExecutor) OpenSession(_ context.Context, sessionName string) error {
	e.openSessionName = sessionName
	e.calls = append(e.calls, "open:"+sessionName)
	return e.openErr
}

func (e *capturingSwitchSessionExecutor) KillSession(_ context.Context, sessionName string) error {
	e.killSessionName = sessionName
	e.calls = append(e.calls, "kill:"+sessionName)
	if e.killErr != nil {
		return e.killErr
	}
	if e.killHook != nil {
		e.killHook(sessionName)
	}
	return nil
}

func (e *capturingSwitchSessionExecutor) AuthorizeProjectHooks(_ context.Context, cwd string) (bool, error) {
	e.authorizeCalled = true
	e.calls = append(e.calls, "authorize:"+cwd)
	if e.authorizeErr != nil {
		return false, e.authorizeErr
	}
	if e.authorizeSet {
		return e.authorizeResult, nil
	}
	return true, nil
}

func (e *capturingSwitchSessionExecutor) AuthorizeProjectLayout(_ context.Context, _ string, artifact corelayout.Artifact) (bool, error) {
	e.layoutAuthorizeCalled = true
	e.calls = append(e.calls, "authorize-layout:"+artifact.RelativePath)
	if e.layoutAuthorizeErr != nil {
		return false, e.layoutAuthorizeErr
	}
	if e.layoutAuthorizeSet {
		return e.layoutAuthorizeResult, nil
	}
	return true, nil
}

func (e *capturingSwitchSessionExecutor) SessionExists(_ context.Context, sessionName string) (bool, error) {
	if e.existsErr != nil {
		return false, e.existsErr
	}
	if e.exists == nil {
		return false, nil
	}
	return e.exists[sessionName], nil
}

func (e *capturingSwitchSessionExecutor) RecentSessions(context.Context) ([]string, error) {
	if e.recentErr != nil {
		return nil, e.recentErr
	}
	return e.recentSessions, nil
}

func sortedStrings(values []string) []string {
	values = append([]string(nil), values...)
	slices.Sort(values)
	return values
}

func pickerItemValues(items []intpicker.Item) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Value)
	}
	return values
}

func pickerItemSearchTexts(items []intpicker.Item) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.SearchText)
	}
	return values
}

func expectedCheapSidebarEntry(name, displayPath, value string, pinned bool) intpickercompat.Entry {
	return expectedSidebarEntry(name, displayPath, value, "new", pinned)
}

func expectedSidebarEntry(name, displayPath, value, modeLabel string, pinned bool) intpickercompat.Entry {
	row := intrender.BuildSwitchRows([]intrender.SwitchCandidate{{
		Path:        value,
		DisplayPath: displayPath,
		DisplayName: name,
		SessionName: name,
		ModeLabel:   modeLabel,
		UI:          "sidebar",
		Pinned:      pinned,
	}})[0]
	return intpickercompat.Entry{
		Label:     row.Item.EffectiveLabel(),
		Value:     value,
		SearchKey: name,
	}
}

func saveSwitchProjectStartupSnapshot(t *testing.T, store sessionstate.Store, sessionName string) {
	t.Helper()
	if err := store.Save(sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    sessionName,
		Source:     sessionstate.SourceAutosave,
		DefaultCWD: "/tmp/workspace",
		SavedAt:    time.Date(2026, time.May, 13, 12, 0, 0, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "shell",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{{
				Index:  0,
				CWD:    "/tmp/workspace",
				Recipe: sessionstate.ShellRecipe(),
			}},
		}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}

func requireSwitchEntryLabel(t *testing.T, entries []intpickercompat.Entry, want string) {
	t.Helper()
	for _, entry := range entries {
		if strings.Contains(entry.Label, want) {
			return
		}
	}
	t.Fatalf("entries = %#v, want label containing %q", entries, want)
}

func requireSwitchEntryValueOrder(t *testing.T, entries []intpickercompat.Entry, want []string) {
	t.Helper()
	if len(entries) != len(want) {
		t.Fatalf("entries = %#v, want %d entries", entries, len(want))
	}
	for i, value := range want {
		if entries[i].Value != value {
			t.Fatalf("entry %d value = %q, want %q; entries = %#v", i, entries[i].Value, value, entries)
		}
	}
}

// stubSwitchPinStore is an in-memory typed pin file.
//
// It counts writes, because "a repeated pin action writes nothing" is a contract
// rather than an optimization and a fake that silently absorbed the write would
// hide it.
type stubSwitchPinStore struct {
	set    pins.Set
	writes int
	err    error
}

// newStubPinStore seeds a typed store with managed pins by uid.
func newStubPinStore(uids ...string) *stubSwitchPinStore {
	set := pins.Set{Format: pins.FormatTyped}
	for _, uid := range uids {
		set = set.With(pins.Pin{Kind: pins.KindProject, Value: uid})
	}
	return &stubSwitchPinStore{set: set}
}

// newCandidateStubPinStore seeds a typed store with candidate pins by path.
func newCandidateStubPinStore(paths ...string) *stubSwitchPinStore {
	set := pins.Set{Format: pins.FormatTyped}
	for _, path := range paths {
		set = set.With(pins.Pin{Kind: pins.KindCandidate, Value: path})
	}
	return &stubSwitchPinStore{set: set}
}

// newLegacyStubPinStore seeds the pre-v2 shape: bare paths with no statement about
// which of them a Project claims.
func newLegacyStubPinStore(paths ...string) *stubSwitchPinStore {
	set := pins.Set{Format: pins.FormatLegacy}
	for _, path := range paths {
		set.Pins = append(set.Pins, pins.Pin{Kind: pins.KindCandidate, Value: path})
	}
	if len(set.Pins) == 0 {
		set.Format = pins.FormatAbsent
	}
	return &stubSwitchPinStore{set: set}
}

func (s *stubSwitchPinStore) Path() string { return "/fixture/pins" }

func (s *stubSwitchPinStore) Load() (pins.Set, error) {
	if s.err != nil {
		return pins.Set{}, s.err
	}
	return s.set, nil
}

func (s *stubSwitchPinStore) Save(set pins.Set) error {
	if s.err != nil {
		return s.err
	}
	s.writes++
	s.set = set
	return nil
}

type capturingSwitchTagStore struct {
	calls  []string
	tagged bool
	list   []string
	err    error
}

func (s *capturingSwitchTagStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
}

func (s *capturingSwitchTagStore) Toggle(name string) (bool, error) {
	s.calls = append(s.calls, name)
	if s.err != nil {
		return false, s.err
	}
	return s.tagged, nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalEntries(got, want []intpickercompat.Entry) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Label != want[i].Label || got[i].Value != want[i].Value {
			return false
		}
		if want[i].SearchKey != "" && got[i].SearchKey != want[i].SearchKey {
			return false
		}
	}
	return true
}

func enableSidebarStartupPickerForTest(t *testing.T, home string) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}
	if err := config.SaveSessionStateToggleFile(paths.SidebarStartupPickerFile(), config.SessionStateToggleOn); err != nil {
		t.Fatalf("SaveSessionStateToggleFile(sidebar startup) error = %v", err)
	}
}

type switchFixtureFS struct {
	root string
	t    *testing.T
}

func newSwitchFixture(t *testing.T) switchFixtureFS {
	t.Helper()

	return switchFixtureFS{
		root: t.TempDir(),
		t:    t,
	}
}

func (f switchFixtureFS) mkdir(rel string) {
	f.t.Helper()

	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatalf("MkdirAll(%q): %v", rel, err)
	}
}

func (f switchFixtureFS) path(rel string) string {
	f.t.Helper()

	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func TestBestSwitchCandidateMatchCrossesSymlinkForms(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	realProj := filepath.Join(tmp, "real", "proj")
	if err := os.MkdirAll(filepath.Join(realProj, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	linkRoot := filepath.Join(tmp, "link")
	if err := os.Symlink(filepath.Join(tmp, "real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}
	symlinkProj := filepath.Join(linkRoot, "proj")

	tests := []struct {
		name       string
		path       string
		candidates []string
		want       string
	}{
		{
			name:       "session cwd via real path matches symlink candidate row",
			path:       realProj,
			candidates: []string{symlinkProj},
			want:       symlinkProj,
		},
		{
			name:       "session cwd via symlink matches real-path candidate row",
			path:       symlinkProj,
			candidates: []string{realProj},
			want:       realProj,
		},
		{
			name:       "nested real cwd prefix-matches symlink candidate, returns display form",
			path:       filepath.Join(realProj, "sub"),
			candidates: []string{symlinkProj},
			want:       symlinkProj,
		},
		{
			name:       "no match returns empty",
			path:       filepath.Join(tmp, "elsewhere"),
			candidates: []string{symlinkProj},
			want:       "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bestSwitchCandidateMatch(tc.path, tc.candidates); got != tc.want {
				t.Fatalf("bestSwitchCandidateMatch(%q, %q) = %q, want %q", tc.path, tc.candidates, got, tc.want)
			}
		})
	}
}

func TestBestSwitchCandidateMatchBrokenLinkFallsBackToLexical(t *testing.T) {
	t.Parallel()

	// Neither path exists on disk, so EvalSymlinks fails and CanonicalPath must
	// fall back to lexical Clean without panicking, still matching by prefix.
	base := filepath.Join(string(filepath.Separator), "no", "such", "root")
	candidate := filepath.Join(base, "proj")
	nested := filepath.Join(candidate, "deep")

	if got := bestSwitchCandidateMatch(nested, []string{candidate}); got != candidate {
		t.Fatalf("bestSwitchCandidateMatch(%q, %q) = %q, want %q", nested, candidate, got, candidate)
	}
	if got := bestSwitchCandidateMatch(candidate, []string{candidate}); got != candidate {
		t.Fatalf("bestSwitchCandidateMatch(exact) = %q, want %q", got, candidate)
	}
	if got := bestSwitchCandidateMatch("", []string{candidate}); got != "" {
		t.Fatalf("bestSwitchCandidateMatch(blank) = %q, want empty", got)
	}
}

func TestSwitchCurrentSelectionMatchesSnappedSymlinkCandidate(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "real", "proj"), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	linkRoot := filepath.Join(tmp, "link")
	if err := os.Symlink(filepath.Join(tmp, "real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	// tmux reports the resolved real path as the active session cwd.
	realCurrent := filepath.Join(tmp, "real", "proj")

	got, err := candidates.Discover(candidates.Inputs{
		ManagedRoots: []string{linkRoot},
		CurrentPath:  realCurrent,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	symlinkProj := filepath.Join(linkRoot, "proj")
	if !slices.Contains(got, symlinkProj) {
		t.Fatalf("Discover() = %q, want symlink-form candidate %q", got, symlinkProj)
	}
	if slices.Contains(got, realCurrent) {
		t.Fatalf("Discover() = %q leaked real-path candidate %q", got, realCurrent)
	}

	// The current-session highlight resolves the real-path cwd onto the single
	// symlink-form candidate row produced by discovery.
	if match := bestSwitchCandidateMatch(realCurrent, got); match != symlinkProj {
		t.Fatalf("bestSwitchCandidateMatch(%q, %q) = %q, want %q", realCurrent, got, match, symlinkProj)
	}
}

func TestSwitchSidebarCommitRecordsOnceAfterMarkerRemoval(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	client := "/dev/pts/7"
	marker := popupMarkerPath(sanitizePopupKey(client), "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\norigin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"work-session": true}}
	cmd := &switchCommand{
		sessions:   executor,
		tmuxRunner: runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(key string) string {
			if key == inttmux.SwitchTargetClientEnv {
				return client
			}
			return ""
		},
	}

	plan := switchPlan{UI: switchUISidebar, Selection: "/repo/work", SessionName: "work-session"}
	if err := cmd.openProjectTargetPathFromSidebar(context.Background(), plan); err != nil {
		t.Fatalf("openProjectTargetPathFromSidebar() error = %v", err)
	}

	if got, want := executor.calls, []string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("session calls = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want removed before commit record", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"run-shell", "-b", "'/tmp/projmux' 'window' 'record'"}},
		{name: "tmux", args: []string{"-L", "projmux", "switch-client", "-c", client, "-t", "=work-session"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want commit then final handoff %#v", runner.calls, want)
	}
}

func TestSwitchSidebarCommitSkipsExplicitRecordWithoutMarker(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	runner := &recordingTmuxRunner{}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"work-session": true}}
	cmd := &switchCommand{
		sessions:   executor,
		tmuxRunner: runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(string) string { return "" },
	}

	plan := switchPlan{UI: switchUISidebar, Selection: "/repo/work", SessionName: "work-session"}
	if err := cmd.openProjectTargetPathFromSidebar(context.Background(), plan); err != nil {
		t.Fatalf("openProjectTargetPathFromSidebar() error = %v", err)
	}

	if got, want := executor.calls, []string{"open:work-session"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("session calls = %q, want %q", got, want)
	}
	// Outside a sidebar popup the open switch fires the session-changed hook
	// itself, so an explicit record would double-record.
	if len(runner.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want none without a sidebar popup marker", runner.calls)
	}
}

func TestSwitchSidebarCancelRestoresOriginSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	client := "/dev/pts/7"
	marker := popupMarkerPath(sanitizePopupKey(client), "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\norigin-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"origin-session": true}}
	cmd := &switchCommand{
		sessions: executor,
		lookupEnv: func(key string) string {
			if key == inttmux.SwitchTargetClientEnv {
				return client
			}
			return ""
		},
	}

	reopen, err := cmd.execute(context.Background(), switchPlan{UI: switchUISidebar, OriginSession: "origin-session"}, io.Discard)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if reopen {
		t.Fatal("execute() reopen = true, want false on cancel")
	}
	if got, want := executor.calls, []string{"open:origin-session"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("session calls = %q, want origin restore %q", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want removed before origin restore", err)
	}
}

func TestSwitchSidebarCancelSkipsRestoreWhenOriginMissing(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	client := "/dev/pts/7"
	marker := popupMarkerPath(sanitizePopupKey(client), "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\norigin-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{}}
	cmd := &switchCommand{
		sessions: executor,
		lookupEnv: func(key string) string {
			if key == inttmux.SwitchTargetClientEnv {
				return client
			}
			return ""
		},
	}

	if _, err := cmd.execute(context.Background(), switchPlan{UI: switchUISidebar, OriginSession: "origin-session"}, io.Discard); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("session calls = %q, want none when the origin session is gone", executor.calls)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want marker removed even without restore", err)
	}
}

func TestSwitchSidebarCancelWithoutMarkerDoesNothing(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"origin-session": true}}
	cmd := &switchCommand{
		sessions:  executor,
		lookupEnv: func(string) string { return "" },
	}

	if _, err := cmd.execute(context.Background(), switchPlan{UI: switchUISidebar, OriginSession: "origin-session"}, io.Discard); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("session calls = %q, want none outside a sidebar popup", executor.calls)
	}
}

func TestSwitchPopupCancelDoesNotRestoreOrigin(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	client := "/dev/pts/7"
	marker := popupMarkerPath(sanitizePopupKey(client), "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\norigin-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"origin-session": true}}
	cmd := &switchCommand{
		sessions: executor,
		lookupEnv: func(key string) string {
			if key == inttmux.SwitchTargetClientEnv {
				return client
			}
			return ""
		},
	}

	if _, err := cmd.execute(context.Background(), switchPlan{UI: switchUIPopup, OriginSession: "origin-session"}, io.Discard); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("session calls = %q, want none for the popup sessionizer", executor.calls)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker stat error = %v, want sidebar marker untouched by popup cancel", err)
	}
}
