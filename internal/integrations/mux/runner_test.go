package mux

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingBackend struct {
	name string
	args []string
	out  []byte
	err  error
}

type sequenceBackend struct {
	outputs [][]byte
	calls   [][]string
}

func (b *sequenceBackend) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, append([]string(nil), args...))
	output := b.outputs[0]
	b.outputs = b.outputs[1:]
	return output, nil
}

func (b *recordingBackend) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	b.name = name
	b.args = append([]string(nil), args...)
	return append([]byte(nil), b.out...), b.err
}

func TestRunnerReadInvokesTmuxWithExactArgs(t *testing.T) {
	backend := &recordingBackend{out: []byte("  value \n")}
	runner := NewRunner(backend)

	out, err := runner.Read(context.Background(), "display-message", "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	if backend.name != "tmux" {
		t.Fatalf("backend name = %q, want tmux", backend.name)
	}
	wantArgs := []string{"display-message", "-p", "#{pane_id}"}
	if !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend args = %#v, want %#v", backend.args, wantArgs)
	}
	if string(out) != "  value \n" {
		t.Fatalf("Read output = %q, want raw output", string(out))
	}
}

func TestRunnerReadTrimmedTrimsOnlyReadTrimmedOutput(t *testing.T) {
	backend := &recordingBackend{out: []byte("  value \n")}
	runner := NewRunner(backend)

	raw, err := runner.Read(context.Background(), "display-message", "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if string(raw) != "  value \n" {
		t.Fatalf("Read output = %q, want raw output", string(raw))
	}

	trimmed, err := runner.ReadTrimmed(context.Background(), "display-message", "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("ReadTrimmed returned error: %v", err)
	}
	if trimmed != "value" {
		t.Fatalf("ReadTrimmed output = %q, want value", trimmed)
	}
}

