package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAttentionToggleMarksPlainPane(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %1 #{pane_title}": []byte("server\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"toggle", "%1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%1", "#{pane_title}"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_attention_state", "reply"}},
		{name: "tmux", args: []string{"select-pane", "-T", "✳ server", "-t", "%1"}},
		{name: "tmux", args: []string{"display-message", "-t", "%1", "attention: needs reply"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionToggleClearsMarkedPane(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %2 #{pane_title}": []byte("✳ worker\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"toggle", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%2", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"select-pane", "-T", "worker", "-t", "%2"}},
		{name: "tmux", args: []string{"display-message", "-t", "%2", "attention: cleared"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionClearAcksAndStripsPrefix(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %3 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %3 #{@projmux_attention_focus_armed}": []byte("1\n"),
			"tmux display-message -p -t %3 #{pane_title}":                     []byte("✔ done\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%3", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%3", "#{@projmux_attention_focus_armed}"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%3", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%3", "@projmux_attention_focus_armed"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}},
		{name: "tmux", args: []string{"select-pane", "-T", "done", "-t", "%3"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionClearKeepsBusyPane(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %4 #{@projmux_attention_state}": []byte("busy\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%4", "#{@projmux_attention_state}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionClearKeepsUnarmedReplyPane(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %5 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %5 #{@projmux_attention_focus_armed}": []byte("\n"),
			"tmux list-clients -F #{client_active_pane}":                      []byte("%99\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%5", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%5", "#{@projmux_attention_focus_armed}"}},
		{name: "tmux", args: []string{"list-clients", "-F", "#{client_active_pane}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionClearKeepsInactiveUnarmedReplyPaneWithTitleBadge(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %8 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %8 #{@projmux_attention_focus_armed}": []byte("\n"),
			"tmux list-clients -F #{client_active_pane}":                      []byte("%99\n"),
			"tmux display-message -p -t %8 #{pane_title}":                     []byte("✔ review\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%8"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%8", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%8", "#{@projmux_attention_focus_armed}"}},
		{name: "tmux", args: []string{"list-clients", "-F", "#{client_active_pane}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionClearAcksActiveUnarmedReplyPane(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %7 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %7 #{@projmux_attention_focus_armed}": []byte("\n"),
			"tmux list-clients -F #{client_active_pane}":                      []byte("%7\n"),
			"tmux display-message -p -t %7 #{pane_title}":                     []byte("repo\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{@projmux_attention_focus_armed}"}},
		{name: "tmux", args: []string{"list-clients", "-F", "#{client_active_pane}"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%7", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%7", "@projmux_attention_focus_armed"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{pane_title}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

// Regression: pane_active is true even when every client has switched to a
// different window/session. The auto-ack must NOT fire in that case.
func TestAttentionClearKeepsReplyPaneWhenClientOnDifferentWindow(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %7 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %7 #{@projmux_attention_focus_armed}": []byte("\n"),
			"tmux list-clients -F #{client_active_pane}":                      []byte("%42\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{@projmux_attention_focus_armed}"}},
		{name: "tmux", args: []string{"list-clients", "-F", "#{client_active_pane}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionArmMarksReplyForNextFocusClear(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %6 #{@projmux_attention_state}": []byte("reply\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"arm", "%6"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%6", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%6", "@projmux_attention_focus_armed", "1"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAttentionWindowPrefersBusyBadge(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux list-panes -t @1 -F #{pane_title}" + attentionListSeparator + "#{@projmux_attention_state}": []byte("plain\treply" + attentionListSeparator + "reply\n⠋ working" + attentionListSeparator + "\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"window", "@1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "#[fg="+tmuxStateProgressFg+"]●"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAttentionWindowShowsReplyBadge(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux list-panes -t @2 -F #{pane_title}" + attentionListSeparator + "#{@projmux_attention_state}": []byte("plain" + attentionListSeparator + "\n✳ ready" + attentionListSeparator + "\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"window", "@2"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "#[fg="+tmuxAccentAttentionStrongBg+"]●"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAttentionWindowFallsBackToBlank(t *testing.T) {
	t.Parallel()

	cmd := &attentionCommand{runner: &recordingAttentionRunner{err: errors.New("tmux missing")}}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"window", "@3"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), " "; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAttentionListShowsLiveAttentionRows(t *testing.T) {
	t.Parallel()

	output := strings.Join([]string{
		attentionListTestRow("dev", "@1", "%1", "1", "server", "", "idle", "codex", "plain work", "/tmp/tmux"),
		attentionListTestRow("dev", "@1", "%2", "0", "✳ review", "", "", "", "", "/tmp/tmux"),
		attentionListTestRow("ops", "@3", "%7", "0", "worker", "busy", "thinking", "claude", "deploy", ""),
	}, "\n") + "\n"
	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux list-panes -a -F " + attentionListFormat: []byte(output),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantCalls := []attentionCall{
		{name: "tmux", args: []string{"list-panes", "-a", "-F", attentionListFormat}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
	want := "SESSION\tWINDOW\tPANE\tACTIVE\tATTENTION\tAI\tAGENT\tTOPIC\tTITLE\n" +
		"dev\t@1\t%2\tno\t-\t-\t-\t-\t✳ review\n" +
		"ops\t@3\t%7\tno\tbusy\tthinking\tclaude\tdeploy\tworker\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAttentionListJSONAllIncludesPanesWithoutAttention(t *testing.T) {
	t.Parallel()

	output := strings.Join([]string{
		attentionListTestRow("dev", "@1", "%1", "1", "server", "", "idle", "codex", "plain work", "/tmp/tmux"),
		attentionListTestRow("dev", "@1", "%2", "0", "✳ review", "reply", "waiting", "codex", "needs input", "/tmp/tmux"),
	}, "\n") + "\n"
	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux list-panes -a -F " + attentionListFormat: []byte(output),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--json", "--all"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var rows []attentionPaneRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("json decode error = %v; stdout = %q", err, stdout.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2 entries", rows)
	}
	if rows[0].Pane != "%1" || !rows[0].Active || rows[0].AttentionState != "" {
		t.Fatalf("rows[0] = %#v, want active pane without attention", rows[0])
	}
	if rows[1].Pane != "%2" || rows[1].AttentionState != "reply" || rows[1].AIState != "waiting" {
		t.Fatalf("rows[1] = %#v, want reply pane", rows[1])
	}
}

func TestAttentionListHelpDescribesLiveStateBoundary(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	if err := (&attentionCommand{}).Run([]string{"list", "--help"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Live tmux pane attention state") {
		t.Fatalf("stderr = %q, want live-state boundary", stderr.String())
	}
	if !strings.Contains(stderr.String(), "does not read or mutate the notify queue") {
		t.Fatalf("stderr = %q, want notify queue boundary", stderr.String())
	}
}

func TestAttentionRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing command", args: nil, want: "attention requires a subcommand"},
		{name: "unknown command", args: []string{"nope"}, want: "unknown attention subcommand: nope"},
		{name: "too many args", args: []string{"clear", "%1", "%2"}, want: "attention clear accepts at most 1 target argument"},
		{name: "list positional arg", args: []string{"list", "%1"}, want: "attention list does not accept positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&attentionCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func attentionListTestRow(fields ...string) string {
	return strings.Join(fields, attentionListSeparator)
}

type attentionCall struct {
	name string
	args []string
}

type recordingAttentionRunner struct {
	calls   []attentionCall
	outputs map[string][]byte
	err     error
}

func (r *recordingAttentionRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, attentionCall{name: name, args: append([]string(nil), args...)})
	if r.err != nil {
		return nil, r.err
	}
	return r.outputs[name+" "+strings.Join(args, " ")], nil
}
