package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSplitOperands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{name: "empty", args: nil, want: []string{}},
		{name: "plain operands", args: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "lone dash is operand", args: []string{"-"}, want: []string{"-"}},
		{name: "double dash dropped", args: []string{"--"}, want: []string{}},
		{name: "after double dash everything is operand", args: []string{"a", "--", "-x", "--", "--zz"}, want: []string{"a", "-x", "--", "--zz"}},
		{name: "long unknown flag", args: []string{"--zz"}, wantErr: "cmd: unknown flag --zz"},
		{name: "short unknown flag", args: []string{"-x"}, wantErr: "cmd: unknown flag -x"},
		{name: "flag after operands", args: []string{"a", "b", "c", "--zz=1"}, wantErr: "cmd: unknown flag --zz=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := splitOperands("cmd", tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || !IsUsageError(err) {
					t.Fatalf("err = %v (usage=%v), want usage error %q", err, IsUsageError(err), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("operands = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAttentionUnknownFlagsAndArityAreUsageErrorsBeforeTmux(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "toggle flag", args: []string{"toggle", "--zz"}, want: "attention toggle: unknown flag --zz"},
		{name: "clear flag", args: []string{"clear", "--zz"}, want: "attention clear: unknown flag --zz"},
		{name: "arm flag", args: []string{"arm", "--zz"}, want: "attention arm: unknown flag --zz"},
		{name: "window flag", args: []string{"window", "--zz"}, want: "attention window: unknown flag --zz"},
		{name: "toggle target then flag", args: []string{"toggle", "%1", "--zz"}, want: "attention toggle: unknown flag --zz"},
		{name: "toggle flag beats arity", args: []string{"toggle", "a", "b", "--zz"}, want: "attention toggle: unknown flag --zz"},
		{name: "window id then flag", args: []string{"window", "@1", "--zz"}, want: "attention window: unknown flag --zz"},
		{name: "window style then flag", args: []string{"window", "@1", "dot", "--zz"}, want: "attention window: unknown flag --zz"},
		{name: "bare", args: []string{}, want: "attention requires a subcommand"},
		{name: "unknown subcommand", args: []string{"zz"}, want: "unknown attention subcommand: zz"},
		{name: "list positional", args: []string{"list", "zz"}, want: "attention list does not accept positional arguments"},
		{name: "toggle arity", args: []string{"toggle", "a", "b"}, want: "attention toggle accepts at most 1 target argument"},
		{name: "window arity", args: []string{"window", "a", "b", "c"}, want: "attention window accepts at most 2 arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := &recordingAttentionRunner{}
			var envReads, homeReads atomic.Int32
			cmd := &attentionCommand{
				runner:    runner,
				lookupEnv: func(string) string { envReads.Add(1); return "" },
				homeDir:   func() (string, error) { homeReads.Add(1); return "", errors.New("unused") },
			}
			var stdout, stderr bytes.Buffer
			err := cmd.Run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsUsageError(err) {
				t.Fatalf("err = %v, want usage error (exit 2)", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if len(runner.calls) != 0 {
				t.Fatalf("tmux calls = %#v, want none", runner.calls)
			}
			if envReads.Load() != 0 || homeReads.Load() != 0 {
				t.Fatalf("env reads = %d, home reads = %d, want none", envReads.Load(), homeReads.Load())
			}
		})
	}
}

func TestAttentionDoubleDashEndsOptions(t *testing.T) {
	t.Parallel()

	t.Run("arm -- %5 targets %5", func(t *testing.T) {
		t.Parallel()
		runner := &recordingAttentionRunner{
			outputs: map[string][]byte{
				"tmux display-message -p -t %5 #{@projmux_attention_state}": []byte("reply\n"),
			},
		}
		cmd := &attentionCommand{runner: runner}
		if err := cmd.Run([]string{"arm", "--", "%5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		want := []attentionCall{
			{name: "tmux", args: []string{"display-message", "-p", "-t", "%5", "#{@projmux_attention_state}"}},
			{name: "tmux", args: []string{"set-option", "-p", "-t", "%5", "@projmux_attention_focus_armed", "1"}},
		}
		if !reflect.DeepEqual(runner.calls, want) {
			t.Fatalf("calls = %#v, want %#v", runner.calls, want)
		}
	})

	t.Run("toggle -- is the omitted target path", func(t *testing.T) {
		t.Parallel()
		runner := &recordingAttentionRunner{}
		cmd := &attentionCommand{runner: runner, lookupEnv: func(string) string { return "" }}
		err := cmd.Run([]string{"toggle", "--"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "requires an explicit pane or valid inherited TMUX_PANE") {
			t.Fatalf("err = %v, want inherited-pane requirement", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("tmux calls = %#v, want none", runner.calls)
		}
	})

	t.Run("window -- @3 dot renders like window @3 dot", func(t *testing.T) {
		t.Parallel()
		var plain, dashed bytes.Buffer
		plainRunner := &recordingAttentionRunner{}
		dashedRunner := &recordingAttentionRunner{}
		if err := (&attentionCommand{runner: plainRunner}).Run([]string{"window", "@3", "dot"}, &plain, &bytes.Buffer{}); err != nil {
			t.Fatalf("plain Run() error = %v", err)
		}
		if err := (&attentionCommand{runner: dashedRunner}).Run([]string{"window", "--", "@3", "dot"}, &dashed, &bytes.Buffer{}); err != nil {
			t.Fatalf("dashed Run() error = %v", err)
		}
		if plain.String() != dashed.String() || !reflect.DeepEqual(plainRunner.calls, dashedRunner.calls) {
			t.Fatalf("dashed = %q %#v, plain = %q %#v", dashed.String(), dashedRunner.calls, plain.String(), plainRunner.calls)
		}
		if len(plainRunner.calls) == 0 {
			t.Fatal("window @3 dot made no tmux calls")
		}
	})
}

func TestHookTrustUnknownFlagsAreUsageErrorsBeforeTrustStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "trust flag", args: []string{"trust", "--zz"}, want: "hook trust: unknown flag --zz"},
		{name: "untrust flag", args: []string{"untrust", "--zz"}, want: "hook untrust: unknown flag --zz"},
		{name: "trust dir then flag", args: []string{"trust", ".", "--zz"}, want: "hook trust: unknown flag --zz"},
		{name: "trust arity", args: []string{"trust", "a", "b"}, want: "trust/untrust takes at most one <project> argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			cmd, _, trustPath := newHookTestCommand(t, home, "", "")
			var envReads, cwdReads atomic.Int32
			lookup := cmd.lookupEnv
			cmd.lookupEnv = func(name string) string { envReads.Add(1); return lookup(name) }
			cmd.getwd = func() (string, error) { cwdReads.Add(1); return home, nil }

			var stdout, stderr bytes.Buffer
			err := cmd.Run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsUsageError(err) {
				t.Fatalf("err = %v, want usage error (exit 2)", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if envReads.Load() != 0 || cwdReads.Load() != 0 {
				t.Fatalf("env reads = %d, cwd reads = %d, want none", envReads.Load(), cwdReads.Load())
			}
			if _, statErr := os.Stat(trustPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("trust store stat = %v, want not created", statErr)
			}
		})
	}
}

func TestHookTrustDoubleDashAllowsDashPath(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, _ := newHookTestCommand(t, home, "", "")
	want, err := filepath.Abs("-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"hook trust", "hook untrust"} {
		got, err := cmd.resolveTrustTarget(verb, []string{"--", "-x"}, func() {})
		if err != nil {
			t.Fatalf("%s -- -x: err = %v", verb, err)
		}
		if got != filepath.Clean(want) {
			t.Fatalf("%s -- -x = %q, want %q", verb, got, want)
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"untrust", "--", "-x"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("untrust -- -x: err = %v", err)
	}
	if got := stdout.String(); got != "no trust entry for "+filepath.Clean(want)+"\n" {
		t.Fatalf("untrust stdout = %q", got)
	}
}

type countingKillTagStore struct {
	items []string
	calls atomic.Int32
}

func (s *countingKillTagStore) List() ([]string, error) {
	s.calls.Add(1)
	return s.items, nil
}

func newCountingKillCommand(store *countingKillTagStore, exec *recordingTaggedKillExecutor, tmuxCalls *atomic.Int32) *killCommand {
	return &killCommand{
		current: currentSessionResolverFunc(func(context.Context) (string, error) {
			tmuxCalls.Add(1)
			return "work-a", nil
		}),
		recent: recentSessionsResolverFunc(func(context.Context) ([]string, error) {
			tmuxCalls.Add(1)
			return []string{"home"}, nil
		}),
		exec:     exec,
		homeDir:  func() (string, error) { return "/home/tester", nil },
		tagStore: store,
	}
}

func TestRuntimeStopUnknownFlagsAreUsageErrorsBeforeStoreAndTmux(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "flag", args: []string{"tagged", "--zz"}, want: "runtime stop: unknown flag --zz"},
		{name: "session then flag", args: []string{"tagged", "s", "--zz"}, want: "runtime stop: unknown flag --zz"},
		{name: "empty session", args: []string{"tagged", ""}, want: "kill tagged requires non-empty tagged sessions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &countingKillTagStore{items: []string{"tagged-a"}}
			exec := &recordingTaggedKillExecutor{}
			var tmuxCalls atomic.Int32
			cmd := newCountingKillCommand(store, exec, &tmuxCalls)

			var stderr bytes.Buffer
			err := cmd.Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsUsageError(err) {
				t.Fatalf("err = %v, want usage error (exit 2)", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if store.calls.Load() != 0 || tmuxCalls.Load() != 0 || exec.inputs.KillTargets != nil {
				t.Fatalf("store calls = %d, tmux calls = %d, exec targets = %#v, want none", store.calls.Load(), tmuxCalls.Load(), exec.inputs.KillTargets)
			}
		})
	}
}

func TestRuntimeStopUnknownFlagThroughRuntimeRoute(t *testing.T) {
	t.Parallel()

	store := &countingKillTagStore{}
	exec := &recordingTaggedKillExecutor{}
	var tmuxCalls atomic.Int32
	app := &App{kill: newCountingKillCommand(store, exec, &tmuxCalls)}

	err := app.Run([]string{"runtime", "stop", "--zz"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "--zz") {
		t.Fatalf("err = %v (usage=%v), want usage error naming --zz", err, IsUsageError(err))
	}
	if store.calls.Load() != 0 || tmuxCalls.Load() != 0 || exec.inputs.KillTargets != nil {
		t.Fatal("rejection touched the store, tmux, or the executor")
	}
}

func TestRuntimeStopDoubleDashAndLoneDashOperands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantTargets []string
		wantStore   int32
	}{
		{name: "dash-prefixed session after --", args: []string{"runtime", "stop", "--", "-x"}, wantTargets: []string{"-x"}},
		{name: "lone dash is a session", args: []string{"runtime", "stop", "-"}, wantTargets: []string{"-"}},
		{name: "bare -- uses the tag store", args: []string{"runtime", "stop", "--"}, wantTargets: []string{"tagged-a"}, wantStore: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &countingKillTagStore{items: []string{"tagged-a"}}
			exec := &recordingTaggedKillExecutor{}
			var tmuxCalls atomic.Int32
			app := &App{kill: newCountingKillCommand(store, exec, &tmuxCalls)}

			if err := app.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := exec.inputs.KillTargets; !reflect.DeepEqual(got, tt.wantTargets) {
				t.Fatalf("kill targets = %#v, want %#v", got, tt.wantTargets)
			}
			if got := store.calls.Load(); got != tt.wantStore {
				t.Fatalf("store calls = %d, want %d", got, tt.wantStore)
			}
		})
	}
}