func TestRunnerRunReturnsBackendError(t *testing.T) {
	wantErr := errors.New("boom")
	backend := &recordingBackend{err: wantErr}
	runner := NewRunner(backend)

	err := runner.Run(context.Background(), "set-option", "-g", "@projmux_app", "1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}

	wantArgs := []string{"set-option", "-g", "@projmux_app", "1"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerSetPaneOptionBuildsPaneScopedSetOption(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SetPaneOption(context.Background(), " %9 ", " @projmux_ai_state ", "waiting"); err != nil {
		t.Fatalf("SetPaneOption returned error: %v", err)
	}

	wantArgs := []string{"set-option", "-p", "-t", "%9", "@projmux_ai_state", "waiting"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerUnsetPaneOptionBuildsPaneScopedUnsetOption(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.UnsetPaneOption(context.Background(), "%9", "@projmux_attention_ack"); err != nil {
		t.Fatalf("UnsetPaneOption returned error: %v", err)
	}

	wantArgs := []string{"set-option", "-p", "-u", "-t", "%9", "@projmux_attention_ack"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerShowPaneOptionReadsDisplayMessageFormat(t *testing.T) {
	backend := &recordingBackend{out: []byte(" reply \n")}
	runner := NewRunner(backend)

	got, err := runner.ShowPaneOption(context.Background(), "%9", "@projmux_attention_state")
	if err != nil {
		t.Fatalf("ShowPaneOption returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "-t", "%9", "#{@projmux_attention_state}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "reply" {
		t.Fatalf("ShowPaneOption = %q, want reply", got)
	}
}

func TestRunnerDisplayMessageBuildsPaneScopedRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("  title  \n")}
	runner := NewRunner(backend)

	got, err := runner.DisplayMessage(context.Background(), DisplayMessageOptions{
		Target: " %9 ",
		Format: TmuxFormat("pane_title"),
	})
	if err != nil {
		t.Fatalf("DisplayMessage returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "-t", "%9", "#{pane_title}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if string(got) != "  title  \n" {
		t.Fatalf("DisplayMessage = %q, want raw output", string(got))
	}
}

func TestRunnerDisplayMessageAllowsTargetlessRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("%3\n")}
	runner := NewRunner(backend)

	got, err := runner.DisplayMessageTrimmed(context.Background(), DisplayMessageOptions{
		Format: TmuxFormat("pane_id"),
	})
	if err != nil {
		t.Fatalf("DisplayMessageTrimmed returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "#{pane_id}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "%3" {
		t.Fatalf("DisplayMessageTrimmed = %q, want %%3", got)
	}
}

func TestRunnerShowOptionUsesPluralCommand(t *testing.T) {
	backend := &recordingBackend{out: []byte("value\n")}
	runner := NewRunner(backend)

	got, err := runner.ShowOption(context.Background(), ShowOptionOptions{
		Global:    true,
		Quiet:     true,
		ValueOnly: true,
		Option:    " @projmux_projdir ",
	})
	if err != nil {
		t.Fatalf("ShowOption returned error: %v", err)
	}

	wantArgs := []string{"show-options", "-gqv", "@projmux_projdir"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "value" {
		t.Fatalf("ShowOption = %q, want value", got)
	}
}

func TestRunnerShowOptionSupportsTargetedSessionRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("fresh\n")}
	runner := NewRunner(backend)

	got, err := runner.ShowOption(context.Background(), ShowOptionOptions{
		Target:    " workspace ",
		Quiet:     true,
		ValueOnly: true,
		Option:    "@projmux_sessionstate_source",
	})
	if err != nil {
		t.Fatalf("ShowOption returned error: %v", err)
	}

	wantArgs := []string{"show-options", "-qv", "-t", "workspace", "@projmux_sessionstate_source"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "fresh" {
		t.Fatalf("ShowOption = %q, want fresh", got)
	}
}

func TestJoinFormatsKeepsCallerDelimiter(t *testing.T) {
	got := JoinFormats("|", TmuxFormat("pane_title"), PaneOptionFormat("@projmux_ai_state"))
	want := "#{pane_title}|#{@projmux_ai_state}"
	if got != want {
		t.Fatalf("JoinFormats = %q, want %q", got, want)
	}
}

func TestRunnerListPanesBuildsStructuredInventoryRead(t *testing.T) {
	backend := &recordingBackend{out: []byte(" dev \x1f %3 \x1f reply \nshort\n")}
	runner := NewRunner(backend)

	rows, err := runner.ListPanes(context.Background(), ListPanesOptions{
		All: true,
		Formats: []string{
			TmuxFormat("session_name"),
			TmuxFormat("pane_id"),
			PaneOptionFormat("@projmux_attention_state"),
		},
	})
	if err != nil {
		t.Fatalf("ListPanes returned error: %v", err)
	}

	wantArgs := []string{
		"list-panes",
		"-a",
		"-F",
		"#{session_name}" + FieldDelimiter + "#{pane_id}" + FieldDelimiter + "#{@projmux_attention_state}",
	}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	wantRows := [][]string{{"dev", "%3", "reply"}}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("ListPanes rows = %#v, want %#v", rows, wantRows)
	}
}

func TestRunnerListWindowsBuildsStructuredInventoryRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("1|editor\n")}
	runner := NewRunner(backend)

	rows, err := runner.ListWindows(context.Background(), ListWindowsOptions{
		Target:    " dev ",
		Delimiter: "|",
		Formats: []string{
			TmuxFormat("window_index"),
			TmuxFormat("window_name"),
		},
	})
	if err != nil {
		t.Fatalf("ListWindows returned error: %v", err)
	}

	wantArgs := []string{"list-windows", "-t", "dev", "-F", "#{window_index}|#{window_name}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	wantRows := [][]string{{"1", "editor"}}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("ListWindows rows = %#v, want %#v", rows, wantRows)
	}
}

func TestRunnerDisplayPaneFieldsBuildsDisplayMessageRead(t *testing.T) {
	backend := &recordingBackend{out: []byte(" dev \x1f @1 \x1f %3 \n")}
	runner := NewRunner(backend)

	row, err := runner.DisplayPaneFields(
		context.Background(),
		" %3 ",
		TmuxFormat("session_name"),
		TmuxFormat("window_id"),
		TmuxFormat("pane_id"),
	)
	if err != nil {
		t.Fatalf("DisplayPaneFields returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "-t", "%3", "#{session_name}" + FieldDelimiter + "#{window_id}" + FieldDelimiter + "#{pane_id}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	wantRow := []string{"dev", "@1", "%3"}
	if !reflect.DeepEqual(row, wantRow) {
		t.Fatalf("DisplayPaneFields row = %#v, want %#v", row, wantRow)
	}
}

func TestRunnerDisplayPopupBuildsExistingPopupArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	err := runner.DisplayPopup(context.Background(), "printf hello", PopupOptions{
		Target:        "%4",
		Cwd:           "/repo",
		Env:           map[string]string{"PROJMUX_TEST_ENV": "value"},
		NoBorder:      true,
		X:             "0",
		Y:             "0",
		Width:         "40",
		Height:        "20",
		CloseBehavior: PopupCloseOnExit,
	})
	if err != nil {
		t.Fatalf("DisplayPopup returned error: %v", err)
	}

	wantArgs := []string{
		"display-popup",
		"-t", "%4",
		"-E",
		"-B",
		"-d", "/repo",
		"-e", "PROJMUX_TEST_ENV=value",
		"-x", "0",
		"-y", "0",
		"-w", "40",
		"-h", "20",
		"printf hello",
	}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestBuildDisplayPopupArgsAddsBodyStyle(t *testing.T) {
	got, err := BuildDisplayPopupArgs("printf hello", PopupOptions{
		BodyStyle: " bg=colour235,fg=colour245 ",
	})
	if err != nil {
		t.Fatalf("BuildDisplayPopupArgs returned error: %v", err)
	}

	want := []string{
		"display-popup",
		"-E",
		"-w", "80%",
		"-h", "80%",
		"-s", "bg=colour235,fg=colour245",
		"printf hello",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildDisplayPopupArgs() = %#v, want %#v", got, want)
	}
}

func TestRunnerClosePopupBuildsScopedCloseArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.ClosePopup(context.Background(), ClosePopupOptions{
		Client: " /dev/pts/7 ",
		Target: " %4 ",
	}); err != nil {
		t.Fatalf("ClosePopup returned error: %v", err)
	}

	wantArgs := []string{"display-popup", "-c", "/dev/pts/7", "-t", "%4", "-C"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerCapturePaneBuildsExistingJoinedCaptureArgs(t *testing.T) {
	backend := &recordingBackend{out: []byte(" line one \n")}
	runner := NewRunner(backend)

	got, err := runner.CapturePane(context.Background(), CapturePaneOptions{
		Target:    "%8",
		StartLine: -80,
		JoinLines: true,
	})
	if err != nil {
		t.Fatalf("CapturePane returned error: %v", err)
	}

	wantArgs := []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%8"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != " line one " {
		t.Fatalf("CapturePane = %q, want line with only trailing newline trimmed", got)
	}
}

func TestRunnerCapturePaneBuildsExistingGenericCaptureArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if _, err := runner.CapturePane(context.Background(), CapturePaneOptions{
		Target:    "%8",
		StartLine: -80,
	}); err != nil {
		t.Fatalf("CapturePane returned error: %v", err)
	}

	wantArgs := []string{"capture-pane", "-p", "-t", "%8", "-S", "-80"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerSwitchClientBuildsSocketScopedArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SwitchClient(context.Background(), SwitchClientOptions{
		Socket: "/tmp/projmux.sock",
		Client: "/dev/pts/9",
		Target: "workspace",
	}); err != nil {
		t.Fatalf("SwitchClient returned error: %v", err)
	}

	wantArgs := []string{"-S", "/tmp/projmux.sock", "switch-client", "-c", "/dev/pts/9", "-t", "workspace"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerSelectPaneBuildsTargetAndTitleArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SelectPane(context.Background(), SelectPaneOptions{
		Socket:   "/tmp/projmux.sock",
		Target:   "%9",
		Title:    "",
		SetTitle: true,
	}); err != nil {
		t.Fatalf("SelectPane returned error: %v", err)
	}

	wantArgs := []string{"-S", "/tmp/projmux.sock", "select-pane", "-T", "", "-t", "%9"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerSelectWindowBuildsSocketScopedArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SelectWindow(context.Background(), SelectWindowOptions{
		Socket: "/tmp/projmux.sock",
		Target: "workspace:1",
	}); err != nil {
		t.Fatalf("SelectWindow returned error: %v", err)
	}

	wantArgs := []string{"-S", "/tmp/projmux.sock", "select-window", "-t", "workspace:1"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerNewEphemeralSessionBuildsDetachedSessionWithEnvAndPaneIDRead(t *testing.T) {
	backend := &recordingBackend{out: []byte(" %7 \n")}
	runner := NewRunner(backend)

	paneID, err := runner.NewEphemeralSession(context.Background(), EphemeralSessionOptions{
		Session: " workspace ",
		Cwd:     " /repo ",
		Env: map[string]string{
			"ZED": "last",
			"FOO": "bar",
		},
		ReturnPaneID: true,
	})
	if err != nil {
		t.Fatalf("NewEphemeralSession returned error: %v", err)
	}

	wantArgs := []string{"new-session", "-d", "-s", "workspace", "-c", "/repo", "-e", "FOO=bar", "-e", "ZED=last", "-P", "-F", "#{pane_id}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if paneID != "%7" {
		t.Fatalf("NewEphemeralSession paneID = %q, want %%7", paneID)
	}
}

func TestRunnerSetHookBuildsAppendAndUnsetArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SetHook(context.Background(), SetHookOptions{
		Global:  true,
		Append:  true,
		Hook:    " alert-bell ",
		Command: `run-shell -b 'projmux internal agent-hook ingest bell --pane "#{pane_id}"'`,
	}); err != nil {
		t.Fatalf("SetHook append returned error: %v", err)
	}

	wantArgs := []string{"set-hook", "-ag", "alert-bell", `run-shell -b 'projmux internal agent-hook ingest bell --pane "#{pane_id}"'`}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}

	if err := runner.SetHook(context.Background(), SetHookOptions{
		Global: true,
		Unset:  true,
		Hook:   "alert-bell[2]",
	}); err != nil {
		t.Fatalf("SetHook unset returned error: %v", err)
	}

	wantArgs = []string{"set-hook", "-gu", "alert-bell[2]"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerSetOptionAndShowOptionBuildGlobalArgs(t *testing.T) {
	backend := &recordingBackend{out: []byte(" on \n")}
	runner := NewRunner(backend)

	if err := runner.SetOption(context.Background(), SetOptionOptions{
		Global: true,
		Option: " allow-passthrough ",
		Value:  "on",
	}); err != nil {
		t.Fatalf("SetOption returned error: %v", err)
	}

	wantArgs := []string{"set-option", "-g", "allow-passthrough", "on"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}

	got, err := runner.ShowOption(context.Background(), ShowOptionOptions{
		Global:    true,
		Quiet:     true,
		ValueOnly: true,
		Option:    "@projmux_app",
	})
	if err != nil {
		t.Fatalf("ShowOption returned error: %v", err)
	}

	wantArgs = []string{"show-options", "-gqv", "@projmux_app"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "on" {
		t.Fatalf("ShowOption = %q, want on", got)
	}
}

func TestParseFormatRowsPinsMalformedAndTrimmingBehavior(t *testing.T) {
	output := []byte("  one \x1f two \r\nmissing\nthree\x1ffour\x1fextra\n five\\037six \n")

	rows := ParseFormatRows(output, FormatRowsOptions{
		Delimiter:  FieldDelimiter,
		FieldCount: 2,
	})
	want := [][]string{{"one", "two"}, {"five", "six"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("ParseFormatRows strict rows = %#v, want %#v", rows, want)
	}

	rows = ParseFormatRows(output, FormatRowsOptions{
		Delimiter:        FieldDelimiter,
		FieldCount:       2,
		AllowExtraFields: true,
	})
	want = [][]string{{"one", "two"}, {"three", "four"}, {"five", "six"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("ParseFormatRows extra rows = %#v, want %#v", rows, want)
	}
}
