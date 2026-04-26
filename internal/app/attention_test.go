package app

import (
	"bytes"
	"context"
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
		},
	}
	cmd := &attentionCommand{runner: runner}

	if err := cmd.Run([]string{"clear", "%5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []attentionCall{
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%5", "#{@projmux_attention_state}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "%5", "#{@projmux_attention_focus_armed}"}},
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
			"tmux list-panes -t @1 -F #{pane_title}\t#{@projmux_attention_state}": []byte("plain\treply\n⠋ working\t\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"window", "@1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "#[fg=colour220]●"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAttentionWindowShowsReplyBadge(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux list-panes -t @2 -F #{pane_title}\t#{@projmux_attention_state}": []byte("plain\t\n✳ ready\t\n"),
		},
	}
	cmd := &attentionCommand{runner: runner}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"window", "@2"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "#[fg=colour82]●"; got != want {
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
